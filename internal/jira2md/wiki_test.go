package jira2md_test

import (
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/jira2md"
	"github.com/try-pulse/pulse-import/internal/platemd"
)

// TestConvertWikiMarkup covers the Jira constructs a real "all fields" export
// puts in the Description column.
func TestConvertWikiMarkup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "heading without space after dot",
			in:   "h3.Steps to reproduce",
			want: "### Steps to reproduce",
		},
		{
			name: "nested bullets",
			in:   "* top\n** child\n*** grandchild",
			want: "- top\n  - child\n    - grandchild",
		},
		{
			name: "nested numbered",
			in:   "# one\n## one a\n# two",
			want: "1. one\n  1. one a\n1. two",
		},
		{
			name: "mixed bullet then numbered",
			in:   "* bullet\n*# numbered child",
			want: "- bullet\n  1. numbered child",
		},
		{
			name: "table with header",
			in:   "||Name||Value||\n|alpha|1|\n|beta|2|",
			want: "| Name | Value |\n| --- | --- |\n| alpha | 1 |\n| beta | 2 |",
		},
		{
			name: "table without header row still gets a delimiter",
			in:   "|alpha|1|\n|beta|2|",
			want: "| alpha | 1 |\n| --- | --- |\n| beta | 2 |",
		},
		{
			name: "quote block",
			in:   "{quote}\nthey said this\n{quote}",
			want: "> they said this",
		},
		{
			name: "panel with title",
			in:   "{panel:title=Note}\nbe careful\n{panel}",
			want: "> be careful",
		},
		{
			name: "strikethrough",
			in:   "this is -gone- now",
			want: "this is ~~gone~~ now",
		},
		{
			name: "hyphenated words are not strikethrough",
			in:   "a well-known set-up value",
			want: "a well-known set-up value",
		},
		{
			name: "dash surrounded by spaces is not strikethrough",
			in:   "alpha - beta - gamma",
			want: "alpha - beta - gamma",
		},
		{
			name: "insert becomes underline",
			in:   "please +read this+ first",
			want: "please <u>read this</u> first",
		},
		{
			name: "superscript and subscript",
			in:   "x^2^ and H~2~O",
			want: "x<sup>2</sup> and H<sub>2</sub>O",
		},
		{
			name: "monospace",
			in:   "call {{doThing()}} now",
			want: "call `doThing()` now",
		},
		{
			name: "named link",
			in:   "see [the docs|https://example.com/a_b-c]",
			want: "see [the docs](https://example.com/a_b-c)",
		},
		{
			name: "named link with smart-link suffix drops the extra field",
			in:   "[docs|https://example.com|smart-link]",
			want: "[docs](https://example.com)",
		},
		{
			name: "url with underscores is not italicised",
			in:   "[https://example.com/a_b_c]",
			want: "https://example.com/a_b_c",
		},
		{
			name: "mention kept as a handle",
			in:   "ping [~accountid:557058:abc-1] please",
			want: "ping `@557058:abc-1` please",
		},
		{
			name: "legacy username mention",
			in:   "[~jsmith] owns this",
			want: "`@jsmith` owns this",
		},
		{
			name: "horizontal rule",
			in:   "before\n----\nafter",
			want: "before\n---\nafter",
		},
		{
			name: "bold inside a list item",
			in:   "* a *strong* point",
			want: "- a **strong** point",
		},
		{
			name: "code fence content untouched",
			in:   "{code:go}\nx := a-b\nh1. no\n{code}",
			want: "```go\nx := a-b\nh1. no\n```",
		},
		{
			name: "colour wrapping bold",
			in:   "{color:#ff0000}*urgent*{color}",
			want: "**urgent**",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := jira2md.Convert(tt.in); got != tt.want {
				t.Fatalf("Convert(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestConvertAttachmentMacros(t *testing.T) {
	t.Parallel()
	attachments := map[string]string{
		"shot.png": "https://acme.atlassian.net/secure/attachment/1/shot.png",
	}

	t.Run("known attachment becomes a link", func(t *testing.T) {
		t.Parallel()
		got := jira2md.ConvertWithAttachments("see !shot.png|thumbnail!", attachments)
		want := "see [shot.png](https://acme.atlassian.net/secure/attachment/1/shot.png)"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	t.Run("unknown attachment keeps the filename", func(t *testing.T) {
		t.Parallel()
		got := jira2md.ConvertWithAttachments("see !missing.png!", attachments)
		if got != "see `missing.png`" {
			t.Fatalf("got %q", got)
		}
	})
}

// TestConvertedMarkdownSurvivesPlate is the contract that matters: whatever the
// converter emits has to become Plate JSON that keeps the structure, because
// that JSON is what Pulse stores as the Main Doc.
func TestConvertedMarkdownSurvivesPlate(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"h2. Summary",
		"Some *bold* and -struck- text with {{code}}.",
		"* first",
		"** nested",
		"# step one",
		"# step two",
		"||Col A||Col B||",
		"|1|2|",
		"{quote}",
		"quoted line",
		"{quote}",
		"{code:go}",
		"fmt.Println()",
		"{code}",
	}, "\n")

	nodes := platemd.ToNodes(jira2md.Convert(source), nil)
	if len(nodes) == 0 {
		t.Fatal("no Plate nodes produced")
	}

	types := map[string]int{}
	for _, node := range nodes {
		if t, ok := node["type"].(string); ok {
			types[t]++
		}
	}
	for _, want := range []string{"h2", "table", "blockquote", "code_block"} {
		if types[want] == 0 {
			t.Errorf("expected a %q node, got types %v", want, types)
		}
	}

	// Round-tripping must not lose the marks or the list nesting.
	back, err := platemd.FromNodes(nodes, nil)
	if err != nil {
		t.Fatalf("FromNodes: %v", err)
	}
	for _, want := range []string{"**bold**", "~~struck~~", "`code`", "  - nested", "| Col A |", "> quoted line"} {
		if !strings.Contains(back, want) {
			t.Errorf("round-trip lost %q; got:\n%s", want, back)
		}
	}
}
