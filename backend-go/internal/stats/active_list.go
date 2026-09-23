// Package stats ports vq_db/stats: the in-memory active-stream list pipeline
// (sort → date window → exact Call-ID → filter expression → total → page slice)
// and the /stats action-based repaging.
package stats

import (
	"sort"
	"strconv"
	"strings"

	"github.com/sevana-ou/vq-db/internal/filter"
)

// Record is one stream's in-memory value-map (start_time in seconds,
// sevana_mos, jitter, sip_callid, ...).
type Record = map[string]any

// sortKeys maps an active-list sort field to the value-map key it orders by.
// Empty/unknown falls back to start_time.
var sortKeys = map[string]string{
	"":               "start_time",
	"sevana_mos":     "sevana_mos",
	"sevana_rfactor": "sevana_rfactor",
	"src_ip":         "src_ip",
	"dst_ip":         "dst_ip",
	"network_mos":    "network_mos",
	"jitter":         "jitter",
	"duration":       "duration",
	"sip_src":        "sip_src",
	"sip_dst":        "sip_dst",
}

// PaginateActive applies the active-list pipeline and returns (page, total).
// total is the size after all filtering; page is the [offset, offset+max) slice.
func PaginateActive(records []Record, flt filter.SearchFilter) ([]Record, int) {
	items := make([]Record, len(records))
	copy(items, records)

	// 1. Sort.
	sortRecords(items, flt.SortField, flt.Descending)

	// 2. Date window (start_time is in seconds).
	if flt.DateInterval != nil {
		lo := float64(flt.DateInterval.StartSeconds)
		hi := float64(flt.DateInterval.EndSeconds)
		filtered := items[:0:0]
		for _, r := range items {
			x := numOf(r["start_time"])
			if lo <= x && x <= hi {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}

	// 3. Exact Call-ID.
	if flt.SIPCallID != "" {
		filtered := items[:0:0]
		for _, r := range items {
			if r["sip_callid"] == flt.SIPCallID {
				filtered = append(filtered, r)
			}
		}
		items = filtered
	}

	// 4. Filter expression (any eval error excludes the record).
	if flt.Expression != "" {
		node, err := filter.Parse(flt.Expression)
		filtered := items[:0:0]
		if err == nil {
			for _, r := range items {
				if filter.EvaluateStreamNode(node, r) {
					filtered = append(filtered, r)
				}
			}
		}
		items = filtered
	}

	// 5. Total after filtering.
	total := len(items)

	// 6. Page slice.
	start := flt.PageOffset
	if start < 0 {
		start = 0
	}
	if flt.MaxCount <= 0 {
		return []Record{}, total
	}
	if start >= len(items) {
		return []Record{}, total
	}
	end := start + flt.MaxCount
	if end > len(items) {
		end = len(items)
	}
	page := make([]Record, end-start)
	copy(page, items[start:end])
	return page, total
}

func sortRecords(items []Record, sortField string, descending bool) {
	keyName, ok := sortKeys[sortField]
	if !ok {
		keyName = "start_time"
	}
	// Decorate each record with its precomputed key so the key travels with the
	// record through the stable sort.
	type paired struct {
		rec Record
		key sortKey
	}
	decorated := make([]paired, len(items))
	for i, r := range items {
		decorated[i] = paired{rec: r, key: makeSortKey(r[keyName])}
	}
	sort.SliceStable(decorated, func(i, j int) bool {
		if descending {
			return keyLess(decorated[j].key, decorated[i].key)
		}
		return keyLess(decorated[i].key, decorated[j].key)
	})
	for i, d := range decorated {
		items[i] = d.rec
	}
}

// sortKey is a mixed-type-tolerant sort key: None(0) < numbers(1) < strings(2).
type sortKey struct {
	rank int
	num  float64
	str  string
}

func makeSortKey(v any) sortKey {
	switch t := v.(type) {
	case nil:
		return sortKey{rank: 0}
	case int64:
		return sortKey{rank: 1, num: float64(t)}
	case int:
		return sortKey{rank: 1, num: float64(t)}
	case float64:
		return sortKey{rank: 1, num: t}
	case bool:
		if t {
			return sortKey{rank: 2, str: "True"}
		}
		return sortKey{rank: 2, str: "False"}
	case string:
		return sortKey{rank: 2, str: t}
	default:
		return sortKey{rank: 2, str: ""}
	}
}

func keyLess(a, b sortKey) bool {
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	if a.rank == 1 {
		return a.num < b.num
	}
	return a.str < b.str
}

func numOf(v any) float64 {
	switch t := v.(type) {
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return 0
}
