package db

import (
	"strings"
	"testing"
)

func TestSafeConnDescription(t *testing.T) {
	cases := []struct {
		engine, conn string
		want         string
	}{
		{"sqlite3", "db=/var/vq-monitor/vq-monitor.sqlite", "/var/vq-monitor/vq-monitor.sqlite"},
		{"sqlite3", "db=:memory:", ":memory:"},
		{"sqlite", "/tmp/x.sqlite", "/tmp/x.sqlite"},
		{"postgresql", "dbname=vq host=db.local user=u password=secret port=5433", "u@db.local:5433/vq"},
		{"mysql", "db=vq user=root host=127.0.0.1", "root@127.0.0.1/vq"},
	}
	for _, c := range cases {
		got := SafeConnDescription(c.engine, c.conn)
		if got != c.want {
			t.Errorf("SafeConnDescription(%q,%q) = %q, want %q", c.engine, c.conn, got, c.want)
		}
		if strings.Contains(got, "secret") {
			t.Errorf("SafeConnDescription leaked the password: %q", got)
		}
	}
}

func TestSOCIToDSN(t *testing.T) {
	cases := []struct {
		engine, conn string
		wantDriver   string
		wantDSN      string
		wantErr      bool
	}{
		{"sqlite3", "db=/var/vq-monitor/vq-monitor.sqlite", DriverSQLite, "/var/vq-monitor/vq-monitor.sqlite", false},
		{"sqlite3", "db=local.sqlite", DriverSQLite, "local.sqlite", false},
		{"sqlite3", "db=:memory:", DriverSQLite, ":memory:", false},
		{"sqlite", "/tmp/x.sqlite", DriverSQLite, "/tmp/x.sqlite", false},
		{"sqlite3", "db='/path with spaces/x.sqlite'", DriverSQLite, "/path with spaces/x.sqlite", false},
		{"postgresql", "dbname=vq host=db.local user=u password=p port=5433", DriverPostgres, "postgres://u:p@db.local:5433/vq", false},
		{"mysql", "db=vq user=root host=127.0.0.1", DriverMySQL, "root@tcp(127.0.0.1)/vq", false},
		{"oracle", "db=x", "", "", true},
	}
	for _, c := range cases {
		driver, dsn, err := SOCIToDSN(c.engine, c.conn)
		if c.wantErr {
			if err == nil {
				t.Errorf("SOCIToDSN(%q,%q) expected error", c.engine, c.conn)
			}
			continue
		}
		if err != nil {
			t.Errorf("SOCIToDSN(%q,%q) unexpected error: %v", c.engine, c.conn, err)
			continue
		}
		if driver != c.wantDriver || dsn != c.wantDSN {
			t.Errorf("SOCIToDSN(%q,%q) = (%q,%q), want (%q,%q)", c.engine, c.conn, driver, dsn, c.wantDriver, c.wantDSN)
		}
	}
}
