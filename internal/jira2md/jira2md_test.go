package jira2md_test

import (
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/jira2md"
)

func TestConvert(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string // substrings that must appear
		deny []string
	}{
		{
			name: "empty",
			in:   "  ",
			want: nil,
		},
		{
			name: "plain markdown passthrough",
			in:   "## Already markdown\n\n- a\n- b",
			want: []string{"## Already markdown", "- a"},
		},
		{
			name: "wiki heading bold link code",
			in:   "h2. Hello\n*bold text*\n[docs|https://example.com]\n{code}\nfmt.Println()\n{code}",
			want: []string{"## Hello", "**bold text**", "[docs](https://example.com)", "```", "fmt.Println()"},
		},
		{
			name: "noformat and panel",
			in:   "{noformat}\nraw\n{noformat}\n{panel}\nnote\n{panel}",
			want: []string{"```", "raw", "> note"},
		},
		{
			name: "color stripped",
			in:   "h1. Title\n{color:red}warn{color}",
			want: []string{"# Title", "warn"},
			deny: []string{"{color"},
		},
		{
			name: "quote and lists",
			in:   "bq. cited\n* one\n# two\nh2. After\n----",
			want: []string{"> cited", "- one", "1. two", "## After", "---"},
		},
		{
			name: "simple url bracket",
			in:   "h3. X\n[https://example.com/a]",
			want: []string{"### X", "https://example.com/a"},
		},
		{
			name: "crlf normalized via wiki path",
			in:   "h1. A\r\n*b*",
			want: []string{"# A", "**b**"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := jira2md.Convert(tt.in)
			if strings.TrimSpace(tt.in) == "" {
				if out != "" {
					t.Fatalf("want empty, got %q", out)
				}
				return
			}
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Fatalf("missing %q in:\n%s", w, out)
				}
			}
			for _, d := range tt.deny {
				if strings.Contains(out, d) {
					t.Fatalf("unexpected %q in:\n%s", d, out)
				}
			}
		})
	}
}
