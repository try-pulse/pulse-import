package platemd

import "strings"

type serializer struct {
	buf     strings.Builder
	opts    Options
	listBuf []Node
}

func newSerializer(opts Options) *serializer {
	return &serializer{opts: opts}
}

func (s *serializer) cloneEmpty() *serializer {
	return &serializer{opts: s.opts}
}

func (s *serializer) finalize() string {
	out := strings.TrimRight(s.buf.String(), "\n")
	if out == "" {
		return ""
	}
	return out + "\n"
}

func (s *serializer) walkBlocks(nodes []Node) {
	for _, n := range nodes {
		if isListItem(n) {
			s.listBuf = append(s.listBuf, n)
			continue
		}
		if len(s.listBuf) > 0 {
			s.emitListBlock(s.listBuf)
			s.listBuf = s.listBuf[:0]
		}
		s.emitBlock(n)
	}
	if len(s.listBuf) > 0 {
		s.emitListBlock(s.listBuf)
		s.listBuf = s.listBuf[:0]
	}
}

func (s *serializer) emitBlock(n Node) {
	switch nodeType(n) {
	case "p":
		s.emitParagraph(n)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		s.emitHeading(n)
	case "blockquote":
		s.emitBlockquote(n)
	case "code_block":
		s.emitCodeBlock(n)
	case "hr":
		s.buf.WriteString("---\n\n")
	case "img":
		s.emitImage(n)
	case "table":
		s.emitTable(n)
	}
}

func (s *serializer) emitParagraph(n Node) {
	children := nodeChildren(n)
	if isEmptyParagraph(children) {
		if s.opts.PreserveEmptyParagraphs {
			s.buf.WriteString("​\n\n")
		}
		return
	}
	s.emitInline(children)
	s.buf.WriteString("\n\n")
}

func isEmptyParagraph(children []Node) bool {
	if len(children) == 0 {
		return true
	}
	for _, c := range children {
		if !isText(c) || leafText(c) != "" {
			return false
		}
	}
	return true
}

func (s *serializer) emitHeading(n Node) {
	t := nodeType(n)
	depth := int(t[1] - '0') // safe: emitBlock dispatches only h1..h6
	depth += s.opts.HeadingShift
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}
	for i := 0; i < depth; i++ {
		s.buf.WriteByte('#')
	}
	s.buf.WriteByte(' ')
	s.emitInline(nodeChildren(n))
	s.buf.WriteString("\n\n")
}

func (s *serializer) emitBlockquote(n Node) {
	sub := s.cloneEmpty()
	sub.walkBlocks(nodeChildren(n))
	inner := strings.TrimRight(sub.buf.String(), "\n")
	if inner == "" {
		s.buf.WriteString(">\n\n")
		return
	}
	for _, line := range strings.Split(inner, "\n") {
		if line == "" {
			s.buf.WriteString(">\n")
			continue
		}
		s.buf.WriteString("> ")
		s.buf.WriteString(line)
		s.buf.WriteByte('\n')
	}
	s.buf.WriteByte('\n')
}

func (s *serializer) emitCodeBlock(n Node) {
	lang := nodeString(n, "lang")
	s.buf.WriteString("```")
	s.buf.WriteString(lang)
	s.buf.WriteByte('\n')

	for i, c := range nodeChildren(n) {
		if i > 0 {
			s.buf.WriteByte('\n')
		}
		switch {
		case nodeType(c) == "code_line":
			for _, leaf := range nodeChildren(c) {
				s.buf.WriteString(leafText(leaf))
			}
		case isText(c):
			s.buf.WriteString(leafText(c))
		}
	}

	s.buf.WriteString("\n```\n\n")
}

func (s *serializer) emitImage(n Node) {
	url := nodeString(n, "url")
	alt := captionText(n)
	s.buf.WriteByte('!')
	s.buf.WriteByte('[')
	s.buf.WriteString(escapeText(alt))
	s.buf.WriteByte(']')
	s.buf.WriteByte('(')
	s.buf.WriteString(escapeLinkURL(url))
	s.buf.WriteByte(')')
	s.buf.WriteString("\n\n")
}

func captionText(n Node) string {
	raw, ok := n["caption"].([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, c := range raw {
		if m, ok := c.(map[string]any); ok {
			b.WriteString(leafText(m))
		}
	}
	return b.String()
}
