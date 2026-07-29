package platemd

import "strings"

// Outer→inner wrap order, matching @platejs/markdown (code is not a wrapper).
type inlineMark string

const (
	markItalic    inlineMark = "italic"
	markBold      inlineMark = "bold"
	markStrike    inlineMark = "strikethrough"
	markUnderline inlineMark = "underline"
	markSub       inlineMark = "subscript"
	markSup       inlineMark = "superscript"
)

var inlineMarkOrder = []inlineMark{
	markItalic,
	markBold,
	markStrike,
	markUnderline,
	markSub,
	markSup,
}

func leafMarks(n Node) []inlineMark {
	var ms []inlineMark
	for _, m := range inlineMarkOrder {
		if nodeBool(n, string(m)) {
			ms = append(ms, m)
		}
	}
	return ms
}

type inlineState struct {
	open []inlineMark
}

func (s *serializer) emitInline(children []Node) {
	st := inlineState{}
	for _, ch := range children {
		if isText(ch) {
			s.emitTextLeaf(ch, &st)
			continue
		}
		s.closeMarksTo(&st, 0)
		s.emitInlineElement(ch)
	}
	s.closeMarksTo(&st, 0)
}

func (s *serializer) emitTextLeaf(n Node, st *inlineState) {
	text := leafText(n)
	if text == "" {
		return
	}
	// "\n" inside a paragraph round-trips as a hard line break in Plate.
	if text == "\n" {
		s.closeMarksTo(st, 0)
		s.buf.WriteString("<br />\n")
		return
	}

	desired := leafMarks(n)

	common := 0
	for common < len(st.open) && common < len(desired) && st.open[common] == desired[common] {
		common++
	}
	s.closeMarksTo(st, common)
	for i := common; i < len(desired); i++ {
		s.writeMarkOpen(desired[i])
		st.open = append(st.open, desired[i])
	}

	if leafHasCode := nodeBool(n, "code"); leafHasCode {
		s.writeInlineCode(text)
		return
	}
	s.buf.WriteString(escapeText(text))
}

func (s *serializer) closeMarksTo(st *inlineState, target int) {
	for i := len(st.open) - 1; i >= target; i-- {
		s.writeMarkClose(st.open[i])
	}
	st.open = st.open[:target]
}

func (s *serializer) writeMarkOpen(m inlineMark) {
	switch m {
	case markBold:
		s.buf.WriteByte(s.opts.StrongMarker)
		s.buf.WriteByte(s.opts.StrongMarker)
	case markItalic:
		s.buf.WriteByte(s.opts.EmphasisMarker)
	case markStrike:
		s.buf.WriteString("~~")
	case markUnderline:
		s.buf.WriteString("<u>")
	case markSub:
		s.buf.WriteString("<sub>")
	case markSup:
		s.buf.WriteString("<sup>")
	}
}

func (s *serializer) writeMarkClose(m inlineMark) {
	switch m {
	case markBold:
		s.buf.WriteByte(s.opts.StrongMarker)
		s.buf.WriteByte(s.opts.StrongMarker)
	case markItalic:
		s.buf.WriteByte(s.opts.EmphasisMarker)
	case markStrike:
		s.buf.WriteString("~~")
	case markUnderline:
		s.buf.WriteString("</u>")
	case markSub:
		s.buf.WriteString("</sub>")
	case markSup:
		s.buf.WriteString("</sup>")
	}
}

// CommonMark §6.1: pick a backtick run longer than any run in the content.
func (s *serializer) writeInlineCode(text string) {
	longest, cur := 0, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			cur++
			if cur > longest {
				longest = cur
			}
		} else {
			cur = 0
		}
	}
	delim := strings.Repeat("`", longest+1)
	pad := strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`")
	s.buf.WriteString(delim)
	if pad {
		s.buf.WriteByte(' ')
	}
	s.buf.WriteString(text)
	if pad {
		s.buf.WriteByte(' ')
	}
	s.buf.WriteString(delim)
}

func (s *serializer) emitInlineElement(n Node) {
	switch nodeType(n) {
	case "a":
		s.emitLink(n)
	case "mention":
		s.emitMention(n)
	case "entity_mention":
		s.emitEntityMention(n)
	}
	// Unsupported inline types are silently dropped (tracked in TODO.md).
}

func (s *serializer) emitLink(n Node) {
	s.buf.WriteByte('[')
	s.emitInline(nodeChildren(n))
	s.buf.WriteByte(']')
	s.buf.WriteByte('(')
	s.buf.WriteString(escapeLinkURL(nodeString(n, "url")))
	s.buf.WriteByte(')')
}

func (s *serializer) emitMention(n Node) {
	val := nodeString(n, "value")
	key := nodeString(n, "key")
	if key == "" {
		key = val
	}
	s.buf.WriteByte('[')
	s.buf.WriteString(escapeText(val))
	s.buf.WriteByte(']')
	s.buf.WriteString("(mention:")
	s.buf.WriteString(encodeMentionKey(key))
	s.buf.WriteByte(')')
}

func (s *serializer) emitEntityMention(n Node) {
	typ := nodeString(n, "entityType")
	id := nodeString(n, "entityId")
	title := entitySnapshotLabel(n)
	if title == "" {
		title = id
	}
	s.buf.WriteByte('[')
	s.buf.WriteString(escapeText(title))
	s.buf.WriteByte(']')
	s.buf.WriteByte('(')
	s.buf.WriteString(escapeLinkURL(buildEntityURL(typ, id)))
	s.buf.WriteByte(')')
}

func entitySnapshotLabel(n Node) string {
	snap, ok := n["snapshot"].(map[string]any)
	if !ok {
		return ""
	}
	if t, ok := snap["title"].(string); ok && t != "" {
		return t
	}
	if c, ok := snap["code"].(string); ok && c != "" {
		return c
	}
	return ""
}
