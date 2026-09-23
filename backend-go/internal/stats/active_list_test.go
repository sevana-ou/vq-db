package stats

import (
	"sort"
	"testing"

	"github.com/sevana-ou/vq-db/internal/filter"
)

func rec(overrides map[string]any) Record {
	base := Record{
		"start_time":     int64(1_700_000_000),
		"sevana_mos":     4.0,
		"network_mos":    4.0,
		"sevana_rfactor": int64(90),
		"jitter":         10.0,
		"duration":       int64(30_000),
		"src_ip":         "10.0.0.1",
		"dst_ip":         "10.0.0.2",
		"sip_src":        "sip:a@h",
		"sip_dst":        "sip:b@h",
		"sip_callid":     "call-1",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return base
}

func mosList(page []Record) []float64 {
	out := make([]float64, len(page))
	for i, r := range page {
		out[i] = r["sevana_mos"].(float64)
	}
	return out
}

func eqFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEmptyFilterReturnsAll(t *testing.T) {
	var recs []Record
	for i := 0; i < 5; i++ {
		recs = append(recs, rec(map[string]any{"sip_callid": "c" + string(rune('0'+i))}))
	}
	page, total := PaginateActive(recs, filter.SearchFilter{MaxCount: 50})
	if total != 5 || len(page) != 5 {
		t.Errorf("total=%d len=%d", total, len(page))
	}
}

func TestSortAscendingBySevanaMos(t *testing.T) {
	recs := []Record{
		rec(map[string]any{"sevana_mos": 3.0, "sip_callid": "c3"}),
		rec(map[string]any{"sevana_mos": 1.0, "sip_callid": "c1"}),
		rec(map[string]any{"sevana_mos": 2.0, "sip_callid": "c2"}),
	}
	page, _ := PaginateActive(recs, filter.SearchFilter{SortField: "sevana_mos", Descending: false, MaxCount: 50})
	if !eqFloats(mosList(page), []float64{1.0, 2.0, 3.0}) {
		t.Errorf("got %v", mosList(page))
	}
}

func TestSortDescendingByJitter(t *testing.T) {
	recs := []Record{
		rec(map[string]any{"jitter": 10.0, "sip_callid": "a"}),
		rec(map[string]any{"jitter": 30.0, "sip_callid": "b"}),
		rec(map[string]any{"jitter": 20.0, "sip_callid": "c"}),
	}
	page, _ := PaginateActive(recs, filter.SearchFilter{SortField: "jitter", Descending: true, MaxCount: 50})
	var got []float64
	for _, r := range page {
		got = append(got, r["jitter"].(float64))
	}
	if !eqFloats(got, []float64{30.0, 20.0, 10.0}) {
		t.Errorf("got %v", got)
	}
}

func TestSortByIPString(t *testing.T) {
	recs := []Record{
		rec(map[string]any{"src_ip": "10.0.0.3", "sip_callid": "3"}),
		rec(map[string]any{"src_ip": "10.0.0.1", "sip_callid": "1"}),
		rec(map[string]any{"src_ip": "10.0.0.2", "sip_callid": "2"}),
	}
	page, _ := PaginateActive(recs, filter.SearchFilter{SortField: "src_ip", Descending: false, MaxCount: 50})
	var got []string
	for _, r := range page {
		got = append(got, r["src_ip"].(string))
	}
	want := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v", got)
			break
		}
	}
}

func TestPagingSlice(t *testing.T) {
	var recs []Record
	for i := 0; i < 10; i++ {
		recs = append(recs, rec(map[string]any{"sevana_mos": float64(i), "sip_callid": "c" + string(rune('0'+i))}))
	}
	page, total := PaginateActive(recs, filter.SearchFilter{SortField: "sevana_mos", Descending: false, PageOffset: 3, MaxCount: 4})
	if total != 10 {
		t.Errorf("total = %d", total)
	}
	if !eqFloats(mosList(page), []float64{3, 4, 5, 6}) {
		t.Errorf("got %v", mosList(page))
	}
}

func TestDateWindowFiltersByStartSeconds(t *testing.T) {
	recs := []Record{
		rec(map[string]any{"start_time": int64(1000), "sip_callid": "old"}),
		rec(map[string]any{"start_time": int64(2000), "sip_callid": "mid"}),
		rec(map[string]any{"start_time": int64(3000), "sip_callid": "new"}),
	}
	flt := filter.SearchFilter{MaxCount: 50, DateInterval: &filter.DateInterval{StartSeconds: 1500, EndSeconds: 2500}}
	page, total := PaginateActive(recs, flt)
	if total != 1 || page[0]["sip_callid"] != "mid" {
		t.Errorf("total=%d page=%v", total, page)
	}
}

func TestSIPCallIDExactMatch(t *testing.T) {
	recs := []Record{
		rec(map[string]any{"sip_callid": "a"}),
		rec(map[string]any{"sip_callid": "b"}),
		rec(map[string]any{"sip_callid": "a"}),
	}
	_, total := PaginateActive(recs, filter.SearchFilter{MaxCount: 50, SIPCallID: "a"})
	if total != 2 {
		t.Errorf("total = %d", total)
	}
}

func TestExpressionFilter(t *testing.T) {
	recs := []Record{
		rec(map[string]any{"sevana_mos": 2.0, "sip_callid": "c2"}),
		rec(map[string]any{"sevana_mos": 3.5, "sip_callid": "c3"}),
		rec(map[string]any{"sevana_mos": 4.5, "sip_callid": "c4"}),
	}
	page, total := PaginateActive(recs, filter.SearchFilter{MaxCount: 50, Expression: "sevana_mos < 4"})
	if total != 2 {
		t.Errorf("total = %d", total)
	}
	got := mosList(page)
	sort.Float64s(got)
	if !eqFloats(got, []float64{2.0, 3.5}) {
		t.Errorf("got %v", got)
	}
}

func TestExpressionErrorExcludesRecord(t *testing.T) {
	recs := []Record{rec(map[string]any{"sip_callid": "a"}), rec(map[string]any{"sip_callid": "b"})}
	_, total := PaginateActive(recs, filter.SearchFilter{MaxCount: 50, Expression: "unknown_field > 1"})
	if total != 0 {
		t.Errorf("total = %d", total)
	}
}

func TestCombinedFiltersAndPaging(t *testing.T) {
	var recs []Record
	for i := 0; i < 10; i++ {
		callid := "keep"
		if i%2 != 0 {
			callid = "drop"
		}
		recs = append(recs, rec(map[string]any{
			"start_time": int64(1000 + i),
			"sevana_mos": float64(i),
			"sip_callid": callid,
		}))
	}
	flt := filter.SearchFilter{
		SortField:  "sevana_mos",
		Descending: false,
		PageOffset: 1,
		MaxCount:   2,
		SIPCallID:  "keep",
		Expression: "sevana_mos > 0",
	}
	page, total := PaginateActive(recs, flt)
	if total != 4 {
		t.Errorf("total = %d", total)
	}
	if !eqFloats(mosList(page), []float64{4.0, 6.0}) {
		t.Errorf("got %v", mosList(page))
	}
}
