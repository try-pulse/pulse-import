package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

type scriptedPrompter struct {
	selectValue string
	inputs      []string
	confirms    []bool
}

func mustPulseClient(t testing.TB, baseURL, token, workspaceID string) *pulseapi.Client {
	t.Helper()
	client, err := pulseapi.New(baseURL, token, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (p *scriptedPrompter) Select(string, []huh.Option[string]) (string, error) {
	return p.selectValue, nil
}

func (p *scriptedPrompter) Input(_ string, _ string, validate func(string) error) (string, error) {
	value := p.inputs[0]
	p.inputs = p.inputs[1:]
	if validate != nil {
		return value, validate(value)
	}
	return value, nil
}

func (p *scriptedPrompter) Secret(title, description string, validate func(string) error) (string, error) {
	return p.Input(title, description, validate)
}

func (p *scriptedPrompter) Confirm(string, bool) (bool, error) {
	value := p.confirms[0]
	p.confirms = p.confirms[1:]
	return value, nil
}

func TestParseJiraURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url     string
		site    string
		custom  string
		wantErr bool
	}{
		{url: "https://acme.atlassian.net", site: "acme"},
		{url: "https://Acme.atlassian.net/jira", site: "acme"},
		{url: "https://jira.example.com/base/", custom: "https://jira.example.com/base"},
		{url: "not-a-url", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			site, custom, err := parseJiraURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || site != tt.site || custom != tt.custom {
				t.Fatalf("site=%q custom=%q err=%v", site, custom, err)
			}
		})
	}
}

func TestRequired(t *testing.T) {
	t.Parallel()
	if err := required("  "); err == nil {
		t.Fatal("expected error")
	}
	if err := required("ok"); err != nil {
		t.Fatal(err)
	}
}

func TestDisplayUser(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		u    pulseapi.User
		want string
	}{
		{name: "display", u: pulseapi.User{DisplayName: "Ada", Email: "a@b.c"}, want: "Ada"},
		{name: "full name", u: pulseapi.User{FirstName: "Ada", LastName: "Lovelace", Email: "a@b.c"}, want: "Ada Lovelace"},
		{name: "email", u: pulseapi.User{Email: "a@b.c"}, want: "a@b.c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := displayUser(&tt.u); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestNewJiraCSVImporter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(path, []byte("Summary,Issue key\nX,K-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := nonInteractive{}
	opts := Options{
		File:     path,
		JiraURL:  "https://acme.atlassian.net",
		NoPrompt: true,
	}

	reg, err := lookupImporter("jira-csv")
	if err != nil {
		t.Fatal(err)
	}
	imp, err := reg.New(opts, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(imp.Name(), "Jira") {
		t.Fatalf("name=%q", imp.Name())
	}

	if _, err := lookupImporter("nope"); err == nil {
		t.Fatal("expected unknown importer error")
	}

	opts.JiraURL = "https://jira.corp.example"
	reg, err = lookupImporter("jira-csv")
	if err != nil {
		t.Fatal(err)
	}
	imp, err = reg.New(opts, p)
	if err != nil {
		t.Fatal(err)
	}
	res, err := imp.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || !strings.Contains(res.Issues[0].BodyMarkdown, "https://jira.corp.example/browse/K-1") {
		t.Fatalf("%+v", res.Issues)
	}
}

func TestPickWorkspace_NoPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    any
		wantID  string
		wantErr string
	}{
		{
			name: "single",
			body: map[string]any{"data": []map[string]any{{
				"workspace": map[string]string{"id": "only", "slug": "o", "name": "Only"},
			}}},
			wantID: "only",
		},
		{
			name: "multiple",
			body: map[string]any{"data": []map[string]any{
				{"workspace": map[string]string{"id": "a", "slug": "a", "name": "A"}},
				{"workspace": map[string]string{"id": "b", "slug": "b", "name": "B"}},
			}},
			wantErr: "--workspace is required when multiple",
		},
		{
			name:    "empty",
			body:    map[string]any{"data": []any{}},
			wantErr: "required in non-interactive mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.body)
			}))
			t.Cleanup(srv.Close)

			id, err := pickWorkspace(context.Background(), mustPulseClient(t, srv.URL, "t", ""), nonInteractive{})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || id != tt.wantID {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
}

func TestResolveTeamAndProject_Flags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/teams":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []pulseapi.Team{{ID: "tid", Name: "Eng"}},
			})
		case "/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []pulseapi.Project{{ID: "pid", Name: "App", TeamID: "tid"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := mustPulseClient(t, srv.URL, "tok", "ws")
	p := nonInteractive{}

	tid, err := resolveTeam(context.Background(), client, Options{Team: "Eng", NoPrompt: true}, p)
	if err != nil || tid != "tid" {
		t.Fatalf("team=%q err=%v", tid, err)
	}

	pid, err := resolveProject(context.Background(), client, Options{Project: "App", NoPrompt: true}, "tid", p)
	if err != nil || pid != "pid" {
		t.Fatalf("project=%q err=%v", pid, err)
	}

	if _, err := resolveTeam(context.Background(), client, Options{Team: "missing", NoPrompt: true}, p); err == nil {
		t.Fatal("expected missing team error")
	}

	if _, err := resolveTeam(context.Background(), client, Options{NoPrompt: true}, p); err == nil {
		t.Fatal("expected --team required")
	}
}

func TestNonInteractivePrompter(t *testing.T) {
	t.Parallel()
	p := nonInteractive{}
	if _, err := p.Select("x", nil); err == nil {
		t.Fatal("expected select error")
	}
	if _, err := p.Input("y", "", nil); err == nil {
		t.Fatal("expected input error")
	}
	if _, err := p.Confirm("z", true); err == nil {
		t.Fatal("expected confirm error")
	}
}

func TestAssigneeMode(t *testing.T) {
	t.Parallel()
	mode, err := assigneeMode(Options{SelfAssign: true}, nonInteractive{})
	if err != nil || mode != "self" {
		t.Fatalf("got %q err=%v", mode, err)
	}
	mode, err = assigneeMode(Options{NoPrompt: true}, nonInteractive{})
	if err != nil || mode != "mapped" {
		t.Fatalf("got %q err=%v", mode, err)
	}
}

func TestResolveProjectRejectsWrongTeamAndAmbiguousName(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []pulseapi.Project{
			{ID: "other", Name: "Other", TeamID: "team-2"},
			{ID: "missing-team", Name: "Missing Team"},
			{ID: "one", Name: "Same", TeamID: "team-1"},
			{ID: "two", Name: "Same", TeamID: "team-1"},
		}})
	}))
	t.Cleanup(server.Close)
	client := mustPulseClient(t, server.URL, "token", "workspace")
	if _, err := resolveProject(
		context.Background(), client, Options{Project: "other"}, "team-1", nonInteractive{},
	); err == nil || !strings.Contains(err.Error(), "different team") {
		t.Fatalf("wrong-team err=%v", err)
	}
	if _, err := resolveProject(
		context.Background(), client, Options{Project: "missing-team"}, "team-1", nonInteractive{},
	); err == nil || !strings.Contains(err.Error(), "different team") {
		t.Fatalf("missing-team err=%v", err)
	}
	if _, err := resolveProject(
		context.Background(), client, Options{Project: "Same"}, "team-1", nonInteractive{},
	); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous err=%v", err)
	}
}

func TestStateFlagParsing(t *testing.T) {
	t.Parallel()
	adopt, err := parseKeyValues([]string{"ENG-1=issue-1"}, "--adopt")
	if err != nil || adopt["eng-1"] != "issue-1" {
		t.Fatalf("adopt=%v err=%v", adopt, err)
	}
	if _, err := parseKeyValues([]string{"bad"}, "--adopt"); err == nil {
		t.Fatal("expected invalid adopt error")
	}
	retry, err := parseKeys([]string{"ENG-1"})
	if err != nil || !retry["eng-1"] {
		t.Fatalf("retry=%v err=%v", retry, err)
	}
	if _, err := parseKeys([]string{"ENG-1", "eng-1"}); err == nil {
		t.Fatal("expected duplicate retry error")
	}
	if got := defaultStatePath("/tmp/input.csv"); got != "/tmp/input.csv.pulse-import.state.jsonl" {
		t.Fatalf("state path=%q", got)
	}
}

func TestInteractiveFlowChoices(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workspaces/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case "/teams":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []pulseapi.Team{{ID: "team-1", Name: "Team"}}})
		case "/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []pulseapi.Project{{ID: "project-1", Name: "Project", TeamID: "team-1"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client := mustPulseClient(t, server.URL, "token", "")

	workspacePrompter := &scriptedPrompter{inputs: []string{"workspace-1"}}
	workspace, err := pickWorkspace(context.Background(), client, workspacePrompter)
	if err != nil || workspace != "workspace-1" {
		t.Fatalf("workspace=%q err=%v", workspace, err)
	}
	team, err := resolveTeam(
		context.Background(), client, Options{}, &scriptedPrompter{selectValue: "team-1"},
	)
	if err != nil || team != "team-1" {
		t.Fatalf("team=%q err=%v", team, err)
	}
	project, err := resolveProject(
		context.Background(), client, Options{}, "team-1",
		&scriptedPrompter{selectValue: "project-1", confirms: []bool{true}},
	)
	if err != nil || project != "project-1" {
		t.Fatalf("project=%q err=%v", project, err)
	}
	noProject, err := resolveProject(
		context.Background(), client, Options{}, "team-1",
		&scriptedPrompter{confirms: []bool{false}},
	)
	if err != nil || noProject != "" {
		t.Fatalf("project=%q err=%v", noProject, err)
	}
}

func TestInteractiveJiraAndAssigneeChoices(t *testing.T) {
	t.Parallel()
	site, custom, err := resolveJiraURLs("", &scriptedPrompter{
		confirms: []bool{true}, inputs: []string{"https://Acme.atlassian.net"},
	})
	if err != nil || site != "acme" || custom != "" {
		t.Fatalf("site=%q custom=%q err=%v", site, custom, err)
	}
	site, custom, err = resolveJiraURLs("", &scriptedPrompter{
		confirms: []bool{false}, inputs: []string{"https://jira.example.com/base/"},
	})
	if err != nil || site != "" || custom != "https://jira.example.com/base" {
		t.Fatalf("site=%q custom=%q err=%v", site, custom, err)
	}
	mode, err := assigneeMode(
		Options{}, &scriptedPrompter{selectValue: string(runner.AssigneeNone)},
	)
	if err != nil || mode != runner.AssigneeNone {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
}
