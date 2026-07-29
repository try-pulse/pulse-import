package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"charm.land/huh/v2"
)

func TestAccessiblePrompterInputSelectAndConfirm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(Prompter) (any, error)
		in   string
		want any
	}{
		{
			name: "input", in: "hello\n", want: "hello",
			run: func(p Prompter) (any, error) { return p.Input("Value", "", required) },
		},
		{
			name: "select", in: "2\n", want: "b",
			run: func(p Prompter) (any, error) {
				return p.Select("Pick", []huh.Option[string]{
					huh.NewOption("A", "a"), huh.NewOption("B", "b"),
				})
			},
		},
		{
			name: "confirm", in: "y\n", want: true,
			run: func(p Prompter) (any, error) { return p.Confirm("Proceed", false) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			prompter := newPrompter(
				context.Background(), false, strings.NewReader(tt.in), &output, true,
			)
			got, err := tt.run(prompter)
			if err != nil || got != tt.want {
				t.Fatalf("got=%v want=%v err=%v output=%q", got, tt.want, err, output.String())
			}
		})
	}
}

func TestPrompterSecretAndNonInteractive(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	prompter := newPrompter(
		context.Background(), false, strings.NewReader("secret\n"), &output, true,
	)
	secret, err := prompter.Secret("Token", "", required)
	if err == nil || secret != "" {
		t.Fatalf("secret=%q err=%v output=%q", secret, err, output.String())
	}
	if _, err := (nonInteractive{}).Secret("Token", "", required); err == nil {
		t.Fatal("expected non-interactive error")
	}
}
