package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CleanupStats describes one RemoveOldRecords pass, for the sweep log line.
type CleanupStats struct {
	Streams   int           // expired streams deleted
	Chunks    int           // write transactions used to delete them
	SipEvents int64         // SIP events deleted
	Read      time.Duration // finding the expired streams (no write lock)
	MaxChunk  time.Duration // longest chunk transaction, begin to commit
	SipDelete time.Duration // the SIP-event delete (holds the write lock)
	Total     time.Duration
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// deleteStreamsByIDs deletes streams (the rtpmon_streams parent plus their
// interval/statistics/audio children) by id, chunked to stay under SQLite's
// bound-parameter limit.
func deleteStreamsByIDs(ex execer, ids []int64) error {
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		for _, table := range []string{"rtpmon_intervals", "rtpmon_statistics", "rtpmon_audio", "rtpmon_streams"} {
			if _, err := ex.Exec(fmt.Sprintf("DELETE FROM %s WHERE stream_id IN (%s)", table, placeholders), args...); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteChunkStreams is how many streams one cleanup transaction deletes, and so
// bounds how long the ingest writer can wait for the lock. In production a
// 500-stream chunk held it for 3.4 s against a 5 s busy_timeout; each stream
// takes its intervals and statistics with it, ~6 KB of report text per row.
const deleteChunkStreams = 100

// expiredStreamsQuery selects the streams RemoveOldRecords expires: a stream
// with no report at or after the cutoff (?1) that either has a report, or was
// created before the cutoff, or has no created_ts (legacy rows).
//
// It first gathers candidates, then drops those with a report at or after the
// cutoff. Any expired stream is a candidate: if it has reports they all lie
// before the cutoff, so it has one there; if it has none it qualifies through
// created_ts. Conversely every candidate meets the second condition by the way
// it was found. The candidate sets are small: the streams table is narrow, and
// the report tables are read only below the cutoff through their timestamp
// indexes, which after hourly sweeps is about an hour of rows.
//
// The candidates must be the outer loop, with the per-stream checks run only
// for them. Written as a join with rtpmon_streams, SQLite scanned all streams
// and ran the NOT EXISTS checks on each before consulting the candidates, which
// read every report again (3m46s cold on the production copy).
//
// INDEXED BY pins the timestamp indexes. Without it the SQLite in
// modernc.org/sqlite (3.53) merged the UNION in stream_id order and so read
// each report table whole through its stream_id index (1m19s cold, 1.3 s
// warm), while the system SQLite 3.45 chose the timestamp indexes itself.
// Bootstrap creates all three.
//
// The previous form listed every stream with a report after the cutoff, nearly
// all 14 days, which read each of those ~6 KB rows: 3 minutes an hour on the
// production database. cleanup_test.go checks the two agree.
const expiredStreamsQuery = `
	WITH cand(stream_id) AS (
		SELECT stream_id FROM rtpmon_streams WHERE created_ts < ?1 OR created_ts IS NULL
		UNION SELECT stream_id FROM rtpmon_intervals INDEXED BY idx_intervals_end_timestamp
			WHERE end_timestamp < ?1
		UNION SELECT stream_id FROM rtpmon_statistics INDEXED BY idx_statistics_end_timestamp
			WHERE end_timestamp < ?1
		UNION SELECT stream_id FROM rtpmon_audio INDEXED BY idx_audio_start_timestamp
			WHERE start_timestamp < ?1
	)
	SELECT stream_id FROM cand
	WHERE EXISTS (SELECT 1 FROM rtpmon_streams s WHERE s.stream_id = cand.stream_id)
	  AND NOT EXISTS (SELECT 1 FROM rtpmon_intervals i WHERE i.stream_id = cand.stream_id AND i.end_timestamp >= ?1)
	  AND NOT EXISTS (SELECT 1 FROM rtpmon_statistics t WHERE t.stream_id = cand.stream_id AND t.end_timestamp >= ?1)
	  AND NOT EXISTS (SELECT 1 FROM rtpmon_audio a WHERE a.stream_id = cand.stream_id AND a.start_timestamp >= ?1)`

// RemoveOldRecords expires whole streams and SIP events older than the cutoff.
// Port of writer.remove_old_records: a stream is removed as a unit once it has
// no row at/after cutoff AND is itself old (has any report row, or created_ts <
// cutoff, or created_ts IS NULL = legacy). Runs purely in SQL so it is safe to
// call from the cleanup goroutine alongside the single-owner writer.
//
// The expired ids are found with a plain read, outside any transaction (in WAL
// mode a reader never blocks the writer); see expiredStreamsQuery. Only the
// deletes take the write lock, one short transaction per chunk of
// deleteChunkStreams, so the ingest writer waits at most one chunk instead of
// the whole sweep. A selected stream has had no report since the cutoff, far
// past the ghost timeout, so nothing writes to it in between.
//
// The returned stats say how long each phase held (or did not hold) the write
// lock, so a later "database is locked" in the ingest writer can be traced.
func (w *Writer) RemoveOldRecords(cutoffMs int64) (st CleanupStats, err error) {
	start := time.Now()
	defer func() { st.Total = time.Since(start) }()
	rows, err := w.conn.Query(expiredStreamsQuery, cutoffMs)
	if err != nil {
		return st, err
	}
	var oldIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return st, err
		}
		oldIDs = append(oldIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return st, err
	}
	rows.Close()
	st.Read = time.Since(start)

	for from := 0; from < len(oldIDs); from += deleteChunkStreams {
		end := min(from+deleteChunkStreams, len(oldIDs))
		t := time.Now()
		if err := w.deleteStreamsTx(oldIDs[from:end]); err != nil {
			return st, err
		}
		st.MaxChunk = max(st.MaxChunk, time.Since(t))
		st.Chunks++
		st.Streams += end - from
	}
	t := time.Now()
	res, err := w.exec("DELETE FROM rtpmon_sip_events WHERE event_timestamp < ?", cutoffMs)
	st.SipDelete = time.Since(t)
	if err != nil {
		return st, err
	}
	st.SipEvents, _ = res.RowsAffected()
	return st, nil
}

// deleteStreamsTx deletes one batch of streams in its own transaction.
func (w *Writer) deleteStreamsTx(ids []int64) error {
	tx, err := w.begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteStreamsByIDs(tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveOldAudio deletes audio chunks older than the cutoff and returns how
// many it deleted.
func (w *Writer) RemoveOldAudio(cutoffMs int64) (int64, error) {
	res, err := w.exec("DELETE FROM rtpmon_audio WHERE start_timestamp < ?", cutoffMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
