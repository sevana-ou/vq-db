// Package translate converts protobuf Event messages into domain event types.
// Port of vq_db/proto/translate.py. One Event carries exactly one payload;
// DecodeEvent returns the single decoded domain object (or nil for payloads
// vq-db ignores: capture_stats / stream_ghost).
package translate

import (
	"fmt"
	"math"
	"net"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/sevana-ou/vq-db/internal/model"
	pb "github.com/sevana-ou/vq-db/internal/proto"
)

// DecodeBytes parses raw bus bytes into a domain event (or nil).
func DecodeBytes(data []byte) (any, error) {
	event := &pb.Event{}
	if err := proto.Unmarshal(data, event); err != nil {
		return nil, err
	}
	return DecodeEvent(event), nil
}

// DecodeEvent returns the single decoded domain object for an Event, or nil.
// The Event uses singular sub-messages (not a oneof); has-bits are tested in
// priority order, matching the Python reference.
func DecodeEvent(event *pb.Event) any {
	if event.GetStreamStart() != nil {
		return streamID(event.GetStreamStart().GetStreamId())
	}
	if event.GetStreamReport() != nil {
		return streamReport(event.GetStreamReport(), false)
	}
	if event.GetStreamFinish() != nil {
		return streamReport(event.GetStreamFinish().GetReport(), true)
	}
	if event.GetStreamAudio() != nil {
		a := event.GetStreamAudio()
		return model.StreamAudio{
			StreamID: streamID(a.GetStreamId()),
			Wav:      append([]byte(nil), a.GetWav()...),
			StartMs:  int64(a.GetStartTimestamp()),
			FinishMs: int64(a.GetFinishTimestamp()),
		}
	}
	if event.GetInstanceStats() != nil {
		s := event.GetInstanceStats()
		caps := make([]model.CaptureStats, 0, len(s.GetCapturers()))
		for _, c := range s.GetCapturers() {
			caps = append(caps, captureStats(c))
		}
		return model.InstanceStatistics{
			ID:                        s.GetId(),
			Name:                      s.GetName(),
			ServerTime:                s.GetServerTime(),
			Version:                   s.GetVersion(),
			UptimeSeconds:             s.GetUptimeSeconds(),
			PvqaInstanceCounter:       s.GetPvqaInstanceCounter(),
			PvqaProcessedSeconds:      s.GetPvqaProcessedSeconds(),
			ActiveDecoderCounter:      s.GetActiveDecoderCounter(),
			ActiveAudioDecoderCounter: s.GetActiveAudioDecoderCounter(),
			TotalDecoderCounter:       s.GetTotalDecoderCounter(),
			Capturers:                 caps,
			SipCallCounter:            s.GetSipCallCounter(),
			ResipMessageCounter:       s.GetResipMessageCounter(),
			ResipSipMessageCounter:    s.GetResipSipMessageCounter(),
			MemAllocatedBytes:         s.GetMemAllocatedBytes(),
			MemHeapSize:               s.GetMemHeapSize(),
			MemPageheapFreeBytes:      s.GetMemPageheapFreeBytes(),
			MemAllocCount:             s.GetMemAllocCount(),
			MemFreeCount:              s.GetMemFreeCount(),
			MemAllocsPerSec:           s.GetMemAllocsPerSec(),
			MemFreesPerSec:            s.GetMemFreesPerSec(),
			CpuUserSeconds:            s.GetCpuUserSeconds(),
			CpuSystemSeconds:          s.GetCpuSystemSeconds(),
			CpuUsagePercent:           s.GetCpuUsagePercent(),
		}
	}
	if event.GetSipCallStart() != nil {
		s := event.GetSipCallStart()
		return model.SipCallStart{
			CallID:          s.GetCallId(),
			Timestamp:       int64(s.GetTimestamp()),
			InviteTimestamp: int64(s.GetInviteTimestamp()),
			SetupCode:       s.GetSetupCode(),
			Caller:          peer(s.GetCaller()),
			Callee:          peer(s.GetCallee()),
		}
	}
	if event.GetSipCallEnd() != nil {
		s := event.GetSipCallEnd()
		return model.SipCallEnd{
			CallID:        s.GetCallId(),
			Timestamp:     int64(s.GetTimestamp()),
			Duration:      int64(s.GetDuration()),
			ByeDirection:  int(s.GetByeDirection()),
			ResponseCodes: append([]uint32(nil), s.GetResponseCodes()...),
			HasBye:        s.GetHasBye(),
		}
	}
	if event.GetSipReinvite() != nil {
		s := event.GetSipReinvite()
		return model.SipReinvite{
			CallID:      s.GetCallId(),
			Timestamp:   int64(s.GetTimestamp()),
			IsRequest:   s.GetIsRequest(),
			Direction:   int(s.GetDirection()),
			UpdatedPeer: peer(s.GetUpdatedPeer()),
			RawMessage:  s.GetRawMessage(),
		}
	}
	if event.GetSipCallFailed() != nil {
		s := event.GetSipCallFailed()
		return model.SipCallFailed{
			CallID:          s.GetCallId(),
			Timestamp:       int64(s.GetTimestamp()),
			InviteTimestamp: int64(s.GetInviteTimestamp()),
			Reason:          int(s.GetReason()),
			ResponseCode:    s.GetResponseCode(),
			ReasonPhrase:    s.GetReasonPhrase(),
			Caller:          peer(s.GetCaller()),
			Callee:          peer(s.GetCallee()),
		}
	}
	if event.GetTurnTaking() != nil {
		t := event.GetTurnTaking()
		out := model.TurnTaking{
			CallID:    t.GetCallId(),
			Timestamp: int64(t.GetTimestamp()),
			Final:     t.GetFinal(),
			Parties:   []model.TurnTakingParty{},
		}
		for _, p := range t.GetParties() {
			party := model.TurnTakingParty{
				StreamID:         p.GetStreamId(),
				HasScore:         p.GetHasScore(),
				Score:            round4(p.GetScore()),
				Turns:            p.GetTurns(),
				SpeechMs:         p.GetSpeechMs(),
				ReplyGapMedianMs: round4(p.GetReplyGapMedianMs()),
				ReplyGapIqrMs:    round4(p.GetReplyGapIqrMs()),
				FastReplyShare:   round4(p.GetFastReplyShare()),
				OverlapShare:     round4(p.GetOverlapShare()),
				Backchannels:     p.GetBackchannels(),
				BargeInStopMs:    round4(p.GetBargeInStopMs()),
				NotAnalysable:    p.GetNotAnalysable(),
			}
			if s := p.GetStream(); s != nil {
				id := streamID(s)
				party.Source = fmt.Sprintf("%s:%d", id.SrcIP, id.SrcPort)
				party.Destination = fmt.Sprintf("%s:%d", id.DstIP, id.DstPort)
				party.SSRC = id.SSRC
			}
			out.Parties = append(out.Parties, party)
		}
		return out
	}
	if event.GetCallHealth() != nil {
		h := event.GetCallHealth()
		out := model.CallHealth{
			CallID:      h.GetCallId(),
			Timestamp:   int64(h.GetTimestamp()),
			StreamCount: int(h.GetStreamCount()),
			Warnings:    []model.CallHealthWarning{},
		}
		for _, w := range h.GetWarnings() {
			out.Warnings = append(out.Warnings, model.CallHealthWarning{
				Code:        strings.ToLower(strings.TrimPrefix(w.GetCode().String(), "CODE_")),
				Tag:         w.GetTag(),
				Explanation: w.GetExplanation(),
				StreamIDs:   append([]string{}, w.GetStreamIds()...),
			})
		}
		return out
	}
	if event.GetEspSaEvent() != nil {
		e := event.GetEspSaEvent()
		return model.EspSaEvent{
			Spis:    append([]uint32(nil), e.GetSpis()...),
			Dst:     ipString(e.GetDst()),
			EncAlg:  e.GetEncAlg(),
			AuthAlg: e.GetAuthAlg(),
			Source:  int(e.GetSource()),
			HasKeys: len(e.GetEncKey()) > 0,
		}
	}
	// capture_stats / stream_ghost: ignored by vq-db.
	return nil
}

func streamID(msg *pb.StreamId) model.MediaStreamId {
	return model.MediaStreamId{
		SrcIP:   ipString(msg.GetSrc()),
		SrcPort: msg.GetSrc().GetPort(),
		DstIP:   ipString(msg.GetDst()),
		DstPort: msg.GetDst().GetPort(),
		SSRC:    msg.GetSsrc(),
		LinkID:  msg.GetUuid(),
	}
}

// ipString decodes a raw in_addr/in6_addr (network order) into a printable
// string, matching the Python _ip (zero-padded to 4/16 bytes for v4/v6).
func ipString(msg *pb.IpAddress) string {
	raw := msg.GetIp()
	if msg.GetFamily() == pb.IpAddress_IPv6 {
		buf := make([]byte, 16)
		copy(buf, raw)
		return net.IP(buf).String()
	}
	buf := make([]byte, 4)
	copy(buf, raw)
	return net.IP(buf).String()
}

func captureStats(msg *pb.CaptureStats) model.CaptureStats {
	return model.CaptureStats{
		Device:                 msg.GetDevice(),
		TotalPacketCounter:     msg.GetTotalPacketCounter(),
		RTPPacketCounter:       msg.GetRtpPacketCounter(),
		SipPacketCounter:       msg.GetSipPacketCounter(),
		TruncatedPacketCounter: msg.GetTruncatedPacketCounter(),
		SrcPortRange:           msg.GetSrcPortRange(),
		DstPortRange:           msg.GetDstPortRange(),
		DroppedHwPacketCounter: msg.GetDroppedHwPacketCounter(),
		ErroneousPacketCounter: msg.GetErroneousPacketCounter(),
		MbufAllocFailedCounter: msg.GetMbufAllocFailedCounter(),
	}
}

func peer(msg *pb.SipPeer) model.SipPeer {
	return model.SipPeer{
		Aor:       msg.GetAor(),
		UserAgent: msg.GetUserAgent(),
		Codecs:    msg.GetCodecs(),
		SSRC:      msg.GetSsrc(),
	}
}

// round4 widens a float32 from the wire without its binary tail (0.92 stays
// 0.92, not 0.9200000166893005).
func round4(v float32) float64 {
	return math.Round(float64(v)*10000) / 10000
}

func streamReport(msg *pb.Stream, full bool) model.StreamReport {
	sevana := float64(msg.GetMosSevana().GetCurrent())
	network := float64(msg.GetMosNetwork().GetCurrent())
	var hasSynthetic bool
	var syntheticScore *float64
	var syntheticSpeech float64
	var syntheticProvisional *float64
	if s := msg.GetSyntheticSpeech(); s != nil {
		hasSynthetic = true
		syntheticSpeech = round4(s.GetSpeechSeconds())
		if s.Score != nil {
			v := round4(s.GetScore())
			syntheticScore = &v
		} else if s.ProvisionalScore != nil {
			v := round4(s.GetProvisionalScore())
			syntheticProvisional = &v
		}
	}
	return model.StreamReport{
		HasSynthetic:         hasSynthetic,
		SyntheticScore:       syntheticScore,
		SyntheticSpeechS:     syntheticSpeech,
		SyntheticProvisional: syntheticProvisional,
		StreamID:             streamID(msg.GetStreamId()),
		StartMs:              int64(msg.GetStartTimestamp()),
		EndMs:                int64(msg.GetFinishTimestamp()),
		StartStreamMs:        int64(msg.GetStartTimestampStream()),
		RTPPacketCounter:     msg.GetRtpPacketCounter(),
		IllegalPacketCounter: msg.GetRtpIllegalPacketCounter(),
		LostPacketCounter:    msg.GetRtpLostPacketCounter(),
		// jitter/rtt/mos are MetricStats aggregates; persist the last
		// (instantaneous) value, matching the single columns in the schema.
		SevanaMOS:      &sevana,
		NetworkMOS:     &network,
		SevanaRfactor:  int64(msg.GetSevanaRfactor()),
		Jitter:         float64(msg.GetJitter().GetCurrent()),
		Codec:          msg.GetCodecname(),
		RTTDelay:       float64(msg.GetRtt().GetCurrent()),
		DetectorReport: msg.GetIntervalReport(),
		DurationAudio:  int64(msg.GetDurationAudio()),
		DtxSid:         msg.GetDtxInfo().GetCountSid(),
		DtxCount:       msg.GetDtxInfo().GetCountDtx(),
		DtxTotal:       msg.GetDtxInfo().GetCountTotal(),
		FullReport:     full,
		SipCallID:      msg.GetSip().GetCallId(),
		SipPeerA:       msg.GetSip().GetPeer1(),
		SipPeerB:       msg.GetSip().GetPeer2(),
	}
}
