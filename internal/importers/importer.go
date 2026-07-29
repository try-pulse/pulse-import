package importers

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
	Title        string
	BodyMarkdown string // Main Doc body; converted to Plate JSON via platemd
	Status       string
	AssigneeID   string // source-side user key (name or email)
	Priority     IssuePriority
	Type         IssueType
	Labels       []string // keys into ImportResult.Labels
	URL          string
	Estimate     *int // hours; unused in v1 Jira CSV
}

type User struct {
	Name      string
	Email     string
	AvatarURL string
}

type Label struct {
	Name        string
	Color       string
	Description string
}

type StatusMeta struct {
	Name  string
	Color string
}

type ImportResult struct {
	Issues   []Issue
	Users    map[string]User
	Labels   map[string]Label
	Statuses map[string]StatusMeta
}

type Importer interface {
	Name() string
	DefaultTeamName() string
	Import() (*ImportResult, error)
}
