// Package model holds the domain event types decoded from the protobuf bus.
// Port of vq_db/model/events.py. All timestamps are UNIX milliseconds.
package model

import "fmt"

// EndpointKey is a stream's network identity without link_id — the key used to
// match interval StreamAudio events (which carry a nil uuid).
type EndpointKey struct {
	SrcIP   string
	SrcPort uint32
	DstIP   string
	DstPort uint32
	SSRC    uint32
}

// MediaStreamId is the identity of an RTP stream. It is comparable so it can key
// the stream->db-id map (matching the C++ std::map<MediaStreamId, StreamId>).
type MediaStreamId struct {
	SrcIP   string
	SrcPort uint32
	DstIP   string
	DstPort uint32
	SSRC    uint32
	LinkID  string // the proto StreamId.uuid; also rtpmon_streams.link_id
}

// EndpointKey returns the identity without LinkID.
func (m MediaStreamId) EndpointKey() EndpointKey {
	return EndpointKey{SrcIP: m.SrcIP, SrcPort: m.SrcPort, DstIP: m.DstIP, DstPort: m.DstPort, SSRC: m.SSRC}
}

func (m MediaStreamId) String() string {
	return fmt.Sprintf("%s:%d->%s:%d/%d", m.SrcIP, m.SrcPort, m.DstIP, m.DstPort, m.SSRC)
}

// StreamReport is an interval or final quality report (protobuf Stream).
// SevanaMOS/NetworkMOS are pointers to preserve the NULL-vs-0 distinction the
// Python Optional[float] fields carry.
type StreamReport struct {
	StreamID             MediaStreamId
	StartMs              int64 // chunk-window start (advances per interval report)
	EndMs                int64 // chunk-window finish
	StartStreamMs        int64 // whole-stream start; constant across interval reports
	RTPPacketCounter     uint64
	IllegalPacketCounter uint64
	LostPacketCounter    uint64
	SevanaMOS            *float64
	NetworkMOS           *float64
	SevanaRfactor        int64
	Jitter               float64
	Codec                string
	RTTDelay             float64
	DetectorReport       string
	DurationAudio        int64
	AmrNbSwitchCounter   int64
	AmrWbSwitchCounter   int64
	DtxSid               uint64
	DtxCount             uint64
	DtxTotal             uint64
	FullReport           bool // -> rtpmon_*.flags (1 if a full/final report)
	// PVQA's synthetic speech score of the stream so far (protobuf
	// Stream.synthetic_speech). HasSynthetic is false when vq-core did not run the
	// analysis; SyntheticScore is nil while the stream holds too little speech.
	HasSynthetic     bool
	SyntheticScore   *float64
	SyntheticSpeechS float64
	UserReport       string
	SipCallID        string
	SipPeerA         string
	SipPeerB         string
}

// StreamAudio is a decoded audio chunk published in-band (protobuf StreamAudio).
type StreamAudio struct {
	StreamID MediaStreamId
	Wav      []byte
	StartMs  int64
	FinishMs int64
}

// SipPeer is one party of a SIP dialog.
type SipPeer struct {
	Aor       string
	UserAgent string
	Codecs    string
	SSRC      uint32
}

// SipCallStart is emitted once a call is established.
type SipCallStart struct {
	CallID          string
	Timestamp       int64
	InviteTimestamp int64
	SetupCode       uint32
	Caller          SipPeer
	Callee          SipPeer
}

// SipCallEnd is emitted on BYE.
type SipCallEnd struct {
	CallID        string
	Timestamp     int64
	Duration      int64
	ByeDirection  int
	ResponseCodes []uint32
	HasBye        bool
}

// SipReinvite covers reINVITE / UPDATE.
type SipReinvite struct {
	CallID      string
	Timestamp   int64
	IsRequest   bool
	Direction   int
	UpdatedPeer SipPeer
	RawMessage  string
}

// SipCallFailed is emitted when an INVITE never reaches the setup code.
type SipCallFailed struct {
	CallID          string
	Timestamp       int64
	InviteTimestamp int64
	Reason          int
	ResponseCode    uint32
	ReasonPhrase    string
	Caller          SipPeer
	Callee          SipPeer
}

// CallHealth is vq-core's call-level verdict for one SIP call (protobuf
// CallHealth): problems visible only when the call's RTP streams are compared
// (one-way audio, codec / ptime / payload asymmetry, late media, one-sided
// silence). An empty Warnings list means the call was judged healthy.
type CallHealth struct {
	CallID      string
	Timestamp   int64
	StreamCount int
	Warnings    []CallHealthWarning
}

// TurnTaking is vq-core's estimate, during a SIP call, of how likely each party
// is an automated voice agent, from conversation timing (protobuf TurnTaking).
// Each event replaces the previous one of its call; Final marks the last.
type TurnTaking struct {
	CallID    string
	Timestamp int64
	Final     bool
	Parties   []TurnTakingParty
}

// TurnTakingParty is one direction of the call: the stream the party sends and
// its reply statistics. Source is "ip:port" of the sender, empty when the
// party's stream was never seen.
type TurnTakingParty struct {
	StreamID         string  `json:"stream_id"`
	Source           string  `json:"source"`
	Destination      string  `json:"destination"`
	SSRC             uint32  `json:"ssrc"`
	HasScore         bool    `json:"has_score"`
	Score            float64 `json:"score"`
	Turns            uint32  `json:"turns"`
	SpeechMs         uint32  `json:"speech_ms"`
	ReplyGapMedianMs float64 `json:"reply_gap_median_ms"`
	ReplyGapIqrMs    float64 `json:"reply_gap_iqr_ms"`
	FastReplyShare   float64 `json:"fast_reply_share"`
	OverlapShare     float64 `json:"overlap_share"`
	Backchannels     uint32  `json:"backchannels"`
	BargeInStopMs    float64 `json:"barge_in_stop_ms"`
	NotAnalysable    string  `json:"not_analysable"`
}

// CallHealthWarning is one call-level problem. Code is the stable lower-case
// identifier ("one_way_audio", "codec_asym", ...), Tag the short label.
type CallHealthWarning struct {
	Code        string   `json:"code"`
	Tag         string   `json:"tag"`
	Explanation string   `json:"explanation"`
	StreamIDs   []string `json:"stream_ids"`
}

// EspSaEvent records an IPsec ESP security association bound in vq-core's key
// store (protobuf EspSaEvent), emitted for coverage monitoring. Key bytes are
// never stored here; HasKeys only records whether they were on the wire.
type EspSaEvent struct {
	Spis    []uint32
	Dst     string
	EncAlg  string
	AuthAlg string
	Source  int // 0 = harvested from signalling, 1 = static file
	HasKeys bool
}

// CaptureStats is per-capture-device packet counters.
type CaptureStats struct {
	Device                 string
	TotalPacketCounter     uint64
	RTPPacketCounter       uint64
	SipPacketCounter       uint64
	TruncatedPacketCounter uint64
	SrcPortRange           string
	DstPortRange           string
	DroppedHwPacketCounter uint64
	ErroneousPacketCounter uint64
	MbufAllocFailedCounter uint64
}

// InstanceStatistics is the periodic vq-core instance snapshot.
type InstanceStatistics struct {
	ID                        string
	Name                      string
	ServerTime                string
	Version                   string
	UptimeSeconds             uint64
	PvqaInstanceCounter       uint64
	PvqaProcessedSeconds      uint64
	ActiveDecoderCounter      uint64
	ActiveAudioDecoderCounter uint64
	TotalDecoderCounter       uint64
	Capturers                 []CaptureStats
	SipCallCounter            uint64
	ResipMessageCounter       uint64
	ResipSipMessageCounter    uint64
	MemAllocatedBytes         uint64
	MemHeapSize               uint64
	MemPageheapFreeBytes      uint64
	MemAllocCount             uint64
	MemFreeCount              uint64
	MemAllocsPerSec           float64
	MemFreesPerSec            float64
	CpuUserSeconds            float64
	CpuSystemSeconds          float64
	CpuUsagePercent           float64
}
