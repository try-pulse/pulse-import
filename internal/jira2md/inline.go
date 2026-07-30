package jira2md

import (
	"fmt"
	"regexp"
	"strings"
)

// sentinel wraps placeholder indexes while mark conversion runs. NUL cannot
// appear in a CSV cell and is not a word character, so a placeholder also acts
// as a delimiter boundary for the mark patterns below.
const sentinel = "\x00"

var (
	reMono       = regexp.MustCompile(`\{\{([^}\n]*)\}\}`)
	reAttachment = regexp.MustCompile(`!([^!|\s][^!|\n]*?)(\|[^!\n]*)?!`)
	reNamedLink  = regexp.MustCompile(`\[([^|\]\n]*)\|([^|\]\n]*)(?:\|[^\]\n]*)?\]`)
	reBareLink   = regexp.MustCompile(`\[((?:https?|mailto|ftp)://?[^\]\s]+)\]`)

	// Mark patterns capture the characters either side of the delimiter because
	// RE2 has no look-around. Content may neither begin nor end with whitespace
	// and may not contain the delimiter, which is what keeps "well-known" from
	// reading as strikethrough and "alpha - beta - gamma" from reading as a mark.
	reBold   = regexp.MustCompile(markPattern(`\*`, `*`))
	reItalic = regexp.MustCompile(markPattern(`_`, `_`))
	reStrike = regexp.MustCompile(markPattern(`-`, `-`))
	reIns    = regexp.MustCompile(markPattern(`\+`, `+`))

	// Superscript and subscript attach directly to a word ("x^2^", "H~2~O"), so
	// they cannot demand a non-word boundary. Their content is restricted to a
	// single whitespace-free token instead, which is what Jira uses them for and
	// what stops prose like "approx ~5 and ~10 items" from being rewritten.
	reSup = regexp.MustCompile(`\^([^\s^]+)\^`)
	reSub = regexp.MustCompile(`~([^\s~]+)~`)
)

// markPattern builds "boundary, delimiter, content, delimiter, boundary" for a
// paired Jira inline marker. quoted is the delimiter escaped for a regexp body,
// raw is the same character escaped for use inside a character class.
func markPattern(quoted, raw string) string {
	class := regexp.QuoteMeta(raw)
	notDelim := `[^` + class + `\s]`
	body := `(` + notDelim + `|` + notDelim + `[^` + class + `\n]*` + notDelim + `)`
	return `(^|[^\w` + class + `])` + quoted + body + quoted + `($|[^\w` + class + `])`
}

// inline converts one line's Jira inline markup to Markdown.
func (c *converter) inline(line string) string {
	if line == "" {
		return ""
	}
	// Colour macros wrap other markup, so unwrap them before anything else.
	line = reColor.ReplaceAllString(line, "$1")

	t := tokenizer{}
	line = t.protect(line, c.attachments)
	line = convertMarks(line)
	return t.restore(line)
}

// tokenizer lifts spans that must not be touched by mark conversion (code,
// links, attachment macros, mentions) out of the line and puts them back once
// emphasis has been applied.
type tokenizer struct {
	spans []string
}

func (t *tokenizer) placeholder(value string) string {
	t.spans = append(t.spans, value)
	return sentinel + fmt.Sprint(len(t.spans)-1) + sentinel
}

func (t *tokenizer) protect(line string, attachments map[string]string) string {
	line = reMono.ReplaceAllStringFunc(line, func(match string) string {
		inner := reMono.FindStringSubmatch(match)[1]
		if inner == "" {
			return t.placeholder("")
		}
		return t.placeholder("`" + inner + "`")
	})

	line = reAttachment.ReplaceAllStringFunc(line, func(match string) string {
		name := strings.TrimSpace(reAttachment.FindStringSubmatch(match)[1])
		// Jira attachment URLs need authentication, so an embedded image node
		// would render broken. Link to the original file when the export gave
		// us its URL, otherwise keep the filename as a literal reference.
		if url, ok := attachments[strings.ToLower(name)]; ok && url != "" {
			return t.placeholder("[" + name + "](" + url + ")")
		}
		return t.placeholder("`" + name + "`")
	})

	line = reMention.ReplaceAllStringFunc(line, func(match string) string {
		handle := strings.TrimSpace(reMention.FindStringSubmatch(match)[1])
		if handle == "" {
			return t.placeholder("")
		}
		return t.placeholder("`@" + handle + "`")
	})

	line = reNamedLink.ReplaceAllStringFunc(line, func(match string) string {
		parts := reNamedLink.FindStringSubmatch(match)
		text, target := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		switch {
		case target == "" && text == "":
			return t.placeholder("")
		case target == "":
			// `[text|]` — nothing to link to.
			return t.placeholder(text)
		case text == "":
			return t.placeholder(target)
		}
		return t.placeholder("[" + text + "](" + target + ")")
	})

	return reBareLink.ReplaceAllStringFunc(line, func(match string) string {
		return t.placeholder(reBareLink.FindStringSubmatch(match)[1])
	})
}

func (t *tokenizer) restore(line string) string {
	for i, span := range t.spans {
		line = strings.ReplaceAll(line, sentinel+fmt.Sprint(i)+sentinel, span)
	}
	return line
}

// convertMarks applies the paired inline markers. Order matters: subscript is
// the single-tilde marker and strikethrough emits a double tilde, so subscript
// has to run first or it would eat its way back into `~~struck~~`.
func convertMarks(line string) string {
	line = reSup.ReplaceAllString(line, "<sup>${1}</sup>")
	line = reSub.ReplaceAllString(line, "<sub>${1}</sub>")
	line = reBold.ReplaceAllString(line, "${1}**${2}**${3}")
	line = reItalic.ReplaceAllString(line, "${1}_${2}_${3}")
	line = reIns.ReplaceAllString(line, "${1}<u>${2}</u>${3}")
	return reStrike.ReplaceAllString(line, "${1}~~${2}~~${3}")
}
