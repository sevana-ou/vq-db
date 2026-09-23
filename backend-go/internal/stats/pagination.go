package stats

import (
	"fmt"
	"strings"
)

// PageAction is a parsed <scope>_<op> repaging action.
type PageAction struct {
	Scope string // "mem" | "db"
	Op    string // "first" | "last" | "prev" | "next" | "refresh"
}

var scopes = map[string]bool{"mem": true, "db": true}
var ops = map[string]bool{"first": true, "last": true, "prev": true, "next": true, "refresh": true}

// ParseAction parses "<scope>_<op>" into a PageAction. The bool is false if the
// string is empty or not a recognized scope/op pair.
func ParseAction(action string) (PageAction, bool) {
	if action == "" {
		return PageAction{}, false
	}
	scope, op, found := strings.Cut(action, "_")
	if !found || !scopes[scope] || !ops[op] {
		return PageAction{}, false
	}
	return PageAction{Scope: scope, Op: op}, true
}

// ResolvePageOffset computes the new page offset for a repaging op, clamped so
// the offset never goes negative or past the last full page. Unknown ops error.
func ResolvePageOffset(op string, offset, maxCount, total int) (int, error) {
	lastPage := max0(total - maxCount)
	switch op {
	case "first":
		return 0, nil
	case "last":
		return lastPage, nil
	case "prev":
		return max0(offset - maxCount), nil
	case "next":
		return max0(min(lastPage, offset+maxCount)), nil
	case "refresh":
		return max0(offset), nil
	}
	return 0, fmt.Errorf("unknown paging op %q", op)
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
