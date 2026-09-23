package stats

import "testing"

func TestParseActionValid(t *testing.T) {
	cases := map[string]PageAction{
		"mem_next":    {"mem", "next"},
		"db_last":     {"db", "last"},
		"mem_refresh": {"mem", "refresh"},
		"db_first":    {"db", "first"},
	}
	for in, want := range cases {
		got, ok := ParseAction(in)
		if !ok || got != want {
			t.Errorf("ParseAction(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
}

func TestParseActionInvalid(t *testing.T) {
	for _, in := range []string{"", "next", "foo_next", "mem_jump", "mem"} {
		if _, ok := ParseAction(in); ok {
			t.Errorf("ParseAction(%q) should be invalid", in)
		}
	}
}

func TestResolvePageOffset(t *testing.T) {
	cases := []struct {
		op                    string
		offset, maxCount, tot int
		want                  int
	}{
		{"first", 80, 50, 200, 0},
		{"last", 0, 50, 200, 150},
		{"last", 0, 50, 30, 0},
		{"prev", 100, 50, 200, 50},
		{"prev", 20, 50, 200, 0},
		{"next", 50, 50, 200, 100},
		{"next", 140, 50, 200, 150},
		{"next", 0, 50, 30, 0},
		{"refresh", 70, 50, 200, 70},
		{"refresh", -5, 50, 200, 0},
	}
	for _, c := range cases {
		got, err := ResolvePageOffset(c.op, c.offset, c.maxCount, c.tot)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.op, err)
			continue
		}
		if got != c.want {
			t.Errorf("ResolvePageOffset(%q,%d,%d,%d) = %d, want %d", c.op, c.offset, c.maxCount, c.tot, got, c.want)
		}
	}
}

func TestResolveUnknownOpErrors(t *testing.T) {
	if _, err := ResolvePageOffset("sideways", 0, 50, 10); err == nil {
		t.Error("expected error")
	}
}
