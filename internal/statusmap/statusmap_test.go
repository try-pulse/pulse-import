package statusmap_test

import (
	"testing"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

func TestMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want statusmap.PulseStatus
	}{
		{"Backlog", statusmap.Backlog},
		{"open", statusmap.Backlog},
		{"New", statusmap.Backlog},
		{"To Do", statusmap.Todo},
		{"todo", statusmap.Todo},
		{"Selected for Development", statusmap.Todo},
		{"In Progress", statusmap.InProgress},
		{"Doing", statusmap.InProgress},
		{"In Review", statusmap.QA},
		{"Code Review", statusmap.QA},
		{"QA", statusmap.QA},
		{"Ready for Release", statusmap.Release},
		{"Done", statusmap.Done},
		{"Resolved", statusmap.Done},
		{"Closed", statusmap.Done},
		{"Canceled", statusmap.Done},
		{"won't do", statusmap.Done},
		{"", statusmap.Backlog},
		{"Completely Unknown XYZ", statusmap.Backlog},
		{"still-in-progress-phase", statusmap.InProgress},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := statusmap.Map(tt.in); got != tt.want {
				t.Fatalf("Map(%q)=%q want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMapPriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want importers.IssuePriority
	}{
		{"Highest", importers.PriorityUrgent},
		{"Blocker", importers.PriorityUrgent},
		{"Critical", importers.PriorityUrgent},
		{"High", importers.PriorityHigh},
		{"Major", importers.PriorityHigh},
		{"Medium", importers.PriorityMedium},
		{"Normal", importers.PriorityMedium},
		{"Low", importers.PriorityLow},
		{"Minor", importers.PriorityLow},
		// Jira has five priorities against Pulse's four. Lowest and Trivial mean
		// "deliberately deprioritised", which is not the same as an empty cell.
		{"Lowest", importers.PriorityLow},
		{"Trivial", importers.PriorityLow},
		{"", importers.PriorityNoPriority},
		{"something urgent here", importers.PriorityUrgent},
		{"P2-high-ish", importers.PriorityHigh},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := statusmap.MapPriority(tt.in); got != tt.want {
				t.Fatalf("MapPriority(%q)=%q want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMapIssueType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		wantTyp importers.IssueType
		wantLbl string
	}{
		{"Bug", importers.TypeBug, "Type: Bug"},
		{"Defect", importers.TypeBug, "Type: Defect"},
		{"Story", importers.TypeStory, "Type: Story"},
		{"User Story", importers.TypeStory, "Type: User Story"},
		{"Feature", importers.TypeFeature, "Type: Feature"},
		{"Enhancement", importers.TypeFeature, "Type: Enhancement"},
		{"Task", importers.TypeTask, "Type: Task"},
		{"Sub-task", importers.TypeTask, "Type: Sub-task"},
		{"Epic", importers.TypeFeature, "Type: Epic"},
		{"", importers.TypeTask, ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			typ, lbl := statusmap.MapIssueType(tt.in)
			if typ != tt.wantTyp || lbl != tt.wantLbl {
				t.Fatalf("got %q %q want %q %q", typ, lbl, tt.wantTyp, tt.wantLbl)
			}
		})
	}
}

func TestMapWithResolution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		status         string
		resolution     string
		want           statusmap.PulseStatus
		wantOverridden bool
	}{
		{
			name:   "open with no resolution stays open",
			status: "Open", want: statusmap.Backlog,
		},
		{
			// Some Jira workflows resolve an issue without renaming its status,
			// so a resolution is the stronger signal.
			name:   "resolution closes an open status",
			status: "Open", resolution: "Won't Do",
			want: statusmap.Done, wantOverridden: true,
		},
		{
			name:   "done stays done without an override",
			status: "Done", resolution: "Done",
			want: statusmap.Done,
		},
		{
			// Jira writes an explicit placeholder in some exports; it must not be
			// read as "finished".
			name:   "Unresolved placeholder is not a resolution",
			status: "In Progress", resolution: "Unresolved",
			want: statusmap.InProgress,
		},
		{
			name:   "dash placeholder is not a resolution",
			status: "To Do", resolution: "-",
			want: statusmap.Todo,
		},
		{
			name:   "duplicate resolution closes the issue",
			status: "In Review", resolution: "Duplicate",
			want: statusmap.Done, wantOverridden: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, overridden := statusmap.MapWithResolution(tt.status, tt.resolution)
			if got != tt.want || overridden != tt.wantOverridden {
				t.Fatalf("MapWithResolution(%q, %q) = (%q, %v), want (%q, %v)",
					tt.status, tt.resolution, got, overridden, tt.want, tt.wantOverridden)
			}
		})
	}
}

func TestParseAndAll(t *testing.T) {
	t.Parallel()
	if len(statusmap.All()) != 6 {
		t.Fatalf("Pulse has six workflow statuses, got %d", len(statusmap.All()))
	}
	for _, status := range statusmap.All() {
		got, ok := statusmap.Parse(string(status))
		if !ok || got != status {
			t.Errorf("Parse(%q) = (%q, %v)", status, got, ok)
		}
	}
	// The CLI accepts the human spelling as well as the wire value.
	if got, ok := statusmap.Parse("In Progress"); !ok || got != statusmap.InProgress {
		t.Errorf("Parse(\"In Progress\") = (%q, %v)", got, ok)
	}
	if _, ok := statusmap.Parse("shipped"); ok {
		t.Error("Parse must reject a status Pulse does not have")
	}
}

func TestIssueTypeClassification(t *testing.T) {
	t.Parallel()
	for _, epic := range []string{"Epic", "epic", " EPIC "} {
		if !statusmap.IsEpicType(epic) {
			t.Errorf("IsEpicType(%q) = false", epic)
		}
	}
	if statusmap.IsEpicType("Story") {
		t.Error("IsEpicType(\"Story\") = true")
	}
	for _, sub := range []string{"Sub-task", "Subtask", "sub_task", "Sub-bug", "Technical task"} {
		if !statusmap.IsSubTaskType(sub) {
			t.Errorf("IsSubTaskType(%q) = false", sub)
		}
	}
	for _, top := range []string{"Task", "Story", "Bug", "Epic"} {
		if statusmap.IsSubTaskType(top) {
			t.Errorf("IsSubTaskType(%q) = true", top)
		}
	}
}

// "Latest" contains "test" but is not a QA status; token matching is what keeps
// substring collisions out of the mapping.
func TestStatusSubstringCollisions(t *testing.T) {
	t.Parallel()
	cases := map[string]statusmap.PulseStatus{
		"Latest":             statusmap.Backlog,
		"Ready for Testing":  statusmap.QA,
		"In Test":            statusmap.QA,
		"Contested":          statusmap.Backlog,
		"Closed - Won't Fix": statusmap.Done,
		"Abandoned":          statusmap.Backlog,
	}
	for name, want := range cases {
		if got := statusmap.Map(name); got != want {
			t.Errorf("Map(%q) = %q, want %q", name, got, want)
		}
	}
}
