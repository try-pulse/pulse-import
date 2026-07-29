package platemd

func (s *serializer) emitTable(n Node) {
	rows := nodeChildren(n)
	if len(rows) == 0 {
		return
	}
	first := nodeChildren(rows[0])
	nCols := len(first)
	if nCols == 0 {
		return
	}

	s.writeTableRow(first, nCols)
	s.buf.WriteByte('|')
	for i := 0; i < nCols; i++ {
		s.buf.WriteString(" --- |")
	}
	s.buf.WriteByte('\n')

	for _, row := range rows[1:] {
		s.writeTableRow(nodeChildren(row), nCols)
	}
	s.buf.WriteByte('\n')
}

func (s *serializer) writeTableRow(cells []Node, nCols int) {
	s.buf.WriteByte('|')
	for i := 0; i < nCols; i++ {
		s.buf.WriteByte(' ')
		if i < len(cells) {
			sub := s.cloneEmpty()
			sub.emitInline(cellInlineChildren(cells[i]))
			s.buf.WriteString(replaceCellNewlines(sub.buf.String()))
		}
		s.buf.WriteString(" |")
	}
	s.buf.WriteByte('\n')
}

func cellInlineChildren(cell Node) []Node {
	children := nodeChildren(cell)
	out := make([]Node, 0, len(children))
	for _, c := range children {
		if nodeType(c) == "p" {
			out = append(out, nodeChildren(c)...)
			continue
		}
		out = append(out, c)
	}
	return out
}
