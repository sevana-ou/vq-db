package db

import (
	"testing"

	"github.com/sevana-ou/vq-db/internal/model"
)

func turnTakingRow(t *testing.T, w *Writer, callID string) (ts int64, final int, parties string, found bool) {
	t.Helper()
	rows, err := w.DB().Query("select event_timestamp, final, parties from rtpmon_turn_taking where call_id = ?", callID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		if err := rows.Scan(&ts, &final, &parties); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n > 1 {
		t.Fatalf("%d rows for %s", n, callID)
	}
	return ts, final, parties, n == 1
}

func TestAddTurnTakingKeepsLatestPerCall(t *testing.T) {
	conn, err := Open("sqlite3", "db=:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	w := NewWriter(conn, "a", "A")
	if err := w.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	clock := int64(1_000_000)
	w.now = func() int64 { return clock }
	party := func(score float64) []model.TurnTakingParty {
		return []model.TurnTakingParty{{HasScore: true, Score: score, Turns: 6}, {}}
	}

	if err := w.AddTurnTaking(model.TurnTaking{CallID: "c1", Timestamp: 100, Parties: party(0.5)}); err != nil {
		t.Fatal(err)
	}
	// One second later: within the storing period, skipped.
	clock += 1000
	w.AddTurnTaking(model.TurnTaking{CallID: "c1", Timestamp: 200, Parties: party(0.6)})
	if ts, final, _, ok := turnTakingRow(t, w, "c1"); !ok || ts != 100 || final != 0 {
		t.Errorf("after a skipped update: ts=%d final=%d ok=%v", ts, final, ok)
	}
	// Past the period: replaces the row.
	clock += turnTakingEveryMs
	w.AddTurnTaking(model.TurnTaking{CallID: "c1", Timestamp: 300, Parties: party(0.7)})
	if ts, _, _, _ := turnTakingRow(t, w, "c1"); ts != 300 {
		t.Errorf("after the period: ts=%d", ts)
	}
	// The final result is stored at once, whatever the period.
	clock += 10
	w.AddTurnTaking(model.TurnTaking{CallID: "c1", Timestamp: 400, Final: true, Parties: party(0.9)})
	ts, final, parties, _ := turnTakingRow(t, w, "c1")
	if ts != 400 || final != 1 || !contains(parties, `"score":0.9`) {
		t.Errorf("final: ts=%d final=%d parties=%s", ts, final, parties)
	}
	// A stray non-final event after the final one does not replace it.
	clock += 2 * turnTakingEveryMs
	w.AddTurnTaking(model.TurnTaking{CallID: "c1", Timestamp: 500, Parties: party(0.1)})
	if ts, final, _, _ := turnTakingRow(t, w, "c1"); ts != 400 || final != 1 {
		t.Errorf("after a stray event: ts=%d final=%d", ts, final)
	}
	// Another call is independent of the first one's period.
	w.AddTurnTaking(model.TurnTaking{CallID: "c2", Timestamp: 500})
	if _, _, parties, ok := turnTakingRow(t, w, "c2"); !ok || parties != "[]" {
		t.Errorf("c2: ok=%v parties=%s", ok, parties)
	}
}

func TestSyntheticScoreColumns(t *testing.T) {
	conn, err := Open("sqlite3", "db=:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A database from before the columns existed gains them on bootstrap.
	if err := Bootstrap(conn); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rtpmon_intervals", "rtpmon_statistics"} {
		for _, col := range []string{"synthetic_score", "synthetic_speech_s"} {
			if _, err := conn.Exec("ALTER TABLE " + table + " DROP COLUMN " + col); err != nil {
				t.Fatal(err)
			}
		}
	}
	w := NewWriter(conn, "a", "A")
	if err := w.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	score := 0.18
	sid := func(link string) model.MediaStreamId {
		return model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 1, DstIP: "10.0.0.2", DstPort: 2, SSRC: 1, LinkID: link}
	}
	w.AddFinal(model.StreamReport{StreamID: sid("scored"), EndMs: 10, FullReport: true, HasSynthetic: true, SyntheticScore: &score, SyntheticSpeechS: 24.15})
	w.AddFinal(model.StreamReport{StreamID: sid("early"), EndMs: 10, FullReport: true, HasSynthetic: true, SyntheticSpeechS: 3})
	w.AddFinal(model.StreamReport{StreamID: sid("none"), EndMs: 10, FullReport: true})
	read := func(link string) (score, speech any) {
		if err := conn.QueryRow("select synthetic_score, synthetic_speech_s from rtpmon_statistics where link_id = ?", link).Scan(&score, &speech); err != nil {
			t.Fatal(err)
		}
		return
	}
	if s, sp := read("scored"); s != 0.18 || sp != 24.15 {
		t.Errorf("scored: %v %v", s, sp)
	}
	if s, sp := read("early"); s != nil || sp != 3.0 {
		t.Errorf("early: %v %v", s, sp)
	}
	if s, sp := read("none"); s != nil || sp != nil {
		t.Errorf("none: %v %v", s, sp)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
