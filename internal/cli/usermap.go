package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"charm.land/huh/v2"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

// maxInteractiveMappings caps how many users the prompt walks through. A large
// Jira project can carry hundreds of names, and asking about every one of them
// turns a five-minute import into an interrogation.
const maxInteractiveMappings = 15

// mapUsersInteractively is Linear's "map users" step: for each source user the
// automatic match could not settle, offer the target team's members, or skip.
//
// Only members of the target team (or a parent team) are offered, because Pulse
// rejects any other assignee outright.
func mapUsersInteractively(
	ctx context.Context,
	out io.Writer,
	p Prompter,
	client teamMemberLister,
	sourceUsers map[string]importers.User,
	teamPath []string,
	existing map[string]string,
) (map[string]string, error) {
	if _, nonInteractiveRun := p.(nonInteractive); nonInteractiveRun {
		return existing, nil
	}
	if len(sourceUsers) == 0 {
		return existing, nil
	}

	members, err := collectMembers(ctx, client, teamPath)
	if err != nil {
		// Mapping is an assist, not a gate: the plan still reports every user it
		// could not resolve, and the import can proceed unassigned.
		_, _ = fmt.Fprintf(out, "warning: could not read team members for the mapping step: %v\n", err)
		return existing, nil
	}
	if len(members) == 0 {
		_, _ = fmt.Fprintln(out, "The target team has no members, so imported issues will be unassigned.")
		return existing, nil
	}

	people := indexMembers(members)
	unresolved := unresolvedUsers(sourceUsers, people, existing)
	if len(unresolved) == 0 {
		return existing, nil
	}

	_, _ = fmt.Fprintf(out, "\n%d Jira user(s) could not be matched automatically.\n", len(unresolved))
	proceed, err := p.Confirm("Map them to Pulse users now?", true)
	if err != nil {
		return nil, err
	}
	if !proceed {
		return existing, nil
	}

	mapped := make(map[string]string, len(existing)+len(unresolved))
	for key, value := range existing {
		mapped[key] = value
	}

	options := memberOptions(members)
	shown := unresolved
	if len(shown) > maxInteractiveMappings {
		shown = shown[:maxInteractiveMappings]
		_, _ = fmt.Fprintf(out,
			"Showing the %d busiest; map the rest with --map-user \"NAME=PULSE-USER-ID\".\n",
			maxInteractiveMappings,
		)
	}
	for _, user := range shown {
		title := fmt.Sprintf("Jira user %q (%d issue(s))", user.Name, user.Rows)
		if user.Email != "" {
			title = fmt.Sprintf("Jira user %q <%s> (%d issue(s))", user.Name, user.Email, user.Rows)
		}
		choice, err := p.Select(title, options)
		if err != nil {
			return nil, err
		}
		mapped[strings.ToLower(user.Name)] = choice
	}
	return mapped, nil
}

type teamMemberLister interface {
	ListTeamMembers(context.Context, string) ([]pulseapi.TeamMember, error)
}

func collectMembers(ctx context.Context, client teamMemberLister, teamPath []string) ([]pulseapi.TeamMember, error) {
	var members []pulseapi.TeamMember
	seenTeams := map[string]bool{}
	seenUsers := map[string]bool{}
	for _, teamID := range teamPath {
		if teamID == "" || seenTeams[teamID] {
			continue
		}
		seenTeams[teamID] = true
		batch, err := client.ListTeamMembers(ctx, teamID)
		if err != nil {
			return nil, err
		}
		for _, member := range batch {
			if member.ID == "" || seenUsers[member.ID] {
				continue
			}
			seenUsers[member.ID] = true
			members = append(members, member)
		}
	}
	return members, nil
}

func indexMembers(members []pulseapi.TeamMember) map[string]int {
	index := map[string]int{}
	for _, member := range members {
		for _, key := range memberKeys(member) {
			index[key]++
		}
	}
	return index
}

func memberKeys(member pulseapi.TeamMember) []string {
	var keys []string
	if email := strings.ToLower(strings.TrimSpace(member.Email)); email != "" {
		keys = append(keys, email)
		if local, _, found := strings.Cut(email, "@"); found {
			keys = append(keys, local)
		}
	}
	if name := strings.ToLower(strings.TrimSpace(member.FirstName + " " + member.LastName)); name != "" {
		keys = append(keys, name)
	}
	return keys
}

// unresolvedUsers lists the source users that need a decision: no unique match
// on email or name, and no explicit mapping already supplied.
func unresolvedUsers(
	sourceUsers map[string]importers.User,
	people map[string]int,
	existing map[string]string,
) []importers.User {
	var out []importers.User
	for key, user := range sourceUsers {
		if _, mapped := existing[key]; mapped {
			continue
		}
		resolved := false
		for _, candidate := range []string{user.Email, user.Name, key} {
			candidate = strings.ToLower(strings.TrimSpace(candidate))
			if candidate != "" && people[candidate] == 1 {
				resolved = true
				break
			}
		}
		if !resolved {
			out = append(out, user)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Rows != out[b].Rows {
			return out[a].Rows > out[b].Rows
		}
		return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
	})
	return out
}

func memberOptions(members []pulseapi.TeamMember) []huh.Option[string] {
	sorted := append([]pulseapi.TeamMember(nil), members...)
	sort.Slice(sorted, func(a, b int) bool {
		return strings.ToLower(memberLabel(sorted[a])) < strings.ToLower(memberLabel(sorted[b]))
	})
	options := make([]huh.Option[string], 0, len(sorted)+1)
	options = append(options, huh.NewOption("Leave unassigned", runner.SkipUser))
	for _, member := range sorted {
		options = append(options, huh.NewOption(memberLabel(member), member.ID))
	}
	return options
}

func memberLabel(member pulseapi.TeamMember) string {
	name := strings.TrimSpace(member.FirstName + " " + member.LastName)
	switch {
	case name != "" && member.Email != "":
		return name + " <" + member.Email + ">"
	case name != "":
		return name
	default:
		return member.Email
	}
}
