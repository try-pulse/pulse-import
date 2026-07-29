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

func Map(name string) PulseStatus {
	n := normalize(name)

	switch n {
	case "backlog", "open", "new":
		return Backlog
	case "to_do", "todo", "ready", "selected_for_development":
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
	case strings.Contains(n, "review"), strings.Contains(n, "qa"), strings.Contains(n, "test"):
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
	case n == "" || n == "lowest" || n == "trivial":
		return importers.PriorityNoPriority
	case n == "highest" || n == "blocker" || n == "critical" ||
		strings.Contains(n, "highest") || strings.Contains(n, "urgent") || strings.Contains(n, "critical"):
		return importers.PriorityUrgent
	case n == "high" || strings.Contains(n, "high"):
		return importers.PriorityHigh
	case n == "medium" || n == "normal" || strings.Contains(n, "medium") || strings.Contains(n, "normal"):
		return importers.PriorityMedium
	case n == "low" || strings.Contains(n, "low"):
		return importers.PriorityLow
	default:
		return importers.PriorityNoPriority
	}
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
	case "feature", "new feature", "improvement", "enhancement":
		return importers.TypeFeature, label
	case "task", "sub-task", "subtask":
		return importers.TypeTask, label
	default:
		return importers.TypeTask, label
	}
}
