package runner

import (
	"fmt"
	"time"

	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

// LabelPolicy decides what happens when a source issue carries more labels than
// Pulse's ten-per-issue ceiling.
type LabelPolicy string

const (
	// LabelPolicyDrop keeps the ten most meaningful labels and warns.
	LabelPolicyDrop LabelPolicy = "drop"
	// LabelPolicyError refuses the import instead of dropping anything.
	LabelPolicyError LabelPolicy = "error"
)

// SkipUser is the sentinel a user mapping uses to mean "import these issues
// unassigned" rather than "match this person".
const SkipUser = "skip"

type Options struct {
	ImporterID  string
	APIURL      string
	WorkspaceID string
	TeamID      string
	// TeamPath is the target team plus its ancestors. Pulse only accepts an
	// assignee who is a member of one of these teams.
	TeamPath []string
	// ProjectID pins every imported issue to one project, overriding the
	// epic-to-project mapping.
	ProjectID  string
	Assignee   AssigneeMode
	SelfUserID string
	// UserMap maps a folded source user key to a Pulse user id, or to SkipUser.
	UserMap map[string]string

	EstimateSettings pulseapi.EstimateSettings
	LabelPolicy      LabelPolicy
	AddMigratedLabel bool
	SkipLabels       bool
	SkipComments     bool
	SkipRelations    bool

	// SkipStatuses and OnlyStatuses filter which issues are imported at all.
	SkipStatuses map[statusmap.PulseStatus]bool
	OnlyStatuses map[statusmap.PulseStatus]bool
	// StaleAfter drops issues whose source Updated timestamp is older than this.
	StaleAfter time.Duration
	// Now anchors staleness so a plan is reproducible in tests.
	Now time.Time

	Concurrency int
}

type Diagnostic struct {
	Key     string
	Row     int
	Message string
}

type LabelPlan struct {
	Key        string
	Name       string
	ExistingID string
	// ArchivedID is set when the only label with this name on the team is
	// archived. Pulse's uniqueness index ignores the archived flag, so it has to
	// be unarchived rather than recreated.
	ArchivedID string
	Create     bool
}

// PreparedItem is one entity to create, fully resolved except for references to
// other items in the same plan, which only get ids at execution time.
type PreparedItem struct {
	Key     string
	Kind    importstate.Kind
	Row     int
	RowHash string
	Title   string

	Issue   pulseapi.CreateIssueRequest
	Project pulseapi.CreateProjectRequest

	LabelKeys []string
	PlateJSON []byte

	// ParentKey and EpicKey reference other items by source key.
	ParentKey string
	EpicKey   string

	Comments  []string
	Blocks    []string
	BlockedBy []string

	// Wave orders creation so a referenced parent or project always exists
	// first: 0 projects, 1 top-level issues, 2 sub-issues.
	Wave int
}

// NeedsLinkPass reports whether the item still owes a relations update once
// every item has an id.
func (i PreparedItem) NeedsLinkPass() bool {
	return len(i.Blocks) > 0 || len(i.BlockedBy) > 0
}

type Plan struct {
	Options           Options
	SourcePath        string
	SourceURL         string
	SourceFingerprint string
	Hash              string

	Items  []PreparedItem
	Labels []LabelPlan

	Warnings []Diagnostic
	Errors   []Diagnostic

	SkippedRows      int
	FilteredIssues   int
	DroppedLabels    int
	CommentCount     int
	RelationCount    int
	SubIssueCount    int
	ProjectCount     int
	EstimatesSet     int
	EstimatesDropped int

	// UserMapping records how each source user was resolved, for the review step.
	UserMapping []UserResolution
	// StatusMapping records source status names per Pulse status.
	StatusMapping map[string][]string
	// IgnoredColumns is what the source file carried but Pulse cannot hold.
	IgnoredColumns []string
	// TeamIssueCount is how many issues the target team already had.
	TeamIssueCount int64
}

// UserResolution is one row of the user-mapping review table.
type UserResolution struct {
	SourceName  string
	SourceEmail string
	Rows        int
	PulseUserID string
	PulseName   string
	// State is "matched", "unmatched", "ambiguous", "skipped" or "self".
	State string
	// Via records how a match was made: "email", "name", "manual" or "self".
	Via string
}

func (p *Plan) Valid() bool {
	return p != nil && len(p.Errors) == 0
}

func (p *Plan) MainDocCount() int {
	count := 0
	for _, item := range p.Items {
		if len(item.PlateJSON) > 0 {
			count++
		}
	}
	return count
}

func (p *Plan) IssueCount() int {
	count := 0
	for _, item := range p.Items {
		if item.Kind == importstate.KindIssue {
			count++
		}
	}
	return count
}

func (p *Plan) LabelsToCreate() int {
	count := 0
	for _, label := range p.Labels {
		if label.Create {
			count++
		}
	}
	return count
}

// LinkPassCount is how many issues still need a second write to apply their
// relations. Relations reference other imported issues, so they can only be set
// once every item has an id.
func (p *Plan) LinkPassCount() int {
	count := 0
	for _, item := range p.Items {
		if item.NeedsLinkPass() {
			count++
		}
	}
	return count
}

// TotalWrites is how many units of work the executor will report progress for:
// one per item, plus one per item that owes a link pass.
func (p *Plan) TotalWrites() int {
	return len(p.Items) + p.LinkPassCount()
}

func (p *Plan) LabelsToUnarchive() int {
	count := 0
	for _, label := range p.Labels {
		if label.ArchivedID != "" {
			count++
		}
	}
	return count
}

type Progress struct {
	Completed int
	Total     int
	Key       string
	Phase     string
}

type ExecuteOptions struct {
	StateFile       string
	ContinueOnError bool
	Adopt           map[string]string
	RetryUnknown    map[string]bool
	OnProgress      func(Progress)
}

type Result struct {
	CreatedIssues   int
	CreatedProjects int
	SkippedIssues   int
	FailedIssues    int
	CreatedMainDocs int
	FailedMainDocs  int
	CreatedComments int
	FailedComments  int
	LinkedIssues    int
	FailedLinks     int
	Warnings        []string
	Errors          []string
}

type PartialError struct {
	Failures int
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("import completed with %d failure(s)", e.Failures)
}

type UnknownOutcomeError struct {
	Key       string
	StateFile string
	Cause     error
}

func (e *UnknownOutcomeError) Error() string {
	return fmt.Sprintf(
		"outcome for %s is unknown; inspect Pulse, then use --adopt or --retry-unknown (state: %s): %v",
		e.Key, e.StateFile, e.Cause,
	)
}

func (e *UnknownOutcomeError) Unwrap() error {
	return e.Cause
}

// PermissionError wraps a 403 with the concrete Pulse permission the caller is
// missing and a way forward, so the CLI never surfaces a bare "pulse api 403".
type PermissionError struct {
	Action     string
	Permission string
	Remedy     string
	Cause      error
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("%s requires the %q permission in Pulse. %s (cause: %v)",
		e.Action, e.Permission, e.Remedy, e.Cause)
}

func (e *PermissionError) Unwrap() error {
	return e.Cause
}
