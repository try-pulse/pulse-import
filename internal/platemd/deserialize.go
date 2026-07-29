package platemd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ToNodes never errors: malformed structure falls back to paragraph text.
// HeadingShift is applied in reverse so Options round-trips with Convert.
func ToNodes(markdown string, opts *Options) []Node {
	o := Options{}
	if opts != nil {
		o = *opts
	}
	o = o.resolved()

	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	sc := &blockScanner{lines: strings.Split(normalized, "\n"), opts: o}
	return sc.run()
}

func ToJSON(markdown string, opts *Options) ([]byte, error) {
	return json.Marshal(ToNodes(markdown, opts))
}

func mkElem(typ string, children []Node) Node {
	return Node{"type": typ, "children": toAnySlice(children)}
}

// Widen for JSON array shape expected by nodeChildren without a marshal round-trip.
func toAnySlice(nodes []Node) []any {
	out := make([]any, len(nodes))
	for i, n := range nodes {
		out[i] = n
	}
	return out
}

type blockScanner struct {
	lines []string
	opts  Options
	i     int
}

func (b *blockScanner) run() []Node {
	blocks := []Node{}
	for b.i < len(b.lines) {
		line := b.lines[b.i]

		if line == "" {
			b.i++
			continue
		}
		if line == "​" { // preserved empty paragraph (zero-width space)
			blocks = append(blocks, mkElem("p", []Node{{"text": ""}}))
			b.i++
			continue
		}
		if lang, ok := fenceLang(line); ok {
			blocks = append(blocks, b.scanCodeBlock(lang))
			continue
		}
		if strings.HasPrefix(line, ">") {
			blocks = append(blocks, b.scanBlockquote())
			continue
		}
		if line == "---" {
			blocks = append(blocks, mkElem("hr", []Node{{"text": ""}}))
			b.i++
			continue
		}
		if level, content, ok := parseHeadingLine(line); ok {
			depth := level - b.opts.HeadingShift
			if depth < 1 {
				depth = 1
			}
			if depth > 6 {
				depth = 6
			}
			blocks = append(blocks, mkElem(fmt.Sprintf("h%d", depth), parseInline(content, b.opts)))
			b.i++
			continue
		}
		if b.isTableStart() {
			blocks = append(blocks, b.scanTable())
			continue
		}
		if _, ok := parseListMarker(line); ok {
			blocks = append(blocks, b.scanList()...)
			continue
		}
		if img, ok := parseImageLine(line); ok {
			blocks = append(blocks, img)
			b.i++
			continue
		}
		blocks = append(blocks, b.scanParagraph())
	}
	return blocks
}

func fenceLang(line string) (string, bool) {
	if !strings.HasPrefix(line, "```") {
		return "", false
	}
	return strings.TrimSpace(line[3:]), true
}

func (b *blockScanner) scanCodeBlock(lang string) Node {
	b.i++ // consume opening fence
	var codeLines []string
	for b.i < len(b.lines) && b.lines[b.i] != "```" {
		codeLines = append(codeLines, b.lines[b.i])
		b.i++
	}
	if b.i < len(b.lines) && b.lines[b.i] == "```" {
		b.i++ // consume closing fence
	}

	var children []Node
	if len(codeLines) == 0 {
		children = []Node{mkElem("code_line", []Node{{"text": ""}})}
	} else {
		for _, cl := range codeLines {
			children = append(children, mkElem("code_line", []Node{{"text": cl}}))
		}
	}
	n := mkElem("code_block", children)
	if lang != "" {
		n["lang"] = lang
	}
	return n
}

func (b *blockScanner) scanBlockquote() Node {
	var inner []string
	for b.i < len(b.lines) && strings.HasPrefix(b.lines[b.i], ">") {
		stripped := b.lines[b.i][1:]
		stripped = strings.TrimPrefix(stripped, " ")
		inner = append(inner, stripped)
		b.i++
	}
	sub := &blockScanner{lines: strings.Split(strings.Join(inner, "\n"), "\n"), opts: b.opts}
	return mkElem("blockquote", sub.run())
}

func parseHeadingLine(line string) (int, string, bool) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n < 1 || n > 6 || n >= len(line) || line[n] != ' ' {
		return 0, "", false
	}
	return n, line[n+1:], true
}

type listItem struct {
	indent    int
	style     string
	hasStart  bool
	listStart int
	hasCheck  bool
	checked   bool
	content   string
}

func parseListMarker(line string) (listItem, bool) {
	rs := []rune(line)
	idx := 0
	for idx < len(rs) && rs[idx] == ' ' {
		idx++
	}
	indent := 1 + idx/2
	if idx >= len(rs) {
		return listItem{}, false
	}
	rest := rs[idx:]
	first := rest[0]

	if first == '-' || first == '*' || first == '+' {
		if len(rest) < 2 || rest[1] != ' ' {
			return listItem{}, false
		}
		afterMarker := rest[2:]
		if len(afterMarker) >= 3 && afterMarker[0] == '[' &&
			(afterMarker[1] == ' ' || afterMarker[1] == 'x' || afterMarker[1] == 'X') &&
			afterMarker[2] == ']' {
			content := afterMarker[3:]
			if len(content) > 0 && content[0] == ' ' {
				content = content[1:]
			}
			return listItem{
				indent:   indent,
				style:    "todo",
				hasCheck: true,
				checked:  afterMarker[1] == 'x' || afterMarker[1] == 'X',
				content:  string(content),
			}, true
		}
		return listItem{indent: indent, style: "disc", content: string(afterMarker)}, true
	}

	if first >= '0' && first <= '9' {
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j < len(rest) && (rest[j] == '.' || rest[j] == ')') && j+1 < len(rest) && rest[j+1] == ' ' {
			num, _ := strconv.Atoi(string(rest[0:j]))
			it := listItem{indent: indent, style: "decimal", content: string(rest[j+2:])}
			if num != 1 {
				it.hasStart = true
				it.listStart = num
			}
			return it, true
		}
	}
	return listItem{}, false
}

func (b *blockScanner) scanList() []Node {
	var out []Node
	for b.i < len(b.lines) {
		it, ok := parseListMarker(b.lines[b.i])
		if !ok {
			break
		}
		n := mkElem("p", parseInline(it.content, b.opts))
		n["listStyleType"] = it.style
		n["indent"] = it.indent
		if it.hasStart {
			n["listStart"] = it.listStart
		}
		if it.hasCheck {
			n["checked"] = it.checked
		}
		out = append(out, n)
		b.i++
	}
	return out
}

func (b *blockScanner) isTableStart() bool {
	if !strings.Contains(b.lines[b.i], "|") || b.i+1 >= len(b.lines) {
		return false
	}
	return isDelimiterRow(b.lines[b.i+1])
}

func isDelimiterRow(line string) bool {
	cells := splitTableCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		trimmed := strings.TrimSpace(cell)
		body := strings.TrimPrefix(trimmed, ":")
		core := strings.TrimSuffix(body, ":")
		if core == "" {
			return false
		}
		for _, r := range core {
			if r != '-' {
				return false
			}
		}
	}
	return true
}

func (b *blockScanner) scanTable() Node {
	headerCells := splitTableCells(b.lines[b.i])
	b.i += 2 // header + delimiter row
	rows := []Node{b.tableRow(headerCells, true)}
	for b.i < len(b.lines) && strings.Contains(b.lines[b.i], "|") {
		if _, ok := parseListMarker(b.lines[b.i]); ok {
			break
		}
		rows = append(rows, b.tableRow(splitTableCells(b.lines[b.i]), false))
		b.i++
	}
	return mkElem("table", rows)
}

func (b *blockScanner) tableRow(cells []string, header bool) Node {
	typ := "td"
	if header {
		typ = "th"
	}
	cellNodes := make([]Node, 0, len(cells))
	for _, cell := range cells {
		inline := parseInline(strings.TrimSpace(cell), b.opts)
		if len(inline) == 0 {
			inline = []Node{{"text": ""}}
		}
		cellNodes = append(cellNodes, mkElem(typ, []Node{mkElem("p", inline)}))
	}
	return mkElem("tr", cellNodes)
}

func splitTableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	if strings.HasSuffix(trimmed, "|") && !strings.HasSuffix(trimmed, "\\|") {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return splitUnescaped(trimmed, '|')
}

func splitUnescaped(s string, sep rune) []string {
	var out []string
	var cur []rune
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			cur = append(cur, rs[i], rs[i+1])
			i++
			continue
		}
		if rs[i] == sep {
			out = append(out, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, rs[i])
	}
	out = append(out, string(cur))
	return out
}

func parseImageLine(line string) (Node, bool) {
	rs := []rune(line)
	if len(rs) <= 4 || rs[0] != '!' || rs[1] != '[' {
		return nil, false
	}
	closeBracket, ok := firstUnescaped(']', rs, 2)
	if !ok || closeBracket+1 >= len(rs) || rs[closeBracket+1] != '(' {
		return nil, false
	}
	closeParen, ok := firstUnescaped(')', rs, closeBracket+2)
	if !ok || closeParen != len(rs)-1 {
		return nil, false
	}

	alt := unescapeText(string(rs[2:closeBracket]))
	u := unescapeLinkURL(string(rs[closeBracket+2 : closeParen]))
	n := mkElem("img", []Node{{"text": ""}})
	n["url"] = u
	if alt != "" {
		n["caption"] = []any{Node{"text": alt}}
	}
	return n, true
}

func firstUnescaped(target rune, rs []rune, from int) (int, bool) {
	for i := from; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			i++
			continue
		}
		if rs[i] == target {
			return i, true
		}
	}
	return 0, false
}

func (b *blockScanner) scanParagraph() Node {
	paraLines := []string{b.lines[b.i]}
	b.i++
	for b.i < len(b.lines) && b.isParagraphContinuation(b.lines[b.i]) {
		paraLines = append(paraLines, b.lines[b.i])
		b.i++
	}
	return mkElem("p", parseInline(strings.Join(paraLines, "\n"), b.opts))
}

func (b *blockScanner) isParagraphContinuation(line string) bool {
	if line == "" || line == "​" {
		return false
	}
	if _, ok := fenceLang(line); ok {
		return false
	}
	if strings.HasPrefix(line, ">") {
		return false
	}
	if line == "---" {
		return false
	}
	if _, _, ok := parseHeadingLine(line); ok {
		return false
	}
	if _, ok := parseListMarker(line); ok {
		return false
	}
	if _, ok := parseImageLine(line); ok {
		return false
	}
	if strings.Contains(line, "|") && b.i+1 < len(b.lines) && isDelimiterRow(b.lines[b.i+1]) {
		return false
	}
	return true
}
