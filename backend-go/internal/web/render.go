package web

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed templates static
var content embed.FS

func staticHandler() http.Handler {
	return http.FileServer(http.FS(content))
}

// pageData is the envelope every template receives.
type pageData struct {
	Base    string // reverse-proxy prefix ("" at site root)
	Path    string // current path, e.g. /ui/streams
	Query   string // raw query string ("" or "?...")
	Nav     string // active nav item key
	Title   string
	Version string
	Sevana  bool // show Sevana MOS figures (App.showSevana)
	Data    any
}

// SelfURL is the current URL (prefix + path + query) — the htmx poll target.
func (p pageData) SelfURL() string {
	return p.Base + p.Path + p.Query
}

var templates = func() map[string]*template.Template {
	pages := []string{
		"summary.html", "core.html", "streams.html", "stream_detail.html",
		"sip_calls.html", "sip_call_detail.html", "chunk_detail.html",
		"track.html",
	}
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		out[p] = template.Must(template.New("layout.html").Funcs(funcs).
			ParseFS(content, "templates/layout.html", "templates/"+p))
	}
	return out
}()

// render writes a page. Non-htmx requests get the full layout. htmx requests
// get just the swapped region: the block whose name equals the HX-Target
// element id (poll regions define such blocks), falling back to "content".
func (a *App) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	data.Base = basePrefix(r)
	data.Path = r.URL.Path
	if q := r.URL.RawQuery; q != "" {
		data.Query = "?" + q
	}
	data.Version = a.deps.Version
	data.Sevana = a.showSevana()

	tpl := templates[page]
	block := "layout.html"
	if r.Header.Get("HX-Request") == "true" {
		block = "content"
		if t := r.Header.Get("HX-Target"); t != "" && tpl.Lookup(t) != nil {
			block = t
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, block, data); err != nil {
		slog.Error("template render failed", "page", page, "block", block, "err", err)
	}
}

func (a *App) renderError(w http.ResponseWriter, r *http.Request, page string, status int, msg string) {
	w.WriteHeader(status)
	a.render(w, r, page, pageData{Title: "Error", Data: map[string]any{"Error": msg}})
}

// ----- template funcs ----- //

var funcs = template.FuncMap{
	"mos":       mosView,
	"fmtInt":    fmtInt,
	"fmtBytes":  fmtBytes,
	"fmtPct":    fmtPct,
	"fmtUptime": fmtUptime,
	"fmtF1":     fmtFixed(1),
	"fmtF2":     fmtFixed(2),
	"fmtF3":     fmtFixed(3),
	"sipTime":   sipTime,
	"sipDur":    sipDur,
	"raw":       rawString,
	"num":       toF64,
	"i64":       toI64,
	"add":       func(a, b int) int { return a + b },
	"pct0":      pct0,
	"queryEsc":  template.URLQueryEscaper,
	"sipCode":   sipCode,
	"dictCard":  func(p pageData, c cardView) map[string]any { return map[string]any{"P": p, "C": c} },
}

// mosView drives the MOS pill. Nil result = render nothing.
type mosPill struct {
	Text  string // raw value, 2 decimals
	Band  string // css class: bad/poor/fair/good/excellent
	Label string // tooltip
}

// mosView mirrors the Flutter MosIndicator: classification is on the value
// clamped to [1,5]; the displayed number is the raw value.
func mosView(v any, hideZero bool) *mosPill {
	f := toF64(v)
	if hideZero && f == 0 {
		return nil
	}
	c := f
	if c != c || c < 1 { // NaN or below scale
		c = 1
	}
	if c > 5 {
		c = 5
	}
	band, label := "bad", "Bad"
	switch {
	case c >= 4.3:
		band, label = "excellent", "Excellent"
	case c >= 4.0:
		band, label = "good", "Good"
	case c >= 3.6:
		band, label = "fair", "Fair"
	case c >= 3.1:
		band, label = "poor", "Poor"
	}
	return &mosPill{Text: strconv.FormatFloat(f, 'f', 2, 64), Band: band, Label: label}
}

// fmtInt renders an integer with thousands separators.
func fmtInt(v any) string {
	n := toI64(v)
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// fmtBytes mirrors the Flutter _fmtBytes: /1024 units, 1 decimal below 100.
func fmtBytes(v any) string {
	f := toF64(v)
	if f <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	dec := 1
	if i == 0 || f >= 100 {
		dec = 0
	}
	return strconv.FormatFloat(f, 'f', dec, 64) + " " + units[i]
}

// fmtPct mirrors _fmtPct: 1 decimal at >=10%, else 2.
func fmtPct(ratio float64) string {
	dec := 2
	if ratio >= 0.1 {
		dec = 1
	}
	return strconv.FormatFloat(ratio*100, 'f', dec, 64) + "%"
}

func pct0(v any) string {
	return strconv.FormatFloat(toF64(v)*100, 'f', 0, 64) + "%"
}

// fmtUptime mirrors _fmtDuration: "1d 2h", "5m 3s", "0s" — minutes hidden
// once days show, seconds hidden once hours show.
func fmtUptime(v any) string {
	sec := toI64(v)
	if sec <= 0 {
		return "0s"
	}
	d, h, m, s := sec/86400, (sec%86400)/3600, (sec%3600)/60, sec%60
	var parts []string
	if d > 0 {
		parts = append(parts, strconv.FormatInt(d, 10)+"d")
	}
	if h > 0 {
		parts = append(parts, strconv.FormatInt(h, 10)+"h")
	}
	if d == 0 && m > 0 {
		parts = append(parts, strconv.FormatInt(m, 10)+"m")
	}
	if d == 0 && h == 0 {
		parts = append(parts, strconv.FormatInt(s, 10)+"s")
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}

func fmtFixed(prec int) func(any) string {
	return func(v any) string {
		return strconv.FormatFloat(toF64(v), 'f', prec, 64)
	}
}

// sipTime renders a Unix-ms timestamp as "yyyy-MM-dd HH:mm:ss" local, "—" for
// zero/negative.
func sipTime(v any) string {
	ms := toI64(v)
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

// sipDur renders a millisecond duration as "h:mm:ss" / "m:ss", "—" for <=0.
func sipDur(v any) string {
	ms := toI64(v)
	if ms <= 0 {
		return "—"
	}
	total := ms / 1000
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// rawString prints a map value the way the Flutter UI did (`toString()`),
// with nil as "".
func rawString(v any) string {
	if v == nil {
		return ""
	}
	if f, ok := v.(float64); ok {
		// Trim float noise: ints stored as float64 print without ".000000".
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func toI64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint64:
		return int64(t)
	case uint32:
		return int64(t)
	case uint:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

func toF64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case int32:
		return float64(t)
	case uint64:
		return float64(t)
	case uint32:
		return float64(t)
	case uint:
		return float64(t)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return 0
}
