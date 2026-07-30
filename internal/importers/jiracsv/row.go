package jiracsv

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// header is a parsed CSV header: the column order plus, for each normalized
// name, every index it appears at.
//
// Jira's "all fields" export repeats a column once per value — three comments
// means three `Comment` columns, and the same is true of Attachment, Watchers,
// Component/s, Sprint, Labels, versions, and issue links. Refusing repeated
// columns therefore rejects the very file the export button produces, so the
// only names treated as single-valued are the ones we index positionally.
type header struct {
	names  []string
	byName map[string][]int
	dupes  []string
}

// singleValued names columns where a repeat would mean the file is malformed
// rather than multi-valued: Jira never emits two Summary or two Issue key
// columns, and silently picking one would import the wrong data.
var singleValued = map[string]bool{
	"summary":     true,
	"issue key":   true,
	"issue id":    true,
	"status":      true,
	"priority":    true,
	"issue type":  true,
	"resolution":  true,
	"assignee":    true,
	"reporter":    true,
	"creator":     true,
	"created":     true,
	"updated":     true,
	"resolved":    true,
	"due date":    true,
	"parent":      true,
	"parent id":   true,
	"project key": true,
}

func parseHeader(raw []string) (*header, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("csv header is empty")
	}
	raw[0] = strings.TrimPrefix(raw[0], "\ufeff")

	h := &header{names: make([]string, len(raw)), byName: map[string][]int{}}
	blank := 0
	for index, cell := range raw {
		name := normalizeColumn(cell)
		if name == "" {
			// Jira pads exports with trailing empty header cells. Keep the
			// column so record width still lines up, but give it a unique
			// unreachable name instead of failing the whole file.
			blank++
			name = fmt.Sprintf("\x00blank-%d", blank)
		}
		h.names[index] = name
		h.byName[name] = append(h.byName[name], index)
	}

	for name, indexes := range h.byName {
		if len(indexes) > 1 && singleValued[name] {
			h.dupes = append(h.dupes, name)
		}
	}
	if len(h.dupes) > 0 {
		return nil, fmt.Errorf(
			"csv repeats the single-valued header(s) %s; export one Jira project at a time",
			strings.Join(h.dupes, ", "),
		)
	}
	if len(h.byName["summary"]) == 0 {
		return nil, fmt.Errorf(`csv is missing required "Summary" header`)
	}
	if len(h.byName["issue key"]) == 0 {
		return nil, fmt.Errorf(`csv is missing required "Issue key" header`)
	}
	return h, nil
}

// normalizeColumn folds a Jira header cell to its lookup key. Jira decorates
// some headers ("Σ Original Estimate") and wraps custom fields, so casing and
// surrounding whitespace are never significant.
func normalizeColumn(cell string) string {
	return strings.ToLower(strings.Join(strings.Fields(cell), " "))
}

// row exposes one CSV record through the header. Values are returned in column
// order, and empty cells are skipped so a repeated column reads as a list.
type row struct {
	header *header
	cells  []string
}

func (r row) all(names ...string) []string {
	var out []string
	for _, name := range names {
		for _, index := range r.header.byName[normalizeColumn(name)] {
			if index >= len(r.cells) {
				continue
			}
			if value := strings.TrimSpace(r.cells[index]); value != "" {
				out = append(out, value)
			}
		}
		if len(out) > 0 {
			// Names are alternatives in priority order (Jira renamed several
			// fields across versions), so stop at the first one present.
			return out
		}
	}
	return out
}

// first returns the first non-empty value across the given alternative names.
func (r row) first(names ...string) string {
	values := r.all(names...)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// custom reads a Jira custom field by its display name, matching both the
// wrapped form Jira exports and the bare name.
func (r row) custom(names ...string) string {
	alternatives := make([]string, 0, len(names)*2)
	for _, name := range names {
		alternatives = append(alternatives, "custom field ("+name+")", name)
	}
	return r.first(alternatives...)
}

// jiraTimeLayouts covers the formats Jira writes into CSV exports. The default
// is `dd/MMM/yy h:mm a`, but instances configure it, and Jira Data Center and
// some Cloud exports emit ISO-like stamps instead.
var jiraTimeLayouts = []string{
	"2/Jan/06 3:04 PM",
	"02/Jan/2006 15:04",
	"2/Jan/2006 3:04 PM",
	"2/Jan/06 15:04",
	"2/Jan/06",
	"2/Jan/2006",
	"2006-01-02 15:04:05.0",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04",
	"2006-01-02",
	"01/02/2006 15:04",
	"01/02/2006",
}

// parseJiraTime parses a Jira CSV timestamp. Values are stored without a zone,
// so they are read as UTC rather than guessing the exporting instance's zone.
func parseJiraTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range jiraTimeLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseNumber reads a Jira numeric cell, tolerating thousands separators and
// the comma decimal mark some locales export.
func parseNumber(value string) (float64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	cleaned := strings.ReplaceAll(value, " ", "")
	if strings.Count(cleaned, ",") == 1 && !strings.Contains(cleaned, ".") {
		cleaned = strings.Replace(cleaned, ",", ".", 1)
	} else {
		cleaned = strings.ReplaceAll(cleaned, ",", "")
	}
	parsed, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// parseSeconds reads a Jira time-tracking cell, which is exported in seconds.
func parseSeconds(value string) (int, bool) {
	seconds, ok := parseNumber(value)
	if !ok || seconds < 0 {
		return 0, false
	}
	return int(seconds), true
}

// jiraComment splits a Jira CSV comment cell. Jira writes
// `date;author;body`, and the body itself may contain semicolons, so only the
// first two separators are consumed. Cells that do not carry the prefix are
// treated as a bare body.
func jiraComment(cell string) (author string, created time.Time, body string) {
	cell = strings.TrimSpace(cell)
	head, rest, found := strings.Cut(cell, ";")
	if !found {
		return "", time.Time{}, cell
	}
	when, ok := parseJiraTime(head)
	if !ok {
		return "", time.Time{}, cell
	}
	author, body, found = strings.Cut(rest, ";")
	if !found {
		return "", when, strings.TrimSpace(rest)
	}
	return strings.TrimSpace(author), when, strings.TrimSpace(body)
}

// jiraAttachment splits a Jira CSV attachment cell, written as
// `date;filename;url`.
func jiraAttachment(cell string) (name, url string) {
	parts := strings.Split(strings.TrimSpace(cell), ";")
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return parts[0], ""
	case 2:
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	default:
		// date;filename;url — the URL may itself contain no semicolons, but be
		// defensive and re-join anything past the filename.
		return strings.TrimSpace(parts[1]), strings.TrimSpace(strings.Join(parts[2:], ";"))
	}
}
