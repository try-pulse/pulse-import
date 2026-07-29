// Package jira2md converts common Jira wiki markup to GitHub-flavored Markdown.
package jira2md

import (
	"regexp"
	"strings"
)

var (
	reHeader     = regexp.MustCompile(`(?m)^h([1-6])\.\s+(.*)$`)
	reBold       = regexp.MustCompile(`\*([^*\n]+)\*`)
	reItalic     = regexp.MustCompile(`(?m)(?P<pre>^|[^a-zA-Z0-9])_([^_\n]+)_(?P<post>$|[^a-zA-Z0-9])`)
	reCodeInline = regexp.MustCompile(`\{\{([^}]+)\}\}`)
	reLink       = regexp.MustCompile(`\[([^|\]]+)\|([^\]]+)\]`)
	reSimpleLink = regexp.MustCompile(`\[(https?://[^\]]+)\]`)
	reQuote      = regexp.MustCompile(`(?m)^bq\.\s+(.*)$`)
	reNoformat   = regexp.MustCompile(`(?s)\{noformat\}(.*?)\{noformat\}`)
	reCode       = regexp.MustCompile(`(?s)\{code(?::[^}]*)?\}(.*?)\{code\}`)
	rePanel      = regexp.MustCompile(`(?s)\{panel(?::[^}]*)?\}(.*?)\{panel\}`)
	reColor      = regexp.MustCompile(`(?s)\{color:[^}]+\}(.*?)\{color\}`)
	reBullet     = regexp.MustCompile(`(?m)^\*\s+`)
	reNumbered   = regexp.MustCompile(`(?m)^#\s+`)
	reHR         = regexp.MustCompile(`(?m)^----$`)
)

func Convert(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	s := strings.ReplaceAll(input, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	if looksLikeWiki(s) {
		return convertWiki(s)
	}
	return strings.TrimSpace(s)
}

func looksLikeWiki(s string) bool {
	return strings.Contains(s, "h1.") ||
		strings.Contains(s, "h2.") ||
		strings.Contains(s, "h3.") ||
		strings.Contains(s, "{code") ||
		strings.Contains(s, "{noformat}") ||
		strings.Contains(s, "{panel") ||
		strings.Contains(s, "bq.") ||
		reLink.MatchString(s)
}

func convertWiki(s string) string {
	s = reNoformat.ReplaceAllStringFunc(s, func(m string) string {
		inner := reNoformat.FindStringSubmatch(m)
		if len(inner) < 2 {
			return m
		}
		return "```\n" + strings.TrimSpace(inner[1]) + "\n```"
	})
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		inner := reCode.FindStringSubmatch(m)
		if len(inner) < 2 {
			return m
		}
		return "```\n" + strings.TrimSpace(inner[1]) + "\n```"
	})
	s = rePanel.ReplaceAllStringFunc(s, func(m string) string {
		inner := rePanel.FindStringSubmatch(m)
		if len(inner) < 2 {
			return m
		}
		lines := strings.Split(strings.TrimSpace(inner[1]), "\n")
		for i, line := range lines {
			lines[i] = "> " + line
		}
		return strings.Join(lines, "\n")
	})
	s = reColor.ReplaceAllString(s, "$1")
	// "# item" must become "1. " before ATX headings are introduced.
	s = reNumbered.ReplaceAllString(s, "1. ")
	s = reHeader.ReplaceAllStringFunc(s, func(m string) string {
		parts := reHeader.FindStringSubmatch(m)
		if len(parts) < 3 {
			return m
		}
		level := parts[1]
		n := 0
		for _, c := range level {
			n = n*10 + int(c-'0')
		}
		return strings.Repeat("#", n) + " " + parts[2]
	})
	s = reQuote.ReplaceAllString(s, "> $1")
	s = reLink.ReplaceAllString(s, "[$1]($2)")
	s = reSimpleLink.ReplaceAllString(s, "$1")
	s = reCodeInline.ReplaceAllString(s, "`$1`")
	s = reBold.ReplaceAllString(s, "**$1**")
	s = reItalic.ReplaceAllString(s, "${pre}_$2_${post}")
	s = reBullet.ReplaceAllString(s, "- ")
	s = reHR.ReplaceAllString(s, "---")
	return strings.TrimSpace(s)
}
