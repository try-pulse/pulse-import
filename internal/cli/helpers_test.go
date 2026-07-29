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

	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

func TestFindTeam(t *testing.T) {
	t.Parallel()
	teams := []pulseapi.Team{
		{ID: "id-1", Name: "Engineering"},
		{ID: "id-2", Name: "Design"},
	}
	tests := []struct {
		q    string
		want string // id or empty
	}{
		{"id-1", "id-1"},
		{"Engineering", "id-1"},
		{"engineering", "id-1"},
		{"missing", ""},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			t.Parallel()
			got := findTeam(teams, tt.q)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil || got.ID != tt.want {
				t.Fatalf("got %+v want %s", got, tt.want)
			}
		})
	}
}

func TestJiraCloudRE(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want string // site slug or empty
	}{
		{"https://acme.atlassian.net", "acme"},
		{"https://Acme.atlassian.net/jira", "Acme"},
		{"http://foo.atlassian.net", "foo"},
		{"https://jira.example.com", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			m := jiraCloudRE.FindStringSubmatch(tt.url)
			got := ""
			if len(m) == 2 {
				got = m[1]
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
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

	p := newPrompter(true)
	opts := Options{
		File:     path,
		JiraURL:  "https://acme.atlassian.net",
		NoPrompt: true,
	}

	for _, id := range []string{"jira-csv", "jira", "jiraCsv"} {
		reg, err := lookupImporter(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		imp, err := reg.New(opts, p)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !strings.Contains(imp.Name(), "Jira") {
			t.Fatalf("name=%q", imp.Name())
		}
	}

	if _, err := lookupImporter("nope"); err == nil {
		t.Fatal("expected unknown importer error")
	}

	opts.JiraURL = "https://jira.corp.example"
	reg, err := lookupImporter("jira-csv")
	if err != nil {
		t.Fatal(err)
	}
	imp, err := reg.New(opts, p)
	if err != nil {
		t.Fatal(err)
	}
	res, err := imp.Import()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || res.Issues[0].URL != "https://jira.corp.example/browse/K-1" {
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

			id, err := pickWorkspace(context.Background(), pulseapi.New(srv.URL, "t", ""), newPrompter(true))
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
	client := pulseapi.New(srv.URL, "tok", "ws")
	p := newPrompter(true)

	tid, err := resolveTeam(context.Background(), client, Options{Team: "Eng", NoPrompt: true}, "Jira", p)
	if err != nil || tid != "tid" {
		t.Fatalf("team=%q err=%v", tid, err)
	}

	pid, err := resolveProject(context.Background(), client, Options{Project: "App", NoPrompt: true}, "tid", p)
	if err != nil || pid != "pid" {
		t.Fatalf("project=%q err=%v", pid, err)
	}

	if _, err := resolveTeam(context.Background(), client, Options{Team: "missing", NoPrompt: true}, "Jira", p); err == nil {
		t.Fatal("expected missing team error")
	}

	if _, err := resolveTeam(context.Background(), client, Options{NoPrompt: true}, "Jira", p); err == nil {
		t.Fatal("expected --team required")
	}
}

func TestNonInteractivePrompter(t *testing.T) {
	t.Parallel()
	p := newPrompter(true)
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
	mode, err := assigneeMode(Options{SelfAssign: true}, newPrompter(true))
	if err != nil || mode != "self" {
		t.Fatalf("got %q err=%v", mode, err)
	}
	mode, err = assigneeMode(Options{NoPrompt: true}, newPrompter(true))
	if err != nil || mode != "mapped" {
		t.Fatalf("got %q err=%v", mode, err)
	}
}
