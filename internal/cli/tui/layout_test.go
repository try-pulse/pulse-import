package tui

import (
	"testing"

	"github.com/clipperhouse/displaywidth"
)

func TestContentWidth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want int
	}{
		{20, minContentWidth},
		{44, 40},
		{80, 76},
		{200, maxContentWidth},
	}
	for _, tt := range tests {
		if got := ContentWidth(tt.in); got != tt.want {
			t.Fatalf("ContentWidth(%d)=%d want %d", tt.in, got, tt.want)
		}
	}
}

func TestTruncateDisplayWidth(t *testing.T) {
	t.Parallel()
	if got := Truncate("hello", 10); got != "hello" {
		t.Fatalf("got %q", got)
	}
	got := Truncate("abcdefghij", 5)
	if displaywidth.String(got) > 5 {
		t.Fatalf("truncate too wide: %q", got)
	}
	got = Truncate("日本語テスト", 5)
	if displaywidth.String(got) > 5 {
		t.Fatalf("cjk truncate too wide: %q width=%d", got, displaywidth.String(got))
	}
}

func TestProgressBarWidth(t *testing.T) {
	t.Parallel()
	if got := ProgressBarWidth(40); got != 12 {
		t.Fatalf("narrow=%d", got)
	}
	if got := ProgressBarWidth(120); got != 40 {
		t.Fatalf("wide=%d", got)
	}
}
