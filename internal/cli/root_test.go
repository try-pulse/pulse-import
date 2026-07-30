package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/try-pulse/pulse-import/internal/auth"
	"github.com/try-pulse/pulse-import/internal/runner"
)

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

type runResult struct {
	stdout string
	stderr string
	err    error
}

func runCLI(t *testing.T, args ...string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := NewRootWithDependencies(testDependencies(&stdout, &stderr))
	command.SetArgs(args)
	err := command.ExecuteContext(context.Background())
	return runResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// The CSV below is the shape a real Jira export has: repeated Comment and
// Labels columns, an epic, a story under it and a sub-task under the story.
const endToEndCSV = "Summary,Issue key,Issue id,Parent,Issue Type,Status,Priority," +
	"Assignee,Description,Labels,Labels,Comment,Comment,Due Date\n" +
	"Checkout revamp,ENG-1,10001,,Epic,In Progress,High,,h2. Goal\\nShip it,,,,,\n" +
	"Speed up payment,ENG-2,10002,ENG-1,Story,In Progress,Highest,Jane Doe," +
	"The step is slow,performance,checkout,03/Jan/25 9:00 AM;jane;Looks like a DB issue," +
	"04/Jan/25 9:00 AM;john;Agreed,28/Feb/25\n" +
	"Add metric,ENG-3,10003,ENG-2,Sub-task,Done,Medium,,Emit a timer,,,,,\n"

func endToEndArgs(server *fakeServer, csvPath, statePath string, extra ...string) []string {
	args := []string{
		"--yes", "--api-url", server.URL,
		"--workspace", "workspace-1", "--team", "team-1",
		"--importer", "jira-csv", "--file", csvPath,
		"--jira-url", "https://acme.atlassian.net",
		"--concurrency", "1",
	}
	if statePath != "" {
		args = append(args, "--state-file", statePath)
	}
	return append(args, extra...)
}

func TestImportEndToEnd(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)
	statePath := filepath.Join(t.TempDir(), "state.jsonl")

	got := runCLI(t, endToEndArgs(server, csvPath, statePath)...)
	if got.err != nil {
		t.Fatalf("import failed: %v\nstdout:\n%s\nstderr:\n%s", got.err, got.stdout, got.stderr)
	}

	if len(server.Projects) != 1 {
		t.Errorf("projects = %d, want 1 (the epic)", len(server.Projects))
	}
	if len(server.Issues) != 2 {
		t.Errorf("issues = %d, want 2 (the story and the sub-task)", len(server.Issues))
	}

	var story, sub map[string]any
	for _, issue := range server.Issues {
		switch issue["title"] {
		case "Speed up payment":
			story = issue
		case "Add metric":
			sub = issue
		}
	}
	if story == nil || sub == nil {
		t.Fatalf("issues = %+v", server.Issues)
	}
	if story["project_id"] == nil {
		t.Error("the story was not filed into the epic's project")
	}
	if story["assignee_id"] != "user-jane" {
		t.Errorf("assignee = %v, want user-jane", story["assignee_id"])
	}
	if sub["parent_id"] != story["id"] {
		t.Errorf("sub-task parent = %v, want %v", sub["parent_id"], story["id"])
	}
	if got := server.Comments[story["id"].(string)]; len(got) != 2 {
		t.Errorf("comments = %v, want 2", got)
	}
	// Every entity with a body gets a Main Doc: the epic project and both issues.
	if len(server.MainDocs) != 3 {
		t.Errorf("main docs = %d, want 3", len(server.MainDocs))
	}
	if !strings.Contains(got.stdout, "Created:") || !strings.Contains(got.stdout, statePath) {
		t.Errorf("stdout:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "rollback --state-file") {
		t.Error("the result should tell the user how to undo the import")
	}

	// Re-running the same import must resume, creating nothing new.
	issuesBefore, projectsBefore := len(server.Issues), len(server.Projects)
	again := runCLI(t, endToEndArgs(server, csvPath, statePath)...)
	if again.err != nil {
		t.Fatalf("re-run failed: %v\n%s", again.err, again.stdout)
	}
	if len(server.Issues) != issuesBefore || len(server.Projects) != projectsBefore {
		t.Fatalf("re-run created new entities: issues %d→%d projects %d→%d",
			issuesBefore, len(server.Issues), projectsBefore, len(server.Projects))
	}
	if !strings.Contains(again.stdout, "Resumed/skipped: 3") {
		t.Errorf("re-run stdout:\n%s", again.stdout)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)

	got := runCLI(t, endToEndArgs(server, csvPath, "", "--dry-run")...)
	if got.err != nil {
		t.Fatalf("dry run failed: %v\n%s\n%s", got.err, got.stdout, got.stderr)
	}
	if len(server.Issues) != 0 || len(server.Projects) != 0 || len(server.Labels) != 0 {
		t.Fatalf("dry run wrote: issues=%d projects=%d labels=%d",
			len(server.Issues), len(server.Projects), len(server.Labels))
	}
	for _, call := range []string{"POST /issues", "POST /projects", "POST /teams/team-1/labels", "POST /comments"} {
		if server.count(call) != 0 {
			t.Errorf("dry run performed %q %d time(s)", call, server.count(call))
		}
	}
	if !strings.Contains(got.stdout, "Dry-run plan") ||
		!strings.Contains(got.stdout, "no Pulse changes were made") {
		t.Fatalf("stdout:\n%s", got.stdout)
	}
}

// The review step is what makes a wrong mapping catchable while it is still free.
func TestDryRunShowsTheReviewTables(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)

	got := runCLI(t, endToEndArgs(server, csvPath, "", "--dry-run")...)
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, want := range []string{
		"Status mapping (source → Pulse)",
		"in_progress  ← In Progress",
		"User mapping:",
		"Jane Doe",
		"Issues are created in ascending source-key order",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("review output is missing %q; got:\n%s", want, got.stdout)
		}
	}
}

func TestStatusFilterSkipsDoneIssues(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)

	got := runCLI(t, endToEndArgs(server, csvPath, "", "--skip-status", "done")...)
	if got.err != nil {
		t.Fatalf("%v\n%s", got.err, got.stdout)
	}
	for _, issue := range server.Issues {
		if issue["title"] == "Add metric" {
			t.Fatal("the Done sub-task should have been filtered out")
		}
	}
	if len(server.Issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(server.Issues))
	}
}

func TestUnknownStatusFilterIsRejectedBeforeAuthenticating(t *testing.T) {
	t.Parallel()
	got := runCLI(t, "--yes", "--skip-status", "nonsense")
	if got.err == nil || !strings.Contains(got.err.Error(), "is not a Pulse status") {
		t.Fatalf("err = %v", got.err)
	}
	// Nothing should have been attempted, so no token is required to fail.
	if strings.Contains(got.stdout, "Signed in") {
		t.Error("flag validation must happen before authentication")
	}
}

func TestConflictingStatusFiltersAreRejected(t *testing.T) {
	t.Parallel()
	got := runCLI(t, "--yes", "--skip-status", "done", "--only-status", "done")
	if got.err == nil || !strings.Contains(got.err.Error(), "both list") {
		t.Fatalf("err = %v", got.err)
	}
}

func TestSelfAssignAndAssigneeFlagConflict(t *testing.T) {
	t.Parallel()
	got := runCLI(t, "--yes", "--self-assign", "--assignee", "none")
	if got.err == nil || !strings.Contains(got.err.Error(), "conflicts with") {
		t.Fatalf("err = %v", got.err)
	}
}

func TestSkipCommentsAndSkipLabels(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)

	got := runCLI(t, endToEndArgs(server, csvPath, "",
		"--skip-comments", "--skip-labels", "--no-migrated-label")...)
	if got.err != nil {
		t.Fatalf("%v\n%s", got.err, got.stdout)
	}
	if server.count("POST /comments") != 0 {
		t.Errorf("comments were posted despite --skip-comments")
	}
	if server.count("POST /teams/team-1/labels") != 0 {
		t.Errorf("labels were created despite --skip-labels")
	}
}

func TestMigratedLabelIsCreatedByDefault(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)

	if got := runCLI(t, endToEndArgs(server, csvPath, "")...); got.err != nil {
		t.Fatalf("%v\n%s", got.err, got.stdout)
	}
	var found bool
	for _, label := range server.Labels {
		if label["name"] == "Migrated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("labels = %+v; the Migrated marker is how an import can be found later", server.Labels)
	}
}

func TestRollbackDeletesWhatTheImportCreated(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)
	statePath := filepath.Join(t.TempDir(), "state.jsonl")

	if got := runCLI(t, endToEndArgs(server, csvPath, statePath)...); got.err != nil {
		t.Fatalf("import: %v\n%s", got.err, got.stdout)
	}
	if len(server.Issues) == 0 || len(server.Projects) == 0 {
		t.Fatal("nothing was imported")
	}
	idByTitle := map[string]string{}
	for id, issue := range server.Issues {
		idByTitle[issue["title"].(string)] = id
	}

	got := runCLI(t, "rollback", "--yes", "--api-url", server.URL,
		"--workspace", "workspace-1", "--state-file", statePath)
	if got.err != nil {
		t.Fatalf("rollback: %v\n%s\n%s", got.err, got.stdout, got.stderr)
	}
	if len(server.Issues) != 0 {
		t.Errorf("issues left behind: %+v", server.Issues)
	}
	if len(server.Projects) != 0 {
		t.Errorf("projects left behind: %+v", server.Projects)
	}
	if len(server.Deleted) != 6 {
		t.Errorf("deleted %d entities (%v), want 3 docs + 2 issues + 1 project",
			len(server.Deleted), server.Deleted)
	}

	// Pulse does not cascade a delete to sub-issues, so the child has to be
	// removed before its parent.
	subIndex := indexOf(server.Deleted, "issue:"+idByTitle["Add metric"])
	storyIndex := indexOf(server.Deleted, "issue:"+idByTitle["Speed up payment"])
	if subIndex == -1 || storyIndex == -1 || subIndex > storyIndex {
		t.Errorf("delete order = %v; the sub-issue (%s) must be deleted before its parent (%s)",
			server.Deleted, idByTitle["Add metric"], idByTitle["Speed up payment"])
	}
}

func TestRollbackKeepDocuments(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)
	statePath := filepath.Join(t.TempDir(), "state.jsonl")

	if got := runCLI(t, endToEndArgs(server, csvPath, statePath)...); got.err != nil {
		t.Fatalf("import: %v", got.err)
	}
	got := runCLI(t, "rollback", "--yes", "--keep-documents", "--api-url", server.URL,
		"--workspace", "workspace-1", "--state-file", statePath)
	if got.err != nil {
		t.Fatalf("rollback: %v\n%s", got.err, got.stderr)
	}
	for _, entry := range server.Deleted {
		if strings.HasPrefix(entry, "document:") {
			t.Fatalf("--keep-documents still deleted %s", entry)
		}
	}
}

func TestRollbackRequiresStateFile(t *testing.T) {
	t.Parallel()
	got := runCLI(t, "rollback", "--yes")
	if got.err == nil || !strings.Contains(got.err.Error(), "--state-file is required") {
		t.Fatalf("err = %v", got.err)
	}

	got = runCLI(t, "rollback", "--yes", "--state-file", "/no/such/state.jsonl")
	if got.err == nil || !strings.Contains(got.err.Error(), "state file not found") {
		t.Fatalf("err = %v", got.err)
	}
}

func TestRollbackRefusesAWorkspaceMismatch(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)
	statePath := filepath.Join(t.TempDir(), "state.jsonl")
	if got := runCLI(t, endToEndArgs(server, csvPath, statePath)...); got.err != nil {
		t.Fatalf("import: %v", got.err)
	}

	got := runCLI(t, "rollback", "--yes", "--api-url", server.URL,
		"--workspace", "workspace-other", "--state-file", statePath)
	if got.err == nil || !strings.Contains(got.err.Error(), "targets workspace") {
		t.Fatalf("err = %v", got.err)
	}
	if len(server.Deleted) != 0 {
		t.Fatalf("a mismatched rollback deleted %v", server.Deleted)
	}
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
	if !strings.Contains(out.String(), "Created: 1 issue(s)") ||
		!strings.Contains(errOut.String(), "warning: warn") ||
		!strings.Contains(errOut.String(), "error: failure") {
		t.Fatalf("out=%q err=%q", out.String(), errOut.String())
	}
	printResult(&out, &errOut, nil, "")

	if command, _, err := NewRoot().Find([]string{"rollback"}); err != nil || command.Name() != "rollback" {
		t.Fatalf("rollback subcommand missing: %v", err)
	}
}

func TestPrintPlanBoundsDiagnostics(t *testing.T) {
	t.Parallel()
	plan := &runner.Plan{
		Options: runner.Options{
			WorkspaceID: "workspace-1", TeamID: "team-1", ProjectID: "project-1",
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
	for _, want := range []string{
		"Import plan", "project=project-1", "1 create · 1 reuse",
		"… 1 more warning(s)", "… 1 more error(s)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPrintPlanWarnsWhenTheTeamAlreadyHasIssues(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	printPlan(&out, &runner.Plan{
		Options: runner.Options{TeamID: "team-1"}, TeamIssueCount: 12,
	}, false)
	if !strings.Contains(out.String(), "already has 12 issue(s)") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestRootRejectsMissingTokenAndInvalidURLWithoutPrintingErrors(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "")
	got := runCLI(t, "--yes", "--api-url", "ftp://example.com")
	if got.err == nil {
		t.Fatal("expected invalid URL error")
	}
	if strings.Contains(got.stderr, "Error:") {
		t.Fatalf("command printed its own final error: %q", got.stderr)
	}

	got = runCLI(t, "--yes")
	if got.err == nil || !strings.Contains(got.err.Error(), "PULSE_ACCESS_TOKEN") {
		t.Fatalf("err=%v", got.err)
	}
}

func indexOf(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

// The single most damaging bug this importer had: a Main Doc uploaded with any
// content type outside {text/plain, application/json} is rendered by Pulse as an
// opaque file to download, not as the issue's description.
func TestMainDocsAreUploadedAsEditableDocuments(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, endToEndCSV)

	if got := runCLI(t, endToEndArgs(server, csvPath, "")...); got.err != nil {
		t.Fatalf("%v\n%s", got.err, got.stdout)
	}
	if len(server.UploadContentTypes) == 0 {
		t.Fatal("nothing was uploaded")
	}
	for index, contentType := range server.UploadContentTypes {
		if contentType != "text/plain" {
			t.Errorf("upload %d had Content-Type %q; Pulse only opens text/plain or "+
				"application/json in its editor", index, contentType)
		}
	}
	for _, name := range server.UploadFilenames {
		if !strings.HasSuffix(name, ".txt") {
			t.Errorf("upload filename %q should match what the Pulse clients send (.txt)", name)
		}
	}
}

// Pulse validates title length in bytes, so a Persian summary that is well under
// 200 characters can still be over 200 bytes.
func TestNonLatinTitlesAreAcceptedByTheAPI(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	long := strings.Repeat("م", 150) // 150 runes, 300 bytes
	csvPath := writeCSV(t, "Summary,Issue key\n"+long+",ENG-1\n")

	got := runCLI(t, endToEndArgs(server, csvPath, "")...)
	if got.err != nil {
		t.Fatalf("%v\n%s\n%s", got.err, got.stdout, got.stderr)
	}
	if len(server.IssueTitles) != 1 {
		t.Fatalf("issues created = %d", len(server.IssueTitles))
	}
	if got := len(server.IssueTitles[0]); got > 200 {
		t.Fatalf("title sent was %d bytes; Pulse rejects anything over 200", got)
	}
	if !utf8.ValidString(server.IssueTitles[0]) {
		t.Fatal("truncation split a rune")
	}
}

func TestMapUserFlagIsHonoured(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, "Summary,Issue key,Assignee\nAssigned work,ENG-1,Some Stranger\n")

	got := runCLI(t, endToEndArgs(server, csvPath, "",
		"--map-user", "Some Stranger=jane@acme.com")...)
	if got.err != nil {
		t.Fatalf("%v\n%s", got.err, got.stdout)
	}
	for _, issue := range server.Issues {
		if issue["assignee_id"] != "user-jane" {
			t.Fatalf("assignee = %v, want user-jane", issue["assignee_id"])
		}
	}
}

func TestMapUserToANonMemberFailsPreflight(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	csvPath := writeCSV(t, "Summary,Issue key,Assignee\nAssigned work,ENG-1,Some Stranger\n")

	got := runCLI(t, endToEndArgs(server, csvPath, "",
		"--map-user", "Some Stranger=nobody@example.com")...)
	if got.err == nil {
		t.Fatal("expected a preflight failure")
	}
	if !strings.Contains(got.stdout, "does not resolve to a member") {
		t.Fatalf("stdout:\n%s", got.stdout)
	}
	if len(server.Issues) != 0 {
		t.Fatal("a failed preflight must not write anything")
	}
}

// Pulse's label uniqueness index spans archived labels, so a name held by an
// archived label used to abort the import with a 409.
func TestArchivedLabelDoesNotBreakTheImport(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "token")
	server := newFakeServer(t)
	server.Labels = []map[string]any{{
		"id": "label-archived", "name": "Migrated", "entity_type": "issue",
		"team_id": "team-1", "archived": true,
	}}
	csvPath := writeCSV(t, "Summary,Issue key\nLabelled work,ENG-1\n")

	got := runCLI(t, endToEndArgs(server, csvPath, "")...)
	if got.err != nil {
		t.Fatalf("%v\n%s\n%s", got.err, got.stdout, got.stderr)
	}
	if len(server.Issues) != 1 {
		t.Fatalf("issues = %d", len(server.Issues))
	}
	for _, label := range server.Labels {
		if label["name"] == "Migrated" && label["archived"] == true {
			t.Fatal("the archived label should have been unarchived and reused")
		}
	}
}
