package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

const diagnosticLimit = 20

// printPlan is the review step: everything that is about to be written, plus
// every mapping decision that was made on the user's behalf, shown before the
// confirmation prompt so a wrong mapping can be caught while it is still free.
func printPlan(out io.Writer, plan *runner.Plan, dryRun bool) {
	heading := "Import plan"
	if dryRun {
		heading = "Dry-run plan"
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", heading)

	_, _ = fmt.Fprintf(out, "  Target: workspace=%s team=%s", plan.Options.WorkspaceID, plan.Options.TeamID)
	if plan.Options.ProjectID != "" {
		_, _ = fmt.Fprintf(out, " project=%s", plan.Options.ProjectID)
	}
	_, _ = fmt.Fprintln(out)

	_, _ = fmt.Fprintf(out, "  Issues: %d (%d sub-issues) · Projects from epics: %d · Main Docs: %d\n",
		plan.IssueCount(), plan.SubIssueCount, plan.ProjectCount, plan.MainDocCount())
	_, _ = fmt.Fprintf(out, "  Comments: %d · Issue links: %d · Estimates: %d\n",
		plan.CommentCount, plan.RelationCount, plan.EstimatesSet)
	_, _ = fmt.Fprintf(out, "  Labels: %d create · %d reuse", plan.LabelsToCreate(),
		len(plan.Labels)-plan.LabelsToCreate()-plan.LabelsToUnarchive())
	if plan.LabelsToUnarchive() > 0 {
		_, _ = fmt.Fprintf(out, " · %d unarchive", plan.LabelsToUnarchive())
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  Rows skipped: %d · Filtered out: %d\n", plan.SkippedRows, plan.FilteredIssues)

	printStatusMapping(out, plan)
	printUserMapping(out, plan)
	printIdentifierNote(out, plan)
	printIgnoredColumns(out, plan)
	printDiagnostics(out, plan)
}

// printStatusMapping shows the best-effort status conversion. Pulse has six
// workflow states and Jira has as many as the project defines, so this is the
// mapping most likely to need a second look.
func printStatusMapping(out io.Writer, plan *runner.Plan) {
	if len(plan.StatusMapping) == 0 {
		return
	}
	_, _ = fmt.Fprintln(out, "\n  Status mapping (source → Pulse)")
	for _, status := range statusmap.All() {
		names := plan.StatusMapping[string(status)]
		if len(names) == 0 {
			continue
		}
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		_, _ = fmt.Fprintf(out, "    %-12s ← %s\n", status, strings.Join(sorted, ", "))
	}
}

func printUserMapping(out io.Writer, plan *runner.Plan) {
	if len(plan.UserMapping) == 0 {
		return
	}
	if plan.Options.Assignee == runner.AssigneeNone {
		_, _ = fmt.Fprintf(out, "\n  Assignees: leaving all %d imported issues unassigned\n", plan.IssueCount())
		return
	}
	if plan.Options.Assignee == runner.AssigneeSelf {
		_, _ = fmt.Fprintf(out, "\n  Assignees: assigning every imported issue to you\n")
		return
	}

	counts := map[string]int{}
	for _, resolution := range plan.UserMapping {
		counts[resolution.State]++
	}
	_, _ = fmt.Fprintf(out, "\n  User mapping: %d matched · %d unmatched · %d ambiguous · %d skipped\n",
		counts["matched"], counts["unmatched"], counts["ambiguous"], counts["skipped"])

	for index, resolution := range plan.UserMapping {
		if index == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "    … %d more\n", len(plan.UserMapping)-diagnosticLimit)
			break
		}
		source := resolution.SourceName
		if resolution.SourceEmail != "" {
			source += " <" + resolution.SourceEmail + ">"
		}
		switch resolution.State {
		case "matched":
			_, _ = fmt.Fprintf(out, "    %-32s → %s (by %s, %d issue(s))\n",
				truncateDisplay(source, 32), resolution.PulseName, resolution.Via, resolution.Rows)
		case "skipped":
			_, _ = fmt.Fprintf(out, "    %-32s → unassigned (%d issue(s))\n",
				truncateDisplay(source, 32), resolution.Rows)
		case "ambiguous":
			_, _ = fmt.Fprintf(out, "    %-32s → unassigned: several team members match (%d issue(s))\n",
				truncateDisplay(source, 32), resolution.Rows)
		default:
			_, _ = fmt.Fprintf(out, "    %-32s → unassigned: no match in the target team (%d issue(s))\n",
				truncateDisplay(source, 32), resolution.Rows)
		}
	}
	if counts["unmatched"]+counts["ambiguous"] > 0 {
		_, _ = fmt.Fprintln(out, "    Map a name explicitly with --map-user \"Jane Doe=<pulse-user-id-or-email>\".")
		_, _ = fmt.Fprintln(out, "    Only members of the target team (or a parent team) can be assignees in Pulse.")
	}
}

// printIdentifierNote explains when Jira and Pulse identifiers will line up.
// Pulse allocates issue codes sequentially per team and the API accepts no
// explicit code, so alignment is only possible in a team that starts empty.
func printIdentifierNote(out io.Writer, plan *runner.Plan) {
	if plan.TeamIssueCount > 0 {
		_, _ = fmt.Fprintf(out,
			"\n  Note: the target team already has %d issue(s), so Pulse identifiers will not "+
				"match the Jira keys. Import into a new or empty team if you need them aligned.\n",
			plan.TeamIssueCount,
		)
		return
	}
	_, _ = fmt.Fprintln(out, "\n  Issues are created in ascending source-key order, so Pulse identifiers "+
		"line up with the Jira keys where the export is contiguous.")
}

func printIgnoredColumns(out io.Writer, plan *runner.Plan) {
	if len(plan.IgnoredColumns) == 0 {
		return
	}
	shown := plan.IgnoredColumns
	suffix := ""
	if len(shown) > 12 {
		shown, suffix = shown[:12], fmt.Sprintf(" (+%d more)", len(plan.IgnoredColumns)-12)
	}
	_, _ = fmt.Fprintf(out, "\n  Not imported (no Pulse equivalent): %s%s\n", strings.Join(shown, ", "), suffix)
}

func printDiagnostics(out io.Writer, plan *runner.Plan) {
	if len(plan.Warnings) > 0 {
		_, _ = fmt.Fprintln(out)
	}
	for index, warning := range plan.Warnings {
		if index == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "  … %d more warning(s)\n", len(plan.Warnings)-diagnosticLimit)
			break
		}
		_, _ = fmt.Fprintf(out, "  warning: %s%s\n", diagnosticPrefix(warning), warning.Message)
	}
	if len(plan.Errors) > 0 {
		_, _ = fmt.Fprintln(out)
	}
	for index, validationErr := range plan.Errors {
		if index == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "  … %d more error(s)\n", len(plan.Errors)-diagnosticLimit)
			break
		}
		_, _ = fmt.Fprintf(out, "  error: %s%s\n", diagnosticPrefix(validationErr), validationErr.Message)
	}
}

func diagnosticPrefix(diagnostic runner.Diagnostic) string {
	var parts []string
	if diagnostic.Key != "" {
		parts = append(parts, diagnostic.Key)
	}
	if diagnostic.Row > 0 {
		parts = append(parts, fmt.Sprintf("row %d", diagnostic.Row))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, ", ") + "] "
}

func truncateDisplay(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func printResult(out, errOut io.Writer, result *runner.Result, stateFile string) {
	if result == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "\nImport result\n")
	_, _ = fmt.Fprintf(out, "  Created: %d issue(s) · %d project(s) · %d Main Doc(s) · %d comment(s) · %d link(s)\n",
		result.CreatedIssues, result.CreatedProjects, result.CreatedMainDocs,
		result.CreatedComments, result.LinkedIssues)
	_, _ = fmt.Fprintf(out, "  Resumed/skipped: %d\n", result.SkippedIssues)
	if failures := result.FailedIssues + result.FailedMainDocs + result.FailedComments + result.FailedLinks; failures > 0 {
		_, _ = fmt.Fprintf(out, "  Failed: %d issue(s) · %d Main Doc(s) · %d comment(s) · %d link(s)\n",
			result.FailedIssues, result.FailedMainDocs, result.FailedComments, result.FailedLinks)
	}
	_, _ = fmt.Fprintf(out, "  State: %s\n", stateFile)
	_, _ = fmt.Fprintf(out, "  Undo this import with: pulse-import rollback --state-file %s\n", stateFile)

	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(errOut, "warning: %s\n", warning)
	}
	for _, itemErr := range result.Errors {
		_, _ = fmt.Fprintf(errOut, "error: %s\n", itemErr)
	}
}
