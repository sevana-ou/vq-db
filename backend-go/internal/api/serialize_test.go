package api

import "testing"

func TestSipCallSummaryEstablished(t *testing.T) {
	v := SipCallSummaryToJSON(Row{
		"call_id": "c1", "caller": "a", "callee": "b",
		"start_timestamp": int64(1000), "end_timestamp": int64(5000), "duration": int64(4000),
		"reinvite_count": int64(1), "established": int64(1), "failed": int64(0),
		"setup_code": int64(200), "response_code": int64(0), "reason": int64(0),
	})
	if v["outcome"] != "established" || v["setup_code"] != int64(200) {
		t.Errorf("got %+v", v)
	}
	if _, ok := v["response_code"]; ok {
		t.Error("response_code should be absent")
	}
}

func TestSipCallSummaryFailed(t *testing.T) {
	v := SipCallSummaryToJSON(Row{
		"call_id": "c1", "caller": "a", "callee": "b",
		"start_timestamp": int64(0), "end_timestamp": int64(5000), "duration": int64(0),
		"reinvite_count": int64(0), "established": int64(0), "failed": int64(1),
		"setup_code": int64(0), "response_code": int64(486), "reason": int64(1),
	})
	if v["outcome"] != "failed" || v["response_code"] != int64(486) || v["reason_label"] != "rejected" {
		t.Errorf("got %+v", v)
	}
}

func TestSipEventRowStart(t *testing.T) {
	v := SipEventRowToJSON(Row{
		"id": int64(7), "event_type": int64(1), "call_id": "c", "event_timestamp": int64(100),
		"invite_timestamp": int64(90), "setup_code": int64(200),
		"caller": "a", "callee": "b", "caller_ua": "UA", "callee_ua": "",
		"caller_codecs": "opus", "callee_codecs": "",
	})
	if v["event_type_label"] != "start" || v["caller"] != "a" || v["setup_code"] != int64(200) {
		t.Errorf("got %+v", v)
	}
}

func TestSipEventRowReinviteUsesCallerAsPeer(t *testing.T) {
	v := SipEventRowToJSON(Row{
		"id": int64(1), "event_type": int64(3), "call_id": "c", "event_timestamp": int64(5),
		"direction": int64(1), "is_request": true,
		"caller": "sip:a@h", "caller_ua": "UA", "caller_codecs": "opus",
	})
	if v["peer"] != "sip:a@h" || v["direction_label"] != "caller_to_callee" {
		t.Errorf("got %+v", v)
	}
}

func TestWidgetStats(t *testing.T) {
	v := WidgetStatsToJSON(Row{
		"stream_count": int64(10), "good_sevana_count": int64(6), "good_network_count": int64(8),
		"with_sevana_mos": int64(9), "sevana_mos": 3.9, "network_mos": 4.1,
		"rfactor": 85.0, "duration": 12.5,
	})
	if v["stream_count"] != int64(10) {
		t.Errorf("stream_count = %v", v["stream_count"])
	}
	avg := v["avg"].(map[string]any)
	if avg["sevana_mos"] != 3.9 {
		t.Errorf("avg.sevana_mos = %v", avg["sevana_mos"])
	}
}

func reportRow(over Row) Row {
	base := Row{
		"start_timestamp": int64(1000), "end_timestamp": int64(11000),
		"rtp_packet_counter": int64(500), "illegal_packet_counter": int64(0), "lost_packet_counter": int64(3),
		"sevana_mos": 3.8, "sevana_rfactor": int64(85), "network_mos": 4.1, "jitter": 12.0,
		"codec": "opus", "detector_report": "snr=1", "rtt_delay": 5.0, "duration_audio": int64(9000),
		"sip_source": "sip:a@h", "sip_destination": "sip:b@h", "sip_callid": "c1",
		"src_ip": "10.0.0.1", "src_port": int64(5004), "dst_ip": "10.0.0.2", "dst_port": int64(5060),
		"ssrc": int64(0x1A2B), "link_id": "lnk-1",
	}
	for k, v := range over {
		base[k] = v
	}
	return base
}

const reportText = "Time; SNR; DeadAir; Status\r\n" +
	"0.00:0.68; 0.83 !; 0.00; Poor\r\n" +
	"0.68:1.36; 0.00; 0.00; Normal\r\n"

func TestReportRowToJSON(t *testing.T) {
	v := ReportRowToJSON(reportRow(Row{"detector_report": reportText}))
	if v["source"] != "10.0.0.1:5004" || v["destination"] != "10.0.0.2:5060" {
		t.Errorf("endpoints: %v %v", v["source"], v["destination"])
	}
	if v["ssrc"] != "1a2b" || v["duration"] != 10.0 || v["sevana_rfactor"] != 50 {
		t.Errorf("ssrc=%v dur=%v rfactor=%v", v["ssrc"], v["duration"], v["sevana_rfactor"])
	}
	dets := v["detectors"].([]map[string]any)
	if len(dets) != 2 || dets[0]["name"] != "SNR" || dets[0]["counter"] != 1 || dets[1]["counter"] != 0 {
		t.Errorf("detectors = %+v", dets)
	}
	if v["detectors_report"] != reportText {
		t.Error("detectors_report mismatch")
	}
}

func TestStreamHistoryChunksCumulative(t *testing.T) {
	rows := []Row{
		reportRow(Row{"start_timestamp": int64(1000), "end_timestamp": int64(11000)}),
		reportRow(Row{"start_timestamp": int64(1000), "end_timestamp": int64(21000)}),
	}
	v := StreamHistoryToJSON(rows, "agent_1", "First", nil, DefaultSilenceRatioThreshold)
	inst := v["instance"].(map[string]any)
	if inst["id"] != "agent_1" || inst["name"] != "First" {
		t.Errorf("instance = %+v", inst)
	}
	chunks := v["chunks"].([]map[string]any)
	if chunks[0]["start_time"] != 0.0 || chunks[0]["end_time"] != 10.0 {
		t.Errorf("chunk0 = %+v", chunks[0])
	}
	if chunks[1]["start_time"] != 10.0 || chunks[1]["end_time"] != 20.0 {
		t.Errorf("chunk1 = %+v", chunks[1])
	}
	if v["codec"] != "opus" {
		t.Errorf("codec = %v", v["codec"])
	}
}

func TestStreamHistoryChunksPerIntervalTimestamps(t *testing.T) {
	// The shape vq-core actually emits: each interval row carries its OWN
	// absolute start/end (contiguous chunks), not the stream's start. Offsets
	// must still come out relative to the stream start.
	rows := []Row{
		reportRow(Row{"start_timestamp": int64(1000), "end_timestamp": int64(11214)}),
		reportRow(Row{"start_timestamp": int64(11214), "end_timestamp": int64(21414)}),
		reportRow(Row{"start_timestamp": int64(21414), "end_timestamp": int64(23774)}),
	}
	v := StreamHistoryToJSON(rows, "agent_1", "First", nil, DefaultSilenceRatioThreshold)
	chunks := v["chunks"].([]map[string]any)
	want := [][2]float64{{0.0, 10.214}, {10.214, 20.414}, {20.414, 22.774}}
	for i, w := range want {
		if chunks[i]["start_time"] != w[0] || chunks[i]["end_time"] != w[1] {
			t.Errorf("chunk%d = %v–%v, want %v–%v", i, chunks[i]["start_time"], chunks[i]["end_time"], w[0], w[1])
		}
	}
}

func TestSilenceFieldsHighRatioFlagged(t *testing.T) {
	v := SilenceFields(Row{"dtx_sid": int64(5), "dtx_count": int64(80), "dtx_total": int64(100)}, DefaultSilenceRatioThreshold)
	if v["dtx_sid"] != int64(5) || v["dtx_count"] != int64(80) || v["dtx_total"] != int64(100) {
		t.Errorf("counts wrong: %+v", v)
	}
	if v["silence_ratio"] != 0.85 || v["silence_suspected"] != true {
		t.Errorf("ratio=%v suspected=%v", v["silence_ratio"], v["silence_suspected"])
	}
}

func TestSilenceFieldsBelowThresholdNotFlagged(t *testing.T) {
	v := SilenceFields(Row{"dtx_sid": int64(0), "dtx_count": int64(50), "dtx_total": int64(100)}, DefaultSilenceRatioThreshold)
	if v["silence_ratio"] != 0.5 || v["silence_suspected"] != false {
		t.Errorf("got %+v", v)
	}
}

func TestSilenceFieldsNoDtxNeverFlagged(t *testing.T) {
	v := SilenceFields(Row{"dtx_sid": int64(0), "dtx_count": int64(0), "dtx_total": int64(0)}, DefaultSilenceRatioThreshold)
	if v["silence_ratio"] != 0.0 || v["silence_suspected"] != false {
		t.Errorf("got %+v", v)
	}
}

func TestSilenceFieldsCustomThreshold(t *testing.T) {
	row := Row{"dtx_sid": int64(0), "dtx_count": int64(60), "dtx_total": int64(100)}
	if SilenceFields(row, 0.5)["silence_suspected"] != true {
		t.Error("0.6>=0.5 should be flagged")
	}
	if SilenceFields(row, 0.7)["silence_suspected"] != false {
		t.Error("0.6>=0.7 should not be flagged")
	}
}

func TestFinishedStreamRow(t *testing.T) {
	r := reportRow(Row{"instance_id": "agent_1", "instance_name": "First"})
	v := FinishedStreamRowToJSON(r, DefaultSilenceRatioThreshold)
	if v["ref_id"] != "lnk-1" || v["duration"] != 10.0 {
		t.Errorf("got %+v", v)
	}
	inst := v["instance"].(map[string]any)
	if inst["id"] != "agent_1" || inst["name"] != "First" {
		t.Errorf("instance = %+v", inst)
	}
}

func TestActiveRecordIncludesSilence(t *testing.T) {
	r := Row{
		"link_id": "lnk-1", "start_ms": int64(1000), "duration": int64(10000), "duration_audio": int64(0),
		"sevana_mos": 0.0, "network_mos": 4.0, "sevana_rfactor": int64(0), "jitter": 5.0,
		"src_ip": "10.0.0.1", "src_port": int64(5004), "dst_ip": "10.0.0.2", "dst_port": int64(5060),
		"ssrc": "1", "sip_src": "", "sip_dst": "", "sip_callid": "",
		"dtx_sid": int64(0), "dtx_count": int64(30), "dtx_total": int64(100),
	}
	v := ActiveRecordToJSON(r, DefaultSilenceRatioThreshold)
	if v["silence_ratio"] != 0.3 || v["silence_suspected"] != false {
		t.Errorf("got %+v", v)
	}
}

func TestJitterAndRttRoundedTo3Decimals(t *testing.T) {
	v := ReportRowToJSON(reportRow(Row{"jitter": 0.6066727638244629, "rtt_delay": 0.0123456789}))
	if v["jitter"] != 0.607 || v["rtt_delay"] != 0.012 {
		t.Errorf("jitter=%v rtt=%v", v["jitter"], v["rtt_delay"])
	}
	f := FinishedStreamRowToJSON(reportRow(Row{"jitter": 12.3456789}), DefaultSilenceRatioThreshold)
	if f["jitter"] != 12.346 {
		t.Errorf("jitter = %v", f["jitter"])
	}
}
