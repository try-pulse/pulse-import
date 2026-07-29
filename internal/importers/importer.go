package importers

import "context"

type IssuePriority string

const (
	PriorityNoPriority IssuePriority = "no_priority"
	PriorityUrgent     IssuePriority = "urgent"
	PriorityHigh       IssuePriority = "high"
	PriorityMedium     IssuePriority = "medium"
	PriorityLow        IssuePriority = "low"
)

type IssueType string

const (
	TypeBug     IssueType = "bug"
	TypeFeature IssueType = "feature"
	TypeTask    IssueType = "task"
	TypeStory   IssueType = "story"
)

type Issue struct {
	Key           string
	SourceRow     int
	RowHash       string
	Title         string
	BodyMarkdown  string // Main Doc body; converted to Plate JSON via platemd
	Status        string
	AssigneeID    string // source-side user key (name or email)
	AssigneeEmail string
	Priority      IssuePriority
	Type          IssueType
	Labels        []string // keys into ImportResult.Labels
}

type User struct {
	Name  string
	Email string
}

type Label struct {
	Name string
}

type DiagnosticLevel string

const (
	DiagnosticWarning DiagnosticLevel = "warning"
	DiagnosticError   DiagnosticLevel = "error"
)

type Diagnostic struct {
	Level   DiagnosticLevel
	Row     int
	Message string
}

type ImportResult struct {
	Issues            []Issue
	Users             map[string]User
	Labels            map[string]Label
	Diagnostics       []Diagnostic
	SourcePath        string
	SourceURL         string
	SourceFingerprint string
}

type Importer interface {
	Name() string
	Import(context.Context) (*ImportResult, error)
}
