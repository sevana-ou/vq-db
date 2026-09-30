package filter

// The SIP calls list filter: the same expression language over one row per
// call (alias "c", see api.GetSipCallList). MOS is the call's worst finished
// RTP stream, matched by Call-ID; for Sevana MOS only streams PVQA analysed,
// and 0 when there are none -- as a stream PVQA skipped has sevana_mos 0.

const callStreamsSQL = " from rtpmon_streams s join rtpmon_statistics t on t.stream_id = s.stream_id" +
	" where s.sip_callid = c.call_id)"

// SipCallNameMap maps a SIP calls filter field to its SQL expression.
var SipCallNameMap = map[string]string{
	"caller":      "c.caller",
	"callee":      "c.callee",
	"call_id":     "c.call_id",
	"start_time":  "(c.start_timestamp / 1000)",
	"duration":    "c.duration",
	"code":        "COALESCE(c.response_code, c.setup_code)",
	"status":      "(CASE WHEN c.failed = 1 THEN 'failed' WHEN c.established = 1 THEN 'established' ELSE 'unknown' END)",
	"reinvites":   "c.reinvite_count",
	"sevana_mos":  "COALESCE((select min(nullif(t.sevana_mos, 0))" + callStreamsSQL + ", 0)",
	"network_mos": "(select min(t.network_mos)" + callStreamsSQL,
}

// BuildSipCallWhere compiles a SIP calls filter expression into a
// parameterized SQL condition over the per-call row "c".
func BuildSipCallWhere(expression, dialect string) (SQLFragment, error) {
	node, err := Parse(expression)
	if err != nil {
		return SQLFragment{}, err
	}
	return node.ToSQL(SipCallNameMap, dialect)
}
