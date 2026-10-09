package alarm

import (
	"context"
	"database/sql"
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

	src := DBSource{DB: conn}
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
	if s, _ := (DBSource{DB: conn, DiskPath: t.TempDir()}).Measure(disk, now); s.FreeDisk <= 0 || s.FreeDisk > 1 {
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
