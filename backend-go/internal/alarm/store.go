package alarm

import (
	"database/sql"
	"log/slog"
)

// StoreNotifier records every event in rtpmon_alarm_events for the dashboard
// and the API. It writes synchronously: one small insert per state change.
type StoreNotifier struct {
	DB *sql.DB
}

// Notify implements Notifier.
func (s StoreNotifier) Notify(ev Event) {
	_, err := s.DB.Exec("INSERT INTO rtpmon_alarm_events"+
		" (name, kind, counter, op, value, limit_value, samples, event_timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		ev.Rule.Name, string(ev.Kind), ev.Rule.Counter, ev.Rule.Op.String(), ev.Value, ev.Rule.Limit, ev.Samples,
		ev.At.UnixMilli())
	if err != nil {
		slog.Error("could not store alarm event", "alarm", ev.Rule.Name, "err", err)
	}
}

// StoredEvent is one row of rtpmon_alarm_events.
type StoredEvent struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Counter   string  `json:"counter"`
	Op        string  `json:"op"`
	Value     float64 `json:"value"`
	Limit     float64 `json:"limit"`
	Samples   int     `json:"samples"`
	Timestamp int64   `json:"timestamp"`
}

// RecentEvents returns up to limit events, newest first.
func RecentEvents(db *sql.DB, limit int) ([]StoredEvent, error) {
	rows, err := db.Query("SELECT name, kind, counter, op, value, limit_value, samples, event_timestamp"+
		" FROM rtpmon_alarm_events ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredEvent
	for rows.Next() {
		var e StoredEvent
		if err := rows.Scan(&e.Name, &e.Kind, &e.Counter, &e.Op, &e.Value, &e.Limit, &e.Samples, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
