package api

import (
	"strings"
	"testing"
)

// The dashboard summary must be answered from idx_statistics_summary alone.
// Reading rtpmon_statistics rows instead walks each ~6 KB detector_report to
// reach duration_audio, which took seconds on a cold cache in production.
func TestWidgetStatsQueryUsesCoveringIndex(t *testing.T) {
	a := seededApp(t)
	rows, err := a.deps.DB.Query("EXPLAIN QUERY PLAN "+widgetStatsQuery, 3.6, 3.6, int64(0))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	got := strings.Join(plan, "; ")
	if !strings.Contains(got, "USING COVERING INDEX idx_statistics_summary") {
		t.Errorf("plan = %q, want a search of the covering index idx_statistics_summary", got)
	}
}
