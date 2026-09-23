package db

import (
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// busyRetryDelays are the pauses between attempts when a write finds the
// database locked. Each attempt already waits up to busy_timeout (5 s) inside
// SQLite, so the schedule gives a write about half a minute before it is
// dropped. The ingest writer runs on the bus goroutine, and ZeroMQ queues what
// arrives meanwhile. A variable so tests can shorten it.
var busyRetryDelays = []time.Duration{
	250 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second,
}

// isBusy reports whether err is SQLITE_BUSY or one of its extended codes
// (BUSY_SNAPSHOT 517, BUSY_RECOVERY 261, BUSY_TIMEOUT 773), which share the
// primary code in their low byte.
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// retryBusy runs op again while it fails with a busy error, pausing per
// busyRetryDelays. Only for operations that write nothing when they fail busy:
// an autocommit statement, or Begin under _txlock=immediate. Both take the
// write lock before changing anything, so a retry cannot apply a change twice.
func retryBusy(what string, op func() error) error {
	err := op()
	for attempt, delay := range busyRetryDelays {
		if !isBusy(err) {
			break
		}
		slog.Warn("database busy, retrying", "op", what, "attempt", attempt+1, "in", delay, "err", err)
		time.Sleep(delay)
		err = op()
	}
	return err
}

// exec runs an autocommit write, retrying while the database is busy.
func (w *Writer) exec(query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := retryBusy("exec", func() error {
		var err error
		res, err = w.conn.Exec(query, args...)
		return err
	})
	return res, err
}

// begin starts a write transaction, retrying while the database is busy.
// Commit is not retried: under _txlock=immediate the lock is already held.
func (w *Writer) begin() (*sql.Tx, error) {
	var tx *sql.Tx
	err := retryBusy("begin", func() error {
		var err error
		tx, err = w.conn.Begin()
		return err
	})
	return tx, err
}
