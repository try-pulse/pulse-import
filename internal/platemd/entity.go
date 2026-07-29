package platemd

import (
	"net/url"
	"strings"
)

// entity_mention ↔ Markdown links like [Title](/issues/<id>).
var entityPathSegment = map[string]string{
	"issue":   "issues",
	"project": "projects",
}

var entitySegmentToType = map[string]string{
	"issues":   "issue",
	"projects": "project",
}

var internalOrigins = map[string]bool{
	"https://app.trypulse.tech": true,
}

type entityRef struct {
	typ string
	id  string
}

func buildEntityURL(typ, id string) string {
	seg := entityPathSegment[typ]
	if seg == "" {
		seg = typ + "s"
	}
	return "/" + seg + "/" + id
}

// Accepts /issues/<id>, /{slug}/issues/<id>, or app.trypulse.tech absolute URLs.
func parseEntityURL(raw string) (entityRef, bool) {
	if raw == "" {
		return entityRef{}, false
	}

	var pathname string
	if strings.HasPrefix(raw, "/") {
		pathname = raw
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return entityRef{}, false
		}
		if !internalOrigins[u.Scheme+"://"+u.Host] {
			return entityRef{}, false
		}
		pathname = u.Path
	}

	segments := splitPathSegments(pathname)

	if len(segments) == 3 {
		if _, ok := entitySegmentToType[segments[1]]; ok {
			segments = segments[1:]
		}
	}
	if len(segments) != 2 {
		return entityRef{}, false
	}

	typ, ok := entitySegmentToType[segments[0]]
	if !ok || !isIDShaped(segments[1]) {
		return entityRef{}, false
	}
	return entityRef{typ: typ, id: segments[1]}, true
}

func splitPathSegments(p string) []string {
	out := make([]string, 0, 4)
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Reject short route words like /issues/new.
func isIDShaped(s string) bool {
	if len(s) < 12 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}
