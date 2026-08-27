package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/try-pulse/pulse-import/internal/importers/jiracsv"
	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

type Options struct {
	APIURL    string
	Workspace string
	Importer  string
	File      string
	Team      string
	Project   string
	JiraURL   string

	Assignee   string
	SelfAssign bool
	MapUser    []string

	Epics         string
	Sprints       string
	SkipComments  bool
	SkipLabels    bool
	SkipRelations bool
	StrictLabels  bool
	NoMigrated    bool

	SkipStatus []string
	OnlyStatus []string
	SkipStale  string

	Concurrency int

	DryRun   bool
	Continue bool
	NoPrompt bool

	StateFile    string
	Adopt        []string
	RetryUnknown []string
}

// assigneeMode resolves the assignee strategy from flags, falling back to a
// prompt when the run is interactive and nothing was specified.
func assigneeMode(opts Options, p Prompter) (runner.AssigneeMode, error) {
	if opts.SelfAssign && opts.Assignee != "" &&
		!strings.EqualFold(opts.Assignee, string(runner.AssigneeSelf)) {
		return "", fmt.Errorf("--self-assign conflicts with --assignee %s", opts.Assignee)
	}
	if opts.SelfAssign {
		return runner.AssigneeSelf, nil
	}
	if opts.Assignee != "" {
		switch strings.ToLower(strings.TrimSpace(opts.Assignee)) {
		case string(runner.AssigneeSelf):
			return runner.AssigneeSelf, nil
		case string(runner.AssigneeMapped):
			return runner.AssigneeMapped, nil
		case string(runner.AssigneeNone):
			return runner.AssigneeNone, nil
		default:
			return "", fmt.Errorf("--assignee must be one of self, mapped, none (got %q)", opts.Assignee)
		}
	}
	if opts.NoPrompt {
		return runner.AssigneeMapped, nil
	}
	value, err := p.Select("Assignee strategy", assigneeOptions())
	if err != nil {
		return "", err
	}
	return runner.AssigneeMode(value), nil
}

func epicMode(opts Options) (jiracsv.EpicMode, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Epics)) {
	case "", string(jiracsv.EpicModeProject):
		return jiracsv.EpicModeProject, nil
	case string(jiracsv.EpicModeLabel):
		return jiracsv.EpicModeLabel, nil
	default:
		return "", fmt.Errorf("--epics must be one of project, label (got %q)", opts.Epics)
	}
}

func sprintMode(opts Options) (runner.SprintMode, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Sprints)) {
	case "", string(runner.SprintModeCycle):
		return runner.SprintModeCycle, nil
	case string(runner.SprintModeLabel):
		return runner.SprintModeLabel, nil
	default:
		return "", fmt.Errorf("--sprints must be one of cycle, label (got %q)", opts.Sprints)
	}
}

// parseStatusFilter turns a comma or repeat separated list of Pulse status names
// into a set, rejecting names Pulse does not have.
func parseStatusFilter(values []string, flagName string) (map[statusmap.PulseStatus]bool, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := map[statusmap.PulseStatus]bool{}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			status, ok := statusmap.Parse(part)
			if !ok {
				return nil, fmt.Errorf(
					"%s: %q is not a Pulse status (valid: %s)",
					flagName, part, strings.Join(statusNames(), ", "),
				)
			}
			out[status] = true
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func statusNames() []string {
	all := statusmap.All()
	names := make([]string, len(all))
	for i, status := range all {
		names[i] = string(status)
	}
	return names
}

// parseStale accepts a bare day count ("180") or a Go duration ("4320h").
func parseStale(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration < 0 {
			return 0, fmt.Errorf("--skip-stale must not be negative")
		}
		return duration, nil
	}
	// strconv rather than Sscanf: Sscanf would accept "180days" by reading 180
	// and silently discarding the rest.
	days, err := strconv.Atoi(value)
	if err != nil || days < 0 {
		return 0, fmt.Errorf(
			"--skip-stale expects a number of days or a duration like 4320h (got %q)", value)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

// parseUserMap parses --map-user "Jane Doe=<id|email|skip>".
func parseUserMap(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, value := range values {
		source, target, ok := strings.Cut(value, "=")
		source, target = strings.TrimSpace(source), strings.TrimSpace(target)
		if !ok || source == "" || target == "" {
			return nil, fmt.Errorf(
				"--map-user expects \"SOURCE NAME=PULSE-USER-ID\" (or =%s), got %q", runner.SkipUser, value,
			)
		}
		folded := strings.ToLower(source)
		if previous, exists := out[folded]; exists && previous != target {
			return nil, fmt.Errorf("--map-user maps %q twice (%q and %q)", source, previous, target)
		}
		out[folded] = target
	}
	return out, nil
}

func parseKeyValues(values []string, flagName string) (map[string]string, error) {
	out := map[string]string{}
	for _, value := range values {
		key, target, ok := strings.Cut(value, "=")
		key, target = strings.TrimSpace(key), strings.TrimSpace(target)
		if !ok || key == "" || target == "" {
			return nil, fmt.Errorf("%s expects JIRA-KEY=PULSE-ID, got %q", flagName, value)
		}
		folded := strings.ToLower(key)
		if _, exists := out[folded]; exists {
			return nil, fmt.Errorf("%s repeats Jira key %q", flagName, key)
		}
		out[folded] = target
	}
	return out, nil
}

func parseKeys(values []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			return nil, fmt.Errorf("--retry-unknown requires a Jira key")
		}
		if out[key] {
			return nil, fmt.Errorf("--retry-unknown repeats Jira key %q", value)
		}
		out[key] = true
	}
	return out, nil
}

func labelPolicy(opts Options) runner.LabelPolicy {
	if opts.StrictLabels {
		return runner.LabelPolicyError
	}
	return runner.LabelPolicyDrop
}
