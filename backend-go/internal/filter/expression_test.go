package filter

import (
	"math"
	"reflect"
	"testing"
)

// --------------------------------------------------------------------------- //
// Tokenizer
// --------------------------------------------------------------------------- //

func mustTokenize(t *testing.T, s string) []Token {
	t.Helper()
	toks, err := Tokenize(s)
	if err != nil {
		t.Fatalf("Tokenize(%q) error: %v", s, err)
	}
	return toks
}

func TestTokenizeNumbersAndOps(t *testing.T) {
	got := mustTokenize(t, "sevana_mos < 3.6")
	want := []Token{{"ident", "sevana_mos"}, {"op", "<"}, {"number", 3.6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTokenizeIntegerVsFloat(t *testing.T) {
	if v := mustTokenize(t, "3")[0].Value; v != int64(3) {
		t.Errorf("3 -> %#v", v)
	}
	if v := mustTokenize(t, "3.6")[0].Value; v != float64(3.6) {
		t.Errorf("3.6 -> %#v", v)
	}
}

func TestTokenizeHex(t *testing.T) {
	if v := mustTokenize(t, "0x1a2b")[0].Value; v != int64(0x1a2b) {
		t.Errorf("0x1a2b -> %#v", v)
	}
}

func TestTokenizeIPBecomesString(t *testing.T) {
	toks := mustTokenize(t, "src_ip == 10.0.0.1")
	last := toks[len(toks)-1]
	if last != (Token{"string", "10.0.0.1"}) {
		t.Errorf("last token = %v", last)
	}
}

func TestTokenizeQuotedString(t *testing.T) {
	toks := mustTokenize(t, `sip_src == "sip:alice@host"`)
	last := toks[len(toks)-1]
	if last != (Token{"string", "sip:alice@host"}) {
		t.Errorf("last token = %v", last)
	}
}

func TestTokenizeTwoCharOperators(t *testing.T) {
	toks := mustTokenize(t, "a <= b >= c == d != e && f || g")
	var vals []any
	for _, tk := range toks {
		vals = append(vals, tk.Value)
	}
	want := []any{"a", "<=", "b", ">=", "c", "==", "d", "!=", "e", "&&", "f", "||", "g"}
	if !reflect.DeepEqual(vals, want) {
		t.Errorf("got %v, want %v", vals, want)
	}
}

func TestTokenizeRejectsLoneEquals(t *testing.T) {
	if _, err := Tokenize("a = b"); err == nil {
		t.Error("expected error for lone =")
	}
}

func TestTokenizeRejectsUnterminatedString(t *testing.T) {
	if _, err := Tokenize(`a == "oops`); err == nil {
		t.Error("expected error for unterminated string")
	}
}

// --------------------------------------------------------------------------- //
// Parser & precedence
// --------------------------------------------------------------------------- //

func mustParse(t *testing.T, s string) Node {
	t.Helper()
	n, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", s, err)
	}
	return n
}

func TestParseSimple(t *testing.T) {
	got := mustParse(t, "sevana_mos < 3")
	want := BinOp{"<", Var{"sevana_mos"}, Number{int64(3)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestPrecedenceComparisonTighterThanAnd(t *testing.T) {
	got := mustParse(t, "a < 3 && b > 4")
	want := BinOp{"&&", BinOp{"<", Var{"a"}, Number{int64(3)}}, BinOp{">", Var{"b"}, Number{int64(4)}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestPrecedenceAndTighterThanOr(t *testing.T) {
	got := mustParse(t, "a || b && c")
	want := BinOp{"||", Var{"a"}, BinOp{"&&", Var{"b"}, Var{"c"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestPrecedenceArithmetic(t *testing.T) {
	got := mustParse(t, "a + b * c")
	want := BinOp{"+", Var{"a"}, BinOp{"*", Var{"b"}, Var{"c"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestLeftAssociativity(t *testing.T) {
	got := mustParse(t, "a - b - c")
	want := BinOp{"-", BinOp{"-", Var{"a"}, Var{"b"}}, Var{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestBracketsOverridePrecedence(t *testing.T) {
	got := mustParse(t, "(a || b) && c")
	want := BinOp{"&&", BinOp{"||", Var{"a"}, Var{"b"}}, Var{"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "a &&", "a b", "(a", "a)", "* a"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) expected error", bad)
		}
	}
}

// --------------------------------------------------------------------------- //
// SQL emission
// --------------------------------------------------------------------------- //

func mustWhere(t *testing.T, expr, dialect string) SQLFragment {
	t.Helper()
	frag, err := BuildWhere(expr, dialect)
	if err != nil {
		t.Fatalf("BuildWhere(%q) error: %v", expr, err)
	}
	return frag
}

func TestSQLBasicNumericInline(t *testing.T) {
	frag := mustWhere(t, "sevana_mos < 3.6", "qmark")
	if frag.Text != "(rtpmon_statistics.sevana_mos < 3.6)" {
		t.Errorf("text = %q", frag.Text)
	}
	if len(frag.Params) != 0 {
		t.Errorf("params = %v", frag.Params)
	}
}

func TestSQLStringIsParameterized(t *testing.T) {
	frag := mustWhere(t, `sip_src == "sip:alice@host"`, "qmark")
	if frag.Text != "(rtpmon_streams.sip_source = ?)" {
		t.Errorf("text = %q", frag.Text)
	}
	if len(frag.Params) != 1 || frag.Params[0] != "sip:alice@host" {
		t.Errorf("params = %v", frag.Params)
	}
}

func TestSQLEqualityInequalityOperators(t *testing.T) {
	if got := mustWhere(t, "sevana_mos == 3", "qmark").Text; got != "(rtpmon_statistics.sevana_mos = 3)" {
		t.Errorf("== -> %q", got)
	}
	if got := mustWhere(t, "sevana_mos != 3", "qmark").Text; got != "(rtpmon_statistics.sevana_mos <> 3)" {
		t.Errorf("!= -> %q", got)
	}
}

func TestSQLBooleanKeywords(t *testing.T) {
	got := mustWhere(t, "sevana_mos < 3 && network_mos > 2", "qmark").Text
	want := "((rtpmon_statistics.sevana_mos < 3) AND (rtpmon_statistics.network_mos > 2))"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestSQLNetworkMosOwnColumn(t *testing.T) {
	if got := mustWhere(t, "network_mos > 3", "qmark").Text; got != "(rtpmon_statistics.network_mos > 3)" {
		t.Errorf("got %q", got)
	}
}

func TestSQLJitterFilterable(t *testing.T) {
	if got := mustWhere(t, "jitter > 20", "qmark").Text; got != "(rtpmon_statistics.jitter > 20)" {
		t.Errorf("got %q", got)
	}
}

func TestSQLStartTimeSeconds(t *testing.T) {
	got := mustWhere(t, "start_time > 1700000000", "qmark").Text
	if got != "((rtpmon_statistics.start_timestamp / 1000) > 1700000000)" {
		t.Errorf("got %q", got)
	}
}

func TestSQLDurationEndMinusStart(t *testing.T) {
	got := mustWhere(t, "duration > 10000", "qmark").Text
	if got != "((rtpmon_statistics.end_timestamp - rtpmon_statistics.start_timestamp) > 10000)" {
		t.Errorf("got %q", got)
	}
}

func TestSQLSilenceRatioFilterable(t *testing.T) {
	got := mustWhere(t, "silence_ratio >= 0.8", "qmark").Text
	want := "((CAST(COALESCE(rtpmon_statistics.dtx_sid, 0)" +
		" + COALESCE(rtpmon_statistics.dtx_count, 0) AS REAL)" +
		" / NULLIF(rtpmon_statistics.dtx_total, 0)) >= 0.8)"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestOrderBySilenceRatio(t *testing.T) {
	got := BuildOrderBy("silence_ratio", true)
	want := "(CAST(COALESCE(rtpmon_statistics.dtx_sid, 0)" +
		" + COALESCE(rtpmon_statistics.dtx_count, 0) AS REAL)" +
		" / NULLIF(rtpmon_statistics.dtx_total, 0)) desc"
	if got != want {
		t.Errorf("got %q", got)
	}
}

func TestSQLUnknownFieldRejected(t *testing.T) {
	if _, err := BuildWhere("bogus_field > 1", "qmark"); err == nil {
		t.Error("expected error")
	}
}

func TestSQLFormatDialect(t *testing.T) {
	frag := mustWhere(t, `sip_callid == "abc"`, "format")
	if frag.Text != "(rtpmon_streams.sip_callid = %s)" {
		t.Errorf("text = %q", frag.Text)
	}
	if len(frag.Params) != 1 || frag.Params[0] != "abc" {
		t.Errorf("params = %v", frag.Params)
	}
}

func TestSQLNumericDialectRenumbers(t *testing.T) {
	frag := mustWhere(t, `sip_src == "a" && sip_dst == "b"`, "numeric")
	want := "((rtpmon_streams.sip_source = :1) AND (rtpmon_streams.sip_destination = :2))"
	if frag.Text != want {
		t.Errorf("text = %q", frag.Text)
	}
	if !reflect.DeepEqual(frag.Params, []any{"a", "b"}) {
		t.Errorf("params = %v", frag.Params)
	}
}

func TestSQLInjectionValueStaysParameter(t *testing.T) {
	frag := mustWhere(t, `sip_callid == "x'; drop table rtpmon_streams; --"`, "qmark")
	if len(frag.Params) != 1 || frag.Params[0] != "x'; drop table rtpmon_streams; --" {
		t.Errorf("params = %v", frag.Params)
	}
	if frag.Text != "(rtpmon_streams.sip_callid = ?)" {
		t.Errorf("text = %q", frag.Text)
	}
}

func TestOrderByKnownField(t *testing.T) {
	if got := BuildOrderBy("sevana_mos", true); got != "rtpmon_statistics.sevana_mos desc" {
		t.Errorf("got %q", got)
	}
}

func TestOrderByDurationExpression(t *testing.T) {
	got := BuildOrderBy("duration", false)
	if got != "(rtpmon_statistics.end_timestamp - rtpmon_statistics.start_timestamp) asc" {
		t.Errorf("got %q", got)
	}
}

func TestOrderByUnknownFallsBack(t *testing.T) {
	if got := BuildOrderBy("haxx; drop", true); got != "rtpmon_statistics.stream_id desc" {
		t.Errorf("got %q", got)
	}
}

// --------------------------------------------------------------------------- //
// In-memory evaluation
// --------------------------------------------------------------------------- //

var sample = map[string]any{
	"start_time":     int64(1_700_000),
	"sevana_mos":     3.2,
	"network_mos":    4.1,
	"sevana_rfactor": int64(80),
	"jitter":         25.0,
	"rtt_delay":      12.0,
	"duration":       int64(30_000),
	"src_ip":         "10.0.0.1",
	"dst_ip":         "10.0.0.2",
	"src_port":       int64(5004),
	"dst_port":       int64(5060),
	"ssrc":           "12345",
	"sip_src":        "sip:alice@host",
	"sip_dst":        "sip:bob@host",
	"sip_callid":     "call-123",
	"instance_id":    "agent_1",
	"instance_name":  "First instance",
	"silence_ratio":  0.9,
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"sevana_mos < 3.6", true},
		{"sevana_mos > 3.6", false},
		{"sevana_mos < 3.6 && network_mos > 3", true},
		{"jitter > 20 || sevana_rfactor < 60", true},
		{"jitter > 30 || sevana_rfactor < 60", false},
		{`src_ip == "10.0.0.1"`, true},
		{`sip_src == "sip:alice@host" && sevana_mos <= 4`, true},
		{`(sevana_mos < 3 || jitter > 20) && dst_port == 5060`, true},
		{"duration > 10", true},
		{"sevana_rfactor == 80", true},
		{"silence_ratio >= 0.8", true},
		{"silence_ratio >= 0.95", false},
	}
	for _, c := range cases {
		if got := EvaluateStream(c.expr, sample); got != c.want {
			t.Errorf("EvaluateStream(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvaluateMissingVariableExcludes(t *testing.T) {
	if EvaluateStream("unknown_field > 1", sample) {
		t.Error("expected false")
	}
}

func TestEvaluateArithmetic(t *testing.T) {
	if !EvaluateStream("src_port + 56 == 5060", map[string]any{"src_port": int64(5004)}) {
		t.Error("expected true")
	}
}

func TestEvaluateIntegerDivisionTruncatesTowardZero(t *testing.T) {
	n := mustParse(t, "a / b")
	got, err := n.Evaluate(map[string]any{"a": int64(7), "b": int64(2)})
	if err != nil || got != int64(3) {
		t.Errorf("7/2 = %v (%v)", got, err)
	}
	got, err = n.Evaluate(map[string]any{"a": int64(-7), "b": int64(2)})
	if err != nil || got != int64(-3) {
		t.Errorf("-7/2 = %v (%v)", got, err)
	}
}

func TestEvaluateDivisionByZeroRaises(t *testing.T) {
	n := mustParse(t, "a / b")
	if _, err := n.Evaluate(map[string]any{"a": int64(1), "b": int64(0)}); err == nil {
		t.Error("expected error")
	}
}

func TestEvaluateFloatDivision(t *testing.T) {
	n := mustParse(t, "a / b")
	got, err := n.Evaluate(map[string]any{"a": 7.0, "b": int64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := got.(float64); math.Abs(f-3.5) > 1e-9 {
		t.Errorf("7.0/2 = %v", got)
	}
}

// --------------------------------------------------------------------------- //
// pyReprFloat parity spot-checks
// --------------------------------------------------------------------------- //

func TestPyReprFloat(t *testing.T) {
	cases := map[float64]string{
		3.6:        "3.6",
		0.8:        "0.8",
		3.0:        "3.0",
		300.0:      "300.0",
		0.0001:     "0.0001",
		0.00001:    "1e-05",
		0.0:        "0.0",
		1e16:       "1e+16",
		1.5e16:     "1.5e+16",
		123456.789: "123456.789",
	}
	for in, want := range cases {
		if got := pyReprFloat(in); got != want {
			t.Errorf("pyReprFloat(%v) = %q, want %q", in, got, want)
		}
	}
}
