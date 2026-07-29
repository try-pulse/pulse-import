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

type Options struct {
	APIURL       string
	Workspace    string
	Importer     string
	File         string
	Team         string
	Project      string
	JiraURL      string
	SelfAssign   bool
	DryRun       bool
	Continue     bool
	NoPrompt     bool
	StateFile    string
	Adopt        []string
	RetryUnknown []string
}

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

func parseKeyValues(values []string, flagName string) (map[string]string, error) {
	out := map[string]string{}
	for _, value := range values {
		key, target, ok := strings.Cut(value, "=")
		key, target = strings.TrimSpace(key), strings.TrimSpace(target)
		if !ok || key == "" || target == "" {
			return nil, fmt.Errorf("%s expects JIRA-KEY=PULSE-ISSUE-ID, got %q", flagName, value)
		}
		folded := strings.ToLower(key)
		if _, exists := out[folded]; exists {
			return nil, fmt.Errorf("%s repeats Jira key %q", flagName, key)
		}
		out[folded] = target
	}
	return out, nil
}

func parseKeys(values []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			return nil, fmt.Errorf("--retry-unknown requires a Jira key")
		}
		if out[key] {
			return nil, fmt.Errorf("--retry-unknown repeats Jira key %q", value)
		}
		out[key] = true
	}
	return out, nil
}

func defaultStatePath(sourcePath string) string {
	return sourcePath + ".pulse-import.state.jsonl"
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
