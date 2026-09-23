package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/model"
)

func fptr(v float64) *float64 { return &v }

func seededApp(t *testing.T) *App {
	t.Helper()
	conn, err := db.Open("sqlite3", "db=:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	w := db.NewWriter(conn, "agent_1", "First")
	if err := w.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	sid := model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 5004, DstIP: "10.0.0.2", DstPort: 5060, SSRC: 0x1234, LinkID: "lnk-1"}
	w.OpenStream(sid)
	w.AddInterval(model.StreamReport{StreamID: sid, StartMs: 1000, EndMs: 11000, SevanaMOS: fptr(3.8), NetworkMOS: fptr(4.1), Jitter: 12.0, Codec: "opus", SipCallID: "c1", SipPeerA: "sip:a@h", SipPeerB: "sip:b@h", RTPPacketCounter: 500, LostPacketCounter: 3})
	w.AddFinal(model.StreamReport{StreamID: sid, StartMs: 1000, EndMs: 21000, SevanaMOS: fptr(3.7), NetworkMOS: fptr(4.0), Jitter: 10.0, Codec: "opus", FullReport: true, SipCallID: "c1", SipPeerA: "sip:a@h", SipPeerB: "sip:b@h", RTPPacketCounter: 1000, LostPacketCounter: 5})
	w.AddSipCallStart(model.SipCallStart{CallID: "c1", Timestamp: 1000, SetupCode: 200, Caller: model.SipPeer{Aor: "sip:a@h"}, Callee: model.SipPeer{Aor: "sip:b@h"}})
	w.AddSipCallEnd(model.SipCallEnd{CallID: "c1", Timestamp: 21000, Duration: 20000, ResponseCodes: []uint32{200}})

	return NewApp(Deps{
		DB: conn, AgentID: "agent_1", AgentName: "First",
		GoodMosThreshold: 3.6, SilenceRatioThreshold: 0.8, MaxStreams: 50, Version: "2.1.3",
	})
}

func getJSON(t *testing.T, app *App, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	app.Handler().ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") == "application/json" {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal %s: %v (body %q)", path, err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestVersionEndpoint(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/version")
	if code != 200 || body["version"] != "2.1.3" {
		t.Errorf("code=%d body=%v", code, body)
	}
}

func TestInstanceList(t *testing.T) {
	app := seededApp(t)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/instance_list", nil))
	var arr []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatal(err)
	}
	if len(arr) != 1 || arr[0]["id"] != "agent_1" {
		t.Errorf("got %v", arr)
	}
}

func TestServerStats(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/server_stats")
	if code != 200 || body["id"] != "agent_1" || body["name"] != "First" {
		t.Errorf("code=%d body=%v", code, body)
	}
}

func TestSummary(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/summary")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if _, ok := body["historical"]; !ok {
		t.Error("missing historical")
	}
	if body["core_available"] != false {
		t.Error("core_available should be false without snapshot")
	}
}

func TestStatsFinished(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/stats?show_finished=true&db_show_count=true")
	if code != 200 {
		t.Fatalf("code %d body %v", code, body)
	}
	if body["available_finished_streams_count"].(float64) != 1 {
		t.Errorf("count = %v", body["available_finished_streams_count"])
	}
	fs := body["finished_streams"].(map[string]any)
	streams := fs["streams"].([]any)
	if len(streams) != 1 {
		t.Fatalf("streams = %v", streams)
	}
	s0 := streams[0].(map[string]any)
	if s0["ref_id"] != "lnk-1" || s0["source"] != "10.0.0.1:5004" {
		t.Errorf("stream = %v", s0)
	}
}

func TestStatsBadFilterReturnsError(t *testing.T) {
	app := seededApp(t)
	// db_filter transport: sort/dir/off/count/<b64 of "bogus_field > 1">
	code, body := getJSON(t, app, "/stats?show_finished=true&db_filter=start_time%2Fdesc%2F0%2F50%2FYm9ndXNfZmllbGQgPiAx")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if _, ok := body["error"]; !ok {
		t.Error("expected error field for bad filter")
	}
	if body["available_finished_streams_count"].(float64) != 0 {
		t.Errorf("count = %v", body["available_finished_streams_count"])
	}
}

func TestReportEndpoint(t *testing.T) {
	app := seededApp(t)
	// interval end_timestamp is 11000
	code, body := getJSON(t, app, "/report?stream_id=lnk-1&timestamp=11000.0")
	if code != 200 {
		t.Fatalf("code %d body %v", code, body)
	}
	if body["ssrc"] != "1234" || body["source"] != "10.0.0.1:5004" {
		t.Errorf("body = %v", body)
	}
}

func TestReportMissingParams(t *testing.T) {
	app := seededApp(t)
	code, _ := getJSON(t, app, "/report?stream_id=lnk-1")
	if code != 503 {
		t.Errorf("code = %d", code)
	}
}

func TestStreamHistory(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/streamhistory?stream_id=lnk-1")
	if code != 200 {
		t.Fatalf("code %d body %v", code, body)
	}
	chunks := body["chunks"].([]any)
	if len(chunks) != 1 {
		t.Errorf("chunks = %v", chunks)
	}
	if body["codec"] != "opus" {
		t.Errorf("codec = %v", body["codec"])
	}
}

func TestStreamHistoryNotFound(t *testing.T) {
	app := seededApp(t)
	code, _ := getJSON(t, app, "/streamhistory?stream_id=nope")
	if code != 404 {
		t.Errorf("code = %d", code)
	}
}

func TestSipCalls(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/sip_calls")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if body["total_calls_count"].(float64) != 1 {
		t.Errorf("count = %v", body["total_calls_count"])
	}
	calls := body["calls"].([]any)
	c0 := calls[0].(map[string]any)
	if c0["call_id"] != "c1" || c0["outcome"] != "established" {
		t.Errorf("call = %v", c0)
	}
}

func TestSipCall(t *testing.T) {
	app := seededApp(t)
	code, body := getJSON(t, app, "/sip_call?call_id=c1")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if body["event_count"].(float64) != 2 {
		t.Errorf("event_count = %v", body["event_count"])
	}
}

func TestTrackWithoutControlReturns503(t *testing.T) {
	app := seededApp(t)
	code, _ := getJSON(t, app, "/track")
	if code != 503 {
		t.Errorf("code = %d", code)
	}
}
