package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/try-pulse/pulse-import/internal/importers/jiracsv"
	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

func TestParseStatusFilter(t *testing.T) {
	t.Parallel()
	got, err := parseStatusFilter([]string{"done, qa", "backlog"}, "--skip-status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []statusmap.PulseStatus{statusmap.Done, statusmap.QA, statusmap.Backlog} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if got[statusmap.Todo] {
		t.Error("todo should not be in the set")
	}
	if _, err := parseStatusFilter([]string{"in progress"}, "--skip-status"); err != nil {
		t.Errorf("a space-separated status name should be accepted: %v", err)
	}
	if _, err := parseStatusFilter([]string{"nope"}, "--skip-status"); err == nil {
		t.Error("expected an error for an unknown status")
	}
	if set, err := parseStatusFilter(nil, "--skip-status"); err != nil || set != nil {
		t.Errorf("empty input should produce no filter: %v %v", set, err)
	}
}

func TestParseStale(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "180", want: 180 * 24 * time.Hour},
		{in: "4320h", want: 4320 * time.Hour},
		{in: "0", want: 0},
		// A trailing unit that is not a Go duration must be rejected outright,
		// not silently read as "180".
		{in: "180days", wantErr: true},
		{in: "-5", wantErr: true},
		{in: "-5h", wantErr: true},
		{in: "soon", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parseStale(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseStale(%q) = %v, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseStale(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseStale(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseUserMap(t *testing.T) {
	t.Parallel()
	got, err := parseUserMap([]string{"Jane Doe=user-1", "John Smith=skip"})
	if err != nil {
		t.Fatal(err)
	}
	if got["jane doe"] != "user-1" || got["john smith"] != "skip" {
		t.Fatalf("map = %v", got)
	}
	for _, bad := range []string{"no-equals", "=user-1", "Jane="} {
		if _, err := parseUserMap([]string{bad}); err == nil {
			t.Errorf("parseUserMap(%q) should fail", bad)
		}
	}
	if _, err := parseUserMap([]string{"Jane=a", "jane=b"}); err == nil {
		t.Error("a conflicting duplicate mapping should fail")
	}
	if _, err := parseUserMap([]string{"Jane=a", "jane=a"}); err != nil {
		t.Errorf("an identical duplicate is harmless: %v", err)
	}
}

func TestAssigneeModeFromFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opts    Options
		want    runner.AssigneeMode
		wantErr string
	}{
		{name: "self-assign shorthand", opts: Options{SelfAssign: true}, want: runner.AssigneeSelf},
		{name: "explicit none", opts: Options{Assignee: "none"}, want: runner.AssigneeNone},
		{name: "explicit mapped", opts: Options{Assignee: "MAPPED"}, want: runner.AssigneeMapped},
		{name: "non-interactive default", opts: Options{NoPrompt: true}, want: runner.AssigneeMapped},
		{name: "unknown value", opts: Options{Assignee: "sideways"}, wantErr: "must be one of"},
		{
			name:    "conflicting flags",
			opts:    Options{SelfAssign: true, Assignee: "none"},
			wantErr: "conflicts with",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := assigneeMode(tt.opts, nonInteractive{})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Fatalf("mode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEpicMode(t *testing.T) {
	t.Parallel()
	if got, err := epicMode(Options{}); err != nil || got != jiracsv.EpicModeProject {
		t.Fatalf("default = %q err=%v", got, err)
	}
	if got, err := epicMode(Options{Epics: "label"}); err != nil || got != jiracsv.EpicModeLabel {
		t.Fatalf("label = %q err=%v", got, err)
	}
	if _, err := epicMode(Options{Epics: "banana"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLabelPolicy(t *testing.T) {
	t.Parallel()
	if got := labelPolicy(Options{}); got != runner.LabelPolicyDrop {
		t.Fatalf("default = %q", got)
	}
	if got := labelPolicy(Options{StrictLabels: true}); got != runner.LabelPolicyError {
		t.Fatalf("strict = %q", got)
	}
}
