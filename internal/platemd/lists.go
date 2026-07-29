package platemd

import "strconv"

func isListItem(n Node) bool {
	if nodeType(n) != "p" {
		return false
	}
	return hasKey(n, "listStyleType")
}

// Flat Plate list items (indent + listStyleType) → nested markdown via indent stack.
// Ordered counters seed from listStart on the item that opens a frame.
func (s *serializer) emitListBlock(items []Node) {
	if len(items) == 0 {
		return
	}

	type listFrame struct {
		indent  int
		style   string
		counter int
	}
	stack := make([]listFrame, 0, 4)

	for _, n := range items {
		indent, _ := nodeInt(n, "indent")
		style := nodeString(n, "listStyleType")

		for len(stack) > 0 && stack[len(stack)-1].indent > indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 && stack[len(stack)-1].indent == indent && stack[len(stack)-1].style != style {
			stack = stack[:len(stack)-1]
		}

		if len(stack) == 0 || stack[len(stack)-1].indent < indent {
			start := 1
			if v, ok := nodeInt(n, "listStart"); ok && v > 0 {
				start = v
			}
			stack = append(stack, listFrame{indent: indent, style: style, counter: start})
		} else {
			stack[len(stack)-1].counter++
		}

		depth := len(stack) - 1
		for i := 0; i < depth; i++ {
			s.buf.WriteString("  ")
		}

		switch style {
		case "decimal":
			s.buf.WriteString(strconv.Itoa(stack[len(stack)-1].counter))
			s.buf.WriteString(". ")
		case "todo":
			s.buf.WriteByte(s.opts.BulletMarker)
			s.buf.WriteByte(' ')
			if nodeBool(n, "checked") {
				s.buf.WriteString("[x] ")
			} else {
				s.buf.WriteString("[ ] ")
			}
		default: // disc and any unrecognized style fall back to bullets
			s.buf.WriteByte(s.opts.BulletMarker)
			s.buf.WriteByte(' ')
		}

		s.emitInline(nodeChildren(n))
		s.buf.WriteByte('\n')
	}

	s.buf.WriteByte('\n')
}
