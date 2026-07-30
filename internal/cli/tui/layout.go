// Package tui holds shared terminal layout helpers for interactive prompts.
package tui

import (
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/clipperhouse/displaywidth"
	"golang.org/x/term"
)

const (
	minContentWidth = 40
	maxContentWidth = 100
	framePadding    = 4
)

// Options control how forms and styled output adapt to the terminal.
type Options struct {
	Accessible bool
	Color      bool
	Width      int
}

// Detect builds Options from the prompt streams and environment.
func Detect(in io.Reader, out io.Writer) Options {
	accessible := !IsTerminal(in) || !IsTerminal(out) ||
		strings.EqualFold(os.Getenv("TERM"), "dumb")
	color := !accessible && os.Getenv("NO_COLOR") == "" && IsTerminal(out)
	width := TermWidth(out)
	return Options{
		Accessible: accessible,
		Color:      color,
		Width:      ContentWidth(width),
	}
}

// IsTerminal reports whether value is an interactive character device.
func IsTerminal(value any) bool {
	file, ok := value.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// TermWidth returns the terminal width in cells, or maxContentWidth as a fallback.
func TermWidth(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return maxContentWidth
	}
	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 {
		return maxContentWidth
	}
	return width
}

// ContentWidth clamps a raw terminal width into a comfortable content column.
func ContentWidth(termWidth int) int {
	width := termWidth - framePadding
	if width < minContentWidth {
		return minContentWidth
	}
	if width > maxContentWidth {
		return maxContentWidth
	}
	return width
}

// ProgressBarWidth scales the determinate bar with the terminal.
func ProgressBarWidth(termWidth int) int {
	width := termWidth - 36 // leave room for description + counts
	switch {
	case width < 12:
		return 12
	case width > 40:
		return 40
	default:
		return width
	}
}

// Truncate shortens s to at most maxWidth display cells, appending an ellipsis.
func Truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displaywidth.String(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	return displaywidth.TruncateString(s, maxWidth, "…")
}

// ApplyForm configures a huh form with theme, width, and accessibility.
func ApplyForm(form *huh.Form, opts Options) *huh.Form {
	form = form.
		WithAccessible(opts.Accessible).
		WithShowHelp(!opts.Accessible).
		WithShowErrors(true)
	if opts.Width > 0 && !opts.Accessible {
		// Leave width unset in accessible mode so line prompts stay simple.
		// When width is 0, huh resizes from WindowSizeMsg on its own.
		form = form.WithWidth(opts.Width)
	}
	if opts.Color {
		// ThemeCharm adapts to tea.BackgroundColorMsg at runtime.
		form = form.WithTheme(huh.ThemeFunc(huh.ThemeCharm))
	}
	return form
}
