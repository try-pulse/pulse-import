package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/try-pulse/pulse-import/internal/auth"
	"github.com/try-pulse/pulse-import/internal/runner"
)

func TestRootDryRunPerformsPreflightWithoutWrites(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	csvPath := writeCSV(t, "Summary,Issue key,Issue Type,Description\nA valid issue,ENG-1,Bug,h1. Details\n")
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": "me", "email": "me@example.com"}})
		case "/teams":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "team-1", "name": "Engineering"}}})
		case "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case "/teams/team-1/labels":
			if r.Method != http.MethodGet {
				writes.Add(1)
			}
			if r.URL.Query().Get("entity_type") != "issue" {
				t.Errorf("query=%s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		default:
			writes.Add(1)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	command := NewRootWithDependencies(testDependencies(&stdout, &stderr))
	command.SetArgs([]string{
		"--yes", "--dry-run", "--api-url", server.URL,
		"--workspace", "workspace-1", "--team", "team-1",
		"--importer", "jira-csv", "--file", csvPath,
		"--jira-url", "https://acme.atlassian.net",
	})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 0 {
		t.Fatalf("dry run performed %d writes", writes.Load())
	}
	if !strings.Contains(stdout.String(), "Dry-run plan") || !strings.Contains(stdout.String(), "no Pulse changes were made") {
		t.Fatalf("stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

func TestRootWriteFlowUsesStateAndReportsResult(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	csvPath := writeCSV(t, "Summary,Issue key,Issue Type,Description\nA valid issue,ENG-1,Bug,h1. Details\n")
	statePath := filepath.Join(t.TempDir(), "state.jsonl")
	var issueCreates, uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": "me", "email": "me@example.com"}})
		case r.URL.Path == "/teams":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "team-1", "name": "Engineering"}}})
		case r.URL.Path == "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case r.URL.Path == "/teams/team-1/labels" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case r.URL.Path == "/teams/team-1/labels" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "label-1", "name": "Type: Bug", "entity_type": "issue"})
		case r.URL.Path == "/issues" && r.Method == http.MethodPost:
			issueCreates.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "issue-1", "title": "A valid issue", "team_id": "team-1"})
		case r.URL.Path == "/content/documents/upload":
			uploads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "doc-1", "title": "A valid issue"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	args := []string{
		"--yes", "--api-url", server.URL,
		"--workspace", "workspace-1", "--team", "team-1",
		"--importer", "jira-csv", "--file", csvPath,
		"--jira-url", "https://acme.atlassian.net", "--state-file", statePath,
	}
	command := NewRootWithDependencies(testDependencies(&stdout, &stderr))
	command.SetArgs(args)
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if issueCreates.Load() != 1 || uploads.Load() != 1 {
		t.Fatalf("creates=%d uploads=%d", issueCreates.Load(), uploads.Load())
	}
	if !strings.Contains(stdout.String(), "1 issues created") || !strings.Contains(stdout.String(), statePath) {
		t.Fatalf("stdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	command = NewRootWithDependencies(testDependencies(&stdout, &stderr))
	command.SetArgs(args)
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if issueCreates.Load() != 1 || uploads.Load() != 1 || !strings.Contains(stdout.String(), "1 resumed/skipped") {
		t.Fatalf("creates=%d uploads=%d stdout=%s", issueCreates.Load(), uploads.Load(), stdout.String())
	}
}

func testDependencies(stdout, stderr *bytes.Buffer) Dependencies {
	return Dependencies{
		In: strings.NewReader(""), Out: stdout, Err: stderr,
		LoadConfig: func() (*auth.Config, error) { return &auth.Config{}, nil },
		SaveConfig: func(*auth.Config) error { return nil },
	}
}

func writeCSV(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jira.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRootHelpersAndDefaults(t *testing.T) {
	t.Parallel()
	if command := NewRoot(); command.Use != "pulse-import" || command.Version == "" {
		t.Fatalf("command=%+v", command)
	}
	if command := NewRootWithDependencies(Dependencies{}); command.InOrStdin() == nil {
		t.Fatal("default dependencies were not filled")
	}
	if got := diagnosticPrefix(runner.Diagnostic{Key: "ENG-1", Row: 2}); got != "[ENG-1, row 2] " {
		t.Fatalf("prefix=%q", got)
	}
	if got := diagnosticPrefix(runner.Diagnostic{}); got != "" {
		t.Fatalf("prefix=%q", got)
	}
	var out, errOut bytes.Buffer
	printResult(&out, &errOut, &runner.Result{
		CreatedIssues: 1, Warnings: []string{"warn"}, Errors: []string{"failure"},
	}, "state.jsonl")
	if !strings.Contains(out.String(), "1 issues created") ||
		!strings.Contains(errOut.String(), "warning: warn") ||
		!strings.Contains(errOut.String(), "error: failure") {
		t.Fatalf("out=%q err=%q", out.String(), errOut.String())
	}
	printResult(&out, &errOut, nil, "")
}

func TestPrintPlanBoundsDiagnostics(t *testing.T) {
	t.Parallel()
	plan := &runner.Plan{
		Options: runner.Options{
			WorkspaceID: "workspace-1",
			TeamID:      "team-1",
			ProjectID:   "project-1",
		},
		Items:  []runner.PreparedItem{{}},
		Labels: []runner.LabelPlan{{Create: true}, {Create: false}},
	}
	for i := 0; i < 21; i++ {
		plan.Warnings = append(plan.Warnings, runner.Diagnostic{Message: "warning"})
		plan.Errors = append(plan.Errors, runner.Diagnostic{Message: "error"})
	}
	var out bytes.Buffer
	printPlan(&out, plan, false)
	got := out.String()
	if !strings.Contains(got, "Import plan") ||
		!strings.Contains(got, "project=project-1") ||
		!strings.Contains(got, "1 create / 1 reuse") ||
		!strings.Contains(got, "… 1 more warning(s)") ||
		!strings.Contains(got, "… 1 more error(s)") {
		t.Fatalf("output=%s", got)
	}
}

func TestRootRejectsMissingTokenAndInvalidURLWithoutPrintingErrors(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	command := NewRootWithDependencies(testDependencies(&stdout, &stderr))
	command.SetArgs([]string{"--yes", "--api-url", "ftp://example.com"})
	if err := command.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected invalid URL error")
	}
	if strings.Contains(stderr.String(), "Error:") {
		t.Fatalf("command printed its own final error: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	command = NewRootWithDependencies(testDependencies(&stdout, &stderr))
	command.SetArgs([]string{"--yes"})
	if err := command.ExecuteContext(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "PULSE_ACCESS_TOKEN") {
		t.Fatalf("err=%v", err)
	}
}
