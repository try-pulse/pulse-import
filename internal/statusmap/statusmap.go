package statusmap

import (
	"strings"

	"github.com/try-pulse/pulse-import/internal/importers"
)

type PulseStatus string

const (
	Backlog    PulseStatus = "backlog"
	Todo       PulseStatus = "todo"
	InProgress PulseStatus = "in_progress"
	QA         PulseStatus = "qa"
	Release    PulseStatus = "release"
	Done       PulseStatus = "done"
)

// All returns every Pulse workflow status, in workflow order.
func All() []PulseStatus {
	return []PulseStatus{Backlog, Todo, InProgress, QA, Release, Done}
}

// Parse resolves a user-supplied status name (as accepted by --skip-status and
// --only-status) to a Pulse status.
func Parse(name string) (PulseStatus, bool) {
	n := normalize(name)
	for _, status := range All() {
		if normalize(string(status)) == n {
			return status, true
		}
	}
	return "", false
}

func Map(name string) PulseStatus {
	n := normalize(name)

	switch n {
	case "backlog", "open", "new":
		return Backlog
	case "to_do", "todo", "ready", "selected_for_development", "reopened":
		return Todo
	case "in_progress", "inprogress", "progress", "doing", "started", "active", "development", "in_development":
		return InProgress
	case "qa", "in_qa", "in_review", "review", "code_review", "testing", "in_testing", "peer_review":
		return QA
	case "release", "ready_for_release", "ready_to_release", "staging", "deploy", "deployed":
		return Release
	case "done", "closed", "resolved", "complete", "completed", "fixed", "shipped",
		"canceled", "cancelled", "won't_do", "wont_do", "rejected", "duplicate":
		return Done
	}

	switch {
	case strings.Contains(n, "progress"), strings.Contains(n, "doing"):
		return InProgress
	case strings.Contains(n, "review"), hasToken(n, "qa"), hasToken(n, "test"),
		hasToken(n, "testing"), hasToken(n, "qc"):
		return QA
	case hasToken(n, "done"), hasToken(n, "complete"), hasToken(n, "completed"),
		hasToken(n, "closed"), strings.Contains(n, "resolv"):
		return Done
	case strings.Contains(n, "backlog"):
		return Backlog
	default:
		return Backlog
	}
}

// resolutionIsOpen reports whether a Jira resolution value means "still open".
// Jira writes an explicit placeholder in some exports rather than leaving the
// cell empty, and those placeholders must not be read as "finished".
func resolutionIsOpen(resolution string) bool {
	switch normalize(resolution) {
	case "", "unresolved", "none", "_", "n/a", "na", "null":
		return true
	}
	return false
}

// MapWithResolution maps a Jira status, letting a resolution close the issue.
// A Jira issue carrying any resolution is finished, whatever its status column
// says — some workflows resolve without renaming the status.
func MapWithResolution(status, resolution string) (mapped PulseStatus, overridden bool) {
	mapped = Map(status)
	if mapped == Done || resolutionIsOpen(resolution) {
		return mapped, false
	}
	return Done, true
}

func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "_")
	return strings.ReplaceAll(s, "-", "_")
}

func hasToken(s, tok string) bool {
	if s == tok {
		return true
	}
	return strings.Contains(s, "_"+tok+"_") ||
		strings.HasPrefix(s, tok+"_") ||
		strings.HasSuffix(s, "_"+tok)
}

func MapPriority(input string) importers.IssuePriority {
	n := strings.ToLower(strings.TrimSpace(input))
	switch {
	case n == "":
		return importers.PriorityNoPriority
	case n == "highest" || n == "blocker" || n == "critical" ||
		strings.Contains(n, "highest") || strings.Contains(n, "urgent") || strings.Contains(n, "critical"):
		return importers.PriorityUrgent
	case n == "high" || n == "major" || strings.Contains(n, "high"):
		return importers.PriorityHigh
	case n == "medium" || n == "normal" || strings.Contains(n, "medium") || strings.Contains(n, "normal"):
		return importers.PriorityMedium
	// Jira ships five priorities against Pulse's four. Lowest and Trivial fold
	// into Low rather than no_priority so "deprioritised" stays distinguishable
	// from "never triaged" (an empty cell).
	case n == "low" || n == "minor" || n == "lowest" || n == "trivial" ||
		strings.Contains(n, "lowest") || strings.Contains(n, "low") || strings.Contains(n, "trivial"):
		return importers.PriorityLow
	default:
		return importers.PriorityNoPriority
	}
}

// IsEpicType reports whether a Jira issue type names an epic.
func IsEpicType(jiraType string) bool {
	return strings.EqualFold(strings.TrimSpace(jiraType), "epic")
}

// IsSubTaskType reports whether a Jira issue type names a sub-task.
func IsSubTaskType(jiraType string) bool {
	switch normalize(jiraType) {
	case "sub_task", "subtask", "sub_bug", "technical_task", "sub_task_bug":
		return true
	}
	return strings.HasPrefix(normalize(jiraType), "sub_")
}

func MapIssueType(jiraType string) (importers.IssueType, string) {
	t := strings.TrimSpace(jiraType)
	label := ""
	if t != "" {
		label = "Type: " + t
	}
	switch strings.ToLower(t) {
	case "bug", "defect", "fault":
		return importers.TypeBug, label
	case "story", "user story":
		return importers.TypeStory, label
	case "feature", "new feature", "improvement", "enhancement", "epic":
		return importers.TypeFeature, label
	case "task", "sub-task", "subtask":
		return importers.TypeTask, label
	default:
		return importers.TypeTask, label
	}
}
