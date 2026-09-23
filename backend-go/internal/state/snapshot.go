package state

import (
	"strconv"
	"sync"

	"github.com/sevana-ou/vq-db/internal/model"
)

// InstanceSnapshot holds the latest instance-statistics snapshot from vq-core
// (for /dashboard/summary and /server_stats). Thread-safe: updated by the bus
// goroutine, read by HTTP handlers. Port of vq_db/state/snapshot.py.
type InstanceSnapshot struct {
	mu    sync.Mutex
	value *model.InstanceStatistics
}

// NewInstanceSnapshot constructs an empty snapshot holder.
func NewInstanceSnapshot() *InstanceSnapshot { return &InstanceSnapshot{} }

// Set replaces the held snapshot.
func (s *InstanceSnapshot) Set(stats model.InstanceStatistics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := stats
	s.value = &v
}

// Get returns the last snapshot, or (zero, false) if vq-core has not published
// one yet.
func (s *InstanceSnapshot) Get() (model.InstanceStatistics, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value == nil {
		return model.InstanceStatistics{}, false
	}
	return *s.value, true
}

func decimalString(v uint64) string { return strconv.FormatUint(v, 10) }
