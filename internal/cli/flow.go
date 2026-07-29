package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
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

func resolveTeam(ctx context.Context, client *pulseapi.Client, opts Options, defaultName string, p Prompter) (string, error) {
	teams, err := client.ListTeams(ctx)
	if err != nil {
		return "", fmt.Errorf("list teams: %w", err)
	}

	if opts.Team != "" {
		t := findTeam(teams, opts.Team)
		if t == nil {
			return "", fmt.Errorf("team %q not found", opts.Team)
		}
		return t.ID, nil
	}

	if _, ok := p.(nonInteractive); ok {
		return "", fmt.Errorf("--team is required in non-interactive mode")
	}

	createNew, err := p.Confirm("Create a new team for imported issues?", true)
	if err != nil {
		return "", err
	}
	if createNew {
		name, err := p.Input("Team name", defaultName, required)
		if err != nil {
			return "", err
		}
		team, err := client.CreateTeam(ctx, strings.TrimSpace(name))
		if err != nil {
			return "", fmt.Errorf("create team: %w", err)
		}
		fmt.Printf("Created team %s\n", team.Name)
		return team.ID, nil
	}

	if len(teams) == 0 {
		return "", fmt.Errorf("no teams available; create one first")
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
			if proj.ID == opts.Project || strings.EqualFold(proj.Name, opts.Project) {
				return proj.ID, nil
			}
		}
		return "", fmt.Errorf("project %q not found", opts.Project)
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
		if proj.TeamID == "" || proj.TeamID == teamID {
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
		options = append(options, huh.NewOption(proj.Name, proj.ID))
	}
	return p.Select("Project", options)
}
