// Package web is the embedded server-rendered dashboard UI: html/template
// pages over the same query/serialize layer the JSON API uses, with htmx
// driving pagination, sorting, filtering and the 10s auto-refresh.
//
// The UI lives under /ui/... so it never collides with the JSON API routes
// (which own /summary, /stats, /track, ... at the root). All assets are
// embedded — the binary serves the whole dashboard with no external files.
package web

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/sevana-ou/vq-db/internal/api"
)

// App renders the embedded dashboard.
type App struct {
	deps api.Deps
}

// NewApp constructs the dashboard UI over the same dependencies as the API.
func NewApp(deps api.Deps) *App {
	return &App{deps: deps}
}

// Handler returns the /ui/... handler (patterns are absolute, mount on the
// root mux).
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/{$}", a.redirectToSummary)
	mux.HandleFunc("GET /ui/summary", a.summaryPage)
	mux.HandleFunc("GET /ui/core", a.corePage)
	mux.HandleFunc("GET /ui/streams", a.streamsPage)
	mux.HandleFunc("GET /ui/stream/{id}", a.streamDetailPage)
	mux.HandleFunc("GET /ui/sip-calls", a.sipCallsPage)
	mux.HandleFunc("GET /ui/sip-call/{id}", a.sipCallDetailPage)
	mux.HandleFunc("GET /ui/chunk/{id}/{ts}", a.chunkDetailPage)
	mux.HandleFunc("GET /ui/track", a.trackPage)
	mux.HandleFunc("POST /ui/track/add", a.trackAdd)
	mux.HandleFunc("POST /ui/track/remove", a.trackRemove)
	mux.HandleFunc("POST /ui/track/clear", a.trackClear)
	mux.Handle("GET /ui/static/", http.StripPrefix("/ui", staticHandler()))
	return mux
}

// RootRedirect 302s "/" to the summary page, preserving a reverse-proxy
// prefix. Old Flutter deep links ("/#/streams") keep working: the fragment
// survives the redirect and layout.html maps it onto the /ui route.
func (a *App) RootRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, basePrefix(r)+"/ui/summary", http.StatusFound)
}

func (a *App) redirectToSummary(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, basePrefix(r)+"/ui/summary", http.StatusFound)
}

var safePrefixRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)

// basePrefix returns the sanitized reverse-proxy prefix ("" when served at
// the site root). Mirrors the API's X-Forwarded-Prefix handling.
func basePrefix(r *http.Request) string {
	prefix := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
	if prefix == "" || !safePrefixRe.MatchString(prefix) {
		return ""
	}
	return prefix
}
