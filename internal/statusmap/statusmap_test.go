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
		{"Medium", importers.PriorityMedium},
		{"Normal", importers.PriorityMedium},
		{"Low", importers.PriorityLow},
		{"Lowest", importers.PriorityNoPriority},
		{"Trivial", importers.PriorityNoPriority},
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
		{"Epic", importers.TypeTask, "Type: Epic"},
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
