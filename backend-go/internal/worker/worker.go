// Package worker holds the background workers assembled by the daemon:
// track-pattern restore/resync, records/audio cleanup, and the ghost-stream
// sweep. Ports the worker classes from vq_db/__main__.py.
package worker

import (
	"log/slog"
	"sync"
	"time"

	"github.com/sevana-ou/vq-db/internal/bus"
)

// Control is the track-list control surface (satisfied by *bus.ControlClient).
type Control interface {
	Send(op bus.TrackOp, patterns []string) bus.TrackAck
}

// TrackLoader loads persisted track patterns (satisfied by *db.TrackStore).
type TrackLoader interface {
	Load() ([]string, error)
}

// RestoreTrackPatterns re-applies persisted track patterns to vq-core on start
// (best-effort). A transport failure is logged, not fatal.
func RestoreTrackPatterns(store TrackLoader, control Control) {
	patterns, err := store.Load()
	if err != nil || len(patterns) == 0 {
		return
	}
	ack := control.Send(bus.TrackReplace, patterns)
	if ack.TransportOK && ack.OK {
		slog.Info("restored persisted track patterns to vq-core", "count", len(patterns))
	} else {
		detail := ack.Error
		if detail == "" {
			detail = "control socket did not reply"
		}
		slog.Warn("could not restore track patterns to vq-core", "reason", detail)
	}
}

// TrackSyncWorker reconciles vq-core's in-memory track list with the persisted
// one, re-applying the persisted patterns if vq-core drifted. vq-core starts
// with an empty list, so a restart under a running vq-db is the usual drift:
// NoteUptime spots it from the instance statistics (sent every second) and
// syncs at once, instead of leaving calls unanalysed until the next periodic
// pass (interval, track-resync-interval; <= 0 disables the periodic pass).
type TrackSyncWorker struct {
	store    TrackLoader
	control  Control
	interval time.Duration
	stop     chan struct{}
	kick     chan struct{}

	mu         sync.Mutex
	lastUptime *uint64
}

// NewTrackSyncWorker constructs a TrackSyncWorker.
func NewTrackSyncWorker(store TrackLoader, control Control, interval time.Duration) *TrackSyncWorker {
	return &TrackSyncWorker{store: store, control: control, interval: interval,
		stop: make(chan struct{}), kick: make(chan struct{}, 1)}
}

// Start runs the sync loop until Stop is called. A Kick before Start is kept
// and handled once it runs.
func (t *TrackSyncWorker) Start() {
	go func() {
		var tick <-chan time.Time
		if t.interval > 0 {
			ticker := time.NewTicker(t.interval)
			defer ticker.Stop()
			tick = ticker.C
		}
		for {
			select {
			case <-t.stop:
				return
			case <-tick:
				t.SyncOnce()
			case <-t.kick:
				t.SyncOnce()
			}
		}
	}()
}

// Kick requests a sync pass now (non-blocking; repeated kicks coalesce).
func (t *TrackSyncWorker) Kick() {
	select {
	case t.kick <- struct{}{}:
	default:
	}
}

// NoteUptime records vq-core's uptime from an instance-statistics event and
// kicks a sync when it went backwards, i.e. vq-core restarted. Safe to call
// from the bus goroutine.
func (t *TrackSyncWorker) NoteUptime(uptimeSeconds uint64) {
	t.mu.Lock()
	restarted := t.lastUptime != nil && uptimeSeconds < *t.lastUptime
	v := uptimeSeconds
	t.lastUptime = &v
	t.mu.Unlock()
	if restarted {
		slog.Info("vq-core restart detected; re-applying track patterns", "uptime_s", uptimeSeconds)
		t.Kick()
	}
}

// Stop halts the worker.
func (t *TrackSyncWorker) Stop() { close(t.stop) }

// SyncOnce runs a single reconcile pass.
func (t *TrackSyncWorker) SyncOnce() {
	desired, err := t.store.Load()
	if err != nil || len(desired) == 0 {
		return // nothing to restore -> no control traffic
	}
	query := t.control.Send(bus.TrackQuery, nil)
	if !query.TransportOK {
		return // vq-core down; retry next tick
	}
	if setEqual(query.Current, desired) {
		return // already in sync
	}
	rep := t.control.Send(bus.TrackReplace, desired)
	if rep.TransportOK && rep.OK {
		slog.Info("re-synced track patterns to vq-core after drift", "count", len(desired))
	} else {
		detail := rep.Error
		if detail == "" {
			detail = "control socket did not reply"
		}
		slog.Warn("track re-sync REPLACE failed", "reason", detail)
	}
}

func setEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}
