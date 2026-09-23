package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Open builds a *sql.DB from the SOCI-style config values and applies
// SQLite-friendly pragmas (WAL + busy_timeout) so reads never collide with the
// single writer. For an in-memory database it forces a single shared connection
// (matching the Python StaticPool).
//
// For a file database the pragmas go in the DSN, not through Exec: *sql.DB is a
// pool, and an Exec'd pragma reaches only the connection that ran it, so every
// other pooled connection had busy_timeout=0 and failed at once on contention.
// _txlock=immediate makes Begin take the write lock up front. Every transaction
// here writes, and a deferred one that reads first cannot be upgraded once
// another connection has committed: SQLite returns SQLITE_BUSY_SNAPSHOT (517)
// immediately, which busy_timeout does not retry.
//
// PostgreSQL and MySQL DSNs are produced by SOCIToDSN, but their drivers are not
// blank-imported here; wire them in when those engines are actually deployed.
func Open(engine, connection string) (*sql.DB, error) {
	driver, dsn, err := SOCIToDSN(engine, connection)
	if err != nil {
		return nil, err
	}
	if driver != DriverSQLite {
		return nil, fmt.Errorf("engine %q: driver %q is not compiled in (sqlite only for now)", engine, driver)
	}

	memory := dsn == ":memory:"
	if memory {
		dsn = "file::memory:?cache=shared"
	} else {
		dsn += sqliteParams(dsn)
	}
	conn, err := sql.Open(DriverSQLite, dsn)
	if err != nil {
		return nil, err
	}
	if memory {
		// A single connection keeps the shared in-memory DB alive for the pool's
		// lifetime and serializes access.
		conn.SetMaxOpenConns(1)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// sqliteParams returns the query string that applies the per-connection
// settings, joined to any query the configured path already carries.
func sqliteParams(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
}
