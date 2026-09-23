package db

import (
	"database/sql"
	"fmt"
)

// Schema DDL mirroring vq_db/db/schema.py (which mirrors
// scripts/sql/create-sqlite3-db.sql), kept column-for-column compatible with
// the C++ so an existing database keeps working.

var createTableStmts = []string{
	`CREATE TABLE IF NOT EXISTS rtpmon_streams (
		stream_id INTEGER PRIMARY KEY,
		link_id VARCHAR(64) NOT NULL,
		src_ip VARCHAR(32) NOT NULL,
		src_port INTEGER NOT NULL,
		dst_ip VARCHAR(32) NOT NULL,
		dst_port INTEGER NOT NULL,
		ssrc INTEGER NOT NULL,
		sip_source VARCHAR(1024),
		sip_destination VARCHAR(1024),
		sip_callid VARCHAR(1024),
		agent_id INTEGER,
		created_ts INTEGER
	)`,
	reportTableDDL("rtpmon_intervals"),
	reportTableDDL("rtpmon_statistics"),
	`CREATE TABLE IF NOT EXISTS rtpmon_instances (
		id INTEGER PRIMARY KEY,
		agent_id TEXT NOT NULL,
		agent_name TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS rtpmon_property (
		name TEXT PRIMARY KEY,
		value TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS rtpmon_sip_events (
		id INTEGER PRIMARY KEY,
		event_type INTEGER NOT NULL,
		call_id VARCHAR(1024) NOT NULL,
		event_timestamp INTEGER NOT NULL,
		invite_timestamp INTEGER,
		caller VARCHAR(1024),
		callee VARCHAR(1024),
		caller_ua VARCHAR(1024),
		callee_ua VARCHAR(1024),
		caller_codecs VARCHAR(1024),
		callee_codecs VARCHAR(1024),
		setup_code INTEGER,
		bye_direction INTEGER,
		duration INTEGER,
		response_codes VARCHAR(256),
		reason INTEGER,
		response_code INTEGER,
		reason_phrase VARCHAR(256),
		direction INTEGER,
		is_request INTEGER
	)`,
	`CREATE TABLE IF NOT EXISTS rtpmon_audio (
		id INTEGER PRIMARY KEY,
		stream_id INTEGER NOT NULL,
		start_timestamp INTEGER NOT NULL,
		end_timestamp INTEGER NOT NULL,
		audio_wav TEXT NOT NULL
	)`,
}

var createIndexStmts = []string{
	`CREATE INDEX IF NOT EXISTS idx_streams_link_id ON rtpmon_streams (link_id)`,
	`CREATE INDEX IF NOT EXISTS idx_streams_agent_id ON rtpmon_streams (agent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_streams_sip_callid ON rtpmon_streams (sip_callid)`,
	`CREATE INDEX IF NOT EXISTS idx_intervals_link_id ON rtpmon_intervals (link_id)`,
	// stream_id indexes keep deleteStreamsByIDs from scanning the whole table
	// per chunk (seconds of write lock on a large database).
	`CREATE INDEX IF NOT EXISTS idx_intervals_stream_id ON rtpmon_intervals (stream_id)`,
	`CREATE INDEX IF NOT EXISTS idx_statistics_stream_id ON rtpmon_statistics (stream_id)`,
	`CREATE INDEX IF NOT EXISTS idx_intervals_end_timestamp ON rtpmon_intervals (end_timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_statistics_link_id ON rtpmon_statistics (link_id)`,
	`CREATE INDEX IF NOT EXISTS idx_statistics_end_timestamp ON rtpmon_statistics (end_timestamp)`,
	// Covers the dashboard summary (api.widgetStatsQuery), so it never reads the
	// table rows: duration_audio follows the large detector_report in each row.
	`CREATE INDEX IF NOT EXISTS idx_statistics_summary ON rtpmon_statistics
		(end_timestamp, sevana_mos, sevana_rfactor, network_mos, duration_audio, stream_id)`,
	`CREATE INDEX IF NOT EXISTS idx_statistics_start_timestamp ON rtpmon_statistics (start_timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_sip_events_call_id ON rtpmon_sip_events (call_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sip_events_event_timestamp ON rtpmon_sip_events (event_timestamp)`,
	`CREATE INDEX IF NOT EXISTS idx_sip_events_event_type ON rtpmon_sip_events (event_type)`,
	`CREATE INDEX IF NOT EXISTS idx_audio_stream_id ON rtpmon_audio (stream_id)`,
	`CREATE INDEX IF NOT EXISTS idx_audio_start_timestamp ON rtpmon_audio (start_timestamp)`,
}

func reportTableDDL(name string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		stream_id INTEGER,
		start_timestamp INTEGER NOT NULL,
		end_timestamp INTEGER NOT NULL,
		rtp_packet_counter INTEGER NOT NULL,
		illegal_packet_counter INTEGER NOT NULL,
		lost_packet_counter INTEGER NOT NULL,
		sevana_mos FLOAT,
		sevana_rfactor INTEGER,
		network_mos FLOAT,
		jitter FLOAT,
		codec VARCHAR(32),
		detector_report TEXT,
		rtt_delay FLOAT,
		flags INTEGER NOT NULL,
		tags VARCHAR(1024),
		amr_wb_switch_counter INTEGER,
		amr_nb_switch_counter INTEGER,
		link_id VARCHAR(64) NOT NULL,
		duration_audio INTEGER,
		user_report TEXT,
		dtx_sid INTEGER,
		dtx_count INTEGER,
		dtx_total INTEGER
	)`, name)
}

// Bootstrap creates tables/indexes if missing, then adds any columns absent
// from an older database (additive migration), matching writer.bootstrap().
func Bootstrap(conn *sql.DB) error {
	for _, stmt := range createTableStmts {
		if _, err := conn.Exec(stmt); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}
	for _, stmt := range createIndexStmts {
		if _, err := conn.Exec(stmt); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	return migrate(conn)
}

// migrate adds columns introduced after a database was first created. Idempotent:
// each ADD COLUMN runs only when PRAGMA table_info shows it missing.
func migrate(conn *sql.DB) error {
	added := []struct{ col, typ string }{
		{"dtx_sid", "INTEGER"},
		{"dtx_count", "INTEGER"},
		{"dtx_total", "INTEGER"},
	}
	for _, table := range []string{"rtpmon_intervals", "rtpmon_statistics"} {
		existing, err := tableColumns(conn, table)
		if err != nil {
			return err
		}
		for _, a := range added {
			if !existing[a.col] {
				if _, err := conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, a.col, a.typ)); err != nil {
					return err
				}
			}
		}
	}
	streamCols, err := tableColumns(conn, "rtpmon_streams")
	if err != nil {
		return err
	}
	if !streamCols["created_ts"] {
		if _, err := conn.Exec("ALTER TABLE rtpmon_streams ADD COLUMN created_ts INTEGER"); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(conn *sql.DB, table string) (map[string]bool, error) {
	rows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}
