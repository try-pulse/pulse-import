package platemd

import (
	"net/url"
	"strings"
)

type markSet struct {
	bold, italic, strike, underline, sub, sup, code bool
}

func (m markSet) leaf(text string) Node {
	n := Node{"text": text}
	if m.bold {
		n["bold"] = true
	}
	if m.italic {
		n["italic"] = true
	}
	if m.strike {
		n["strikethrough"] = true
	}
	if m.underline {
		n["underline"] = true
	}
	if m.sub {
		n["subscript"] = true
	}
	if m.sup {
		n["superscript"] = true
	}
	if m.code {
		n["code"] = true
	}
	return n
}

type inlineParser struct {
	chars []rune
	opts  Options
}

func parseInline(s string, opts Options) []Node {
	p := &inlineParser{chars: []rune(s), opts: opts}
	var out []Node
	p.parse(0, len(p.chars), markSet{}, &out)
	return out
}

func (p *inlineParser) parse(lo, hi int, marks markSet, out *[]Node) {
	strongDelim := []rune{rune(p.opts.StrongMarker), rune(p.opts.StrongMarker)}
	emphasis := rune(p.opts.EmphasisMarker)

	var buf []rune
	flush := func() {
		if len(buf) > 0 {
			*out = append(*out, marks.leaf(string(buf)))
			buf = buf[:0]
		}
	}

	i := lo
	for i < hi {
		c := p.chars[i]

		if c == '\\' && i+1 < hi && isTextEscapableRune(p.chars[i+1]) {
			buf = append(buf, p.chars[i+1])
			i += 2
			continue
		}

		if c == '<' && p.matchesStr("<br />", i, hi) {
			flush()
			*out = append(*out, Node{"text": "\n"})
			i += 6
			if i < hi && p.chars[i] == '\n' {
				i++
			}
			continue
		}
		if c == '\n' {
			flush()
			*out = append(*out, Node{"text": "\n"})
			i++
			continue
		}

		if c == '`' {
			if content, next, ok := p.parseInlineCode(i, hi); ok {
				flush()
				cm := marks
				cm.code = true
				*out = append(*out, cm.leaf(content))
				i = next
				continue
			}
		}

		if p.matches(strongDelim, i, hi) {
			if close, ok := p.findDelim(strongDelim, i+2, hi, lo); ok {
				flush()
				inner := marks
				inner.bold = true
				p.parse(i+2, close, inner, out)
				i = close + 2
				continue
			}
		}
		if p.matchesStr("~~", i, hi) {
			if close, ok := p.findDelim([]rune{'~', '~'}, i+2, hi, lo); ok {
				flush()
				inner := marks
				inner.strike = true
				p.parse(i+2, close, inner, out)
				i = close + 2
				continue
			}
		}
		if c == emphasis {
			if close, ok := p.findDelim([]rune{emphasis}, i+1, hi, lo); ok {
				flush()
				inner := marks
				inner.italic = true
				p.parse(i+1, close, inner, out)
				i = close + 1
				continue
			}
		}

		if c == '<' {
			if next, ok := p.parseHTMLMark(i, hi, marks, out, &buf); ok {
				i = next
				continue
			}
		}

		if c == '[' {
			if node, next, ok := p.parseBracket(i, hi, marks); ok {
				flush()
				*out = append(*out, node)
				i = next
				continue
			}
		}

		buf = append(buf, c)
		i++
	}
	flush()
}

func (p *inlineParser) parseInlineCode(i, hi int) (string, int, bool) {
	runLen := 0
	j := i
	for j < hi && p.chars[j] == '`' {
		runLen++
		j++
	}
	contentStart := j

	k := contentStart
	for k < hi {
		if p.chars[k] != '`' {
			k++
			continue
		}
		run := 0
		m := k
		for m < hi && p.chars[m] == '`' {
			run++
			m++
		}
		if run == runLen {
			content := string(p.chars[contentStart:k])
			// CommonMark §6.1: strip one pad space when content isn't all spaces.
			if strings.HasPrefix(content, " ") && strings.HasSuffix(content, " ") &&
				strings.ContainsFunc(content, func(r rune) bool { return r != ' ' }) {
				content = content[1 : len(content)-1]
			}
			return content, m, true
		}
		k = m
	}
	return "", 0, false
}

func (p *inlineParser) parseHTMLMark(i, hi int, marks markSet, out *[]Node, buf *[]rune) (int, bool) {
	tags := []struct {
		open, close string
		apply       func(*markSet)
	}{
		{"<u>", "</u>", func(m *markSet) { m.underline = true }},
		{"<sub>", "</sub>", func(m *markSet) { m.sub = true }},
		{"<sup>", "</sup>", func(m *markSet) { m.sup = true }},
	}
	for _, t := range tags {
		if !p.matchesStr(t.open, i, hi) {
			continue
		}
		innerStart := i + len([]rune(t.open))
		close, ok := p.findSubstring(t.close, innerStart, hi)
		if !ok {
			continue
		}
		if len(*buf) > 0 {
			*out = append(*out, marks.leaf(string(*buf)))
			*buf = (*buf)[:0]
		}
		inner := marks
		t.apply(&inner)
		p.parse(innerStart, close, inner, out)
		return close + len([]rune(t.close)), true
	}
	return 0, false
}

func (p *inlineParser) parseBracket(i, hi int, marks markSet) (Node, int, bool) {
	closeBracket, ok := p.findDelim([]rune{']'}, i+1, hi, i+1)
	if !ok || closeBracket+1 >= hi || p.chars[closeBracket+1] != '(' {
		return nil, 0, false
	}
	closeParen, ok := p.findDelim([]rune{')'}, closeBracket+2, hi, closeBracket+2)
	if !ok {
		return nil, 0, false
	}

	label := string(p.chars[i+1 : closeBracket])
	target := string(p.chars[closeBracket+2 : closeParen])
	next := closeParen + 1

	if strings.HasPrefix(target, "mention:") {
		key := decodeMentionKey(target[len("mention:"):])
		node := Node{
			"type":     "mention",
			"value":    unescapeText(label),
			"key":      key,
			"children": []any{Node{"text": ""}},
		}
		return node, next, true
	}

	if ref, ok := parseEntityURL(target); ok {
		node := Node{
			"type":       "entity_mention",
			"entityType": ref.typ,
			"entityId":   ref.id,
			"snapshot":   map[string]any{"title": unescapeText(label)},
			"children":   []any{Node{"text": ""}},
		}
		return node, next, true
	}

	var children []Node
	p.parse(i+1, closeBracket, marks, &children)
	if len(children) == 0 {
		children = []Node{{"text": ""}}
	}
	node := Node{
		"type":     "a",
		"url":      unescapeLinkURL(target),
		"children": toAnySlice(children),
	}
	return node, next, true
}

func decodeMentionKey(s string) string {
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	return s
}

func (p *inlineParser) matches(s []rune, i, hi int) bool {
	if i+len(s) > hi {
		return false
	}
	for k, ch := range s {
		if p.chars[i+k] != ch {
			return false
		}
	}
	return true
}

func (p *inlineParser) matchesStr(s string, i, hi int) bool {
	return p.matches([]rune(s), i, hi)
}

func (p *inlineParser) findDelim(delim []rune, from, hi, openLo int) (int, bool) {
	i := from
	for i+len(delim) <= hi {
		if p.matches(delim, i, hi) && p.isUnescaped(i, openLo) {
			return i, true
		}
		i++
	}
	return 0, false
}

func (p *inlineParser) findSubstring(s string, from, hi int) (int, bool) {
	rs := []rune(s)
	i := from
	for i+len(rs) <= hi {
		if p.matches(rs, i, hi) {
			return i, true
		}
		i++
	}
	return 0, false
}

func (p *inlineParser) isUnescaped(i, lo int) bool {
	backslashes := 0
	for j := i - 1; j >= lo && p.chars[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 0
}
