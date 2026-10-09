package worker

import (
	"reflect"
	"sync"
	"sync/atomic"
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

// notifyControl reports every Send on a channel, for tests that drive the
// worker goroutine.
type notifyControl struct {
	acks map[bus.TrackOp]bus.TrackAck
	sent chan bus.TrackOp
}

func (n *notifyControl) Send(op bus.TrackOp, patterns []string) bus.TrackAck {
	n.sent <- op
	return n.acks[op]
}

func TestSyncKickedWhenVqcoreRestarts(t *testing.T) {
	ctrl := &notifyControl{sent: make(chan bus.TrackOp, 8), acks: map[bus.TrackOp]bus.TrackAck{
		bus.TrackQuery:   {TransportOK: true, OK: true, Current: []string{}},
		bus.TrackReplace: {TransportOK: true, OK: true, Current: []string{"alice"}},
	}}
	// Interval 0: no periodic pass, so any traffic comes from the restart kick.
	w := NewTrackSyncWorker(fakeStore{[]string{"alice"}}, ctrl, 0)
	w.Start()
	defer w.Stop()

	w.NoteUptime(100)
	w.NoteUptime(101) // uptime growing: vq-core kept running
	select {
	case op := <-ctrl.sent:
		t.Fatalf("unexpected %v without a restart", op)
	case <-time.After(100 * time.Millisecond):
	}

	w.NoteUptime(2) // uptime went back: vq-core restarted
	for _, want := range []bus.TrackOp{bus.TrackQuery, bus.TrackReplace} {
		select {
		case op := <-ctrl.sent:
			if op != want {
				t.Fatalf("op = %v, want %v", op, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %v after the restart", want)
		}
	}
}

func TestKickBeforeStartIsKept(t *testing.T) {
	ctrl := &notifyControl{sent: make(chan bus.TrackOp, 8), acks: map[bus.TrackOp]bus.TrackAck{
		bus.TrackQuery: {TransportOK: true, OK: true, Current: []string{"alice"}},
	}}
	w := NewTrackSyncWorker(fakeStore{[]string{"alice"}}, ctrl, 0)
	w.Kick()
	w.Kick() // coalesces
	w.Start()
	defer w.Stop()
	select {
	case op := <-ctrl.sent:
		if op != bus.TrackQuery {
			t.Fatalf("op = %v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("kick before Start was lost")
	}
}

// staleControl mimics zmq4 after vq-core restarted: the old socket never
// replies until Reconnect redials it.
type staleControl struct {
	notifyControl
	mu         sync.Mutex
	stale      bool
	reconnects int
}

func (s *staleControl) Send(op bus.TrackOp, patterns []string) bus.TrackAck {
	s.mu.Lock()
	stale := s.stale
	s.mu.Unlock()
	if stale {
		return bus.TrackAck{Error: "control socket did not reply"}
	}
	return s.notifyControl.Send(op, patterns)
}

func (s *staleControl) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconnects++
	s.stale = false
	return nil
}

func TestRestartReconnectsBeforeSync(t *testing.T) {
	ctrl := &staleControl{stale: true, notifyControl: notifyControl{sent: make(chan bus.TrackOp, 8), acks: map[bus.TrackOp]bus.TrackAck{
		bus.TrackQuery:   {TransportOK: true, OK: true, Current: []string{}},
		bus.TrackReplace: {TransportOK: true, OK: true, Current: []string{"alice"}},
	}}}
	w := NewTrackSyncWorker(fakeStore{[]string{"alice"}}, ctrl, 0)
	w.Start()
	defer w.Stop()
	w.NoteUptime(500)
	w.NoteUptime(1)
	for _, want := range []bus.TrackOp{bus.TrackQuery, bus.TrackReplace} {
		select {
		case op := <-ctrl.sent:
			if op != want {
				t.Fatalf("op = %v, want %v", op, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %v after the restart", want)
		}
	}
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if ctrl.reconnects != 1 {
		t.Errorf("reconnects = %d, want 1", ctrl.reconnects)
	}
}

// unreachableControl never answers, whatever Reconnect does.
type unreachableControl struct{ attempts atomic.Int32 }

func (u *unreachableControl) Send(bus.TrackOp, []string) bus.TrackAck {
	u.attempts.Add(1)
	return bus.TrackAck{Error: "control socket did not reply"}
}

func (u *unreachableControl) Reconnect() error { return nil }

func TestRestartRetriesWhileVqcoreStarting(t *testing.T) {
	ctrl := &unreachableControl{}
	w := NewTrackSyncWorker(fakeStore{[]string{"alice"}}, ctrl, 0)
	w.retryEvery = 10 * time.Millisecond
	w.retryFor = 200 * time.Millisecond
	w.Start()
	defer w.Stop()
	w.NoteUptime(9)
	w.NoteUptime(0)
	time.Sleep(400 * time.Millisecond)
	if n := ctrl.attempts.Load(); n < 3 {
		t.Errorf("attempts = %d, want retries while vq-core is unreachable", n)
	}
}
