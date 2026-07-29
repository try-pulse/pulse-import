// Package platemd converts Plate.js JSON ↔ Markdown for the node set Pulse uses.
// Parse is scoped to what the serializer emits (plus GFM tables & task lists),
// not general CommonMark — that closed scope keeps serialize→parse→serialize stable.
package platemd

import (
	"encoding/json"
	"fmt"
)

type Node = map[string]any

// Options configures markdown emission. Zero value matches @platejs/markdown defaults
// (emphasis "_", strong "**", bullet "-").
type Options struct {
	// HeadingShift promotes (negative) or demotes (positive) headings; clamped to 1..6.
	HeadingShift int

	EmphasisMarker byte // '_' (default) or '*'
	StrongMarker   byte // '*' (default) or '_'
	BulletMarker   byte // '-' (default), '*', or '+'

	// PreserveEmptyParagraphs emits a ZWSP line so blank paragraphs survive round-trip.
	PreserveEmptyParagraphs bool
}

func (o Options) resolved() Options {
	if o.EmphasisMarker == 0 {
		o.EmphasisMarker = '_'
	}
	if o.StrongMarker == 0 {
		o.StrongMarker = '*'
	}
	if o.BulletMarker == 0 {
		o.BulletMarker = '-'
	}
	return o
}

func Convert(jsonStr string) (string, error) {
	return FromJSON([]byte(jsonStr), nil)
}

func MustConvert(jsonStr string) string {
	md, err := Convert(jsonStr)
	if err != nil {
		panic(err)
	}
	return md
}

func FromJSON(data []byte, opts *Options) (string, error) {
	var raw []Node
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("platemd: invalid JSON: %w", err)
	}
	return FromNodes(raw, opts)
}

func FromNodes(nodes []Node, opts *Options) (string, error) {
	o := Options{}
	if opts != nil {
		o = *opts
	}
	s := newSerializer(o.resolved())
	s.walkBlocks(nodes)
	return s.finalize(), nil
}
