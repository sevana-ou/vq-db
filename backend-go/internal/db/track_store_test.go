package db

import (
	"reflect"
	"testing"
)

func newTrackStore(t *testing.T) (*TrackStore, *Writer) {
	t.Helper()
	w := newTestWriter(t)
	return NewTrackStore(w), w
}

func TestTrackRoundTrip(t *testing.T) {
	store, _ := newTrackStore(t)
	got, _ := store.Load()
	if len(got) != 0 {
		t.Errorf("initial load = %v", got)
	}
	store.Save([]string{"alice", "sip:bob@h"})
	got, _ = store.Load()
	if !reflect.DeepEqual(got, []string{"alice", "sip:bob@h"}) {
		t.Errorf("got %v", got)
	}
}

func TestTrackSaveDropsEmptyAndOverwrites(t *testing.T) {
	store, _ := newTrackStore(t)
	store.Save([]string{"a", "", "b"})
	got, _ := store.Load()
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("got %v", got)
	}
	store.Save([]string{})
	got, _ = store.Load()
	if len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestTrackLoadIgnoresMalformedValue(t *testing.T) {
	store, w := newTrackStore(t)
	w.SetProperty(PropertyKey, "not json[")
	got, _ := store.Load()
	if len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
