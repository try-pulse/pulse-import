package cli

import (
	"context"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

func pickWorkspace(ctx context.Context, client *pulseapi.Client, p Prompter) (string, error) {
	memberships, err := client.ListMyWorkspaces(ctx)
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}
	options := workspaceOptions(memberships)
	if len(options) == 0 {
		id, err := p.Input("Workspace ID", "", required)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(id), nil
	}
	if _, ok := p.(nonInteractive); ok {
		if len(options) == 1 {
			return options[0].Value, nil
		}
		return "", fmt.Errorf("--workspace is required when multiple workspaces exist")
	}
	return p.Select("Import into workspace", options)
}

// resolveTeam picks the target team from the already-fetched team list. The list
// is shared with the assignee roster and estimate-settings lookups, so it is
// fetched once by the caller.
func resolveTeam(teams []pulseapi.Team, opts Options, p Prompter) (string, error) {
	if opts.Team != "" {
		for _, team := range teams {
			if team.ID == opts.Team {
				return team.ID, nil
			}
		}
		var matches []pulseapi.Team
		for _, team := range teams {
			if strings.EqualFold(team.Name, opts.Team) {
				matches = append(matches, team)
			}
		}
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("team %q not found", opts.Team)
		case 1:
			return matches[0].ID, nil
		default:
			return "", fmt.Errorf("team name %q is ambiguous; pass the team ID", opts.Team)
		}
	}

	if _, ok := p.(nonInteractive); ok {
		return "", fmt.Errorf("--team is required in non-interactive mode")
	}

	if len(teams) == 0 {
		return "", fmt.Errorf("no teams available; create a Pulse team before importing")
	}
	options := make([]huh.Option[string], 0, len(teams))
	for _, t := range teams {
		options = append(options, huh.NewOption(t.Name, t.ID))
	}
	return p.Select("Import into team", options)
}

func resolveProject(ctx context.Context, client *pulseapi.Client, opts Options, teamID string, p Prompter) (string, error) {
	if opts.Project != "" {
		projects, err := client.ListProjects(ctx)
		if err != nil {
			return "", err
		}
		for _, proj := range projects {
			if proj.ID == opts.Project {
				if proj.TeamID != teamID {
					return "", fmt.Errorf("project %q belongs to a different team", opts.Project)
				}
				return proj.ID, nil
			}
		}
		var matches []pulseapi.Project
		for _, proj := range projects {
			if strings.EqualFold(proj.Title, opts.Project) && proj.TeamID == teamID {
				matches = append(matches, proj)
			}
		}
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("project %q was not found in the selected team", opts.Project)
		case 1:
			return matches[0].ID, nil
		default:
			return "", fmt.Errorf("project name %q is ambiguous; pass the project ID", opts.Project)
		}
	}
	if _, ok := p.(nonInteractive); ok {
		return "", nil
	}

	projects, err := client.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	var teamProjects []pulseapi.Project
	for _, proj := range projects {
		if proj.TeamID == teamID {
			teamProjects = append(teamProjects, proj)
		}
	}
	if len(teamProjects) == 0 {
		return "", nil
	}

	include, err := p.Confirm("Import into a specific project?", false)
	if err != nil {
		return "", err
	}
	if !include {
		return "", nil
	}
	options := make([]huh.Option[string], 0, len(teamProjects))
	for _, proj := range teamProjects {
		options = append(options, huh.NewOption(proj.Title, proj.ID))
	}
	return p.Select("Project", options)
}
