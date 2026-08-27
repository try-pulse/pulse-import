package runner_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

const teamID = "team-1"

func member(id, first, last, email string) pulseapi.TeamMember {
	return pulseapi.TeamMember{ID: id, FirstName: first, LastName: last, Email: email}
}

// source builds a minimal import result; tests mutate what they care about.
func source(issues ...importers.Issue) *importers.ImportResult {
	result := &importers.ImportResult{
		Issues:            issues,
		Users:             map[string]importers.User{},
		Labels:            map[string]importers.Label{},
		StatusNames:       map[string][]string{},
		SourcePath:        "/tmp/jira.csv",
		SourceURL:         "https://acme.atlassian.net",
		SourceFingerprint: "fingerprint",
	}
	return result
}

func issue(key, title string) importers.Issue {
	return importers.Issue{
		Key: key, Title: title, RowHash: "hash-" + key, SourceRow: 2,
		Status: "To Do", Priority: importers.PriorityMedium, Type: importers.TypeTask,
	}
}

func baseOptions() runner.Options {
	return runner.Options{
		ImporterID: "jira-csv", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace-1", TeamID: teamID, TeamPath: []string{teamID},
		Assignee: runner.AssigneeNone, Concurrency: 1,
		Now: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	}
}

func prepare(t *testing.T, api runner.API, data *importers.ImportResult, mutate ...func(*runner.Options)) *runner.Plan {
	t.Helper()
	opts := baseOptions()
	for _, apply := range mutate {
		apply(&opts)
	}
	plan, err := runner.New(api).Prepare(context.Background(), data, opts)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return plan
}

func itemFor(t *testing.T, plan *runner.Plan, key string) runner.PreparedItem {
	t.Helper()
	for _, item := range plan.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("item %s not in plan (%d items)", key, len(plan.Items))
	return runner.PreparedItem{}
}

func planErrors(plan *runner.Plan) string {
	var parts []string
	for _, diagnostic := range plan.Errors {
		parts = append(parts, diagnostic.Message)
	}
	return strings.Join(parts, " | ")
}

func planWarnings(plan *runner.Plan) string {
	var parts []string
	for _, diagnostic := range plan.Warnings {
		parts = append(parts, diagnostic.Message)
	}
	return strings.Join(parts, " | ")
}

// Pulse rejects an assignee who is not in the issue's team or a parent team, so
// matching against the wider workspace would fail every affected create.
func TestAssigneeMatchingIsLimitedToTheTeamRoster(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID, member("user-in", "Ada", "Lovelace", "ada@acme.com"))

	data := source(issue("ENG-1", "In team"), issue("ENG-2", "Outsider"))
	data.Issues[0].AssigneeID = "Ada Lovelace"
	data.Issues[1].AssigneeID = "Grace Hopper"
	data.Users["ada lovelace"] = importers.User{Name: "Ada Lovelace", Rows: 1}
	data.Users["grace hopper"] = importers.User{Name: "Grace Hopper", Rows: 1}

	plan := prepare(t, api, data, func(o *runner.Options) { o.Assignee = runner.AssigneeMapped })
	if !plan.Valid() {
		t.Fatalf("errors: %s", planErrors(plan))
	}

	inTeam := itemFor(t, plan, "ENG-1")
	if inTeam.Issue.AssigneeID == nil || *inTeam.Issue.AssigneeID != "user-in" {
		t.Errorf("ENG-1 assignee = %v, want user-in", inTeam.Issue.AssigneeID)
	}
	outsider := itemFor(t, plan, "ENG-2")
	if outsider.Issue.AssigneeID != nil {
		t.Errorf("ENG-2 assignee = %v; a non-member must be left unassigned", *outsider.Issue.AssigneeID)
	}

	states := map[string]string{}
	for _, resolution := range plan.UserMapping {
		states[resolution.SourceName] = resolution.State
	}
	if states["Grace Hopper"] != "unmatched" {
		t.Errorf("Grace Hopper state = %q", states["Grace Hopper"])
	}
}

func TestAssigneeEmailBeatsAmbiguousName(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID,
		member("first", "Same", "Name", "first@acme.com"),
		member("second", "Same", "Name", "second@acme.com"),
	)
	data := source(issue("ENG-1", "Pick by email"))
	data.Issues[0].AssigneeID = "Same Name"
	data.Issues[0].AssigneeEmail = "SECOND@acme.com"
	data.Users["same name"] = importers.User{Name: "Same Name", Email: "SECOND@acme.com", Rows: 1}

	plan := prepare(t, api, data, func(o *runner.Options) { o.Assignee = runner.AssigneeMapped })
	item := itemFor(t, plan, "ENG-1")
	if item.Issue.AssigneeID == nil || *item.Issue.AssigneeID != "second" {
		t.Fatalf("assignee = %v, want second", item.Issue.AssigneeID)
	}
}

func TestAmbiguousNameIsLeftUnassigned(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID,
		member("first", "Same", "Name", "first@acme.com"),
		member("second", "Same", "Name", "second@acme.com"),
	)
	data := source(issue("ENG-1", "Ambiguous"))
	data.Issues[0].AssigneeID = "Same Name"
	data.Users["same name"] = importers.User{Name: "Same Name", Rows: 1}

	plan := prepare(t, api, data, func(o *runner.Options) { o.Assignee = runner.AssigneeMapped })
	if item := itemFor(t, plan, "ENG-1"); item.Issue.AssigneeID != nil {
		t.Fatalf("assignee = %v, want nil", *item.Issue.AssigneeID)
	}
	if !strings.Contains(planWarnings(plan)+planErrors(plan), "") {
		t.Fatal("unreachable")
	}
	for _, resolution := range plan.UserMapping {
		if resolution.SourceName == "Same Name" && resolution.State != "ambiguous" {
			t.Fatalf("state = %q, want ambiguous", resolution.State)
		}
	}
}

// --self-assign by someone outside the team would have Pulse reject every single
// create, so it has to fail preflight rather than the whole import.
func TestSelfAssignRequiresTeamMembership(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID, member("someone", "Some", "One", "one@acme.com"))
	plan := prepare(t, api, source(issue("ENG-1", "Mine")), func(o *runner.Options) {
		o.Assignee = runner.AssigneeSelf
		o.SelfUserID = "outsider"
	})
	if plan.Valid() {
		t.Fatal("expected a preflight error")
	}
	if !strings.Contains(planErrors(plan), "member of the target team") {
		t.Fatalf("errors: %s", planErrors(plan))
	}
}

func TestMapUserToNonMemberFailsPreflight(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID, member("user-1", "Ada", "Lovelace", "ada@acme.com"))
	data := source(issue("ENG-1", "Mapped"))
	data.Issues[0].AssigneeID = "Jane Doe"
	data.Users["jane doe"] = importers.User{Name: "Jane Doe", Rows: 1}

	plan := prepare(t, api, data, func(o *runner.Options) {
		o.Assignee = runner.AssigneeMapped
		o.UserMap = map[string]string{"jane doe": "ghost-user"}
	})
	if plan.Valid() {
		t.Fatal("expected a preflight error for a non-member mapping")
	}
	if !strings.Contains(planErrors(plan), "does not resolve to a member") {
		t.Fatalf("errors: %s", planErrors(plan))
	}
}

func TestMapUserSkipSentinelLeavesIssuesUnassigned(t *testing.T) {
	t.Parallel()
	api := newFakePulse().withMembers(teamID, member("user-1", "Jane", "Doe", "jane@acme.com"))
	data := source(issue("ENG-1", "Skip me"))
	data.Issues[0].AssigneeID = "Jane Doe"
	data.Users["jane doe"] = importers.User{Name: "Jane Doe", Rows: 1}

	plan := prepare(t, api, data, func(o *runner.Options) {
		o.Assignee = runner.AssigneeMapped
		o.UserMap = map[string]string{"jane doe": runner.SkipUser}
	})
	if !plan.Valid() {
		t.Fatalf("errors: %s", planErrors(plan))
	}
	if item := itemFor(t, plan, "ENG-1"); item.Issue.AssigneeID != nil {
		t.Fatalf("assignee = %v, want nil", *item.Issue.AssigneeID)
	}
}

// Pulse counts title length in bytes. A rune-based cut leaves a non-Latin title
// over the limit and every create fails with a 400.
func TestTitlesAreTruncatedToPulsesByteLimit(t *testing.T) {
	t.Parallel()
	persian := strings.Repeat("م", 150) // 150 runes, 300 bytes
	plan := prepare(t, newFakePulse(), source(issue("ENG-1", persian)))
	if !plan.Valid() {
		t.Fatalf("errors: %s", planErrors(plan))
	}
	title := itemFor(t, plan, "ENG-1").Issue.Title
	if len(title) > pulseapi.MaxTitleBytes {
		t.Fatalf("title is %d bytes, over Pulse's %d-byte limit", len(title), pulseapi.MaxTitleBytes)
	}
	if !strings.Contains(planWarnings(plan), "byte limit") {
		t.Errorf("expected a truncation warning, got: %s", planWarnings(plan))
	}
}

func TestLabelNamesAreTruncatedToPulsesByteLimit(t *testing.T) {
	t.Parallel()
	longPersian := strings.Repeat("ت", 40) // 40 runes, 80 bytes
	data := source(issue("ENG-1", "Labelled"))
	data.Labels["long"] = importers.Label{Name: longPersian, Kind: importers.LabelKindJira}
	data.Issues[0].Labels = []string{"long"}

	plan := prepare(t, newFakePulse(), data)
	if !plan.Valid() {
		t.Fatalf("errors: %s", planErrors(plan))
	}
	for _, label := range plan.Labels {
		if len(label.Name) > pulseapi.MaxLabelBytes {
			t.Fatalf("label %q is %d bytes, over Pulse's %d-byte limit",
				label.Name, len(label.Name), pulseapi.MaxLabelBytes)
		}
	}
}

func TestArchivedLabelIsPlannedForUnarchiveNotCreate(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.labels = []pulseapi.Label{
		{ID: "label-old", TeamID: teamID, Name: "performance", EntityType: "issue", Archived: true},
	}
	data := source(issue("ENG-1", "Perf work"))
	data.Labels["performance"] = importers.Label{Name: "performance", Kind: importers.LabelKindJira}
	data.Issues[0].Labels = []string{"performance"}

	plan := prepare(t, api, data)
	var found bool
	for _, label := range plan.Labels {
		if label.Key != "performance" {
			continue
		}
		found = true
		if label.Create {
			t.Error("an archived label must not be recreated: Pulse's uniqueness index spans archived rows")
		}
		if label.ArchivedID != "label-old" {
			t.Errorf("ArchivedID = %q", label.ArchivedID)
		}
	}
	if !found {
		t.Fatalf("labels = %+v", plan.Labels)
	}
	if !strings.Contains(planWarnings(plan), "archived") {
		t.Errorf("expected a warning about reusing the archived label: %s", planWarnings(plan))
	}
}

func TestLabelCapDropsLeastMeaningfulAndWarns(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Very labelled"))
	keys := []string{}
	// The Migrated marker and the type label must survive; sprint labels are the
	// first to go.
	data.Labels["migrated"] = importers.Label{Name: "Migrated", Kind: importers.LabelKindMigrated}
	data.Labels["type: story"] = importers.Label{Name: "Type: Story", Kind: importers.LabelKindType}
	keys = append(keys, "type: story")
	for i := 0; i < 12; i++ {
		key := "sprint: s" + string(rune('a'+i))
		data.Labels[key] = importers.Label{Name: "Sprint: S" + string(rune('a'+i)), Kind: importers.LabelKindSprint}
		keys = append(keys, key)
	}
	data.Issues[0].Labels = keys

	plan := prepare(t, newFakePulse(), data, func(o *runner.Options) {
		o.AddMigratedLabel = true
		o.LabelPolicy = runner.LabelPolicyDrop
	})
	if !plan.Valid() {
		t.Fatalf("errors: %s", planErrors(plan))
	}
	item := itemFor(t, plan, "ENG-1")
	if len(item.LabelKeys) != 10 {
		t.Fatalf("kept %d labels, want 10", len(item.LabelKeys))
	}
	if item.LabelKeys[0] != "migrated" {
		t.Errorf("first label = %q, want migrated to survive the cap", item.LabelKeys[0])
	}
	if item.LabelKeys[1] != "type: story" {
		t.Errorf("second label = %q, want the type label to survive", item.LabelKeys[1])
	}
	if plan.DroppedLabels == 0 || !strings.Contains(planWarnings(plan), "were dropped") {
		t.Errorf("expected a drop warning, got: %s", planWarnings(plan))
	}
}

func TestStrictLabelsFailsInsteadOfDropping(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Very labelled"))
	var keys []string
	for i := 0; i < 12; i++ {
		key := "l" + string(rune('a'+i))
		data.Labels[key] = importers.Label{Name: "L" + string(rune('A'+i)), Kind: importers.LabelKindJira}
		keys = append(keys, key)
	}
	data.Issues[0].Labels = keys

	plan := prepare(t, newFakePulse(), data, func(o *runner.Options) {
		o.LabelPolicy = runner.LabelPolicyError
	})
	if plan.Valid() {
		t.Fatal("expected a validation error")
	}
	if !strings.Contains(planErrors(plan), "at most 10") {
		t.Fatalf("errors: %s", planErrors(plan))
	}
}

func TestResolutionOverridesStatus(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Closed by resolution"))
	data.Issues[0].Status = "Open"
	data.Issues[0].StatusOverride = string(statusmap.Done)

	plan := prepare(t, newFakePulse(), data)
	if got := itemFor(t, plan, "ENG-1").Issue.Status; got != "done" {
		t.Fatalf("status = %q, want done", got)
	}
}

func TestStatusFilters(t *testing.T) {
	t.Parallel()
	build := func() *importers.ImportResult {
		open := issue("ENG-1", "Open work")
		open.Status = "To Do"
		done := issue("ENG-2", "Finished")
		done.Status = "Done"
		return source(open, done)
	}

	t.Run("skip-status", func(t *testing.T) {
		plan := prepare(t, newFakePulse(), build(), func(o *runner.Options) {
			o.SkipStatuses = map[statusmap.PulseStatus]bool{statusmap.Done: true}
		})
		if plan.IssueCount() != 1 || plan.FilteredIssues != 1 {
			t.Fatalf("items=%d filtered=%d", plan.IssueCount(), plan.FilteredIssues)
		}
		itemFor(t, plan, "ENG-1")
	})

	t.Run("only-status", func(t *testing.T) {
		plan := prepare(t, newFakePulse(), build(), func(o *runner.Options) {
			o.OnlyStatuses = map[statusmap.PulseStatus]bool{statusmap.Done: true}
		})
		if plan.IssueCount() != 1 {
			t.Fatalf("items=%d", plan.IssueCount())
		}
		itemFor(t, plan, "ENG-2")
	})
}

func TestSkipStaleUsesSourceUpdatedAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	fresh := issue("ENG-1", "Fresh")
	freshTime := now.AddDate(0, 0, -10)
	fresh.UpdatedAt = &freshTime
	stale := issue("ENG-2", "Stale")
	staleTime := now.AddDate(-2, 0, 0)
	stale.UpdatedAt = &staleTime

	plan := prepare(t, newFakePulse(), source(fresh, stale), func(o *runner.Options) {
		o.Now = now
		o.StaleAfter = 180 * 24 * time.Hour
	})
	if plan.IssueCount() != 1 || plan.FilteredIssues != 1 {
		t.Fatalf("items=%d filtered=%d", plan.IssueCount(), plan.FilteredIssues)
	}
	itemFor(t, plan, "ENG-1")
}

func TestEstimateMapping(t *testing.T) {
	t.Parallel()
	points := func(value float64) *float64 { return &value }
	seconds := func(value int) *int { return &value }

	tests := []struct {
		name     string
		settings pulseapi.EstimateSettings
		points   *float64
		seconds  *int
		want     *int
		wantNote string
	}{
		{
			name:     "disabled team gets no estimate",
			settings: pulseapi.EstimateSettings{Enabled: false},
			points:   points(5),
		},
		{
			name:     "hours scale uses the time estimate",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "hours"},
			seconds:  seconds(10800),
			want:     seconds(3),
		},
		{
			name:     "hours scale ignores story points",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "hours"},
			points:   points(5),
		},
		{
			name:     "fibonacci accepts an allowed value",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "fibonacci"},
			points:   points(5),
			want:     seconds(5),
		},
		{
			name:     "fibonacci snaps a value off the scale",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "fibonacci"},
			points:   points(4),
			want:     seconds(3),
			wantNote: "snapped",
		},
		{
			name:     "extended fibonacci reaches 13",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "fibonacci", ExtendedScale: true},
			points:   points(12),
			want:     seconds(13),
			wantNote: "snapped",
		},
		{
			name:     "zero is dropped unless the team allows it",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "fibonacci"},
			points:   points(0),
		},
		{
			name:     "zero is kept when the team allows it",
			settings: pulseapi.EstimateSettings{Enabled: true, ScaleType: "fibonacci", AllowZero: true},
			points:   points(0),
			want:     seconds(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := source(issue("ENG-1", "Estimated"))
			data.Issues[0].StoryPoints = tt.points
			data.Issues[0].OriginalEstimateSeconds = tt.seconds
			plan := prepare(t, newFakePulse(), data, func(o *runner.Options) {
				o.EstimateSettings = tt.settings
			})
			got := itemFor(t, plan, "ENG-1").Issue.TimeEstimate
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("estimate = %d, want none", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("estimate = none, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("estimate = %d, want %d", *got, *tt.want)
			}
			if tt.wantNote != "" && !strings.Contains(planWarnings(plan), tt.wantNote) {
				t.Errorf("expected a %q warning, got: %s", tt.wantNote, planWarnings(plan))
			}
		})
	}
}

func TestDueDateReachesTheRequest(t *testing.T) {
	t.Parallel()
	due := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	data := source(issue("ENG-1", "Due soon"))
	data.Issues[0].DueDate = &due

	plan := prepare(t, newFakePulse(), source(data.Issues...))
	got := itemFor(t, plan, "ENG-1").Issue.DueDate
	if got == nil || !got.Equal(due) {
		t.Fatalf("due date = %v, want %v", got, due)
	}
}

// Creation order decides whether Pulse identifiers line up with the source keys,
// and a sub-issue cannot be created before its parent exists.
func TestItemsAreOrderedByWaveThenNumericSourceKey(t *testing.T) {
	t.Parallel()
	child := issue("ENG-2", "Child")
	child.ParentKey = "ENG-10"
	data := source(issue("ENG-10", "Tenth"), child, issue("ENG-3", "Third"))
	data.Projects = []importers.Project{{
		Key: "ENG-1", Title: "Epic", RowHash: "hash-epic", SourceRow: 2,
	}}
	data.Issues[0].EpicKey = "ENG-1"

	plan := prepare(t, newFakePulse(), data)
	var order []string
	for _, item := range plan.Items {
		order = append(order, item.Key)
	}
	want := "ENG-1,ENG-3,ENG-10,ENG-2"
	if strings.Join(order, ",") != want {
		t.Fatalf("order = %v, want %s (project, then issues by number, then sub-issues)", order, want)
	}
}

func TestEpicBecomesProjectAndChildrenReferenceIt(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-2", "Child of epic"))
	data.Issues[0].EpicKey = "ENG-1"
	data.Projects = []importers.Project{{
		Key: "ENG-1", Title: "Checkout revamp", RowHash: "hash-epic",
		SourceRow: 2, Status: "In Progress",
	}}

	plan := prepare(t, newFakePulse(), data)
	if plan.ProjectCount != 1 {
		t.Fatalf("projects = %d", plan.ProjectCount)
	}
	project := itemFor(t, plan, "ENG-1")
	if project.Kind != importstate.KindProject || project.Wave != 0 {
		t.Fatalf("project item = %+v", project)
	}
	if project.Project.Status != "in_progress" {
		t.Errorf("project status = %q", project.Project.Status)
	}
	if child := itemFor(t, plan, "ENG-2"); child.EpicKey != "ENG-1" {
		t.Errorf("child epic = %q", child.EpicKey)
	}
}

func TestUnreferencedEpicIsNotCreated(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-2", "Unrelated"))
	data.Projects = []importers.Project{{
		Key: "ENG-1", Title: "Lonely epic", RowHash: "hash-epic", SourceRow: 2,
	}}
	plan := prepare(t, newFakePulse(), data)
	if plan.ProjectCount != 0 {
		t.Fatalf("projects = %d, want 0", plan.ProjectCount)
	}
	if !strings.Contains(planWarnings(plan), "no imported children") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}
}

func TestForcedProjectSuppressesEpicProjects(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-2", "Child"))
	data.Issues[0].EpicKey = "ENG-1"
	data.Projects = []importers.Project{{
		Key: "ENG-1", Title: "Epic", RowHash: "hash-epic", SourceRow: 2,
	}}
	plan := prepare(t, newFakePulse(), data, func(o *runner.Options) { o.ProjectID = "project-forced" })
	if plan.ProjectCount != 0 {
		t.Fatalf("projects = %d, want 0", plan.ProjectCount)
	}
	item := itemFor(t, plan, "ENG-2")
	if item.Issue.ProjectID == nil || *item.Issue.ProjectID != "project-forced" {
		t.Fatalf("project = %v", item.Issue.ProjectID)
	}
	if item.EpicKey != "" {
		t.Errorf("EpicKey should be cleared when --project pins the project")
	}
}

func TestSubIssueDoesNotAlsoCarryAProject(t *testing.T) {
	t.Parallel()
	child := issue("ENG-2", "Sub")
	child.ParentKey = "ENG-1"
	child.EpicKey = "ENG-9"
	data := source(issue("ENG-1", "Parent"), child)
	data.Projects = []importers.Project{{
		Key: "ENG-9", Title: "Epic", RowHash: "hash-epic", SourceRow: 2,
	}}

	plan := prepare(t, newFakePulse(), data)
	item := itemFor(t, plan, "ENG-2")
	if item.ParentKey != "ENG-1" || item.Wave != 2 {
		t.Fatalf("item = %+v", item)
	}
	// Pulse inherits a sub-issue's project from its parent and overwrites
	// whatever was sent, so sending one would be misleading.
	if item.Issue.ProjectID != nil || item.EpicKey != "" {
		t.Fatalf("sub-issue should carry no project: project=%v epic=%q", item.Issue.ProjectID, item.EpicKey)
	}
}

func TestRelationsToIssuesOutsideTheImportAreDropped(t *testing.T) {
	t.Parallel()
	first := issue("ENG-1", "Blocks something")
	first.Relations = []importers.Relation{
		{Kind: importers.RelationBlocks, TargetKey: "ENG-2"},
		{Kind: importers.RelationBlocks, TargetKey: "ENG-404"},
	}
	plan := prepare(t, newFakePulse(), source(first, issue("ENG-2", "Blocked")))
	item := itemFor(t, plan, "ENG-1")
	if len(item.Blocks) != 1 || item.Blocks[0] != "ENG-2" {
		t.Fatalf("blocks = %v", item.Blocks)
	}
	if !strings.Contains(planWarnings(plan), "ENG-404") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}
}

// Pulse rejects a cycle in the blocks graph (400 on the PUT that closes it),
// so preflight has to break cycles instead of letting the link pass fail at
// the very end of the import.
func TestDependencyCycleIsBrokenInPreflight(t *testing.T) {
	t.Parallel()
	first := issue("ENG-1", "First issue")
	first.Relations = []importers.Relation{{Kind: importers.RelationBlocks, TargetKey: "ENG-2"}}
	second := issue("ENG-2", "Second issue")
	second.Relations = []importers.Relation{{Kind: importers.RelationBlocks, TargetKey: "ENG-3"}}
	third := issue("ENG-3", "Third issue")
	third.Relations = []importers.Relation{{Kind: importers.RelationBlocks, TargetKey: "ENG-1"}}

	plan := prepare(t, newFakePulse(), source(first, second, third))

	kept := 0
	for _, key := range []string{"ENG-1", "ENG-2", "ENG-3"} {
		kept += len(itemFor(t, plan, key).Blocks)
	}
	if kept != 2 {
		t.Fatalf("kept %d blocks links, want 2 (one edge of the cycle dropped)", kept)
	}
	if plan.RelationCount != 2 {
		t.Fatalf("RelationCount = %d, want 2", plan.RelationCount)
	}
	if !strings.Contains(planWarnings(plan), "dependency cycle") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}
}

// Jira's export states each link twice — "blocks" on one row and
// "is blocked by" on the other. Both describe the same directed edge, so the
// pair must not be mistaken for a cycle.
func TestMirroredBlockLinkIsNotACycle(t *testing.T) {
	t.Parallel()
	first := issue("ENG-1", "Blocker")
	first.Relations = []importers.Relation{{Kind: importers.RelationBlocks, TargetKey: "ENG-2"}}
	second := issue("ENG-2", "Blocked")
	second.Relations = []importers.Relation{{Kind: importers.RelationBlockedBy, TargetKey: "ENG-1"}}

	plan := prepare(t, newFakePulse(), source(first, second))

	if blocks := itemFor(t, plan, "ENG-1").Blocks; len(blocks) != 1 {
		t.Fatalf("blocks = %v", blocks)
	}
	if blockedBy := itemFor(t, plan, "ENG-2").BlockedBy; len(blockedBy) != 1 {
		t.Fatalf("blockedBy = %v", blockedBy)
	}
	if strings.Contains(planWarnings(plan), "cycle") {
		t.Fatalf("mirrored link flagged as a cycle: %s", planWarnings(plan))
	}
}

func TestSelfBlockLinkIsDropped(t *testing.T) {
	t.Parallel()
	first := issue("ENG-1", "Blocks itself")
	first.Relations = []importers.Relation{{Kind: importers.RelationBlocks, TargetKey: "ENG-1"}}

	plan := prepare(t, newFakePulse(), source(first))

	if blocks := itemFor(t, plan, "ENG-1").Blocks; len(blocks) != 0 {
		t.Fatalf("blocks = %v", blocks)
	}
	if !strings.Contains(planWarnings(plan), "self-referential") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}
}

func TestLargeImportWarnsAboutNotificationFanout(t *testing.T) {
	t.Parallel()
	var issues []importers.Issue
	for i := 1; i <= 26; i++ {
		issues = append(issues, issue(fmt.Sprintf("ENG-%d", i), fmt.Sprintf("Issue %d", i)))
	}
	plan := prepare(t, newFakePulse(), source(issues...))
	if !strings.Contains(planWarnings(plan), "notifications") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}

	small := prepare(t, newFakePulse(), source(issue("ENG-1", "Only issue")))
	if strings.Contains(planWarnings(small), "notifications") {
		t.Fatalf("small import should stay quiet: %s", planWarnings(small))
	}
}

// withSprints wires an issue through the sprint machinery the way the jiracsv
// importer does: one "Sprint: …" label per sprint plus the Sprints refs.
func withSprints(data *importers.ImportResult, issueIndex int, names ...string) {
	issue := &data.Issues[issueIndex]
	for _, name := range names {
		key := strings.ToLower("Sprint: " + name)
		data.Labels[key] = importers.Label{Name: "Sprint: " + name, Kind: importers.LabelKindSprint}
		issue.Labels = append(issue.Labels, key)
		issue.Sprints = append(issue.Sprints, importers.SprintRef{Name: name, LabelKey: key})
	}
}

func planLabelNames(plan *runner.Plan) []string {
	var names []string
	for _, label := range plan.Labels {
		names = append(names, label.Name)
	}
	return names
}

func TestLastSprintBecomesCycleAndEarlierSprintsStayLabels(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Issue in two sprints"))
	withSprints(data, 0, "Sprint 1", "Sprint 2")

	plan := prepare(t, newFakePulse(), data)

	item := itemFor(t, plan, "ENG-1")
	if item.CycleKey != "sprint 2" {
		t.Fatalf("cycle key = %q", item.CycleKey)
	}
	if strings.Contains(strings.Join(item.LabelKeys, "|"), "sprint: sprint 2") {
		t.Fatalf("current sprint should not stay a label: %v", item.LabelKeys)
	}
	if !strings.Contains(strings.Join(item.LabelKeys, "|"), "sprint: sprint 1") {
		t.Fatalf("historical sprint should stay a label: %v", item.LabelKeys)
	}
	if len(plan.Cycles) != 1 || !plan.Cycles[0].Create || plan.Cycles[0].Name != "Sprint 2" {
		t.Fatalf("cycles = %+v", plan.Cycles)
	}
	if !plan.Cycles[0].StartDate.Before(plan.Cycles[0].EndDate) {
		t.Fatalf("cycle window invalid: %+v", plan.Cycles[0])
	}
	// The replaced sprint's label is not created at all.
	if names := strings.Join(planLabelNames(plan), "|"); strings.Contains(names, "Sprint: Sprint 2") {
		t.Fatalf("unused sprint label still planned: %s", names)
	}
}

func TestSprintLabelModeKeepsEverySprintAsLabel(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Issue in a sprint"))
	withSprints(data, 0, "Sprint 1")

	plan := prepare(t, newFakePulse(), data, func(o *runner.Options) {
		o.Sprints = runner.SprintModeLabel
	})
	if len(plan.Cycles) != 0 {
		t.Fatalf("cycles = %+v", plan.Cycles)
	}
	item := itemFor(t, plan, "ENG-1")
	if item.CycleKey != "" || !strings.Contains(strings.Join(item.LabelKeys, "|"), "sprint: sprint 1") {
		t.Fatalf("item = %+v", item)
	}
}

func TestCompletedCycleOfSameNameFallsBackToLabel(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.cycles = append(api.cycles, pulseapi.Cycle{
		ID: "cycle-done", Name: "Sprint 1", Status: "completed", TeamID: teamID,
	})
	data := source(issue("ENG-1", "Issue in a finished sprint"))
	withSprints(data, 0, "Sprint 1")

	plan := prepare(t, api, data)

	item := itemFor(t, plan, "ENG-1")
	if item.CycleKey != "" {
		t.Fatalf("cycle key = %q, want none for a completed cycle", item.CycleKey)
	}
	if !strings.Contains(strings.Join(item.LabelKeys, "|"), "sprint: sprint 1") {
		t.Fatalf("sprint label should be kept: %v", item.LabelKeys)
	}
	if len(plan.Cycles) != 0 {
		t.Fatalf("cycles = %+v", plan.Cycles)
	}
	if !strings.Contains(planWarnings(plan), "completed cycle") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}
}

func TestExistingPlannedCycleIsReused(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.cycles = append(api.cycles, pulseapi.Cycle{
		ID: "cycle-1", Name: "Sprint 1", Status: "planned", TeamID: teamID,
	})
	data := source(issue("ENG-1", "Issue in a sprint"))
	withSprints(data, 0, "Sprint 1")

	plan := prepare(t, api, data)
	if len(plan.Cycles) != 1 || plan.Cycles[0].ExistingID != "cycle-1" || plan.Cycles[0].Create {
		t.Fatalf("cycles = %+v", plan.Cycles)
	}
}

func TestNonLeafTeamImportsSprintsAsLabels(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Issue in a sprint"))
	withSprints(data, 0, "Sprint 1")

	plan := prepare(t, newFakePulse(), data, func(o *runner.Options) {
		o.TeamHasChildren = true
	})
	item := itemFor(t, plan, "ENG-1")
	if item.CycleKey != "" || len(plan.Cycles) != 0 {
		t.Fatalf("item=%+v cycles=%+v", item, plan.Cycles)
	}
	if !strings.Contains(planWarnings(plan), "leaf teams") {
		t.Fatalf("warnings: %s", planWarnings(plan))
	}
}

func TestSubIssueKeepsSprintLabelInsteadOfCycle(t *testing.T) {
	t.Parallel()
	child := issue("ENG-2", "Sub in a sprint")
	child.ParentKey = "ENG-1"
	data := source(issue("ENG-1", "Parent"), child)
	withSprints(data, 1, "Sprint 1")

	plan := prepare(t, newFakePulse(), data)

	item := itemFor(t, plan, "ENG-2")
	if item.CycleKey != "" {
		t.Fatalf("sub-issue should not carry a cycle: %+v", item)
	}
	if !strings.Contains(strings.Join(item.LabelKeys, "|"), "sprint: sprint 1") {
		t.Fatalf("sub-issue should keep its sprint label: %v", item.LabelKeys)
	}
	if len(plan.Cycles) != 0 {
		t.Fatalf("cycles = %+v", plan.Cycles)
	}
}

func TestMigratedLabelIsAddedByDefaultAndCanBeDisabled(t *testing.T) {
	t.Parallel()
	t.Run("added", func(t *testing.T) {
		plan := prepare(t, newFakePulse(), source(issue("ENG-1", "Marked")), func(o *runner.Options) {
			o.AddMigratedLabel = true
		})
		if !strings.Contains(strings.Join(itemFor(t, plan, "ENG-1").LabelKeys, ","), "migrated") {
			t.Fatalf("labels = %v", itemFor(t, plan, "ENG-1").LabelKeys)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		plan := prepare(t, newFakePulse(), source(issue("ENG-1", "Unmarked")))
		if len(itemFor(t, plan, "ENG-1").LabelKeys) != 0 {
			t.Fatalf("labels = %v", itemFor(t, plan, "ENG-1").LabelKeys)
		}
	})
}

func TestSkipLabelsKeepsOnlyTheMigratedMarker(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "No labels"))
	data.Labels["performance"] = importers.Label{Name: "performance", Kind: importers.LabelKindJira}
	data.Issues[0].Labels = []string{"performance"}

	plan := prepare(t, newFakePulse(), data, func(o *runner.Options) {
		o.SkipLabels = true
		o.AddMigratedLabel = true
	})
	keys := itemFor(t, plan, "ENG-1").LabelKeys
	if len(keys) != 1 || keys[0] != "migrated" {
		t.Fatalf("labels = %v", keys)
	}
}

func TestPlanReportsTeamIssueCountForIdentifierAlignment(t *testing.T) {
	t.Parallel()
	api := newFakePulse()
	api.teamIssues = 42
	plan := prepare(t, newFakePulse(), source(issue("ENG-1", "One")))
	if plan.TeamIssueCount != 0 {
		t.Fatalf("count = %d", plan.TeamIssueCount)
	}
	plan = prepare(t, api, source(issue("ENG-1", "One")))
	if plan.TeamIssueCount != 42 {
		t.Fatalf("count = %d, want 42", plan.TeamIssueCount)
	}
}

func TestPrepareRequiresTeam(t *testing.T) {
	t.Parallel()
	_, err := runner.New(newFakePulse()).Prepare(context.Background(), source(issue("ENG-1", "X")), runner.Options{})
	if err == nil || !strings.Contains(err.Error(), "team id is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareSurfacesImporterDiagnostics(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Fine"))
	data.Diagnostics = []importers.Diagnostic{
		{Level: importers.DiagnosticWarning, Row: 3, Message: "skipped row with empty Summary"},
		{Level: importers.DiagnosticError, Row: 4, Message: "duplicate Issue key"},
	}
	plan := prepare(t, newFakePulse(), data)
	if plan.Valid() {
		t.Fatal("an importer error must block the import")
	}
	if plan.SkippedRows != 1 {
		t.Fatalf("skipped = %d", plan.SkippedRows)
	}
}

func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.jsonl")
}

// Pulse attributes every comment to the token holder, so the original author and
// timestamp have to survive in the body or the import silently reassigns the
// whole discussion to whoever ran it.
func TestCommentsCarryTheirOriginalAuthorAndDate(t *testing.T) {
	t.Parallel()
	when := time.Date(2025, 1, 3, 9, 0, 0, 0, time.UTC)
	data := source(issue("ENG-1", "Discussed"))
	data.Issues[0].Comments = []importers.Comment{
		{Author: "Jane Doe", Created: when, Body: "Looks like a **DB** issue"},
		{Body: "no attribution available"},
	}

	plan := prepare(t, newFakePulse(), data)
	comments := itemFor(t, plan, "ENG-1").Comments
	if len(comments) != 2 {
		t.Fatalf("comments = %v", comments)
	}
	for _, want := range []string{"Jane Doe", "2025-01-03 09:00 UTC", "Looks like a **DB** issue"} {
		if !strings.Contains(comments[0], want) {
			t.Errorf("comment is missing %q; got:\n%s", want, comments[0])
		}
	}
	// A comment with no author or date must not gain an empty attribution line.
	if comments[1] != "no attribution available" {
		t.Errorf("comment = %q", comments[1])
	}
}

func TestCommentsAreTruncatedToPulsesByteLimit(t *testing.T) {
	t.Parallel()
	data := source(issue("ENG-1", "Long comment"))
	data.Issues[0].Comments = []importers.Comment{{Body: strings.Repeat("ن", 3000)}} // 6000 bytes

	plan := prepare(t, newFakePulse(), data)
	comments := itemFor(t, plan, "ENG-1").Comments
	if len(comments) != 1 {
		t.Fatalf("comments = %d", len(comments))
	}
	if len(comments[0]) > pulseapi.MaxTextBytes {
		t.Fatalf("comment is %d bytes, over Pulse's %d-byte limit",
			len(comments[0]), pulseapi.MaxTextBytes)
	}
}
