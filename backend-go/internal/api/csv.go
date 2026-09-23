package api

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"strconv"
	"time"

	"github.com/sevana-ou/vq-db/internal/filter"
	"github.com/sevana-ou/vq-db/internal/pvqa"
)

// BasicHeaders are the fixed CSV columns. Port of csv_report.BASIC_HEADERS.
var BasicHeaders = []string{
	"Source", "Destination", "SSRC", "DB_ID", "Time_Start", "Length",
	"Sevana_MOS", "Sevana_Rfactor", "Network_MOS", "Jitter",
	"Rtp_Received", "Rtp_Lost", "Call_ID", "SIP_source", "SIP_dest",
}

// ExportLimit is effectively "no limit" for an export (C++ used INT_MAX).
const ExportLimit = 1_000_000

// fmtCSVTs formats a Unix-ms timestamp as UTC "YYYY-MM-DD HH:MM:SS.mmm".
func fmtCSVTs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05.000")
}

// fmtNum formats a float with fixed decimals (round-half-even, like Python %.Nf).
func fmtNum(v float64, ndigits int) string {
	return strconv.FormatFloat(v, 'f', ndigits, 64)
}

func basicFromActive(rec Row) []string {
	return []string{
		endpoint(getStr(rec, "src_ip"), rec["src_port"]),
		endpoint(getStr(rec, "dst_ip"), rec["dst_port"]),
		getStr(rec, "ssrc"),
		getStr(rec, "link_id"),
		fmtCSVTs(getI64(rec, "start_ms")),
		fmtNum(float64(getI64(rec, "duration"))/1000.0, 3),
		fmtNum(getF64(rec, "sevana_mos"), 2),
		strconv.FormatInt(getI64(rec, "sevana_rfactor"), 10),
		fmtNum(getF64(rec, "network_mos"), 2),
		fmtNum(getF64(rec, "jitter"), 2),
		strconv.FormatInt(getI64(rec, "rtp_packet_counter"), 10),
		strconv.FormatInt(getI64(rec, "lost_packet_counter"), 10),
		getStr(rec, "sip_callid"),
		getStr(rec, "sip_src"),
		getStr(rec, "sip_dst"),
	}
}

// BasicFromFinished builds the basic CSV cells for a finished (DB) stream row.
func BasicFromFinished(row Row) []string {
	durationS := float64(getI64(row, "end_timestamp")-getI64(row, "start_timestamp")) / 1000.0
	return []string{
		endpoint(getStr(row, "src_ip"), row["src_port"]),
		endpoint(getStr(row, "dst_ip"), row["dst_port"]),
		strconv.FormatInt(getI64(row, "ssrc"), 10),
		getStr(row, "link_id"),
		fmtCSVTs(getI64(row, "start_timestamp")),
		fmtNum(durationS, 3),
		fmtNum(getF64(row, "sevana_mos"), 2),
		strconv.FormatInt(getI64(row, "sevana_rfactor"), 10),
		fmtNum(getF64(row, "network_mos"), 2),
		fmtNum(getF64(row, "jitter"), 2),
		strconv.FormatInt(getI64(row, "rtp_packet_counter"), 10),
		strconv.FormatInt(getI64(row, "lost_packet_counter"), 10),
		getStr(row, "sip_callid"),
		getStr(row, "sip_source"),
		getStr(row, "sip_destination"),
	}
}

func renderCSV(streams [][2][]string, detailed bool, detectorNames []string) string {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	header := append([]string(nil), BasicHeaders...)
	if detailed {
		header = append(header, detectorNames...)
	}
	w.Write(header)
	for _, s := range streams {
		row := append([]string(nil), s[0]...)
		if detailed {
			row = append(row, s[1]...)
		}
		w.Write(row)
	}
	w.Flush()
	return buf.String()
}

// FinishedCSV renders the finished (DB) streams as CSV, exporting up to
// ExportLimit rows regardless of the filter's page size.
func FinishedCSV(db *sql.DB, flt filter.SearchFilter, detailed bool) (string, error) {
	export := filter.SearchFilter{
		PageOffset: 0, MaxCount: ExportLimit, SortField: flt.SortField,
		Descending: flt.Descending, Expression: flt.Expression,
		DateInterval: flt.DateInterval, SIPCallID: flt.SIPCallID,
	}
	rows, err := GetFinishedStreams(db, export)
	if err != nil {
		return "", err
	}
	var detectorNames []string
	var out [][2][]string
	for _, row := range rows {
		basic := BasicFromFinished(row)
		var det []string
		if detailed {
			history, err := GetStreamHistory(db, getStr(row, "link_id"))
			if err != nil {
				return "", err
			}
			texts := make([]string, len(history))
			for i, h := range history {
				texts[i] = getStr(h, "detector_report")
			}
			decomp := pvqa.Decompose(texts)
			if len(detectorNames) == 0 && len(decomp.Detectors) > 0 {
				for _, dc := range decomp.Detectors {
					detectorNames = append(detectorNames, dc.Name)
				}
			}
			for _, dc := range decomp.Detectors {
				det = append(det, strconv.Itoa(dc.Count))
			}
		}
		out = append(out, [2][]string{basic, det})
	}
	return renderCSV(out, detailed, detectorNames), nil
}

// ActiveCSV renders the active-stream records as CSV.
func ActiveCSV(records []Row, detailed bool) string {
	var detectorNames []string
	var out [][2][]string
	for _, rec := range records {
		basic := basicFromActive(rec)
		var det []string
		if detailed {
			var texts []string
			if reports, ok := rec["detector_reports"].([]string); ok {
				texts = reports
			}
			decomp := pvqa.Decompose(texts)
			if len(detectorNames) == 0 && len(decomp.Detectors) > 0 {
				for _, dc := range decomp.Detectors {
					detectorNames = append(detectorNames, dc.Name)
				}
			}
			for _, dc := range decomp.Detectors {
				det = append(det, strconv.Itoa(dc.Count))
			}
		}
		out = append(out, [2][]string{basic, det})
	}
	return renderCSV(out, detailed, detectorNames)
}
