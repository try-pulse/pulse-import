package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/try-pulse/pulse-import/internal/auth"
	"github.com/try-pulse/pulse-import/internal/cli/tui"
	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importers/jiracsv"
	"github.com/try-pulse/pulse-import/internal/runner"
)

func TestBackPrompterMapsCancelToBack(t *testing.T) {
	t.Parallel()
	p := withBack(cancelPrompter{})
	if _, err := p.Confirm("x", false); !errors.Is(err, ErrBack) {
		t.Fatalf("got %v want ErrBack", err)
	}
	if _, err := p.Select("x", nil); !errors.Is(err, ErrBack) {
		t.Fatalf("select: got %v want ErrBack", err)
	}
}

func TestWithBackLeavesNonInteractiveAlone(t *testing.T) {
	t.Parallel()
	p := withBack(nonInteractive{})
	if _, ok := p.(nonInteractive); !ok {
		t.Fatalf("got %T, want nonInteractive", p)
	}
}

func TestCloneStringMap(t *testing.T) {
	t.Parallel()
	in := map[string]string{"a": "1"}
	out := cloneStringMap(in)
	out["a"] = "2"
	if in["a"] != "1" {
		t.Fatal("clone mutated source")
	}
	if cloneStringMap(nil) == nil {
		t.Fatal("nil should become empty map")
	}
}

func TestRunSourceFormSequentialForceReasksFileAndURL(t *testing.T) {
	t.Parallel()
	csvPath := writeCSV(t, "Summary,Issue key,Issue id,Issue Type,Status\nA,ENG-1,1,Story,To Do\n")
	p := &scriptedPrompter{
		inputs:   []string{csvPath, "https://other.atlassian.net"},
		confirms: []bool{true},
	}
	got, err := runSourceFormSequential(p, []huh.Option[string]{
		huh.NewOption("Jira (CSV export)", "jira-csv"),
	}, sourceDefaults{
		ImporterID: "jira-csv",
		FilePath:   csvPath,
		JiraURL:    "https://acme.atlassian.net",
		IsCloud:    true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != csvPath {
		t.Fatalf("file=%q", got.FilePath)
	}
	if got.JiraURL != "https://other.atlassian.net" {
		t.Fatalf("jira=%q", got.JiraURL)
	}
}

func TestWizardSourceRevisitClearsCacheWhenFileChanges(t *testing.T) {
	t.Parallel()
	csvA := writeCSV(t, "Summary,Issue key,Issue id,Issue Type,Status\nA,ENG-1,1,Story,To Do\n")
	csvB := writeCSV(t, "Summary,Issue key,Issue id,Issue Type,Status\nB,ENG-2,2,Story,To Do\n")

	state := &wizardState{
		source: sourceAnswers{
			ImporterID: "jira-csv",
			FilePath:   csvA,
			JiraURL:    "https://acme.atlassian.net",
			IsCloud:    true,
		},
	}
	opts := &Options{Importer: "jira-csv", File: csvA, JiraURL: "https://acme.atlassian.net"}
	var out, errOut bytes.Buffer
	if err := wizardSource(context.Background(), &out, &errOut, opts, jiracsv.EpicModeProject, nonInteractive{}, state); err != nil {
		t.Fatal(err)
	}
	if state.data == nil || state.parsedFile != csvA {
		t.Fatalf("expected cached parse of A, got parsedFile=%q data=%v", state.parsedFile, state.data != nil)
	}
	// Plant a sentinel so we can prove the cache was dropped before re-parse.
	state.data = &importers.ImportResult{Issues: []importers.Issue{{Key: "STALE"}}, SourcePath: csvA}

	state.revisitSource = true
	p := &scriptedPrompter{
		inputs:   []string{csvB, "https://acme.atlassian.net"},
		confirms: []bool{true},
	}
	if err := wizardSource(context.Background(), &out, &errOut, opts, jiracsv.EpicModeProject, p, state); err != nil {
		t.Fatal(err)
	}
	if state.source.FilePath != csvB {
		t.Fatalf("source file=%q want B", state.source.FilePath)
	}
	if state.parsedFile != csvB {
		t.Fatalf("parsedFile=%q want B", state.parsedFile)
	}
	if state.data == nil || len(state.data.Issues) != 1 || state.data.Issues[0].Key != "ENG-2" {
		t.Fatalf("expected re-parsed B, got %+v", state.data)
	}
}

func TestRunWizardPhasesDryRunNonInteractive(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)
	client := mustPulseClient(t, server.URL, "token", "workspace-1")
	me, err := client.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sess := &session{client: client, apiURL: server.URL, workspaceID: "workspace-1", me: me}
	opts := &Options{
		Importer: "jira-csv", File: csvPath, Team: "team-1",
		JiraURL: "https://acme.atlassian.net", DryRun: true, NoPrompt: true, Concurrency: 1,
	}
	var stdout, stderr bytes.Buffer
	state, plan, err := runWizardPhases(
		context.Background(), &stdout, &stderr, sess, opts,
		jiracsv.EpicModeProject, nonInteractive{}, nil, runner.AssigneeMapped,
	)
	if !errors.Is(err, errDryRunDone) {
		t.Fatalf("err=%v", err)
	}
	if state == nil || plan == nil || !plan.Valid() {
		t.Fatalf("state/plan invalid")
	}
	if !strings.Contains(stdout.String(), "Dry run OK") {
		t.Fatalf("stdout missing dry-run markers:\n%s", stdout.String())
	}
	if len(server.Issues) != 0 || len(server.Projects) != 0 {
		t.Fatalf("dry-run wrote to API: issues=%d projects=%d", len(server.Issues), len(server.Projects))
	}
}

func TestRunWizardPhasesBackRevisitsSource(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)
	client := mustPulseClient(t, server.URL, "token", "workspace-1")
	me, err := client.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sess := &session{client: client, apiURL: server.URL, workspaceID: "workspace-1", me: me}

	// Source (file+cloud+url) → Destination team Select aborts (Back) →
	// Source again → Destination team + skip project + assignee none → Review proceed.
	p := &wizardScriptPrompter{
		files:    []string{csvPath, csvPath},
		inputs:   []string{"https://acme.atlassian.net", "https://acme.atlassian.net"},
		confirms: []bool{true, true, true}, // cloud, cloud (revisit), proceed
		selects: []selectResult{
			{err: ErrCanceled},
			{value: "team-1"},
			{value: string(runner.AssigneeNone)},
		},
	}
	opts := &Options{Concurrency: 1}
	var stdout, stderr bytes.Buffer
	state, plan, err := runWizardPhases(
		context.Background(), &stdout, &stderr, sess, opts,
		jiracsv.EpicModeProject, p, nil, "",
	)
	if err != nil {
		t.Fatalf("err=%v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if plan == nil || state == nil || state.assignee != runner.AssigneeNone {
		t.Fatalf("assignee=%q plan=%v", state.assignee, plan != nil)
	}
	if p.fileCalls < 2 {
		t.Fatalf("expected Source form twice after Back, fileCalls=%d", p.fileCalls)
	}
	if len(p.confirmTitles) == 0 || p.confirmTitles[len(p.confirmTitles)-1] != "Proceed with this import plan?" {
		t.Fatalf("confirm titles=%v", p.confirmTitles)
	}
}

func TestCompletionsRegistered(t *testing.T) {
	t.Parallel()
	cmd := NewRoot()
	cmd.SetArgs([]string{"__complete", "--importer", ""})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	_ = cmd.Execute()
	if !strings.Contains(out.String(), "jira-csv") {
		t.Fatalf("completion output missing jira-csv: %q", out.String())
	}
}

func TestAbsExistingRejectsMissing(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "nope.csv")
	if _, err := absExisting(missing); err == nil {
		t.Fatal("expected error")
	}
	path := writeCSV(t, "Summary,Issue key,Issue id,Issue Type,Status\nA,ENG-1,1,Story,To Do\n")
	got, err := absExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("not abs: %q", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFormAccessibleSkipsWidth(t *testing.T) {
	t.Parallel()
	form := huh.NewForm(huh.NewGroup(huh.NewInput().Title("x")))
	got := tui.ApplyForm(form, tui.Options{Accessible: true, Width: 80, Color: false})
	if got == nil {
		t.Fatal("nil form")
	}
}

type selectResult struct {
	value string
	err   error
}

type wizardScriptPrompter struct {
	files         []string
	inputs        []string
	confirms      []bool
	selects       []selectResult
	fileCalls     int
	confirmTitles []string
}

func (p *wizardScriptPrompter) Options() tui.Options { return tui.Options{Accessible: true} }

func (p *wizardScriptPrompter) Select(_ string, _ []huh.Option[string]) (string, error) {
	if len(p.selects) == 0 {
		return "", errors.New("no select left")
	}
	next := p.selects[0]
	p.selects = p.selects[1:]
	return next.value, next.err
}

func (p *wizardScriptPrompter) Input(_ string, _ string, validate func(string) error) (string, error) {
	if len(p.inputs) == 0 {
		return "", errors.New("no input left")
	}
	value := p.inputs[0]
	p.inputs = p.inputs[1:]
	if validate != nil {
		return value, validate(value)
	}
	return value, nil
}

func (p *wizardScriptPrompter) Secret(string, string, func(string) error) (string, error) {
	return "", errors.New("unexpected Secret")
}

func (p *wizardScriptPrompter) Confirm(title string, def bool) (bool, error) {
	p.confirmTitles = append(p.confirmTitles, title)
	if len(p.confirms) == 0 {
		return false, fmt.Errorf("no confirm left for %q (default would be %v)", title, def)
	}
	value := p.confirms[0]
	p.confirms = p.confirms[1:]
	return value, nil
}

func (p *wizardScriptPrompter) File(_ string, _ string, validate func(string) error) (string, error) {
	p.fileCalls++
	if len(p.files) == 0 {
		return "", errors.New("no file left")
	}
	value := p.files[0]
	p.files = p.files[1:]
	if validate != nil {
		return value, validate(value)
	}
	return value, nil
}

type cancelPrompter struct{}

func (cancelPrompter) Select(string, []huh.Option[string]) (string, error) {
	return "", ErrCanceled
}
func (cancelPrompter) Input(string, string, func(string) error) (string, error) {
	return "", ErrCanceled
}
func (cancelPrompter) Secret(string, string, func(string) error) (string, error) {
	return "", ErrCanceled
}
func (cancelPrompter) Confirm(string, bool) (bool, error) { return false, ErrCanceled }
func (cancelPrompter) File(string, string, func(string) error) (string, error) {
	return "", ErrCanceled
}
func (cancelPrompter) Options() tui.Options { return tui.Options{} }
