package api

import (
	"strings"
	"testing"
)

const csvReport = "Time; SNR; DeadAir; Status\r\n" +
	"0.00:0.68; 0.83 !; 0.00; Poor\r\n" +
	"0.68:1.36; 0.00; 0.00; Normal\r\n"

func activeRecord(over Row) Row {
	base := Row{
		"link_id": "lnk-1", "start_ms": int64(1_700_000_000_000), "duration": int64(20_000),
		"sevana_mos": 3.8, "network_mos": 4.0, "jitter": 12.0, "sevana_rfactor": int64(85),
		"src_ip": "10.0.0.1", "src_port": int64(5004), "dst_ip": "10.0.0.2", "dst_port": int64(5060),
		"ssrc": "4660", "sip_src": "sip:a@h", "sip_dst": "sip:b@h", "sip_callid": "c1",
		"rtp_packet_counter": int64(500), "lost_packet_counter": int64(3),
		"detector_reports": []string{csvReport},
	}
	for k, v := range over {
		base[k] = v
	}
	return base
}

func csvLines(out string) []string {
	return strings.Split(strings.TrimRight(out, "\r\n"), "\r\n")
}

func TestActiveCSVBasic(t *testing.T) {
	out := ActiveCSV([]Row{activeRecord(nil)}, false)
	lines := csvLines(out)
	if lines[0] != strings.Join(BasicHeaders, ",") {
		t.Errorf("header = %q", lines[0])
	}
	cells := strings.Split(lines[1], ",")
	checks := map[int]string{0: "10.0.0.1:5004", 1: "10.0.0.2:5060", 2: "4660", 3: "lnk-1", 5: "20.000", 6: "3.80", 10: "500", 11: "3"}
	for i, want := range checks {
		if cells[i] != want {
			t.Errorf("cell[%d] = %q, want %q", i, cells[i], want)
		}
	}
}

func TestActiveCSVWithDetectors(t *testing.T) {
	out := ActiveCSV([]Row{activeRecord(nil)}, true)
	lines := csvLines(out)
	header := strings.Split(lines[0], ",")
	if strings.Join(header[:len(BasicHeaders)], ",") != strings.Join(BasicHeaders, ",") {
		t.Error("basic header prefix wrong")
	}
	det := header[len(BasicHeaders):]
	if strings.Join(det, ",") != "SNR,DeadAir" {
		t.Errorf("detector header = %v", det)
	}
	row := strings.Split(lines[1], ",")
	if row[len(row)-2] != "1" || row[len(row)-1] != "0" {
		t.Errorf("detector counts = %v", row[len(row)-2:])
	}
}

func TestActiveCSVEmpty(t *testing.T) {
	out := ActiveCSV(nil, false)
	if strings.TrimSpace(out) != strings.Join(BasicHeaders, ",") {
		t.Errorf("got %q", out)
	}
}
