package filter

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

func encode(field, direction string, offset, count int, expression string) string {
	parts := []string{field, direction, strconv.Itoa(offset), strconv.Itoa(count)}
	if expression != "" {
		parts = append(parts, base64.StdEncoding.EncodeToString([]byte(expression)))
	}
	return strings.Join(parts, "/")
}

func TestParseWithoutExpression(t *testing.T) {
	f := ParseSearchFilter("sevana_mos/asc/20/40")
	if f.SortField != "sevana_mos" || f.Descending || f.PageOffset != 20 || f.MaxCount != 40 || f.Expression != "" {
		t.Errorf("got %+v", f)
	}
}

func TestParseWithExpression(t *testing.T) {
	image := encode("start_time", "desc", 0, 50, "sevana_mos < 3.6")
	f := ParseSearchFilter(image)
	if f.SortField != "start_time" || !f.Descending || f.Expression != "sevana_mos < 3.6" {
		t.Errorf("got %+v", f)
	}
}

func TestParseTooFewTokensKeepsDefaults(t *testing.T) {
	f := ParseSearchFilter("only/two")
	if f.SortField != "start_time" || f.MaxCount != 50 {
		t.Errorf("got %+v", f)
	}
}

func TestParseEmbeddedSlashPreserved(t *testing.T) {
	expr := `sevana_mos < 3 && sip_src == "a/b"`
	payload := base64.StdEncoding.EncodeToString([]byte(expr))
	image := strings.Join([]string{"start_time", "desc", "0", "50", payload}, "/")
	f := ParseSearchFilter(image)
	if f.Expression != expr {
		t.Errorf("got %q", f.Expression)
	}
}

func TestParseURLSafeBase64(t *testing.T) {
	expr := `sip_src == "sip:alice@host?x=1"`
	payload := strings.TrimRight(base64.URLEncoding.EncodeToString([]byte(expr)), "=")
	image := strings.Join([]string{"start_time", "desc", "0", "50", payload}, "/")
	f := ParseSearchFilter(image)
	if f.Expression != expr {
		t.Errorf("got %q", f.Expression)
	}
}

func TestParseGarbageExpressionDegradesToEmpty(t *testing.T) {
	f := ParseSearchFilter("start_time/desc/0/50/!!!not-base64!!!")
	if f.Expression != "" {
		t.Errorf("got %q", f.Expression)
	}
}

func TestRoundTripToString(t *testing.T) {
	f := SearchFilter{SortField: "sevana_mos", Descending: false, PageOffset: 10, MaxCount: 25, Expression: "jitter > 20"}
	re := ParseSearchFilter(f.String())
	if re.SortField != "sevana_mos" || re.Descending || re.PageOffset != 10 || re.MaxCount != 25 || re.Expression != "jitter > 20" {
		t.Errorf("got %+v", re)
	}
}

func TestDeriveWithSort(t *testing.T) {
	f := SearchFilter{SortField: "start_time", Descending: true, Expression: "jitter > 1"}
	d := f.DeriveWithSort("sevana_mos", false)
	if d.SortField != "sevana_mos" || d.Descending || d.Expression != "jitter > 1" {
		t.Errorf("got %+v", d)
	}
}

func TestPercent2FUnescaped(t *testing.T) {
	f := ParseSearchFilter("start_time/desc/0/50")
	if f.SortField != "start_time" {
		t.Errorf("got %+v", f)
	}
}
