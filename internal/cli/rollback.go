package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"

	"github.com/try-pulse/pulse-import/internal/cli/tui"
	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

// rollbackAPI is the subset of the client rollback needs.
type rollbackAPI interface {
	DeleteIssue(context.Context, string) error
	DeleteProject(context.Context, string) error
	DeleteDocument(context.Context, string) error
}

type rollbackOptions struct {
	APIURL    string
	Workspace string
	StateFile string
	NoPrompt  bool
	KeepDocs  bool
	Continue  bool
}

// newRollbackCommand undoes an import. Pulse has no server-side import id, so
// the state journal is the only record of what was created — and the only safe
// basis for deleting anything. Nothing outside the journal is ever touched.
func newRollbackCommand(deps Dependencies) *cobra.Command {
	deps = withDefaults(deps)
	opts := rollbackOptions{}

	cmd := &cobra.Command{
		Use:           "rollback",
		Short:         "Delete everything a previous import created, using its state file",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Delete the issues, projects and Main Docs recorded in an import's state file.

Only entities the state file records as created are deleted, newest first so a
sub-issue is removed before its parent. Labels are never deleted: they may
already be in use by work that was not imported.

  pulse-import rollback --state-file ./jira-export.csv.pulse-import.state.jsonl`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRollback(cmd, opts, deps)
		},
	}
	cmd.Flags().StringVar(&opts.StateFile, "state-file", "", "Import state journal to undo (required)")
	cmd.Flags().StringVar(&opts.APIURL, "api-url", "", "Pulse API base URL")
	cmd.Flags().StringVar(&opts.Workspace, "workspace", "", "Workspace ID for X-Workspace-ID")
	cmd.Flags().BoolVar(&opts.NoPrompt, "yes", false, "Do not ask for confirmation")
	cmd.Flags().BoolVar(&opts.KeepDocs, "keep-documents", false, "Delete issues and projects but keep their Main Docs")
	cmd.Flags().BoolVar(&opts.Continue, "continue-on-error", false, "Keep going after a delete fails")
	registerRollbackCompletions(cmd)
	return cmd
}

func runRollback(cmd *cobra.Command, opts rollbackOptions, deps Dependencies) error {
	ctx := cmd.Context()
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	stateFile := strings.TrimSpace(opts.StateFile)
	if stateFile == "" {
		return fmt.Errorf("--state-file is required")
	}
	if _, err := os.Stat(stateFile); err != nil {
		return fmt.Errorf("state file not found: %s", stateFile)
	}

	accessible := !tui.IsTerminal(cmd.InOrStdin()) || !tui.IsTerminal(errOut) ||
		strings.EqualFold(os.Getenv("TERM"), "dumb")
	prompter := deps.NewPrompter(ctx, opts.NoPrompt, cmd.InOrStdin(), errOut, accessible)

	sess, err := newSession(cmd, deps, prompter, opts.APIURL, opts.Workspace)
	if err != nil {
		return err
	}

	// Opening with the journal's own identity keeps this read-only with respect
	// to the identity check: rollback must never rewrite what an import targeted.
	identity, err := readIdentity(stateFile)
	if err != nil {
		return err
	}
	journal, err := importstate.Open(stateFile, identity)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()

	targets := rollbackTargets(journal.Items(), opts.KeepDocs)
	if len(targets) == 0 {
		_, _ = fmt.Fprintln(out, "Nothing to roll back: the state file records no created entities")
		return nil
	}

	issues, projects, docs := countTargets(targets)
	styles := reviewStyles(out)
	_, _ = fmt.Fprintf(out, "\n%s\n", styles.HeadingText("Rollback plan"))
	summary := fmt.Sprintf("State: %s\nTarget: workspace=%s team=%s\nDelete: %d issue(s) · %d project(s) · %d Main Doc(s)\nLabels are left alone; they may be shared with work that was not imported.",
		stateFile, identity.WorkspaceID, identity.TeamID, issues, projects, docs)
	if styles.Enabled {
		_, _ = fmt.Fprintln(out, styles.Box.Render(summary))
	} else {
		_, _ = fmt.Fprintf(out, "  State: %s\n", stateFile)
		_, _ = fmt.Fprintf(out, "  Target: workspace=%s team=%s\n", identity.WorkspaceID, identity.TeamID)
		_, _ = fmt.Fprintf(out, "  Delete: %d issue(s) · %d project(s) · %d Main Doc(s)\n", issues, projects, docs)
		_, _ = fmt.Fprintln(out, "  Labels are left alone; they may be shared with work that was not imported.")
	}

	if identity.WorkspaceID != sess.workspaceID {
		return fmt.Errorf(
			"state file targets workspace %s but this run is scoped to %s; pass --workspace %s",
			identity.WorkspaceID, sess.workspaceID, identity.WorkspaceID,
		)
	}

	if !opts.NoPrompt {
		confirmed, err := prompter.Confirm(
			fmt.Sprintf("Permanently delete these %d entities from Pulse?", len(targets)), false)
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				_, _ = fmt.Fprintln(out, "Rollback canceled — no Pulse changes were made")
				return nil
			}
			return err
		}
		if !confirmed {
			_, _ = fmt.Fprintln(out, "Rollback canceled — no Pulse changes were made")
			return nil
		}
	}

	bar := newProgressBar(errOut, len(targets))
	deleted, failed := deleteTargets(ctx, sess.client, targets, opts.Continue, errOut, bar)
	if bar != nil {
		_ = bar.Finish()
	}
	resultStyles := reviewStyles(out)
	_, _ = fmt.Fprintf(out, "\n%s\n", resultStyles.HeadingText("Rollback result"))
	_, _ = fmt.Fprintf(out, "  %d deleted · %d failed\n", deleted, failed)
	if failed > 0 {
		return fmt.Errorf("rollback finished with %d failure(s); the state file was left in place", failed)
	}
	_, _ = fmt.Fprintf(out, "%s\n", resultStyles.OKLine("You can now delete the state file: "+stateFile))
	return nil
}

// target is one deletion, in the order it must happen.
type target struct {
	Key  string
	Kind string // "document", "issue" or "project"
	ID   string
}

// rollbackTargets lists what to delete, reversing creation order so a sub-issue
// goes before its parent (Pulse does not cascade) and a document before the
// entity it hangs off.
func rollbackTargets(items []importstate.Item, keepDocs bool) []target {
	var targets []target
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if !keepDocs && item.MainDocID != "" {
			targets = append(targets, target{Key: item.Key, Kind: "document", ID: item.MainDocID})
		}
		switch {
		case item.Kind == importstate.KindProject && item.ProjectID != "":
			targets = append(targets, target{Key: item.Key, Kind: "project", ID: item.ProjectID})
		case item.Kind != importstate.KindProject && item.IssueID != "":
			targets = append(targets, target{Key: item.Key, Kind: "issue", ID: item.IssueID})
		}
	}
	return targets
}

func countTargets(targets []target) (issues, projects, docs int) {
	for _, t := range targets {
		switch t.Kind {
		case "issue":
			issues++
		case "project":
			projects++
		case "document":
			docs++
		}
	}
	return issues, projects, docs
}

func deleteTargets(
	ctx context.Context,
	client rollbackAPI,
	targets []target,
	continueOnError bool,
	errOut io.Writer,
	bar *progressbar.ProgressBar,
) (deleted, failed int) {
	for i, t := range targets {
		if err := ctx.Err(); err != nil {
			_, _ = fmt.Fprintf(errOut, "error: rollback interrupted: %v\n", err)
			return deleted, failed + 1
		}
		var err error
		switch t.Kind {
		case "document":
			err = client.DeleteDocument(ctx, t.ID)
		case "project":
			err = client.DeleteProject(ctx, t.ID)
		default:
			err = client.DeleteIssue(ctx, t.ID)
		}
		switch {
		case err == nil, pulseapi.IsNotFound(err):
			// A 404 means it is already gone, which is the desired end state.
			deleted++
		default:
			failed++
			_, _ = fmt.Fprintf(errOut, "error: delete %s %s (%s): %v\n", t.Kind, t.ID, t.Key, err)
			if !continueOnError {
				return deleted, failed
			}
		}
		if bar != nil {
			bar.Describe("Deleting " + t.Kind)
			_ = bar.Set(i + 1)
		}
	}
	return deleted, failed
}

// readIdentity reads the header of a state file without validating it against a
// target, which is what rollback needs: the journal itself is the source of
// truth for where the import went.
func readIdentity(path string) (importstate.Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return importstate.Identity{}, fmt.Errorf("read state file: %w", err)
	}
	identity, err := importstate.ReadIdentity(data)
	if err != nil {
		return importstate.Identity{}, fmt.Errorf("%s: %w", path, err)
	}
	return identity, nil
}
