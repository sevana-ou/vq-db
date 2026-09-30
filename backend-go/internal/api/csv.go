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

// csvStream is one exported stream: its basic cells and, for a detailed
// export, its detector decomposition.
type csvStream struct {
	basic     []string
	detectors []pvqa.DetectorCount
}

// renderCSV writes the CSV. Detector columns are the union of every stream's
// detector names in first-seen order, and each stream's counts are placed by
// name: streams analysed under different PVQA configs have different detector
// lists, so position alone would put counts under the wrong header. A stream
// that did not run a detector leaves that cell empty.
func renderCSV(streams []csvStream, detailed bool) string {
	var detectorNames []string
	column := map[string]int{}
	if detailed {
		for _, s := range streams {
			for _, dc := range s.detectors {
				if _, ok := column[dc.Name]; !ok {
					column[dc.Name] = len(detectorNames)
					detectorNames = append(detectorNames, dc.Name)
				}
			}
		}
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	header := append([]string(nil), BasicHeaders...)
	header = append(header, detectorNames...)
	w.Write(header)
	for _, s := range streams {
		row := append([]string(nil), s.basic...)
		if detailed {
			det := make([]string, len(detectorNames))
			for _, dc := range s.detectors {
				det[column[dc.Name]] = strconv.Itoa(dc.Count)
			}
			row = append(row, det...)
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
	var out []csvStream
	for _, row := range rows {
		s := csvStream{basic: BasicFromFinished(row)}
		if detailed {
			history, err := GetStreamHistory(db, getStr(row, "link_id"))
			if err != nil {
				return "", err
			}
			texts := make([]string, len(history))
			for i, h := range history {
				texts[i] = getStr(h, "detector_report")
			}
			s.detectors = pvqa.Decompose(texts).Detectors
		}
		out = append(out, s)
	}
	return renderCSV(out, detailed), nil
}

// ActiveCSV renders the active-stream records as CSV.
func ActiveCSV(records []Row, detailed bool) string {
	var out []csvStream
	for _, rec := range records {
		s := csvStream{basic: basicFromActive(rec)}
		if detailed {
			var texts []string
			if reports, ok := rec["detector_reports"].([]string); ok {
				texts = reports
			}
			s.detectors = pvqa.Decompose(texts).Detectors
		}
		out = append(out, s)
	}
	return renderCSV(out, detailed)
}
