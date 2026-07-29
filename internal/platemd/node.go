package platemd

func nodeType(n Node) string {
	if v, ok := n["type"].(string); ok {
		return v
	}
	return ""
}

func nodeChildren(n Node) []Node {
	raw, ok := n["children"].([]any)
	if !ok {
		return nil
	}
	out := make([]Node, 0, len(raw))
	for _, c := range raw {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func nodeString(n Node, key string) string {
	if v, ok := n[key].(string); ok {
		return v
	}
	return ""
}

func nodeBool(n Node, key string) bool {
	if v, ok := n[key].(bool); ok {
		return v
	}
	return false
}

func nodeInt(n Node, key string) (int, bool) {
	switch v := n[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// True for any value including null/false/0 — distinguishes absent from zero (lists).
func hasKey(n Node, key string) bool {
	_, ok := n[key]
	return ok
}

func isText(n Node) bool {
	if _, ok := n["text"].(string); !ok {
		return false
	}
	return nodeType(n) == ""
}

func leafText(n Node) string {
	s, _ := n["text"].(string)
	return s
}
