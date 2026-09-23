package pvqa

import (
	"reflect"
	"testing"
)

const report = "Time; SNR; DeadAir; Status\r\n" +
	"0.00:0.68; 0.83 !; 0.00; Poor\r\n" +
	"0.68:1.36; 0.00; 0.00; Normal\r\n"

const reportAvg = "Time; Average; SNR; DeadAir; Status\n" +
	"0.00:0.68; 3.5; 0.90 !; 0.10 !; Poor\n"

func detectorMap(d Decomposition) map[string]int {
	m := map[string]int{}
	for _, dc := range d.Detectors {
		m[dc.Name] = dc.Count
	}
	return m
}

func TestParseBasic(t *testing.T) {
	p := ParseIntervalReport(report)
	if !reflect.DeepEqual(p.DetectorList, []string{"SNR", "DeadAir"}) {
		t.Errorf("detectors = %v", p.DetectorList)
	}
	if len(p.Rows) != 2 {
		t.Fatalf("rows = %d", len(p.Rows))
	}
	if !p.Rows[0].Poor || !reflect.DeepEqual(p.Rows[0].Values, []float64{0.83, 0.0}) {
		t.Errorf("row0 = %+v", p.Rows[0])
	}
	if p.Rows[1].Poor {
		t.Errorf("row1 should not be poor")
	}
}

func TestParseWithAverageColumn(t *testing.T) {
	p := ParseIntervalReport(reportAvg)
	if !reflect.DeepEqual(p.DetectorList, []string{"SNR", "DeadAir"}) {
		t.Errorf("detectors = %v", p.DetectorList)
	}
	if !reflect.DeepEqual(p.Rows[0].Values, []float64{0.90, 0.10}) {
		t.Errorf("values = %v", p.Rows[0].Values)
	}
	if !p.Rows[0].Poor {
		t.Error("row0 should be poor")
	}
}

func TestParseEmpty(t *testing.T) {
	if len(ParseIntervalReport("").Rows) != 0 {
		t.Error("empty should have no rows")
	}
	if len(ParseIntervalReport("   ").DetectorList) != 0 {
		t.Error("whitespace should have no detectors")
	}
}

func TestDecomposeSingle(t *testing.T) {
	d := Decompose([]string{report})
	if d.Intervals != 2 || d.PoorIntervals != 1 {
		t.Errorf("intervals=%d poor=%d", d.Intervals, d.PoorIntervals)
	}
	if !reflect.DeepEqual(detectorMap(d), map[string]int{"SNR": 1, "DeadAir": 0}) {
		t.Errorf("detectors = %v", detectorMap(d))
	}
	if d.RfactorPercents() != 50 {
		t.Errorf("rfactor = %d", d.RfactorPercents())
	}
}

func TestDecomposeAggregatesMultiple(t *testing.T) {
	d := Decompose([]string{report, report})
	if d.Intervals != 4 || d.PoorIntervals != 2 {
		t.Errorf("intervals=%d poor=%d", d.Intervals, d.PoorIntervals)
	}
	if !reflect.DeepEqual(detectorMap(d), map[string]int{"SNR": 2, "DeadAir": 0}) {
		t.Errorf("detectors = %v", detectorMap(d))
	}
	if d.RfactorPercents() != 50 {
		t.Errorf("rfactor = %d", d.RfactorPercents())
	}
}

func TestDecomposeAllGoodIs100(t *testing.T) {
	good := "Time; SNR; Status\r\n0.00:0.68; 0.00; Normal\r\n"
	d := Decompose([]string{good})
	if d.RfactorPercents() != 100 {
		t.Errorf("rfactor = %d", d.RfactorPercents())
	}
	if d.PoorIntervals != 0 {
		t.Errorf("poor = %d", d.PoorIntervals)
	}
}

func TestDecomposeEmptyIsZero(t *testing.T) {
	d := Decompose([]string{""})
	if d.Intervals != 0 || d.RfactorPercents() != 0 || len(d.Detectors) != 0 {
		t.Errorf("got %+v", d)
	}
}
