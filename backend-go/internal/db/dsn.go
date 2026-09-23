// Package db holds the schema bootstrap, the single-writer, read queries, the
// track store and connection setup. This file ports the SOCI connection-string
// parsing from the Python vq_db/db/engine.py, translating the C++ SOCI style
// (used in the shared vq-monitor.cfg) into a Go database/sql driver + DSN.
package db

import (
	"fmt"
	"strings"
)

// Driver names for database/sql registration.
const (
	DriverSQLite   = "sqlite" // modernc.org/sqlite
	DriverPostgres = "pgx"    // github.com/jackc/pgx/v5/stdlib
	DriverMySQL    = "mysql"  // github.com/go-sql-driver/mysql
)

var (
	sqliteEngines   = map[string]bool{"sqlite3": true, "sqlite": true}
	postgresEngines = map[string]bool{"postgresql": true, "postgres": true, "pgsql": true}
	mysqlEngines    = map[string]bool{"mysql": true}
)

// parseSOCI parses a SOCI "key=value" (space-separated) connection string.
// Values may be single- or double-quoted; unquoted values run to the next
// space. This mirrors Python engine._parse_soci exactly.
func parseSOCI(connection string) map[string]string {
	params := map[string]string{}
	i, n := 0, len(connection)
	for i < n {
		for i < n && isSpace(connection[i]) {
			i++
		}
		if i >= n {
			break
		}
		eq := strings.IndexByte(connection[i:], '=')
		if eq < 0 {
			break
		}
		eq += i
		key := strings.TrimSpace(connection[i:eq])
		j := eq + 1
		var value string
		if j < n && (connection[j] == '\'' || connection[j] == '"') {
			quote := connection[j]
			k := strings.IndexByte(connection[j+1:], quote)
			var end int
			if k < 0 {
				end = n
			} else {
				end = j + 1 + k
			}
			value = connection[j+1 : end]
			i = end + 1
		} else {
			k := j
			for k < n && !isSpace(connection[k]) {
				k++
			}
			value = connection[j:k]
			i = k
		}
		params[key] = value
	}
	return params
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}

// SOCIToDSN translates a SOCI engine + connection string into a Go database/sql
// driver name and DSN. Unlike the Python soci_to_url (which emits SQLAlchemy
// URLs), this emits DSNs for the Go drivers, but the SOCI parsing semantics are
// identical.
func SOCIToDSN(engine, connection string) (driver, dsn string, err error) {
	eng := strings.ToLower(strings.TrimSpace(engine))
	params := parseSOCI(connection)

	switch {
	case sqliteEngines[eng]:
		path := firstNonEmpty(params["db"], params["dbname"], strings.TrimSpace(connection))
		if path == "" || path == ":memory:" {
			return DriverSQLite, ":memory:", nil
		}
		return DriverSQLite, path, nil

	case postgresEngines[eng]:
		dbName := firstNonEmpty(params["dbname"], params["db"])
		user := params["user"]
		password := params["password"]
		host := firstNonEmpty(params["host"], "localhost")
		port := params["port"]
		auth := user
		if password != "" {
			auth = user + ":" + password
		}
		netloc := host
		if port != "" {
			netloc = host + ":" + port
		}
		cred := ""
		if auth != "" {
			cred = auth + "@"
		}
		return DriverPostgres, fmt.Sprintf("postgres://%s%s/%s", cred, netloc, dbName), nil

	case mysqlEngines[eng]:
		dbName := firstNonEmpty(params["dbname"], params["db"])
		user := params["user"]
		password := params["password"]
		host := firstNonEmpty(params["host"], "localhost")
		port := params["port"]
		auth := user
		if password != "" {
			auth = user + ":" + password
		}
		addr := host
		if port != "" {
			addr = host + ":" + port
		}
		cred := ""
		if auth != "" {
			cred = auth + "@"
		}
		return DriverMySQL, fmt.Sprintf("%stcp(%s)/%s", cred, addr, dbName), nil
	}

	return "", "", fmt.Errorf("unsupported database engine %q", engine)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// SafeConnDescription returns a human-readable, password-free description of a
// connection for logging (e.g. the SQLite file path, or "user@host/dbname" for
// PostgreSQL/MySQL) — never the raw connection string, which may carry a
// password.
func SafeConnDescription(engine, connection string) string {
	eng := strings.ToLower(strings.TrimSpace(engine))
	params := parseSOCI(connection)
	switch {
	case sqliteEngines[eng]:
		path := firstNonEmpty(params["db"], params["dbname"], strings.TrimSpace(connection))
		if path == "" || path == ":memory:" {
			return ":memory:"
		}
		return path
	case postgresEngines[eng], mysqlEngines[eng]:
		dbName := firstNonEmpty(params["dbname"], params["db"])
		host := firstNonEmpty(params["host"], "localhost")
		if port := params["port"]; port != "" {
			host += ":" + port
		}
		user := params["user"]
		if user != "" {
			return fmt.Sprintf("%s@%s/%s", user, host, dbName)
		}
		return fmt.Sprintf("%s/%s", host, dbName)
	}
	return eng
}
