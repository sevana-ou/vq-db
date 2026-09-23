package state

import (
	"testing"

	"github.com/sevana-ou/vq-db/internal/model"
)

func sid(link string, ssrc uint32) model.MediaStreamId {
	return model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 5004, DstIP: "10.0.0.2", DstPort: 5060, SSRC: ssrc, LinkID: link}
}

func fptr(v float64) *float64 { return &v }

func TestDetectAndCount(t *testing.T) {
	reg := NewActiveStreamRegistry("agent_1", "First")
	reg.OnStreamDetected(sid("lnk-1", 0x1234))
	reg.OnStreamDetected(sid("lnk-2", 2))
	if reg.ActiveCount() != 2 || reg.SessionStarted() != 2 {
		t.Errorf("active=%d started=%d", reg.ActiveCount(), reg.SessionStarted())
	}
}

func TestDetectIsIdempotent(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	reg.OnStreamDetected(sid("lnk-1", 0x1234))
	reg.OnStreamDetected(sid("lnk-1", 0x1234))
	if reg.ActiveCount() != 1 {
		t.Errorf("active = %d", reg.ActiveCount())
	}
}

func TestDetectedWithoutReportExcludedFromRecords(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	reg.OnStreamDetected(sid("lnk-1", 0x1234))
	if reg.ActiveCount() != 1 || len(reg.SnapshotRecords()) != 0 {
		t.Error("detected-without-report should be excluded")
	}
}

func TestRecordBuiltFromFirstAndLastReport(t *testing.T) {
	reg := NewActiveStreamRegistry("agent_1", "First")
	s := sid("lnk-1", 0x1234)
	reg.OnStreamDetected(s)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 10_000, EndMs: 20_000, SevanaMOS: fptr(3.0), Jitter: 5.0, DurationAudio: 10000, SipCallID: "c1", SipPeerA: "sip:a@h"})
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 20_000, EndMs: 30_000, SevanaMOS: fptr(3.8), Jitter: 7.0, DurationAudio: 10000, SipCallID: "c1"})
	rec := reg.SnapshotRecords()[0]
	if rec["start_time"] != int64(10) {
		t.Errorf("start_time = %v", rec["start_time"])
	}
	if rec["start_ms"] != int64(10_000) {
		t.Errorf("start_ms = %v", rec["start_ms"])
	}
	if rec["sevana_mos"] != 3.8 {
		t.Errorf("sevana_mos = %v", rec["sevana_mos"])
	}
	if rec["duration"] != int64(20_000) {
		t.Errorf("duration = %v", rec["duration"])
	}
	if rec["duration_audio"] != int64(20000) {
		t.Errorf("duration_audio = %v", rec["duration_audio"])
	}
	if rec["instance_id"] != "agent_1" || rec["link_id"] != "lnk-1" || rec["ssrc"] != "4660" {
		t.Errorf("id=%v link=%v ssrc=%v", rec["instance_id"], rec["link_id"], rec["ssrc"])
	}
}

func TestRecordAccumulatesDtxAndSilenceRatio(t *testing.T) {
	reg := NewActiveStreamRegistry("agent_1", "First")
	s := sid("lnk-1", 0x1234)
	reg.OnStreamDetected(s)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 10_000, DtxSid: 1, DtxCount: 40, DtxTotal: 50})
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 10_000, EndMs: 20_000, DtxSid: 1, DtxCount: 44, DtxTotal: 50})
	rec := reg.SnapshotRecords()[0]
	if rec["dtx_sid"] != int64(2) || rec["dtx_count"] != int64(84) || rec["dtx_total"] != int64(100) {
		t.Errorf("dtx = %v/%v/%v", rec["dtx_sid"], rec["dtx_count"], rec["dtx_total"])
	}
	if rec["silence_ratio"] != 0.86 {
		t.Errorf("silence_ratio = %v", rec["silence_ratio"])
	}
}

func TestRecordSilenceRatioZeroWithoutDtx(t *testing.T) {
	reg := NewActiveStreamRegistry("agent_1", "First")
	s := sid("lnk-1", 0x1234)
	reg.OnStreamDetected(s)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 10_000, SevanaMOS: fptr(4.0)})
	if reg.SnapshotRecords()[0]["silence_ratio"] != 0.0 {
		t.Error("silence_ratio should be 0")
	}
}

func TestFinishedRemovesAndCounts(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	s := sid("lnk-1", 0x1234)
	reg.OnStreamDetected(s)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 10_000, SevanaMOS: fptr(4.0)})
	reg.OnFinished(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 10_000, SevanaMOS: fptr(4.0), FullReport: true})
	if reg.ActiveCount() != 0 || reg.SessionFinished() != 1 {
		t.Errorf("active=%d finished=%d", reg.ActiveCount(), reg.SessionFinished())
	}
}

func TestReportWithoutDetectIsTrackedLazily(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	s := sid("lnk-1", 0x1234)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 10_000, SevanaMOS: fptr(4.0)})
	if reg.ActiveCount() != 1 || len(reg.SnapshotRecords()) != 1 {
		t.Error("lazy tracking failed")
	}
}

func TestSweepGhostsFinalizesIdleStream(t *testing.T) {
	reg := NewActiveStreamRegistry("agent_1", "First")
	s := sid("lnk-1", 0x1234)
	reg.OnStreamDetected(s)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 10_000, EndMs: 20_000, SevanaMOS: fptr(3.0), RTPPacketCounter: 500, LostPacketCounter: 5, DtxSid: 1, DtxCount: 2, DtxTotal: 10, DurationAudio: 10000})
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 20_000, EndMs: 30_000, SevanaMOS: fptr(3.8), RTPPacketCounter: 500, LostPacketCounter: 3, DtxSid: 1, DtxCount: 2, DtxTotal: 10, DurationAudio: 10000})

	finals := reg.SweepGhostsAt(1, 1<<62)
	if len(finals) != 1 {
		t.Fatalf("finals = %d", len(finals))
	}
	f := finals[0]
	if !f.FullReport || f.StreamID != s || f.StartMs != 10_000 || f.EndMs != 30_000 {
		t.Errorf("final = %+v", f)
	}
	if f.RTPPacketCounter != 1000 || f.LostPacketCounter != 8 || *f.SevanaMOS != 3.8 || f.DtxTotal != 20 {
		t.Errorf("counters wrong: %+v", f)
	}
	if f.UserReport != "auto-finalized" {
		t.Errorf("user_report = %q", f.UserReport)
	}
	if reg.ActiveCount() != 0 || reg.SessionFinished() != 1 {
		t.Errorf("active=%d finished=%d", reg.ActiveCount(), reg.SessionFinished())
	}
}

func TestSweepGhostsKeepsFreshStream(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	s := sid("lnk-1", 0x1234)
	reg.OnStreamDetected(s)
	reg.OnReport(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 10_000, SevanaMOS: fptr(4.0)})
	if len(reg.SweepGhosts(60_000)) != 0 || reg.ActiveCount() != 1 {
		t.Error("fresh stream swept")
	}
}

func TestSweepGhostsEvictsReportlessWithoutFinal(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	reg.OnStreamDetected(sid("lnk-1", 0x1234))
	finals := reg.SweepGhostsAt(1, 1<<62)
	if len(finals) != 0 || reg.ActiveCount() != 0 || reg.SessionFinished() != 1 {
		t.Errorf("finals=%d active=%d finished=%d", len(finals), reg.ActiveCount(), reg.SessionFinished())
	}
}

func TestNoteUptimeDetectsRestart(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	if reg.NoteUptime(100) {
		t.Error("first call should be false")
	}
	if reg.NoteUptime(150) {
		t.Error("climbing should be false")
	}
	if !reg.NoteUptime(5) {
		t.Error("drop should be true")
	}
}

func TestDrainAllFinalizesEveryActiveStream(t *testing.T) {
	reg := NewActiveStreamRegistry("", "")
	a, b := sid("a", 1), sid("b", 2)
	reg.OnStreamDetected(a)
	reg.OnReport(model.StreamReport{StreamID: a, StartMs: 0, EndMs: 10_000, SevanaMOS: fptr(4.0)})
	reg.OnStreamDetected(b)
	finals := reg.DrainAll()
	if len(finals) != 1 || finals[0].StreamID != a {
		t.Errorf("finals = %+v", finals)
	}
	if reg.ActiveCount() != 0 || reg.SessionFinished() != 2 {
		t.Errorf("active=%d finished=%d", reg.ActiveCount(), reg.SessionFinished())
	}
}
