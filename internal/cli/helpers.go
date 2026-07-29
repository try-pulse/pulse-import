package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

type Options struct {
	APIURL     string
	Token      string
	Workspace  string
	Importer   string
	File       string
	Team       string
	Project    string
	JiraURL    string
	SelfAssign bool
	DryRun     bool
	Continue   bool
	NoPrompt   bool
}

var jiraCloudRE = regexp.MustCompile(`(?i)^https?://([^\./]+)\.atlassian\.net`)

func displayUser(u *pulseapi.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	return u.Email
}

func required(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

func validateFileExists(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required")
	}
	if _, err := os.Stat(s); err != nil {
		return fmt.Errorf("file not found: %s", s)
	}
	return nil
}

func absExisting(path string) (string, error) {
	path = strings.TrimSpace(path)
	if err := validateFileExists(path); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func findTeam(teams []pulseapi.Team, q string) *pulseapi.Team {
	for i := range teams {
		if teams[i].ID == q || strings.EqualFold(teams[i].Name, q) {
			return &teams[i]
		}
	}
	return nil
}

func resolveJiraURLs(jiraURL string, p Prompter) (site, custom string, err error) {
	jiraURL = strings.TrimSpace(jiraURL)
	if jiraURL == "" {
		isCloud, err := p.Confirm("Is your Jira installation on Jira Cloud (*.atlassian.net)?", true)
		if err != nil {
			return "", "", err
		}
		if isCloud {
			jiraURL, err = p.Input("Jira Cloud URL", "https://acme.atlassian.net", func(s string) error {
				if !jiraCloudRE.MatchString(strings.TrimSpace(s)) {
					return fmt.Errorf("expected https://<site>.atlassian.net")
				}
				return nil
			})
		} else {
			jiraURL, err = p.Input("On-prem Jira URL", "https://jira.mydomain.com", required)
		}
		if err != nil {
			return "", "", err
		}
	}
	jiraURL = strings.TrimSpace(jiraURL)
	if m := jiraCloudRE.FindStringSubmatch(jiraURL); len(m) == 2 {
		return m[1], "", nil
	}
	if jiraURL != "" {
		return "", strings.TrimRight(jiraURL, "/"), nil
	}
	return "", "", nil
}

func workspaceOptions(memberships []pulseapi.WorkspaceMembership) []huh.Option[string] {
	var options []huh.Option[string]
	for _, m := range memberships {
		if m.Workspace == nil {
			continue
		}
		label := fmt.Sprintf("%s (%s)", m.Workspace.Name, m.Workspace.Slug)
		options = append(options, huh.NewOption(label, m.Workspace.ID))
	}
	return options
}

func assigneeMode(opts Options, p Prompter) (runner.AssigneeMode, error) {
	if opts.SelfAssign {
		return runner.AssigneeSelf, nil
	}
	if opts.NoPrompt {
		return runner.AssigneeMapped, nil
	}
	v, err := p.Select("Assignee strategy", []huh.Option[string]{
		huh.NewOption("Assign to myself", string(runner.AssigneeSelf)),
		huh.NewOption("Keep mapped assignees when possible", string(runner.AssigneeMapped)),
		huh.NewOption("Leave unassigned", string(runner.AssigneeNone)),
	})
	if err != nil {
		return "", err
	}
	return runner.AssigneeMode(v), nil
}
