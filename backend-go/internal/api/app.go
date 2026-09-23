package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sevana-ou/vq-db/internal/bus"
	"github.com/sevana-ou/vq-db/internal/filter"
	"github.com/sevana-ou/vq-db/internal/model"
	"github.com/sevana-ou/vq-db/internal/stats"
)

const farFutureSeconds = int64(1) << 40

// Registry is the read surface of the active-stream registry the API needs.
type Registry interface {
	ActiveCount() int
	SessionStarted() int
	SessionFinished() int
	SnapshotRecords() []map[string]any
}

// Snapshot is the read surface of the instance-stats snapshot.
type Snapshot interface {
	Get() (model.InstanceStatistics, bool)
}

// Control is the track-list control surface (a *bus.ControlClient).
type Control interface {
	Send(op bus.TrackOp, patterns []string) bus.TrackAck
}

// TrackSaver persists track patterns (a *db.TrackStore).
type TrackSaver interface {
	Save(patterns []string) error
}

// Deps holds the API dependencies.
type Deps struct {
	DB                    *sql.DB
	AgentID               string
	AgentName             string
	GoodMosThreshold      float64
	SilenceRatioThreshold float64
	MaxStreams            int
	SevanaMos             string // dashboard: "auto", "on" or "off" (see config)
	Version               string
	Registry              Registry   // may be nil
	Snapshot              Snapshot   // may be nil
	Control               Control    // may be nil
	TrackStore            TrackSaver // may be nil
	StaticDir             string
	// UI is the embedded dashboard handler (internal/web). When set it owns
	// /ui/... and the root redirect, and StaticDir is not served.
	UI http.Handler
}

// App is the read REST API.
type App struct {
	deps Deps
}

// NewApp constructs an App.
func NewApp(deps Deps) *App { return &App{deps: deps} }

// Handler returns the HTTP handler for the API (all routes + SPA fallback).
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /instance_list", a.instanceList)
	mux.HandleFunc("GET /server_stats", a.serverStats)
	mux.HandleFunc("GET /version", a.version)
	mux.HandleFunc("GET /dashboard/summary", a.summary)
	mux.HandleFunc("GET /summary", a.summary)
	mux.HandleFunc("GET /sip_calls", a.sipCalls)
	mux.HandleFunc("GET /sip_call", a.sipCall)
	mux.HandleFunc("GET /sip_events", a.sipEvents)
	mux.HandleFunc("GET /audio", a.audio)
	mux.HandleFunc("GET /streamhistory", a.streamHistory)
	mux.HandleFunc("GET /report", a.report)
	mux.HandleFunc("GET /stats", a.stats)
	mux.HandleFunc("GET /track", func(w http.ResponseWriter, r *http.Request) { a.track(w, r, bus.TrackQuery, false) })
	mux.HandleFunc("GET /track/add", func(w http.ResponseWriter, r *http.Request) { a.track(w, r, bus.TrackAdd, true) })
	mux.HandleFunc("GET /track/remove", func(w http.ResponseWriter, r *http.Request) { a.track(w, r, bus.TrackRemove, true) })
	mux.HandleFunc("GET /track/replace", func(w http.ResponseWriter, r *http.Request) { a.track(w, r, bus.TrackReplace, false) })
	if a.deps.UI != nil {
		mux.Handle("GET /ui/", a.deps.UI)
		mux.Handle("POST /ui/", a.deps.UI)
		mux.HandleFunc("GET /{$}", a.redirectToUI)
	} else if a.deps.StaticDir != "" {
		mux.HandleFunc("GET /", a.serveStatic)
	}
	return mux
}

// redirectToUI sends "/" to the embedded dashboard, honoring a reverse-proxy
// prefix. Legacy hash deep links ("/#/streams") survive the redirect — the
// fragment is client-side and the UI layout maps it onto an /ui route.
func (a *App) redirectToUI(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
	if prefix != "" && !safePrefixRe.MatchString(prefix) {
		prefix = ""
	}
	http.Redirect(w, r, prefix+"/ui/summary", http.StatusFound)
}

// ----- helpers ----- //

func (a *App) envelope() map[string]any {
	return AgentEnvelope(a.deps.AgentID, a.deps.AgentName)
}

func (a *App) snapshotJSON() (map[string]any, bool) {
	if a.deps.Snapshot == nil {
		return nil, false
	}
	stats, ok := a.deps.Snapshot.Get()
	if !ok {
		return nil, false
	}
	return CoreStatsToJSON(stats), true
}

func (a *App) activeCount() int {
	if a.deps.Registry == nil {
		return 0
	}
	return a.deps.Registry.ActiveCount()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(body)
}

func httpError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

func queryIntOpt(r *http.Request, key string) *int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func queryFloatOpt(r *http.Request, key string) *float64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(r.URL.Query().Get(key)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// ----- handlers ----- //

func (a *App) instanceList(w http.ResponseWriter, r *http.Request) {
	agents, err := GetAgentList(a.deps.DB)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (a *App) serverStats(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"id": a.deps.AgentID, "name": a.deps.AgentName}
	if snap, ok := a.snapshotJSON(); ok {
		for k, v := range snap {
			body[k] = v
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *App) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": a.deps.Version})
}

func (a *App) summary(w http.ResponseWriter, r *http.Request) {
	snap, snapOK := a.snapshotJSON()
	body := a.envelope()
	if snapOK {
		body["core"] = snap
	} else {
		body["core"] = map[string]any{}
	}
	body["core_available"] = snapOK
	live := map[string]any{"active_streams": a.activeCount()}
	if a.deps.Registry != nil {
		live["session_started"] = a.deps.Registry.SessionStarted()
		live["session_finished"] = a.deps.Registry.SessionFinished()
	}
	body["live"] = live

	oneH, err := FindWidgetStats(a.deps.DB, 3600, a.deps.GoodMosThreshold, 0)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	dayH, err := FindWidgetStats(a.deps.DB, 86400, a.deps.GoodMosThreshold, 0)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body["historical"] = map[string]any{
		"good_mos_threshold": a.deps.GoodMosThreshold,
		"1h":                 WidgetStatsToJSON(oneH),
		"24h":                WidgetStatsToJSON(dayH),
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *App) sipCalls(w http.ResponseWriter, r *http.Request) {
	limit := int(a.optOr(queryIntOpt(r, "limit"), 0))
	if limit <= 0 {
		limit = a.deps.MaxStreams
	}
	offset := int(a.optOr(queryIntOpt(r, "offset"), 0))
	if offset < 0 {
		offset = 0
	}
	callID := r.URL.Query().Get("call_id")
	start := queryIntOpt(r, "start_timestamp")
	end := queryIntOpt(r, "end_timestamp")
	count, err := GetSipCallCount(a.deps.DB, start, end, callID)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	list, err := GetSipCallList(a.deps.DB, start, end, limit, offset, callID)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body := a.envelope()
	body["total_calls_count"] = count
	calls := make([]map[string]any, 0, len(list))
	for _, s := range list {
		calls = append(calls, SipCallSummaryToJSON(s))
	}
	body["calls"] = calls
	writeJSON(w, http.StatusOK, body)
}

func (a *App) sipCall(w http.ResponseWriter, r *http.Request) {
	callID := r.URL.Query().Get("call_id")
	if callID == "" {
		httpError(w, http.StatusUnprocessableEntity, "call_id is required")
		return
	}
	events, err := GetSipCallEvents(a.deps.DB, callID)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body := a.envelope()
	body["call_id"] = callID
	evs := make([]map[string]any, 0, len(events))
	for _, e := range events {
		evs = append(evs, SipEventRowToJSON(e))
	}
	body["events"] = evs
	body["event_count"] = len(events)
	writeJSON(w, http.StatusOK, body)
}

func (a *App) sipEvents(w http.ResponseWriter, r *http.Request) {
	limit := int(a.optOr(queryIntOpt(r, "limit"), 0))
	if limit <= 0 {
		limit = a.deps.MaxStreams
	}
	offset := int(a.optOr(queryIntOpt(r, "offset"), 0))
	if offset < 0 {
		offset = 0
	}
	eventType := queryIntOpt(r, "type")
	start := queryIntOpt(r, "start_timestamp")
	end := queryIntOpt(r, "end_timestamp")
	count, err := GetSipEventCount(a.deps.DB, eventType, start, end)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	list, err := GetSipEvents(a.deps.DB, eventType, start, end, limit, offset)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	body := a.envelope()
	body["total_events_count"] = count
	evs := make([]map[string]any, 0, len(list))
	for _, e := range list {
		evs = append(evs, SipEventRowToJSON(e))
	}
	body["events"] = evs
	writeJSON(w, http.StatusOK, body)
}

func (a *App) audio(w http.ResponseWriter, r *http.Request) {
	linkID := r.URL.Query().Get("link_id")
	if linkID == "" {
		httpError(w, http.StatusUnprocessableEntity, "link_id is required")
		return
	}
	chunks, err := GetStreamAudioChunks(a.deps.DB, linkID)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	wav := ConcatWav(chunks)
	if len(wav) == 0 {
		httpError(w, http.StatusNotFound, "No audio stored for this stream.")
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", `attachment; filename="`+linkID+`.wav"`)
	w.WriteHeader(http.StatusOK)
	w.Write(wav)
}

func (a *App) streamHistory(w http.ResponseWriter, r *http.Request) {
	sid := firstNonEmptyQ(r, "stream_id", "id")
	if sid == "" {
		httpError(w, http.StatusServiceUnavailable, "Param stream_id is required.")
		return
	}
	rows, err := GetStreamHistory(a.deps.DB, sid)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	final, err := GetStreamFinal(a.deps.DB, sid)
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if len(rows) == 0 && final == nil {
		httpError(w, http.StatusNotFound, "Stream not found.")
		return
	}
	writeJSON(w, http.StatusOK, StreamHistoryToJSON(rows, a.deps.AgentID, a.deps.AgentName, final, a.deps.SilenceRatioThreshold))
}

func (a *App) report(w http.ResponseWriter, r *http.Request) {
	sid := firstNonEmptyQ(r, "stream_id", "id")
	ts := queryFloatOpt(r, "timestamp")
	if ts == nil {
		ts = queryFloatOpt(r, "time")
	}
	if sid == "" || ts == nil {
		httpError(w, http.StatusServiceUnavailable, "Params stream_id and timestamp are required.")
		return
	}
	row, err := GetReport(a.deps.DB, sid, int64(*ts))
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if row == nil {
		httpError(w, http.StatusNotFound, "Report not found.")
		return
	}
	writeJSON(w, http.StatusOK, ReportRowToJSON(row))
}

func (a *App) stats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if queryBool(r, "download_db") {
		dbf := filter.NewSearchFilter()
		if s := q.Get("db_filter"); s != "" {
			dbf = filter.ParseSearchFilter(s)
		}
		csv, err := FinishedCSV(a.deps.DB, dbf, queryBool(r, "show_detectors"))
		if err != nil {
			httpError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		a.writeCSV(w, csv, "vq-db-finished.csv")
		return
	}
	if queryBool(r, "download_active") {
		var records []map[string]any
		if a.deps.Registry != nil {
			records = a.deps.Registry.SnapshotRecords()
		}
		a.writeCSV(w, ActiveCSV(records, true), "vq-db-active.csv")
		return
	}

	body := a.envelope()

	if queryBool(r, "show_active") || queryBool(r, "mem_show_count") {
		mem := a.statsFilter(q.Get("mem_filter"), queryIntOpt(r, "mem_start_timestamp"), queryIntOpt(r, "mem_end_timestamp"), q.Get("sip_callid"))
		var records []map[string]any
		if a.deps.Registry != nil {
			records = a.deps.Registry.SnapshotRecords()
		}
		page, total := stats.PaginateActive(records, mem)
		body["available_active_streams_count"] = total
		if queryBool(r, "show_active") {
			body["total_active_streams_count"] = a.activeCount()
			streams := make([]map[string]any, 0, len(page))
			for _, rec := range page {
				streams = append(streams, ActiveRecordToJSON(rec, a.deps.SilenceRatioThreshold))
			}
			body["active_streams"] = map[string]any{"streams": streams}
		}
	}

	if queryBool(r, "show_finished") || queryBool(r, "db_show_count") {
		dbf := a.statsFilter(q.Get("db_filter"), queryIntOpt(r, "db_start_timestamp"), queryIntOpt(r, "db_end_timestamp"), q.Get("sip_callid"))
		count, err := GetFinishedStreamCount(a.deps.DB, dbf)
		if err != nil {
			body["available_finished_streams_count"] = 0
			if queryBool(r, "show_finished") {
				body["finished_streams"] = map[string]any{"streams": []any{}}
			}
			body["error"] = err.Error()
		} else {
			body["available_finished_streams_count"] = count
			if queryBool(r, "show_finished") {
				total, _ := GetFinishedStreamCount(a.deps.DB, filter.NewSearchFilter())
				body["total_finished_streams_count"] = total
				rows, err := GetFinishedStreams(a.deps.DB, dbf)
				if err != nil {
					body["finished_streams"] = map[string]any{"streams": []any{}}
					body["error"] = err.Error()
				} else {
					streams := make([]map[string]any, 0, len(rows))
					for _, row := range rows {
						streams = append(streams, FinishedStreamRowToJSON(row, a.deps.SilenceRatioThreshold))
					}
					body["finished_streams"] = map[string]any{"streams": streams}
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *App) statsFilter(image string, start, end *int64, sipCallID string) filter.SearchFilter {
	f := filter.NewSearchFilter()
	if image != "" {
		f = filter.ParseSearchFilter(image)
	}
	if f.MaxCount <= 0 {
		f.MaxCount = a.deps.MaxStreams
	}
	if start != nil || end != nil {
		lo := int64(0)
		if start != nil {
			lo = *start
		}
		hi := farFutureSeconds
		if end != nil {
			hi = *end
		}
		f.DateInterval = &filter.DateInterval{StartSeconds: lo, EndSeconds: hi}
	}
	if sipCallID != "" {
		f.SIPCallID = sipCallID
	}
	return f
}

func (a *App) track(w http.ResponseWriter, r *http.Request, op bus.TrackOp, require bool) {
	if a.deps.Control == nil {
		httpError(w, http.StatusServiceUnavailable, "track-list control is not configured")
		return
	}
	var patterns []string
	for _, p := range r.URL.Query()["pattern"] {
		if p != "" {
			patterns = append(patterns, p)
		}
	}
	if require && len(patterns) == 0 {
		httpError(w, http.StatusBadRequest, "at least one 'pattern' parameter is required")
		return
	}
	ack := a.deps.Control.Send(op, patterns)
	if !ack.TransportOK {
		detail := ack.Error
		if detail == "" {
			detail = "vq-core control socket unreachable"
		}
		httpError(w, http.StatusServiceUnavailable, detail)
		return
	}
	if op != bus.TrackQuery && ack.OK && a.deps.TrackStore != nil {
		if err := a.deps.TrackStore.Save(ack.Current); err != nil {
			slog.Error("failed to persist track patterns", "err", err)
		}
	}
	body := map[string]any{"ok": ack.OK, "affected_streams": ack.AffectedStreams, "current": ack.Current}
	if ack.Error != "" {
		body["error"] = ack.Error
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *App) writeCSV(w http.ResponseWriter, content, filename string) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(content))
}

func (a *App) optOr(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}

func firstNonEmptyQ(r *http.Request, keys ...string) string {
	for _, k := range keys {
		if v := r.URL.Query().Get(k); v != "" {
			return v
		}
	}
	return ""
}

// ----- static SPA fallback ----- //

var baseHrefRe = regexp.MustCompile(`<base href="[^"]*">`)
var safePrefixRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)

func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) {
	root := a.deps.StaticDir
	fullPath := strings.TrimPrefix(r.URL.Path, "/")
	rootAbs, _ := filepath.Abs(root)
	if fullPath != "" {
		candidate, err := filepath.Abs(filepath.Join(root, fullPath))
		if err == nil && strings.HasPrefix(candidate, rootAbs+string(os.PathSeparator)) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				http.ServeFile(w, r, candidate)
				return
			}
		}
	}
	index := filepath.Join(root, "index.html")
	if info, err := os.Stat(index); err == nil && !info.IsDir() {
		a.serveIndex(w, r, index)
		return
	}
	httpError(w, http.StatusNotFound, "Not found")
}

func (a *App) serveIndex(w http.ResponseWriter, r *http.Request, index string) {
	html, err := os.ReadFile(index)
	if err != nil {
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
	prefix := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
	if prefix != "" && safePrefixRe.MatchString(prefix) {
		replaced := false
		html = baseHrefRe.ReplaceAllFunc(html, func(m []byte) []byte {
			if replaced {
				return m
			}
			replaced = true
			return []byte(`<base href="` + prefix + `/">`)
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(html)
}
