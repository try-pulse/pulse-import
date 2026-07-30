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

func withDefaults(deps Dependencies) Dependencies {
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
	return deps
}

func NewRootWithDependencies(deps Dependencies) *cobra.Command {
	deps = withDefaults(deps)

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
    --jira-url https://acme.atlassian.net

Undo:
  pulse-import rollback --state-file ./jira.csv.pulse-import.state.jsonl`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runImport(cmd, opts, deps)
		},
	}
	cmd.SetIn(deps.In)
	cmd.SetOut(deps.Out)
	cmd.SetErr(deps.Err)

	flags := cmd.Flags()
	flags.StringVar(&opts.APIURL, "api-url", "", "Pulse API base URL (default https://api.trypulse.tech/api/v1)")
	flags.StringVar(&opts.Workspace, "workspace", "", "Workspace ID for X-Workspace-ID")
	flags.StringVar(&opts.Importer, "importer", "", "Importer id (jira-csv)")
	flags.StringVar(&opts.File, "file", "", "Path to source export file")
	flags.StringVar(&opts.Team, "team", "", "Target team id or name")
	flags.StringVar(&opts.Project, "project", "", "Pin every imported issue to this project (id or name); disables epic→project mapping")
	flags.StringVar(&opts.JiraURL, "jira-url", "", "Jira Cloud or on-prem base URL")

	flags.StringVar(&opts.Assignee, "assignee", "", "Assignee strategy: mapped (default), self, none")
	flags.BoolVar(&opts.SelfAssign, "self-assign", false, "Shorthand for --assignee self")
	flags.StringArrayVar(&opts.MapUser, "map-user", nil,
		`Map a source user: --map-user "Jane Doe=<pulse-user-id|email|skip>" (repeatable)`)

	flags.StringVar(&opts.Epics, "epics", "project", "How to import Jira epics: project or label")
	flags.BoolVar(&opts.SkipComments, "skip-comments", false, "Do not import comments")
	flags.BoolVar(&opts.SkipLabels, "skip-labels", false, "Do not create or attach labels")
	flags.BoolVar(&opts.SkipRelations, "skip-relations", false, "Do not import blocks/blocked-by links")
	flags.BoolVar(&opts.StrictLabels, "strict-labels", false,
		"Fail instead of dropping labels past Pulse's limit of 10 per issue")
	flags.BoolVar(&opts.NoMigrated, "no-migrated-label", false, `Do not add the "Migrated" label`)

	flags.StringSliceVar(&opts.SkipStatus, "skip-status", nil,
		"Skip issues that map to these Pulse statuses (comma separated)")
	flags.StringSliceVar(&opts.OnlyStatus, "only-status", nil,
		"Import only issues that map to these Pulse statuses (comma separated)")
	flags.StringVar(&opts.SkipStale, "skip-stale", "",
		"Skip issues not updated within this many days (or a duration like 4320h)")

	flags.IntVar(&opts.Concurrency, "concurrency", 4, "Parallel Pulse writes (1 disables parallelism)")

	flags.BoolVar(&opts.DryRun, "dry-run", false, "Validate and show the write plan without creating anything")
	flags.BoolVar(&opts.Continue, "continue-on-error", false, "Continue after a definitive per-issue failure")
	flags.BoolVar(&opts.NoPrompt, "yes", false, "Non-interactive: require flags/env and accept a valid plan")

	flags.StringVar(&opts.StateFile, "state-file", "", "Resume journal path (default: <csv>.pulse-import.state.jsonl)")
	flags.StringSliceVar(&opts.Adopt, "adopt", nil, "Resolve unknown create as SOURCE-KEY=PULSE-ID (repeatable)")
	flags.StringSliceVar(&opts.RetryUnknown, "retry-unknown", nil,
		"Explicitly retry an unknown source key (repeatable; duplicate risk)")

	cmd.SetVersionTemplate(fmt.Sprintf(
		"{{.Name}} {{.Version}}\ncommit: %s\ndate: %s\n",
		buildCommit, buildDate,
	))
	cmd.AddCommand(newRollbackCommand(deps))
	return cmd
}

func ExecuteContext(ctx context.Context) error {
	return NewRoot().ExecuteContext(ctx)
}

// session is everything an authenticated run needs, shared by import and
// rollback.
type session struct {
	client      *pulseapi.Client
	apiURL      string
	workspaceID string
	me          *pulseapi.User
}

func newSession(cmd *cobra.Command, deps Dependencies, prompter Prompter, apiURLFlag, workspaceFlag string) (*session, error) {
	ctx := cmd.Context()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return nil, err
	}
	apiURL := firstNonEmpty(
		strings.TrimSpace(apiURLFlag),
		strings.TrimSpace(os.Getenv(auth.EnvAPIURL)),
		strings.TrimSpace(cfg.APIURL),
		auth.DefaultAPIURL(),
	)
	token := auth.AccessToken()
	if token == "" {
		token, err = prompter.Secret(
			"Pulse API token",
			"Paste a JWT access token. It is used for this run only and is never saved.",
			required,
		)
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(token)
	}

	workspaceID := firstNonEmpty(
		strings.TrimSpace(workspaceFlag),
		strings.TrimSpace(os.Getenv(auth.EnvWorkspace)),
		strings.TrimSpace(cfg.WorkspaceID),
	)

	client, err := deps.NewClient(apiURL, token, workspaceID)
	if err != nil {
		return nil, err
	}
	apiURL = client.BaseURL
	if client.InsecureRemote() {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: Pulse API uses unencrypted HTTP; the bearer token can be intercepted\n")
	}
	me, err := client.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Signed in as %s\n", displayUser(me))

	if workspaceID == "" {
		workspaceID, err = pickWorkspace(ctx, client, prompter)
		if err != nil {
			return nil, err
		}
	}
	client.WorkspaceID = workspaceID

	cfg.APIURL, cfg.WorkspaceID = apiURL, workspaceID
	if err := deps.SaveConfig(cfg); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: could not save non-secret config (%s): %v\n", auth.ConfigPathHint(), err)
	}
	return &session{client: client, apiURL: apiURL, workspaceID: workspaceID, me: me}, nil
}

func runImport(cmd *cobra.Command, opts Options, deps Dependencies) error {
	ctx := cmd.Context()
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	// Validate flag combinations before authenticating: a typo should not cost a
	// round trip, let alone a token prompt.
	mode, err := assigneeMode(opts, nonInteractive{})
	if err != nil && !isPromptRequired(err) {
		return err
	}
	epics, err := epicMode(opts)
	if err != nil {
		return err
	}
	skipStatuses, err := parseStatusFilter(opts.SkipStatus, "--skip-status")
	if err != nil {
		return err
	}
	onlyStatuses, err := parseStatusFilter(opts.OnlyStatus, "--only-status")
	if err != nil {
		return err
	}
	for status := range skipStatuses {
		if onlyStatuses[status] {
			return fmt.Errorf("--skip-status and --only-status both list %q", status)
		}
	}
	staleAfter, err := parseStale(opts.SkipStale)
	if err != nil {
		return err
	}
	userMap, err := parseUserMap(opts.MapUser)
	if err != nil {
		return err
	}
	adoptions, err := parseKeyValues(opts.Adopt, "--adopt")
	if err != nil {
		return err
	}
	retryUnknown, err := parseKeys(opts.RetryUnknown)
	if err != nil {
		return err
	}
	if opts.Concurrency < 1 {
		return fmt.Errorf("--concurrency must be at least 1")
	}

	accessible := !isTerminal(cmd.InOrStdin()) || !isTerminal(errOut) ||
		strings.EqualFold(os.Getenv("TERM"), "dumb")
	prompter := deps.NewPrompter(ctx, opts.NoPrompt, cmd.InOrStdin(), errOut, accessible)

	sess, err := newSession(cmd, deps, prompter, opts.APIURL, opts.Workspace)
	if err != nil {
		return err
	}

	importerID := strings.TrimSpace(opts.Importer)
	if importerID == "" {
		options := make([]huh.Option[string], 0, len(registry))
		for _, registration := range registry {
			options = append(options, huh.NewOption(registration.Label, registration.ID))
		}
		if importerID, err = prompter.Select("Which service would you like to import from?", options); err != nil {
			return err
		}
	}
	registration, err := lookupImporter(importerID)
	if err != nil {
		return err
	}
	importerID = registration.ID
	importer, err := registration.New(opts, epics, prompter)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Parsing %s…\n", importer.Name())
	data, err := importer.Import(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Parsed %d issue(s), %d epic(s), %d label(s), %d user(s)\n",
		len(data.Issues), len(data.Projects), len(data.Labels), len(data.Users))

	teams, err := sess.client.ListTeams(ctx)
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}
	teamID, err := resolveTeam(teams, opts, prompter)
	if err != nil {
		return err
	}
	projectID, err := resolveProject(ctx, sess.client, opts, teamID, prompter)
	if err != nil {
		return err
	}
	if mode == "" {
		if mode, err = assigneeMode(opts, prompter); err != nil {
			return err
		}
	}

	path := teamPath(teams, teamID)
	if mode == runner.AssigneeMapped {
		if userMap, err = mapUsersInteractively(
			ctx, out, prompter, sess.client, data.Users, path, userMap,
		); err != nil {
			return err
		}
	}

	estimateSettings := pulseapi.EstimateSettings{}
	if team, ok := findTeam(teams, teamID); ok {
		estimateSettings = team.EstimateSettings
	}

	engine := runner.New(sess.client)
	plan, err := engine.Prepare(ctx, data, runner.Options{
		ImporterID:       importerID,
		APIURL:           sess.apiURL,
		WorkspaceID:      sess.workspaceID,
		TeamID:           teamID,
		TeamPath:         path,
		ProjectID:        projectID,
		Assignee:         mode,
		SelfUserID:       sess.me.ID,
		UserMap:          userMap,
		EstimateSettings: estimateSettings,
		LabelPolicy:      labelPolicy(opts),
		AddMigratedLabel: !opts.NoMigrated,
		SkipLabels:       opts.SkipLabels,
		SkipComments:     opts.SkipComments,
		SkipRelations:    opts.SkipRelations,
		SkipStatuses:     skipStatuses,
		OnlyStatuses:     onlyStatuses,
		StaleAfter:       staleAfter,
		Concurrency:      opts.Concurrency,
	})
	if err != nil {
		return err
	}
	printPlan(out, plan, opts.DryRun)
	if !plan.Valid() {
		return fmt.Errorf(
			"preflight failed with %d validation error(s); no Pulse changes were made", len(plan.Errors))
	}
	if opts.DryRun {
		_, _ = fmt.Fprintf(out, "Dry run OK — no Pulse changes were made\n")
		return nil
	}
	if len(plan.Items) == 0 {
		_, _ = fmt.Fprintln(out, "Nothing to import after filtering — no Pulse changes were made")
		return nil
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
			_, _ = fmt.Fprintln(out, "Import canceled — no Pulse changes were made")
			return nil
		}
	}

	stateFile := strings.TrimSpace(opts.StateFile)
	if stateFile == "" {
		stateFile = defaultStatePath(data.SourcePath)
	}
	bar := newProgressBar(errOut, plan.TotalWrites())
	result, runErr := engine.Execute(ctx, plan, runner.ExecuteOptions{
		StateFile: stateFile, ContinueOnError: opts.Continue,
		Adopt: adoptions, RetryUnknown: retryUnknown,
		OnProgress: func(progress runner.Progress) {
			if bar != nil {
				_ = bar.Set(progress.Completed)
			}
		},
	})
	if bar != nil {
		_ = bar.Finish()
	}
	printResult(out, errOut, result, stateFile)
	return runErr
}

func newProgressBar(errOut io.Writer, total int) *progressbar.ProgressBar {
	if !isTerminal(errOut) {
		return nil
	}
	return progressbar.NewOptions(
		total,
		progressbar.OptionSetWriter(errOut),
		progressbar.OptionSetDescription("Importing"),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(20),
		progressbar.OptionClearOnFinish(),
	)
}

// isPromptRequired reports the sentinel the non-interactive prompter returns when
// a value has to come from a prompt. It lets flag validation run before
// authentication without deciding the value yet.
func isPromptRequired(err error) bool {
	return err != nil && strings.Contains(err.Error(), "required in non-interactive mode")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isTerminal(value any) bool {
	file, ok := value.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
