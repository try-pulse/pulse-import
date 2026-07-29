package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"

	"github.com/try-pulse/pulse-import/internal/auth"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/version"
)

type Dependencies struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	LoadConfig  func() (*auth.Config, error)
	SaveConfig  func(*auth.Config) error
	NewClient   func(string, string, string) (*pulseapi.Client, error)
	NewPrompter func(context.Context, bool, io.Reader, io.Writer, bool) Prompter
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		LoadConfig: auth.Load, SaveConfig: auth.Save, NewClient: pulseapi.New,
		NewPrompter: newPrompter,
	}
}

func NewRoot() *cobra.Command {
	return NewRootWithDependencies(DefaultDependencies())
}

func NewRootWithDependencies(deps Dependencies) *cobra.Command {
	defaults := DefaultDependencies()
	if deps.In == nil {
		deps.In = defaults.In
	}
	if deps.Out == nil {
		deps.Out = defaults.Out
	}
	if deps.Err == nil {
		deps.Err = defaults.Err
	}
	if deps.LoadConfig == nil {
		deps.LoadConfig = defaults.LoadConfig
	}
	if deps.SaveConfig == nil {
		deps.SaveConfig = defaults.SaveConfig
	}
	if deps.NewClient == nil {
		deps.NewClient = defaults.NewClient
	}
	if deps.NewPrompter == nil {
		deps.NewPrompter = defaults.NewPrompter
	}

	opts := Options{}
	buildVersion, buildCommit, buildDate := version.Build()
	cmd := &cobra.Command{
		Use:           "pulse-import",
		Short:         "Import issues into Pulse (Jira CSV and more)",
		Version:       buildVersion,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Import issues into Pulse (v1: Jira CSV).

Auth:
  export PULSE_ACCESS_TOKEN=<jwt>

Non-interactive:
  PULSE_ACCESS_TOKEN=… pulse-import \
    --yes \
    --importer jira-csv \
    --file ./jira.csv \
    --workspace <workspace-id> \
    --team <team-id-or-name> \
    --jira-url https://acme.atlassian.net`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runImport(cmd, opts, deps)
		},
	}
	cmd.SetIn(deps.In)
	cmd.SetOut(deps.Out)
	cmd.SetErr(deps.Err)
	cmd.Flags().StringVar(&opts.APIURL, "api-url", "", "Pulse API base URL (default https://api.trypulse.tech/api/v1)")
	cmd.Flags().StringVar(&opts.Workspace, "workspace", "", "Workspace ID for X-Workspace-ID")
	cmd.Flags().StringVar(&opts.Importer, "importer", "", "Importer id (jira-csv)")
	cmd.Flags().StringVar(&opts.File, "file", "", "Path to source export file")
	cmd.Flags().StringVar(&opts.Team, "team", "", "Target team id or name")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Optional project id or name")
	cmd.Flags().StringVar(&opts.JiraURL, "jira-url", "", "Jira Cloud or on-prem base URL")
	cmd.Flags().BoolVar(&opts.SelfAssign, "self-assign", false, "Assign all imported issues to yourself")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Validate and show the write plan without creating anything")
	cmd.Flags().BoolVar(&opts.Continue, "continue-on-error", false, "Continue after a definitive per-issue failure")
	cmd.Flags().BoolVar(&opts.NoPrompt, "yes", false, "Non-interactive: require flags/env and accept a valid plan")
	cmd.Flags().StringVar(&opts.StateFile, "state-file", "", "Resume journal path (default: <csv>.pulse-import.state.jsonl)")
	cmd.Flags().StringSliceVar(&opts.Adopt, "adopt", nil, "Resolve unknown create as JIRA-KEY=PULSE-ISSUE-ID (repeatable)")
	cmd.Flags().StringSliceVar(&opts.RetryUnknown, "retry-unknown", nil, "Explicitly retry an unknown Jira key (repeatable; duplicate risk)")
	cmd.SetVersionTemplate(fmt.Sprintf(
		"{{.Name}} {{.Version}}\ncommit: %s\ndate: %s\n",
		buildCommit,
		buildDate,
	))
	return cmd
}

func ExecuteContext(ctx context.Context) error {
	return NewRoot().ExecuteContext(ctx)
}

func runImport(cmd *cobra.Command, opts Options, deps Dependencies) error {
	ctx := cmd.Context()
	accessible := !isTerminal(cmd.InOrStdin()) || !isTerminal(cmd.ErrOrStderr()) ||
		strings.EqualFold(os.Getenv("TERM"), "dumb")
	prompter := deps.NewPrompter(ctx, opts.NoPrompt, cmd.InOrStdin(), cmd.ErrOrStderr(), accessible)

	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	apiURL := strings.TrimSpace(opts.APIURL)
	if apiURL == "" {
		apiURL = strings.TrimSpace(os.Getenv(auth.EnvAPIURL))
	}
	if apiURL == "" {
		apiURL = strings.TrimSpace(cfg.APIURL)
	}
	if apiURL == "" {
		apiURL = auth.DefaultAPIURL()
	}
	token := auth.AccessToken()
	if token == "" {
		token, err = prompter.Secret(
			"Pulse API token",
			"Paste a JWT access token. It is used for this run only and is never saved.",
			required,
		)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(token)
	}

	workspaceID := strings.TrimSpace(opts.Workspace)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(os.Getenv(auth.EnvWorkspace))
	}
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(cfg.WorkspaceID)
	}

	client, err := deps.NewClient(apiURL, token, workspaceID)
	if err != nil {
		return err
	}
	apiURL = client.BaseURL
	if client.InsecureRemote() {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: Pulse API uses unencrypted HTTP; the bearer token can be intercepted\n")
	}
	me, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Signed in as %s\n", displayUser(me))

	if workspaceID == "" {
		workspaceID, err = pickWorkspace(ctx, client, prompter)
		if err != nil {
			return err
		}
		client.WorkspaceID = workspaceID
	} else {
		client.WorkspaceID = workspaceID
	}
	cfg.APIURL = apiURL
	cfg.WorkspaceID = workspaceID
	if err := deps.SaveConfig(cfg); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not save non-secret config (%s): %v\n", auth.ConfigPathHint(), err)
	}

	importerID := strings.TrimSpace(opts.Importer)
	if importerID == "" {
		options := make([]huh.Option[string], 0, len(registry))
		for _, registration := range registry {
			options = append(options, huh.NewOption(registration.Label, registration.ID))
		}
		importerID, err = prompter.Select("Which service would you like to import from?", options)
		if err != nil {
			return err
		}
	}
	registration, err := lookupImporter(importerID)
	if err != nil {
		return err
	}
	importerID = registration.ID
	importer, err := registration.New(opts, prompter)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Parsing %s…\n", importer.Name())
	data, err := importer.Import(ctx)
	if err != nil {
		return err
	}

	teamID, err := resolveTeam(ctx, client, opts, prompter)
	if err != nil {
		return err
	}
	projectID, err := resolveProject(ctx, client, opts, teamID, prompter)
	if err != nil {
		return err
	}
	mode, err := assigneeMode(opts, prompter)
	if err != nil {
		return err
	}

	engine := runner.New(client)
	plan, err := engine.Prepare(ctx, data, runner.Options{
		ImporterID: importerID, APIURL: apiURL, WorkspaceID: workspaceID,
		TeamID: teamID, ProjectID: projectID, Assignee: mode, SelfUserID: me.ID,
	})
	if err != nil {
		return err
	}
	printPlan(cmd.OutOrStdout(), plan, opts.DryRun)
	if !plan.Valid() {
		return fmt.Errorf("preflight failed with %d validation error(s); no Pulse changes were made", len(plan.Errors))
	}
	if opts.DryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Dry run OK — no Pulse changes were made\n")
		return nil
	}

	adoptions, err := parseKeyValues(opts.Adopt, "--adopt")
	if err != nil {
		return err
	}
	retryUnknown, err := parseKeys(opts.RetryUnknown)
	if err != nil {
		return err
	}
	if len(retryUnknown) > 0 && !opts.NoPrompt {
		confirmed, err := prompter.Confirm(
			"Retry unknown creates? This can create duplicates if Pulse accepted the earlier request.",
			false,
		)
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("unknown create retry was not confirmed")
		}
	}
	if !opts.NoPrompt {
		confirmed, err := prompter.Confirm("Proceed with this import plan?", false)
		if err != nil {
			return err
		}
		if !confirmed {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Import canceled — no Pulse changes were made")
			return nil
		}
	}

	stateFile := strings.TrimSpace(opts.StateFile)
	if stateFile == "" {
		stateFile = defaultStatePath(data.SourcePath)
	}
	var bar *progressbar.ProgressBar
	if isTerminal(cmd.ErrOrStderr()) {
		bar = progressbar.NewOptions(
			len(plan.Items),
			progressbar.OptionSetWriter(cmd.ErrOrStderr()),
			progressbar.OptionSetDescription("Importing issues"),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWidth(20),
			progressbar.OptionClearOnFinish(),
		)
	}
	result, runErr := engine.Execute(ctx, plan, runner.ExecuteOptions{
		StateFile: stateFile, ContinueOnError: opts.Continue,
		Adopt: adoptions, RetryUnknown: retryUnknown,
		OnProgress: func(runner.Progress) {
			if bar != nil {
				_ = bar.Add(1)
			}
		},
	})
	if bar != nil {
		_ = bar.Finish()
	}
	printResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), result, stateFile)
	return runErr
}

func printPlan(out io.Writer, plan *runner.Plan, dryRun bool) {
	mode := "Import plan"
	if dryRun {
		mode = "Dry-run plan"
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", mode)
	_, _ = fmt.Fprintf(out, "  Target: workspace=%s team=%s", plan.Options.WorkspaceID, plan.Options.TeamID)
	if plan.Options.ProjectID != "" {
		_, _ = fmt.Fprintf(out, " project=%s", plan.Options.ProjectID)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "  Issues: %d · Main Docs: %d · Labels: %d create / %d reuse\n",
		len(plan.Items), plan.MainDocCount(), plan.LabelsToCreate(), len(plan.Labels)-plan.LabelsToCreate())
	_, _ = fmt.Fprintf(out, "  Assignees: %d matched · %d unmatched · %d ambiguous · Rows skipped: %d\n",
		plan.AssigneesMatched, plan.AssigneesUnmatched, plan.AssigneesAmbiguous, plan.SkippedRows)
	const diagnosticLimit = 20
	for i, warning := range plan.Warnings {
		if i == diagnosticLimit {
			_, _ = fmt.Fprintf(out, "  … %d more warning(s)\n", len(plan.Warnings)-diagnosticLimit)
			break
		}
		_, _ = fmt.Fprintf(out, "  warning: %s%s\n", diagnosticPrefix(warning), warning.Message)
	}
	for i, validationErr := range plan.Errors {
		if i == diagnosticLimit {
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

func printResult(out, errOut io.Writer, result *runner.Result, stateFile string) {
	if result == nil {
		return
	}
	_, _ = fmt.Fprintf(out,
		"\nImport result: %d issues created · %d resumed/skipped · %d issue failures · %d Main Docs created · %d Main Doc failures\n",
		result.CreatedIssues, result.SkippedIssues, result.FailedIssues,
		result.CreatedMainDocs, result.FailedMainDocs,
	)
	_, _ = fmt.Fprintf(out, "State: %s\n", stateFile)
	for _, warning := range result.Warnings {
		_, _ = fmt.Fprintf(errOut, "warning: %s\n", warning)
	}
	for _, itemErr := range result.Errors {
		_, _ = fmt.Fprintf(errOut, "error: %s\n", itemErr)
	}
}

func isTerminal(value any) bool {
	file, ok := value.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
