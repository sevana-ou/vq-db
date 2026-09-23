package db

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/sevana-ou/vq-db/internal/model"
)

// Writer is the single-writer DB layer: it turns domain events into row
// inserts/updates. Port of vq_db/db/writer.py.
//
// Single-writer contract: like the Python reference (and the C++ before it),
// Writer is NOT internally locked and must be driven by a single owner — the
// ingest goroutine. It keeps the MediaStreamId->db-id map, an endpoint->db-id
// map (for nil-uuid audio matching) and an agent-id cache in memory.
type Writer struct {
	conn             *sql.DB
	defaultAgentID   string
	defaultAgentName string
	streamMap        map[model.MediaStreamId]int64
	endpointMap      map[model.EndpointKey]int64
	agentCache       map[string]int64
	now              func() int64 // wall-clock ms; injectable for tests
	// Bounded-retention options (opt-in). recordsLimit caps how many recent calls
	// are kept (0 = unlimited); storeSipFilter (lowercased substrings) keeps only
	// SIP-matching calls (empty = keep all). See SetRecordsLimit / SetStoreSipFilter.
	recordsLimit   int
	storeSipFilter []string
}

// NewWriter constructs a Writer over an open connection.
func NewWriter(conn *sql.DB, defaultAgentID, defaultAgentName string) *Writer {
	return &Writer{
		conn:             conn,
		defaultAgentID:   defaultAgentID,
		defaultAgentName: defaultAgentName,
		streamMap:        map[model.MediaStreamId]int64{},
		endpointMap:      map[model.EndpointKey]int64{},
		agentCache:       map[string]int64{},
		now:              func() int64 { return time.Now().UnixMilli() },
	}
}

// Bootstrap creates tables and applies additive migrations.
func (w *Writer) Bootstrap() error { return Bootstrap(w.conn) }

// SetRecordsLimit caps stored calls to the N most-recent (0 = unlimited). When
// set, each completed call triggers eviction of the oldest calls beyond N.
func (w *Writer) SetRecordsLimit(n int) { w.recordsLimit = n }

// SetStoreSipFilter keeps only calls whose SIP source/destination matches one of
// the given substrings (case-insensitive). Empty disables the filter. Patterns
// are normalized to lowercase and empties dropped.
func (w *Writer) SetStoreSipFilter(patterns []string) {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	w.storeSipFilter = out
}

func (w *Writer) sipFilterActive() bool { return len(w.storeSipFilter) > 0 }

func reportHasSip(r model.StreamReport) bool { return r.SipPeerA != "" || r.SipPeerB != "" }

// sipMatches reports whether the report's SIP peers match the store filter.
func (w *Writer) sipMatches(r model.StreamReport) bool {
	a := strings.ToLower(r.SipPeerA)
	b := strings.ToLower(r.SipPeerB)
	for _, p := range w.storeSipFilter {
		if strings.Contains(a, p) || strings.Contains(b, p) {
			return true
		}
	}
	return false
}

// FindOrAddAgent returns the rtpmon_instances row id for an agent, inserting it
// if absent. Cached in memory.
func (w *Writer) FindOrAddAgent(agentID, agentName string) (int64, error) {
	if id, ok := w.agentCache[agentID]; ok {
		return id, nil
	}
	var id int64
	err := w.conn.QueryRow("SELECT id FROM rtpmon_instances WHERE agent_id = ?", agentID).Scan(&id)
	if err == sql.ErrNoRows {
		res, ierr := w.exec("INSERT INTO rtpmon_instances (agent_id, agent_name) VALUES (?, ?)", agentID, agentName)
		if ierr != nil {
			return 0, ierr
		}
		id, ierr = res.LastInsertId()
		if ierr != nil {
			return 0, ierr
		}
	} else if err != nil {
		return 0, err
	}
	w.agentCache[agentID] = id
	return id, nil
}

// OpenStream inserts a rtpmon_streams row once (using the default agent identity
// and the current wall-clock as created_ts) and returns its db stream_id.
func (w *Writer) OpenStream(id model.MediaStreamId) (int64, error) {
	return w.OpenStreamAt(id, w.now())
}

// OpenStreamAt is OpenStream with an explicit created_ts (wall-clock ms).
func (w *Writer) OpenStreamAt(id model.MediaStreamId, createdTS int64) (int64, error) {
	if existing, ok := w.streamMap[id]; ok {
		return existing, nil
	}
	agentRow, err := w.FindOrAddAgent(w.defaultAgentID, w.defaultAgentName)
	if err != nil {
		return 0, err
	}
	res, err := w.exec(
		`INSERT INTO rtpmon_streams (link_id, src_ip, src_port, dst_ip, dst_port, ssrc, agent_id, created_ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id.LinkID, id.SrcIP, id.SrcPort, id.DstIP, id.DstPort, id.SSRC, agentRow, createdTS,
	)
	if err != nil {
		return 0, err
	}
	dbID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	w.streamMap[id] = dbID
	w.endpointMap[id.EndpointKey()] = dbID
	return dbID, nil
}

// AddInterval writes an interval report and updates SIP correlation. When a SIP
// store filter is active and this report already carries a non-matching SIP
// correlation, the interval is skipped (any earlier rows are purged at the final
// report).
func (w *Writer) AddInterval(report model.StreamReport) error {
	if w.sipFilterActive() && reportHasSip(report) && !w.sipMatches(report) {
		return nil
	}
	if err := w.addReport(report, "rtpmon_intervals"); err != nil {
		return err
	}
	return w.maybeUpdateSip(report)
}

// AddFinal writes a final report and updates SIP correlation. With a SIP store
// filter active, a completed call that has no matching SIP correlation is purged
// entirely instead of being stored. With a records limit set, the oldest calls
// beyond the limit are evicted after a matching call is stored.
func (w *Writer) AddFinal(report model.StreamReport) error {
	if w.sipFilterActive() && !w.sipMatches(report) {
		// Call completed but does not match (or has no SIP under an active
		// filter): drop it and anything stored for it during the call.
		return w.purgeStream(report.StreamID)
	}
	if err := w.addReport(report, "rtpmon_statistics"); err != nil {
		return err
	}
	if err := w.maybeUpdateSip(report); err != nil {
		return err
	}
	if w.recordsLimit > 0 {
		return w.TrimToLastNCalls(w.recordsLimit)
	}
	return nil
}

// AddAudio stores a base64-encoded WAV chunk, matched by stream id then by
// endpoint (nil-uuid interval audio); dropped silently if the stream row is
// not created yet.
func (w *Writer) AddAudio(audio model.StreamAudio) error {
	if len(audio.Wav) == 0 {
		return nil
	}
	dbID, ok := w.streamMap[audio.StreamID]
	if !ok {
		dbID, ok = w.endpointMap[audio.StreamID.EndpointKey()]
	}
	if !ok {
		return nil // stream row not created yet -> drop silently (matches C++)
	}
	encoded := base64.StdEncoding.EncodeToString(audio.Wav)
	_, err := w.exec(
		`INSERT INTO rtpmon_audio (stream_id, start_timestamp, end_timestamp, audio_wav) VALUES (?, ?, ?, ?)`,
		dbID, audio.StartMs, audio.FinishMs, encoded,
	)
	return err
}

// ----- SIP events ----- //

func (w *Writer) AddSipCallStart(e model.SipCallStart) error {
	return w.insertSip(map[string]any{
		"event_type":       1,
		"call_id":          e.CallID,
		"event_timestamp":  e.Timestamp,
		"invite_timestamp": e.InviteTimestamp,
		"setup_code":       e.SetupCode,
		"caller":           e.Caller.Aor,
		"callee":           e.Callee.Aor,
		"caller_ua":        e.Caller.UserAgent,
		"callee_ua":        e.Callee.UserAgent,
		"caller_codecs":    e.Caller.Codecs,
		"callee_codecs":    e.Callee.Codecs,
	})
}

func (w *Writer) AddSipCallEnd(e model.SipCallEnd) error {
	codes := make([]string, len(e.ResponseCodes))
	for i, c := range e.ResponseCodes {
		codes[i] = fmt.Sprintf("%d", c)
	}
	return w.insertSip(map[string]any{
		"event_type":      2,
		"call_id":         e.CallID,
		"event_timestamp": e.Timestamp,
		"duration":        e.Duration,
		"bye_direction":   e.ByeDirection,
		"response_codes":  strings.Join(codes, ","),
	})
}

func (w *Writer) AddSipReinvite(e model.SipReinvite) error {
	isReq := 0
	if e.IsRequest {
		isReq = 1
	}
	return w.insertSip(map[string]any{
		"event_type":      3,
		"call_id":         e.CallID,
		"event_timestamp": e.Timestamp,
		"direction":       e.Direction,
		"is_request":      isReq,
		"caller":          e.UpdatedPeer.Aor,
		"caller_ua":       e.UpdatedPeer.UserAgent,
		"caller_codecs":   e.UpdatedPeer.Codecs,
	})
}

func (w *Writer) AddSipCallFailed(e model.SipCallFailed) error {
	return w.insertSip(map[string]any{
		"event_type":       4,
		"call_id":          e.CallID,
		"event_timestamp":  e.Timestamp,
		"invite_timestamp": e.InviteTimestamp,
		"reason":           e.Reason,
		"response_code":    e.ResponseCode,
		"reason_phrase":    e.ReasonPhrase,
		"caller":           e.Caller.Aor,
		"callee":           e.Callee.Aor,
		"caller_ua":        e.Caller.UserAgent,
		"callee_ua":        e.Callee.UserAgent,
		"caller_codecs":    e.Caller.Codecs,
		"callee_codecs":    e.Callee.Codecs,
	})
}

// ----- maintenance ----- //

// SetProperty upserts a rtpmon_property key/value (check-then-write).
func (w *Writer) SetProperty(name, value string) error {
	var existing string
	err := w.conn.QueryRow("SELECT name FROM rtpmon_property WHERE name = ?", name).Scan(&existing)
	if err == sql.ErrNoRows {
		_, ierr := w.exec("INSERT INTO rtpmon_property (name, value) VALUES (?, ?)", name, value)
		return ierr
	}
	if err != nil {
		return err
	}
	_, err = w.exec("UPDATE rtpmon_property SET value = ? WHERE name = ?", value, name)
	return err
}

// GetProperty returns a rtpmon_property value, or ("", false) if absent.
func (w *Writer) GetProperty(name string) (string, bool, error) {
	var value sql.NullString
	err := w.conn.QueryRow("SELECT value FROM rtpmon_property WHERE name = ?", name).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value.String, true, nil
}

// GetStreamCount returns the number of finished streams (rtpmon_statistics rows).
func (w *Writer) GetStreamCount() (int, error) {
	var n int
	err := w.conn.QueryRow("SELECT COUNT(*) FROM rtpmon_statistics").Scan(&n)
	return n, err
}

// CountRows returns the row count of a table (test/diagnostic helper). The table
// name must be a trusted constant — it is interpolated, not bound.
func (w *Writer) CountRows(table string) (int, error) {
	var n int
	err := w.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	return n, err
}

// DB returns the underlying connection (for read queries in other packages).
func (w *Writer) DB() *sql.DB { return w.conn }

// ----- internals ----- //

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func (w *Writer) addReport(report model.StreamReport, table string) error {
	dbID, ok := w.streamMap[report.StreamID]
	if !ok {
		var err error
		dbID, err = w.OpenStream(report.StreamID)
		if err != nil {
			return err
		}
	}
	flags := 0
	if report.FullReport {
		flags = 1
	}
	_, err := w.exec(
		`INSERT INTO `+table+` (
			stream_id, start_timestamp, end_timestamp, rtp_packet_counter,
			illegal_packet_counter, lost_packet_counter, sevana_mos, sevana_rfactor,
			network_mos, jitter, codec, detector_report, rtt_delay, flags, tags,
			amr_nb_switch_counter, amr_wb_switch_counter, link_id, duration_audio,
			user_report, dtx_sid, dtx_count, dtx_total
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dbID, report.StartMs, report.EndMs, report.RTPPacketCounter,
		report.IllegalPacketCounter, report.LostPacketCounter, nullFloat(report.SevanaMOS), report.SevanaRfactor,
		nullFloat(report.NetworkMOS), report.Jitter, report.Codec, report.DetectorReport, report.RTTDelay, flags, nil,
		report.AmrNbSwitchCounter, report.AmrWbSwitchCounter, report.StreamID.LinkID, report.DurationAudio,
		report.UserReport, report.DtxSid, report.DtxCount, report.DtxTotal,
	)
	return err
}

// purgeStream removes a stream (parent + children) entirely and forgets it from
// the in-memory maps, so a later re-detect reopens it fresh.
func (w *Writer) purgeStream(id model.MediaStreamId) error {
	dbID, ok := w.streamMap[id]
	if !ok {
		return nil
	}
	tx, err := w.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteStreamsByIDs(tx, []int64{dbID}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(w.streamMap, id)
	delete(w.endpointMap, id.EndpointKey())
	return nil
}

// callKeyExpr groups finished streams into "calls": distinct sip_callid, or a
// per-stream key when a stream has no call-id.
const callKeyExpr = "CASE WHEN rtpmon_streams.sip_callid IS NULL OR rtpmon_streams.sip_callid = '' " +
	"THEN 'S:' || rtpmon_streams.stream_id ELSE 'C:' || rtpmon_streams.sip_callid END"

// TrimToLastNCalls keeps only the N most-recently-active calls (a call = distinct
// sip_callid, or a single stream when it has no call-id), evicting the streams of
// older calls. Runs on the single-writer goroutine; the finished set stays near
// N, so the grouping scan is cheap.
func (w *Writer) TrimToLastNCalls(n int) error {
	if n <= 0 {
		return nil
	}
	keyRows, err := w.conn.Query(
		"SELECT " + callKeyExpr + " AS gkey, MAX(rtpmon_statistics.end_timestamp) AS last_ts " +
			"FROM rtpmon_statistics " +
			"JOIN rtpmon_streams ON rtpmon_streams.stream_id = rtpmon_statistics.stream_id " +
			"GROUP BY gkey ORDER BY last_ts DESC, gkey DESC")
	if err != nil {
		return err
	}
	var keys []string
	for keyRows.Next() {
		var gkey string
		var lastTS int64
		if err := keyRows.Scan(&gkey, &lastTS); err != nil {
			keyRows.Close()
			return err
		}
		keys = append(keys, gkey)
	}
	if err := keyRows.Err(); err != nil {
		keyRows.Close()
		return err
	}
	keyRows.Close()
	if len(keys) <= n {
		return nil
	}
	evict := keys[n:]

	// Resolve the evicted call keys to their stream ids (chunked IN).
	var ids []int64
	const chunkSize = 500
	for start := 0; start < len(evict); start += chunkSize {
		end := start + chunkSize
		if end > len(evict) {
			end = len(evict)
		}
		chunk := evict[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := w.conn.Query(
			"SELECT stream_id FROM rtpmon_streams WHERE ("+callKeyExpr+") IN ("+placeholders+")", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	tx, err := w.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteStreamsByIDs(tx, ids); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	w.forgetDBIDs(ids)
	return nil
}

// forgetDBIDs drops evicted streams from the in-memory maps so they stay bounded
// alongside the trimmed database.
func (w *Writer) forgetDBIDs(ids []int64) {
	if len(ids) == 0 {
		return
	}
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	for k, v := range w.streamMap {
		if set[v] {
			delete(w.streamMap, k)
			delete(w.endpointMap, k.EndpointKey())
		}
	}
}

func (w *Writer) maybeUpdateSip(report model.StreamReport) error {
	if report.SipCallID == "" {
		return nil
	}
	dbID, ok := w.streamMap[report.StreamID]
	if !ok {
		return nil
	}
	_, err := w.exec(
		`UPDATE rtpmon_streams SET sip_callid = ?, sip_source = ?, sip_destination = ? WHERE stream_id = ?`,
		report.SipCallID, report.SipPeerA, report.SipPeerB, dbID,
	)
	return err
}

// sipColumns is the fixed column order for sip-event inserts.
var sipColumns = []string{
	"event_type", "call_id", "event_timestamp", "invite_timestamp", "setup_code",
	"caller", "callee", "caller_ua", "callee_ua", "caller_codecs", "callee_codecs",
	"bye_direction", "duration", "response_codes", "reason", "response_code",
	"reason_phrase", "direction", "is_request",
}

func (w *Writer) insertSip(values map[string]any) error {
	var cols []string
	var placeholders []string
	var args []any
	for _, c := range sipColumns {
		if v, ok := values[c]; ok {
			cols = append(cols, c)
			placeholders = append(placeholders, "?")
			args = append(args, v)
		}
	}
	stmt := fmt.Sprintf("INSERT INTO rtpmon_sip_events (%s) VALUES (%s)",
		strings.Join(cols, ", "), strings.Join(placeholders, ", "))
	_, err := w.exec(stmt, args...)
	return err
}
