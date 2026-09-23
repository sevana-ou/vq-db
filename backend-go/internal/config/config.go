// Package config loads the shared vq-monitor.cfg (YAML) into a typed Config for
// vq-db. vq-db only needs a subset of the file (logging, the agent identity, the
// ZeroMQ ports, the dashboard block, and the database block). Unknown keys are
// ignored so the same file can stay shared with vq-core.
//
// This is a straight port of the Python vq_db/config.py; behavior (defaults,
// duration parsing, tolerant nested lookups) matches it exactly.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Defaults mirror the C++ server_settings.h.
const (
	DefaultZeroMQPort        = 9125
	DefaultZeroMQControlPort = 9127
	DefaultDashboardPort     = 9126
)

// Config is the typed vq-db configuration.
type Config struct {
	// Bus
	ZeroMQPort        int
	ZeroMQControlPort int
	// How often to re-apply persisted track patterns to vq-core (recovers from a
	// vq-core restart under a running vq-db). 0 disables the periodic resync.
	TrackResyncIntervalS int
	// An active stream not seen (no interval report) for this long is treated as
	// a ghost and finalized into the DB. 0 disables the idle sweep.
	GhostStreamTimeoutS int

	// Agent identity
	AgentID   string
	AgentName string

	// Dashboard
	DashboardHost         string // bind address; 127.0.0.1 behind a reverse proxy
	DashboardPort         int
	DashboardRoot         string
	MaxStreams            int
	GoodMosThreshold      float64
	SilenceRatioThreshold float64

	// Database
	DBEngine         string
	DBConnection     string
	RecordsLifetimeS int
	AudioLifetimeS   int
	CSVPath          string
	// Bounded-retention options (opt-in; both default off). Useful especially
	// with an in-memory database (connection: "db=:memory:") to keep only recent
	// and/or SIP-selected calls in RAM without touching permanent storage.
	// RecordsLimit keeps at most N most-recent calls (0 = unlimited).
	RecordsLimit int
	// StoreSipFilter persists only calls whose SIP source/destination matches one
	// of these substrings (case-insensitive); empty = store every call.
	StoreSipFilter []string

	// Logging
	LogLevel   string
	LogFile    string
	LogConsole bool

	// Ops
	Pidfile string
}

// HasDatabase reports whether both a DB engine and connection are configured.
func (c *Config) HasDatabase() bool {
	return c.DBEngine != "" && c.DBConnection != ""
}

// defaults returns a Config populated with the same defaults as the Python
// VqDbConfig dataclass.
func defaults() Config {
	return Config{
		ZeroMQPort:            DefaultZeroMQPort,
		ZeroMQControlPort:     DefaultZeroMQControlPort,
		TrackResyncIntervalS:  60,
		GhostStreamTimeoutS:   120,
		DashboardHost:         "0.0.0.0",
		DashboardPort:         DefaultDashboardPort,
		MaxStreams:            50,
		GoodMosThreshold:      3.6,
		SilenceRatioThreshold: 0.8,
		LogLevel:              "info",
	}
}

var durationRe = regexp.MustCompile(`(?i)^\s*(\d+)\s*(ms|s|m|h|d)?\s*$`)

var unitSeconds = map[string]float64{
	"ms": 0.001, "s": 1, "m": 60, "h": 3600, "d": 86400,
}

// ParseDurationSeconds parses a duration like "7d" / "1h" / "10200ms" / "0s"
// into whole seconds. Empty / nil / unparseable -> 0. A bare number is seconds.
func ParseDurationSeconds(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	m := durationRe.FindStringSubmatch(fmt.Sprintf("%v", value))
	if m == nil {
		return 0
	}
	qty, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	unit := strings.ToLower(m[2])
	if unit == "" {
		unit = "s"
	}
	return int(float64(qty) * unitSeconds[unit])
}

// nestedGet walks nested maps tolerating missing intermediate dicts / nil
// values, mirroring the Python _get helper.
func nestedGet(doc any, keys ...string) any {
	cur := doc
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		v, present := m[k]
		if !present || v == nil {
			return nil
		}
		cur = v
	}
	return cur
}

// asInt coerces a YAML scalar to int, following Python int(...) semantics for
// the value types yaml.v3 produces. Falls back to def on absence/failure.
func asInt(v any, def int) int {
	switch t := v.(type) {
	case nil:
		return def
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	}
	return def
}

func asFloat(v any, def float64) float64 {
	switch t := v.(type) {
	case nil:
		return def
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case float64:
		return t
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return def
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func asBool(v any, def bool) bool {
	switch t := v.(type) {
	case nil:
		return def
	case bool:
		return t
	}
	return def
}

// asStringSlice coerces a YAML scalar or sequence into a []string, dropping
// empty entries. A bare scalar becomes a one-element slice.
func asStringSlice(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := asString(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Load reads and parses a vq-monitor.cfg file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return parse(doc), nil
}

// parse builds a Config from a decoded YAML document (map[string]any tree).
func parse(doc any) *Config {
	cfg := defaults()

	cfg.ZeroMQPort = asInt(nestedGet(doc, "server", "zeromq-port"), DefaultZeroMQPort)
	cfg.ZeroMQControlPort = asInt(nestedGet(doc, "server", "zeromq-control-port"), DefaultZeroMQControlPort)
	cfg.TrackResyncIntervalS = asInt(nestedGet(doc, "server", "track-resync-interval"), cfg.TrackResyncIntervalS)
	if v := nestedGet(doc, "server", "ghost-stream-timeout"); v != nil {
		cfg.GhostStreamTimeoutS = ParseDurationSeconds(v)
	}

	cfg.AgentID = asString(nestedGet(doc, "server", "instance", "id"))
	cfg.AgentName = asString(nestedGet(doc, "server", "instance", "name"))
	cfg.Pidfile = asString(nestedGet(doc, "server", "pidfile-db"))

	if v := asString(nestedGet(doc, "dashboard", "host")); v != "" {
		cfg.DashboardHost = v
	}
	cfg.DashboardPort = asInt(nestedGet(doc, "dashboard", "port"), DefaultDashboardPort)
	cfg.DashboardRoot = asString(nestedGet(doc, "dashboard", "root"))
	cfg.MaxStreams = asInt(nestedGet(doc, "dashboard", "max-streams"), 50)
	cfg.GoodMosThreshold = asFloat(nestedGet(doc, "dashboard", "good-mos-threshold"), 3.6)
	cfg.SilenceRatioThreshold = asFloat(nestedGet(doc, "dashboard", "silence-ratio-threshold"), 0.8)

	cfg.DBEngine = asString(nestedGet(doc, "database", "engine"))
	cfg.DBConnection = asString(nestedGet(doc, "database", "connection"))
	cfg.RecordsLifetimeS = ParseDurationSeconds(nestedGet(doc, "database", "records-lifetime"))
	cfg.AudioLifetimeS = ParseDurationSeconds(nestedGet(doc, "database", "audio-lifetime"))
	cfg.CSVPath = asString(nestedGet(doc, "database", "csv"))
	cfg.RecordsLimit = asInt(nestedGet(doc, "database", "records-limit"), 0)
	cfg.StoreSipFilter = asStringSlice(nestedGet(doc, "database", "store-sip-filter"))

	if v := nestedGet(doc, "logging", "level"); v != nil {
		cfg.LogLevel = asString(v)
	}
	cfg.LogFile = asString(nestedGet(doc, "logging", "db"))
	cfg.LogConsole = asBool(nestedGet(doc, "logging", "console"), false)

	return &cfg
}
