package db

import (
	"encoding/json"
	"log/slog"
)

// PropertyKey is the rtpmon_property key under which track patterns are stored.
const PropertyKey = "track_patterns"

// PropertyStore is the minimal interface TrackStore needs (satisfied by *Writer).
type PropertyStore interface {
	GetProperty(name string) (string, bool, error)
	SetProperty(name, value string) error
}

// TrackStore loads/saves the persisted SIP track patterns over a property store.
// Port of vq_db/db/track_store.py.
type TrackStore struct {
	store PropertyStore
	key   string
}

// NewTrackStore constructs a TrackStore over a property store.
func NewTrackStore(store PropertyStore) *TrackStore {
	return &TrackStore{store: store, key: PropertyKey}
}

// Load returns the persisted patterns (empty slice if none / malformed).
func (t *TrackStore) Load() ([]string, error) {
	raw, ok, err := t.store.GetProperty(t.key)
	if err != nil {
		return nil, err
	}
	if !ok || raw == "" {
		return []string{}, nil
	}
	var value []string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		slog.Warn("ignoring malformed persisted track patterns", "raw", raw)
		return []string{}, nil
	}
	out := make([]string, 0, len(value))
	for _, p := range value {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// Save persists the patterns (dropping empties), overwriting any prior value.
func (t *TrackStore) Save(patterns []string) error {
	filtered := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p != "" {
			filtered = append(filtered, p)
		}
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		return err
	}
	return t.store.SetProperty(t.key, string(data))
}
