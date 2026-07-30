package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"

	"github.com/try-pulse/pulse-import/internal/cli/tui"
	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importers/jiracsv"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

const wizardSteps = 4

// wizardState holds answers across interactive phases so Back can restore them.
type wizardState struct {
	source        sourceAnswers
	data          *importers.ImportResult
	teamID        string
	projectID     string
	assignee      runner.AssigneeMode
	userMap       map[string]string
	parsedFile    string // abs path last successfully parsed
	revisitSource bool   // true after Back from Destination — re-open the Source form
}

type wizardPhase int

const (
	phaseSource wizardPhase = iota
	phaseDestination
	phaseMapping
	phaseReview
	phaseDone
)

func (p wizardPhase) stepNumber() int {
	switch p {
	case phaseSource:
		return 1
	case phaseDestination:
		return 2
	case phaseMapping:
		return 3
	case phaseReview:
		return 4
	default:
		return 5
	}
}

func (p wizardPhase) title() string {
	switch p {
	case phaseSource:
		return "Source"
	case phaseDestination:
		return "Destination"
	case phaseMapping:
		return "User mapping"
	case phaseReview:
		return "Review"
	default:
		return "Import"
	}
}

// errDryRunDone stops the import after a successful dry-run plan.
var errDryRunDone = errors.New("dry-run complete")

// errNothingToImport stops when the plan is empty after filtering.
var errNothingToImport = errors.New("nothing to import")

// runWizardPhases drives Source → Destination → Mapping → Review with Esc/abort = Back.
func runWizardPhases(
	ctx context.Context,
	cmdOut, errOut io.Writer,
	sess *session,
	opts *Options,
	epics jiracsv.EpicMode,
	prompter Prompter,
	userMap map[string]string,
	mode runner.AssigneeMode,
) (*wizardState, *runner.Plan, error) {
	state := &wizardState{
		userMap:  cloneStringMap(userMap),
		assignee: mode,
		source: sourceAnswers{
			ImporterID: strings.TrimSpace(opts.Importer),
			FilePath:   strings.TrimSpace(opts.File),
			JiraURL:    strings.TrimSpace(opts.JiraURL),
			IsCloud:    true,
		},
		teamID:    strings.TrimSpace(opts.Team),
		projectID: strings.TrimSpace(opts.Project),
	}

	styles := reviewStyles(cmdOut)
	phase := phaseSource
	_, isNonInteractive := prompter.(nonInteractive)
	interactive := !isNonInteractive

	for phase != phaseDone {
		if interactive {
			tui.StepBanner(cmdOut, styles, phase.stepNumber(), wizardSteps, phase.title())
			if phase > phaseSource {
				_, _ = fmt.Fprintln(errOut, styles.MutedText("  Esc / Ctrl+C returns to the previous step"))
			}
		}

		var err error
		switch phase {
		case phaseSource:
			err = wizardSource(ctx, cmdOut, errOut, opts, epics, prompter, state)
			if err == nil {
				phase = phaseDestination
			}
		case phaseDestination:
			err = wizardDestination(ctx, sess, opts, withBack(prompter), state)
			if errors.Is(err, ErrBack) {
				state.revisitSource = true
				phase = phaseSource
				continue
			}
			if err == nil {
				if state.assignee == runner.AssigneeMapped {
					phase = phaseMapping
				} else {
					phase = phaseReview
				}
			}
		case phaseMapping:
			err = wizardMapping(ctx, cmdOut, sess, withBack(prompter), state)
			if errors.Is(err, ErrBack) {
				phase = phaseDestination
				continue
			}
			if err == nil {
				phase = phaseReview
			}
		case phaseReview:
			var plan *runner.Plan
			plan, err = wizardReview(ctx, cmdOut, sess, opts, withBack(prompter), state)
			if errors.Is(err, ErrBack) {
				if state.assignee == runner.AssigneeMapped {
					phase = phaseMapping
				} else {
					phase = phaseDestination
				}
				continue
			}
			if err == nil {
				return state, plan, nil
			}
			return state, plan, err
		default:
			return nil, nil, fmt.Errorf("internal: unknown wizard phase %d", phase)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return state, nil, nil
}

func wizardSource(
	ctx context.Context,
	out, errOut io.Writer,
	opts *Options,
	epics jiracsv.EpicMode,
	prompter Prompter,
	state *wizardState,
) error {
	if state.source.ImporterID == "" && len(registry) == 1 {
		state.source.ImporterID = registry[0].ID
	}
	options := make([]huh.Option[string], 0, len(registry))
	for _, registration := range registry {
		options = append(options, huh.NewOption(registration.Label, registration.ID))
	}

	needForm := state.source.ImporterID == "" || state.source.FilePath == "" || state.source.JiraURL == ""
	_, nonInteractiveRun := prompter.(nonInteractive)
	if needForm || (state.revisitSource && !nonInteractiveRun) {
		state.revisitSource = false
		if nonInteractiveRun {
			if state.source.ImporterID == "" {
				return fmt.Errorf("Which service would you like to import from?: required in non-interactive mode (pass --importer)")
			}
			if state.source.FilePath == "" {
				return fmt.Errorf("Path to Jira CSV export: required in non-interactive mode (pass --file)")
			}
			if state.source.JiraURL == "" {
				return fmt.Errorf("Jira base URL: required in non-interactive mode (pass --jira-url)")
			}
		} else {
			// Revisit with answers already filled → force every Source field again.
			force := !needForm
			answers, err := runSourceForm(prompter, options, sourceDefaults{
				ImporterID: state.source.ImporterID,
				FilePath:   state.source.FilePath,
				JiraURL:    state.source.JiraURL,
				IsCloud:    state.source.IsCloud,
			}, force)
			if err != nil {
				return err
			}
			if state.parsedFile != "" && answers.FilePath != state.parsedFile {
				state.data = nil
				state.parsedFile = ""
			}
			state.source = answers
		}
	} else if state.source.FilePath != "" {
		path, err := absExisting(state.source.FilePath)
		if err != nil {
			return err
		}
		state.source.FilePath = path
	}

	opts.Importer = state.source.ImporterID
	opts.File = state.source.FilePath
	opts.JiraURL = state.source.JiraURL

	if state.data != nil && state.parsedFile == state.source.FilePath {
		return nil
	}

	registration, err := lookupImporter(state.source.ImporterID)
	if err != nil {
		return err
	}
	importer, err := registration.New(*opts, epics, nonInteractive{})
	if err != nil {
		importer, err = registration.New(*opts, epics, prompter)
		if err != nil {
			return err
		}
	}

	tui.StatusLine(errOut, fmt.Sprintf("Parsing %s…", importer.Name()))
	data, err := importer.Import(ctx)
	if err != nil {
		return fmt.Errorf("parse %s: %w\nHint: export Excel CSV (all fields) from Jira Advanced issue search and pass --file", importer.Name(), err)
	}
	_, _ = fmt.Fprintf(out, "Parsed %d issue(s), %d epic(s), %d label(s), %d user(s)\n",
		len(data.Issues), len(data.Projects), len(data.Labels), len(data.Users))
	state.data = data
	state.parsedFile = state.source.FilePath
	return nil
}

func wizardDestination(
	ctx context.Context,
	sess *session,
	opts *Options,
	prompter Prompter,
	state *wizardState,
) error {
	teams, err := sess.client.ListTeams(ctx)
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}

	teamID, err := resolveTeam(teams, *opts, prompter)
	if err != nil {
		return err
	}
	state.teamID = teamID

	projectID, err := resolveProject(ctx, sess.client, *opts, teamID, prompter)
	if err != nil {
		return err
	}
	state.projectID = projectID

	if opts.Assignee != "" || opts.SelfAssign {
		mode, err := assigneeMode(*opts, nonInteractive{})
		if err != nil {
			return err
		}
		state.assignee = mode
		return nil
	}
	if _, ok := prompter.(nonInteractive); ok {
		if state.assignee == "" {
			state.assignee = runner.AssigneeMapped
		}
		return nil
	}
	mode, err := assigneeMode(Options{}, prompter)
	if err != nil {
		return err
	}
	state.assignee = mode
	return nil
}

func wizardMapping(
	ctx context.Context,
	out io.Writer,
	sess *session,
	prompter Prompter,
	state *wizardState,
) error {
	if state.assignee != runner.AssigneeMapped || state.data == nil {
		return nil
	}
	teams, err := sess.client.ListTeams(ctx)
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}
	path := teamPath(teams, state.teamID)
	mapped, err := mapUsersInteractively(ctx, out, prompter, sess.client, state.data.Users, path, state.userMap)
	if err != nil {
		return err
	}
	state.userMap = mapped
	return nil
}

func wizardReview(
	ctx context.Context,
	out io.Writer,
	sess *session,
	opts *Options,
	prompter Prompter,
	state *wizardState,
) (*runner.Plan, error) {
	teams, err := sess.client.ListTeams(ctx)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	estimateSettings := pulseapi.EstimateSettings{}
	if team, ok := findTeam(teams, state.teamID); ok {
		estimateSettings = team.EstimateSettings
	}

	skipStatuses, err := parseStatusFilter(opts.SkipStatus, "--skip-status")
	if err != nil {
		return nil, err
	}
	onlyStatuses, err := parseStatusFilter(opts.OnlyStatus, "--only-status")
	if err != nil {
		return nil, err
	}
	staleAfter, err := parseStale(opts.SkipStale)
	if err != nil {
		return nil, err
	}

	engine := runner.New(sess.client)
	plan, err := engine.Prepare(ctx, state.data, runner.Options{
		ImporterID:       state.source.ImporterID,
		APIURL:           sess.apiURL,
		WorkspaceID:      sess.workspaceID,
		TeamID:           state.teamID,
		TeamPath:         teamPath(teams, state.teamID),
		ProjectID:        state.projectID,
		Assignee:         state.assignee,
		SelfUserID:       sess.me.ID,
		UserMap:          state.userMap,
		EstimateSettings: estimateSettings,
		LabelPolicy:      labelPolicy(*opts),
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
		return nil, err
	}
	printPlan(out, plan, opts.DryRun)
	if !plan.Valid() {
		return nil, fmt.Errorf(
			"preflight failed with %d validation error(s); no Pulse changes were made", len(plan.Errors))
	}
	if opts.DryRun {
		_, _ = fmt.Fprintf(out, "%s\n", reviewStyles(out).OKLine("Dry run OK — no Pulse changes were made"))
		return plan, errDryRunDone
	}
	if len(plan.Items) == 0 {
		_, _ = fmt.Fprintln(out, "Nothing to import after filtering — no Pulse changes were made")
		return plan, errNothingToImport
	}

	if !opts.NoPrompt {
		confirmed, err := prompter.Confirm("Proceed with this import plan?", false)
		if err != nil {
			return nil, err
		}
		if !confirmed {
			_, _ = fmt.Fprintln(out, "Import canceled — no Pulse changes were made")
			return nil, ErrCanceled
		}
	}
	return plan, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
