package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sevana-ou/vq-db/internal/model"
)

func mustPragma(t *testing.T, conn *sql.DB, name string) int64 {
	t.Helper()
	n, err := pragmaInt(conn, name)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fillAudio stores n audio chunks of about 16 KB each for one stream, from
// StartMs 0 upward, so RemoveOldAudio can free a known share of the pages.
func fillAudio(t *testing.T, w *Writer, n int) {
	t.Helper()
	id := sid("lnk-vac", 0x42)
	if err := w.AddFinal(model.StreamReport{StreamID: id, EndMs: 1, SevanaMOS: fptr(4), FullReport: true}); err != nil {
		t.Fatal(err)
	}
	wav := []byte(strings.Repeat("w", 12_000)) // base64 makes it 16 KB
	for i := range n {
		if err := w.AddAudio(model.StreamAudio{StreamID: id, Wav: wav, StartMs: int64(i), FinishMs: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
}

// openLegacy creates a database the way vq-db did before compaction existed
// (no auto_vacuum pragma), with the schema bootstrapped.
func openLegacy(t *testing.T, path string) {
	t.Helper()
	c, err := sql.Open(DriverSQLite, path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := Bootstrap(c); err != nil {
		t.Fatal(err)
	}
	if mode := mustPragma(t, c, "auto_vacuum"); mode != AutoVacuumNone {
		t.Fatalf("legacy auto_vacuum = %d", mode)
	}
}

func openFile(t *testing.T, path string) *Writer {
	t.Helper()
	conn, err := Open("sqlite3", "db="+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	w := NewWriter(conn, "agent", "agent")
	if err := w.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestNewDatabasesAreIncremental(t *testing.T) {
	w := openFile(t, filepath.Join(t.TempDir(), "new.sqlite"))
	if mode := mustPragma(t, w.conn, "auto_vacuum"); mode != AutoVacuumIncremental {
		t.Errorf("file auto_vacuum = %d", mode)
	}
	mem := newTestWriter(t)
	if mode := mustPragma(t, mem.conn, "auto_vacuum"); mode != AutoVacuumIncremental {
		t.Errorf(":memory: auto_vacuum = %d", mode)
	}
}

// IncrementalVacuum gives the pages of deleted rows back: the file shrinks.
func TestIncrementalVacuumShrinksFile(t *testing.T) {
	saved := vacuumStepPages
	vacuumStepPages = 64
	t.Cleanup(func() { vacuumStepPages = saved })

	w := openFile(t, filepath.Join(t.TempDir(), "v.sqlite"))
	fillAudio(t, w, 300)
	before := mustPragma(t, w.conn, "page_count")
	if _, err := w.RemoveOldAudio(250); err != nil {
		t.Fatal(err)
	}
	free := mustPragma(t, w.conn, "freelist_count")
	if free < 500 {
		t.Fatalf("freelist = %d after deleting 250 chunks", free)
	}

	st, err := w.IncrementalVacuum(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Skipped != "" || st.Pages != free || st.Remaining != 0 || st.Steps < 2 {
		t.Errorf("stats = %+v, freelist was %d", st, free)
	}
	if st.Bytes != st.Pages*mustPragma(t, w.conn, "page_size") {
		t.Errorf("bytes = %d for %d pages", st.Bytes, st.Pages)
	}
	if got := mustPragma(t, w.conn, "freelist_count"); got != 0 {
		t.Errorf("freelist after = %d", got)
	}
	if after := mustPragma(t, w.conn, "page_count"); after > before-free { // pointer-map pages may go too
		t.Errorf("page_count %d -> %d, freed %d", before, after, free)
	}
	if count(t, w, "rtpmon_audio") != 50 {
		t.Errorf("audio rows = %d", count(t, w, "rtpmon_audio"))
	}

	// Nothing left: a quiet no-op.
	if st, err := w.IncrementalVacuum(time.Minute); err != nil || st.Pages != 0 || st.Steps != 0 {
		t.Errorf("second pass = %+v, %v", st, err)
	}
}

// An exhausted budget still runs one step and leaves the rest for later.
func TestIncrementalVacuumStopsAtBudget(t *testing.T) {
	saved := vacuumStepPages
	vacuumStepPages = 16
	t.Cleanup(func() { vacuumStepPages = saved })

	w := openFile(t, filepath.Join(t.TempDir(), "b.sqlite"))
	fillAudio(t, w, 100)
	if _, err := w.RemoveOldAudio(100); err != nil {
		t.Fatal(err)
	}
	free := mustPragma(t, w.conn, "freelist_count")
	st, err := w.IncrementalVacuum(0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Steps != 1 || st.Pages != 16 || st.Remaining != free-16 {
		t.Errorf("stats = %+v, freelist was %d", st, free)
	}
}

func TestIncrementalVacuumSkipsLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	openLegacy(t, path)
	w := openFile(t, path)
	st, err := w.IncrementalVacuum(time.Minute)
	if err != nil || !strings.Contains(st.Skipped, "not incremental") || st.Steps != 0 {
		t.Errorf("stats = %+v, %v", st, err)
	}
}

// A database from before compaction is converted once, keeps its data, and
// loses its free space.
func TestConvertLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	openLegacy(t, path)
	w := openFile(t, path)
	fillAudio(t, w, 300)
	if _, err := w.RemoveOldAudio(250); err != nil {
		t.Fatal(err)
	}
	if mode := mustPragma(t, w.conn, "auto_vacuum"); mode != AutoVacuumNone {
		t.Fatalf("the DSN pragma alone changed auto_vacuum to %d", mode)
	}

	st, err := ConvertToIncrementalVacuum(w.conn, path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Converted || st.Skipped != "" || st.BytesAfter >= st.BytesBefore/2 {
		t.Errorf("stats = %+v", st)
	}
	if mode := mustPragma(t, w.conn, "auto_vacuum"); mode != AutoVacuumIncremental {
		t.Errorf("auto_vacuum = %d", mode)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != st.BytesAfter {
		t.Errorf("file size = %v (%v), want %d", fi.Size(), err, st.BytesAfter)
	}
	if count(t, w, "rtpmon_audio") != 50 {
		t.Errorf("audio rows = %d", count(t, w, "rtpmon_audio"))
	}

	// Already incremental: nothing to do.
	if st, err := ConvertToIncrementalVacuum(w.conn, path); err != nil || st.Converted || st.Skipped != "" {
		t.Errorf("second conversion = %+v, %v", st, err)
	}
}

func TestConvertSkippedWithoutDiskSpace(t *testing.T) {
	saved := freeBytes
	freeBytes = func(string) int64 { return 1 << 20 }
	t.Cleanup(func() { freeBytes = saved })

	path := filepath.Join(t.TempDir(), "full.sqlite")
	openLegacy(t, path)
	w := openFile(t, path)
	st, err := ConvertToIncrementalVacuum(w.conn, path)
	if err != nil || st.Converted || !strings.Contains(st.Skipped, "MB free") {
		t.Errorf("stats = %+v, %v", st, err)
	}
	if mode := mustPragma(t, w.conn, "auto_vacuum"); mode != AutoVacuumNone {
		t.Errorf("auto_vacuum = %d", mode)
	}
}
