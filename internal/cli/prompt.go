package cli

import (
	"context"
	"fmt"
	"io"

	"charm.land/huh/v2"
)

type Prompter interface {
	Select(title string, options []huh.Option[string]) (string, error)
	Input(title, placeholder string, validate func(string) error) (string, error)
	Secret(title, description string, validate func(string) error) (string, error)
	Confirm(title string, def bool) (bool, error)
}

type ttyPrompter struct {
	ctx        context.Context
	in         io.Reader
	out        io.Writer
	accessible bool
}

func (p ttyPrompter) run(form *huh.Form) error {
	return form.
		WithInput(p.in).
		WithOutput(p.out).
		WithAccessible(p.accessible).
		RunWithContext(p.ctx)
}

func (p ttyPrompter) Select(title string, options []huh.Option[string]) (string, error) {
	var value string
	err := p.run(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Options(options...).Value(&value),
	)))
	return value, err
}

func (p ttyPrompter) Input(title, placeholder string, validate func(string) error) (string, error) {
	var value string
	input := huh.NewInput().Title(title).Placeholder(placeholder).Value(&value)
	if validate != nil {
		input = input.Validate(validate)
	}
	err := p.run(huh.NewForm(huh.NewGroup(input)))
	if err == nil && validate != nil {
		err = validate(value)
	}
	return value, err
}

func (p ttyPrompter) Secret(title, description string, validate func(string) error) (string, error) {
	var value string
	input := huh.NewInput().
		Title(title).
		Description(description).
		EchoMode(huh.EchoModePassword).
		Value(&value)
	if validate != nil {
		input = input.Validate(validate)
	}
	err := p.run(huh.NewForm(huh.NewGroup(input)))
	if err == nil && validate != nil {
		err = validate(value)
	}
	return value, err
}

func (p ttyPrompter) Confirm(title string, def bool) (bool, error) {
	value := def
	err := p.run(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Value(&value),
	)))
	return value, err
}

type nonInteractive struct{}

func (nonInteractive) Select(title string, _ []huh.Option[string]) (string, error) {
	return "", fmt.Errorf("%s: required in non-interactive mode (pass a flag)", title)
}

func (nonInteractive) Input(title, _ string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%s: required in non-interactive mode (pass a flag)", title)
}

func (nonInteractive) Secret(title, _ string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%s: required in non-interactive mode (set PULSE_ACCESS_TOKEN)", title)
}

func (nonInteractive) Confirm(title string, _ bool) (bool, error) {
	return false, fmt.Errorf("%s: required in non-interactive mode (pass --yes)", title)
}

func newPrompter(
	ctx context.Context,
	noPrompt bool,
	in io.Reader,
	out io.Writer,
	accessible bool,
) Prompter {
	if noPrompt {
		return nonInteractive{}
	}
	return ttyPrompter{ctx: ctx, in: in, out: out, accessible: accessible}
}
