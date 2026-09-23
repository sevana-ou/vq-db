package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sevana-ou/vq-db/internal/api"
	"github.com/sevana-ou/vq-db/internal/bus"
	"github.com/sevana-ou/vq-db/internal/filter"
	"github.com/sevana-ou/vq-db/internal/model"
	"github.com/sevana-ou/vq-db/internal/stats"
)

const farFutureSeconds = int64(1) << 40

// ----- summary ----- //

type rateView struct {
	Label string
	Text  string
	Ratio float64
}

type histView struct {
	Title        string
	Threshold    string
	StreamCount  string
	Sevana       rateView
	Network      rateView
	AvgSevana    *mosPill
	AvgNetwork   *mosPill
	AvgRfactor   string
	AvgDuration  string
	CoverageText string
}

func buildHistView(title string, w map[string]any, threshold float64) histView {
	streams := toI64(w["stream_count"])
	goodSevana := toI64(w["good_sevana_count"])
	goodNetwork := toI64(w["good_network_count"])
	withSevana := toI64(w["with_sevana_mos"])
	avg, _ := w["avg"].(map[string]any)

	ratio := func(count, total int64) float64 {
		if total <= 0 {
			return 0
		}
		return float64(count) / float64(total)
	}
	rate := func(label string, count, total int64) rateView {
		r := ratio(count, total)
		w := r * 100
		if w > 100 {
			w = 100
		}
		return rateView{
			Label: label,
			Text:  fmtPct(r) + "  (" + fmtInt(count) + " / " + fmtInt(total) + ")",
			Ratio: w, // bar width in percent
		}
	}
	coverage := ratio(withSevana, streams)
	return histView{
		Title:        title,
		Threshold:    "≥ " + strconv.FormatFloat(threshold, 'f', 2, 64),
		StreamCount:  fmtInt(streams),
		Sevana:       rate("Sevana good", goodSevana, withSevana),
		Network:      rate("Network good", goodNetwork, streams),
		AvgSevana:    mosView(avg["sevana_mos"], true),
		AvgNetwork:   mosView(avg["network_mos"], false),
		AvgRfactor:   fmtFixed(2)(avg["rfactor"]),
		AvgDuration:  fmtFixed(1)(avg["duration"]),
		CoverageText: "PVQA coverage: " + fmtPct(coverage) + " (" + fmtInt(withSevana) + " / " + fmtInt(streams) + ")",
	}
}

func (a *App) summaryPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	if a.deps.Snapshot != nil {
		if s, ok := a.deps.Snapshot.Get(); ok {
			core := api.CoreStatsToJSON(s)
			data["Core"] = core
			if caps, ok := core["capturers"].([]map[string]any); ok {
				data["Capturers"] = caps
			}
		}
	}
	if a.deps.Registry != nil {
		data["ActiveStreams"] = fmtInt(a.deps.Registry.ActiveCount())
		data["SessionStarted"] = fmtInt(a.deps.Registry.SessionStarted())
		data["SessionFinished"] = fmtInt(a.deps.Registry.SessionFinished())
	} else {
		data["ActiveStreams"] = "0"
		data["SessionStarted"] = "0"
		data["SessionFinished"] = "0"
	}
	if h1, err := api.FindWidgetStats(a.deps.DB, 3600, a.deps.GoodMosThreshold, 0); err == nil {
		data["H1"] = buildHistView("Last 1 hour", api.WidgetStatsToJSON(h1), a.deps.GoodMosThreshold)
	}
	if h24, err := api.FindWidgetStats(a.deps.DB, 86400, a.deps.GoodMosThreshold, 0); err == nil {
		data["H24"] = buildHistView("Last 24 hours", api.WidgetStatsToJSON(h24), a.deps.GoodMosThreshold)
	}
	a.render(w, r, "summary.html", pageData{Nav: "summary", Title: "Summary", Data: data})
}

// ----- core status ----- //

// coreRow is one label/value line in the Core details list.
type coreRow struct{ Label, Value string }

// corePage renders vq-core's instance-statistics snapshot (capturers + core
// details) on its own tab. Same data source as the summary chip; auto-refreshes
// every 10s via the #core-body poll region.
func (a *App) corePage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	if a.deps.Snapshot != nil {
		if s, ok := a.deps.Snapshot.Get(); ok {
			data["Core"] = true
			data["CoreRows"] = coreDetailRows(s)
			core := api.CoreStatsToJSON(s)
			if caps, ok := core["capturers"].([]map[string]any); ok {
				data["Capturers"] = caps
			}
		}
	}
	a.render(w, r, "core.html", pageData{Nav: "core", Title: "Core status", Data: data})
}

// coreDetailRows builds the Core details list, adapting vq-core's loadmonitor
// (scripts/loadtest/monitor/web.go): related counters are grouped into single
// "a / b" rows, and the CPU and tcmalloc rows appear only when vq-core actually
// reports them. Version, uptime and server time are kept as identity rows (the
// loadmonitor shows them in its header, not the metric list).
func coreDetailRows(s model.InstanceStatistics) []coreRow {
	rows := []coreRow{
		{"Version", s.Version},
		{"Uptime", fmtUptime(s.UptimeSeconds)},
	}
	if s.ServerTime != "" {
		rows = append(rows, coreRow{"Server time", s.ServerTime})
	}
	rows = append(rows,
		coreRow{"Live decoders / calls", fmt.Sprintf("%s / %s", fmtInt(s.ActiveDecoderCounter), fmtInt(s.SipCallCounter))},
		coreRow{"Audio / active decoders", fmt.Sprintf("%s / %s", fmtInt(s.ActiveAudioDecoderCounter), fmtInt(s.ActiveDecoderCounter))},
		coreRow{"Total decoders", fmtInt(s.TotalDecoderCounter)},
		coreRow{"PVQA instances / processed", fmt.Sprintf("%s / %ss", fmtInt(s.PvqaInstanceCounter), fmtInt(s.PvqaProcessedSeconds))},
		coreRow{"reSIProcate msgs (all / sip)", fmt.Sprintf("%s / %s", fmtInt(s.ResipMessageCounter), fmtInt(s.ResipSipMessageCounter))},
	)
	// CPU: only when vq-core reports process CPU (loadmonitor condition).
	if s.CpuUserSeconds > 0 || s.CpuSystemSeconds > 0 || s.CpuUsagePercent > 0 {
		rows = append(rows, coreRow{"CPU usage (1 core=100%)",
			fmt.Sprintf("%.1f%%  (user %.1fs / sys %.1fs)", s.CpuUsagePercent, s.CpuUserSeconds, s.CpuSystemSeconds)})
	}
	// tcmalloc memory: only on a tcmalloc build (heap size reported).
	if s.MemHeapSize > 0 {
		rows = append(rows, coreRow{"tcmalloc live / heap / free",
			fmt.Sprintf("%s / %s / %s", fmtBytes(s.MemAllocatedBytes), fmtBytes(s.MemHeapSize), fmtBytes(s.MemPageheapFreeBytes))})
	}
	// tcmalloc alloc/free: only when allocation tracking is enabled in vq-core.
	if s.MemAllocCount > 0 || s.MemAllocsPerSec > 0 {
		rows = append(rows, coreRow{"tcmalloc alloc / free /s",
			fmt.Sprintf("%.0f / %.0f  (net %+.0f/s, total %s / %s)",
				s.MemAllocsPerSec, s.MemFreesPerSec, s.MemAllocsPerSec-s.MemFreesPerSec,
				fmtInt(s.MemAllocCount), fmtInt(s.MemFreeCount))})
	}
	return rows
}

// ----- streams ----- //

func (a *App) streamsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	base := basePrefix(r)
	aq := readStreamsQuery(q, "a")
	fq := readStreamsQuery(q, "f")

	// "New since page open" baseline for the active card. Links drop it, the
	// poll URL keeps it, so any user action resets the baseline.
	since := int64(intParam(q.Get("a_since"), 0))
	if since == 0 {
		since = time.Now().Unix()
	}

	// Active card.
	var records []map[string]any
	if a.deps.Registry != nil {
		records = a.deps.Registry.SnapshotRecords()
	}
	pageRecs, totalActive := stats.PaginateActive(records, aq.searchFilter())
	activeRows := make([]map[string]any, 0, len(pageRecs))
	for _, rec := range pageRecs {
		activeRows = append(activeRows, api.ActiveRecordToJSON(rec, a.deps.SilenceRatioThreshold))
	}
	sinceFilter := filter.NewSearchFilter()
	sinceFilter.MaxCount = 1
	sinceFilter.DateInterval = &filter.DateInterval{StartSeconds: since, EndSeconds: farFutureSeconds}
	_, newCount := stats.PaginateActive(records, sinceFilter)

	activeCard := buildCardView(base, aq, "a", fq, "f", "Active Streams", activeRows, totalActive, "")
	activeCard.NewCount = newCount

	// Finished card.
	var finishedRows []map[string]any
	var totalFinished int
	var finishedErr string
	if count, err := api.GetFinishedStreamCount(a.deps.DB, fq.searchFilter()); err != nil {
		finishedErr = err.Error()
	} else if rows, err := api.GetFinishedStreams(a.deps.DB, fq.searchFilter()); err != nil {
		finishedErr = err.Error()
	} else {
		totalFinished = count
		for _, row := range rows {
			finishedRows = append(finishedRows, api.FinishedStreamRowToJSON(row, a.deps.SilenceRatioThreshold))
		}
	}
	finishedCard := buildCardView(base, fq, "f", aq, "a", "Finished Streams", finishedRows, totalFinished, finishedErr)

	// The poll URL carries the baseline; both cards poll the same URL and
	// each swaps only its own block.
	vals := url.Values{}
	aq.params(vals, "a")
	fq.params(vals, "f")
	vals.Set("a_since", strconv.FormatInt(since, 10))
	pollURL := base + "/ui/streams?" + vals.Encode()

	a.render(w, r, "streams.html", pageData{
		Nav: "streams", Title: "Streams",
		Data: map[string]any{"Active": activeCard, "Finished": finishedCard, "PollURL": pollURL},
	})
}

// ----- stream detail ----- //

type chunkView struct {
	Label      string
	TS         string // stream_end_time, the /report key
	SevanaMos  *mosPill
	NetworkMos *mosPill
	CopyJSON   string
	CopyMD     string
}

func (a *App) streamDetailPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := api.GetStreamHistory(a.deps.DB, id)
	if err != nil {
		a.renderError(w, r, "stream_detail.html", http.StatusServiceUnavailable, err.Error())
		return
	}
	final, err := api.GetStreamFinal(a.deps.DB, id)
	if err != nil {
		a.renderError(w, r, "stream_detail.html", http.StatusServiceUnavailable, err.Error())
		return
	}
	if len(rows) == 0 && final == nil {
		a.renderError(w, r, "stream_detail.html", http.StatusNotFound, "Stream not found.")
		return
	}
	d := api.StreamHistoryToJSON(rows, a.deps.AgentID, a.deps.AgentName, final, a.deps.SilenceRatioThreshold)

	data := map[string]any{
		"LinkID":    id,
		"D":         d,
		"CopyJSON":  copyJSON(d),
		"CopyMD":    mapToMarkdown(d, "Stream "+id),
		"AudioPath": "/audio?link_id=" + url.QueryEscape(id),
	}
	// Sevana R-factor is meaningless without a Sevana MOS.
	if toF64(d["sevana_mos"]) != 0 {
		data["Rfactor"] = rawString(d["sevana_rfactor"])
	}
	if toI64(d["dtx_total"]) > 0 {
		data["DtxText"] = rawString(d["dtx_count"]) + " / " + rawString(d["dtx_sid"]) + " / " + rawString(d["dtx_total"])
		data["SilencePct"] = pct0(d["silence_ratio"])
		data["SilenceSuspected"] = d["silence_suspected"] == true
	}
	if list, ok := d["detectors"].([]map[string]any); ok && len(list) > 0 {
		data["HasDetectors"] = true
		data["Detectors"] = triggeredDetectors(d["detectors"])
	}
	if chunks, ok := d["chunks"].([]map[string]any); ok {
		views := make([]chunkView, 0, len(chunks))
		for _, c := range chunks {
			label := fmtFixed(3)(c["start_time"]) + " - " + fmtFixed(3)(c["end_time"])
			views = append(views, chunkView{
				Label:      label,
				TS:         strconv.FormatInt(toI64(c["stream_end_time"]), 10),
				SevanaMos:  mosView(c["sevana_mos"], true),
				NetworkMos: mosView(c["network_mos"], false),
				CopyJSON:   copyJSON(c),
				CopyMD:     mapToMarkdown(c, "Chunk "+label),
			})
		}
		data["Chunks"] = views
	}
	a.render(w, r, "stream_detail.html", pageData{Nav: "streams", Title: "Stream " + id, Data: data})
}

// ----- chunk detail ----- //

func (a *App) chunkDetailPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ts, _ := strconv.ParseFloat(r.PathValue("ts"), 64)
	row, err := api.GetReport(a.deps.DB, id, int64(ts))
	if err != nil {
		a.renderError(w, r, "chunk_detail.html", http.StatusServiceUnavailable, err.Error())
		return
	}
	if row == nil {
		a.renderError(w, r, "chunk_detail.html", http.StatusNotFound, "Report not found.")
		return
	}
	d := api.ReportRowToJSON(row)
	a.render(w, r, "chunk_detail.html", pageData{
		Nav: "streams", Title: "Chunk report",
		Data: map[string]any{
			"LinkID":    id,
			"D":         d,
			"CopyJSON":  copyJSON(d),
			"CopyMD":    mapToMarkdown(d, "Chunk report (stream "+id+")"),
			"Detectors": parseDetectorReport(rawString(d["detectors_report"])),
		},
	})
}

// ----- SIP calls ----- //

func (a *App) sipCallsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	base := basePrefix(r)
	offset := intParam(q.Get("offset"), 0)
	limit := intParam(q.Get("limit"), 25)
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 500 {
		limit = 25
	}
	data := map[string]any{}
	count, err := api.GetSipCallCount(a.deps.DB, nil, nil, "")
	if err != nil {
		data["Error"] = err.Error()
	} else if list, err := api.GetSipCallList(a.deps.DB, nil, nil, limit, offset, ""); err != nil {
		data["Error"] = err.Error()
	} else {
		rows := make([]map[string]any, 0, len(list))
		for _, s := range list {
			rows = append(rows, api.SipCallSummaryToJSON(s))
		}
		data["Rows"] = rows
		data["Total"] = count
		mk := func(o, l int) string {
			vals := url.Values{}
			if o != 0 {
				vals.Set("offset", strconv.Itoa(o))
			}
			if l != 25 {
				vals.Set("limit", strconv.Itoa(l))
			}
			u := base + "/ui/sip-calls"
			if enc := vals.Encode(); enc != "" {
				u += "?" + enc
			}
			return u
		}
		data["Offset"] = offset
		data["Pager"] = buildPager(offset, limit, len(rows), count, mk)
	}
	a.render(w, r, "sip_calls.html", pageData{Nav: "sip-calls", Title: "SIP calls", Data: data})
}

// sipCode mirrors the Flutter _codeText: setup code, else "code (reason)".
func sipCode(row map[string]any) string {
	if v, ok := row["setup_code"]; ok {
		return rawString(v)
	}
	if v, ok := row["response_code"]; ok {
		if label, ok := row["reason_label"].(string); ok && label != "" {
			return rawString(v) + " (" + label + ")"
		}
		return rawString(v)
	}
	return ""
}

func (a *App) sipCallDetailPage(w http.ResponseWriter, r *http.Request) {
	callID := r.PathValue("id")
	events, err := api.GetSipCallEvents(a.deps.DB, callID)
	if err != nil {
		a.renderError(w, r, "sip_call_detail.html", http.StatusServiceUnavailable, err.Error())
		return
	}
	evs := make([]map[string]any, 0, len(events))
	for _, e := range events {
		evs = append(evs, api.SipEventRowToJSON(e))
	}

	// Call summary: caller/callee from the start (or failed) event.
	outcome := "unknown"
	var origin map[string]any
	for _, e := range evs {
		if toI64(e["event_type"]) == 1 {
			origin = e
			outcome = "established"
			break
		}
	}
	if origin == nil {
		for _, e := range evs {
			if toI64(e["event_type"]) == 4 {
				origin = e
				outcome = "failed"
				break
			}
		}
	}

	// Streams correlated by SIP Call-ID: active first, then finished — same
	// lookup the JSON /stats?sip_callid=… does.
	f := filter.NewSearchFilter()
	f.SIPCallID = callID
	f.MaxCount = a.deps.MaxStreams
	var streams []map[string]any
	if a.deps.Registry != nil {
		page, _ := stats.PaginateActive(a.deps.Registry.SnapshotRecords(), f)
		for _, rec := range page {
			s := api.ActiveRecordToJSON(rec, a.deps.SilenceRatioThreshold)
			s["active"] = true
			streams = append(streams, s)
		}
	}
	if rows, err := api.GetFinishedStreams(a.deps.DB, f); err == nil {
		for _, row := range rows {
			streams = append(streams, api.FinishedStreamRowToJSON(row, a.deps.SilenceRatioThreshold))
		}
	}

	data := map[string]any{
		"CallID":     callID,
		"Events":     buildEventViews(evs),
		"EventCount": len(evs),
		"Outcome":    outcome,
		"Streams":    streams,
	}
	if origin != nil {
		data["Caller"] = rawString(origin["caller"])
		data["Callee"] = rawString(origin["callee"])
	}
	a.render(w, r, "sip_call_detail.html", pageData{Nav: "sip-calls", Title: "SIP call " + callID, Data: data})
}

// ----- track ----- //

func (a *App) trackPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	if affected := intParam(r.URL.Query().Get("affected"), 0); affected > 0 {
		data["Affected"] = affected
	}
	if a.deps.Control == nil {
		data["Unavailable"] = true
	} else {
		ack := a.deps.Control.Send(bus.TrackQuery, nil)
		if !ack.TransportOK {
			data["Unavailable"] = true
		} else {
			data["Patterns"] = ack.Current
			data["Count"] = len(ack.Current)
			if ack.Error != "" {
				data["Error"] = ack.Error
			}
		}
	}
	a.render(w, r, "track.html", pageData{Nav: "track", Title: "Track list", Data: data})
}

func (a *App) trackAdd(w http.ResponseWriter, r *http.Request) {
	var patterns []string
	if p := r.FormValue("pattern"); p != "" {
		patterns = []string{p}
	}
	a.trackMutate(w, r, bus.TrackAdd, patterns, true)
}

func (a *App) trackRemove(w http.ResponseWriter, r *http.Request) {
	var patterns []string
	if p := r.FormValue("pattern"); p != "" {
		patterns = []string{p}
	}
	a.trackMutate(w, r, bus.TrackRemove, patterns, true)
}

func (a *App) trackClear(w http.ResponseWriter, r *http.Request) {
	a.trackMutate(w, r, bus.TrackReplace, nil, false)
}

func (a *App) trackMutate(w http.ResponseWriter, r *http.Request, op bus.TrackOp, patterns []string, require bool) {
	target := basePrefix(r) + "/ui/track"
	if a.deps.Control != nil && (!require || len(patterns) > 0) {
		ack := a.deps.Control.Send(op, patterns)
		if ack.TransportOK && ack.OK {
			if a.deps.TrackStore != nil {
				_ = a.deps.TrackStore.Save(ack.Current)
			}
			if ack.AffectedStreams > 0 {
				target += "?affected=" + strconv.FormatUint(uint64(ack.AffectedStreams), 10)
			}
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func intParam(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
