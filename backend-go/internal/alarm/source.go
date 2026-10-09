package alarm

import (
	"database/sql"
	"fmt"
	"strings"
	"syscall"

	"github.com/sevana-ou/vq-db/internal/filter"
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
		return s.CaptureDrops, 1, true
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
	DiskPath string // directory whose filesystem free_disk_space watches; "" = unknown
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
func (s DBSource) Measure(r Rule, nowMs int64) (Snapshot, error) {
	snap := Snapshot{FreeDisk: -1}
	if r.Scope == ScopeHost {
		snap.FreeDisk = freeDiskShare(s.DiskPath)
		return snap, nil
	}
	if r.Scope != ScopeStreams {
		return snap, fmt.Errorf("counter %s is not measured yet", r.Counter)
	}
	query := streamKPIs
	args := []any{nowMs - r.Window.Milliseconds()}
	if r.Filter != "" {
		frag, err := filter.BuildWhere(r.Filter, "qmark")
		if err != nil {
			return snap, err
		}
		query += " and (" + frag.Text + ")"
		args = append(args, frag.Params...)
	}
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
	return snap, nil
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
