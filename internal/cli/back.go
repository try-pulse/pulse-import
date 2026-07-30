package cli

import (
	"errors"

	"charm.land/huh/v2"

	"github.com/try-pulse/pulse-import/internal/cli/tui"
)

// backPrompter maps user abort to ErrBack so a wizard phase can return to the previous step.
type backPrompter struct {
	inner Prompter
}

func withBack(p Prompter) Prompter {
	if _, ok := p.(nonInteractive); ok {
		return p
	}
	return backPrompter{inner: p}
}

func (b backPrompter) Options() tui.Options { return b.inner.Options() }

func (b backPrompter) mapErr(err error) error {
	if errors.Is(err, ErrCanceled) || errors.Is(err, huh.ErrUserAborted) {
		return ErrBack
	}
	return err
}

func (b backPrompter) Select(title string, options []huh.Option[string]) (string, error) {
	value, err := b.inner.Select(title, options)
	return value, b.mapErr(err)
}

func (b backPrompter) Input(title, placeholder string, validate func(string) error) (string, error) {
	value, err := b.inner.Input(title, placeholder, validate)
	return value, b.mapErr(err)
}

func (b backPrompter) Secret(title, description string, validate func(string) error) (string, error) {
	value, err := b.inner.Secret(title, description, validate)
	return value, b.mapErr(err)
}

func (b backPrompter) Confirm(title string, def bool) (bool, error) {
	value, err := b.inner.Confirm(title, def)
	return value, b.mapErr(err)
}

func (b backPrompter) File(title, description string, validate func(string) error) (string, error) {
	value, err := b.inner.File(title, description, validate)
	return value, b.mapErr(err)
}
