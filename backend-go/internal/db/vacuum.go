package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Compaction. SQLite never shrinks a database file by itself: deleted rows only
// put their pages on the freelist, where later inserts reuse them, so the file
// stays at the largest size the data ever reached. vq-db runs SQLite in
// incremental auto-vacuum mode instead. Every connection asks for it (the
// auto_vacuum pragma in the DSN), which a new database takes on before its first
// table; the cleanup worker then returns the freelist to the filesystem a few
// megabytes at a time after each sweep (IncrementalVacuum).
//
// A database created without auto-vacuum (every file from before this change)
// can only switch modes through a full VACUUM, which rewrites the file under an
// exclusive lock. ConvertToIncrementalVacuum does that once, at startup, before
// the bus subscriber starts.

// Values of PRAGMA auto_vacuum.
const (
	AutoVacuumNone        = 0
	AutoVacuumFull        = 1
	AutoVacuumIncremental = 2
)

// vacuumStepPages is how many free pages one incremental_vacuum statement
// returns to the filesystem: 8 MB at the default 4 KB page size. Each statement
// is its own write transaction, so this bounds how long the ingest writer waits
// for the lock, as deleteChunkStreams does for the deletes. A var so tests can
// shrink it.
var vacuumStepPages int64 = 2048

// VacuumStats describes one IncrementalVacuum pass, for the sweep log line.
type VacuumStats struct {
	Skipped   string        // why nothing ran ("" when it ran)
	Steps     int           // incremental_vacuum statements run
	Pages     int64         // pages returned to the filesystem
	Bytes     int64         // the same in bytes
	Remaining int64         // free pages left (budget ran out)
	MaxStep   time.Duration // longest statement, i.e. longest write-lock hold
	Total     time.Duration
}

// ConvertStats describes the outcome of ConvertToIncrementalVacuum.
type ConvertStats struct {
	Converted   bool   // the VACUUM ran
	Skipped     string // why it did not ("" when it ran or was not needed)
	BytesBefore int64
	BytesAfter  int64
	Duration    time.Duration
}

// pragmaInt reads a single-integer pragma.
func pragmaInt(q interface {
	QueryRow(string, ...any) *sql.Row
}, name string) (int64, error) {
	var n int64
	if err := q.QueryRow("PRAGMA " + name).Scan(&n); err != nil {
		return 0, fmt.Errorf("PRAGMA %s: %w", name, err)
	}
	return n, nil
}

// AutoVacuumMode returns the database's PRAGMA auto_vacuum value.
func AutoVacuumMode(conn *sql.DB) (int64, error) { return pragmaInt(conn, "auto_vacuum") }

// freeBytes is the space available to an unprivileged writer on the filesystem
// holding dir, or -1 when unknown. A var so tests can fake a full disk.
var freeBytes = func(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// ConvertToIncrementalVacuum switches a file database from auto_vacuum=NONE to
// INCREMENTAL with a one-time VACUUM. path is the database file (from
// SOCIToDSN). It does nothing when the database is already incremental, and it
// skips the VACUUM (reporting why) when the disk lacks room for it: SQLite builds
// the compacted copy in a temporary file and then writes it back through the
// WAL, so the database's filesystem must hold about twice the live data, and the
// temporary directory once more.
//
// The VACUUM holds an exclusive lock for its whole run, so call it before
// anything else uses the database.
func ConvertToIncrementalVacuum(conn *sql.DB, path string) (ConvertStats, error) {
	var st ConvertStats
	mode, err := AutoVacuumMode(conn)
	if err != nil || mode == AutoVacuumIncremental {
		return st, err
	}
	pageSize, err := pragmaInt(conn, "page_size")
	if err != nil {
		return st, err
	}
	pages, err := pragmaInt(conn, "page_count")
	if err != nil {
		return st, err
	}
	free, err := pragmaInt(conn, "freelist_count")
	if err != nil {
		return st, err
	}
	st.BytesBefore = pages * pageSize
	live := (pages - free) * pageSize
	// Headroom for the indexes VACUUM rebuilds and for the WAL around it.
	const slack = 64 << 20
	if avail := freeBytes(filepath.Dir(path)); avail >= 0 && avail < 2*live+slack {
		st.Skipped = fmt.Sprintf("needs %d MB free next to the database, %d MB available", (2*live+slack)>>20, avail>>20)
		return st, nil
	}
	if tmp := os.TempDir(); !sameFS(tmp, filepath.Dir(path)) {
		if avail := freeBytes(tmp); avail >= 0 && avail < live+slack {
			st.Skipped = fmt.Sprintf("needs %d MB free in %s, %d MB available", (live+slack)>>20, tmp, avail>>20)
			return st, nil
		}
	}

	// auto_vacuum and the VACUUM that applies it must run on one connection.
	ctx := context.Background()
	c, err := conn.Conn(ctx)
	if err != nil {
		return st, err
	}
	defer c.Close()
	start := time.Now()
	if _, err := c.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return st, err
	}
	if _, err := c.ExecContext(ctx, "VACUUM"); err != nil {
		return st, fmt.Errorf("VACUUM: %w", err)
	}
	// Fold the rewritten pages back into the main file and shrink the WAL.
	if _, err := c.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return st, err
	}
	st.Duration = time.Since(start)
	st.Converted = true
	if mode, err = AutoVacuumMode(conn); err != nil {
		return st, err
	}
	if mode != AutoVacuumIncremental {
		return st, fmt.Errorf("auto_vacuum is still %d after VACUUM", mode)
	}
	if pages, err = pragmaInt(conn, "page_count"); err != nil {
		return st, err
	}
	st.BytesAfter = pages * pageSize
	return st, nil
}

// sameFS reports whether two directories are on one filesystem (false when
// either cannot be inspected, so the caller checks both).
func sameFS(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

// IncrementalVacuum returns free pages to the filesystem, vacuumStepPages per
// statement, until the freelist is empty or budget has passed (what is left
// waits for the next sweep). Each statement is its own short write transaction,
// so the ingest writer gets the lock between steps. A database not in
// incremental mode is left alone (Skipped says so).
//
// In WAL mode the file itself shrinks when the moved pages are checkpointed; a
// passive checkpoint at the end does that unless readers still hold old
// snapshots, in which case SQLite's own autocheckpoint does it later.
func (w *Writer) IncrementalVacuum(budget time.Duration) (st VacuumStats, err error) {
	start := time.Now()
	defer func() { st.Total = time.Since(start) }()
	mode, err := AutoVacuumMode(w.conn)
	if err != nil {
		return st, err
	}
	if mode != AutoVacuumIncremental {
		st.Skipped = fmt.Sprintf("auto_vacuum is %d, not incremental", mode)
		return st, nil
	}
	pageSize, err := pragmaInt(w.conn, "page_size")
	if err != nil {
		return st, err
	}
	for {
		free, err := pragmaInt(w.conn, "freelist_count")
		if err != nil {
			return st, err
		}
		st.Remaining = free
		if free == 0 || (st.Steps > 0 && time.Since(start) >= budget) {
			break
		}
		n := min(free, vacuumStepPages)
		t := time.Now()
		if _, err := w.exec(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", n)); err != nil {
			return st, err
		}
		st.MaxStep = max(st.MaxStep, time.Since(t))
		st.Steps++
		st.Pages += n
	}
	st.Bytes = st.Pages * pageSize
	if st.Pages > 0 {
		var busy, logFrames, checkpointed int64
		if err := w.conn.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
			return st, err
		}
	}
	return st, nil
}
