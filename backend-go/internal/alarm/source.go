package alarm

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"syscall"

	"github.com/sevana-ou/vq-db/internal/api"
	"github.com/sevana-ou/vq-db/internal/filter"
	"github.com/sevana-ou/vq-db/internal/pvqa"
)

// Snapshot holds every stream KPI over one window, for one rule's filter. A
// rule alarms on its own counter; the command gets the whole record, as the
// C++ alarm manager passed it ($r_factor, $sevana_mos, ...).
type Snapshot struct {
	Streams       int // finished streams in the window
	SevanaStreams int // of them, analysed by PVQA (Sevana MOS > 0)
	RFactor       float64
	SevanaMOS     float64
	NetworkMOS    float64
	PacketLoss    float64 // percent of expected packets
	Jitter        float64 // ms
	Duration      float64 // seconds
	FreeDisk      float64 // free share of the database's filesystem, 0..1; -1 unknown

	// Shares (percent) of the window's streams / calls, for the counters added
	// with the Go implementation; filled by the sources that support them.
	SilentStreams float64
	Calls         int
	CallHealth    float64
	FailedCalls   float64
	CaptureDrops  float64
	CaptureKnown  bool // vq-core's capture statistics have arrived
}

// Value returns the rule's counter from a snapshot and the number of samples
// it rests on; ok is false when the counter cannot be measured.
func (r Rule) Value(s Snapshot) (v float64, samples int, ok bool) {
	switch r.Counter {
	case "r_factor":
		return s.RFactor, s.SevanaStreams, s.SevanaStreams > 0
	case "sevana_mos":
		return s.SevanaMOS, s.SevanaStreams, s.SevanaStreams > 0
	case "network_mos":
		return s.NetworkMOS, s.Streams, s.Streams > 0
	case "packet_loss":
		return s.PacketLoss, s.Streams, s.Streams > 0
	case "jitter":
		return s.Jitter, s.Streams, s.Streams > 0
	case "duration":
		return s.Duration, s.Streams, s.Streams > 0
	case "silent_streams":
		return s.SilentStreams, s.SevanaStreams, s.SevanaStreams > 0
	case "free_disk_space":
		return s.FreeDisk, 1, s.FreeDisk >= 0
	case "call_health":
		return s.CallHealth, s.Calls, s.Calls > 0
	case "failed_calls":
		return s.FailedCalls, s.Calls, s.Calls > 0
	case "capture_drops":
		return s.CaptureDrops, 1, s.CaptureKnown
	}
	return 0, 0, false
}

// Source measures a rule at a moment.
type Source interface {
	Measure(r Rule, nowMs int64) (Snapshot, error)
}

// DBSource measures from vq-db's SQLite database.
type DBSource struct {
	DB       *sql.DB
	DiskPath string       // directory whose filesystem free_disk_space watches; "" = unknown
	Drops    *DropHistory // vq-core's capture drops; nil = capture_drops unknown

	mu     sync.Mutex
	silent map[int64]bool // stream_id -> silent, so a report is parsed once
}

// streamKPIs aggregates the finished streams of a window: the same tables and
// filter columns as the dashboard's stream list (api.finishedBase).
const streamKPIs = "select count(*) as n," +
	" sum(case when rtpmon_statistics.sevana_mos > 0.0001 then 1 else 0 end) as n_sev," +
	" avg(case when rtpmon_statistics.sevana_mos > 0.0001 then rtpmon_statistics.sevana_rfactor end) as r_factor," +
	" avg(case when rtpmon_statistics.sevana_mos > 0.0001 then rtpmon_statistics.sevana_mos end) as sevana_mos," +
	" avg(rtpmon_statistics.network_mos) as network_mos," +
	" sum(rtpmon_statistics.lost_packet_counter) as lost," +
	" sum(rtpmon_statistics.rtp_packet_counter) as rtp," +
	" avg(rtpmon_statistics.jitter) as jitter," +
	" avg((rtpmon_statistics.end_timestamp - rtpmon_statistics.start_timestamp) / 1000.0) as duration" +
	" from rtpmon_statistics" +
	" inner join rtpmon_streams on rtpmon_streams.stream_id = rtpmon_statistics.stream_id" +
	" inner join rtpmon_instances on rtpmon_streams.agent_id = rtpmon_instances.id" +
	" where rtpmon_statistics.end_timestamp > ?"

// Measure implements Source.
func (s *DBSource) Measure(r Rule, nowMs int64) (Snapshot, error) {
	snap := Snapshot{FreeDisk: -1}
	fromMs := nowMs - r.Window.Milliseconds()
	switch r.Scope {
	case ScopeHost:
		snap.FreeDisk = freeDiskShare(s.DiskPath)
		return snap, nil
	case ScopeCapture:
		if s.Drops != nil {
			n, ok := s.Drops.Within(r.Window)
			snap.CaptureDrops, snap.CaptureKnown = float64(n), ok
		}
		return snap, nil
	case ScopeCalls:
		return snap, s.measureCalls(r, fromMs, &snap)
	}
	cond, args, err := streamFilter(r.Filter)
	if err != nil {
		return snap, err
	}
	query := streamKPIs + cond
	args = append([]any{fromMs}, args...)
	var n, nSev sql.NullInt64
	var rf, sev, net, lost, rtp, jit, dur sql.NullFloat64
	if err := s.DB.QueryRow(query, args...).Scan(&n, &nSev, &rf, &sev, &net, &lost, &rtp, &jit, &dur); err != nil {
		return snap, err
	}
	snap.Streams, snap.SevanaStreams = int(n.Int64), int(nSev.Int64)
	snap.RFactor, snap.SevanaMOS, snap.NetworkMOS = rf.Float64, sev.Float64, net.Float64
	snap.Jitter, snap.Duration = jit.Float64, dur.Float64
	if expected := rtp.Float64 + lost.Float64; expected > 0 {
		snap.PacketLoss = 100 * lost.Float64 / expected
	}
	if r.Counter == "silent_streams" {
		if err := s.measureSilent(fromMs, r.Filter, &snap); err != nil {
			return snap, err
		}
	}
	return snap, nil
}

// streamFilter is " and (<filter>)" with its parameters, or "" for no filter.
func streamFilter(expr string) (string, []any, error) {
	if strings.TrimSpace(expr) == "" {
		return "", nil, nil
	}
	frag, err := filter.BuildWhere(expr, "qmark")
	if err != nil {
		return "", nil, err
	}
	return " and (" + frag.Text + ")", frag.Params, nil
}

const streamsFrom = " from rtpmon_statistics" +
	" inner join rtpmon_streams on rtpmon_streams.stream_id = rtpmon_statistics.stream_id" +
	" inner join rtpmon_instances on rtpmon_streams.agent_id = rtpmon_instances.id" +
	" where rtpmon_statistics.end_timestamp > ? and rtpmon_statistics.sevana_mos > 0.0001"

// measureSilent sets SilentStreams: the share of the window's PVQA-analysed
// streams that carry only silence (pvqa.SilentStream, the dashboard's rule).
// Each stream's report is parsed once and remembered by stream id.
func (s *DBSource) measureSilent(fromMs int64, expr string, snap *Snapshot) error {
	cond, args, err := streamFilter(expr)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query("select rtpmon_statistics.stream_id"+streamsFrom+cond, append([]any{fromMs}, args...)...)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.silent == nil {
		s.silent = map[int64]bool{}
	}
	keep := make(map[int64]bool, len(ids))
	silent := 0
	for _, id := range ids {
		v, ok := s.silent[id]
		if !ok {
			var report sql.NullString
			if err := s.DB.QueryRow("select detector_report from rtpmon_statistics where stream_id = ?", id).Scan(&report); err != nil && err != sql.ErrNoRows {
				return err
			}
			v, _ = pvqa.SilentStream([]string{report.String})
		}
		keep[id] = v
		if v {
			silent++
		}
	}
	s.silent = keep // forget streams that left every window
	if len(ids) > 0 {
		snap.SilentStreams = 100 * float64(silent) / float64(len(ids))
	}
	snap.SevanaStreams = len(ids)
	return nil
}

// callFilter is " where (<filter>)" over the per-call row "c", or "".
func callFilter(expr string) (string, []any, error) {
	if strings.TrimSpace(expr) == "" {
		return "", nil, nil
	}
	frag, err := filter.BuildSipCallWhere(expr, "qmark")
	if err != nil {
		return "", nil, err
	}
	return " where (" + frag.Text + ")", frag.Params, nil
}

// measureCalls sets Calls and the counter's share. failed_calls counts the
// calls that were set up or failed in the window; call_health the calls vq-core
// judged in the window (its latest verdict per call), bad when the verdict has
// a warning (of Code, when given). The filter is the SIP calls list's.
func (s *DBSource) measureCalls(r Rule, fromMs int64, snap *Snapshot) error {
	cond, fargs, err := callFilter(r.Filter)
	if err != nil {
		return err
	}
	switch r.Counter {
	case "failed_calls":
		agg := api.SipCallAggregate(" where call_id in (select call_id from rtpmon_sip_events" +
			" where event_timestamp > ? and event_type in (1, 4))")
		where := " where (c.established = 1 or c.failed = 1)"
		if cond != "" {
			where += " and" + strings.TrimPrefix(cond, " where")
		}
		var n, failed sql.NullInt64
		err := s.DB.QueryRow("select count(*), sum(case when c.failed = 1 then 1 else 0 end) from ("+agg+") c"+where,
			append([]any{fromMs}, fargs...)...).Scan(&n, &failed)
		if err != nil {
			return err
		}
		snap.Calls = int(n.Int64)
		if n.Int64 > 0 {
			snap.FailedCalls = 100 * float64(failed.Int64) / float64(n.Int64)
		}
		return nil
	case "call_health":
		query := "select h.call_id, h.warnings from rtpmon_call_health h where h.event_timestamp > ?"
		args := []any{fromMs}
		if cond != "" {
			agg := api.SipCallAggregate(" where call_id in (select call_id from rtpmon_call_health where event_timestamp > ?)")
			query += " and h.call_id in (select c.call_id from (" + agg + ") c" + cond + ")"
			args = append(append(args, fromMs), fargs...)
		}
		rows, err := s.DB.Query(query+" order by h.id", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		latest := map[string]bool{}
		for rows.Next() {
			var id, text string
			if err := rows.Scan(&id, &text); err != nil {
				return err
			}
			var ws []struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal([]byte(text), &ws)
			bad := false
			for _, w := range ws {
				if r.Code == "" || w.Code == r.Code {
					bad = true
				}
			}
			latest[id] = bad // later verdicts win
		}
		if err := rows.Err(); err != nil {
			return err
		}
		bad := 0
		for _, b := range latest {
			if b {
				bad++
			}
		}
		snap.Calls = len(latest)
		if len(latest) > 0 {
			snap.CallHealth = 100 * float64(bad) / float64(len(latest))
		}
		return nil
	}
	return fmt.Errorf("counter %s is not a call counter", r.Counter)
}

// freeDiskShare is the free share (0..1) of the filesystem holding path, or -1.
func freeDiskShare(path string) float64 {
	if strings.TrimSpace(path) == "" {
		return -1
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return -1
	}
	return float64(st.Bavail) / float64(st.Blocks)
}
