package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/try-pulse/pulse-import/internal/auth"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
	"github.com/try-pulse/pulse-import/internal/version"
)

func NewRoot() *cobra.Command {
	opts := Options{}
	cmd := &cobra.Command{
		Use:     "pulse-import",
		Short:   "Import issues into Pulse (Jira CSV and more)",
		Version: version.Version,
		Long: `Import issues into Pulse (v1: Jira CSV).

Auth:
  export PULSE_ACCESS_TOKEN=<jwt>   # or PULSE_API_KEY
  # or paste when prompted

Non-interactive:
  PULSE_ACCESS_TOKEN=… pulse-import \
    --importer jira-csv \
    --file ./jira.csv \
    --workspace <workspace-id> \
    --team <team-id-or-name> \
    --jira-url https://acme.atlassian.net`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.APIURL, "api-url", "", "Pulse API base URL (default https://api.trypulse.tech/api/v1)")
	cmd.Flags().StringVar(&opts.Token, "token", "", "Pulse JWT access token (or env PULSE_ACCESS_TOKEN)")
	cmd.Flags().StringVar(&opts.Workspace, "workspace", "", "Workspace ID for X-Workspace-ID")
	cmd.Flags().StringVar(&opts.Importer, "importer", "", "Importer id (jira-csv)")
	cmd.Flags().StringVar(&opts.File, "file", "", "Path to source export file")
	cmd.Flags().StringVar(&opts.Team, "team", "", "Target team id or name")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Optional project id or name")
	cmd.Flags().StringVar(&opts.JiraURL, "jira-url", "", "Jira Cloud or on-prem base URL")
	cmd.Flags().BoolVar(&opts.SelfAssign, "self-assign", false, "Assign all imported issues to yourself")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Parse and map without creating issues")
	cmd.Flags().BoolVar(&opts.Continue, "continue-on-error", false, "Continue importing after a failed issue")
	cmd.Flags().BoolVar(&opts.NoPrompt, "yes", false, "Non-interactive: require flags/env, never prompt")
	cmd.SetVersionTemplate(fmt.Sprintf(
		"{{.Name}} {{.Version}}\ncommit: %s\ndate: %s\n",
		version.Commit,
		version.Date,
	))
	return cmd
}

func Execute() error {
	return NewRoot().Execute()
}

func runImport(ctx context.Context, opts Options) error {
	p := newPrompter(opts.NoPrompt)

	cfg, err := auth.Load()
	if err != nil {
		return err
	}

	apiURL := opts.APIURL
	if apiURL == "" {
		if cfg.APIURL != "" {
			apiURL = cfg.APIURL
		} else {
			apiURL = auth.DefaultAPIURL()
		}
	}
	apiURL = strings.TrimRight(apiURL, "/")

	token := strings.TrimSpace(opts.Token)
	if token == "" {
		token, err = auth.ResolveToken(cfg, opts.NoPrompt)
		if err != nil {
			return err
		}
	}

	workspaceID := strings.TrimSpace(opts.Workspace)
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(os.Getenv(auth.EnvWorkspace))
	}
	if workspaceID == "" && cfg.WorkspaceID != "" {
		workspaceID = cfg.WorkspaceID
	}

	client := pulseapi.New(apiURL, token, workspaceID)

	me, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	fmt.Println(successStyle.Render(fmt.Sprintf("Signed in as %s", displayUser(me))))

	if workspaceID == "" {
		workspaceID, err = pickWorkspace(ctx, client, p)
		if err != nil {
			return err
		}
		client.WorkspaceID = workspaceID
		cfg.WorkspaceID = workspaceID
		if err := auth.Save(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save config (%s): %v\n", auth.ConfigPathHint(), err)
		}
	} else {
		client.WorkspaceID = workspaceID
	}

	importerID := opts.Importer
	if importerID == "" {
		options := make([]huh.Option[string], 0, len(registry))
		for _, r := range registry {
			options = append(options, huh.NewOption(r.Label, r.ID))
		}
		importerID, err = p.Select("Which service would you like to import from?", options)
		if err != nil {
			return err
		}
	}

	reg, err := lookupImporter(importerID)
	if err != nil {
		return err
	}
	imp, err := reg.New(opts, p)
	if err != nil {
		return err
	}

	fmt.Printf("Parsing %s…\n", imp.Name())
	data, err := imp.Import()
	if err != nil {
		return err
	}
	fmt.Printf("Found %d issues, %d labels, %d users\n", len(data.Issues), len(data.Labels), len(data.Users))
	if len(data.Issues) == 0 {
		return fmt.Errorf("no issues to import")
	}

	teamID, err := resolveTeam(ctx, client, opts, imp.DefaultTeamName(), p)
	if err != nil {
		return err
	}

	projectID, err := resolveProject(ctx, client, opts, teamID, p)
	if err != nil {
		return err
	}

	mode, err := assigneeMode(opts, p)
	if err != nil {
		return err
	}

	r := runner.New(client)
	result, err := r.Run(ctx, data, runner.Options{
		TeamID:          teamID,
		ProjectID:       projectID,
		Assignee:        mode,
		SelfUserID:      me.ID,
		DryRun:          opts.DryRun,
		ContinueOnError: opts.Continue,
	})
	if err != nil && result == nil {
		return err
	}

	fmt.Println()
	if opts.DryRun {
		fmt.Println(successStyle.Render(fmt.Sprintf("Dry run OK — would create %d issues (%d with main docs)", result.Created, result.MainDocs)))
	} else {
		fmt.Println(successStyle.Render(fmt.Sprintf("Imported %d issues (%d main docs, %d failed)", result.Created, result.MainDocs, result.Failed)))
	}
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "  • %s\n", e)
	}
	return err
}
