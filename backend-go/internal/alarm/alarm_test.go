package alarm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sevana-ou/vq-db/internal/config"
	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/model"
)

func cfg(counter string, limit float64) config.AlarmConfig {
	return config.AlarmConfig{Name: "t", Counter: counter, Limit: limit, HasLimit: true, IntervalS: 60, Command: "true"}
}

func TestNewRuleDefaultsAndValidation(t *testing.T) {
	r, err := NewRule(cfg("packet_loss", 5))
	if err != nil || r.Op != Above || r.Window != time.Minute || r.MinSamples != 1 || r.Unit != "%" {
		t.Errorf("packet_loss rule = %+v, %v", r, err)
	}
	r, _ = NewRule(cfg("network_mos", 3.6))
	if r.Op != Below {
		t.Errorf("network_mos op = %v", r.Op)
	}
	c := cfg("jitter", 30)
	c.Op = "below"
	if r, _ := NewRule(c); r.Op != Below {
		t.Errorf("explicit op ignored")
	}
	c = cfg("network_mos", 3)
	c.IntervalS = 0
	if r, _ := NewRule(c); r.Window != DefaultWindow {
		t.Errorf("default window = %v", r.Window)
	}
	for name, bad := range map[string]config.AlarmConfig{
		"unknown counter": cfg("nope", 1),
		"no limit":        {Name: "x", Counter: "jitter", Command: "true"},
		"no action":       {Name: "x", Counter: "jitter", Limit: 1, HasLimit: true},
		"bad op":          {Name: "x", Counter: "jitter", Limit: 1, HasLimit: true, Op: "sideways", Command: "true"},
		"bad filter":      {Name: "x", Counter: "jitter", Limit: 1, HasLimit: true, Filter: "jitter >>", Command: "true"},
		"filter on disk":  {Name: "x", Counter: "free_disk_space", Limit: 1, HasLimit: true, Filter: "jitter > 1", Command: "true"},
		"code on jitter":  {Name: "x", Counter: "jitter", Limit: 1, HasLimit: true, Code: "late_media", Command: "true"},
		"ftp webhook":     {Name: "x", Counter: "jitter", Limit: 1, HasLimit: true, Webhook: "ftp://h"},
	} {
		if _, err := NewRule(bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// fakeSource returns the value set for each call.
type fakeSource struct {
	mu   sync.Mutex
	snap Snapshot
}

func (f *fakeSource) set(s Snapshot) { f.mu.Lock(); f.snap = s; f.mu.Unlock() }
func (f *fakeSource) Measure(Rule, int64) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, nil
}

type recorder struct{ events []Event }

func (r *recorder) Notify(ev Event) { r.events = append(r.events, ev) }
func (r *recorder) kinds() string {
	var out []string
	for _, e := range r.events {
		out = append(out, string(e.Kind))
	}
	return strings.Join(out, ",")
}

func TestEngineRaiseRepeatClear(t *testing.T) {
	c := cfg("network_mos", 3.6)
	c.MinSamples = 3
	r, _ := NewRule(c)
	src := &fakeSource{}
	rec := &recorder{}
	e := NewEngine([]Rule{r}, src, Instance{ID: "i"}, rec)
	clock := time.Unix(1000, 0)
	e.now = func() time.Time { return clock }
	step := func(d time.Duration, s Snapshot) { clock = clock.Add(d); src.set(s); e.Tick() }

	step(0, Snapshot{Streams: 2, NetworkMOS: 2.0})              // too few samples: no change
	step(10*time.Second, Snapshot{Streams: 5, NetworkMOS: 2.0}) // raise
	step(10*time.Second, Snapshot{Streams: 5, NetworkMOS: 2.0}) // within the window: quiet
	step(50*time.Second, Snapshot{Streams: 5, NetworkMOS: 2.0}) // a window later: repeat
	step(10*time.Second, Snapshot{Streams: 0})                  // no traffic: state kept
	step(10*time.Second, Snapshot{Streams: 5, NetworkMOS: 4.1}) // clear
	step(10*time.Second, Snapshot{Streams: 5, NetworkMOS: 4.1}) // stays clear
	if got := rec.kinds(); got != "alarm,repeat,cleared" {
		t.Errorf("events = %s", got)
	}
	st := e.Statuses()[0]
	if st.Alarm || !st.Measured || st.Value != 4.1 {
		t.Errorf("status = %+v", st)
	}
	if rec.events[0].Value != 2.0 || rec.events[0].Samples != 5 || rec.events[0].Instance.ID != "i" {
		t.Errorf("raise event = %+v", rec.events[0])
	}
}

func TestEngineAboveCounterAndDisk(t *testing.T) {
	loss, _ := NewRule(cfg("packet_loss", 5))
	disk, _ := NewRule(cfg("free_disk_space", 0.1))
	src := &fakeSource{}
	rec := &recorder{}
	e := NewEngine([]Rule{loss, disk}, src, Instance{}, rec)
	src.set(Snapshot{Streams: 3, PacketLoss: 12, FreeDisk: 0.05})
	e.Tick()
	if got := rec.kinds(); got != "alarm,alarm" {
		t.Errorf("events = %s", got)
	}
	src.set(Snapshot{Streams: 3, PacketLoss: 1, FreeDisk: -1}) // disk unknown: kept
	e.Tick()
	if got := rec.kinds(); got != "alarm,alarm,cleared" {
		t.Errorf("events = %s", got)
	}
}

func TestCommandLineAndEnv(t *testing.T) {
	c := cfg("network_mos", 3.6)
	c.Name = "low mos; rm -rf /" // names never reach the shell line
	c.Command = "~/notify.sh $network_mos $limit $packet_loss $r_factor $free_disk_space"
	r, _ := NewRule(c)
	ev := Event{Rule: r, Kind: Raised, Value: 2.5, Samples: 4, At: time.Unix(0, 0),
		Snapshot: Snapshot{NetworkMOS: 2.5, PacketLoss: 7.25, RFactor: 61, FreeDisk: -1, Streams: 4}}
	line := CommandLine(ev)
	if !strings.HasSuffix(line, "/notify.sh 2.500 3.600 7.250 61.000 ") || strings.Contains(line, "rm -rf") {
		t.Errorf("line = %q", line)
	}
	env := strings.Join(Env(ev), "\n")
	for _, want := range []string{"VQ_ALARM_NAME=low mos; rm -rf /", "VQ_ALARM_STATE=alarm", "VQ_ALARM_VALUE=2.500",
		"VQ_ALARM_OP=below", "VQ_ALARM_SAMPLES=4", "VQ_ALARM_TIME=1970-01-01T00:00:00Z", "VQ_PACKET_LOSS=7.250"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	if strings.Contains(env, "VQ_FREE_DISK_SPACE") {
		t.Error("unknown free disk space should not be exported")
	}
}

func TestCommandNotifierRunsAndSkipsClear(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	c := NewCommandNotifier(time.Second)
	c.run = func(_ context.Context, line string, env []string) ([]byte, error) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
		return nil, nil
	}
	r, _ := NewRule(cfg("jitter", 30))
	r.Command = "echo $jitter"
	c.Notify(Event{Rule: r, Kind: Raised, Snapshot: Snapshot{Jitter: 42}})
	c.Notify(Event{Rule: r, Kind: Cleared}) // on-clear is off
	r.OnClear = true
	c.Notify(Event{Rule: r, Kind: Cleared, Snapshot: Snapshot{Jitter: 3}})
	c.Close()
	if strings.Join(lines, "|") != "echo 42.000|echo 3.000" {
		t.Errorf("ran %q", lines)
	}
}

func TestRunShellPassesEnv(t *testing.T) {
	out, err := runShell(context.Background(), `printf '%s' "$VQ_ALARM_NAME"`, []string{"VQ_ALARM_NAME=a;b"})
	if err != nil || string(out) != "a;b" {
		t.Errorf("out=%q err=%v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := runShell(ctx, "sleep 5", nil); err == nil {
		t.Error("timeout not enforced")
	}
}

// --- DBSource against a real database ---

func testDB(t *testing.T) (*sql.DB, *db.Writer) {
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
	return conn, w
}

func fp(v float64) *float64 { return &v }

func addStream(t *testing.T, w *db.Writer, link, dst string, endMs int64, net, sev float64, rf int64, rtp, lost uint64, jitter float64) {
	t.Helper()
	id := model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 4000, DstIP: "10.0.0.2", DstPort: 4002, SSRC: uint32(len(link)), LinkID: link}
	if _, err := w.OpenStream(id); err != nil {
		t.Fatal(err)
	}
	var sevp *float64
	if sev > 0 {
		sevp = fp(sev)
	}
	err := w.AddFinal(model.StreamReport{StreamID: id, StartMs: endMs - 20000, EndMs: endMs, NetworkMOS: fp(net), SevanaMOS: sevp,
		SevanaRfactor: rf, RTPPacketCounter: rtp, LostPacketCounter: lost, Jitter: jitter, FullReport: true,
		SipPeerA: "sip:a@h", SipPeerB: dst, SipCallID: "c-" + link})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDBSourceStreamKPIs(t *testing.T) {
	conn, w := testDB(t)
	now := int64(10_000_000)
	addStream(t, w, "s1", "sip:voice@h", now-60_000, 4.0, 4.2, 90, 900, 100, 10)
	addStream(t, w, "s2", "sip:voice@h", now-30_000, 2.0, 0, 0, 500, 500, 30)
	addStream(t, w, "s3", "sip:other@h", now-20_000, 3.0, 3.0, 50, 1000, 0, 20)
	addStream(t, w, "old", "sip:voice@h", now-3_600_000, 1.0, 1.0, 10, 10, 90, 99) // outside the window

	src := &DBSource{DB: conn}
	r, _ := NewRule(cfg("network_mos", 3.6))
	r.Window = 5 * time.Minute
	s, err := src.Measure(r, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Streams != 3 || s.SevanaStreams != 2 || s.NetworkMOS != 3.0 || s.SevanaMOS != 3.6 || s.RFactor != 70 {
		t.Errorf("snapshot = %+v", s)
	}
	if s.PacketLoss != 20 || s.Jitter != 20 || s.Duration != 20 { // 600 lost of 3000 expected
		t.Errorf("loss/jitter/duration = %v %v %v", s.PacketLoss, s.Jitter, s.Duration)
	}

	r.Filter = `sip_dst ~ "voice@"`
	s, err = src.Measure(r, now)
	if err != nil || s.Streams != 2 || s.NetworkMOS != 3.0 {
		t.Errorf("filtered snapshot = %+v, %v", s, err)
	}

	disk, _ := NewRule(cfg("free_disk_space", 0.1))
	if s, _ := (&DBSource{DB: conn, DiskPath: t.TempDir()}).Measure(disk, now); s.FreeDisk <= 0 || s.FreeDisk > 1 {
		t.Errorf("free disk = %v", s.FreeDisk)
	}
	if s, _ := src.Measure(disk, now); s.FreeDisk != -1 {
		t.Errorf("no disk path: free disk = %v, want -1", s.FreeDisk)
	}
}

func TestStoreNotifierAndRecentEvents(t *testing.T) {
	conn, _ := testDB(t)
	r, _ := NewRule(cfg("jitter", 30))
	st := StoreNotifier{DB: conn}
	st.Notify(Event{Rule: r, Kind: Raised, Value: 42, Samples: 3, At: time.UnixMilli(1000)})
	st.Notify(Event{Rule: r, Kind: Cleared, Value: 5, Samples: 3, At: time.UnixMilli(2000)})
	evs, err := RecentEvents(conn, 10)
	if err != nil || len(evs) != 2 || evs[0].Kind != "cleared" || evs[1].Value != 42 || evs[1].Op != "above" {
		t.Errorf("events = %+v, %v", evs, err)
	}
}

func TestDropHistoryWindowAndRestart(t *testing.T) {
	h := NewDropHistory(time.Hour)
	clock := time.Unix(0, 0)
	h.now = func() time.Time { return clock }
	stat := func(dropped, mbuf uint64) model.InstanceStatistics {
		return model.InstanceStatistics{Capturers: []model.CaptureStats{{DroppedHwPacketCounter: dropped, MbufAllocFailedCounter: mbuf}}}
	}
	if _, ok := h.Within(time.Minute); ok {
		t.Error("no statistics yet: want unknown")
	}
	h.Note(stat(100, 0)) // t=0
	clock = clock.Add(30 * time.Second)
	h.Note(stat(150, 10)) // +60
	clock = clock.Add(30 * time.Second)
	h.Note(stat(5, 0)) // vq-core restarted: +5
	clock = clock.Add(30 * time.Second)
	h.Note(stat(25, 0)) // +20
	if n, _ := h.Within(time.Hour); n != 85 {
		t.Errorf("drops in the hour = %d, want 85", n)
	}
	if n, _ := h.Within(45 * time.Second); n != 25 { // since the sample at t=60s
		t.Errorf("drops in 45 s = %d, want 25", n)
	}
}

func TestWebhookPayloadAndRetry(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway) // first attempt fails, retry succeeds
			return
		}
		var b map[string]any
		_ = json.NewDecoder(req.Body).Decode(&b)
		if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("User-Agent") != "vq-db/test" {
			t.Errorf("headers = %v", req.Header)
		}
		bodies = append(bodies, b)
	}))
	defer srv.Close()
	r, _ := NewRule(config.AlarmConfig{Name: "loss", Counter: "packet_loss", Limit: 5, HasLimit: true, Webhook: srv.URL})
	w := NewWebhookNotifier("vq-db/test")
	w.retryWait = 10 * time.Millisecond
	w.Notify(Event{Rule: r, Kind: Raised, Value: 12.5, Samples: 7, At: time.Unix(60, 0),
		Instance: Instance{ID: "agent_1"}, Snapshot: Snapshot{Streams: 7, PacketLoss: 12.5, FreeDisk: -1}})
	w.Close()
	if calls != 2 || len(bodies) != 1 {
		t.Fatalf("calls=%d bodies=%d", calls, len(bodies))
	}
	b := bodies[0]
	if b["alarm"] != "loss" || b["state"] != "alarm" || b["value"] != 12.5 || b["op"] != "above" || b["unit"] != "%" ||
		b["time"] != "1970-01-01T00:01:00Z" || b["instance"].(map[string]any)["id"] != "agent_1" {
		t.Errorf("payload = %v", b)
	}
	if _, has := b["kpis"].(map[string]any)["free_disk_space"]; has {
		t.Error("unknown free disk space should be left out")
	}
}

func silentReport(level string, n int) string {
	text := "Time; SNR; SilentCall; Status\n"
	for i := 0; i < n; i++ {
		text += fmt.Sprintf("%d.00:%d.68; 0.000; %s; Ok\n", i, i, level)
	}
	return text
}

func TestDBSourceSilentStreams(t *testing.T) {
	conn, w := testDB(t)
	now := int64(10_000_000)
	add := func(link, report string, sev float64) {
		id := model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 4000, DstIP: "10.0.0.2", DstPort: 4002, SSRC: uint32(len(link)), LinkID: link}
		w.OpenStream(id)
		var sp *float64
		if sev > 0 {
			sp = fp(sev)
		}
		if err := w.AddFinal(model.StreamReport{StreamID: id, StartMs: now - 30000, EndMs: now - 10000, NetworkMOS: fp(4),
			SevanaMOS: sp, FullReport: true, DetectorReport: report, SipPeerB: "sip:" + link + "@h"}); err != nil {
			t.Fatal(err)
		}
	}
	add("silent1", silentReport("1.000", 20), 2.6)
	add("talk1", silentReport("0.200", 20), 4.0)
	add("talk2", silentReport("0.100", 20), 4.0)
	add("talk3", silentReport("0.100", 20), 4.0)
	add("nopvqa", "", 0) // not analysed: not counted
	r, _ := NewRule(cfg("silent_streams", 20))
	src := &DBSource{DB: conn}
	s, err := src.Measure(r, now)
	if err != nil || s.SevanaStreams != 4 || s.SilentStreams != 25 {
		t.Errorf("snapshot = %+v, %v", s, err)
	}
	if v, n, ok := r.Value(s); !ok || v != 25 || n != 4 || !r.Bad(v) {
		t.Errorf("value = %v %v %v", v, n, ok)
	}
	if s2, _ := src.Measure(r, now); s2.SilentStreams != 25 || len(src.silent) != 4 { // cached
		t.Errorf("second pass = %+v, cache %d", s2, len(src.silent))
	}
}

func TestDBSourceCallCounters(t *testing.T) {
	conn, w := testDB(t)
	now := int64(10_000_000)
	start := func(id, caller string) {
		w.AddSipCallStart(model.SipCallStart{CallID: id, Timestamp: now - 20000, SetupCode: 200,
			Caller: model.SipPeer{Aor: caller}, Callee: model.SipPeer{Aor: "sip:b@h"}})
	}
	start("c1", "sip:alice@h")
	start("c2", "sip:alice@h")
	start("c3", "sip:bob@h")
	w.AddSipCallFailed(model.SipCallFailed{CallID: "c4", Timestamp: now - 15000, ResponseCode: 486,
		Caller: model.SipPeer{Aor: "sip:alice@h"}, Callee: model.SipPeer{Aor: "sip:b@h"}})
	w.AddSipCallFailed(model.SipCallFailed{CallID: "old", Timestamp: now - 3_600_000, ResponseCode: 503})

	src := &DBSource{DB: conn}
	r, _ := NewRule(cfg("failed_calls", 10))
	s, err := src.Measure(r, now)
	if err != nil || s.Calls != 4 || s.FailedCalls != 25 {
		t.Errorf("failed_calls = %+v, %v", s, err)
	}
	r.Filter = `caller ~ "alice"`
	if s, err := src.Measure(r, now); err != nil || s.Calls != 3 || int(s.FailedCalls) != 33 {
		t.Errorf("filtered failed_calls = %+v, %v", s, err)
	}

	w.AddCallHealth(model.CallHealth{CallID: "c1", Timestamp: now - 5000, StreamCount: 1,
		Warnings: []model.CallHealthWarning{{Code: "one_way_audio", Tag: "One-way audio"}}})
	w.AddCallHealth(model.CallHealth{CallID: "c2", Timestamp: now - 5000, StreamCount: 2,
		Warnings: []model.CallHealthWarning{{Code: "late_media", Tag: "Late media"}}})
	w.AddCallHealth(model.CallHealth{CallID: "c3", Timestamp: now - 5000, StreamCount: 2})
	h, _ := NewRule(cfg("call_health", 10))
	if s, err := src.Measure(h, now); err != nil || s.Calls != 3 || int(s.CallHealth) != 66 {
		t.Errorf("call_health = %+v, %v", s, err)
	}
	h.Code = "one_way_audio"
	if s, _ := src.Measure(h, now); int(s.CallHealth) != 33 {
		t.Errorf("call_health one_way_audio = %v", s.CallHealth)
	}
	h.Code, h.Filter = "", `caller ~ "bob"`
	if s, err := src.Measure(h, now); err != nil || s.Calls != 1 || s.CallHealth != 0 {
		t.Errorf("call_health for bob = %+v, %v", s, err)
	}
}

func TestDBSourceCaptureDrops(t *testing.T) {
	r, _ := NewRule(cfg("capture_drops", 0))
	src := &DBSource{}
	if s, _ := src.Measure(r, 0); s.CaptureKnown {
		t.Error("no drop history: want unknown")
	}
	src.Drops = NewDropHistory(time.Hour)
	src.Drops.Note(model.InstanceStatistics{Capturers: []model.CaptureStats{{DroppedHwPacketCounter: 3}}})
	src.Drops.Note(model.InstanceStatistics{Capturers: []model.CaptureStats{{DroppedHwPacketCounter: 10}}})
	s, _ := src.Measure(r, 0)
	if v, _, ok := r.Value(s); !ok || v != 7 || !r.Bad(v) {
		t.Errorf("capture_drops = %v %v", v, ok)
	}
}
