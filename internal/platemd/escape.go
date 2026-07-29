package platemd

import "strings"

func escapeText(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\', '`', '*', '_', '[', ']', '(', ')', '#', '+', '!', '|', '~', '<':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func escapeLinkURL(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', ')':
			b.WriteByte('\\')
			b.WriteByte(c)
		case ' ':
			b.WriteString("%20")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// @platejs/markdown mention encoding (incl. literal ( ) → %28 %29).
func encodeMentionKey(s string) string {
	if s == "" {
		return s
	}
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if shouldPercentEscape(c) {
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0F])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func shouldPercentEscape(c byte) bool {
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return false
	}
	switch c {
	case '-', '_', '.', '~', '!', '*', '\'':
		return false
	}
	return true
}

func isTextEscapableRune(r rune) bool {
	switch r {
	case '\\', '`', '*', '_', '[', ']', '(', ')', '#', '+', '!', '|', '~', '<':
		return true
	}
	return false
}

func unescapeText(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) && isTextEscapableRune(rs[i+1]) {
			b.WriteRune(rs[i+1])
			i++
			continue
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

func unescapeLinkURL(s string) string {
	s = strings.ReplaceAll(s, "\\(", "(")
	s = strings.ReplaceAll(s, "\\)", ")")
	s = strings.ReplaceAll(s, "%20", " ")
	return s
}

func replaceCellNewlines(s string) string {
	if s == "" || !strings.ContainsRune(s, '\n') {
		return s
	}
	return strings.ReplaceAll(s, "\n", "<br />")
}
