package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/try-pulse/pulse-import/internal/cli/tui"
	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

const diagnosticLimit = 20

func reviewStyles(out io.Writer) tui.Styles {
	opts := tui.Detect(os.Stdin, out)
	var outFile *os.File
	if f, ok := out.(*os.File); ok {
		outFile = f
	}
	return tui.NewStyles(opts, os.Stdin, outFile)
}

// printPlan is the review step: everything that is about to be written, plus
// every mapping decision that was made on the user's behalf, shown before the
// confirmation prompt so a wrong mapping can be caught while it is still free.
func printPlan(out io.Writer, plan *runner.Plan, dryRun bool) {
	styles := reviewStyles(out)
	heading := "Import plan"
	if dryRun {
		heading = "Dry-run plan"
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", styles.HeadingText(heading))

	var body strings.Builder
	fmt.Fprintf(&body, "Target: workspace=%s team=%s", plan.Options.WorkspaceID, plan.Options.TeamID)
	if plan.Options.ProjectID != "" {
		fmt.Fprintf(&body, " project=%s", plan.Options.ProjectID)
	}
	body.WriteByte('\n')
	fmt.Fprintf(&body, "Issues: %d (%d sub-issues) · Projects from epics: %d · Main Docs: %d\n",
		plan.IssueCount(), plan.SubIssueCount, plan.ProjectCount, plan.MainDocCount())
	fmt.Fprintf(&body, "Comments: %d · Issue links: %d · Estimates: %d\n",
		plan.CommentCount, plan.RelationCount, plan.EstimatesSet)
	fmt.Fprintf(&body, "Labels: %d create · %d reuse", plan.LabelsToCreate(),
		len(plan.Labels)-plan.LabelsToCreate()-plan.LabelsToUnarchive())
	if plan.LabelsToUnarchive() > 0 {
		fmt.Fprintf(&body, " · %d unarchive", plan.LabelsToUnarchive())
	}
	body.WriteByte('\n')
	fmt.Fprintf(&body, "Rows skipped: %d · Filtered out: %d\n", plan.SkippedRows, plan.FilteredIssues)

	if styles.Enabled {
		_, _ = fmt.Fprintln(out, styles.Box.Render(strings.TrimRight(body.String(), "\n")))
	} else {
		for _, line := range strings.Split(strings.TrimRight(body.String(), "\n"), "\n") {
			_, _ = fmt.Fprintf(out, "  %s\n", line)
		}
	}

	printStatusMapping(out, styles, plan)
	printUserMapping(out, styles, plan)
	printIdentifierNote(out, styles, plan)
	printIgnoredColumns(out, styles, plan)
	printDiagnostics(out, styles, plan)
}

func printStatusMapping(out io.Writer, styles tui.Styles, plan *runner.Plan) {
	if len(plan.StatusMapping) == 0 {
		return
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", styles.MutedText("  Status mapping (source → Pulse)"))
	nameWidth := clampPad(12, styles.Width)
	for _, status := range statusmap.All() {
		names := plan.StatusMapping[string(status)]
		if len(names) == 0 {
			continue
		}
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		joined := strings.Join(sorted, ", ")
		maxJoin := styles.Width - nameWidth - 6
		if maxJoin < 20 {
			maxJoin = 20
		}
		_, _ = fmt.Fprintf(out, "    %-*s ← %s\n", nameWidth, status, tui.Truncate(joined, maxJoin))
	}
}

func printUserMapping(out io.Writer, styles tui.Styles, plan *runner.Plan) {
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

	col := clampPad(32, styles.Width/2)
	for index, resolution := range plan.UserMapping {
		if index == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "    … %d more\n", len(plan.UserMapping)-diagnosticLimit)
			break
		}
		source := resolution.SourceName
		if resolution.SourceEmail != "" {
			source += " <" + resolution.SourceEmail + ">"
		}
		src := tui.Truncate(source, col)
		switch resolution.State {
		case "matched":
			_, _ = fmt.Fprintf(out, "    %-*s → %s (by %s, %d issue(s))\n",
				col, src, resolution.PulseName, resolution.Via, resolution.Rows)
		case "skipped":
			_, _ = fmt.Fprintf(out, "    %-*s → unassigned (%d issue(s))\n",
				col, src, resolution.Rows)
		case "ambiguous":
			_, _ = fmt.Fprintf(out, "    %-*s → unassigned: several team members match (%d issue(s))\n",
				col, src, resolution.Rows)
		default:
			_, _ = fmt.Fprintf(out, "    %-*s → unassigned: no match in the target team (%d issue(s))\n",
				col, src, resolution.Rows)
		}
	}
	if counts["unmatched"]+counts["ambiguous"] > 0 {
		_, _ = fmt.Fprintln(out, styles.MutedText(
			`    Map a name explicitly with --map-user "Jane Doe=<pulse-user-id-or-email>".`))
		_, _ = fmt.Fprintln(out, styles.MutedText(
			"    Only members of the target team (or a parent team) can be assignees in Pulse."))
	}
}

func printIdentifierNote(out io.Writer, styles tui.Styles, plan *runner.Plan) {
	if plan.TeamIssueCount > 0 {
		_, _ = fmt.Fprintf(out, "\n  %s\n", styles.WarnLine(fmt.Sprintf(
			"the target team already has %d issue(s), so Pulse identifiers will not "+
				"match the Jira keys. Import into a new or empty team if you need them aligned.",
			plan.TeamIssueCount,
		)))
		return
	}
	_, _ = fmt.Fprintln(out, styles.MutedText("\n  Issues are created in ascending source-key order, so Pulse identifiers "+
		"line up with the Jira keys where the export is contiguous."))
}

func printIgnoredColumns(out io.Writer, styles tui.Styles, plan *runner.Plan) {
	if len(plan.IgnoredColumns) == 0 {
		return
	}
	shown := plan.IgnoredColumns
	suffix := ""
	if len(shown) > 12 {
		shown, suffix = shown[:12], fmt.Sprintf(" (+%d more)", len(plan.IgnoredColumns)-12)
	}
	line := fmt.Sprintf("Not imported (no Pulse equivalent): %s%s", strings.Join(shown, ", "), suffix)
	_, _ = fmt.Fprintf(out, "\n  %s\n", styles.MutedText(tui.Truncate(line, styles.Width)))
}

func printDiagnostics(out io.Writer, styles tui.Styles, plan *runner.Plan) {
	if len(plan.Warnings) > 0 {
		_, _ = fmt.Fprintln(out)
	}
	for index, warning := range plan.Warnings {
		if index == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "  … %d more warning(s)\n", len(plan.Warnings)-diagnosticLimit)
			break
		}
		_, _ = fmt.Fprintf(out, "  %s\n", styles.WarnLine(diagnosticPrefix(warning)+warning.Message))
	}
	if len(plan.Errors) > 0 {
		_, _ = fmt.Fprintln(out)
	}
	for index, validationErr := range plan.Errors {
		if index == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "  … %d more error(s)\n", len(plan.Errors)-diagnosticLimit)
			break
		}
		_, _ = fmt.Fprintf(out, "  %s\n", styles.ErrorLine(diagnosticPrefix(validationErr)+validationErr.Message))
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

func clampPad(want, width int) int {
	if width <= 0 {
		return want
	}
	max := width / 3
	if max < 12 {
		max = 12
	}
	if want > max {
		return max
	}
	return want
}

func printResult(out, errOut io.Writer, result *runner.Result, stateFile string) {
	if result == nil {
		return
	}
	styles := reviewStyles(out)
	_, _ = fmt.Fprintf(out, "\n%s\n", styles.HeadingText("Import result"))
	summary := fmt.Sprintf(
		"Created: %d issue(s) · %d project(s) · %d Main Doc(s) · %d comment(s) · %d link(s)\n"+
			"Resumed/skipped: %d",
		result.CreatedIssues, result.CreatedProjects, result.CreatedMainDocs,
		result.CreatedComments, result.LinkedIssues, result.SkippedIssues,
	)
	failures := result.FailedIssues + result.FailedMainDocs + result.FailedComments + result.FailedLinks
	if failures > 0 {
		summary += fmt.Sprintf("\nFailed: %d issue(s) · %d Main Doc(s) · %d comment(s) · %d link(s)",
			result.FailedIssues, result.FailedMainDocs, result.FailedComments, result.FailedLinks)
	}
	summary += fmt.Sprintf("\nState: %s\nUndo: pulse-import rollback --state-file %s", stateFile, stateFile)
	if styles.Enabled {
		_, _ = fmt.Fprintln(out, styles.Box.Render(summary))
		if failures == 0 {
			_, _ = fmt.Fprintln(out, styles.OKLine("Import finished"))
		}
	} else {
		for _, line := range strings.Split(summary, "\n") {
			_, _ = fmt.Fprintf(out, "  %s\n", line)
		}
	}

	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(errOut, "%s\n", styles.WarnLine(warning))
	}
	for _, itemErr := range result.Errors {
		_, _ = fmt.Fprintf(errOut, "%s\n", styles.ErrorLine(itemErr))
	}
}
