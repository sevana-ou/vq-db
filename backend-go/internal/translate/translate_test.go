package translate

import (
	"math"
	"net"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/sevana-ou/vq-db/internal/model"
	pb "github.com/sevana-ou/vq-db/internal/proto"
)

func fillStreamID(msg *pb.StreamId) {
	msg.Src = &pb.IpAddress{Family: pb.IpAddress_IPv4, Ip: net.ParseIP("10.0.0.1").To4(), Port: 5004}
	msg.Dst = &pb.IpAddress{Family: pb.IpAddress_IPv4, Ip: net.ParseIP("10.0.0.2").To4(), Port: 5060}
	msg.Ssrc = 0x1234
	msg.Uuid = "lnk-1"
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestStreamStartDecodesToMediaStreamId(t *testing.T) {
	e := &pb.Event{StreamStart: &pb.StreamStart{StreamId: &pb.StreamId{}}}
	fillStreamID(e.StreamStart.StreamId)
	out := DecodeEvent(e)
	want := model.MediaStreamId{SrcIP: "10.0.0.1", SrcPort: 5004, DstIP: "10.0.0.2", DstPort: 5060, SSRC: 0x1234, LinkID: "lnk-1"}
	if out != want {
		t.Errorf("got %+v want %+v", out, want)
	}
}

func TestIPv6Decoding(t *testing.T) {
	e := &pb.Event{StreamStart: &pb.StreamStart{StreamId: &pb.StreamId{
		Src: &pb.IpAddress{Family: pb.IpAddress_IPv6, Ip: net.ParseIP("2001:db8::1"), Port: 5004},
		Dst: &pb.IpAddress{Family: pb.IpAddress_IPv6, Ip: net.ParseIP("2001:db8::2")},
	}}}
	out := DecodeEvent(e).(model.MediaStreamId)
	if out.SrcIP != "2001:db8::1" || out.DstIP != "2001:db8::2" {
		t.Errorf("got src=%q dst=%q", out.SrcIP, out.DstIP)
	}
}

func TestStreamReportInterval(t *testing.T) {
	s := &pb.Stream{StreamId: &pb.StreamId{}}
	fillStreamID(s.StreamId)
	s.StartTimestamp = 1000
	s.FinishTimestamp = 11000
	s.RtpPacketCounter = 500
	s.RtpLostPacketCounter = 3
	s.MosSevana = &pb.MetricStats{Current: 3.8}
	s.MosNetwork = &pb.MetricStats{Current: 4.1}
	s.SevanaRfactor = 85
	s.Jitter = &pb.MetricStats{Current: 12.5}
	s.Codecname = "opus"
	s.Sip = &pb.Stream_SipInfo{CallId: "call-1", Peer1: "sip:a@h", Peer2: "sip:b@h"}
	s.DtxInfo = &pb.Stream_DtxInfo{CountSid: 4, CountDtx: 30, CountTotal: 50}
	e := &pb.Event{StreamReport: s}

	out, ok := DecodeEvent(e).(model.StreamReport)
	if !ok {
		t.Fatalf("not a StreamReport")
	}
	if out.FullReport {
		t.Error("interval should not be full")
	}
	if out.DtxSid != 4 || out.DtxCount != 30 || out.DtxTotal != 50 {
		t.Errorf("dtx = %d/%d/%d", out.DtxSid, out.DtxCount, out.DtxTotal)
	}
	if !approx(*out.SevanaMOS, 3.8) || !approx(*out.NetworkMOS, 4.1) {
		t.Errorf("mos = %v/%v", *out.SevanaMOS, *out.NetworkMOS)
	}
	if out.SevanaRfactor != 85 || !approx(out.Jitter, 12.5) {
		t.Errorf("rfactor=%d jitter=%v", out.SevanaRfactor, out.Jitter)
	}
	if out.Codec != "opus" || out.SipCallID != "call-1" {
		t.Errorf("codec=%q callid=%q", out.Codec, out.SipCallID)
	}
}

func TestStreamFinishIsFullReport(t *testing.T) {
	s := &pb.Stream{StreamId: &pb.StreamId{}, MosSevana: &pb.MetricStats{Current: 4.0}}
	fillStreamID(s.StreamId)
	e := &pb.Event{StreamFinish: &pb.StreamFinish{Report: s}}
	out, ok := DecodeEvent(e).(model.StreamReport)
	if !ok || !out.FullReport {
		t.Errorf("expected full report, got %+v %v", out, ok)
	}
}

func TestStreamAudio(t *testing.T) {
	sa := &pb.StreamAudio{StreamId: &pb.StreamId{}, Wav: []byte("RIFFwav"), StartTimestamp: 100, FinishTimestamp: 200}
	fillStreamID(sa.StreamId)
	e := &pb.Event{StreamAudio: sa}
	out, ok := DecodeEvent(e).(model.StreamAudio)
	if !ok || string(out.Wav) != "RIFFwav" || out.StartMs != 100 || out.FinishMs != 200 {
		t.Errorf("got %+v %v", out, ok)
	}
}

func TestSipCallStart(t *testing.T) {
	e := &pb.Event{SipCallStart: &pb.SipCallStart{
		CallId:          "c1",
		Timestamp:       5000,
		InviteTimestamp: 4000,
		SetupCode:       200,
		Caller:          &pb.SipPeer{Aor: "sip:a@h", UserAgent: "UA-A"},
		Callee:          &pb.SipPeer{Aor: "sip:b@h"},
	}}
	out, ok := DecodeEvent(e).(model.SipCallStart)
	if !ok || out.CallID != "c1" || out.SetupCode != 200 || out.Caller.Aor != "sip:a@h" || out.Caller.UserAgent != "UA-A" {
		t.Errorf("got %+v %v", out, ok)
	}
}

func TestSipCallEndResponseCodes(t *testing.T) {
	e := &pb.Event{SipCallEnd: &pb.SipCallEnd{CallId: "c1", Timestamp: 9000, Duration: 4000, ResponseCodes: []uint32{100, 180, 200}}}
	out, ok := DecodeEvent(e).(model.SipCallEnd)
	if !ok || out.Duration != 4000 || len(out.ResponseCodes) != 3 || out.ResponseCodes[2] != 200 {
		t.Errorf("got %+v %v", out, ok)
	}
}

func TestSipReinviteAndFailed(t *testing.T) {
	e := &pb.Event{SipReinvite: &pb.SipReinvite{CallId: "c1", IsRequest: true, UpdatedPeer: &pb.SipPeer{Aor: "sip:a@h"}}}
	out, ok := DecodeEvent(e).(model.SipReinvite)
	if !ok || !out.IsRequest || out.UpdatedPeer.Aor != "sip:a@h" {
		t.Errorf("got %+v %v", out, ok)
	}
	e2 := &pb.Event{SipCallFailed: &pb.SipCallFailed{CallId: "c2", ResponseCode: 486, ReasonPhrase: "Busy Here"}}
	out2, ok := DecodeEvent(e2).(model.SipCallFailed)
	if !ok || out2.ResponseCode != 486 {
		t.Errorf("got %+v %v", out2, ok)
	}
}

func TestInstanceStats(t *testing.T) {
	e := &pb.Event{InstanceStats: &pb.InstanceStatistics{Id: "agent_1", Name: "First", UptimeSeconds: 3600}}
	out, ok := DecodeEvent(e).(model.InstanceStatistics)
	if !ok || out.ID != "agent_1" || out.UptimeSeconds != 3600 {
		t.Errorf("got %+v %v", out, ok)
	}
}

func TestIgnoredPayloadsReturnNil(t *testing.T) {
	e := &pb.Event{CaptureStats: &pb.CaptureStats{Device: "eth0"}}
	if DecodeEvent(e) != nil {
		t.Error("capture_stats should be nil")
	}
	e2 := &pb.Event{StreamGhost: &pb.StreamGhost{StreamId: &pb.StreamId{}}}
	fillStreamID(e2.StreamGhost.StreamId)
	if DecodeEvent(e2) != nil {
		t.Error("stream_ghost should be nil")
	}
}

func TestDecodeBytesRoundtrip(t *testing.T) {
	e := &pb.Event{StreamStart: &pb.StreamStart{StreamId: &pb.StreamId{}}}
	fillStreamID(e.StreamStart.StreamId)
	data, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	msid, ok := out.(model.MediaStreamId)
	if !ok || msid.LinkID != "lnk-1" {
		t.Errorf("got %+v %v", out, ok)
	}
}
