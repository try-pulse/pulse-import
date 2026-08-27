package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

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

func resolveJiraURLs(jiraURL string, p Prompter) (site, custom string, err error) {
	jiraURL = strings.TrimSpace(jiraURL)
	if jiraURL == "" {
		isCloud, err := p.Confirm("Is your Jira installation on Jira Cloud (*.atlassian.net)?", true)
		if err != nil {
			return "", "", err
		}
		if isCloud {
			jiraURL, err = p.Input("Jira Cloud URL", "https://acme.atlassian.net", func(s string) error {
				parsedSite, _, parseErr := parseJiraURL(s)
				if parseErr != nil || parsedSite == "" {
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
	return parseJiraURL(jiraURL)
}

func parseJiraURL(raw string) (site, custom string, err error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid Jira URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("invalid Jira URL: scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("invalid Jira URL: host is required; credentials, query and fragment are not allowed")
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.HasSuffix(host, ".atlassian.net") {
		site = strings.TrimSuffix(host, ".atlassian.net")
		if site == "" || strings.Contains(site, ".") {
			return "", "", fmt.Errorf("invalid Jira Cloud host %q", host)
		}
		return site, "", nil
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return "", strings.TrimRight(parsed.String(), "/"), nil
}

func defaultStatePath(sourcePath string) string {
	return sourcePath + ".pulse-import.state.jsonl"
}

// workspaceOptions lists the workspaces the caller may actually import into.
// GET /workspaces/me returns every membership, including ones that are not
// active; offering those would hand the user a workspace whose every request
// comes back 403.
func workspaceOptions(memberships []pulseapi.WorkspaceMembership) []huh.Option[string] {
	var options []huh.Option[string]
	for _, m := range memberships {
		if m.Workspace == nil {
			continue
		}
		if m.Status != "" && !strings.EqualFold(m.Status, "active") {
			continue
		}
		label := fmt.Sprintf("%s (%s)", m.Workspace.Name, m.Workspace.Slug)
		options = append(options, huh.NewOption(label, m.Workspace.ID))
	}
	return options
}

func assigneeOptions() []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption("Keep mapped assignees when possible", string(runner.AssigneeMapped)),
		huh.NewOption("Assign to myself", string(runner.AssigneeSelf)),
		huh.NewOption("Leave unassigned", string(runner.AssigneeNone)),
	}
}

// teamPath returns the target team plus every ancestor, root last. Pulse accepts
// an assignee who is a member of any team on this path.
func teamPath(teams []pulseapi.Team, teamID string) []string {
	byID := make(map[string]pulseapi.Team, len(teams))
	for _, team := range teams {
		byID[team.ID] = team
	}
	var path []string
	seen := map[string]bool{}
	for current := teamID; current != "" && !seen[current]; {
		seen[current] = true
		path = append(path, current)
		team, ok := byID[current]
		if !ok || team.Parent == nil {
			break
		}
		current = team.Parent.ID
	}
	return path
}

// teamHasChildren reports whether any listed team names this one as its
// parent. Pulse only allows cycles on leaf teams.
func teamHasChildren(teams []pulseapi.Team, teamID string) bool {
	for _, team := range teams {
		if team.Parent != nil && team.Parent.ID == teamID {
			return true
		}
	}
	return false
}

func findTeam(teams []pulseapi.Team, teamID string) (pulseapi.Team, bool) {
	for _, team := range teams {
		if team.ID == teamID {
			return team, true
		}
	}
	return pulseapi.Team{}, false
}
