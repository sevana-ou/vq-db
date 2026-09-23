package db

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sevana-ou/vq-db/internal/model"
)

// newFileWriterShortBusy opens a file database whose connections wait only
// 20 ms for a lock, so a held write lock turns into SQLITE_BUSY quickly.
func newFileWriterShortBusy(t *testing.T) (*Writer, *sql.DB) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "t.sqlite") +
		"?_pragma=busy_timeout(20)&_pragma=journal_mode(WAL)&_txlock=immediate"
	conn, err := sql.Open(DriverSQLite, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	w := NewWriter(conn, "agent_1", "First")
	if err := w.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	return w, conn
}

// shortRetries makes the retry schedule fast for the duration of a test.
func shortRetries(t *testing.T, delays ...time.Duration) {
	t.Helper()
	saved := busyRetryDelays
	busyRetryDelays = delays
	t.Cleanup(func() { busyRetryDelays = saved })
}

// captureLog routes slog to a buffer for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(saved) })
	return &buf
}

// holdWriteLock takes the write lock on another connection and releases it
// after d, the way a cleanup chunk holds it against the ingest writer.
func holdWriteLock(t *testing.T, conn *sql.DB, d time.Duration) <-chan struct{} {
	t.Helper()
	tx, err := conn.Begin() // _txlock=immediate: the lock is taken here
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO rtpmon_property (name, value) VALUES ('lock', 'held')"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		time.Sleep(d)
		tx.Commit()
		close(done)
	}()
	return done
}

// The failure seen in production: add_interval found the lock held longer than
// busy_timeout and the interval was dropped. It must now wait and succeed.
func TestAddIntervalRetriesWhileLocked(t *testing.T) {
	w, conn := newFileWriterShortBusy(t)
	shortRetries(t, 50*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond)
	logs := captureLog(t)

	done := holdWriteLock(t, conn, 150*time.Millisecond)
	if err := w.AddInterval(model.StreamReport{StreamID: sid("lnk", 1), SevanaMOS: fptr(4.0)}); err != nil {
		t.Fatalf("AddInterval while locked: %v", err)
	}
	<-done
	if n := count(t, w, "rtpmon_intervals"); n != 1 {
		t.Errorf("intervals = %d, want 1", n)
	}
	if !strings.Contains(logs.String(), "database busy, retrying") {
		t.Errorf("no retry was logged, so the lock was never hit:\n%s", logs)
	}
}

// Transactions retry their Begin: purgeStream / TrimToLastNCalls / the cleanup
// chunks all start with one.
func TestBeginRetriesWhileLocked(t *testing.T) {
	w, conn := newFileWriterShortBusy(t)
	shortRetries(t, 50*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond)
	captureLog(t)

	done := holdWriteLock(t, conn, 120*time.Millisecond)
	tx, err := w.begin()
	if err != nil {
		t.Fatalf("begin while locked: %v", err)
	}
	tx.Rollback()
	<-done
}

// A lock held past the whole schedule still fails, with the busy error.
func TestRetryGivesUpAfterSchedule(t *testing.T) {
	w, conn := newFileWriterShortBusy(t)
	shortRetries(t, 10*time.Millisecond, 10*time.Millisecond)
	logs := captureLog(t)

	done := holdWriteLock(t, conn, 500*time.Millisecond)
	defer func() { <-done }()
	_, err := w.exec("INSERT INTO rtpmon_property (name, value) VALUES ('x', 'y')")
	if !isBusy(err) {
		t.Fatalf("err = %v, want a busy error", err)
	}
	if got := strings.Count(logs.String(), "database busy, retrying"); got != 2 {
		t.Errorf("retries logged = %d, want 2", got)
	}
}

// Anything other than a busy error returns at once.
func TestRetryIgnoresOtherErrors(t *testing.T) {
	shortRetries(t, time.Hour) // would hang the test if it retried
	calls := 0
	want := errors.New("constraint failed")
	err := retryBusy("test", func() error { calls++; return want })
	if err != want || calls != 1 {
		t.Errorf("err = %v after %d calls, want the original error after 1", err, calls)
	}
	if isBusy(nil) || isBusy(want) {
		t.Error("isBusy true for a non-SQLite error")
	}
}
