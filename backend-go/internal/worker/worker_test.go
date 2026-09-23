package worker

import (
	"reflect"
	"testing"

	"bytes"
	"errors"
	"github.com/sevana-ou/vq-db/internal/bus"
	"github.com/sevana-ou/vq-db/internal/db"
	"log/slog"
	"strings"
	"time"
)

type call struct {
	op       bus.TrackOp
	patterns []string
}

type fakeControl struct {
	acks  map[bus.TrackOp]bus.TrackAck
	def   bus.TrackAck
	calls []call
}

func (f *fakeControl) Send(op bus.TrackOp, patterns []string) bus.TrackAck {
	f.calls = append(f.calls, call{op, append([]string(nil), patterns...)})
	if a, ok := f.acks[op]; ok {
		return a
	}
	return f.def
}

func (f *fakeControl) ops() []bus.TrackOp {
	var out []bus.TrackOp
	for _, c := range f.calls {
		out = append(out, c.op)
	}
	return out
}

type fakeStore struct{ patterns []string }

func (s fakeStore) Load() ([]string, error) { return s.patterns, nil }

func TestRestorePushesReplaceWhenPatternsPersisted(t *testing.T) {
	ctrl := &fakeControl{def: bus.TrackAck{TransportOK: true, OK: true, Current: []string{"alice", "bob"}}}
	RestoreTrackPatterns(fakeStore{[]string{"alice", "bob"}}, ctrl)
	if len(ctrl.calls) != 1 || ctrl.calls[0].op != bus.TrackReplace || !reflect.DeepEqual(ctrl.calls[0].patterns, []string{"alice", "bob"}) {
		t.Errorf("calls = %+v", ctrl.calls)
	}
}

func TestRestoreNoopWhenEmpty(t *testing.T) {
	ctrl := &fakeControl{def: bus.TrackAck{TransportOK: true, OK: true}}
	RestoreTrackPatterns(fakeStore{nil}, ctrl)
	if len(ctrl.calls) != 0 {
		t.Errorf("calls = %+v", ctrl.calls)
	}
}

func TestRestoreSurvivesTransportFailure(t *testing.T) {
	ctrl := &fakeControl{def: bus.TrackAck{TransportOK: false, Error: "unreachable"}}
	RestoreTrackPatterns(fakeStore{[]string{"alice"}}, ctrl) // must not panic
	if len(ctrl.calls) != 1 || ctrl.calls[0].op != bus.TrackReplace {
		t.Errorf("calls = %+v", ctrl.calls)
	}
}

func TestSyncRePushesWhenVqcoreDrifted(t *testing.T) {
	ctrl := &fakeControl{acks: map[bus.TrackOp]bus.TrackAck{
		bus.TrackQuery:   {TransportOK: true, OK: true, Current: []string{}},
		bus.TrackReplace: {TransportOK: true, OK: true, Current: []string{"alice", "bob"}},
	}}
	NewTrackSyncWorker(fakeStore{[]string{"alice", "bob"}}, ctrl, 0).SyncOnce()
	if !reflect.DeepEqual(ctrl.ops(), []bus.TrackOp{bus.TrackQuery, bus.TrackReplace}) {
		t.Errorf("ops = %v", ctrl.ops())
	}
}

func TestSyncNoopWhenInSync(t *testing.T) {
	ctrl := &fakeControl{acks: map[bus.TrackOp]bus.TrackAck{
		bus.TrackQuery: {TransportOK: true, OK: true, Current: []string{"alice"}},
	}}
	NewTrackSyncWorker(fakeStore{[]string{"alice"}}, ctrl, 0).SyncOnce()
	if !reflect.DeepEqual(ctrl.ops(), []bus.TrackOp{bus.TrackQuery}) {
		t.Errorf("ops = %v", ctrl.ops())
	}
}

func TestSyncSkipsEntirelyWhenNothingPersisted(t *testing.T) {
	ctrl := &fakeControl{def: bus.TrackAck{TransportOK: true, OK: true}}
	NewTrackSyncWorker(fakeStore{nil}, ctrl, 0).SyncOnce()
	if len(ctrl.calls) != 0 {
		t.Errorf("calls = %+v", ctrl.calls)
	}
}

func TestSyncToleratesVqcoreDown(t *testing.T) {
	ctrl := &fakeControl{acks: map[bus.TrackOp]bus.TrackAck{
		bus.TrackQuery: {TransportOK: false, Error: "down"},
	}}
	NewTrackSyncWorker(fakeStore{[]string{"alice"}}, ctrl, 0).SyncOnce()
	if !reflect.DeepEqual(ctrl.ops(), []bus.TrackOp{bus.TrackQuery}) {
		t.Errorf("ops = %v", ctrl.ops())
	}
}

type fakeCleaner struct {
	stats   db.CleanupStats
	err     error
	audio   int64
	cutoffs []int64
}

func (f *fakeCleaner) RemoveOldRecords(cutoffMs int64) (db.CleanupStats, error) {
	f.cutoffs = append(f.cutoffs, cutoffMs)
	return f.stats, f.err
}

func (f *fakeCleaner) RemoveOldAudio(cutoffMs int64) (int64, error) {
	f.cutoffs = append(f.cutoffs, cutoffMs)
	return f.audio, nil
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(saved) })
	return &buf
}

// Each sweep logs what it deleted and how long the write-locked phases took.
func TestCleanupSweepLogsStats(t *testing.T) {
	logs := captureSlog(t)
	f := &fakeCleaner{audio: 3, stats: db.CleanupStats{
		Streams: 120, Chunks: 1, SipEvents: 240,
		Read: 2500 * time.Millisecond, MaxChunk: 800 * time.Millisecond,
		SipDelete: 5 * time.Millisecond, Total: 3400 * time.Millisecond,
	}}
	c := NewCleanupWorker(f, 3600, 60, 0)
	c.now = func() int64 { return 10_000_000 }
	c.sweep()

	if len(f.cutoffs) != 2 || f.cutoffs[0] != 10_000_000-3600_000 || f.cutoffs[1] != 10_000_000-60_000 {
		t.Errorf("cutoffs = %v", f.cutoffs)
	}
	out := logs.String()
	for _, want := range []string{
		`msg="cleanup remove_old_records"`, "streams=120", "chunks=1", "sip_events=240",
		"read=2.5s", "max_chunk=800ms", "sip_delete=5ms", "total=3.4s",
		`msg="cleanup remove_old_audio"`, "chunks=3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// A failed sweep logs the error with whatever it had done before failing.
func TestCleanupSweepLogsFailure(t *testing.T) {
	logs := captureSlog(t)
	f := &fakeCleaner{err: errors.New("database is locked"), stats: db.CleanupStats{Streams: 500, Chunks: 1}}
	NewCleanupWorker(f, 3600, 0, 0).sweep()
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, `err="database is locked"`) ||
		!strings.Contains(out, "streams=500") {
		t.Errorf("failure log:\n%s", out)
	}
}
