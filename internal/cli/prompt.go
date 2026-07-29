package cli

import (
	"fmt"

	"github.com/charmbracelet/huh"
)

type Prompter interface {
	Select(title string, options []huh.Option[string]) (string, error)
	Input(title, placeholder string, validate func(string) error) (string, error)
	Confirm(title string, def bool) (bool, error)
}

type ttyPrompter struct{}

func (ttyPrompter) Select(title string, options []huh.Option[string]) (string, error) {
	var v string
	err := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Options(options...).Value(&v),
	)).Run()
	return v, err
}

func (ttyPrompter) Input(title, placeholder string, validate func(string) error) (string, error) {
	var v string
	input := huh.NewInput().Title(title).Placeholder(placeholder).Value(&v)
	if validate != nil {
		input = input.Validate(validate)
	}
	err := huh.NewForm(huh.NewGroup(input)).Run()
	return v, err
}

func (ttyPrompter) Confirm(title string, def bool) (bool, error) {
	v := def
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(title).Value(&v),
	)).Run()
	return v, err
}

type nonInteractive struct{}

func (nonInteractive) Select(title string, _ []huh.Option[string]) (string, error) {
	return "", fmt.Errorf("%s: required in non-interactive mode (pass a flag)", title)
}

func (nonInteractive) Input(title, _ string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%s: required in non-interactive mode (pass a flag)", title)
}

func (nonInteractive) Confirm(title string, _ bool) (bool, error) {
	return false, fmt.Errorf("%s: required in non-interactive mode (pass a flag)", title)
}

func newPrompter(noPrompt bool) Prompter {
	if noPrompt {
		return nonInteractive{}
	}
	return ttyPrompter{}
}
