package importers

import (
	"context"
	"time"
)

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

// Comment is a source-side comment. Pulse always attributes comments to the
// importing user, so Author/CreatedAt are preserved inside Body instead.
type Comment struct {
	Author  string
	Created time.Time
	Body    string
}

// Relation is a source-side issue link expressed between source keys.
type Relation struct {
	// Kind is "blocks" (this issue blocks Target) or "blocked_by".
	Kind      string
	TargetKey string
}

const (
	RelationBlocks    = "blocks"
	RelationBlockedBy = "blocked_by"
)

// SprintRef is one sprint the source issue passed through, paired with the key
// its "Sprint: …" label registered under so a cycle mapping can strip exactly
// that label.
type SprintRef struct {
	Name     string
	LabelKey string
}

type Issue struct {
	Key          string
	SourceRow    int
	RowHash      string
	Title        string
	BodyMarkdown string // Main Doc body; converted to Plate JSON via platemd
	Status       string
	// StatusOverride, when set, wins over Status. Used when a Jira resolution
	// proves the issue is finished even though its status name does not say so.
	StatusOverride string
	AssigneeID     string // source-side user key (name or email)
	AssigneeEmail  string
	Priority       IssuePriority
	Type           IssueType
	Labels         []string // keys into ImportResult.Labels

	// ParentKey is the source key of the issue this one hangs under
	// (Jira sub-task → parent story). Empty when top-level.
	ParentKey string
	// EpicKey is the source key of the epic this issue belongs to. Resolved to
	// a Pulse project when the epic was imported as one.
	EpicKey string
	// IsEpic marks a row that should become a Pulse project rather than an issue.
	IsEpic bool

	DueDate *time.Time
	// StoryPoints is the source point estimate (Jira story points).
	StoryPoints *float64
	// OriginalEstimateSeconds is Jira's time estimate, used for hour-scale teams.
	OriginalEstimateSeconds *int
	// CreatedAt is the source creation time, used to approximate cycle dates.
	CreatedAt *time.Time
	// UpdatedAt is the source last-updated time, used by staleness filters.
	UpdatedAt *time.Time
	// Sprints lists the sprints the issue passed through, oldest first; the
	// last entry is the sprint the issue currently sits in.
	Sprints []SprintRef

	Comments  []Comment
	Relations []Relation
}

// Project is a source container (Jira epic) that maps onto a Pulse project.
type Project struct {
	Key          string
	SourceRow    int
	RowHash      string
	Title        string
	BodyMarkdown string
	Status       string
	Labels       []string
}

type User struct {
	Name  string
	Email string
	// Rows counts how many source rows reference this user, so the mapping
	// step can put the busiest names first.
	Rows int
}

type Label struct {
	Name string
	// Kind groups labels by origin so a per-issue label cap can drop the least
	// valuable ones first.
	Kind LabelKind
}

// LabelKind orders labels by how much meaning they carry, most important first.
type LabelKind int

const (
	LabelKindMigrated LabelKind = iota
	LabelKindType
	LabelKindJira
	LabelKindComponent
	LabelKindFixVersion
	LabelKindAffectsVersion
	LabelKindEpic
	LabelKindSprint
)

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
	Projects          []Project
	Users             map[string]User
	Labels            map[string]Label
	Diagnostics       []Diagnostic
	SourcePath        string
	SourceURL         string
	SourceFingerprint string
	// StatusNames maps each Pulse status to the source status names that landed
	// on it, so the plan can show the mapping before anything is written.
	StatusNames map[string][]string
	// IgnoredColumns lists source columns the importer knowingly does not map.
	IgnoredColumns []string
}

type Importer interface {
	Name() string
	Import(context.Context) (*ImportResult, error)
}
