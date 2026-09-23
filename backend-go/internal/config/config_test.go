package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{"7d", 7 * 86400},
		{"1h", 3600},
		{"30m", 1800},
		{"10200ms", 10},
		{"0s", 0},
		{"45", 45}, // bare number = seconds
		{"", 0},
		{nil, 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := ParseDurationSeconds(c.in); got != c.want {
			t.Errorf("ParseDurationSeconds(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

const sampleConfig = `
logging:
    level: debug
    db: /var/log/vq-db.log
    console: false
server:
    instance:
        id: agent_1
        name: First instance
    pidfile-db: /var/vq-monitor/vq-db.pid
    ghost-stream-timeout: 90s
dashboard:
    host: 127.0.0.1
    port: 9126
    max-streams: 40
    root: /var/vq-monitor/dashboard
    good-mos-threshold: 3.7
database:
    engine: sqlite3
    connection: "db=/var/vq-monitor/vq-monitor.sqlite"
    records-lifetime: 7d
    audio-lifetime: 1d
    csv: /var/vq-monitor/vq-db.csv
`

func TestLoadConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vq-monitor.cfg")
	if err := os.WriteFile(p, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != "agent_1" {
		t.Errorf("AgentID = %q", cfg.AgentID)
	}
	if cfg.AgentName != "First instance" {
		t.Errorf("AgentName = %q", cfg.AgentName)
	}
	if cfg.Pidfile != "/var/vq-monitor/vq-db.pid" {
		t.Errorf("Pidfile = %q", cfg.Pidfile)
	}
	if cfg.DashboardHost != "127.0.0.1" {
		t.Errorf("DashboardHost = %q", cfg.DashboardHost)
	}
	if cfg.DashboardPort != 9126 {
		t.Errorf("DashboardPort = %d", cfg.DashboardPort)
	}
	if cfg.MaxStreams != 40 {
		t.Errorf("MaxStreams = %d", cfg.MaxStreams)
	}
	if cfg.DashboardRoot != "/var/vq-monitor/dashboard" {
		t.Errorf("DashboardRoot = %q", cfg.DashboardRoot)
	}
	if cfg.GoodMosThreshold != 3.7 {
		t.Errorf("GoodMosThreshold = %v", cfg.GoodMosThreshold)
	}
	if cfg.DBEngine != "sqlite3" {
		t.Errorf("DBEngine = %q", cfg.DBEngine)
	}
	if cfg.DBConnection != "db=/var/vq-monitor/vq-monitor.sqlite" {
		t.Errorf("DBConnection = %q", cfg.DBConnection)
	}
	if cfg.RecordsLifetimeS != 7*86400 {
		t.Errorf("RecordsLifetimeS = %d", cfg.RecordsLifetimeS)
	}
	if cfg.AudioLifetimeS != 86400 {
		t.Errorf("AudioLifetimeS = %d", cfg.AudioLifetimeS)
	}
	if !cfg.HasDatabase() {
		t.Error("HasDatabase = false")
	}
	if cfg.GhostStreamTimeoutS != 90 {
		t.Errorf("GhostStreamTimeoutS = %d", cfg.GhostStreamTimeoutS)
	}
	if cfg.ZeroMQPort != 9125 {
		t.Errorf("ZeroMQPort = %d", cfg.ZeroMQPort)
	}
	if cfg.ZeroMQControlPort != 9127 {
		t.Errorf("ZeroMQControlPort = %d", cfg.ZeroMQControlPort)
	}
}

func TestBoundedRetentionDefaultsOff(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vq-monitor.cfg")
	if err := os.WriteFile(p, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RecordsLimit != 0 || len(cfg.StoreSipFilter) != 0 {
		t.Errorf("bounded retention should default off: limit=%d filter=%v", cfg.RecordsLimit, cfg.StoreSipFilter)
	}
}

func TestBoundedRetentionParsed(t *testing.T) {
	cfg := `
database:
    engine: sqlite3
    connection: "db=:memory:"
    records-limit: 500
    store-sip-filter:
        - "sip:vip@"
        - "1800"
`
	p := filepath.Join(t.TempDir(), "mem.cfg")
	if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.RecordsLimit != 500 {
		t.Errorf("RecordsLimit = %d", c.RecordsLimit)
	}
	if len(c.StoreSipFilter) != 2 || c.StoreSipFilter[0] != "sip:vip@" || c.StoreSipFilter[1] != "1800" {
		t.Errorf("StoreSipFilter = %v", c.StoreSipFilter)
	}
}

func TestStoreSipFilterAcceptsScalar(t *testing.T) {
	cfg := "database:\n    engine: sqlite3\n    connection: \"db=:memory:\"\n    store-sip-filter: \"sip:vip@\"\n"
	p := filepath.Join(t.TempDir(), "s.cfg")
	if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.StoreSipFilter) != 1 || c.StoreSipFilter[0] != "sip:vip@" {
		t.Errorf("StoreSipFilter = %v", c.StoreSipFilter)
	}
}

func TestLoadConfigDefaultsWhenBlocksMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "min.cfg")
	if err := os.WriteFile(p, []byte("server:\n    instance:\n        id: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != "a" {
		t.Errorf("AgentID = %q", cfg.AgentID)
	}
	if cfg.DashboardHost != "0.0.0.0" {
		t.Errorf("DashboardHost = %q, want 0.0.0.0", cfg.DashboardHost)
	}
	if cfg.DashboardPort != 9126 {
		t.Errorf("DashboardPort = %d", cfg.DashboardPort)
	}
	if cfg.GhostStreamTimeoutS != 120 {
		t.Errorf("GhostStreamTimeoutS = %d", cfg.GhostStreamTimeoutS)
	}
	if cfg.HasDatabase() {
		t.Error("HasDatabase = true")
	}
}

func TestSevanaMosMode(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"", "auto"},
		{"    sevana-mos: auto\n", "auto"},
		{"    sevana-mos: true\n", "on"},
		{"    sevana-mos: yes\n", "on"},
		{"    sevana-mos: off\n", "off"},
		{"    sevana-mos: false\n", "off"},
		{"    sevana-mos: maybe\n", "auto"},
	} {
		p := filepath.Join(t.TempDir(), "d.cfg")
		if err := os.WriteFile(p, []byte("dashboard:\n    port: 9146\n"+c.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SevanaMos != c.want {
			t.Errorf("%q: SevanaMos = %q, want %q", c.yaml, cfg.SevanaMos, c.want)
		}
	}
}
