package jiracsv

import (
	"fmt"
	"strings"
	"time"
)

// attachment is one file referenced by a Jira row.
type attachment struct {
	Name string
	URL  string
}

// docBuilder assembles an issue's Main Doc: the converted description first,
// then a provenance section carrying every field Pulse cannot store natively
// (reporter, creator, created/updated/resolved stamps, resolution, sprint,
// environment, time tracking) plus links to the original attachments.
//
// Pulse stamps created_at itself and takes the reporter from the access token,
// so without this section that information would simply be lost.
type docBuilder struct {
	BrowseURL   string
	Key         string
	Fields      []docField
	Attachments []attachment
	Description string
}

type docField struct {
	Label string
	Value string
}

func (b *docBuilder) add(label, value string) {
	if value = strings.TrimSpace(value); value != "" {
		b.Fields = append(b.Fields, docField{Label: label, Value: value})
	}
}

func (b *docBuilder) addList(label string, values []string) {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	b.add(label, strings.Join(cleaned, ", "))
}

func (b *docBuilder) addTime(label string, value time.Time, ok bool) {
	if ok {
		b.add(label, value.Format("2006-01-02 15:04 UTC"))
	}
}

// build renders the Main Doc markdown. It returns an empty string only when
// there is genuinely nothing to record.
func (b *docBuilder) build() string {
	var sections []string
	if description := strings.TrimSpace(b.Description); description != "" {
		sections = append(sections, description)
	}

	var provenance []string
	switch {
	case b.BrowseURL != "" && b.Key != "":
		provenance = append(provenance, fmt.Sprintf("Imported from Jira issue [%s](%s).", b.Key, b.BrowseURL))
	case b.Key != "":
		provenance = append(provenance, fmt.Sprintf("Imported from Jira issue %s.", b.Key))
	}
	if len(b.Fields) > 0 {
		table := []string{"| Jira field | Value |", "| --- | --- |"}
		for _, field := range b.Fields {
			table = append(table, fmt.Sprintf("| %s | %s |", field.Label, escapeCell(field.Value)))
		}
		provenance = append(provenance, strings.Join(table, "\n"))
	}
	if len(b.Attachments) > 0 {
		list := []string{"**Attachments**", ""}
		for _, file := range b.Attachments {
			if file.URL != "" {
				list = append(list, fmt.Sprintf("- [%s](%s)", escapeCell(file.Name), file.URL))
				continue
			}
			list = append(list, fmt.Sprintf("- %s", escapeCell(file.Name)))
		}
		provenance = append(provenance, strings.Join(list, "\n"))
	}

	if len(provenance) == 0 {
		return strings.Join(sections, "\n\n")
	}
	if len(sections) > 0 {
		sections = append(sections, "---")
	}
	sections = append(sections, strings.Join(provenance, "\n\n"))
	return strings.Join(sections, "\n\n")
}

// escapeCell keeps a value inside one Markdown table cell: pipes would end the
// cell and newlines would end the row.
func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}

// formatSeconds renders a Jira time-tracking value as a compact duration.
func formatSeconds(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// formatPoints renders a story-point value without a trailing ".0".
func formatPoints(points float64) string {
	if points == float64(int64(points)) {
		return fmt.Sprintf("%d", int64(points))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", points), "0"), ".")
}
