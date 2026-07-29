// Package jira2md converts a deliberately small, documented subset of Jira
// wiki markup to GitHub-flavored Markdown.
package jira2md

import (
	"regexp"
	"strings"
)

var (
	reHeader     = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	reBold       = regexp.MustCompile(`\*([^*\n]+)\*`)
	reItalic     = regexp.MustCompile(`(^|[^a-zA-Z0-9])_([^_\n]+)_($|[^a-zA-Z0-9])`)
	reCodeInline = regexp.MustCompile(`\{\{([^}\n]+)\}\}`)
	reLink       = regexp.MustCompile(`\[([^|\]]+)\|([^\]]+)\]`)
	reSimpleLink = regexp.MustCompile(`\[(https?://[^\]]+)\]`)
	reColor      = regexp.MustCompile(`\{color:[^}]+\}([^{]*)\{color\}`)
	reCodeOpen   = regexp.MustCompile(`^\{code(?::[^}]*)?\}\s*$`)
	rePanelOpen  = regexp.MustCompile(`^\{panel(?::[^}]*)?\}\s*$`)
)

func Convert(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	inCode := false
	inPanel := false
	jiraStyle := looksLikeJiraWiki(input)

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case inCode && (trimmed == "{code}" || trimmed == "{noformat}"):
			out = append(out, "```")
			inCode = false
			continue
		case inCode:
			out = append(out, line)
			continue
		case reCodeOpen.MatchString(trimmed) || trimmed == "{noformat}":
			out = append(out, "```")
			inCode = true
			continue
		case inPanel && trimmed == "{panel}":
			inPanel = false
			continue
		case !inPanel && rePanelOpen.MatchString(trimmed):
			inPanel = true
			continue
		}

		converted := convertLine(line, lines, index, jiraStyle)
		if inPanel {
			converted = "> " + converted
		}
		out = append(out, converted)
	}
	// Preserve unmatched opening blocks rather than silently dropping content.
	if inCode {
		out = append(out, "```")
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func convertLine(line string, all []string, index int, jiraStyle bool) string {
	if match := reHeader.FindStringSubmatch(line); len(match) == 3 {
		return strings.Repeat("#", int(match[1][0]-'0')) + " " + convertInline(match[2])
	}
	if strings.HasPrefix(line, "bq. ") {
		return "> " + convertInline(strings.TrimPrefix(line, "bq. "))
	}
	if strings.HasPrefix(line, "* ") {
		return "- " + convertInline(strings.TrimPrefix(line, "* "))
	}
	if strings.HasPrefix(line, "# ") && (jiraStyle || jiraNumberedContext(all, index)) {
		return "1. " + convertInline(strings.TrimPrefix(line, "# "))
	}
	if strings.TrimSpace(line) == "----" {
		return "---"
	}
	return convertInline(line)
}

func looksLikeJiraWiki(input string) bool {
	return strings.Contains(input, "bq. ") ||
		strings.Contains(input, "{code") ||
		strings.Contains(input, "{noformat}") ||
		strings.Contains(input, "{panel") ||
		strings.Contains(input, "{color:") ||
		strings.Contains(input, "{{") ||
		reHeader.MatchString(input) ||
		reLink.MatchString(input)
}

func jiraNumberedContext(lines []string, index int) bool {
	if index > 0 && strings.HasPrefix(lines[index-1], "# ") {
		return true
	}
	return index+1 < len(lines) && strings.HasPrefix(lines[index+1], "# ")
}

func convertInline(line string) string {
	line = reColor.ReplaceAllString(line, "$1")
	line = reLink.ReplaceAllString(line, "[$1]($2)")
	line = reSimpleLink.ReplaceAllString(line, "$1")
	line = reCodeInline.ReplaceAllString(line, "`$1`")
	line = reBold.ReplaceAllString(line, "**$1**")
	line = reItalic.ReplaceAllString(line, "$1_$2_$3")
	return line
}
