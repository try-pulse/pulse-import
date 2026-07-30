package runner_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

func execute(
	t *testing.T,
	api runner.API,
	plan *runner.Plan,
	stateFile string,
	mutate ...func(*runner.ExecuteOptions),
) (*runner.Result, error) {
	t.Helper()
	opts := runner.ExecuteOptions{StateFile: stateFile}
	for _, apply := range mutate {
		apply(&opts)
	}
	return runner.New(api).Execute(context.Background(), plan, opts)
}

func TestExecuteCreatesTheWholeGraph(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID, member("user-1", "Jane", "Doe", "jane@acme.com"))

	story := issue("ENG-2", "Speed up payment")
	story.EpicKey = "ENG-1"
	story.AssigneeID = "Jane Doe"
	story.BodyMarkdown = "# Body\n\ntext"
	story.Comments = []importers.Comment{{Body: "first"}, {Body: "second"}}
	story.Relations = []importers.Relation{{Kind: importers.RelationBlocks, TargetKey: "ENG-4"}}

	sub := issue("ENG-3", "Add metric")
	sub.ParentKey = "ENG-2"

	blocked := issue("ENG-4", "Drop gateway")

	data := source(story, sub, blocked)
	data.Users["jane doe"] = importers.User{Name: "Jane Doe", Rows: 1}
	data.Projects = []importers.Project{{
		Key: "ENG-1", Title: "Checkout revamp", RowHash: "hash-epic", SourceRow: 2,
		BodyMarkdown: "epic body",
	}}
	data.Labels["performance"] = importers.Label{Name: "performance", Kind: importers.LabelKindJira}
	story.Labels = []string{"performance"}
	data.Issues[0].Labels = []string{"performance"}

	plan := prepare(t, api, data, func(o *runner.Options) {
		o.Assignee = runner.AssigneeMapped
		o.AddMigratedLabel = true
	})
	if !plan.Valid() {
		t.Fatalf("errors: %s", planErrors(plan))
	}

	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors: %v)", err, result.Errors)
	}

	if result.CreatedProjects != 1 {
		t.Errorf("projects = %d", result.CreatedProjects)
	}
	if result.CreatedIssues != 3 {
		t.Errorf("issues = %d", result.CreatedIssues)
	}
	if result.CreatedComments != 2 {
		t.Errorf("comments = %d", result.CreatedComments)
	}
	if result.LinkedIssues != 1 {
		t.Errorf("links = %d", result.LinkedIssues)
	}
	if result.CreatedMainDocs != 2 {
		t.Errorf("main docs = %d, want 2 (the epic project and the story)", result.CreatedMainDocs)
	}

	storyIssue := api.issueByTitle("Speed up payment")
	if storyIssue == nil {
		t.Fatal("story not created")
	}
	if storyIssue.ProjectID == nil {
		t.Error("story was not filed into the epic's project")
	}
	if storyIssue.AssigneeID == nil || *storyIssue.AssigneeID != "user-1" {
		t.Errorf("assignee = %v", storyIssue.AssigneeID)
	}
	if len(storyIssue.BlocksIDs) != 1 {
		t.Errorf("blocks = %v", storyIssue.BlocksIDs)
	}

	subIssue := api.issueByTitle("Add metric")
	if subIssue == nil || subIssue.ParentID == nil || *subIssue.ParentID != storyIssue.ID {
		t.Errorf("sub-issue parent = %+v", subIssue)
	}
	if got := api.commentsFor(storyIssue.ID); len(got) != 2 {
		t.Errorf("comments = %v", got)
	}
}

// The documented way to pick up Pulse users who joined after the first attempt
// is to re-run the import. That must resume, not refuse.
func TestReRunAfterUsersJoinResumesInsteadOfFailing(t *testing.T) {
	t.Parallel()
	state := statePath(t)

	build := func(api runner.API, assigneeKnown bool) *runner.Plan {
		data := source(issue("ENG-1", "Assign me"))
		data.Issues[0].AssigneeID = "Jane Doe"
		data.Users["jane doe"] = importers.User{Name: "Jane Doe", Rows: 1}
		return prepare(t, api, data, func(o *runner.Options) { o.Assignee = runner.AssigneeMapped })
	}

	before := newFakePulse()
	firstPlan := build(before, false)
	if _, err := execute(t, before, firstPlan, state); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if before.callCount("CreateIssue") != 1 {
		t.Fatalf("first run created %d issue(s)", before.callCount("CreateIssue"))
	}

	// Jane has now joined the target team, so the plan's assignee resolution
	// differs from the first run.
	after := newFakePulse().withMembers(teamID, member("user-jane", "Jane", "Doe", "jane@acme.com"))
	secondPlan := build(after, true)
	if firstPlan.Hash == secondPlan.Hash {
		t.Fatal("expected the plans to differ; the test would not prove anything otherwise")
	}
	result, err := execute(t, after, secondPlan, state)
	if err != nil {
		t.Fatalf("re-run must resume, got: %v", err)
	}
	if after.callCount("CreateIssue") != 0 {
		t.Fatalf("re-run created %d issue(s); completed work must be skipped",
			after.callCount("CreateIssue"))
	}
	if result.SkippedIssues != 1 {
		t.Fatalf("skipped = %d, want 1", result.SkippedIssues)
	}
}

func TestResumeSkipsCompletedWorkAndFinishesTheRest(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()

	build := func() *runner.Plan {
		first := issue("ENG-1", "First")
		first.BodyMarkdown = "body one"
		second := issue("ENG-2", "Second")
		second.BodyMarkdown = "body two"
		second.Comments = []importers.Comment{{Body: "note"}}
		return prepare(t, api, source(first, second))
	}

	// Fail the second issue's Main Doc upload so the first run stops part-way.
	api.uploadHook = func(_, entityID string) (*pulseapi.Document, error) {
		if issue := api.issues[entityID]; issue != nil && issue.Title == "Second" {
			return nil, apiError(http.StatusBadRequest, "INVALID_REQUEST", "nope")
		}
		return nil, nil
	}
	if _, err := execute(t, api, build(), state, func(o *runner.ExecuteOptions) {
		o.ContinueOnError = true
	}); err == nil {
		t.Fatal("expected a partial failure")
	}
	if api.callCount("CreateIssue") != 2 {
		t.Fatalf("created %d issues", api.callCount("CreateIssue"))
	}

	api.uploadHook = nil
	creates := api.callCount("CreateIssue")
	result, err := execute(t, api, build(), state)
	if err != nil {
		t.Fatalf("resume: %v (errors %v)", err, result.Errors)
	}
	if api.callCount("CreateIssue") != creates {
		t.Fatalf("resume re-created issues: %d → %d", creates, api.callCount("CreateIssue"))
	}
	if result.CreatedMainDocs != 1 {
		t.Errorf("resume uploaded %d main doc(s), want the one that failed", result.CreatedMainDocs)
	}
	if result.CreatedComments != 1 {
		t.Errorf("resume posted %d comment(s), want 1", result.CreatedComments)
	}
}

// A create whose response never arrived may or may not have happened. Retrying it
// blindly is how an importer produces duplicates, so it must stop and ask.
func TestAmbiguousCreateStopsAndRequiresAnExplicitDecision(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()
	api.createIssueHook = func(pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		return nil, errors.New("connection reset by peer")
	}

	plan := prepare(t, api, source(issue("ENG-1", "Maybe created")))
	_, err := execute(t, api, plan, state)
	var unknown *runner.UnknownOutcomeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want UnknownOutcomeError", err)
	}

	// A plain re-run must not guess.
	api.createIssueHook = nil
	if _, err := execute(t, api, plan, state); !errors.As(err, &unknown) {
		t.Fatalf("re-run err = %v; an unknown outcome must persist until resolved", err)
	}
	if api.callCount("CreateIssue") != 1 {
		t.Fatalf("CreateIssue called %d times; the retry must be blocked",
			api.callCount("CreateIssue"))
	}

	// --retry-unknown is the explicit opt-in.
	result, err := execute(t, api, plan, state, func(o *runner.ExecuteOptions) {
		o.RetryUnknown = map[string]bool{"eng-1": true}
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.CreatedIssues != 1 {
		t.Fatalf("created = %d", result.CreatedIssues)
	}
}

func TestAdoptResolvesAnUnknownCreate(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()
	api.createIssueHook = func(pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		return nil, errors.New("connection reset by peer")
	}
	plan := prepare(t, api, source(issue("ENG-1", "Maybe created")))
	if _, err := execute(t, api, plan, state); err == nil {
		t.Fatal("expected an unknown outcome")
	}

	// The user found the issue in Pulse and hands us its id.
	api.createIssueHook = nil
	existing := &pulseapi.Issue{ID: "issue-adopted", Title: "Maybe created", TeamID: teamID}
	api.issues[existing.ID] = existing

	result, err := execute(t, api, plan, state, func(o *runner.ExecuteOptions) {
		o.Adopt = map[string]string{"ENG-1": "issue-adopted"}
	})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if api.callCount("CreateIssue") != 1 {
		t.Fatalf("CreateIssue called %d times; adoption must not create anything",
			api.callCount("CreateIssue"))
	}
	if result.CreatedIssues != 0 || result.SkippedIssues != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAdoptRejectsAMismatchedIssue(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()
	api.createIssueHook = func(pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		return nil, errors.New("connection reset by peer")
	}
	plan := prepare(t, api, source(issue("ENG-1", "Real title")))
	if _, err := execute(t, api, plan, state); err == nil {
		t.Fatal("expected an unknown outcome")
	}
	api.createIssueHook = nil
	api.issues["issue-other"] = &pulseapi.Issue{ID: "issue-other", Title: "Different", TeamID: teamID}

	_, err := execute(t, api, plan, state, func(o *runner.ExecuteOptions) {
		o.Adopt = map[string]string{"ENG-1": "issue-other"}
	})
	if err == nil || !strings.Contains(err.Error(), "does not match planned title") {
		t.Fatalf("err = %v", err)
	}
}

// An ambiguous Main Doc upload can be settled by reading the issue back, so it
// must not escalate to a manual decision.
func TestAmbiguousMainDocUploadIsReconciled(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	first := true
	api.uploadHook = func(_, entityID string) (*pulseapi.Document, error) {
		if first {
			first = false
			// The write landed; only the response was lost.
			api.mainDocs[entityID] = "doc-landed"
			return nil, apiError(http.StatusBadGateway, "", "bad gateway")
		}
		return nil, nil
	}

	data := source(issue("ENG-1", "Has a doc"))
	data.Issues[0].BodyMarkdown = "body"
	plan := prepare(t, api, data)

	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors %v)", err, result.Errors)
	}
	if result.CreatedMainDocs != 1 || result.FailedMainDocs != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAmbiguousCommentIsReconciledFromTheCommentCount(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	failed := false
	api.commentHook = func(issueID, text string) (*pulseapi.Comment, error) {
		if !failed {
			failed = true
			// The comment landed; the response was lost.
			api.comments[issueID] = append(api.comments[issueID], text)
			return nil, apiError(http.StatusBadGateway, "", "bad gateway")
		}
		return nil, nil
	}

	data := source(issue("ENG-1", "Commented"))
	data.Issues[0].Comments = []importers.Comment{{Body: "one"}, {Body: "two"}}
	plan := prepare(t, api, data)

	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors %v)", err, result.Errors)
	}
	issue := api.issueByTitle("Commented")
	if got := api.commentsFor(issue.ID); len(got) != 2 {
		t.Fatalf("comments = %v; the lost-response comment must not be posted twice", got)
	}
}

// A child whose parent failed must fail too, so a re-run can create both
// correctly rather than silently flattening the hierarchy.
func TestChildFailsWhenItsParentDidNotLand(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.createIssueHook = func(req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		if req.Title == "Parent" {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE", "no")
		}
		return nil, nil
	}
	child := issue("ENG-2", "Child")
	child.ParentKey = "ENG-1"
	plan := prepare(t, api, source(issue("ENG-1", "Parent"), child))

	result, err := execute(t, api, plan, statePath(t), func(o *runner.ExecuteOptions) {
		o.ContinueOnError = true
	})
	if err == nil {
		t.Fatal("expected a partial failure")
	}
	if result.FailedIssues != 2 {
		t.Fatalf("failed = %d, want the parent and the child: %v", result.FailedIssues, result.Errors)
	}
	if !strings.Contains(strings.Join(result.Errors, " "), "cannot be imported as its sub-issue") {
		t.Fatalf("errors = %v", result.Errors)
	}
}

func TestFirstFailureStopsTheRunUnlessContinueOnError(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.createIssueHook = func(req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		if req.Title == "Bad" {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE", "no")
		}
		return nil, nil
	}
	plan := prepare(t, api, source(issue("ENG-1", "Bad"), issue("ENG-2", "Good")), func(o *runner.Options) {
		o.Concurrency = 1
	})
	_, err := execute(t, api, plan, statePath(t))
	var partial *runner.PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want PartialError", err)
	}
	if api.callCount("CreateIssue") != 1 {
		t.Fatalf("CreateIssue called %d times; the run should have stopped", api.callCount("CreateIssue"))
	}
}

// A 403 on label creation is a permissions problem with a concrete fix, not an
// opaque API error, and it must surface before any issue is written.
func TestForbiddenLabelCreateBecomesAnActionablePermissionError(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.createLabelHook = func(string) (*pulseapi.Label, error) {
		return nil, apiError(http.StatusForbidden, "INSUFFICIENT_PERMISSIONS",
			"You don't have permission to perform this action")
	}
	data := source(issue("ENG-1", "Labelled"))
	data.Labels["performance"] = importers.Label{Name: "performance", Kind: importers.LabelKindJira}
	data.Issues[0].Labels = []string{"performance"}

	plan := prepare(t, api, data)
	_, err := execute(t, api, plan, statePath(t))
	var permission *runner.PermissionError
	if !errors.As(err, &permission) {
		t.Fatalf("err = %v, want PermissionError", err)
	}
	if permission.Permission != "labels:create" || !strings.Contains(permission.Remedy, "--skip-labels") {
		t.Fatalf("permission error = %+v", permission)
	}
	if api.callCount("CreateIssue") != 0 {
		t.Fatal("labels are resolved before the first issue is created")
	}
}

// Pulse's label uniqueness index ignores the archived flag, so creating a label
// whose name is held by an archived one returns 409. That used to abort the whole
// import.
func TestArchivedLabelIsUnarchivedAndReused(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.labels = []pulseapi.Label{
		{ID: "label-archived", TeamID: teamID, Name: "performance", EntityType: "issue", Archived: true},
	}
	data := source(issue("ENG-1", "Labelled"))
	data.Labels["performance"] = importers.Label{Name: "performance", Kind: importers.LabelKindJira}
	data.Issues[0].Labels = []string{"performance"}

	plan := prepare(t, api, data)
	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors %v)", err, result.Errors)
	}
	if api.callCount("UnarchiveLabel") != 1 {
		t.Fatalf("UnarchiveLabel called %d times", api.callCount("UnarchiveLabel"))
	}
	if api.callCount("CreateLabel") != 0 {
		t.Fatal("the archived label must be reused, not recreated")
	}
	if result.CreatedIssues != 1 {
		t.Fatalf("result = %+v", result)
	}
}

// A label archived between preflight and execution still has to be recovered.
func TestLabelConflictDuringExecutionIsRecovered(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	data := source(issue("ENG-1", "Labelled"))
	data.Labels["performance"] = importers.Label{Name: "performance", Kind: importers.LabelKindJira}
	data.Issues[0].Labels = []string{"performance"}
	plan := prepare(t, api, data)

	// Someone archives a label with that name after preflight ran.
	api.labels = append(api.labels, pulseapi.Label{
		ID: "label-race", TeamID: teamID, Name: "performance", EntityType: "issue", Archived: true,
	})

	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors %v)", err, result.Errors)
	}
	if result.CreatedIssues != 1 {
		t.Fatalf("result = %+v", result)
	}
	issue := api.issueByTitle("Labelled")
	if issue == nil {
		t.Fatal("issue not created")
	}
}

func TestConcurrentExecutionCreatesEverythingExactlyOnce(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	const count = 40
	issues := make([]importers.Issue, 0, count)
	for i := 1; i <= count; i++ {
		current := issue(fmt.Sprintf("ENG-%d", i), fmt.Sprintf("Issue %d", i))
		current.BodyMarkdown = "body"
		current.Comments = []importers.Comment{{Body: "note"}}
		issues = append(issues, current)
	}
	plan := prepare(t, api, source(issues...), func(o *runner.Options) { o.Concurrency = 8 })

	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors %v)", err, result.Errors)
	}
	if result.CreatedIssues != count {
		t.Fatalf("created = %d, want %d", result.CreatedIssues, count)
	}
	if api.callCount("CreateIssue") != count {
		t.Fatalf("CreateIssue called %d times, want %d", api.callCount("CreateIssue"), count)
	}
	if result.CreatedComments != count {
		t.Fatalf("comments = %d, want %d", result.CreatedComments, count)
	}
}

// Waves are a correctness requirement: a sub-issue cannot be created before its
// parent, whatever the concurrency setting.
func TestParentsAreCreatedBeforeChildrenUnderConcurrency(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	var issues []importers.Issue
	for i := 1; i <= 10; i++ {
		issues = append(issues, issue(fmt.Sprintf("ENG-%d", i), fmt.Sprintf("Parent %d", i)))
	}
	for i := 1; i <= 10; i++ {
		child := issue(fmt.Sprintf("ENG-1%02d", i), fmt.Sprintf("Child %d", i))
		child.ParentKey = fmt.Sprintf("ENG-%d", i)
		issues = append(issues, child)
	}
	plan := prepare(t, api, source(issues...), func(o *runner.Options) { o.Concurrency = 8 })

	result, err := execute(t, api, plan, statePath(t))
	if err != nil {
		t.Fatalf("execute: %v (errors %v)", err, result.Errors)
	}
	if result.CreatedIssues != 20 {
		t.Fatalf("created = %d", result.CreatedIssues)
	}
	for i := 1; i <= 10; i++ {
		child := api.issueByTitle(fmt.Sprintf("Child %d", i))
		parent := api.issueByTitle(fmt.Sprintf("Parent %d", i))
		if child == nil || parent == nil {
			t.Fatalf("missing pair %d", i)
		}
		if child.ParentID == nil || *child.ParentID != parent.ID {
			t.Fatalf("child %d parent = %v, want %s", i, child.ParentID, parent.ID)
		}
	}
}

func TestIssuesAreCreatedInSourceKeyOrder(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	// Deliberately out of order, and with numbers that sort differently as text.
	plan := prepare(t, api, source(
		issue("ENG-10", "Tenth"),
		issue("ENG-2", "Second"),
		issue("ENG-1", "First"),
	), func(o *runner.Options) { o.Concurrency = 1 })

	if _, err := execute(t, api, plan, statePath(t)); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"First", "Second", "Tenth"}
	if got := api.createOrder(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("creation order = %v, want %v", got, want)
	}
}

func TestExecuteRefusesAnInvalidPlan(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	data := source(issue("ENG-1", "X"))
	data.Diagnostics = []importers.Diagnostic{
		{Level: importers.DiagnosticError, Row: 2, Message: "duplicate Issue key"},
	}
	plan := prepare(t, api, data)
	if _, err := execute(t, api, plan, statePath(t)); err == nil {
		t.Fatal("expected a refusal")
	}
	if api.callCount("CreateIssue") != 0 {
		t.Fatal("an invalid plan must not write anything")
	}
}

func TestStateFileForADifferentTargetIsRejectedWithGuidance(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()
	plan := prepare(t, api, source(issue("ENG-1", "One")))
	if _, err := execute(t, api, plan, state); err != nil {
		t.Fatalf("first run: %v", err)
	}

	other := prepare(t, api, source(issue("ENG-1", "One")), func(o *runner.Options) {
		o.TeamID = "team-other"
		o.TeamPath = []string{"team-other"}
	})
	_, err := execute(t, api, other, state)
	if err == nil || !strings.Contains(err.Error(), "belongs to a different import") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "--state-file") {
		t.Fatalf("the error must say how to proceed: %v", err)
	}
}

func TestJournalRecordsEverythingRollbackNeeds(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()

	story := issue("ENG-2", "Story")
	story.EpicKey = "ENG-1"
	story.BodyMarkdown = "body"
	data := source(story)
	data.Projects = []importers.Project{{
		Key: "ENG-1", Title: "Epic", RowHash: "hash-epic", SourceRow: 2, BodyMarkdown: "epic body",
	}}
	plan := prepare(t, api, data)
	if _, err := execute(t, api, plan, state); err != nil {
		t.Fatalf("execute: %v", err)
	}

	journal, err := importstate.Open(state, importstate.Identity{
		Importer: "jira-csv", SourceURL: "https://acme.atlassian.net",
		SourceFingerprint: "fingerprint", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace-1", TeamID: teamID,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	items := journal.Items()
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	// The project was created first, so it must be recorded first: rollback walks
	// this list backwards.
	if items[0].Kind != importstate.KindProject || items[0].ProjectID == "" {
		t.Fatalf("first item = %+v", items[0])
	}
	if items[0].MainDocID == "" {
		t.Error("the project's Main Doc id must be recorded so rollback can delete it")
	}
	if items[1].Kind != importstate.KindIssue || items[1].IssueID == "" || items[1].MainDocID == "" {
		t.Fatalf("second item = %+v", items[1])
	}
	for _, item := range items {
		if !item.Complete() {
			t.Errorf("%s did not reach a terminal state: %s", item.Key, item.Status)
		}
	}
}

// A failed phase must leave the item at the phase it reached. If execution
// carried on to the next phase and marked the item complete, the failed work
// would be lost: a resume would consider the item done.
func TestAFailedPhaseDoesNotMarkTheItemComplete(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()
	api.uploadHook = func(string, string) (*pulseapi.Document, error) {
		return nil, apiError(http.StatusBadRequest, "INVALID_REQUEST", "refused")
	}

	data := source(issue("ENG-1", "Doc fails"))
	data.Issues[0].BodyMarkdown = "body"
	data.Issues[0].Comments = []importers.Comment{{Body: "note"}}
	plan := prepare(t, api, data)

	if _, err := execute(t, api, plan, state, func(o *runner.ExecuteOptions) {
		o.ContinueOnError = true
	}); err == nil {
		t.Fatal("expected a partial failure")
	}
	// The comment phase comes after the document phase, so it must not have run.
	if api.callCount("CreateComment") != 0 {
		t.Errorf("comments were posted past a failed phase (%d calls)", api.callCount("CreateComment"))
	}

	journal, err := importstate.Open(state, importstate.Identity{
		Importer: "jira-csv", SourceURL: "https://acme.atlassian.net",
		SourceFingerprint: "fingerprint", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace-1", TeamID: teamID,
	})
	if err != nil {
		t.Fatal(err)
	}
	item, _ := journal.Item("ENG-1")
	_ = journal.Close()
	if item.Complete() {
		t.Fatalf("item was marked complete despite a failed phase: %+v", item)
	}
	if item.Status != importstate.StatusCreated {
		t.Fatalf("status = %q, want created (the phase it reached)", item.Status)
	}
}

// A comment POST whose response was lost leaves the item at comment_unknown. The
// next run must reconcile the count and persist it before the comment phase runs,
// otherwise that phase re-reads the old count from the journal and posts the
// comment twice.
func TestCommentUnknownIsReconciledAcrossRuns(t *testing.T) {
	t.Parallel()
	state := statePath(t)
	api := newFakePulse()

	data := source(issue("ENG-1", "Commented"))
	data.Issues[0].Comments = []importers.Comment{{Body: "one"}, {Body: "two"}}

	// First run: the first comment lands but the response is lost, and the
	// count lookup fails too, so the item is parked at comment_unknown.
	api.commentHook = func(issueID, text string) (*pulseapi.Comment, error) {
		api.comments[issueID] = append(api.comments[issueID], text)
		return nil, apiError(http.StatusBadGateway, "", "bad gateway")
	}
	api.countCommentsHook = func(string) (int64, error) {
		return 0, apiError(http.StatusBadGateway, "", "bad gateway")
	}
	if _, err := execute(t, api, prepare(t, api, data), state); err == nil {
		t.Fatal("expected an unknown outcome")
	}

	// Second run: everything works again.
	api.commentHook = nil
	api.countCommentsHook = nil
	result, err := execute(t, api, prepare(t, api, data), state)
	if err != nil {
		t.Fatalf("resume: %v (errors %v)", err, result.Errors)
	}

	created := api.issueByTitle("Commented")
	got := api.commentsFor(created.ID)
	if len(got) != 2 {
		t.Fatalf("comments = %v; the comment whose response was lost must not be posted twice", got)
	}
	if got[0] != "one" || got[1] != "two" {
		t.Fatalf("comments = %v, want [one two]", got)
	}
}
