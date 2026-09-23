package db

import (
	"encoding/base64"
	"testing"

	"github.com/sevana-ou/vq-db/internal/model"
)

func newTestWriter(t *testing.T) *Writer {
	t.Helper()
	conn, err := Open("sqlite3", "db=:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	w := NewWriter(conn, "agent_1", "First")
	if err := w.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	return w
}

func sid(link string, ssrc uint32) model.MediaStreamId {
	return model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 5004, DstIP: "10.0.0.2", DstPort: 5060, SSRC: ssrc, LinkID: link}
}

func fptr(v float64) *float64 { return &v }

func count(t *testing.T, w *Writer, table string) int {
	t.Helper()
	var n int
	if err := w.conn.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenStreamInsertsRowAndCaches(t *testing.T) {
	w := newTestWriter(t)
	a, err := w.OpenStream(sid("lnk-1", 0x1234))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := w.OpenStream(sid("lnk-1", 0x1234))
	if a != b {
		t.Errorf("not idempotent: %d != %d", a, b)
	}
	if count(t, w, "rtpmon_streams") != 1 {
		t.Errorf("streams = %d", count(t, w, "rtpmon_streams"))
	}
}

func TestOpenStreamCreatesAgent(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234))
	if count(t, w, "rtpmon_instances") != 1 {
		t.Fatal("agent not created")
	}
	w.OpenStream(sid("lnk-2", 0x9999))
	if count(t, w, "rtpmon_instances") != 1 {
		t.Error("agent should be reused")
	}
}

func TestAddIntervalAndFinal(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234))
	if err := w.AddInterval(model.StreamReport{StreamID: sid("lnk-1", 0x1234), StartMs: 0, EndMs: 10000, SevanaMOS: fptr(3.5), Jitter: 9.0}); err != nil {
		t.Fatal(err)
	}
	if err := w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), StartMs: 0, EndMs: 20000, SevanaMOS: fptr(3.7), Jitter: 8.0, FullReport: true}); err != nil {
		t.Fatal(err)
	}
	if count(t, w, "rtpmon_intervals") != 1 || count(t, w, "rtpmon_statistics") != 1 {
		t.Error("interval/final counts wrong")
	}
}

func TestReportAutoOpensStream(t *testing.T) {
	w := newTestWriter(t)
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: fptr(4.0), FullReport: true})
	if count(t, w, "rtpmon_streams") != 1 || count(t, w, "rtpmon_statistics") != 1 {
		t.Error("auto-open failed")
	}
}

func TestDtxCountsPersisted(t *testing.T) {
	w := newTestWriter(t)
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), FullReport: true, DtxSid: 3, DtxCount: 40, DtxTotal: 50})
	var a, b, c int
	if err := w.conn.QueryRow("SELECT dtx_sid, dtx_count, dtx_total FROM rtpmon_statistics").Scan(&a, &b, &c); err != nil {
		t.Fatal(err)
	}
	if a != 3 || b != 40 || c != 50 {
		t.Errorf("dtx = %d/%d/%d", a, b, c)
	}
}

func TestMigrateAddsDtxColumnsToOldDB(t *testing.T) {
	w := newTestWriter(t)
	for _, table := range []string{"rtpmon_intervals", "rtpmon_statistics"} {
		for _, col := range []string{"dtx_sid", "dtx_count", "dtx_total"} {
			if _, err := w.conn.Exec("ALTER TABLE " + table + " DROP COLUMN " + col); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := migrate(w.conn); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rtpmon_intervals", "rtpmon_statistics"} {
		cols, _ := tableColumns(w.conn, table)
		if !cols["dtx_sid"] || !cols["dtx_count"] || !cols["dtx_total"] {
			t.Errorf("%s missing dtx cols", table)
		}
	}
	if err := migrate(w.conn); err != nil { // idempotent
		t.Fatal(err)
	}
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), FullReport: true, DtxSid: 1, DtxCount: 9, DtxTotal: 10})
	var total int
	w.conn.QueryRow("SELECT dtx_total FROM rtpmon_statistics").Scan(&total)
	if total != 10 {
		t.Errorf("total = %d", total)
	}
}

func TestJitterPersistedNotZeroed(t *testing.T) {
	w := newTestWriter(t)
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: fptr(4.0), Jitter: 17.5, FullReport: true})
	var jitter float64
	w.conn.QueryRow("SELECT jitter FROM rtpmon_statistics").Scan(&jitter)
	if jitter != 17.5 {
		t.Errorf("jitter = %v", jitter)
	}
}

func TestFinalPersistedRegardlessOfMos(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234))
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: nil, FullReport: true})
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: fptr(0.0), FullReport: true})
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: fptr(3.9), FullReport: true})
	if count(t, w, "rtpmon_statistics") != 3 {
		t.Errorf("count = %d", count(t, w, "rtpmon_statistics"))
	}
}

func TestSipInfoUpdateOnReport(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234))
	w.AddInterval(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SipCallID: "call-9", SipPeerA: "sip:a@h", SipPeerB: "sip:b@h"})
	var callid, src, dst string
	w.conn.QueryRow("SELECT sip_callid, sip_source, sip_destination FROM rtpmon_streams").Scan(&callid, &src, &dst)
	if callid != "call-9" || src != "sip:a@h" || dst != "sip:b@h" {
		t.Errorf("got %q/%q/%q", callid, src, dst)
	}
}

func TestAudioStoredWhenStreamMapped(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234))
	w.AddAudio(model.StreamAudio{StreamID: sid("lnk-1", 0x1234), Wav: []byte("RIFFdata"), StartMs: 1, FinishMs: 2})
	var wav string
	w.conn.QueryRow("SELECT audio_wav FROM rtpmon_audio").Scan(&wav)
	decoded, _ := base64.StdEncoding.DecodeString(wav)
	if string(decoded) != "RIFFdata" {
		t.Errorf("got %q", decoded)
	}
}

func TestAudioDroppedWhenStreamUnmapped(t *testing.T) {
	w := newTestWriter(t)
	w.AddAudio(model.StreamAudio{StreamID: sid("lnk-1", 0x1234), Wav: []byte("x"), StartMs: 1, FinishMs: 2})
	if count(t, w, "rtpmon_audio") != 0 {
		t.Error("audio should be dropped")
	}
}

func TestSipEventsAllTypes(t *testing.T) {
	w := newTestWriter(t)
	w.AddSipCallStart(model.SipCallStart{CallID: "c", Timestamp: 1, SetupCode: 200, Caller: model.SipPeer{Aor: "sip:a@h"}, Callee: model.SipPeer{Aor: "sip:b@h"}})
	w.AddSipCallEnd(model.SipCallEnd{CallID: "c", Timestamp: 9, Duration: 8, ResponseCodes: []uint32{180, 200}})
	w.AddSipReinvite(model.SipReinvite{CallID: "c", Timestamp: 5, IsRequest: true, UpdatedPeer: model.SipPeer{Aor: "sip:a@h"}})
	w.AddSipCallFailed(model.SipCallFailed{CallID: "d", Timestamp: 2, ResponseCode: 486, ReasonPhrase: "Busy"})
	if count(t, w, "rtpmon_sip_events") != 4 {
		t.Fatalf("events = %d", count(t, w, "rtpmon_sip_events"))
	}
	var endCodes, reinvitePeer string
	w.conn.QueryRow("SELECT response_codes FROM rtpmon_sip_events WHERE event_type = 2").Scan(&endCodes)
	w.conn.QueryRow("SELECT caller FROM rtpmon_sip_events WHERE event_type = 3").Scan(&reinvitePeer)
	if endCodes != "180,200" || reinvitePeer != "sip:a@h" {
		t.Errorf("codes=%q peer=%q", endCodes, reinvitePeer)
	}
}

func TestGetStreamCountCountsFinished(t *testing.T) {
	w := newTestWriter(t)
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: fptr(4.0), FullReport: true})
	w.AddInterval(model.StreamReport{StreamID: sid("lnk-1", 0x1234), SevanaMOS: fptr(4.0)})
	n, _ := w.GetStreamCount()
	if n != 1 {
		t.Errorf("count = %d", n)
	}
}

func TestCleanupOldAudio(t *testing.T) {
	w := newTestWriter(t)
	w.AddFinal(model.StreamReport{StreamID: sid("lnk-1", 0x1234), StartMs: 0, EndMs: 9000, SevanaMOS: fptr(4.0), FullReport: true})
	w.AddAudio(model.StreamAudio{StreamID: sid("lnk-1", 0x1234), Wav: []byte("old"), StartMs: 500, FinishMs: 600})
	w.AddAudio(model.StreamAudio{StreamID: sid("lnk-1", 0x1234), Wav: []byte("new"), StartMs: 8000, FinishMs: 8100})
	n, err := w.RemoveOldAudio(5000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	if count(t, w, "rtpmon_audio") != 1 {
		t.Errorf("audio = %d", count(t, w, "rtpmon_audio"))
	}
}

func TestCleanupRemovesWholeExpiredStreamAndSip(t *testing.T) {
	w := newTestWriter(t)
	old := sid("old", 0xA1)
	nw := sid("new", 0xB2)
	w.AddFinal(model.StreamReport{StreamID: old, StartMs: 0, EndMs: 1000, SevanaMOS: fptr(4.0), FullReport: true})
	w.AddInterval(model.StreamReport{StreamID: old, StartMs: 0, EndMs: 900, SevanaMOS: fptr(4.0)})
	w.AddAudio(model.StreamAudio{StreamID: old, Wav: []byte("o"), StartMs: 100, FinishMs: 200})
	w.AddFinal(model.StreamReport{StreamID: nw, StartMs: 0, EndMs: 9000, SevanaMOS: fptr(4.0), FullReport: true})
	w.AddSipCallStart(model.SipCallStart{CallID: "old", Timestamp: 1000, Caller: model.SipPeer{Aor: "sip:a@h"}, Callee: model.SipPeer{Aor: "sip:b@h"}})
	w.AddSipCallStart(model.SipCallStart{CallID: "new", Timestamp: 9000, Caller: model.SipPeer{Aor: "sip:a@h"}, Callee: model.SipPeer{Aor: "sip:b@h"}})

	st, err := w.RemoveOldRecords(5000)
	if err != nil {
		t.Fatal(err)
	}
	if st.Streams != 1 || st.Chunks != 1 || st.SipEvents != 1 {
		t.Errorf("stats = %+v, want 1 stream in 1 chunk and 1 SIP event", st)
	}
	if st.Total < st.Read || st.Total < st.MaxChunk {
		t.Errorf("stats = %+v: total shorter than a phase", st)
	}
	if count(t, w, "rtpmon_streams") != 1 || count(t, w, "rtpmon_statistics") != 1 ||
		count(t, w, "rtpmon_intervals") != 0 || count(t, w, "rtpmon_audio") != 0 ||
		count(t, w, "rtpmon_sip_events") != 1 {
		t.Error("cleanup did not remove expired stream/sip correctly")
	}
}

func TestCleanupKeepsFreshlyOpenedStreamWithoutReports(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234)) // created_ts ~ now
	w.RemoveOldRecords(5000)
	if count(t, w, "rtpmon_streams") != 1 {
		t.Error("fresh stream swept")
	}
}

func TestCleanupRemovesChildlessStreamPastCreatedTs(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStreamAt(sid("stale", 0xC3), 1000)
	w.OpenStreamAt(sid("fresh", 0xD4), 9000)
	w.RemoveOldRecords(5000)
	if count(t, w, "rtpmon_streams") != 1 {
		t.Errorf("streams = %d", count(t, w, "rtpmon_streams"))
	}
}

func TestCleanupRemovesLegacyNullCreatedTsOrphan(t *testing.T) {
	w := newTestWriter(t)
	w.OpenStream(sid("lnk-1", 0x1234))
	w.conn.Exec("UPDATE rtpmon_streams SET created_ts = NULL")
	w.RemoveOldRecords(5000)
	if count(t, w, "rtpmon_streams") != 0 {
		t.Errorf("streams = %d", count(t, w, "rtpmon_streams"))
	}
}

func addFinalCall(t *testing.T, w *Writer, link string, ssrc uint32, endMs int64, callid, peer string) {
	t.Helper()
	if err := w.AddFinal(model.StreamReport{
		StreamID: sid(link, ssrc), StartMs: 0, EndMs: endMs, SevanaMOS: fptr(4.0),
		FullReport: true, SipCallID: callid, SipPeerA: peer,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordsLimitEvictsOldestCalls(t *testing.T) {
	w := newTestWriter(t)
	w.SetRecordsLimit(2)
	addFinalCall(t, w, "l1", 1, 1000, "c1", "sip:a@h")
	addFinalCall(t, w, "l2", 2, 2000, "c2", "sip:a@h")
	addFinalCall(t, w, "l3", 3, 3000, "c3", "sip:a@h")
	if count(t, w, "rtpmon_statistics") != 2 || count(t, w, "rtpmon_streams") != 2 {
		t.Fatalf("stats=%d streams=%d", count(t, w, "rtpmon_statistics"), count(t, w, "rtpmon_streams"))
	}
	// The oldest call (c1) must be the one evicted.
	var remaining string
	rows, _ := w.conn.Query("SELECT sip_callid FROM rtpmon_streams ORDER BY sip_callid")
	defer rows.Close()
	for rows.Next() {
		var c string
		rows.Scan(&c)
		remaining += c
	}
	if remaining != "c2c3" {
		t.Errorf("remaining calls = %q, want c2c3", remaining)
	}
}

func TestStoreSipFilterKeepsOnlyMatching(t *testing.T) {
	w := newTestWriter(t)
	w.SetStoreSipFilter([]string{"VIP"}) // case-insensitive
	w.OpenStream(sid("m", 1))
	w.OpenStream(sid("n", 2))
	addFinalCall(t, w, "m", 1, 1000, "c1", "sip:vip@host")
	addFinalCall(t, w, "n", 2, 1000, "c2", "sip:bob@host")
	if count(t, w, "rtpmon_statistics") != 1 || count(t, w, "rtpmon_streams") != 1 {
		t.Fatalf("stats=%d streams=%d", count(t, w, "rtpmon_statistics"), count(t, w, "rtpmon_streams"))
	}
	var callid string
	w.conn.QueryRow("SELECT sip_callid FROM rtpmon_streams").Scan(&callid)
	if callid != "c1" {
		t.Errorf("kept call = %q, want c1", callid)
	}
}

func TestStoreSipFilterPurgesNonMatchWithEarlyIntervals(t *testing.T) {
	w := newTestWriter(t)
	w.SetStoreSipFilter([]string{"vip"})
	s := sid("s", 1)
	w.OpenStream(s)
	// Early interval with no SIP correlation yet -> stored.
	if err := w.AddInterval(model.StreamReport{StreamID: s, StartMs: 0, EndMs: 1000, SevanaMOS: fptr(4.0)}); err != nil {
		t.Fatal(err)
	}
	if count(t, w, "rtpmon_intervals") != 1 {
		t.Fatal("early interval should be stored")
	}
	// Final reveals a non-matching SIP -> the whole stream is purged.
	addFinalCall(t, w, "s", 1, 2000, "c", "sip:bob@h")
	if count(t, w, "rtpmon_intervals") != 0 || count(t, w, "rtpmon_statistics") != 0 || count(t, w, "rtpmon_streams") != 0 {
		t.Errorf("non-match not fully purged: intervals=%d stats=%d streams=%d",
			count(t, w, "rtpmon_intervals"), count(t, w, "rtpmon_statistics"), count(t, w, "rtpmon_streams"))
	}
}

func TestStoreSipFilterSkipsNonMatchInterval(t *testing.T) {
	w := newTestWriter(t)
	w.SetStoreSipFilter([]string{"vip"})
	s := sid("s", 1)
	w.OpenStream(s)
	// Interval already carries a non-matching SIP -> skipped outright.
	if err := w.AddInterval(model.StreamReport{StreamID: s, EndMs: 1000, SevanaMOS: fptr(4.0), SipPeerA: "sip:bob@h"}); err != nil {
		t.Fatal(err)
	}
	if count(t, w, "rtpmon_intervals") != 0 {
		t.Errorf("known non-match interval should be skipped")
	}
}

func TestSipFilterAndRecordsLimitCombined(t *testing.T) {
	w := newTestWriter(t)
	w.SetStoreSipFilter([]string{"vip"})
	w.SetRecordsLimit(2)
	// Three matching calls + one non-matching.
	addFinalCall(t, w, "l1", 1, 1000, "c1", "sip:vip1@h")
	addFinalCall(t, w, "x", 9, 1500, "cx", "sip:bob@h") // non-match: dropped
	addFinalCall(t, w, "l2", 2, 2000, "c2", "sip:vip2@h")
	addFinalCall(t, w, "l3", 3, 3000, "c3", "sip:vip3@h")
	// Only the 2 most-recent MATCHING calls remain (c2, c3); c1 evicted, cx never stored.
	if count(t, w, "rtpmon_statistics") != 2 {
		t.Fatalf("stats=%d", count(t, w, "rtpmon_statistics"))
	}
	var remaining string
	rows, _ := w.conn.Query("SELECT sip_callid FROM rtpmon_streams ORDER BY sip_callid")
	defer rows.Close()
	for rows.Next() {
		var c string
		rows.Scan(&c)
		remaining += c
	}
	if remaining != "c2c3" {
		t.Errorf("remaining = %q, want c2c3", remaining)
	}
}

func TestPropertySetGet(t *testing.T) {
	w := newTestWriter(t)
	if _, ok, _ := w.GetProperty("x"); ok {
		t.Error("x should be absent")
	}
	w.SetProperty("x", "1")
	if v, _, _ := w.GetProperty("x"); v != "1" {
		t.Errorf("x = %q", v)
	}
	w.SetProperty("x", "2")
	if v, _, _ := w.GetProperty("x"); v != "2" {
		t.Errorf("x = %q", v)
	}
}
