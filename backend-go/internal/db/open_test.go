package db

import (
	"path/filepath"
	"testing"
	"time"
)

// A transaction that reads before it writes, racing a commit from another
// pooled connection: the sequence the hourly cleanup hit against the ingest
// writer, failing every time with SQLITE_BUSY_SNAPSHOT (517).
func TestOpenFileTxReadThenWriteSurvivesConcurrentCommit(t *testing.T) {
	conn, err := Open("sqlite3", "db="+filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec("CREATE TABLE t (v INTEGER)"); err != nil {
		t.Fatal(err)
	}

	tx, err := conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}

	other := make(chan error, 1)
	go func() {
		_, err := conn.Exec("INSERT INTO t VALUES (1)")
		other <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the other writer reach the lock

	if _, err := tx.Exec("INSERT INTO t VALUES (2)"); err != nil {
		t.Fatalf("write after read in tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Fatalf("concurrent writer: %v", err)
	}
}

// busy_timeout must hold on every pooled connection, not just the first.
func TestOpenFileBusyTimeoutOnEveryConnection(t *testing.T) {
	conn, err := Open("sqlite3", "db="+filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx := t.Context()
	for i := range 3 {
		c, err := conn.Conn(ctx) // held, so each iteration gets a new connection
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var ms int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&ms); err != nil {
			t.Fatal(err)
		}
		if ms != 5000 {
			t.Errorf("connection %d: busy_timeout = %d, want 5000", i, ms)
		}
	}
}
