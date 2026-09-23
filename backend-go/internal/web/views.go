package web

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/sevana-ou/vq-db/internal/filter"
)

// ----- streams page ----- //

// streamsQuery is one card's list state, prefixed in the URL ("a_" active,
// "f_" finished) so the two cards stay independent.
type streamsQuery struct {
	Sort   string
	Dir    string // "asc" / "desc"
	Offset int
	Limit  int
	Filter string
}

func defaultStreamsQuery() streamsQuery {
	return streamsQuery{Sort: "start_time", Dir: "desc", Offset: 0, Limit: 25}
}

func readStreamsQuery(q url.Values, prefix string) streamsQuery {
	sq := defaultStreamsQuery()
	if v := q.Get(prefix + "_sort"); v != "" {
		sq.Sort = v
	}
	if q.Get(prefix+"_dir") == "asc" {
		sq.Dir = "asc"
	}
	sq.Offset = intParam(q.Get(prefix+"_offset"), 0)
	sq.Limit = intParam(q.Get(prefix+"_limit"), 25)
	sq.Filter = q.Get(prefix + "_q")
	if sq.Offset < 0 {
		sq.Offset = 0
	}
	if sq.Limit <= 0 || sq.Limit > 500 {
		sq.Limit = 25
	}
	return sq
}

func (sq streamsQuery) searchFilter() filter.SearchFilter {
	f := filter.NewSearchFilter()
	f.SortField = sq.Sort
	f.Descending = sq.Dir != "asc"
	f.PageOffset = sq.Offset
	f.MaxCount = sq.Limit
	f.Expression = sq.Filter
	return f
}

// params writes the card's non-default state into vals.
func (sq streamsQuery) params(vals url.Values, prefix string) {
	if sq.Sort != "start_time" {
		vals.Set(prefix+"_sort", sq.Sort)
	}
	if sq.Dir != "desc" {
		vals.Set(prefix+"_dir", sq.Dir)
	}
	if sq.Offset != 0 {
		vals.Set(prefix+"_offset", strconv.Itoa(sq.Offset))
	}
	if sq.Limit != 25 {
		vals.Set(prefix+"_limit", strconv.Itoa(sq.Limit))
	}
	if sq.Filter != "" {
		vals.Set(prefix+"_q", sq.Filter)
	}
}

type hiddenField struct{ Name, Value string }

type sortCol struct {
	Label  string
	URL    string // "" = not sortable
	Sorted bool
	Desc   bool
	Num    bool
}

type limitLink struct {
	N       int
	URL     string
	Current bool
}

// pagerView is the shared table pager state.
type pagerView struct {
	RangeText  string
	PrevURL    string // "" = disabled
	NextURL    string
	LimitLinks []limitLink
}

// cardView is everything one streams card template needs.
type cardView struct {
	ID          string // poll region id: "tbl-a" / "tbl-f"
	Prefix      string // "a" / "f"
	Title       string
	Rows        []map[string]any
	Total       int
	NewCount    int // active card only
	Error       string
	Q           streamsQuery
	Cols        []sortCol
	Pager       pagerView
	CSVPath     string // API path relative to Base
	FilterName  string // filter input name
	FilterValue string
	Hidden      []hiddenField // hidden inputs for the filter form
}

var streamColumns = []struct {
	Label string
	Field string // "" = not sortable
	Num   bool
}{
	{"#", "", true},
	{"Time", "start_time", false},
	{"Source", "source", false},
	{"Destination", "destination", false},
	{"Duration (sec)", "duration", true},
	{"Sevana MOS", "sevana_mos", false},
	{"Network MOS", "network_mos", false},
	{"Jitter (ms)", "jitter", true},
	{"SIP source", "sip_src", false},
	{"SIP dest", "sip_dst", false},
	{"Details", "", false},
}

// streamsURL builds /ui/streams?... from both cards' state with `own`
// replacing the state of the card named by prefix. Links deliberately drop
// a_since so any user action resets the "new since" baseline (as the
// Flutter UI did).
func streamsURL(base string, own streamsQuery, prefix string, other streamsQuery, otherPrefix string) string {
	vals := url.Values{}
	own.params(vals, prefix)
	other.params(vals, otherPrefix)
	u := base + "/ui/streams"
	if enc := vals.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// buildCardView assembles sort-header URLs, the pager and the filter form for
// one card.
func buildCardView(base string, own streamsQuery, prefix string, other streamsQuery, otherPrefix, title string, rows []map[string]any, total int, cardErr string) cardView {
	mk := func(q streamsQuery) string { return streamsURL(base, q, prefix, other, otherPrefix) }

	cols := make([]sortCol, 0, len(streamColumns))
	for _, c := range streamColumns {
		col := sortCol{Label: c.Label, Num: c.Num}
		if c.Field != "" {
			q := own
			q.Offset = 0
			if own.Sort == c.Field {
				col.Sorted = true
				col.Desc = own.Dir == "desc"
				if own.Dir == "desc" {
					q.Dir = "asc"
				} else {
					q.Dir = "desc"
				}
			} else {
				q.Sort = c.Field
				q.Dir = "asc"
			}
			col.URL = mk(q)
		}
		cols = append(cols, col)
	}

	// Hidden fields keep the other card's state (and this card's sort) when
	// the filter form submits.
	vals := url.Values{}
	other.params(vals, otherPrefix)
	noFilter := own
	noFilter.Offset = 0
	noFilter.Filter = ""
	noFilter.params(vals, prefix)
	var hidden []hiddenField
	for k, vs := range vals {
		for _, v := range vs {
			hidden = append(hidden, hiddenField{k, v})
		}
	}
	sort.Slice(hidden, func(i, j int) bool { return hidden[i].Name < hidden[j].Name })

	csv := "/stats?download_db=true"
	if prefix == "a" {
		csv = "/stats?download_active=true"
	}

	return cardView{
		ID:          "tbl-" + prefix,
		Prefix:      prefix,
		Title:       title,
		Rows:        rows,
		Total:       total,
		Error:       cardErr,
		Q:           own,
		Cols:        cols,
		Pager:       buildPager(own.Offset, own.Limit, len(rows), total, func(offset, limit int) string { q := own; q.Offset = offset; q.Limit = limit; return mk(q) }),
		CSVPath:     csv,
		FilterName:  prefix + "_q",
		FilterValue: own.Filter,
		Hidden:      hidden,
	}
}

// buildPager mirrors the Flutter paginator: rows-per-page [10 25 50 200],
// "first–last of total", chevrons.
func buildPager(offset, limit, count, total int, mk func(offset, limit int) string) pagerView {
	first, last := 0, 0
	if total > 0 {
		first = offset + 1
		last = offset + count
		if last > total {
			last = total
		}
	}
	p := pagerView{
		RangeText: strconv.Itoa(first) + "–" + strconv.Itoa(last) + " of " + strconv.Itoa(total),
	}
	if offset > 0 {
		prev := offset - limit
		if prev < 0 {
			prev = 0
		}
		p.PrevURL = mk(prev, limit)
	}
	if offset+limit < total {
		p.NextURL = mk(offset+limit, limit)
	}
	for _, n := range []int{10, 25, 50, 200} {
		p.LimitLinks = append(p.LimitLinks, limitLink{N: n, URL: mk(0, n), Current: n == limit})
	}
	return p
}

// ----- detail pages ----- //

// detectorTable is the parsed semicolon-separated detectors report.
type detectorTable struct {
	Header []string
	Body   [][]string
}

// parseDetectorReport splits the detector_report text: lines by "\n", cells
// by ";", first line is the header; body rows are padded to header length.
func parseDetectorReport(text string) detectorTable {
	var t detectorTable
	lines := strings.Split(text, "\n")
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		cells := strings.Split(ln, ";")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if t.Header == nil {
			t.Header = cells
			continue
		}
		for len(cells) < len(t.Header) {
			cells = append(cells, "")
		}
		t.Body = append(t.Body, cells[:len(t.Header)])
	}
	return t
}

// detectorView is one triggered impairment for the stream detail page.
type detectorView struct {
	Name  string
	Count int64
}

// triggeredDetectors filters to count>0 and sorts by count descending.
func triggeredDetectors(detectors any) []detectorView {
	list, ok := detectors.([]map[string]any)
	if !ok {
		return nil
	}
	var out []detectorView
	for _, d := range list {
		name, _ := d["name"].(string)
		count := toI64(d["counter"])
		if count > 0 {
			out = append(out, detectorView{Name: name, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// ----- SIP call detail ----- //

// eventView is one timeline tile.
type eventView struct {
	TypeLabel string
	Tone      string // chip css class
	Time      string
	KV        []hiddenField // ordered label/value rows
	CopyJSON  string
	CopyMD    string
}

var eventTones = map[int64]string{1: "positive", 2: "info", 3: "warning", 4: "danger"}

// buildEventViews shapes serialized SIP events for the timeline: every field
// except the header ones, alphabetical (the JSON order the Flutter UI saw),
// with duration/timestamps humanized.
func buildEventViews(events []map[string]any) []eventView {
	skip := map[string]bool{"id": true, "event_type": true, "event_type_label": true, "call_id": true}
	out := make([]eventView, 0, len(events))
	for _, e := range events {
		label, _ := e["event_type_label"].(string)
		if label == "" {
			label = "unknown"
		}
		tone := eventTones[toI64(e["event_type"])]
		if tone == "" {
			tone = "neutral"
		}
		keys := make([]string, 0, len(e))
		for k := range e {
			if !skip[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		kv := make([]hiddenField, 0, len(keys))
		for _, k := range keys {
			var v string
			switch k {
			case "duration":
				v = sipDur(e[k])
			case "timestamp", "invite_timestamp":
				v = sipTime(e[k])
			default:
				v = rawString(e[k])
			}
			kv = append(kv, hiddenField{k, v})
		}
		out = append(out, eventView{
			TypeLabel: label,
			Tone:      tone,
			Time:      sipTime(e["timestamp"]),
			KV:        kv,
			CopyJSON:  copyJSON(e),
			CopyMD:    mapToMarkdown(e, "SIP "+label+" event"),
		})
	}
	return out
}
