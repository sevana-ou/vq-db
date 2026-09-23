package filter

// SearchFilter transport: parse/serialize the "field/dir/offset/count/<b64expr>"
// string the frontend sends as mem_filter / db_filter. Port of
// vq_db/filter/search_filter.py. The 5th token is base64; parsing is defensive
// about standard vs URL-safe alphabets and missing padding, degrading to an
// empty expression rather than erroring.

import (
	"encoding/base64"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DateInterval is an inclusive [start, end] window in Unix seconds.
type DateInterval struct {
	StartSeconds int64
	EndSeconds   int64
}

// SearchFilter is the decoded mem_filter/db_filter transport.
type SearchFilter struct {
	PageOffset   int
	MaxCount     int
	SortField    string
	Descending   bool
	Expression   string
	DateInterval *DateInterval
	// SIPCallID is an optional exact-match on the correlated SIP Call-ID, handled
	// outside the filter mini-language. Empty = no filter.
	SIPCallID string
}

// NewSearchFilter returns a SearchFilter with the same defaults as the Python
// dataclass (offset 0, count 50, sort start_time, descending).
func NewSearchFilter() SearchFilter {
	return SearchFilter{PageOffset: 0, MaxCount: 50, SortField: "start_time", Descending: true}
}

// ParseSearchFilter parses a transport string into a SearchFilter.
func ParseSearchFilter(image string) SearchFilter {
	f := NewSearchFilter()
	f.Parse(image)
	return f
}

// Parse populates fields from a transport string. Tolerant of fewer than four
// tokens (leaves defaults in place).
func (f *SearchFilter) Parse(image string) {
	// The C++ replaces literal "%2F" with "/" before splitting.
	decoded := strings.ReplaceAll(image, "%2F", "/")
	// maxsplit keeps any '/' inside the base64 payload intact.
	tokens := strings.SplitN(decoded, "/", 5)
	if len(tokens) < 4 {
		return
	}
	f.SortField = tokens[0]
	f.Descending = tokens[1] != "asc"
	f.PageOffset = safeInt(tokens[2], f.PageOffset)
	f.MaxCount = safeInt(tokens[3], f.MaxCount)

	if len(tokens) >= 5 && tokens[4] != "" {
		f.Expression = strings.TrimSpace(b64DecodeText(tokens[4]))
	}
}

// String serializes back to the transport form (base64 for the expression).
func (f SearchFilter) String() string {
	direction := "desc"
	if !f.Descending {
		direction = "asc"
	}
	parts := []string{f.SortField, direction, strconv.Itoa(f.PageOffset), strconv.Itoa(f.MaxCount)}
	if f.Expression != "" {
		parts = append(parts, base64.StdEncoding.EncodeToString([]byte(f.Expression)))
	}
	return strings.Join(parts, "/")
}

// DeriveWithSort returns a copy with a new sort field/direction, carrying the
// rest of the filter state.
func (f SearchFilter) DeriveWithSort(field string, descending bool) SearchFilter {
	return SearchFilter{
		PageOffset:   f.PageOffset,
		MaxCount:     f.MaxCount,
		SortField:    field,
		Descending:   descending,
		Expression:   f.Expression,
		DateInterval: f.DateInterval,
		SIPCallID:    f.SIPCallID,
	}
}

func safeInt(text string, def int) int {
	if v, err := strconv.Atoi(text); err == nil {
		return v
	}
	return def
}

// b64DecodeText decodes a base64 token to text, tolerating standard and
// URL-safe alphabets and missing padding. Returns "" on garbage.
func b64DecodeText(token string) string {
	padding := (4 - len(token)%4) % 4
	padded := token + strings.Repeat("=", padding)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(padded); err == nil && utf8.Valid(b) {
			return string(b)
		}
	}
	return ""
}
