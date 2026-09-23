// Package ingest routes decoded domain events to their sinks (the DB writer,
// the active-stream registry, and the instance-stats snapshot). Port of
// vq_db/ingest/pipeline.py. The pipeline owns the writer's single-threaded
// contract: it must be fed from one goroutine (the bus loop).
package ingest

import (
	"log/slog"

	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/model"
	"github.com/sevana-ou/vq-db/internal/state"
	"github.com/sevana-ou/vq-db/internal/translate"
)

// Pipeline dispatches decoded events to the writer, the (optional) registry and
// the (optional) instance-stats callback.
type Pipeline struct {
	writer          *db.Writer
	registry        *state.ActiveStreamRegistry // may be nil
	onInstanceStats func(model.InstanceStatistics)
}

// NewPipeline constructs a Pipeline. registry and onInstanceStats may be nil.
func NewPipeline(writer *db.Writer, registry *state.ActiveStreamRegistry, onInstanceStats func(model.InstanceStatistics)) *Pipeline {
	return &Pipeline{writer: writer, registry: registry, onInstanceStats: onInstanceStats}
}

// OnBytes decodes raw bus bytes and dispatches. Suitable as the ZMQ message
// callback. Unparseable / ignored payloads are skipped silently.
func (p *Pipeline) OnBytes(data []byte) {
	event, err := translate.DecodeBytes(data)
	if err != nil {
		return
	}
	if event != nil {
		p.Dispatch(event)
	}
}

// Dispatch routes a decoded domain event to its sinks.
func (p *Pipeline) Dispatch(event any) {
	switch e := event.(type) {
	case model.MediaStreamId:
		if _, err := p.writer.OpenStream(e); err != nil {
			slog.Error("open_stream failed", "err", err)
		}
		if p.registry != nil {
			p.registry.OnStreamDetected(e)
		}
	case model.StreamReport:
		if e.FullReport {
			if err := p.writer.AddFinal(e); err != nil {
				slog.Error("add_final failed", "err", err)
			}
			if p.registry != nil {
				p.registry.OnFinished(e)
			}
		} else {
			if err := p.writer.AddInterval(e); err != nil {
				slog.Error("add_interval failed", "err", err)
			}
			if p.registry != nil {
				p.registry.OnReport(e)
			}
		}
	case model.StreamAudio:
		if err := p.writer.AddAudio(e); err != nil {
			slog.Error("add_audio failed", "err", err)
		}
	case model.SipCallStart:
		if err := p.writer.AddSipCallStart(e); err != nil {
			slog.Error("add_sip_call_start failed", "err", err)
		}
	case model.SipCallEnd:
		if err := p.writer.AddSipCallEnd(e); err != nil {
			slog.Error("add_sip_call_end failed", "err", err)
		}
	case model.SipReinvite:
		if err := p.writer.AddSipReinvite(e); err != nil {
			slog.Error("add_sip_reinvite failed", "err", err)
		}
	case model.SipCallFailed:
		if err := p.writer.AddSipCallFailed(e); err != nil {
			slog.Error("add_sip_call_failed failed", "err", err)
		}
	case model.InstanceStatistics:
		if p.onInstanceStats != nil {
			p.onInstanceStats(e)
		}
		if p.registry != nil && p.registry.NoteUptime(e.UptimeSeconds) {
			// vq-core restarted: finalize now-orphaned active streams on this
			// (writer-owning) goroutine.
			finals := p.registry.DrainAll()
			for _, report := range finals {
				if err := p.writer.AddFinal(report); err != nil {
					slog.Error("add_final (ghost) failed", "err", err)
				}
			}
			slog.Info("vq-core restart detected; finalized ghost streams", "count", len(finals))
		}
	}
}
