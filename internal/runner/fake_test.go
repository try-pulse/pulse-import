package runner_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

// fakePulse is an in-memory stand-in for the Pulse API that enforces the same
// rules the real server does, so a test that passes here would pass against
// pulse-api. The constraints reproduced on purpose:
//
//   - title and label length are counted in BYTES (len(string)), which is how
//     pulse-api validates them, not in runes;
//   - an assignee must be a member of the issue's team or a parent team;
//   - label names are unique per (team, entity_type) regardless of the archived
//     flag, so an archived label blocks creating a live one with the same name;
//   - a parent issue must exist, belong to the same team, and not itself be a
//     sub-task;
//   - at most ten labels per issue.
type fakePulse struct {
	mu sync.Mutex

	teamMembers map[string][]pulseapi.TeamMember
	labels      []pulseapi.Label
	cycles      []pulseapi.Cycle
	issues      map[string]*pulseapi.Issue
	projects    map[string]*pulseapi.Project
	mainDocs    map[string]string
	comments    map[string][]string
	teamIssues  int64
	nextID      int

	// Hooks inject faults. They run before the in-memory write.
	createIssueHook   func(pulseapi.CreateIssueRequest) (*pulseapi.Issue, error)
	uploadHook        func(entityType, entityID string) (*pulseapi.Document, error)
	commentHook       func(issueID, text string) (*pulseapi.Comment, error)
	createLabelHook   func(name string) (*pulseapi.Label, error)
	createCycleHook   func(pulseapi.CreateCycleRequest) (*pulseapi.Cycle, error)
	listCyclesHook    func(teamID string) ([]pulseapi.Cycle, error)
	unarchiveHook     func(labelID string) (*pulseapi.Label, error)
	listMembersHook   func(teamID string) ([]pulseapi.TeamMember, error)
	updateIssueHook   func(issueID string) (*pulseapi.Issue, error)
	countCommentsHook func(issueID string) (int64, error)

	calls map[string]int
}

func newFakePulse() *fakePulse {
	return &fakePulse{
		teamMembers: map[string][]pulseapi.TeamMember{},
		issues:      map[string]*pulseapi.Issue{},
		projects:    map[string]*pulseapi.Project{},
		mainDocs:    map[string]string{},
		comments:    map[string][]string{},
		calls:       map[string]int{},
	}
}

func apiError(status int, code, message string) error {
	return &pulseapi.APIError{Status: status, Code: code, Message: message}
}

func (f *fakePulse) record(name string) {
	f.calls[name]++
}

func (f *fakePulse) callCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakePulse) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakePulse) withMembers(teamID string, members ...pulseapi.TeamMember) *fakePulse {
	f.teamMembers[teamID] = append(f.teamMembers[teamID], members...)
	return f
}

func (f *fakePulse) ListTeamMembers(_ context.Context, teamID string) ([]pulseapi.TeamMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListTeamMembers")
	if f.listMembersHook != nil {
		return f.listMembersHook(teamID)
	}
	return f.teamMembers[teamID], nil
}

func (f *fakePulse) isMember(userID string) bool {
	for _, members := range f.teamMembers {
		for _, member := range members {
			if member.ID == userID {
				return true
			}
		}
	}
	return false
}

func (f *fakePulse) ListLabels(_ context.Context, teamID string) ([]pulseapi.Label, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListLabels")
	var out []pulseapi.Label
	for _, label := range f.labels {
		if label.TeamID == teamID && !label.Archived {
			out = append(out, label)
		}
	}
	return out, nil
}

func (f *fakePulse) ListArchivedLabels(_ context.Context, teamID string) ([]pulseapi.Label, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListArchivedLabels")
	var out []pulseapi.Label
	for _, label := range f.labels {
		if label.TeamID == teamID && label.Archived {
			out = append(out, label)
		}
	}
	return out, nil
}

func (f *fakePulse) UnarchiveLabel(_ context.Context, labelID string) (*pulseapi.Label, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("UnarchiveLabel")
	if f.unarchiveHook != nil {
		return f.unarchiveHook(labelID)
	}
	for index := range f.labels {
		if f.labels[index].ID == labelID {
			f.labels[index].Archived = false
			copied := f.labels[index]
			return &copied, nil
		}
	}
	return nil, apiError(http.StatusNotFound, "NOT_FOUND", "label not found")
}

func (f *fakePulse) CreateLabel(_ context.Context, teamID string, req pulseapi.CreateLabelRequest) (*pulseapi.Label, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateLabel")
	if f.createLabelHook != nil {
		if label, err := f.createLabelHook(req.Name); err != nil {
			return label, err
		}
	}
	if len(req.Name) > pulseapi.MaxLabelBytes {
		return nil, apiError(http.StatusUnprocessableEntity, "VALIDATION",
			"label name exceeds 50 characters")
	}
	// Uniqueness spans archived labels, exactly as the Mongo index does.
	for _, label := range f.labels {
		if label.TeamID == teamID && label.EntityType == req.EntityType &&
			strings.EqualFold(label.Name, req.Name) {
			return nil, apiError(http.StatusConflict, "DUPLICATE_NAME",
				"a label with this name already exists in this team and entity type")
		}
	}
	label := pulseapi.Label{
		ID: f.id("label"), TeamID: teamID, Name: req.Name,
		EntityType: req.EntityType, Color: req.Color,
	}
	f.labels = append(f.labels, label)
	return &label, nil
}

func (f *fakePulse) CreateIssue(_ context.Context, req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateIssue")
	if f.createIssueHook != nil {
		if issue, err := f.createIssueHook(req); err != nil {
			return issue, err
		}
	}
	if len(req.Title) > pulseapi.MaxTitleBytes {
		return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE",
			"Issue title must be less than 200 characters")
	}
	if len([]rune(strings.TrimSpace(req.Title))) < 2 {
		return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE", "Issue title is required")
	}
	if len(req.LabelIDs) > 10 {
		return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE", "Issue has more than 10 labels")
	}
	if req.AssigneeID != nil && *req.AssigneeID != "" && !f.isMember(*req.AssigneeID) {
		return nil, apiError(http.StatusBadRequest, "INVALID_ASSIGNEE",
			"Assignee must be a member of the issue's team or one of its parent teams")
	}
	if req.ParentID != nil && *req.ParentID != "" {
		parent, ok := f.issues[*req.ParentID]
		if !ok {
			return nil, apiError(http.StatusNotFound, "ISSUE_NOT_FOUND", "Parent issue not found")
		}
		if parent.TeamID != req.TeamID {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE",
				"Parent issue must belong to the same team")
		}
		if parent.ParentID != nil && *parent.ParentID != "" {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE",
				"Parent issue cannot be a sub-task (nested hierarchy not supported)")
		}
	}
	if req.ProjectID != nil && *req.ProjectID != "" {
		project, ok := f.projects[*req.ProjectID]
		if !ok {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE", "Project not found")
		}
		if project.TeamID != req.TeamID {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE",
				"Project must belong to the same team as the issue")
		}
	}

	if req.CycleID != nil && *req.CycleID != "" {
		cycle := f.cycleByID(*req.CycleID)
		if cycle == nil {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE", "Cycle not found")
		}
		if cycle.TeamID != req.TeamID {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE",
				"Cycle must belong to the same team as the issue")
		}
		if cycle.Status == "completed" {
			return nil, apiError(http.StatusBadRequest, "INVALID_ISSUE",
				"Cannot assign issue to a completed cycle")
		}
	}

	issue := &pulseapi.Issue{
		ID: f.id("issue"), Title: req.Title, TeamID: req.TeamID,
		Status: req.Status, Priority: req.Priority, Type: req.Type,
		ProjectID: req.ProjectID, ParentID: req.ParentID, AssigneeID: req.AssigneeID,
		CycleID: req.CycleID,
	}
	f.issues[issue.ID] = issue
	return issue, nil
}

func (f *fakePulse) cycleByID(id string) *pulseapi.Cycle {
	for i := range f.cycles {
		if f.cycles[i].ID == id {
			return &f.cycles[i]
		}
	}
	return nil
}

func (f *fakePulse) ListTeamCycles(_ context.Context, teamID string) ([]pulseapi.Cycle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListTeamCycles")
	if f.listCyclesHook != nil {
		return f.listCyclesHook(teamID)
	}
	var out []pulseapi.Cycle
	for _, cycle := range f.cycles {
		if cycle.TeamID == teamID {
			out = append(out, cycle)
		}
	}
	return out, nil
}

func (f *fakePulse) CreateCycle(_ context.Context, req pulseapi.CreateCycleRequest) (*pulseapi.Cycle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateCycle")
	if f.createCycleHook != nil {
		if cycle, err := f.createCycleHook(req); err != nil {
			return cycle, err
		}
	}
	if len([]rune(strings.TrimSpace(req.Name))) < 2 || len(req.Name) > 200 {
		return nil, apiError(http.StatusBadRequest, "INVALID_CYCLE", "Cycle name out of range")
	}
	if req.StartDate.IsZero() || req.EndDate.IsZero() || !req.StartDate.Before(req.EndDate) {
		return nil, apiError(http.StatusBadRequest, "INVALID_CYCLE",
			"Start date must be before end date")
	}
	switch req.Status {
	case "planned", "active", "completed":
	default:
		return nil, apiError(http.StatusBadRequest, "INVALID_CYCLE", "Unknown cycle status")
	}
	if req.TeamID == "" {
		return nil, apiError(http.StatusBadRequest, "INVALID_CYCLE", "Cycle team is required")
	}
	cycle := pulseapi.Cycle{
		ID: f.id("cycle"), Name: req.Name, Status: req.Status,
		TeamID: req.TeamID, StartDate: req.StartDate, EndDate: req.EndDate,
	}
	f.cycles = append(f.cycles, cycle)
	return &cycle, nil
}

func (f *fakePulse) UpdateIssue(_ context.Context, issueID string, req pulseapi.UpdateIssueRequest) (*pulseapi.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("UpdateIssue")
	if f.updateIssueHook != nil {
		if issue, err := f.updateIssueHook(issueID); err != nil {
			return issue, err
		}
	}
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, apiError(http.StatusNotFound, "ISSUE_NOT_FOUND", "Issue not found")
	}
	if req.BlocksIDs != nil {
		issue.BlocksIDs = *req.BlocksIDs
	}
	if req.BlockedByIDs != nil {
		issue.BlockedByIDs = *req.BlockedByIDs
	}
	copied := *issue
	return &copied, nil
}

func (f *fakePulse) GetIssue(_ context.Context, issueID string) (*pulseapi.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetIssue")
	issue, ok := f.issues[issueID]
	if !ok {
		return nil, apiError(http.StatusNotFound, "ISSUE_NOT_FOUND", "Issue not found")
	}
	copied := *issue
	if docID := f.mainDocs[issueID]; docID != "" {
		copied.MainDocID = &docID
	}
	return &copied, nil
}

func (f *fakePulse) CreateProject(_ context.Context, req pulseapi.CreateProjectRequest) (*pulseapi.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateProject")
	if len(req.Title) > pulseapi.MaxTitleBytes {
		return nil, apiError(http.StatusBadRequest, "INVALID_PROJECT",
			"Project title must be less than 200 characters")
	}
	project := &pulseapi.Project{ID: f.id("project"), Title: req.Title, TeamID: req.TeamID}
	f.projects[project.ID] = project
	return project, nil
}

func (f *fakePulse) GetProject(_ context.Context, projectID string) (*pulseapi.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("GetProject")
	project, ok := f.projects[projectID]
	if !ok {
		return nil, apiError(http.StatusNotFound, "NOT_FOUND", "Project not found")
	}
	copied := *project
	if docID := f.mainDocs[projectID]; docID != "" {
		copied.MainDocID = &docID
	}
	return &copied, nil
}

func (f *fakePulse) UploadMainDoc(
	_ context.Context, entityType, entityID, title string, _ []byte,
) (*pulseapi.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("UploadMainDoc")
	if f.uploadHook != nil {
		if doc, err := f.uploadHook(entityType, entityID); err != nil {
			return doc, err
		}
	}
	if len(title) > pulseapi.MaxTitleBytes {
		return nil, apiError(http.StatusBadRequest, "INVALID_REQUEST", "title too long")
	}
	docID := f.id("doc")
	f.mainDocs[entityID] = docID
	return &pulseapi.Document{ID: docID, Title: title}, nil
}

func (f *fakePulse) CreateComment(_ context.Context, req pulseapi.CreateCommentRequest) (*pulseapi.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateComment")
	if f.commentHook != nil {
		if comment, err := f.commentHook(req.TargetID, req.Text); err != nil {
			return comment, err
		}
	}
	if len([]rune(req.Text)) > 4000 || strings.TrimSpace(req.Text) == "" {
		return nil, apiError(http.StatusBadRequest, "INVALID_REQUEST", "text out of range")
	}
	f.comments[req.TargetID] = append(f.comments[req.TargetID], req.Text)
	return &pulseapi.Comment{ID: f.id("comment")}, nil
}

func (f *fakePulse) CountComments(_ context.Context, issueID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CountComments")
	if f.countCommentsHook != nil {
		return f.countCommentsHook(issueID)
	}
	return int64(len(f.comments[issueID])), nil
}

func (f *fakePulse) CountTeamIssues(context.Context, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CountTeamIssues")
	return f.teamIssues, nil
}

func (f *fakePulse) issueByTitle(title string) *pulseapi.Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, issue := range f.issues {
		if issue.Title == title {
			copied := *issue
			return &copied
		}
	}
	return nil
}

func (f *fakePulse) commentsFor(issueID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.comments[issueID]...)
}

// createOrder returns the issue titles in the order they were created, which is
// what determines whether Pulse identifiers line up with the source keys.
func (f *fakePulse) createOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ordered := make([]string, 0, len(f.issues))
	for index := 1; index <= f.nextID; index++ {
		if issue, ok := f.issues[fmt.Sprintf("issue-%d", index)]; ok {
			ordered = append(ordered, issue.Title)
		}
	}
	return ordered
}
