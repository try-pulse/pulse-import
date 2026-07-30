package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

type memberLister struct {
	byTeam map[string][]pulseapi.TeamMember
	err    error
	calls  int
}

func (m *memberLister) ListTeamMembers(_ context.Context, teamID string) ([]pulseapi.TeamMember, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.byTeam[teamID], nil
}

// choicePrompter answers Select from a queue, so a multi-user mapping walk can be
// scripted end to end.
type choicePrompter struct {
	confirm bool
	choices []string
	titles  []string
}

func (p *choicePrompter) Select(title string, _ []huh.Option[string]) (string, error) {
	p.titles = append(p.titles, title)
	if len(p.choices) == 0 {
		return "", errors.New("no scripted choice left")
	}
	choice := p.choices[0]
	p.choices = p.choices[1:]
	return choice, nil
}

func (p *choicePrompter) Input(string, string, func(string) error) (string, error) {
	return "", errors.New("unexpected Input")
}

func (p *choicePrompter) Secret(string, string, func(string) error) (string, error) {
	return "", errors.New("unexpected Secret")
}

func (p *choicePrompter) Confirm(string, bool) (bool, error) { return p.confirm, nil }

func sourceUsers(names ...string) map[string]importers.User {
	out := map[string]importers.User{}
	for index, name := range names {
		out[strings.ToLower(name)] = importers.User{Name: name, Rows: len(names) - index}
	}
	return out
}

func TestMapUsersInteractivelyOnlyAsksAboutUnresolvedNames(t *testing.T) {
	t.Parallel()
	client := &memberLister{byTeam: map[string][]pulseapi.TeamMember{
		"team-1": {
			{ID: "user-ada", FirstName: "Ada", LastName: "Lovelace", Email: "ada@acme.com"},
			{ID: "user-jane", FirstName: "Jane", LastName: "Doe", Email: "jane@acme.com"},
		},
	}}
	// Ada matches automatically; Grace does not.
	users := sourceUsers("Ada Lovelace", "Grace Hopper")
	prompter := &choicePrompter{confirm: true, choices: []string{"user-jane"}}

	var out bytes.Buffer
	got, err := mapUsersInteractively(
		context.Background(), &out, prompter, client, users, []string{"team-1"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompter.titles) != 1 {
		t.Fatalf("asked %d question(s): %v; only unmatched users need a decision",
			len(prompter.titles), prompter.titles)
	}
	if !strings.Contains(prompter.titles[0], "Grace Hopper") {
		t.Errorf("asked about the wrong user: %q", prompter.titles[0])
	}
	if got["grace hopper"] != "user-jane" {
		t.Errorf("mapping = %v", got)
	}
	if _, mapped := got["ada lovelace"]; mapped {
		t.Error("an automatic match must not be overridden by the prompt step")
	}
}

func TestMapUsersInteractivelyRespectsDecline(t *testing.T) {
	t.Parallel()
	client := &memberLister{byTeam: map[string][]pulseapi.TeamMember{
		"team-1": {{ID: "user-ada", FirstName: "Ada", LastName: "Lovelace", Email: "ada@acme.com"}},
	}}
	prompter := &choicePrompter{confirm: false}

	var out bytes.Buffer
	got, err := mapUsersInteractively(
		context.Background(), &out, prompter, client, sourceUsers("Grace Hopper"),
		[]string{"team-1"}, map[string]string{"existing": "user-x"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompter.titles) != 0 {
		t.Fatalf("declining must skip the walk, got %v", prompter.titles)
	}
	if got["existing"] != "user-x" {
		t.Fatalf("existing mappings must be preserved: %v", got)
	}
}

// The mapping step is an assist, not a gate: a roster it cannot read must not
// stop the import, because the plan already reports every unresolved user.
func TestMapUsersInteractivelySurvivesARosterError(t *testing.T) {
	t.Parallel()
	client := &memberLister{err: errors.New("boom")}
	prompter := &choicePrompter{confirm: true}

	var out bytes.Buffer
	got, err := mapUsersInteractively(
		context.Background(), &out, prompter, client, sourceUsers("Grace Hopper"),
		[]string{"team-1"}, map[string]string{"a": "b"},
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got["a"] != "b" {
		t.Fatalf("mapping = %v", got)
	}
	if !strings.Contains(out.String(), "could not read team members") {
		t.Errorf("the user should be told why the step was skipped: %q", out.String())
	}
}

func TestMapUsersInteractivelyIsSkippedNonInteractively(t *testing.T) {
	t.Parallel()
	client := &memberLister{byTeam: map[string][]pulseapi.TeamMember{"team-1": {{ID: "u"}}}}
	var out bytes.Buffer
	got, err := mapUsersInteractively(
		context.Background(), &out, nonInteractive{}, client,
		sourceUsers("Grace Hopper"), []string{"team-1"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("mapping = %v, want the input unchanged", got)
	}
	if client.calls != 0 {
		t.Error("a non-interactive run must not fetch the roster for the prompt step")
	}
}

func TestMemberOptionsAlwaysOfferUnassigned(t *testing.T) {
	t.Parallel()
	options := memberOptions([]pulseapi.TeamMember{
		{ID: "b", FirstName: "Zoe", Email: "zoe@acme.com"},
		{ID: "a", FirstName: "Ada", Email: "ada@acme.com"},
	})
	if len(options) != 3 {
		t.Fatalf("options = %d, want 2 members plus the unassigned choice", len(options))
	}
	if options[0].Value != runner.SkipUser {
		t.Fatalf("first option = %q, want the unassigned escape hatch first", options[0].Value)
	}
	if options[1].Key != "Ada <ada@acme.com>" {
		t.Fatalf("members are not sorted by label: %q", options[1].Key)
	}
}

func TestCollectMembersDeduplicatesAcrossTheTeamPath(t *testing.T) {
	t.Parallel()
	shared := pulseapi.TeamMember{ID: "user-shared", FirstName: "Shared", Email: "s@acme.com"}
	client := &memberLister{byTeam: map[string][]pulseapi.TeamMember{
		"team-child":  {shared, {ID: "user-child", FirstName: "Child", Email: "c@acme.com"}},
		"team-parent": {shared, {ID: "user-parent", FirstName: "Parent", Email: "p@acme.com"}},
	}}
	members, err := collectMembers(context.Background(), client, []string{"team-child", "team-parent", "team-child"})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("members = %d, want 3 distinct users", len(members))
	}
	if client.calls != 2 {
		t.Fatalf("fetched %d times, want one per distinct team", client.calls)
	}
}
