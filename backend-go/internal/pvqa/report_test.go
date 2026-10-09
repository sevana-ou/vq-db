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

// PVQA 1.9 inserts Noise at column 2, shifting DeadAir right. Counts must
// follow the detector name, not the column.
const reportNoise = "Time; SNR; Noise; DeadAir; Status\n" +
	"0.00:0.68; 0.00; 0.50 !; 0.90 !; Poor\n"

func TestDecomposeDifferentDetectorLists(t *testing.T) {
	d := Decompose([]string{report, reportNoise})
	want := map[string]int{"SNR": 1, "Noise": 1, "DeadAir": 1}
	if !reflect.DeepEqual(detectorMap(d), want) {
		t.Errorf("detectors = %v, want %v", detectorMap(d), want)
	}
	var names []string
	for _, dc := range d.Detectors {
		names = append(names, dc.Name)
	}
	if !reflect.DeepEqual(names, []string{"SNR", "DeadAir", "Noise"}) {
		t.Errorf("order = %v", names)
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

// PVQA 1.9 shape: no " !" marks, frame detectors report a fraction every
// interval, and a silent stream's intervals are not rated Poor.
const reportV2 = "Time; SilentCall; PacketLoss; Echo; Custom; Status\n" +
	"0.00:0.68; 1.000; 0.857; 0.000; 0.20; Uncertain\n" +
	"0.68:1.36; 0.971; 0.400; 0.120; 0.00; Poor\n" +
	"1.36:2.04; 1.000; 0.900; 0.000; 0.00; Ok\n"

var v2Thresholds = Thresholds{
	"SilentCall": {IntThresh: 0.99, Flagged: true},
	"PacketLoss": {IntThresh: 0.5, Flagged: true},
	"Echo":       {IntThresh: 0.0, Flagged: true},
}

func TestDecomposeWithThresholds(t *testing.T) {
	d := DecomposeWith([]string{reportV2}, v2Thresholds)
	// SilentCall: 1.000 twice (0.971 is below 0.99), whatever the status.
	// PacketLoss: 0.857 and 0.900. Echo: IntThresh 0.0 means "above zero".
	// Custom has no threshold: legacy rule, Poor rows only, so its 0.20 in an
	// Uncertain row does not count.
	want := map[string]int{"SilentCall": 2, "PacketLoss": 2, "Echo": 1, "Custom": 0}
	if got := detectorMap(d); !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	if d.Intervals != 3 || d.PoorIntervals != 1 || d.RfactorPercents() != 66 {
		t.Errorf("intervals=%d poor=%d rfactor=%d", d.Intervals, d.PoorIntervals, d.RfactorPercents())
	}
}

func TestDecomposeWithoutThresholdsKeepsLegacyRule(t *testing.T) {
	want := map[string]int{"SilentCall": 1, "PacketLoss": 1, "Echo": 1, "Custom": 0}
	if got := detectorMap(DecomposeWith([]string{reportV2}, nil)); !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}

func TestDecomposeMarkAlwaysTriggers(t *testing.T) {
	text := "Time; SNR; Status\n0.00:0.68; 0.05 !; Normal\n"
	if got := detectorMap(DecomposeWith([]string{text}, Thresholds{"SNR": {IntThresh: 0.1, Flagged: true}})); got["SNR"] != 1 {
		t.Errorf("marked cell below threshold: SNR = %d, want 1", got["SNR"])
	}
}

func TestDecomposeUsesDefaultThresholds(t *testing.T) {
	SetDefaultThresholds(v2Thresholds)
	defer SetDefaultThresholds(nil)
	if got := detectorMap(Decompose([]string{reportV2})); got["SilentCall"] != 2 {
		t.Errorf("SilentCall = %d, want 2", got["SilentCall"])
	}
}

func TestDecomposeSkipsUnflaggedDetectors(t *testing.T) {
	th := Thresholds{
		"SilentCall": {IntThresh: 0.99},
		"PacketLoss": {IntThresh: 0.5},
		"Echo":       {IntThresh: 0.0, Flagged: true},
	}
	// Model-only detectors are left out of the list, not shown with a count.
	want := map[string]int{"Echo": 1, "Custom": 0}
	if got := detectorMap(DecomposeWith([]string{reportV2}, th)); !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
}
