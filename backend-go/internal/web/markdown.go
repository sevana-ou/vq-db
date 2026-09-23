package web

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// copyJSON is the "Copy JSON" payload: the verbatim json_report string when
// present, otherwise the whole map pretty-printed (matches the Flutter
// CopyJsonButton).
func copyJSON(data map[string]any) string {
	if s, ok := data["json_report"].(string); ok && s != "" {
		return s
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// mapToMarkdown ports the Flutter copy_markdown_button.dart converter:
// scalars as bullets, nested maps / lists-of-maps as subsections,
// semicolon-separated report strings as tables. json_report is excluded.
func mapToMarkdown(data map[string]any, title string) string {
	var b strings.Builder
	if title != "" {
		b.WriteString("# " + title + "\n\n")
	}
	writeMap(&b, data, 2)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeMap(b *strings.Builder, data map[string]any, level int) {
	keys := make([]string, 0, len(data))
	for k := range data {
		if k == "json_report" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Scalars first, then subsections, so bullets stay together.
	var nested []string
	for _, k := range keys {
		v := data[k]
		if isNested(v) {
			nested = append(nested, k)
			continue
		}
		writeScalar(b, k, v)
	}
	for _, k := range nested {
		heading := strings.Repeat("#", min(level, 6))
		b.WriteString("\n" + heading + " " + k + "\n\n")
		switch t := data[k].(type) {
		case map[string]any:
			writeMap(b, t, level+1)
		case []map[string]any:
			writeMapList(b, t, level+1)
		case []any:
			maps := make([]map[string]any, 0, len(t))
			for _, item := range t {
				if m, ok := item.(map[string]any); ok {
					maps = append(maps, m)
				}
			}
			writeMapList(b, maps, level+1)
		}
	}
}

func writeMapList(b *strings.Builder, items []map[string]any, level int) {
	for i, m := range items {
		title := "Item " + strconv.Itoa(i+1)
		s, sok := numOK(m["start_time"])
		e, eok := numOK(m["end_time"])
		if sok && eok {
			title = strconv.FormatFloat(s, 'f', 3, 64) + " - " + strconv.FormatFloat(e, 'f', 3, 64)
		}
		heading := strings.Repeat("#", min(level, 6))
		b.WriteString(heading + " " + title + "\n\n")
		writeMap(b, m, level+1)
		b.WriteString("\n")
	}
}

func writeScalar(b *strings.Builder, key string, v any) {
	switch t := v.(type) {
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = fmt.Sprint(item)
		}
		b.WriteString("- **" + key + "**: " + strings.Join(parts, ", ") + "\n")
	case string:
		if tbl, ok := semicolonTable(t); ok {
			b.WriteString("\n**" + key + "**\n\n" + tbl + "\n")
			return
		}
		if strings.Contains(t, "\n") {
			b.WriteString("- **" + key + "**:\n\n```\n" + t + "\n```\n")
			return
		}
		b.WriteString("- **" + key + "**: " + t + "\n")
	default:
		b.WriteString("- **" + key + "**: " + fmt.Sprint(v) + "\n")
	}
}

// semicolonTable renders a detector-report style string (>=2 non-empty lines
// that all contain ';') as a GitHub Markdown table.
func semicolonTable(s string) (string, bool) {
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) < 2 {
		return "", false
	}
	for _, ln := range lines {
		if !strings.Contains(ln, ";") {
			return "", false
		}
	}
	var b strings.Builder
	for i, ln := range lines {
		cells := strings.Split(ln, ";")
		for j, c := range cells {
			cells[j] = strings.ReplaceAll(strings.TrimSpace(c), "|", "\\|")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if i == 0 {
			seps := make([]string, len(cells))
			for j := range seps {
				seps[j] = "---"
			}
			b.WriteString("| " + strings.Join(seps, " | ") + " |\n")
		}
	}
	return b.String(), true
}

func isNested(v any) bool {
	switch t := v.(type) {
	case map[string]any, []map[string]any:
		return true
	case []any:
		for _, item := range t {
			if _, ok := item.(map[string]any); ok {
				return true
			}
		}
	}
	return false
}

func numOK(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	}
	return 0, false
}
