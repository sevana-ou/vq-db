// Package alarm evaluates threshold alarms over the stored results and
// notifies a shell command and/or a webhook when a rule enters or leaves the
// alarm state. It restores the `alarm:` configuration the C++ vq-db documented
// (name, counter, limit, interval, command), measured from the database so it
// survives restarts and covers every vq-core feeding this vq-db.
package alarm

import (
	"fmt"
	"strings"
	"time"

	"github.com/sevana-ou/vq-db/internal/config"
	"github.com/sevana-ou/vq-db/internal/filter"
)

// Op is the direction in which a value is bad.
type Op int

const (
	Below Op = iota // alarm while the value is below the limit
	Above           // alarm while the value is above the limit
)

func (o Op) String() string {
	if o == Above {
		return "above"
	}
	return "below"
}

// Scope tells what a counter is measured over.
type Scope int

const (
	ScopeStreams Scope = iota // finished RTP streams in the window
	ScopeCalls                // SIP calls in the window
	ScopeHost                 // the host itself (disk)
	ScopeCapture              // vq-core's capture counters
)

// counterSpec describes one counter: the direction in which it is bad by
// default and what it is measured over.
type counterSpec struct {
	op    Op
	scope Scope
	unit  string
}

var counters = map[string]counterSpec{
	// The counters the C++ vq-db documented. Averages over the window's streams.
	"r_factor":    {Below, ScopeStreams, "R"},
	"sevana_mos":  {Below, ScopeStreams, "MOS"},
	"network_mos": {Below, ScopeStreams, "MOS"},
	"packet_loss": {Above, ScopeStreams, "%"},
	"jitter":      {Above, ScopeStreams, "ms"},
	"duration":    {Below, ScopeStreams, "s"},
	// Free share (0..1) of the filesystem holding the database.
	"free_disk_space": {Below, ScopeHost, ""},
	// Added with the Go implementation. Shares are percentages of the window.
	"silent_streams": {Above, ScopeStreams, "%"},
	"call_health":    {Above, ScopeCalls, "%"},
	"failed_calls":   {Above, ScopeCalls, "%"},
	"capture_drops":  {Above, ScopeCapture, "packets"},
}

// Counters lists the counter names, for documentation and errors.
func Counters() []string {
	out := make([]string, 0, len(counters))
	for _, name := range []string{"r_factor", "sevana_mos", "network_mos", "packet_loss", "jitter",
		"duration", "free_disk_space", "silent_streams", "call_health", "failed_calls", "capture_drops"} {
		out = append(out, name)
	}
	return out
}

// Rule is one validated alarm.
type Rule struct {
	Name       string
	Counter    string
	Op         Op
	Limit      float64
	Window     time.Duration
	MinSamples int
	Filter     string
	Code       string
	Command    string
	Webhook    string
	OnClear    bool
	Scope      Scope
	Unit       string
}

// DefaultWindow applies when a rule gives no interval.
const DefaultWindow = 15 * time.Minute

// NewRule validates one configured alarm and applies the defaults.
func NewRule(c config.AlarmConfig) (Rule, error) {
	r := Rule{
		Name: strings.TrimSpace(c.Name), Counter: c.Counter, Limit: c.Limit,
		Window: time.Duration(c.IntervalS) * time.Second, MinSamples: c.MinSamples,
		Filter: strings.TrimSpace(c.Filter), Code: c.Code,
		Command: strings.TrimSpace(c.Command), Webhook: c.Webhook, OnClear: c.OnClear,
	}
	spec, ok := counters[c.Counter]
	if !ok {
		return r, fmt.Errorf("alarm %q: unknown counter %q (one of: %s)", c.Name, c.Counter, strings.Join(Counters(), ", "))
	}
	if r.Name == "" {
		r.Name = c.Counter
	}
	if !c.HasLimit {
		return r, fmt.Errorf("alarm %q: no limit", r.Name)
	}
	r.Scope, r.Unit, r.Op = spec.scope, spec.unit, spec.op
	switch c.Op {
	case "":
	case "below", "<":
		r.Op = Below
	case "above", ">":
		r.Op = Above
	default:
		return r, fmt.Errorf("alarm %q: op %q is neither below nor above", r.Name, c.Op)
	}
	if r.Window <= 0 {
		r.Window = DefaultWindow
	}
	if r.MinSamples <= 0 {
		r.MinSamples = 1
	}
	if r.Command == "" && r.Webhook == "" {
		return r, fmt.Errorf("alarm %q: neither command nor webhook is set", r.Name)
	}
	if r.Webhook != "" && !strings.HasPrefix(r.Webhook, "http://") && !strings.HasPrefix(r.Webhook, "https://") {
		return r, fmt.Errorf("alarm %q: webhook %q is not an http(s) URL", r.Name, r.Webhook)
	}
	if r.Code != "" && r.Counter != "call_health" {
		return r, fmt.Errorf("alarm %q: code applies to the call_health counter only", r.Name)
	}
	if r.Filter != "" {
		switch r.Scope {
		case ScopeStreams:
			if _, err := filter.BuildWhere(r.Filter, "qmark"); err != nil {
				return r, fmt.Errorf("alarm %q: filter: %v", r.Name, err)
			}
		case ScopeCalls:
			if _, err := filter.BuildSipCallWhere(r.Filter, "qmark"); err != nil {
				return r, fmt.Errorf("alarm %q: filter: %v", r.Name, err)
			}
		default:
			return r, fmt.Errorf("alarm %q: counter %s takes no filter", r.Name, r.Counter)
		}
	}
	return r, nil
}

// Bad reports whether a value is on the alarm side of the limit.
func (r Rule) Bad(v float64) bool {
	if r.Op == Above {
		return v > r.Limit
	}
	return v < r.Limit
}
