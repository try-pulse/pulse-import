// Package jira2md converts Jira wiki markup to the GitHub-flavored Markdown
// subset that internal/platemd can turn into Plate JSON.
//
// Inline conversion is tokenized rather than a chain of regexp replacements:
// links, code spans and attachment macros are lifted out first, so a URL
// containing `_`, `-` or `*` cannot be mangled into emphasis.
package jira2md

import (
	"regexp"
	"strings"
)

var (
	// Jira accepts "h2. Text" and, in practice, "h2.Text".
	reHeader     = regexp.MustCompile(`^h([1-6])\.[ \t]*(.*)$`)
	reCodeOpen   = regexp.MustCompile(`^\{code(?::([^}]*))?\}$`)
	rePanelOpen  = regexp.MustCompile(`^\{panel(?::[^}]*)?\}$`)
	reQuoteOpen  = regexp.MustCompile(`^\{quote\}$`)
	reColor      = regexp.MustCompile(`\{color:[^}]*\}([\s\S]*?)\{color\}`)
	reMention    = regexp.MustCompile(`\[~(?:accountid:)?([^\]]+)\]`)
	reListMarker = regexp.MustCompile(`^([*#-]+)[ \t]+(.*)$`)
	reHR         = regexp.MustCompile(`^-{4,}$`)
)

// Convert renders Jira wiki markup as Markdown.
func Convert(input string) string {
	return ConvertWithAttachments(input, nil)
}

// ConvertWithAttachments additionally resolves Jira `!file.png!` macros against
// a filename → URL map (built from the export's Attachment columns), so embedded
// media becomes a real link instead of dangling markup.
func ConvertWithAttachments(input string, attachments map[string]string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")

	lines := strings.Split(input, "\n")
	c := &converter{attachments: attachments, jiraStyle: looksLikeJiraWiki(input)}
	return strings.TrimSpace(strings.Join(c.blocks(lines), "\n"))
}

type converter struct {
	attachments map[string]string
	// jiraStyle records whether the source carries markup only Jira produces.
	// It decides the one genuinely ambiguous case: a leading `#` run is a Jira
	// ordered-list marker in wiki markup but an ATX heading in Markdown, and
	// descriptions reach this package in both dialects.
	jiraStyle bool
}

var reJiraOrderedMarker = regexp.MustCompile(`^[*#-]*#[ \t]`)

func looksLikeJiraWiki(input string) bool {
	for _, marker := range []string{
		"bq. ", "{code", "{noformat}", "{panel", "{quote}", "{color:", "{{", "[~",
	} {
		if strings.Contains(input, marker) {
			return true
		}
	}
	if reNamedLink.MatchString(input) || reAttachment.MatchString(input) {
		return true
	}
	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)
		if reHeader.MatchString(trimmed) || strings.HasPrefix(trimmed, "||") {
			return true
		}
	}
	return false
}

// orderedListContext reports whether the neighbouring lines prove that a `#`
// run at index i is part of a Jira ordered list rather than a Markdown heading.
func orderedListContext(lines []string, i int) bool {
	if i > 0 && reJiraOrderedMarker.MatchString(strings.TrimLeft(lines[i-1], " \t")) {
		return true
	}
	return i+1 < len(lines) && reJiraOrderedMarker.MatchString(strings.TrimLeft(lines[i+1], " \t"))
}

func (c *converter) blocks(lines []string) []string {
	out := make([]string, 0, len(lines))
	inCode := false
	quoteDepth := 0 // inside {quote} or {panel}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case inCode && (trimmed == "{code}" || trimmed == "{noformat}"):
			out = append(out, "```")
			inCode = false
			continue
		case inCode:
			out = append(out, line)
			continue
		case trimmed == "{noformat}":
			out = append(out, "```")
			inCode = true
			continue
		case reCodeOpen.MatchString(trimmed):
			out = append(out, "```"+codeLanguage(trimmed))
			inCode = true
			continue
		case quoteDepth > 0 && (trimmed == "{quote}" || trimmed == "{panel}"):
			quoteDepth--
			continue
		case reQuoteOpen.MatchString(trimmed) || rePanelOpen.MatchString(trimmed):
			quoteDepth++
			continue
		}

		// A Jira table runs until the first line that is not a table row.
		if isTableRow(trimmed) {
			rows := []string{}
			for i < len(lines) && isTableRow(strings.TrimSpace(lines[i])) {
				rows = append(rows, strings.TrimSpace(lines[i]))
				i++
			}
			i-- // the outer loop advances
			out = append(out, c.table(rows, quoteDepth)...)
			continue
		}

		converted := c.line(line, lines, i)
		if quoteDepth > 0 {
			if converted == "" {
				converted = ">"
			} else {
				converted = "> " + converted
			}
		}
		out = append(out, converted)
	}
	// Never drop content because a block was left open.
	if inCode {
		out = append(out, "```")
	}
	return out
}

func (c *converter) line(line string, all []string, index int) string {
	trimmed := strings.TrimSpace(line)

	if match := reHeader.FindStringSubmatch(trimmed); len(match) == 3 {
		return strings.Repeat("#", int(match[1][0]-'0')) + " " + c.inline(match[2])
	}
	if strings.HasPrefix(trimmed, "bq. ") {
		return "> " + c.inline(strings.TrimPrefix(trimmed, "bq. "))
	}
	if reHR.MatchString(trimmed) {
		return "---"
	}
	if depth, ordered, content, ok := parseListMarker(line); ok {
		// A run made only of `#` is the one ambiguous marker: an ATX heading in
		// Markdown, an ordered-list item in Jira. Leave it alone unless the
		// source proves it is wiki markup, otherwise every heading in a
		// Markdown description would be demoted to a list item. Mixed runs like
		// `*#` cannot be headings, so they never need the guard.
		if ordered && onlyHashes(line) && !c.jiraStyle && !orderedListContext(all, index) {
			return c.inline(line)
		}
		marker := "-"
		if ordered {
			marker = "1."
		}
		return strings.Repeat("  ", depth-1) + marker + " " + c.inline(content)
	}
	return c.inline(line)
}

// parseListMarker reads Jira's repeated-marker list syntax: `*` / `-` for
// bullets, `#` for numbers, repeated once per nesting level, and mixable
// (`*#` is a numbered item at depth 2). The trailing space is required, which
// is what keeps `*bold*` and `**bold**` from reading as list items.
func parseListMarker(line string) (depth int, ordered bool, content string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	match := reListMarker.FindStringSubmatch(trimmed)
	if match == nil {
		return 0, false, "", false
	}
	markers := match[1]
	// `----` is a horizontal rule, not a list; handled before this point, but a
	// bare run of dashes with trailing text is still not a Jira list marker.
	if strings.Trim(markers, "-") == "" && len(markers) > 1 {
		return 0, false, "", false
	}
	return len(markers), markers[len(markers)-1] == '#', match[2], true
}

// onlyHashes reports whether a line's list marker run consists solely of `#`,
// making it indistinguishable from a Markdown ATX heading.
func onlyHashes(line string) bool {
	match := reListMarker.FindStringSubmatch(strings.TrimLeft(line, " \t"))
	return match != nil && strings.Trim(match[1], "#") == ""
}

var reCodeLanguage = regexp.MustCompile(`^[A-Za-z0-9+#_.-]+$`)

// codeLanguage pulls the syntax hint out of a Jira code-macro opener. Jira
// accepts both `{code:go}` and `{code:language=go|title=Example}`.
func codeLanguage(opener string) string {
	match := reCodeOpen.FindStringSubmatch(opener)
	if len(match) < 2 || match[1] == "" {
		return ""
	}
	for _, param := range strings.Split(match[1], "|") {
		param = strings.TrimSpace(param)
		if param == "" {
			continue
		}
		name, value, hasValue := strings.Cut(param, "=")
		candidate := strings.TrimSpace(name)
		if hasValue {
			if !strings.EqualFold(strings.TrimSpace(name), "language") {
				continue
			}
			candidate = strings.TrimSpace(value)
		}
		if reCodeLanguage.MatchString(candidate) {
			return candidate
		}
	}
	return ""
}

func isTableRow(line string) bool {
	return strings.HasPrefix(line, "|") && len(line) > 1
}

// table converts a run of Jira table rows into a GFM table. platemd only
// recognises a table when a delimiter row follows the header, so one is always
// emitted — synthesised from the first row when the export has no `||` header.
func (c *converter) table(rows []string, quoteDepth int) []string {
	type parsedRow struct {
		cells  []string
		header bool
	}
	parsed := make([]parsedRow, 0, len(rows))
	width := 0
	for _, row := range rows {
		header := strings.HasPrefix(row, "||")
		sep := "|"
		if header {
			sep = "||"
		}
		body := strings.TrimSuffix(strings.TrimPrefix(row, sep), sep)
		cells := strings.Split(body, sep)
		for i, cell := range cells {
			cells[i] = c.inline(strings.TrimSpace(cell))
		}
		if len(cells) > width {
			width = len(cells)
		}
		parsed = append(parsed, parsedRow{cells: cells, header: header})
	}
	if width == 0 {
		return nil
	}

	pad := func(cells []string) string {
		padded := make([]string, width)
		for i := range padded {
			if i < len(cells) {
				padded[i] = escapePipes(cells[i])
			}
		}
		return "| " + strings.Join(padded, " | ") + " |"
	}
	delimiter := "|" + strings.Repeat(" --- |", width)

	out := make([]string, 0, len(parsed)+1)
	out = append(out, pad(parsed[0].cells), delimiter)
	for _, row := range parsed[1:] {
		out = append(out, pad(row.cells))
	}
	if quoteDepth > 0 {
		for i, line := range out {
			out[i] = "> " + line
		}
	}
	return out
}

func escapePipes(cell string) string {
	return strings.ReplaceAll(cell, "|", `\|`)
}
