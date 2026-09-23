package filter

// This file ports vq_db/filter/stream_filter.py: the SQL column name-map, the
// ORDER BY sort whitelist, and the build_where/build_order_by/evaluate_stream
// glue. The maps correct the C++ latent bugs (network_mos, duration, jitter,
// start_time units) rather than reproducing them — see FILTER.md §6.

const silenceRatioSQL = "(CAST(COALESCE(rtpmon_statistics.dtx_sid, 0)" +
	" + COALESCE(rtpmon_statistics.dtx_count, 0) AS REAL)" +
	" / NULLIF(rtpmon_statistics.dtx_total, 0))"

// SQLNameMap maps a filter field to its SQL column/expression. start_time is
// divided to seconds so it matches the in-memory value-map unit.
var SQLNameMap = map[string]string{
	"start_time":      "(rtpmon_statistics.start_timestamp / 1000)",
	"sevana_mos":      "rtpmon_statistics.sevana_mos",
	"sevana_rfactor":  "rtpmon_statistics.sevana_rfactor",
	"network_mos":     "rtpmon_statistics.network_mos",
	"jitter":          "rtpmon_statistics.jitter",
	"rtt_delay":       "rtpmon_statistics.rtt_delay",
	"silence_ratio":   silenceRatioSQL,
	"duration":        "(rtpmon_statistics.end_timestamp - rtpmon_statistics.start_timestamp)",
	"duration_audio":  "rtpmon_statistics.duration_audio",
	"src_ip":          "rtpmon_streams.src_ip",
	"src_port":        "rtpmon_streams.src_port",
	"dst_ip":          "rtpmon_streams.dst_ip",
	"dst_port":        "rtpmon_streams.dst_port",
	"sip_source":      "rtpmon_streams.sip_source",
	"sip_src":         "rtpmon_streams.sip_source",
	"sip_destination": "rtpmon_streams.sip_destination",
	"sip_dst":         "rtpmon_streams.sip_destination",
	"sip_callid":      "rtpmon_streams.sip_callid",
	"ssrc":            "rtpmon_streams.ssrc",
	"instance_id":     "rtpmon_instances.agent_id",
	"instance_name":   "rtpmon_instances.agent_name",
}

// SortColumns is the ORDER BY whitelist (filter sort_field -> SQL expression).
// The empty string is the default; unknown fields fall back to it.
var SortColumns = map[string]string{
	"":               "rtpmon_statistics.stream_id",
	"duration":       "(rtpmon_statistics.end_timestamp - rtpmon_statistics.start_timestamp)",
	"jitter":         "rtpmon_statistics.jitter",
	"silence_ratio":  silenceRatioSQL,
	"sevana_mos":     "rtpmon_statistics.sevana_mos",
	"network_mos":    "rtpmon_statistics.network_mos",
	"start_time":     "rtpmon_statistics.start_timestamp",
	"end_time":       "rtpmon_statistics.end_timestamp",
	"src_ip":         "rtpmon_streams.src_ip",
	"dst_ip":         "rtpmon_streams.dst_ip",
	"sip_src":        "rtpmon_streams.sip_source",
	"sip_dst":        "rtpmon_streams.sip_destination",
	"sevana_rfactor": "rtpmon_statistics.sevana_rfactor",
}

// BuildWhere compiles a filter expression string into a parameterized SQL WHERE
// fragment. dialect is one of "qmark", "format", "numeric".
func BuildWhere(expression, dialect string) (SQLFragment, error) {
	node, err := Parse(expression)
	if err != nil {
		return SQLFragment{}, err
	}
	return node.ToSQL(SQLNameMap, dialect)
}

// BuildWhereNode compiles an already-parsed node.
func BuildWhereNode(node Node, dialect string) (SQLFragment, error) {
	return node.ToSQL(SQLNameMap, dialect)
}

// BuildOrderBy returns a safe ORDER BY expression + direction for a whitelisted
// sort field. Unknown fields fall back to the stream_id default.
func BuildOrderBy(sortField string, descending bool) string {
	column, ok := SortColumns[sortField]
	if !ok {
		column = SortColumns[""]
	}
	direction := "asc"
	if descending {
		direction = "desc"
	}
	return column + " " + direction
}

// EvaluateStream evaluates the in-memory predicate for one active stream's
// value-map. Any error means "exclude" (returns false), matching the C++/Python
// active-list semantics.
func EvaluateStream(expression string, values map[string]any) bool {
	node, err := Parse(expression)
	if err != nil {
		return false
	}
	result, err := node.Evaluate(values)
	if err != nil {
		return false
	}
	b, err := asBool(result)
	if err != nil {
		return false
	}
	return b
}

// EvaluateStreamNode is EvaluateStream for a pre-parsed node.
func EvaluateStreamNode(node Node, values map[string]any) bool {
	result, err := node.Evaluate(values)
	if err != nil {
		return false
	}
	b, err := asBool(result)
	if err != nil {
		return false
	}
	return b
}
