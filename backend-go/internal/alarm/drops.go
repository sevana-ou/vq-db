package alarm

import (
	"sync"
	"time"

	"github.com/sevana-ou/vq-db/internal/model"
)

// DropHistory turns vq-core's cumulative capture drop counters (from its
// instance statistics, sent every second) into the number of packets dropped
// within a window: hardware/kernel drops plus DPDK buffer allocation failures,
// summed over the capture devices.
type DropHistory struct {
	mu      sync.Mutex
	samples []dropSample
	keep    time.Duration
	now     func() time.Time
}

type dropSample struct {
	at    time.Time
	raw   uint64 // vq-core's counter as reported
	total uint64 // drops since tracking began, carried across vq-core restarts
}

const dropSpacing = 5 * time.Second

// NewDropHistory keeps enough samples for the longest window.
func NewDropHistory(keep time.Duration) *DropHistory {
	return &DropHistory{keep: keep, now: time.Now}
}

func drops(s model.InstanceStatistics) uint64 {
	var n uint64
	for _, c := range s.Capturers {
		n += c.DroppedHwPacketCounter + c.MbufAllocFailedCounter
	}
	return n
}

// Note records one instance-statistics event. A vq-core restart resets its
// counters; the drops seen before it stay counted.
func (h *DropHistory) Note(s model.InstanceStatistics) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	cur := drops(s)
	sample := dropSample{at: now, raw: cur}
	if n := len(h.samples); n > 0 {
		last := h.samples[n-1]
		if cur >= last.raw {
			sample.total = last.total + (cur - last.raw)
		} else { // counters went back: vq-core restarted
			sample.total = last.total + cur
		}
	}
	// One sample per dropSpacing at most: vq-core reports every second, and a
	// long window does not need that resolution. The newest sample is updated
	// in place until the spacing has passed.
	if n := len(h.samples); n > 1 && now.Sub(h.samples[n-2].at) < dropSpacing {
		h.samples[n-1] = sample
	} else {
		h.samples = append(h.samples, sample)
	}
	cutoff := now.Add(-h.keep)
	i := 0
	for i < len(h.samples)-1 && h.samples[i+1].at.Before(cutoff) {
		i++
	}
	h.samples = h.samples[i:]
}

// Within returns the packets dropped in the last window; ok is false before
// any statistics arrived.
func (h *DropHistory) Within(window time.Duration) (n uint64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.samples) == 0 {
		return 0, false
	}
	last := h.samples[len(h.samples)-1]
	from := h.now().Add(-window)
	base := h.samples[0]
	for _, s := range h.samples {
		if s.at.After(from) {
			break
		}
		base = s
	}
	return last.total - base.total, true
}
