package runner_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

type fakeAPI struct {
	users               []pulseapi.User
	labels              []pulseapi.Label
	issues              map[string]*pulseapi.Issue
	listUsersErr        error
	listLabelsErr       error
	createLabelErr      error
	createLabelAccepted bool
	getIssue            func(string) (*pulseapi.Issue, error)
	createIssue         func(pulseapi.CreateIssueRequest) (*pulseapi.Issue, error)
	uploadDoc           func(string) (*pulseapi.Document, error)
	creates             int
	uploads             int
	labelCreates        int
	lastRequest         pulseapi.CreateIssueRequest
}

func (f *fakeAPI) ListUsers(context.Context) ([]pulseapi.User, error) {
	return f.users, f.listUsersErr
}

func (f *fakeAPI) ListLabels(context.Context, string) ([]pulseapi.Label, error) {
	return f.labels, f.listLabelsErr
}

func (f *fakeAPI) CreateLabel(_ context.Context, teamID string, req pulseapi.CreateLabelRequest) (*pulseapi.Label, error) {
	f.labelCreates++
	if f.createLabelErr != nil {
		if f.createLabelAccepted {
			f.labels = append(f.labels, pulseapi.Label{
				ID: "accepted-label", TeamID: teamID, Name: req.Name, EntityType: req.EntityType,
			})
		}
		return nil, f.createLabelErr
	}
	label := pulseapi.Label{
		ID: "label-" + req.Name, TeamID: teamID, Name: req.Name,
		EntityType: req.EntityType, Color: req.Color,
	}
	f.labels = append(f.labels, label)
	return &label, nil
}

func (f *fakeAPI) CreateIssue(_ context.Context, req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
	f.creates++
	f.lastRequest = req
	if f.createIssue != nil {
		return f.createIssue(req)
	}
	issue := &pulseapi.Issue{ID: "issue-1", TeamID: req.TeamID, Title: req.Title}
	if f.issues == nil {
		f.issues = map[string]*pulseapi.Issue{}
	}
	f.issues[issue.ID] = issue
	return issue, nil
}

func TestPrepareMapsAssigneesAndNormalizesLongLabels(t *testing.T) {
	api := &fakeAPI{users: []pulseapi.User{{
		ID: "user-1", DisplayName: "Ada Lovelace", Email: "ada@example.com",
	}}}
	data := sourceData("")
	data.Users["ada jira"] = importers.User{Name: "Ada Jira", Email: "ADA@example.com"}
	data.Issues[0].AssigneeID = "Ada Jira"
	longName := strings.Repeat("release-", 10)
	data.Labels["long"] = importers.Label{Name: longName}
	data.Issues[0].Labels = []string{"long"}
	plan := prepare(t, api, data)
	if !plan.Valid() || plan.AssigneesMatched != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Items[0].Request.AssigneeID == nil || *plan.Items[0].Request.AssigneeID != "user-1" {
		t.Fatalf("assignee=%v", plan.Items[0].Request.AssigneeID)
	}
	if len([]rune(plan.Labels[0].Name)) != 50 || !strings.Contains(plan.Labels[0].Name, "-") {
		t.Fatalf("normalized label=%q", plan.Labels[0].Name)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected label rename warning")
	}
}

func TestPrepareUsesPerIssueAssigneeEmailBeforeDuplicateDisplayNameMetadata(t *testing.T) {
	t.Parallel()
	data := sourceData("")
	data.Issues[0].AssigneeID = "Same Name"
	data.Issues[0].AssigneeEmail = "SECOND@example.com"
	data.Users["same name"] = importers.User{Name: "Same Name", Email: "first@example.com"}
	api := &fakeAPI{users: []pulseapi.User{
		{ID: "first", Email: "first@example.com", DisplayName: "Same Name"},
		{ID: "second", Email: "second@example.com", DisplayName: "Same Name"},
	}}
	plan, err := runner.New(api).Prepare(context.Background(), data, runner.Options{
		TeamID: "team-1", Assignee: runner.AssigneeMapped,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Request.AssigneeID == nil || *plan.Items[0].Request.AssigneeID != "second" {
		t.Fatalf("assignee=%v", plan.Items[0].Request.AssigneeID)
	}
}

func TestExecuteCreatesAndReusesLabelsDeterministically(t *testing.T) {
	api := &fakeAPI{labels: []pulseapi.Label{{
		ID: "existing", Name: "Type: Bug", EntityType: "issue",
	}}}
	data := sourceData("")
	data.Labels["type"] = importers.Label{Name: "type: bug"}
	data.Labels["release"] = importers.Label{Name: "Release: 1.0"}
	data.Issues[0].Labels = []string{"type", "release"}
	plan := prepare(t, api, data)
	if plan.LabelsToCreate() != 1 {
		t.Fatalf("labels=%+v", plan.Labels)
	}
	result, err := runner.New(api).Execute(context.Background(), plan, runner.ExecuteOptions{
		StateFile: filepath.Join(t.TempDir(), "state.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedIssues != 1 || api.labelCreates != 1 {
		t.Fatalf("result=%+v labels=%d", result, api.labelCreates)
	}
	if len(api.lastRequest.LabelIDs) != 2 || api.lastRequest.LabelIDs[0] != "existing" {
		t.Fatalf("label ids=%v", api.lastRequest.LabelIDs)
	}
}

func TestPrepareSelfAssignProjectAndLongTitle(t *testing.T) {
	api := &fakeAPI{}
	data := sourceData("")
	data.Issues[0].Title = strings.Repeat("界", 220)
	plan, err := runner.New(api).Prepare(context.Background(), data, runner.Options{
		ImporterID: "jira-csv", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace", TeamID: "team-1", ProjectID: "project-1",
		Assignee: runner.AssigneeSelf, SelfUserID: "me",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := plan.Items[0].Request
	if len([]rune(request.Title)) != 200 || request.ProjectID == nil || *request.ProjectID != "project-1" {
		t.Fatalf("request=%+v", request)
	}
	if request.AssigneeID == nil || *request.AssigneeID != "me" || len(plan.Warnings) == 0 {
		t.Fatalf("assignee=%v warnings=%v", request.AssigneeID, plan.Warnings)
	}
}

func (f *fakeAPI) GetIssue(_ context.Context, issueID string) (*pulseapi.Issue, error) {
	if f.getIssue != nil {
		return f.getIssue(issueID)
	}
	if issue := f.issues[issueID]; issue != nil {
		copy := *issue
		return &copy, nil
	}
	return nil, &pulseapi.APIError{Status: http.StatusNotFound, Message: "not found"}
}

func (f *fakeAPI) UploadMainDoc(_ context.Context, issueID, _ string, _ []byte) (*pulseapi.Document, error) {
	f.uploads++
	if f.uploadDoc != nil {
		return f.uploadDoc(issueID)
	}
	document := &pulseapi.Document{ID: "doc-1", Title: "Doc"}
	if issue := f.issues[issueID]; issue != nil {
		issue.MainDocID = &document.ID
	}
	return document, nil
}

func sourceData(body string) *importers.ImportResult {
	return &importers.ImportResult{
		SourcePath:        "/tmp/jira.csv",
		SourceURL:         "https://acme.atlassian.net",
		SourceFingerprint: "file-hash",
		Users:             map[string]importers.User{},
		Labels:            map[string]importers.Label{},
		Issues: []importers.Issue{{
			Key: "ENG-1", SourceRow: 2, RowHash: "row-hash",
			Title: "A valid issue", BodyMarkdown: body,
			Priority: importers.PriorityHigh, Type: importers.TypeBug,
		}},
	}
}

func prepare(t *testing.T, api *fakeAPI, data *importers.ImportResult) *runner.Plan {
	t.Helper()
	plan, err := runner.New(api).Prepare(context.Background(), data, runner.Options{
		ImporterID: "jira-csv", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace-1", TeamID: "team-1", Assignee: runner.AssigneeMapped,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPrepareCollectsValidationAndMappingDiagnostics(t *testing.T) {
	api := &fakeAPI{users: []pulseapi.User{
		{ID: "u1", DisplayName: "Same Name", Email: "one@example.com"},
		{ID: "u2", DisplayName: "Same Name", Email: "two@example.com"},
	}}
	data := sourceData("")
	data.Users["same name"] = importers.User{Name: "Same Name"}
	data.Issues[0].AssigneeID = "Same Name"
	data.Issues[0].Title = "x"
	data.Issues[0].Labels = make([]string, 11)
	for index := range data.Issues[0].Labels {
		key := string(rune('a' + index))
		data.Issues[0].Labels[index] = key
		data.Labels[key] = importers.Label{Name: "label-" + key}
	}
	plan := prepare(t, api, data)
	if plan.Valid() {
		t.Fatal("expected blocking validation errors")
	}
	if plan.AssigneesAmbiguous != 1 {
		t.Fatalf("ambiguous = %d", plan.AssigneesAmbiguous)
	}
	joined := ""
	for _, itemErr := range plan.Errors {
		joined += itemErr.Message + "\n"
	}
	for _, want := range []string{"at least 2", "at most 10"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestExecuteSuccessAndCompletedResume(t *testing.T) {
	api := &fakeAPI{}
	plan := prepare(t, api, sourceData("# Main Doc"))
	statePath := filepath.Join(t.TempDir(), "state.jsonl")
	engine := runner.New(api)

	first, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if first.CreatedIssues != 1 || first.CreatedMainDocs != 1 {
		t.Fatalf("first = %+v", first)
	}
	second, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if second.SkippedIssues != 1 || api.creates != 1 || api.uploads != 1 {
		t.Fatalf("second=%+v creates=%d uploads=%d", second, api.creates, api.uploads)
	}
}

func TestAmbiguousCreateStopsAndCanBeAdopted(t *testing.T) {
	api := &fakeAPI{
		issues: map[string]*pulseapi.Issue{
			"adopted": {ID: "adopted", TeamID: "team-1", Title: "A valid issue"},
		},
		createIssue: func(pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
			return nil, errors.New("connection reset after write")
		},
	}
	plan := prepare(t, api, sourceData("# Doc"))
	statePath := filepath.Join(t.TempDir(), "state.jsonl")
	engine := runner.New(api)

	_, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	var unknown *runner.UnknownOutcomeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %T %v", err, err)
	}
	if api.creates != 1 {
		t.Fatalf("creates = %d", api.creates)
	}
	_, err = engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	if !errors.As(err, &unknown) || api.creates != 1 {
		t.Fatalf("rerun err=%v creates=%d", err, api.creates)
	}

	result, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{
		StateFile: statePath,
		Adopt:     map[string]string{"eng-1": "adopted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SkippedIssues != 1 || result.CreatedMainDocs != 1 || api.creates != 1 {
		t.Fatalf("result=%+v creates=%d", result, api.creates)
	}
}

func TestUnknownCreateRetriesOnlyWithExplicitAuthorization(t *testing.T) {
	api := &fakeAPI{}
	ambiguous := true
	api.createIssue = func(req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		if ambiguous {
			return nil, errors.New("timeout after request write")
		}
		issue := &pulseapi.Issue{ID: "retried", TeamID: req.TeamID, Title: req.Title}
		if api.issues == nil {
			api.issues = map[string]*pulseapi.Issue{}
		}
		api.issues[issue.ID] = issue
		return issue, nil
	}
	plan := prepare(t, api, sourceData(""))
	statePath := filepath.Join(t.TempDir(), "state.jsonl")
	engine := runner.New(api)
	_, _ = engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	ambiguous = false
	result, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{
		StateFile: statePath, RetryUnknown: map[string]bool{"eng-1": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedIssues != 1 || api.creates != 2 {
		t.Fatalf("result=%+v creates=%d", result, api.creates)
	}
}

func TestMainDocFailureIsPartialAndResumesWithoutNewIssue(t *testing.T) {
	api := &fakeAPI{}
	failUpload := true
	api.uploadDoc = func(issueID string) (*pulseapi.Document, error) {
		if failUpload {
			return nil, &pulseapi.APIError{Status: http.StatusBadRequest, Message: "bad document"}
		}
		document := &pulseapi.Document{ID: "doc-retry"}
		api.issues[issueID].MainDocID = &document.ID
		return document, nil
	}
	plan := prepare(t, api, sourceData("# Doc"))
	statePath := filepath.Join(t.TempDir(), "state.jsonl")
	engine := runner.New(api)

	first, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	var partial *runner.PartialError
	if !errors.As(err, &partial) || first.FailedMainDocs != 1 {
		t.Fatalf("result=%+v err=%v", first, err)
	}
	failUpload = false
	second, err := engine.Execute(context.Background(), plan, runner.ExecuteOptions{StateFile: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedMainDocs != 1 || api.creates != 1 || api.uploads != 2 {
		t.Fatalf("second=%+v creates=%d uploads=%d", second, api.creates, api.uploads)
	}
}

func TestContinueAfterDefinitiveCreateFailure(t *testing.T) {
	api := &fakeAPI{}
	attempt := 0
	api.createIssue = func(req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
		attempt++
		if attempt == 1 {
			return nil, &pulseapi.APIError{Status: http.StatusBadRequest, Message: "invalid"}
		}
		issue := &pulseapi.Issue{ID: "issue-2", TeamID: req.TeamID, Title: req.Title}
		if api.issues == nil {
			api.issues = map[string]*pulseapi.Issue{}
		}
		api.issues[issue.ID] = issue
		return issue, nil
	}
	data := sourceData("")
	second := data.Issues[0]
	second.Key, second.RowHash, second.Title, second.SourceRow = "ENG-2", "row-2", "Second issue", 3
	data.Issues = append(data.Issues, second)
	plan := prepare(t, api, data)
	result, err := runner.New(api).Execute(context.Background(), plan, runner.ExecuteOptions{
		StateFile: filepath.Join(t.TempDir(), "state.jsonl"), ContinueOnError: true,
	})
	var partial *runner.PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v", err)
	}
	if result.FailedIssues != 1 || result.CreatedIssues != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestPrepareOperationalErrorsAndPlanHelpers(t *testing.T) {
	engine := runner.New(&fakeAPI{})
	if _, err := engine.Prepare(context.Background(), nil, runner.Options{TeamID: "team"}); err == nil {
		t.Fatal("expected nil data error")
	}
	if _, err := engine.Prepare(context.Background(), sourceData(""), runner.Options{}); err == nil {
		t.Fatal("expected missing team error")
	}
	if _, err := runner.New(&fakeAPI{listUsersErr: errors.New("users down")}).
		Prepare(context.Background(), sourceData(""), runner.Options{
			TeamID: "team", Assignee: runner.AssigneeMapped,
		}); err == nil || !strings.Contains(err.Error(), "list users") {
		t.Fatalf("err=%v", err)
	}
	if _, err := runner.New(&fakeAPI{listLabelsErr: errors.New("labels down")}).
		Prepare(context.Background(), sourceData(""), runner.Options{
			TeamID: "team", Assignee: runner.AssigneeNone,
		}); err == nil || !strings.Contains(err.Error(), "list labels") {
		t.Fatalf("err=%v", err)
	}
	plan := prepare(t, &fakeAPI{}, sourceData("# Doc"))
	if plan.MainDocCount() != 1 {
		t.Fatalf("main docs=%d", plan.MainDocCount())
	}
	partial := &runner.PartialError{Failures: 2}
	if !strings.Contains(partial.Error(), "2") {
		t.Fatal(partial.Error())
	}
	cause := errors.New("timeout")
	unknown := &runner.UnknownOutcomeError{Key: "ENG-1", StateFile: "state", Cause: cause}
	if !errors.Is(unknown, cause) || !strings.Contains(unknown.Error(), "ENG-1") {
		t.Fatal(unknown.Error())
	}
}

func TestPrepareDetectsLabelCollisionsAndDuplicatePulseLabels(t *testing.T) {
	data := sourceData("")
	data.Labels["one"] = importers.Label{Name: "Same"}
	data.Labels["two"] = importers.Label{Name: "same"}
	data.Issues[0].Labels = []string{"one"}
	api := &fakeAPI{labels: []pulseapi.Label{
		{ID: "l1", Name: "Existing", EntityType: "issue"},
		{ID: "l2", Name: "existing", EntityType: "issue"},
	}}
	plan := prepare(t, api, data)
	if plan.Valid() {
		t.Fatal("expected label collision errors")
	}
	var messages string
	for _, itemErr := range plan.Errors {
		messages += itemErr.Message
	}
	for _, want := range []string{"normalize to the same", "multiple active"} {
		if !strings.Contains(messages, want) {
			t.Fatalf("missing %q in %s", want, messages)
		}
	}
}

func TestAmbiguousLabelCreateRecoversByExactRead(t *testing.T) {
	api := &fakeAPI{createLabelErr: errors.New("connection reset"), createLabelAccepted: true}
	data := sourceData("")
	data.Labels["bug"] = importers.Label{Name: "Bug"}
	data.Issues[0].Labels = []string{"bug"}
	plan := prepare(t, api, data)
	result, err := runner.New(api).Execute(context.Background(), plan, runner.ExecuteOptions{
		StateFile: filepath.Join(t.TempDir(), "state.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedIssues != 1 || api.lastRequest.LabelIDs[0] != "accepted-label" {
		t.Fatalf("result=%+v request=%+v", result, api.lastRequest)
	}
}

func TestAmbiguousMainDocUploadRecoveredFromIssue(t *testing.T) {
	api := &fakeAPI{}
	api.uploadDoc = func(issueID string) (*pulseapi.Document, error) {
		docID := "accepted-doc"
		api.issues[issueID].MainDocID = &docID
		return nil, &pulseapi.APIError{Status: http.StatusBadGateway, Message: "lost response"}
	}
	plan := prepare(t, api, sourceData("# Doc"))
	result, err := runner.New(api).Execute(context.Background(), plan, runner.ExecuteOptions{
		StateFile: filepath.Join(t.TempDir(), "state.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedMainDocs != 1 || result.FailedMainDocs != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecuteRejectsInvalidPlan(t *testing.T) {
	if _, err := runner.New(&fakeAPI{}).Execute(context.Background(), nil, runner.ExecuteOptions{}); err == nil {
		t.Fatal("expected nil plan error")
	}
	plan := &runner.Plan{Errors: []runner.Diagnostic{{Message: "bad"}}}
	if _, err := runner.New(&fakeAPI{}).Execute(context.Background(), plan, runner.ExecuteOptions{}); err == nil {
		t.Fatal("expected invalid plan error")
	}
}
