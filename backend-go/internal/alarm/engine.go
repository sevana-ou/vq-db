package alarm

import (
	"log/slog"
	"sync"
	"time"
)

// Kind is what happened to a rule.
type Kind string

const (
	Raised  Kind = "alarm"   // the rule entered the alarm state
	Repeat  Kind = "repeat"  // still in alarm one window after the last notification
	Cleared Kind = "cleared" // the rule left the alarm state
)

// Event is one notification.
type Event struct {
	Rule     Rule
	Kind     Kind
	Value    float64
	Samples  int
	Snapshot Snapshot
	At       time.Time
	Instance Instance
}

// Instance identifies this vq-db in notifications.
type Instance struct {
	ID   string
	Name string
}

// Notifier delivers events. Notify must not block: the engine calls it from
// its evaluation goroutine.
type Notifier interface {
	Notify(Event)
}

// Status is a rule's current state, for the dashboard and the API.
type Status struct {
	Rule       Rule
	Alarm      bool
	Value      float64
	Samples    int
	Measured   bool // a value with enough samples was available at the last check
	LastCheck  time.Time
	LastChange time.Time
	LastNotify time.Time
	LastError  string
}

type ruleState struct {
	rule   Rule
	status Status
}

// Engine evaluates the rules periodically.
type Engine struct {
	src       Source
	notifiers []Notifier
	instance  Instance
	now       func() time.Time

	mu    sync.Mutex
	rules []*ruleState
	stop  chan struct{}
	done  chan struct{}
}

// NewEngine builds an engine; rules keep their configured order.
func NewEngine(rules []Rule, src Source, instance Instance, notifiers ...Notifier) *Engine {
	e := &Engine{src: src, notifiers: notifiers, instance: instance, now: time.Now}
	for _, r := range rules {
		e.rules = append(e.rules, &ruleState{rule: r, status: Status{Rule: r}})
	}
	return e
}

// Tick evaluates every rule once.
//
// A rule whose counter has fewer than MinSamples samples in its window keeps
// its state: no traffic is not evidence that quality recovered, nor that it
// failed. A rule in alarm notifies again once per window while it stays bad.
func (e *Engine) Tick() {
	now := e.now()
	for _, st := range e.rules {
		snap, err := e.src.Measure(st.rule, now.UnixMilli())
		e.mu.Lock()
		s := &st.status
		s.LastCheck = now
		if err != nil {
			s.LastError, s.Measured = err.Error(), false
			e.mu.Unlock()
			slog.Warn("alarm check failed", "alarm", st.rule.Name, "err", err)
			continue
		}
		s.LastError = ""
		v, n, ok := st.rule.Value(snap)
		s.Value, s.Samples = v, n
		s.Measured = ok && n >= st.rule.MinSamples
		var ev *Event
		if s.Measured {
			bad := st.rule.Bad(v)
			switch {
			case bad && !s.Alarm:
				s.Alarm, s.LastChange, s.LastNotify = true, now, now
				ev = &Event{Kind: Raised}
			case bad && now.Sub(s.LastNotify) >= st.rule.Window:
				s.LastNotify = now
				ev = &Event{Kind: Repeat}
			case !bad && s.Alarm:
				s.Alarm, s.LastChange, s.LastNotify = false, now, now
				ev = &Event{Kind: Cleared}
			}
		}
		e.mu.Unlock()
		if ev != nil {
			ev.Rule, ev.Value, ev.Samples, ev.Snapshot, ev.At, ev.Instance = st.rule, v, n, snap, now, e.instance
			e.emit(*ev)
		}
	}
}

func (e *Engine) emit(ev Event) {
	attrs := []any{"alarm", ev.Rule.Name, "counter", ev.Rule.Counter, "value", ev.Value,
		"op", ev.Rule.Op.String(), "limit", ev.Rule.Limit, "samples", ev.Samples}
	switch ev.Kind {
	case Cleared:
		slog.Info("alarm cleared", attrs...)
	case Repeat:
		slog.Warn("alarm repeated", attrs...)
	default:
		slog.Warn("alarm raised", attrs...)
	}
	for _, n := range e.notifiers {
		n.Notify(ev)
	}
}

// Statuses returns a copy of every rule's state.
func (e *Engine) Statuses() []Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Status, 0, len(e.rules))
	for _, st := range e.rules {
		out = append(out, st.status)
	}
	return out
}

// Start evaluates the rules every `every` until Stop.
func (e *Engine) Start(every time.Duration) {
	e.stop, e.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(e.done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-e.stop:
				return
			case <-t.C:
				e.Tick()
			}
		}
	}()
}

// Stop halts the evaluation loop.
func (e *Engine) Stop() {
	if e.stop == nil {
		return
	}
	close(e.stop)
	<-e.done
}
