package worker

import (
	"log/slog"
	"time"

	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/model"
)

// Cleaner is the writer surface the cleanup worker needs.
type Cleaner interface {
	RemoveOldRecords(cutoffMs int64) (db.CleanupStats, error)
	RemoveOldAudio(cutoffMs int64) (int64, error)
}

// CleanupWorker periodically purges records / audio older than the configured
// lifetimes. Port of vq_db/__main__.py::CleanupWorker.
type CleanupWorker struct {
	writer   Cleaner
	recordsS int64
	audioS   int64
	interval time.Duration
	now      func() int64
	stop     chan struct{}
}

// NewCleanupWorker constructs a CleanupWorker (interval defaults to 1h).
func NewCleanupWorker(writer Cleaner, recordsS, audioS int64, interval time.Duration) *CleanupWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	return &CleanupWorker{
		writer: writer, recordsS: recordsS, audioS: audioS, interval: interval,
		now:  func() int64 { return time.Now().UnixMilli() },
		stop: make(chan struct{}),
	}
}

// Start runs the periodic sweep until Stop is called.
func (c *CleanupWorker) Start() {
	go func() {
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-ticker.C:
				c.sweep()
			}
		}
	}()
}

// Stop halts the worker.
func (c *CleanupWorker) Stop() { close(c.stop) }

func (c *CleanupWorker) sweep() {
	nowMs := c.now()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("cleanup sweep panicked", "recover", r)
		}
	}()
	// One line per sweep: what was deleted and how long each phase took, so a
	// "database is locked" in the ingest writer can be matched to a sweep.
	if c.recordsS > 0 {
		st, err := c.writer.RemoveOldRecords(nowMs - c.recordsS*1000)
		attrs := []any{
			"streams", st.Streams, "chunks", st.Chunks, "sip_events", st.SipEvents,
			"read", st.Read.Round(time.Millisecond), "max_chunk", st.MaxChunk.Round(time.Millisecond),
			"sip_delete", st.SipDelete.Round(time.Millisecond), "total", st.Total.Round(time.Millisecond),
		}
		if err != nil {
			slog.Error("cleanup remove_old_records failed", append(attrs, "err", err)...)
		} else {
			slog.Info("cleanup remove_old_records", attrs...)
		}
	}
	if c.audioS > 0 {
		t := time.Now()
		n, err := c.writer.RemoveOldAudio(nowMs - c.audioS*1000)
		if err != nil {
			slog.Error("cleanup remove_old_audio failed", "err", err)
		} else {
			slog.Info("cleanup remove_old_audio", "chunks", n, "total", time.Since(t).Round(time.Millisecond))
		}
	}
}

// GhostRegistry is the registry surface the ghost sweeper needs.
type GhostRegistry interface {
	SweepGhosts(idleMs int64) []model.StreamReport
}

// Finalizer writes finalized ghost reports (satisfied by *db.Writer).
type Finalizer interface {
	AddFinal(report model.StreamReport) error
}

// GhostSweeper finalizes ghost streams (active streams whose stream_finish
// never arrived). Not a goroutine: Tick is the bus subscriber's on_idle
// callback, so it runs on the writer-owning goroutine. It self-throttles to
// interval between actual sweeps. Port of vq_db/__main__.py::GhostSweeper.
type GhostSweeper struct {
	writer   Finalizer
	registry GhostRegistry
	idleMs   int64
	interval time.Duration
	nextAt   time.Time
	now      func() time.Time
}

// NewGhostSweeper constructs a GhostSweeper (interval defaults to 30s).
func NewGhostSweeper(writer Finalizer, registry GhostRegistry, idleS int64, interval time.Duration) *GhostSweeper {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &GhostSweeper{
		writer: writer, registry: registry, idleMs: idleS * 1000, interval: interval,
		now: time.Now,
	}
}

// Tick runs a throttled ghost sweep. Cheap when not due.
func (g *GhostSweeper) Tick() {
	now := g.now()
	if !g.nextAt.IsZero() && now.Before(g.nextAt) {
		return
	}
	g.nextAt = now.Add(g.interval)
	defer func() {
		if r := recover(); r != nil {
			slog.Error("ghost sweep panicked", "recover", r)
		}
	}()
	finals := g.registry.SweepGhosts(g.idleMs)
	for _, report := range finals {
		if err := g.writer.AddFinal(report); err != nil {
			slog.Error("ghost AddFinal failed", "err", err)
		}
	}
	if len(finals) > 0 {
		slog.Info("finalized ghost stream(s) past idle timeout", "count", len(finals))
	}
}
