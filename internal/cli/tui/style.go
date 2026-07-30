package tui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/clipperhouse/displaywidth"
)

// Styles holds lipgloss styles for plan/result output.
type Styles struct {
	Enabled bool
	Width   int

	Heading lipgloss.Style
	Muted   lipgloss.Style
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
	Box     lipgloss.Style
}

// NewStyles builds semantic styles for the given options and streams.
func NewStyles(opts Options, in, out *os.File) Styles {
	s := Styles{Enabled: opts.Color, Width: opts.Width}
	if s.Width <= 0 {
		s.Width = maxContentWidth
	}
	if !opts.Color {
		return s
	}
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stderr
	}
	dark := lipgloss.HasDarkBackground(in, out)
	lightDark := lipgloss.LightDark(dark)

	s.Heading = lipgloss.NewStyle().Bold(true).Foreground(lightDark(lipgloss.Color("#1a1a1a"), lipgloss.Color("#EDEDED")))
	s.Muted = lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#6B7280"), lipgloss.Color("#9CA3AF")))
	s.Success = lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#15803D"), lipgloss.Color("#4ADE80")))
	s.Warning = lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#A16207"), lipgloss.Color("#FBBF24")))
	s.Error = lipgloss.NewStyle().Foreground(lightDark(lipgloss.Color("#B91C1C"), lipgloss.Color("#F87171")))
	s.Box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lightDark(lipgloss.Color("#D1D5DB"), lipgloss.Color("#374151"))).
		Padding(0, 1).
		Width(s.Width)
	return s
}

// HeadingText styles a section heading.
func (s Styles) HeadingText(text string) string {
	if !s.Enabled {
		return text
	}
	return s.Heading.Render(text)
}

// WarnLine prefixes a warning with a symbol (or plain "warning:").
func (s Styles) WarnLine(text string) string {
	if !s.Enabled {
		return "warning: " + text
	}
	return s.Warning.Render("⚠ ") + text
}

// ErrorLine prefixes an error with a symbol (or plain "error:").
func (s Styles) ErrorLine(text string) string {
	if !s.Enabled {
		return "error: " + text
	}
	return s.Error.Render("✗ ") + text
}

// OKLine prefixes success text with a check mark when color is enabled.
func (s Styles) OKLine(text string) string {
	if !s.Enabled {
		return text
	}
	return s.Success.Render("✓ ") + text
}

// MutedText softens secondary copy.
func (s Styles) MutedText(text string) string {
	if !s.Enabled {
		return text
	}
	return s.Muted.Render(text)
}

// StatusLine writes a short status message to errOut (spinners / parsing notes).
func StatusLine(errOut io.Writer, msg string) {
	_, _ = fmt.Fprintf(errOut, "%s\n", msg)
}

// StepBanner prints a phase header for wizard chrome.
func StepBanner(out io.Writer, styles Styles, step, total int, title string) {
	label := fmt.Sprintf("Step %d of %d · %s", step, total, title)
	if styles.Enabled {
		_, _ = fmt.Fprintf(out, "\n%s\n", styles.HeadingText(label))
		_, _ = fmt.Fprintf(out, "%s\n", styles.MutedText(strings.Repeat("─", min(styles.Width, displaywidth.String(label)+8))))
		return
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", label)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
