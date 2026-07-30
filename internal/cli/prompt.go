package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"

	"github.com/try-pulse/pulse-import/internal/cli/tui"
)

// ErrCanceled is returned when the user aborts the first wizard phase.
var ErrCanceled = errors.New("import canceled — no Pulse changes were made")

// ErrBack is returned when the user leaves a later wizard phase to revisit the previous one.
var ErrBack = errors.New("back")

type Prompter interface {
	Select(title string, options []huh.Option[string]) (string, error)
	Input(title, placeholder string, validate func(string) error) (string, error)
	Secret(title, description string, validate func(string) error) (string, error)
	Confirm(title string, def bool) (bool, error)
	File(title, description string, validate func(string) error) (string, error)
	Options() tui.Options
}

type ttyPrompter struct {
	ctx  context.Context
	in   io.Reader
	out  io.Writer
	opts tui.Options
}

func (p ttyPrompter) Options() tui.Options { return p.opts }

func (p ttyPrompter) run(form *huh.Form) error {
	err := tui.ApplyForm(form, p.opts).
		WithInput(p.in).
		WithOutput(p.out).
		RunWithContext(p.ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrCanceled
	}
	return err
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

func (p ttyPrompter) File(title, description string, validate func(string) error) (string, error) {
	if p.opts.Accessible {
		return p.Input(title, "./export.csv", validate)
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	var value string
	picker := huh.NewFilePicker().
		Title(title).
		Description(description).
		CurrentDirectory(cwd).
		AllowedTypes([]string{".csv"}).
		ShowHidden(false).
		Picking(true).
		DirAllowed(true).
		FileAllowed(true).
		Value(&value)
	if validate != nil {
		picker = picker.Validate(validate)
	}
	height := 12
	if p.opts.Width < 60 {
		height = 8
	}
	picker = picker.Height(height)
	err = p.run(huh.NewForm(huh.NewGroup(picker)))
	if err != nil {
		return "", err
	}
	if validate != nil {
		if err := validate(value); err != nil {
			return "", err
		}
	}
	return value, nil
}

type nonInteractive struct{}

func (nonInteractive) Options() tui.Options { return tui.Options{} }

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

func (nonInteractive) File(title, _ string, _ func(string) error) (string, error) {
	return "", fmt.Errorf("%s: required in non-interactive mode (pass --file)", title)
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
	opts := tui.Detect(in, out)
	if accessible {
		opts.Accessible = true
		opts.Color = false
	}
	return ttyPrompter{ctx: ctx, in: in, out: out, opts: opts}
}

// runSourceForm gathers importer id, CSV path and Jira URL in one multi-group form
// so the user can move between fields with next/prev. Returns ErrCanceled on abort.
// When force is true, every field is shown again even if defaults are already set
// (used when the wizard returns to Source via Back).
func runSourceForm(
	p Prompter,
	importerOptions []huh.Option[string],
	defaults sourceDefaults,
	force bool,
) (sourceAnswers, error) {
	tty, ok := p.(ttyPrompter)
	if !ok || tty.opts.Accessible {
		return runSourceFormSequential(p, importerOptions, defaults, force)
	}
	// Multi-group form always shows every field; `force` only affects the sequential path.

	answers := sourceAnswers{
		ImporterID: defaults.ImporterID,
		FilePath:   defaults.FilePath,
		JiraURL:    defaults.JiraURL,
		IsCloud:    defaults.IsCloud,
	}
	if answers.ImporterID == "" && len(importerOptions) == 1 {
		answers.ImporterID = importerOptions[0].Value
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	// Prefer starting the picker in the current directory without inventing a
	// path that fails validation when the placeholder file does not exist.
	fileValue := answers.FilePath
	if fileValue != "" {
		if _, statErr := os.Stat(fileValue); statErr != nil {
			fileValue = ""
		}
	}

	importerField := huh.NewSelect[string]().
		Title("Which service would you like to import from?").
		Options(importerOptions...).
		Value(&answers.ImporterID)
	if len(importerOptions) <= 1 {
		importerField = importerField.Description("Jira CSV is the only importer in v1")
	}

	fileField := huh.NewFilePicker().
		Title("Jira CSV export").
		Description("Export Excel CSV (all fields) from Jira Advanced issue search.").
		CurrentDirectory(cwd).
		AllowedTypes([]string{".csv"}).
		ShowHidden(false).
		Picking(true).
		DirAllowed(true).
		FileAllowed(true).
		Height(10).
		Value(&fileValue).
		Validate(validateFileExists)

	cloudField := huh.NewConfirm().
		Title("Is your Jira installation on Jira Cloud (*.atlassian.net)?").
		Value(&answers.IsCloud)

	urlField := huh.NewInput().
		Title("Jira base URL").
		Placeholder("https://acme.atlassian.net").
		Value(&answers.JiraURL).
		Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return fmt.Errorf("required")
			}
			if answers.IsCloud {
				site, _, err := parseJiraURL(s)
				if err != nil || site == "" {
					return fmt.Errorf("expected https://<site>.atlassian.net")
				}
				return nil
			}
			_, _, err := parseJiraURL(s)
			return err
		})

	form := huh.NewForm(
		huh.NewGroup(importerField, fileField).
			Title("Source").
			Description("Choose the export and the CSV file. Esc cancels."),
		huh.NewGroup(cloudField, urlField).
			Title("Jira site").
			Description("Used to rewrite attachment and issue links in Main Docs."),
	)
	if err := tty.run(form); err != nil {
		return sourceAnswers{}, err
	}
	answers.FilePath = fileValue
	path, err := absExisting(answers.FilePath)
	if err != nil {
		return sourceAnswers{}, err
	}
	answers.FilePath = path
	answers.JiraURL = strings.TrimSpace(answers.JiraURL)
	return answers, nil
}

type sourceDefaults struct {
	ImporterID string
	FilePath   string
	JiraURL    string
	IsCloud    bool
}

type sourceAnswers struct {
	ImporterID string
	FilePath   string
	JiraURL    string
	IsCloud    bool
}

func runSourceFormSequential(
	p Prompter,
	importerOptions []huh.Option[string],
	defaults sourceDefaults,
	force bool,
) (sourceAnswers, error) {
	answers := sourceAnswers{
		ImporterID: defaults.ImporterID,
		FilePath:   defaults.FilePath,
		JiraURL:    defaults.JiraURL,
		IsCloud:    defaults.IsCloud,
	}
	var err error
	if answers.ImporterID == "" && len(importerOptions) == 1 {
		answers.ImporterID = importerOptions[0].Value
	}
	if answers.ImporterID == "" || (force && len(importerOptions) > 1) {
		answers.ImporterID, err = p.Select("Which service would you like to import from?", importerOptions)
		if err != nil {
			return sourceAnswers{}, err
		}
	}
	if answers.FilePath == "" || force {
		answers.FilePath, err = p.File(
			"Path to Jira CSV export",
			"Export Excel CSV (all fields) from Jira Advanced issue search.",
			validateFileExists,
		)
		if err != nil {
			return sourceAnswers{}, err
		}
	}
	path, err := absExisting(answers.FilePath)
	if err != nil {
		return sourceAnswers{}, err
	}
	answers.FilePath = path

	if answers.JiraURL == "" || force {
		cloudDefault := answers.IsCloud
		if answers.JiraURL == "" && defaults.JiraURL == "" {
			cloudDefault = true
		}
		answers.IsCloud, err = p.Confirm("Is your Jira installation on Jira Cloud (*.atlassian.net)?", cloudDefault)
		if err != nil {
			return sourceAnswers{}, err
		}
		placeholder := "https://acme.atlassian.net"
		if !answers.IsCloud {
			placeholder = "https://jira.mydomain.com"
		} else if answers.JiraURL != "" {
			placeholder = answers.JiraURL
		}
		if answers.IsCloud {
			answers.JiraURL, err = p.Input("Jira Cloud URL", placeholder, func(s string) error {
				site, _, parseErr := parseJiraURL(s)
				if parseErr != nil || site == "" {
					return fmt.Errorf("expected https://<site>.atlassian.net")
				}
				return nil
			})
		} else {
			answers.JiraURL, err = p.Input("On-prem Jira URL", placeholder, required)
		}
		if err != nil {
			return sourceAnswers{}, err
		}
	}
	answers.JiraURL = strings.TrimSpace(answers.JiraURL)
	return answers, nil
}
