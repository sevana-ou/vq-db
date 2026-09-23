package api

import (
	"database/sql"
	"encoding/base64"
	"strings"
	"time"

	"github.com/sevana-ou/vq-db/internal/filter"
)

// queryRows runs a query and returns each row as a Row (map[string]any), with
// []byte values normalized to string (SQLite TEXT).
func queryRows(db *sql.DB, query string, args ...any) ([]Row, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(Row, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[c] = v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const sipEventColumns = "id, event_type, call_id, event_timestamp, invite_timestamp," +
	" caller, callee, caller_ua, callee_ua, caller_codecs, callee_codecs," +
	" setup_code, bye_direction, duration, response_codes," +
	" reason, response_code, reason_phrase, direction, is_request"

// Int64Opt is an optional filter bound (nil = no bound).
type Int64Opt = *int64

func i64p(v int64) *int64 { return &v }

// sipEventWhere builds the WHERE clause and appends bound args.
func sipEventWhere(eventType, startMs, endMs Int64Opt, callID string) (string, []any) {
	var conds []string
	var args []any
	if eventType != nil {
		conds = append(conds, "event_type = ?")
		args = append(args, *eventType)
	}
	if startMs != nil {
		conds = append(conds, "event_timestamp >= ?")
		args = append(args, *startMs)
	}
	if endMs != nil {
		conds = append(conds, "event_timestamp <= ?")
		args = append(args, *endMs)
	}
	if callID != "" {
		conds = append(conds, "call_id = ?")
		args = append(args, callID)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " where " + strings.Join(conds, " and ") + " ", args
}

// GetAgentList returns the list of agents.
func GetAgentList(db *sql.DB) ([]map[string]any, error) {
	rows, err := queryRows(db, "select agent_id, agent_name from rtpmon_instances")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"id": getStr(r, "agent_id"), "name": getStr(r, "agent_name")})
	}
	return out, nil
}

// GetSipCallList returns per-Call-ID summaries.
func GetSipCallList(db *sql.DB, startMs, endMs Int64Opt, limit, offset int, callID string) ([]Row, error) {
	where, args := sipEventWhere(nil, startMs, endMs, callID)
	query := "select call_id," +
		" max(case when event_type in (1,4) then caller end) as caller," +
		" max(case when event_type in (1,4) then callee end) as callee," +
		" max(case when event_type=1 then event_timestamp end) as start_timestamp," +
		" max(case when event_type in (2,4) then event_timestamp end) as end_timestamp," +
		" max(case when event_type=1 then setup_code end) as setup_code," +
		" max(case when event_type=4 then response_code end) as response_code," +
		" max(case when event_type=4 then reason end) as reason," +
		" max(duration) as duration," +
		" sum(case when event_type=3 then 1 else 0 end) as reinvite_count," +
		" max(case when event_type=1 then 1 else 0 end) as established," +
		" max(case when event_type=4 then 1 else 0 end) as failed," +
		" max(event_timestamp) as last_ts" +
		" from rtpmon_sip_events" + where +
		" group by call_id order by last_ts desc limit ? offset ?"
	args = append(args, limit, offset)
	rows, err := queryRows(db, query, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		for _, k := range []string{"start_timestamp", "end_timestamp", "duration", "setup_code", "response_code", "reason", "reinvite_count"} {
			r[k] = getI64(r, k)
		}
		r["caller"] = strings.TrimSpace(getStr(r, "caller"))
		r["callee"] = strings.TrimSpace(getStr(r, "callee"))
	}
	return rows, nil
}

// GetSipCallCount returns the number of distinct calls matching the window.
func GetSipCallCount(db *sql.DB, startMs, endMs Int64Opt, callID string) (int, error) {
	where, args := sipEventWhere(nil, startMs, endMs, callID)
	var n int
	err := db.QueryRow("select count(distinct call_id) from rtpmon_sip_events"+where, args...).Scan(&n)
	return n, err
}

// GetSipCallEvents returns the full event timeline of one call.
func GetSipCallEvents(db *sql.DB, callID string) ([]Row, error) {
	query := "select " + sipEventColumns + " from rtpmon_sip_events where call_id = ? order by event_timestamp, id"
	rows, err := queryRows(db, query, callID)
	if err != nil {
		return nil, err
	}
	return normalizeSipRows(rows), nil
}

// GetSipEvents returns a paged flat SIP event log.
func GetSipEvents(db *sql.DB, eventType, startMs, endMs Int64Opt, limit, offset int) ([]Row, error) {
	where, args := sipEventWhere(eventType, startMs, endMs, "")
	query := "select " + sipEventColumns + " from rtpmon_sip_events" + where +
		" order by event_timestamp desc, id desc limit ? offset ?"
	args = append(args, limit, offset)
	rows, err := queryRows(db, query, args...)
	if err != nil {
		return nil, err
	}
	return normalizeSipRows(rows), nil
}

// GetSipEventCount returns the number of SIP events matching the window.
func GetSipEventCount(db *sql.DB, eventType, startMs, endMs Int64Opt) (int, error) {
	where, args := sipEventWhere(eventType, startMs, endMs, "")
	var n int
	err := db.QueryRow("select count(id) from rtpmon_sip_events"+where, args...).Scan(&n)
	return n, err
}

func normalizeSipRows(rows []Row) []Row {
	intCols := []string{"invite_timestamp", "setup_code", "bye_direction", "duration", "reason", "response_code", "direction"}
	strCols := []string{"call_id", "caller", "callee", "caller_ua", "callee_ua", "caller_codecs", "callee_codecs", "response_codes", "reason_phrase"}
	for _, r := range rows {
		for _, c := range intCols {
			r[c] = getI64(r, c)
		}
		for _, c := range strCols {
			r[c] = strings.TrimSpace(getStr(r, c))
		}
		r["is_request"] = getBool(r, "is_request")
	}
	return rows
}

// GetStreamAudioChunks returns base64-decoded WAV chunks for a stream.
func GetStreamAudioChunks(db *sql.DB, linkID string) ([][]byte, error) {
	query := "select rtpmon_audio.audio_wav from rtpmon_audio" +
		" inner join rtpmon_streams on rtpmon_streams.stream_id = rtpmon_audio.stream_id" +
		" where rtpmon_streams.link_id = ?" +
		" order by rtpmon_audio.start_timestamp, rtpmon_audio.id"
	rows, err := db.Query(query, linkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var encoded sql.NullString
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		if !encoded.Valid || encoded.String == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded.String)
		if err != nil {
			continue
		}
		out = append(out, decoded)
	}
	return out, rows.Err()
}

// FindWidgetStats computes the summary widget stats over the trailing window.
// widgetStatsQuery backs the dashboard summary (last hour and last day). Every
// column it reads is in idx_statistics_summary, so SQLite answers it from that
// index alone: duration_audio sits after the ~6 KB detector_report in the row,
// and reading it from the table walked that report's overflow pages for every
// matching stream (7.5 s on a cold cache for a day of test calls).
const widgetStatsQuery = "select count(stream_id) as stream_count," +
	" avg(sevana_rfactor) as rfactor," +
	" avg(case when sevana_mos > 0.0001 then sevana_mos else null end) as sevana_mos," +
	" avg(network_mos) as network_mos," +
	" avg(duration_audio) as duration," +
	" sum(case when sevana_mos >= ? then 1 else 0 end) as good_sevana_count," +
	" sum(case when network_mos >= ? then 1 else 0 end) as good_network_count," +
	" sum(case when sevana_mos > 0.0001 then 1 else 0 end) as with_sevana_mos" +
	" from rtpmon_statistics where end_timestamp > ?"

func FindWidgetStats(db *sql.DB, seconds int, goodMosThreshold float64, nowMs int64) (Row, error) {
	if nowMs == 0 {
		nowMs = time.Now().UnixMilli()
	}
	cutoff := nowMs - int64(seconds)*1000
	rows, err := queryRows(db, widgetStatsQuery, goodMosThreshold, goodMosThreshold, cutoff)
	if err != nil {
		return nil, err
	}
	r := rows[0]
	return Row{
		"stream_count":       getI64(r, "stream_count"),
		"rfactor":            getF64(r, "rfactor"),
		"sevana_mos":         getF64(r, "sevana_mos"),
		"network_mos":        getF64(r, "network_mos"),
		"duration":           getF64(r, "duration") / 1000.0,
		"good_sevana_count":  getI64(r, "good_sevana_count"),
		"good_network_count": getI64(r, "good_network_count"),
		"with_sevana_mos":    getI64(r, "with_sevana_mos"),
	}, nil
}

func reportCols(table string) string {
	c := table + ".start_timestamp, " + table + ".end_timestamp, " +
		table + ".rtp_packet_counter, " + table + ".illegal_packet_counter, " + table + ".lost_packet_counter, " +
		table + ".sevana_mos, " + table + ".sevana_rfactor, " + table + ".network_mos, " + table + ".jitter, " +
		table + ".codec, " + table + ".detector_report, " + table + ".rtt_delay, " + table + ".duration_audio, " +
		table + ".dtx_sid, " + table + ".dtx_count, " + table + ".dtx_total, " +
		"rtpmon_streams.sip_source, rtpmon_streams.sip_destination, rtpmon_streams.sip_callid, " +
		"rtpmon_streams.src_ip, rtpmon_streams.src_port, rtpmon_streams.dst_ip, " +
		"rtpmon_streams.dst_port, rtpmon_streams.ssrc, rtpmon_streams.link_id"
	return c
}

func streamJoin(table string) string {
	return " from " + table + " inner join rtpmon_streams on rtpmon_streams.stream_id = " + table + ".stream_id"
}

// GetStreamFinal returns the stream's final statistics row, or nil.
func GetStreamFinal(db *sql.DB, linkID string) (Row, error) {
	query := "select " + reportCols("rtpmon_statistics") + streamJoin("rtpmon_statistics") +
		" where rtpmon_statistics.link_id = ? order by rtpmon_statistics.end_timestamp desc limit 1"
	rows, err := queryRows(db, query, linkID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// GetStreamHistory returns all interval rows of a stream, time-ordered.
func GetStreamHistory(db *sql.DB, linkID string) ([]Row, error) {
	query := "select " + reportCols("rtpmon_intervals") + streamJoin("rtpmon_intervals") +
		" where rtpmon_intervals.link_id = ? order by rtpmon_intervals.start_timestamp"
	return queryRows(db, query, linkID)
}

// GetReport returns one interval row identified by link_id + end_timestamp.
func GetReport(db *sql.DB, linkID string, finishTimestamp int64) (Row, error) {
	query := "select " + reportCols("rtpmon_intervals") + streamJoin("rtpmon_intervals") +
		" where rtpmon_intervals.link_id = ? and rtpmon_intervals.end_timestamp = ?" +
		" order by rtpmon_intervals.start_timestamp limit 1"
	rows, err := queryRows(db, query, linkID, finishTimestamp)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

const finishedBase = " from rtpmon_statistics" +
	" inner join rtpmon_streams on rtpmon_streams.stream_id = rtpmon_statistics.stream_id" +
	" inner join rtpmon_instances on rtpmon_streams.agent_id = rtpmon_instances.id"

// finishedWhere builds the WHERE clause and args for the finished-stream query.
// Returns an error if the filter expression is invalid.
func finishedWhere(flt filter.SearchFilter) (string, []any, error) {
	var conds []string
	var args []any
	if flt.DateInterval != nil {
		conds = append(conds, "rtpmon_statistics.start_timestamp >= ?")
		conds = append(conds, "rtpmon_statistics.end_timestamp < ?")
		args = append(args, flt.DateInterval.StartSeconds*1000, flt.DateInterval.EndSeconds*1000)
	}
	if flt.SIPCallID != "" {
		conds = append(conds, "rtpmon_streams.sip_callid = ?")
		args = append(args, flt.SIPCallID)
	}
	if flt.Expression != "" {
		frag, err := filter.BuildWhere(flt.Expression, "qmark")
		if err != nil {
			return "", nil, err
		}
		conds = append(conds, frag.Text)
		args = append(args, frag.Params...)
	}
	if len(conds) == 0 {
		return "", args, nil
	}
	return " where " + strings.Join(conds, " and "), args, nil
}

// GetFinishedStreams returns the finished (DB) stream rows for /stats.
func GetFinishedStreams(db *sql.DB, flt filter.SearchFilter) ([]Row, error) {
	cols := reportCols("rtpmon_statistics") + ", rtpmon_instances.agent_id as instance_id, rtpmon_instances.agent_name as instance_name"
	where, args, err := finishedWhere(flt)
	if err != nil {
		return nil, err
	}
	order := filter.BuildOrderBy(flt.SortField, flt.Descending)
	offset := flt.PageOffset
	if offset < 0 {
		offset = 0
	}
	query := "select " + cols + finishedBase + where + " order by " + order + " limit ? offset ?"
	args = append(args, flt.MaxCount, offset)
	return queryRows(db, query, args...)
}

// GetFinishedStreamCount returns the number of finished streams matching flt.
func GetFinishedStreamCount(db *sql.DB, flt filter.SearchFilter) (int, error) {
	where, args, err := finishedWhere(flt)
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRow("select count(*)"+finishedBase+where, args...).Scan(&n)
	return n, err
}
