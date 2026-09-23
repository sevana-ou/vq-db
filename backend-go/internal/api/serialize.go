package api

import (
	"github.com/sevana-ou/vq-db/internal/model"
	"github.com/sevana-ou/vq-db/internal/pvqa"
)

// DefaultSilenceRatioThreshold matches the Python serialize default.
const DefaultSilenceRatioThreshold = 0.8

// SipEventTypeLabel maps a SIP event type code to its label.
func SipEventTypeLabel(t int) string {
	switch t {
	case 1:
		return "start"
	case 2:
		return "end"
	case 3:
		return "reinvite"
	case 4:
		return "failed"
	}
	return "unknown"
}

// SipDirectionLabel maps a SIP direction code to its label.
func SipDirectionLabel(d int) string {
	switch d {
	case 1:
		return "caller_to_callee"
	case 2:
		return "callee_to_caller"
	}
	return "unknown"
}

// SipReasonLabel maps a SIP failure reason code to its label.
func SipReasonLabel(r int) string {
	switch r {
	case 1:
		return "rejected"
	case 2:
		return "canceled"
	case 3:
		return "timeout"
	}
	return "unknown"
}

// AgentEnvelope is the {"instance": {id, name}} block every endpoint includes.
func AgentEnvelope(agentID, agentName string) map[string]any {
	return map[string]any{"instance": map[string]any{"id": agentID, "name": agentName}}
}

// SilenceFields returns the DTX frame stats plus the derived silence signal.
func SilenceFields(row Row, threshold float64) map[string]any {
	sid := getI64(row, "dtx_sid")
	count := getI64(row, "dtx_count")
	total := getI64(row, "dtx_total")
	ratio := 0.0
	if total > 0 {
		ratio = float64(sid+count) / float64(total)
	}
	return map[string]any{
		"dtx_sid":           sid,
		"dtx_count":         count,
		"dtx_total":         total,
		"silence_ratio":     round3(ratio),
		"silence_suspected": total > 0 && ratio >= threshold,
	}
}

func detectorsJSON(d pvqa.Decomposition) []map[string]any {
	out := []map[string]any{}
	for _, dc := range d.Detectors {
		if dc.Name != "" {
			out = append(out, map[string]any{"name": dc.Name, "counter": dc.Count})
		}
	}
	return out
}

// ReportRowToJSON shapes one interval/statistics row for /report.
func ReportRowToJSON(r Row) map[string]any {
	durationMs := getI64(r, "end_timestamp") - getI64(r, "start_timestamp")
	text := getStr(r, "detector_report")
	decomp := pvqa.Decompose([]string{text})
	return map[string]any{
		"source":                 endpoint(getStr(r, "src_ip"), r["src_port"]),
		"destination":            endpoint(getStr(r, "dst_ip"), r["dst_port"]),
		"ssrc":                   hexOf(r["ssrc"]),
		"stream_id":              getStr(r, "link_id"),
		"codec":                  getStr(r, "codec"),
		"sevana_mos":             getF64(r, "sevana_mos"),
		"network_mos":            getF64(r, "network_mos"),
		"jitter":                 round3(getF64(r, "jitter")),
		"rtt_delay":              round3(getF64(r, "rtt_delay")),
		"duration":               float64(durationMs) / 1000.0,
		"rtp_packet_counter":     getI64(r, "rtp_packet_counter"),
		"lost_packet_counter":    getI64(r, "lost_packet_counter"),
		"illegal_packet_counter": getI64(r, "illegal_packet_counter"),
		"sevana_rfactor":         decomp.RfactorPercents(),
		"detectors":              detectorsJSON(decomp),
		"detectors_report":       text,
		"sip_src":                getStr(r, "sip_source"),
		"sip_dst":                getStr(r, "sip_destination"),
		"sip_callid":             getStr(r, "sip_callid"),
		"json_report":            text,
	}
}

// StreamHistoryToJSON shapes interval rows for /streamhistory: cumulative chunks
// plus a whole-stream summary. final is the stream's final statistics row (or
// nil for a still-active stream, when the last interval row is used).
func StreamHistoryToJSON(rows []Row, agentID, agentName string, final Row, silenceThreshold float64) map[string]any {
	result := AgentEnvelope(agentID, agentName)
	chunks := []map[string]any{}
	prevEndMs := int64(0)
	// Chunk offsets are seconds from the STREAM start. vq-core emits each
	// interval with its own absolute start/end timestamps, so the end offset
	// must be anchored to the first interval's start — subtracting the row's
	// own start (as the Python reference did) yields the chunk's length, not
	// its end offset. Anchoring also stays correct for the legacy cumulative
	// shape where every row repeats the stream's start_timestamp.
	var streamStartMs int64
	if len(rows) > 0 {
		streamStartMs = getI64(rows[0], "start_timestamp")
	}
	for _, r := range rows {
		endMs := getI64(r, "end_timestamp") - streamStartMs
		start := prevEndMs
		end := endMs
		prevEndMs = endMs
		chunks = append(chunks, map[string]any{
			"link_id":         getStr(r, "link_id"),
			"stream_end_time": getI64(r, "end_timestamp"),
			"start_time":      float64(start) / 1000.0,
			"end_time":        float64(end) / 1000.0,
			"sevana_mos":      getF64(r, "sevana_mos"),
			"network_mos":     getF64(r, "network_mos"),
			"jitter":          round3(getF64(r, "jitter")),
			"sevana_rfactor":  getF64(r, "sevana_rfactor"),
		})
	}
	result["chunks"] = chunks

	if len(rows) > 0 || final != nil {
		s := final
		if s == nil {
			s = rows[len(rows)-1]
		}
		texts := make([]string, len(rows))
		for i, r := range rows {
			texts[i] = getStr(r, "detector_report")
		}
		decomp := pvqa.Decompose(texts)
		rfactor := getI64(s, "sevana_rfactor")
		if len(rows) > 0 {
			rfactor = int64(decomp.RfactorPercents())
		}
		result["sevana_mos"] = getF64(s, "sevana_mos")
		result["network_mos"] = getF64(s, "network_mos")
		result["jitter"] = round3(getF64(s, "jitter"))
		result["rtt_delay"] = round3(getF64(s, "rtt_delay"))
		result["codec"] = getStr(s, "codec")
		result["rtp_packet_counter"] = getI64(s, "rtp_packet_counter")
		result["lost_packet_counter"] = getI64(s, "lost_packet_counter")
		result["source"] = endpoint(getStr(s, "src_ip"), s["src_port"])
		result["destination"] = endpoint(getStr(s, "dst_ip"), s["dst_port"])
		result["ssrc"] = hexOf(s["ssrc"])
		result["sip_src"] = getStr(s, "sip_source")
		result["sip_dst"] = getStr(s, "sip_destination")
		result["sip_callid"] = getStr(s, "sip_callid")
		result["sevana_rfactor"] = rfactor
		result["detectors"] = detectorsJSON(decomp)
		result["json_report"] = getStr(s, "detector_report")
		for k, v := range SilenceFields(s, silenceThreshold) {
			result[k] = v
		}
	} else {
		result["json_report"] = ""
	}
	return result
}

// FinishedStreamRowToJSON shapes one finished (DB) stream row for /stats.
func FinishedStreamRowToJSON(r Row, silenceThreshold float64) map[string]any {
	durationMs := getI64(r, "end_timestamp") - getI64(r, "start_timestamp")
	out := SilenceFields(r, silenceThreshold)
	out["stream_id"] = getStr(r, "link_id")
	out["ref_id"] = getStr(r, "link_id")
	out["instance"] = map[string]any{"id": getStr(r, "instance_id"), "name": getStr(r, "instance_name")}
	out["start_time"] = startTimeLabel(getI64(r, "start_timestamp"))
	out["duration"] = float64(durationMs) / 1000.0
	out["duration_audio"] = getI64(r, "duration_audio")
	out["sevana_mos"] = getF64(r, "sevana_mos")
	out["network_mos"] = getF64(r, "network_mos")
	out["jitter"] = round3(getF64(r, "jitter"))
	out["sevana_rfactor"] = getI64(r, "sevana_rfactor")
	out["source"] = endpoint(getStr(r, "src_ip"), r["src_port"])
	out["destination"] = endpoint(getStr(r, "dst_ip"), r["dst_port"])
	out["sip_src"] = getStr(r, "sip_source")
	out["sip_dst"] = getStr(r, "sip_destination")
	out["sip_callid"] = getStr(r, "sip_callid")
	return out
}

// ActiveRecordToJSON shapes one active-stream registry record for /stats.
func ActiveRecordToJSON(r Row, silenceThreshold float64) map[string]any {
	out := SilenceFields(r, silenceThreshold)
	out["stream_id"] = getStr(r, "link_id")
	out["ref_id"] = getStr(r, "link_id")
	out["instance"] = map[string]any{"id": getStr(r, "instance_id"), "name": getStr(r, "instance_name")}
	out["start_time"] = startTimeLabel(getI64(r, "start_ms"))
	out["duration"] = float64(getI64(r, "duration")) / 1000.0
	out["duration_audio"] = getI64(r, "duration_audio")
	out["sevana_mos"] = getF64(r, "sevana_mos")
	out["network_mos"] = getF64(r, "network_mos")
	out["jitter"] = round3(getF64(r, "jitter"))
	out["sevana_rfactor"] = getI64(r, "sevana_rfactor")
	out["source"] = endpoint(getStr(r, "src_ip"), r["src_port"])
	out["destination"] = endpoint(getStr(r, "dst_ip"), r["dst_port"])
	out["sip_src"] = getStr(r, "sip_src")
	out["sip_dst"] = getStr(r, "sip_dst")
	out["sip_callid"] = getStr(r, "sip_callid")
	return out
}

// SipCallSummaryToJSON shapes one per-Call-ID summary.
func SipCallSummaryToJSON(s Row) map[string]any {
	established := getBool(s, "established")
	failed := getBool(s, "failed")
	outcome := "unknown"
	if failed {
		outcome = "failed"
	} else if established {
		outcome = "established"
	}
	v := map[string]any{
		"call_id":         getStr(s, "call_id"),
		"caller":          getStr(s, "caller"),
		"callee":          getStr(s, "callee"),
		"start_timestamp": getI64(s, "start_timestamp"),
		"end_timestamp":   getI64(s, "end_timestamp"),
		"duration":        getI64(s, "duration"),
		"reinvite_count":  getI64(s, "reinvite_count"),
		"outcome":         outcome,
	}
	if established {
		v["setup_code"] = getI64(s, "setup_code")
	}
	if failed {
		v["response_code"] = getI64(s, "response_code")
		v["reason"] = getI64(s, "reason")
		v["reason_label"] = SipReasonLabel(int(getI64(s, "reason")))
	}
	return v
}

// SipEventRowToJSON shapes one flat SIP event row.
func SipEventRowToJSON(r Row) map[string]any {
	t := int(getI64(r, "event_type"))
	v := map[string]any{
		"id":               getI64(r, "id"),
		"event_type":       t,
		"event_type_label": SipEventTypeLabel(t),
		"call_id":          getStr(r, "call_id"),
		"timestamp":        getI64(r, "event_timestamp"),
	}
	switch t {
	case 1:
		v["invite_timestamp"] = getI64(r, "invite_timestamp")
		v["setup_code"] = getI64(r, "setup_code")
		v["caller"] = getStr(r, "caller")
		v["callee"] = getStr(r, "callee")
		v["caller_ua"] = getStr(r, "caller_ua")
		v["callee_ua"] = getStr(r, "callee_ua")
		v["caller_codecs"] = getStr(r, "caller_codecs")
		v["callee_codecs"] = getStr(r, "callee_codecs")
	case 2:
		v["duration"] = getI64(r, "duration")
		v["bye_direction"] = getI64(r, "bye_direction")
		v["bye_direction_label"] = SipDirectionLabel(int(getI64(r, "bye_direction")))
		v["response_codes"] = getStr(r, "response_codes")
	case 3:
		v["direction"] = getI64(r, "direction")
		v["direction_label"] = SipDirectionLabel(int(getI64(r, "direction")))
		v["is_request"] = getBool(r, "is_request")
		v["peer"] = getStr(r, "caller")
		v["peer_ua"] = getStr(r, "caller_ua")
		v["peer_codecs"] = getStr(r, "caller_codecs")
	case 4:
		v["invite_timestamp"] = getI64(r, "invite_timestamp")
		v["reason"] = getI64(r, "reason")
		v["reason_label"] = SipReasonLabel(int(getI64(r, "reason")))
		v["response_code"] = getI64(r, "response_code")
		v["reason_phrase"] = getStr(r, "reason_phrase")
		v["caller"] = getStr(r, "caller")
		v["callee"] = getStr(r, "callee")
	}
	return v
}

// WidgetStatsToJSON shapes the summary widget stats.
func WidgetStatsToJSON(s Row) map[string]any {
	return map[string]any{
		"stream_count":       getI64(s, "stream_count"),
		"good_sevana_count":  getI64(s, "good_sevana_count"),
		"good_network_count": getI64(s, "good_network_count"),
		"with_sevana_mos":    getI64(s, "with_sevana_mos"),
		"avg": map[string]any{
			"sevana_mos":  getF64(s, "sevana_mos"),
			"network_mos": getF64(s, "network_mos"),
			"rfactor":     getF64(s, "rfactor"),
			"duration":    getF64(s, "duration"),
		},
	}
}

// CoreStatsToJSON shapes an InstanceStatistics snapshot into the documented
// `core` object (see vq_db_api.md).
func CoreStatsToJSON(s model.InstanceStatistics) map[string]any {
	capturers := make([]map[string]any, 0, len(s.Capturers))
	for _, c := range s.Capturers {
		capturers = append(capturers, map[string]any{
			"device":             c.Device,
			"src_port_range":     c.SrcPortRange,
			"dst_port_range":     c.DstPortRange,
			"packets_total":      c.TotalPacketCounter,
			"packets_rtp":        c.RTPPacketCounter,
			"packets_sip":        c.SipPacketCounter,
			"packets_truncated":  c.TruncatedPacketCounter,
			"packets_dropped_hw": c.DroppedHwPacketCounter,
			"packets_erroneous":  c.ErroneousPacketCounter,
			"mbuf_alloc_failed":  c.MbufAllocFailedCounter,
		})
	}
	return map[string]any{
		"instance":                     map[string]any{"id": s.ID, "name": s.Name},
		"capturers":                    capturers,
		"server_time":                  s.ServerTime,
		"version":                      s.Version,
		"uptime":                       s.UptimeSeconds,
		"pvqa_instance_counter":        s.PvqaInstanceCounter,
		"pvqa_processed_seconds":       s.PvqaProcessedSeconds,
		"active_decoder_counter":       s.ActiveDecoderCounter,
		"active_audio_decoder_counter": s.ActiveAudioDecoderCounter,
		"total_decoder_counter":        s.TotalDecoderCounter,
		"sip_call_counter":             s.SipCallCounter,
		"resip_message_counter":        s.ResipMessageCounter,
		"resip_sip_message_counter":    s.ResipSipMessageCounter,
		"mem_allocated_bytes":          s.MemAllocatedBytes,
		"mem_heap_size":                s.MemHeapSize,
		"mem_pageheap_free_bytes":      s.MemPageheapFreeBytes,
		"mem_alloc_count":              s.MemAllocCount,
		"mem_free_count":               s.MemFreeCount,
		"mem_allocs_per_sec":           s.MemAllocsPerSec,
		"mem_frees_per_sec":            s.MemFreesPerSec,
		"cpu_user_seconds":             s.CpuUserSeconds,
		"cpu_system_seconds":           s.CpuSystemSeconds,
		"cpu_usage_percent":            s.CpuUsagePercent,
	}
}
