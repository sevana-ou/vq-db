package api

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Row is a DB/registry record: a key->value map, as produced by the read
// queries and the active-stream registry.
type Row = map[string]any

// getStr returns row[key] as a string, "" for nil/missing (matching Python
// `row[key] or ""`).
func getStr(row Row, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// getI64 coerces row[key] to int64, 0 for nil/missing/unparseable.
func getI64(row Row, key string) int64 {
	return toI64(row[key])
}

func toI64(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case uint32:
		return int64(t)
	case uint64:
		return int64(t)
	case float64:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return int64(f)
		}
	}
	return 0
}

// getF64 coerces row[key] to float64, 0 for nil/missing/unparseable.
func getF64(row Row, key string) float64 {
	return toF64(row[key])
}

func toF64(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case uint32:
		return float64(t)
	case uint64:
		return float64(t)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return f
		}
	}
	return 0
}

// getBool coerces row[key] to bool (nonzero number / nonempty string / true).
func getBool(row Row, key string) bool {
	switch t := row[key].(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case int:
		return t != 0
	case float64:
		return t != 0
	case string:
		return t != ""
	}
	return false
}

// round3 rounds to 3 decimals using round-half-to-even, matching Python round().
func round3(v float64) float64 {
	return math.RoundToEven(v*1000) / 1000
}

// hexOf formats an integer value as lowercase hex (Python format(int(v), "x")).
func hexOf(v any) string {
	return strconv.FormatInt(toI64(v), 16)
}

// endpoint formats "ip:port".
func endpoint(ip string, port any) string {
	return fmt.Sprintf("%s:%v", ip, portString(port))
}

func portString(port any) string {
	switch t := port.(type) {
	case nil:
		return "0"
	case string:
		return t
	default:
		return strconv.FormatInt(toI64(port), 10)
	}
}

// startTimeLabel formats a Unix-ms timestamp as "YYYY-MM-DD HH:MM:SS.mmm" in
// local time (the string the C++ dashboard emitted, rendered verbatim).
func startTimeLabel(ms int64) string {
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05.000")
}
