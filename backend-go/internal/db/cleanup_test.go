package db

import (
	"math/rand"
	"slices"
	"testing"
)

// legacyExpiredStreamsQuery is the query expiredStreamsQuery replaced, kept as
// the reference for which streams a sweep must expire.
const legacyExpiredStreamsQuery = `SELECT stream_id FROM rtpmon_streams
	 WHERE stream_id NOT IN (
		SELECT stream_id FROM rtpmon_intervals WHERE end_timestamp >= ?1
		UNION SELECT stream_id FROM rtpmon_statistics WHERE end_timestamp >= ?1
		UNION SELECT stream_id FROM rtpmon_audio WHERE start_timestamp >= ?1
	 )
	 AND (
		stream_id IN (
			SELECT stream_id FROM rtpmon_intervals
			UNION SELECT stream_id FROM rtpmon_statistics
			UNION SELECT stream_id FROM rtpmon_audio
		)
		OR created_ts < ?1
		OR created_ts IS NULL
	 )`

func streamIDs(t *testing.T, w *Writer, query string, cutoff int64) []int64 {
	t.Helper()
	rows, err := w.conn.Query(query, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Random databases covering every case the legacy query distinguishes:
// created_ts before/after the cutoff or NULL, streams with and without
// reports, reports on both sides of the cutoff in each table, and orphan
// report rows whose stream no longer exists.
func TestExpiredStreamsQueryMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	expired := 0
	for trial := range 40 {
		// A subtest per trial: the shared in-memory database lives until its
		// last connection closes, which t.Cleanup does at the end of the subtest.
		t.Run("", func(t *testing.T) {
			w := newTestWriter(t)
			ts := func() int64 { return rng.Int63n(20_000) }
			for s := int64(1); s <= 60; s++ {
				var created any
				switch rng.Intn(4) {
				case 0:
					created = nil
				default:
					created = ts()
				}
				if _, err := w.conn.Exec(`INSERT INTO rtpmon_streams (stream_id, link_id, src_ip, src_port, dst_ip, dst_port, ssrc, agent_id, created_ts)
				VALUES (?, 'l', 'a', 1, 'b', 2, 3, 1, ?)`, s, created); err != nil {
					t.Fatal(err)
				}
			}
			// Report rows for existing streams (1..60) and orphans (61..70).
			for range 150 {
				sid := rng.Int63n(70) + 1
				start := ts()
				end := start + rng.Int63n(2_000)
				switch rng.Intn(3) {
				case 0, 1:
					table := []string{"rtpmon_intervals", "rtpmon_statistics"}[rng.Intn(2)]
					if _, err := w.conn.Exec(`INSERT INTO `+table+` (stream_id, start_timestamp, end_timestamp,
					rtp_packet_counter, illegal_packet_counter, lost_packet_counter, flags, link_id)
					VALUES (?, ?, ?, 0, 0, 0, 0, 'l')`, sid, start, end); err != nil {
						t.Fatal(err)
					}
				case 2:
					if _, err := w.conn.Exec(`INSERT INTO rtpmon_audio (stream_id, start_timestamp, end_timestamp, audio_wav)
					VALUES (?, ?, ?, '')`, sid, start, end); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, cutoff := range []int64{0, 5_000, 10_000, 15_000, 25_000} {
				want := streamIDs(t, w, legacyExpiredStreamsQuery, cutoff)
				got := streamIDs(t, w, expiredStreamsQuery, cutoff)
				if !slices.Equal(got, want) {
					t.Fatalf("trial %d cutoff %d: got %v, want %v", trial, cutoff, got, want)
				}
				expired += len(want)
			}
		})
	}
	if expired < 1000 {
		t.Errorf("only %d expired streams across all trials; the comparison is too weak", expired)
	}
}

// A sweep deletes in chunks of deleteChunkStreams, one transaction each.
func TestRemoveOldRecordsChunks(t *testing.T) {
	w := newTestWriter(t)
	n := 2*deleteChunkStreams + 50
	for i := range n {
		if _, err := w.OpenStreamAt(sid("old", uint32(i)), 1000); err != nil {
			t.Fatal(err)
		}
	}
	st, err := w.RemoveOldRecords(5000)
	if err != nil {
		t.Fatal(err)
	}
	if st.Streams != n || st.Chunks != 3 {
		t.Errorf("stats = %+v, want %d streams in 3 chunks", st, n)
	}
	if c := count(t, w, "rtpmon_streams"); c != 0 {
		t.Errorf("streams left = %d", c)
	}
}
