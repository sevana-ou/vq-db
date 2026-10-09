// Package pvqa parses the stored PVQA interval-report text and decomposes
// detector triggers. Port of vq_db/pvqa/report.py.
//
// Text format:
//
//	Time; [Average;] Det1; Det2; ...; Status   <- header line
//	start:end; [avg;] v1; v2; ...; <Status>    <- one line per interval
//
// A detector cell whose value triggered the interval carries a trailing " !".
// Decomposition: a detector's counter is the number of intervals it triggered
// in. A cell triggers when it carries "!", or when its value is above the
// detector's IntThresh from pvqa.cfg (see Thresholds). A detector with no known
// threshold falls back to the legacy rule: value > 0.001 in a row whose status
// is exactly "Poor". The R-factor is int((intervals - poor) / intervals * 100),
// clamped to <= 100.
package pvqa

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// Row is one interval row.
type Row struct {
	Poor   bool
	Values []float64
	Marked []bool // cell carried the " !" trigger mark
}

// Thresholds maps a detector name to its pvqa.cfg IntThresh: the fraction of
// flagged frames above which the detector triggers for an interval.
//
// PVQA 1.9 no longer writes the " !" mark, and its frame detectors report a
// fraction for every interval, so without thresholds the legacy rule counts
// nearly every detector in every Poor interval, clean calls included.
type Thresholds map[string]float64

var defaultThresholds atomic.Pointer[Thresholds]

// SetDefaultThresholds sets the thresholds Decompose uses. main sets them once
// at startup from the pvqa.cfg named by the config's pvqa.config; nil restores
// the legacy rule.
func SetDefaultThresholds(t Thresholds) {
	if t == nil {
		defaultThresholds.Store(nil)
		return
	}
	defaultThresholds.Store(&t)
}

// DefaultThresholds returns the thresholds set by SetDefaultThresholds, or nil.
func DefaultThresholds() Thresholds {
	if p := defaultThresholds.Load(); p != nil {
		return *p
	}
	return nil
}

// ParsedReport is the parsed header + rows of one interval report.
type ParsedReport struct {
	DetectorList []string
	Rows         []Row
}

// DetectorCount is a detector name paired with its trigger count.
type DetectorCount struct {
	Name  string
	Count int
}

// Decomposition aggregates detector trigger counts and interval totals.
type Decomposition struct {
	Detectors     []DetectorCount // in detector order
	Intervals     int
	PoorIntervals int
}

// RfactorPercents returns int((intervals - poor) / intervals * 100), clamped to
// <= 100, or 0 when there are no intervals.
func (d Decomposition) RfactorPercents() int {
	if d.Intervals <= 0 {
		return 0
	}
	pct := int(float64(d.Intervals-d.PoorIntervals) / float64(d.Intervals) * 100)
	if pct > 100 {
		return 100
	}
	return pct
}

// splitLines mimics Python str.splitlines for the \n / \r\n / \r cases the
// report format uses.
func splitLines(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func splitStrip(s string) []string {
	parts := strings.Split(s, ";")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// ParseIntervalReport parses one interval-report text.
func ParseIntervalReport(text string) ParsedReport {
	if strings.TrimSpace(text) == "" {
		return ParsedReport{}
	}

	lines := splitLines(text)
	header := splitStrip(lines[0])

	averagePresent := false
	if len(header) > 0 && header[0] == "Time" {
		header = header[1:]
	}
	if len(header) > 0 && header[0] == "Average" {
		averagePresent = true
		header = header[1:]
	}
	if len(header) > 0 && header[len(header)-1] == "Status" {
		header = header[:len(header)-1]
	}

	result := ParsedReport{DetectorList: header}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitStrip(line)
		if len(fields) < 2 {
			continue
		}
		startOffset := 1
		if averagePresent {
			startOffset = 2
		}
		cells := fields[startOffset : len(fields)-1]
		values := make([]float64, 0, len(cells))
		marked := make([]bool, 0, len(cells))
		for _, cell := range cells {
			v := cell
			mark := strings.HasSuffix(v, "!")
			if mark {
				v = strings.TrimSpace(v[:len(v)-1])
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				f = 0.0
			}
			values = append(values, f)
			marked = append(marked, mark)
		}
		status := fields[len(fields)-1]
		result.Rows = append(result.Rows, Row{Poor: status == "Poor", Values: values, Marked: marked})
	}

	return result
}

// Decompose is DecomposeWith using the thresholds set by SetDefaultThresholds.
func Decompose(texts []string) Decomposition {
	return DecomposeWith(texts, DefaultThresholds())
}

// triggered reports whether one detector cell counts as a trigger.
func triggered(row Row, idx int, threshold float64, known bool) bool {
	if idx < len(row.Marked) && row.Marked[idx] {
		return true
	}
	if known {
		return row.Values[idx] > threshold
	}
	return row.Poor && row.Values[idx] > 0.001
}

// DecomposeWith aggregates detector trigger counts and the R-factor across one
// or more interval reports. Counts are keyed by detector name, not by column, so
// reports whose detector lists differ (a PVQA config change adds or reorders
// detectors) still add up per detector. Detectors keep first-seen order.
func DecomposeWith(texts []string, thresholds Thresholds) Decomposition {
	var detectors []DetectorCount
	index := map[string]int{}
	intervals := 0
	poor := 0

	for _, text := range texts {
		parsed := ParseIntervalReport(text)
		slots := make([]int, len(parsed.DetectorList))
		limits := make([]float64, len(parsed.DetectorList))
		known := make([]bool, len(parsed.DetectorList))
		for i, name := range parsed.DetectorList {
			slot, ok := index[name]
			if !ok {
				slot = len(detectors)
				index[name] = slot
				detectors = append(detectors, DetectorCount{Name: name})
			}
			slots[i] = slot
			limits[i], known[i] = thresholds[name]
		}

		for _, row := range parsed.Rows {
			intervals++
			if row.Poor {
				poor++
			}
			for idx := range row.Values {
				if idx < len(slots) && triggered(row, idx, limits[idx], known[idx]) {
					detectors[slots[idx]].Count++
				}
			}
		}
	}

	return Decomposition{Detectors: detectors, Intervals: intervals, PoorIntervals: poor}
}
