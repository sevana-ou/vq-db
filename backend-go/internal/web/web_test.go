package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sevana-ou/vq-db/internal/api"
	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/model"
	"github.com/sevana-ou/vq-db/internal/state"
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

	return NewApp(api.Deps{
		DB: conn, AgentID: "agent_1", AgentName: "First",
		GoodMosThreshold: 3.6, SilenceRatioThreshold: 0.8, MaxStreams: 50, Version: "2.1.3",
	})
}

func get(t *testing.T, app *App, path string, hdr map[string]string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	app.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestSummaryPageRenders(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/summary", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"<!doctype html>", "Active streams", "Last 1 hour", "Last 24 hours", "Core offline"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary page missing %q", want)
		}
	}
}

// TestCorePageRendersUint64Fields guards against a regression where toI64/toF64
// had no uint64 case, so every uint64 core field (uptime, memory, counters)
// rendered as 0 even when vq-core reported real values.
func TestCorePageRendersUint64Fields(t *testing.T) {
	snap := state.NewInstanceSnapshot()
	snap.Set(model.InstanceStatistics{
		Version:              "vq-core-test-9",
		UptimeSeconds:        3661, // 1h 1m
		MemAllocatedBytes:    314572800,
		MemHeapSize:          524288000, // 500 MB — gates the tcmalloc row
		ActiveDecoderCounter: 42,
		SipCallCounter:       5,
	})
	app := NewApp(api.Deps{AgentID: "agent_1", AgentName: "First", Version: "test", Snapshot: snap})
	code, body := get(t, app, "/ui/core", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	// uint64 fields must render their real values, not 0 (regression: toI64/toF64
	// lacked a uint64 case). Grouped rows mirror vq-core's loadmonitor.
	for _, want := range []string{"Core status", "Core online", "vq-core-test-9", "1h 1m", "42 / 5", "500 MB"} {
		if !strings.Contains(body, want) {
			t.Errorf("core page missing %q (uint64 field likely rendering as 0)", want)
		}
	}
}

// TestCorePageHidesUnreportedRows verifies the loadmonitor-style conditional
// rows: with no CPU and no tcmalloc heap reported, those rows are omitted.
func TestCorePageHidesUnreportedRows(t *testing.T) {
	snap := state.NewInstanceSnapshot()
	snap.Set(model.InstanceStatistics{Version: "vq-core-idle", UptimeSeconds: 60})
	app := NewApp(api.Deps{AgentID: "agent_1", AgentName: "First", Version: "test", Snapshot: snap})
	code, body := get(t, app, "/ui/core", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, unwanted := range []string{"CPU usage", "tcmalloc"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("core page should omit %q when vq-core does not report it", unwanted)
		}
	}
}

func TestStreamsPageRenders(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/streams", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"Active Streams : 0", "Finished Streams : 1", "10.0.0.1:5004", "/ui/stream/lnk-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("streams page missing %q", want)
		}
	}
}

func TestStreamsPartialRender(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/streams", map[string]string{"HX-Request": "true", "HX-Target": "tbl-f"})
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Error("partial render should not include the layout")
	}
	if !strings.Contains(body, "Finished Streams : 1") {
		t.Error("partial render missing finished card body")
	}
	if strings.Contains(body, "Active Streams") {
		t.Error("tbl-f partial should not include the active card")
	}
}

func TestStreamsFilterError(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/streams?f_q="+"bogus_field+%3E+1", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(body, "error-banner") {
		t.Error("bad filter should render an error banner")
	}
}

func TestStreamDetailRenders(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/stream/lnk-1", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"Statistics", "opus", "10.0.0.1:5004", "Stream chunks", "/audio?link_id=lnk-1", "/ui/sip-call/c1"} {
		if !strings.Contains(body, want) {
			t.Errorf("stream detail missing %q", want)
		}
	}
}

func TestStreamDetailNotFound(t *testing.T) {
	app := seededApp(t)
	code, _ := get(t, app, "/ui/stream/nope", nil)
	if code != 404 {
		t.Errorf("code = %d", code)
	}
}

func TestChunkDetailRenders(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/chunk/lnk-1/11000", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"Chunk statistics", "1234", "Detectors report"} {
		if !strings.Contains(body, want) {
			t.Errorf("chunk detail missing %q", want)
		}
	}
}

func TestChunkInlinePartial(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/chunk/lnk-1/11000", map[string]string{"HX-Request": "true", "HX-Target": "chunk-inline"})
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(body, "<!doctype html>") || strings.Contains(body, "permanent link") {
		t.Error("inline chunk partial should be the card only")
	}
	if !strings.Contains(body, "Chunk statistics") {
		t.Error("inline chunk partial missing card")
	}
}

func TestSipCallsPageRenders(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/sip-calls", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"SIP Calls : 1", "sip:a@h", "/ui/sip-call/c1", "Ok"} {
		if !strings.Contains(body, want) {
			t.Errorf("sip calls page missing %q", want)
		}
	}
}

func TestSipCallDetailRenders(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/sip-call/c1", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"Call summary", "Established", "Event timeline", "Related RTP streams", "/ui/stream/lnk-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("sip call detail missing %q", want)
		}
	}
}

func TestTrackPageUnavailableWithoutControl(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/track", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(body, "control socket unavailable") {
		t.Error("track page should show the unavailable state")
	}
}

func TestStaticAssetsServed(t *testing.T) {
	app := seededApp(t)
	for _, p := range []string{"/ui/static/app.css", "/ui/static/app.js", "/ui/static/htmx.min.js"} {
		code, body := get(t, app, p, nil)
		if code != 200 || len(body) == 0 {
			t.Errorf("%s: code=%d len=%d", p, code, len(body))
		}
	}
}

func TestUIRootRedirects(t *testing.T) {
	app := seededApp(t)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/ui/summary" {
		t.Errorf("code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestForwardedPrefixInLinks(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/summary", map[string]string{"X-Forwarded-Prefix": "/beta"})
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(body, `href="/beta/ui/streams"`) {
		t.Error("nav links should carry the forwarded prefix")
	}
}

func TestPagerLimitAndOffset(t *testing.T) {
	app := seededApp(t)
	code, body := get(t, app, "/ui/streams?f_limit=10&f_offset=0", nil)
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(body, "1–1 of 1") {
		t.Error("pager range text missing")
	}
}

func TestMosView(t *testing.T) {
	if p := mosView(0.0, true); p != nil {
		t.Error("hideZero should suppress zero")
	}
	cases := []struct {
		v    float64
		band string
	}{
		{4.5, "excellent"}, {4.3, "excellent"}, {4.1, "good"}, {3.8, "fair"},
		{3.3, "poor"}, {2.0, "bad"}, {0.0, "bad"},
	}
	for _, c := range cases {
		p := mosView(c.v, false)
		if p == nil || p.Band != c.band {
			t.Errorf("mosView(%v) = %+v, want band %s", c.v, p, c.band)
		}
	}
	if p := mosView(3.85, false); p.Text != "3.85" {
		t.Errorf("Text = %s", p.Text)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := fmtInt(int64(1234567)); got != "1,234,567" {
		t.Errorf("fmtInt = %s", got)
	}
	if got := fmtBytes(int64(1536)); got != "1.5 KB" {
		t.Errorf("fmtBytes = %s", got)
	}
	if got := fmtBytes(0); got != "0 B" {
		t.Errorf("fmtBytes(0) = %s", got)
	}
	if got := fmtUptime(int64(90061)); got != "1d 1h" {
		t.Errorf("fmtUptime = %s", got)
	}
	if got := fmtUptime(int64(303)); got != "5m 3s" {
		t.Errorf("fmtUptime(303) = %s", got)
	}
	if got := sipDur(int64(83000)); got != "1:23" {
		t.Errorf("sipDur = %s", got)
	}
	if got := sipDur(int64(3723000)); got != "1:02:03" {
		t.Errorf("sipDur(h) = %s", got)
	}
	if got := fmtPct(0.5); got != "50.0%" {
		t.Errorf("fmtPct = %s", got)
	}
	if got := fmtPct(0.05); got != "5.00%" {
		t.Errorf("fmtPct small = %s", got)
	}
}

func TestParseDetectorReport(t *testing.T) {
	tbl := parseDetectorReport("Name;Count\nEcho;3\nNoise;1\n")
	if len(tbl.Header) != 2 || tbl.Header[0] != "Name" {
		t.Errorf("header = %v", tbl.Header)
	}
	if len(tbl.Body) != 2 || tbl.Body[0][0] != "Echo" {
		t.Errorf("body = %v", tbl.Body)
	}
}

func TestMapToMarkdownSemicolonTable(t *testing.T) {
	md := mapToMarkdown(map[string]any{
		"codec":  "opus",
		"report": "Name;Count\nEcho;3",
	}, "Title")
	if !strings.Contains(md, "# Title") || !strings.Contains(md, "- **codec**: opus") {
		t.Errorf("md = %s", md)
	}
	if !strings.Contains(md, "| Name | Count |") || !strings.Contains(md, "| --- | --- |") {
		t.Errorf("semicolon table not rendered: %s", md)
	}
}

func TestCopyJSONPrefersJsonReport(t *testing.T) {
	if got := copyJSON(map[string]any{"json_report": "RAW", "x": 1}); got != "RAW" {
		t.Errorf("copyJSON = %q", got)
	}
	got := copyJSON(map[string]any{"x": int64(1)})
	if !strings.Contains(got, `"x": 1`) {
		t.Errorf("copyJSON fallback = %q", got)
	}
}

// sevanaPages are the pages that show Sevana MOS figures when PVQA is present.
var sevanaPages = []string{"/ui/summary", "/ui/streams", "/ui/stream/lnk-1", "/ui/chunk/lnk-1/11000", "/ui/sip-call/c1", "/ui/core", "/ui/track"}

// withSnapshot returns the seeded app with vq-core reporting version and the
// dashboard.sevana-mos mode.
func withSnapshot(t *testing.T, version, mode string) *App {
	t.Helper()
	app := seededApp(t)
	snap := state.NewInstanceSnapshot()
	snap.Set(model.InstanceStatistics{Version: version})
	app.deps.Snapshot = snap
	app.deps.SevanaMos = mode
	return app
}

func TestSevanaHiddenForEngineWithoutPvqa(t *testing.T) {
	app := withSnapshot(t, "Server v1.9.1 / build number 13914 / network analysis only (no PVQA)", "auto")
	for _, path := range sevanaPages {
		code, body := get(t, app, path, nil)
		if code != 200 {
			t.Fatalf("%s: code %d", path, code)
		}
		// Labels only: the Copy JSON / Markdown payloads carry the API's fields.
		for _, bad := range []string{"Sevana MOS", "Sevana good", "Sevana Rfactor", "PVQA coverage", "PVQA instances", "Detectors report", "Impairments", "sevana_mos &gt;", "Avg R-factor", "Avg duration"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s shows %q for an engine without PVQA", path, bad)
			}
		}
	}
	// Network MOS stays.
	for _, path := range []string{"/ui/summary", "/ui/streams", "/ui/stream/lnk-1"} {
		if _, body := get(t, app, path, nil); !strings.Contains(body, "Network MOS") && !strings.Contains(body, "Network good") {
			t.Errorf("%s lost its network MOS", path)
		}
	}
}

func TestSevanaShownForPvqaEngineAndByOverride(t *testing.T) {
	cases := []struct{ version, mode string }{
		{"Server v1.9.1 / build number 13914 / PVQA v1.8.2", "auto"},
		{"Server v1.9.1 / build number 13914 / network analysis only (no PVQA)", "on"},
	}
	for _, c := range cases {
		app := withSnapshot(t, c.version, c.mode)
		for _, path := range []string{"/ui/summary", "/ui/streams", "/ui/stream/lnk-1", "/ui/sip-call/c1"} {
			if _, body := get(t, app, path, nil); !strings.Contains(body, "Sevana") {
				t.Errorf("%q/%s: %s does not show Sevana MOS", c.version, c.mode, path)
			}
		}
	}
	// "off" hides it even for a PVQA engine.
	app := withSnapshot(t, "Server v1.9.1 / PVQA v1.8.2", "off")
	if _, body := get(t, app, "/ui/streams", nil); strings.Contains(body, "Sevana MOS") {
		t.Error("sevana-mos: off still shows the Sevana MOS column")
	}
}
