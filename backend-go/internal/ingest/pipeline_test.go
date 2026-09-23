package ingest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/go-zeromq/zmq4"
	"google.golang.org/protobuf/proto"

	"github.com/sevana-ou/vq-db/internal/bus"
	"github.com/sevana-ou/vq-db/internal/db"
	"github.com/sevana-ou/vq-db/internal/model"
	pb "github.com/sevana-ou/vq-db/internal/proto"
	"github.com/sevana-ou/vq-db/internal/state"
)

func newPipeline(t *testing.T) (*db.Writer, *Pipeline, *[]model.InstanceStatistics) {
	t.Helper()
	conn, err := db.Open("sqlite3", "db=:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	writer := db.NewWriter(conn, "agent_1", "First")
	if err := writer.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	var captured []model.InstanceStatistics
	p := NewPipeline(writer, nil, func(s model.InstanceStatistics) { captured = append(captured, s) })
	return writer, p, &captured
}

func fillSID(sid *pb.StreamId, uuid string) {
	sid.Src = &pb.IpAddress{Family: pb.IpAddress_IPv4, Ip: net.ParseIP("10.0.0.1").To4(), Port: 5004}
	sid.Dst = &pb.IpAddress{Family: pb.IpAddress_IPv4, Ip: net.ParseIP("10.0.0.2").To4(), Port: 5060}
	sid.Ssrc = 0x1234
	sid.Uuid = uuid
}

func streamStartBytes(t *testing.T, uuid string) []byte {
	e := &pb.Event{StreamStart: &pb.StreamStart{StreamId: &pb.StreamId{}}}
	fillSID(e.StreamStart.StreamId, uuid)
	return mustMarshal(t, e)
}

func finalBytes(t *testing.T, uuid string, mos float32) []byte {
	e := &pb.Event{StreamFinish: &pb.StreamFinish{Report: &pb.Stream{StreamId: &pb.StreamId{}, MosSevana: &pb.MetricStats{Current: mos}}}}
	fillSID(e.StreamFinish.Report.StreamId, uuid)
	return mustMarshal(t, e)
}

func intervalBytes(t *testing.T, uuid string, mos float32) []byte {
	e := &pb.Event{StreamReport: &pb.Stream{StreamId: &pb.StreamId{}, MosSevana: &pb.MetricStats{Current: mos}}}
	fillSID(e.StreamReport.StreamId, uuid)
	return mustMarshal(t, e)
}

func instanceStatsBytes(t *testing.T, uptime uint64) []byte {
	e := &pb.Event{InstanceStats: &pb.InstanceStatistics{Id: "agent_1", UptimeSeconds: uptime}}
	return mustMarshal(t, e)
}

func mustMarshal(t *testing.T, e *pb.Event) []byte {
	t.Helper()
	data, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func count(t *testing.T, w *db.Writer, table string) int {
	t.Helper()
	n, err := w.CountRows(table)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPipelineOnBytesWritesRows(t *testing.T) {
	w, p, _ := newPipeline(t)
	p.OnBytes(streamStartBytes(t, "lnk-1"))
	p.OnBytes(finalBytes(t, "lnk-1", 4.0))
	if count(t, w, "rtpmon_streams") != 1 || count(t, w, "rtpmon_statistics") != 1 {
		t.Error("rows not written")
	}
}

func TestPipelineInstanceStatsHook(t *testing.T) {
	_, p, captured := newPipeline(t)
	p.OnBytes(instanceStatsBytes(t, 42))
	if len(*captured) != 1 || (*captured)[0].UptimeSeconds != 42 {
		t.Errorf("captured = %+v", *captured)
	}
}

func TestPipelineIgnoresBadBytes(t *testing.T) {
	w, p, _ := newPipeline(t)
	p.OnBytes([]byte("\xff\xff not protobuf"))
	if count(t, w, "rtpmon_streams") != 0 {
		t.Error("bad bytes should be ignored")
	}
}

func TestPipelineFeedsRegistry(t *testing.T) {
	conn, _ := db.Open("sqlite3", "db=:memory:")
	t.Cleanup(func() { conn.Close() })
	writer := db.NewWriter(conn, "agent_1", "")
	writer.Bootstrap()
	reg := state.NewActiveStreamRegistry("agent_1", "First")
	p := NewPipeline(writer, reg, nil)

	p.OnBytes(streamStartBytes(t, "lnk-1"))
	if reg.ActiveCount() != 1 {
		t.Fatal("expected 1 active")
	}
	p.OnBytes(finalBytes(t, "lnk-1", 4.0))
	if reg.ActiveCount() != 0 || reg.SessionStarted() != 1 || reg.SessionFinished() != 1 {
		t.Errorf("active=%d started=%d finished=%d", reg.ActiveCount(), reg.SessionStarted(), reg.SessionFinished())
	}
}

func TestVqcoreRestartFinalizesGhostStreams(t *testing.T) {
	conn, _ := db.Open("sqlite3", "db=:memory:")
	t.Cleanup(func() { conn.Close() })
	writer := db.NewWriter(conn, "agent_1", "")
	writer.Bootstrap()
	reg := state.NewActiveStreamRegistry("agent_1", "First")
	p := NewPipeline(writer, reg, nil)

	p.OnBytes(streamStartBytes(t, "lnk-1"))
	p.OnBytes(intervalBytes(t, "lnk-1", 4.0))
	if reg.ActiveCount() != 1 || count(t, writer, "rtpmon_statistics") != 0 {
		t.Fatal("setup wrong")
	}
	p.OnBytes(instanceStatsBytes(t, 100))
	if reg.ActiveCount() != 1 {
		t.Fatal("baseline should not finalize")
	}
	p.OnBytes(instanceStatsBytes(t, 3))
	if reg.ActiveCount() != 0 || reg.SessionFinished() != 1 || count(t, writer, "rtpmon_statistics") != 1 {
		t.Errorf("active=%d finished=%d stats=%d", reg.ActiveCount(), reg.SessionFinished(), count(t, writer, "rtpmon_statistics"))
	}
}

func TestZmqPubSubEndToEnd(t *testing.T) {
	dir := t.TempDir()
	conn, err := db.Open("sqlite3", "db="+dir+"/vq.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	writer := db.NewWriter(conn, "agent_1", "First")
	if err := writer.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(writer, nil, nil)

	pub := zmq4.NewPub(context.Background())
	if err := pub.Listen("tcp://127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	endpoint := "tcp://" + pub.Addr().String()

	sub := bus.NewSubscriber(endpoint, p.OnBytes, nil)
	if err := sub.Start(); err != nil {
		t.Fatal(err)
	}
	defer sub.Stop(2 * time.Second)

	deadline := time.Now().Add(5 * time.Second)
	for count(t, writer, "rtpmon_statistics") == 0 && time.Now().Before(deadline) {
		pub.Send(zmq4.NewMsg(streamStartBytes(t, "lnk-1")))
		pub.Send(zmq4.NewMsg(finalBytes(t, "lnk-1", 4.0)))
		time.Sleep(50 * time.Millisecond)
	}
	if count(t, writer, "rtpmon_streams") < 1 || count(t, writer, "rtpmon_statistics") < 1 {
		t.Errorf("streams=%d stats=%d", count(t, writer, "rtpmon_streams"), count(t, writer, "rtpmon_statistics"))
	}
}
