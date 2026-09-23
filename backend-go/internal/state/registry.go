// Package state holds the in-memory active-stream registry and the latest
// instance-statistics snapshot. Port of vq_db/state.
package state

import (
	"sort"
	"sync"
	"time"

	"github.com/sevana-ou/vq-db/internal/model"
)

type entry struct {
	streamID   model.MediaStreamId
	reports    []model.StreamReport
	order      int
	lastSeenMs int64
}

// ActiveStreamRegistry is the in-memory registry of active (in-progress) RTP
// streams. Thread-safe: the bus goroutine mutates it while HTTP handlers read
// snapshots. Port of vq_db/state/registry.py.
type ActiveStreamRegistry struct {
	agentID    string
	agentName  string
	mu         sync.Mutex
	streams    map[model.MediaStreamId]*entry
	order      int
	started    int
	finished   int
	lastUptime *uint64
	now        func() int64 // wall-clock ms; injectable for tests
}

// NewActiveStreamRegistry constructs a registry for the given agent identity.
func NewActiveStreamRegistry(agentID, agentName string) *ActiveStreamRegistry {
	return &ActiveStreamRegistry{
		agentID:   agentID,
		agentName: agentName,
		streams:   map[model.MediaStreamId]*entry{},
		now:       func() int64 { return time.Now().UnixMilli() },
	}
}

// ----- bus-fed mutations ----- //

// OnStreamDetected records a stream_start.
func (r *ActiveStreamRegistry) OnStreamDetected(id model.MediaStreamId) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started++
	r.order++
	if _, ok := r.streams[id]; !ok {
		r.streams[id] = &entry{streamID: id, order: r.order, lastSeenMs: r.now()}
	}
}

// OnReport records an interval report (lazily creating the stream if unseen).
func (r *ActiveStreamRegistry) OnReport(report model.StreamReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.streams[report.StreamID]
	if !ok {
		r.order++
		e = &entry{streamID: report.StreamID, order: r.order, lastSeenMs: r.now()}
		r.streams[report.StreamID] = e
	}
	e.reports = append(e.reports, report)
	e.lastSeenMs = r.now()
}

// OnFinished removes a finalized stream and counts it.
func (r *ActiveStreamRegistry) OnFinished(report model.StreamReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished++
	delete(r.streams, report.StreamID)
}

// ----- ghost cleanup ----- //

// NoteUptime feeds vq-core's reported uptime; returns true when it dropped below
// the previously seen value (vq-core restarted). The first call only records the
// baseline and returns false.
func (r *ActiveStreamRegistry) NoteUptime(uptimeSeconds uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.lastUptime
	v := uptimeSeconds
	r.lastUptime = &v
	return previous != nil && uptimeSeconds < *previous
}

// SweepGhosts evicts active streams not seen for at least idleMs (using the
// current wall-clock) and returns synthesized finals for those with report data.
func (r *ActiveStreamRegistry) SweepGhosts(idleMs int64) []model.StreamReport {
	return r.SweepGhostsAt(idleMs, r.now())
}

// SweepGhostsAt is SweepGhosts with an explicit now (ms).
func (r *ActiveStreamRegistry) SweepGhostsAt(idleMs, nowMs int64) []model.StreamReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	var stale []*entry
	for _, e := range r.orderedEntriesLocked() {
		if nowMs-e.lastSeenMs >= idleMs {
			stale = append(stale, e)
		}
	}
	return r.evictLocked(stale)
}

// DrainAll evicts every active stream (used on vq-core restart) and returns
// synthesized finals for those with report data.
func (r *ActiveStreamRegistry) DrainAll() []model.StreamReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evictLocked(r.orderedEntriesLocked())
}

func (r *ActiveStreamRegistry) evictLocked(entries []*entry) []model.StreamReport {
	var finals []model.StreamReport
	for _, e := range entries {
		delete(r.streams, e.streamID)
		r.finished++
		if len(e.reports) > 0 {
			finals = append(finals, synthesizeFinal(e))
		}
	}
	return finals
}

func synthesizeFinal(e *entry) model.StreamReport {
	reports := e.reports
	first := reports[0]
	last := reports[len(reports)-1]

	var rtp, lost, illegal uint64
	var durationAudio, amrNb, amrWb int64
	var dtxSid, dtxCount, dtxTotal uint64
	for _, rp := range reports {
		rtp += rp.RTPPacketCounter
		lost += rp.LostPacketCounter
		illegal += rp.IllegalPacketCounter
		durationAudio += rp.DurationAudio
		amrNb += rp.AmrNbSwitchCounter
		amrWb += rp.AmrWbSwitchCounter
		dtxSid += rp.DtxSid
		dtxCount += rp.DtxCount
		dtxTotal += rp.DtxTotal
	}
	userReport := last.UserReport
	if userReport == "" {
		userReport = "auto-finalized"
	}
	return model.StreamReport{
		StreamID:             e.streamID,
		StartMs:              first.StartMs,
		EndMs:                last.EndMs,
		RTPPacketCounter:     rtp,
		IllegalPacketCounter: illegal,
		LostPacketCounter:    lost,
		SevanaMOS:            last.SevanaMOS,
		NetworkMOS:           last.NetworkMOS,
		SevanaRfactor:        last.SevanaRfactor,
		Jitter:               last.Jitter,
		Codec:                last.Codec,
		RTTDelay:             last.RTTDelay,
		DetectorReport:       last.DetectorReport,
		DurationAudio:        durationAudio,
		AmrNbSwitchCounter:   amrNb,
		AmrWbSwitchCounter:   amrWb,
		DtxSid:               dtxSid,
		DtxCount:             dtxCount,
		DtxTotal:             dtxTotal,
		FullReport:           true,
		UserReport:           userReport,
		SipCallID:            last.SipCallID,
		SipPeerA:             last.SipPeerA,
		SipPeerB:             last.SipPeerB,
	}
}

// ----- reads ----- //

// ActiveCount returns the number of streams currently in the registry.
func (r *ActiveStreamRegistry) ActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}

// SessionStarted returns the total streams detected since start.
func (r *ActiveStreamRegistry) SessionStarted() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// SessionFinished returns the total streams finished since start.
func (r *ActiveStreamRegistry) SessionFinished() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

// SnapshotRecords returns value-map records for the active streams that have at
// least one report (detected-but-not-reported streams are counted in
// ActiveCount but excluded here, matching the C++ list).
func (r *ActiveStreamRegistry) SnapshotRecords() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var records []map[string]any
	for _, e := range r.orderedEntriesLocked() {
		if len(e.reports) == 0 {
			continue
		}
		records = append(records, r.recordLocked(e))
	}
	return records
}

// orderedEntriesLocked returns entries sorted by insertion order (reproducing
// Python dict iteration order). Caller holds the lock.
func (r *ActiveStreamRegistry) orderedEntriesLocked() []*entry {
	entries := make([]*entry, 0, len(r.streams))
	for _, e := range r.streams {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].order < entries[j].order })
	return entries
}

func mosOrZero(p *float64) float64 {
	if p == nil {
		return 0.0
	}
	return *p
}

func (r *ActiveStreamRegistry) recordLocked(e *entry) map[string]any {
	first := e.reports[0]
	last := e.reports[len(e.reports)-1]
	sid := e.streamID

	streamStart := first.StartStreamMs
	if streamStart == 0 {
		streamStart = first.StartMs
	}
	durationMs := last.EndMs - streamStart
	if durationMs < 0 {
		durationMs = 0
	}

	var dtxSid, dtxCount, dtxTotal uint64
	var durationAudio int64
	var rtp, lost uint64
	detectorReports := make([]string, 0, len(e.reports))
	for _, rp := range e.reports {
		dtxSid += rp.DtxSid
		dtxCount += rp.DtxCount
		dtxTotal += rp.DtxTotal
		durationAudio += rp.DurationAudio
		rtp += rp.RTPPacketCounter
		lost += rp.LostPacketCounter
		detectorReports = append(detectorReports, rp.DetectorReport)
	}
	silenceRatio := 0.0
	if dtxTotal > 0 {
		silenceRatio = float64(dtxSid+dtxCount) / float64(dtxTotal)
	}

	return map[string]any{
		// filter/sort value-map (units match SQLNameMap)
		"start_time":     streamStart / 1000, // seconds
		"sevana_mos":     mosOrZero(last.SevanaMOS),
		"network_mos":    mosOrZero(last.NetworkMOS),
		"sevana_rfactor": last.SevanaRfactor,
		"jitter":         last.Jitter,
		"rtt_delay":      last.RTTDelay,
		"silence_ratio":  silenceRatio,
		"duration":       durationMs, // ms
		"src_ip":         sid.SrcIP,
		"dst_ip":         sid.DstIP,
		"src_port":       int64(sid.SrcPort),
		"dst_port":       int64(sid.DstPort),
		"ssrc":           decimalString(uint64(sid.SSRC)),
		"sip_src":        last.SipPeerA,
		"sip_dst":        last.SipPeerB,
		"sip_callid":     last.SipCallID,
		"instance_id":    r.agentID,
		"instance_name":  r.agentName,
		// serialization extras
		"link_id":             sid.LinkID,
		"duration_audio":      durationAudio,
		"start_ms":            streamStart,
		"rtp_packet_counter":  int64(rtp),
		"lost_packet_counter": int64(lost),
		"dtx_sid":             int64(dtxSid),
		"dtx_count":           int64(dtxCount),
		"dtx_total":           int64(dtxTotal),
		"detector_reports":    detectorReports,
	}
}
