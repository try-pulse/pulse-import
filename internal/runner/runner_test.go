package runner_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/runner"
)

func TestRun_Validation(t *testing.T) {
	t.Parallel()
	r := runner.New(pulseapi.New("http://example.invalid", "t", "w"))
	if _, err := r.Run(context.Background(), nil, runner.Options{TeamID: "t"}); err == nil {
		t.Fatal("expected nil data error")
	}
	if _, err := r.Run(context.Background(), &importers.ImportResult{}, runner.Options{}); err == nil {
		t.Fatal("expected missing team error")
	}
}

func TestDryRun(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[{"id":"u1","email":"a@b.c","display_name":"Alice"}]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client := pulseapi.New(srv.URL, "tok", "ws")
	r := runner.New(client)
	res, err := r.Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{{
			Title:        "Hello",
			BodyMarkdown: "# Doc",
			Priority:     importers.PriorityMedium,
			Type:         importers.TypeTask,
			Labels:       []string{"Type: Task"},
		}},
		Labels: map[string]importers.Label{"Type: Task": {Name: "Type: Task"}},
		Users:  map[string]importers.User{},
	}, runner.Options{
		TeamID: "team1",
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.MainDocs != 1 {
		t.Fatalf("got %+v", res)
	}
}

func TestCreateIssueAndMainDoc(t *testing.T) {
	t.Parallel()
	var createdIssue, uploaded atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"lab1","name":"Type: Bug","entity_type":"issue"}`))
		case r.URL.Path == "/issues" && r.Method == http.MethodPost:
			createdIssue.Store(true)
			_, _ = w.Write([]byte(`{"id":"iss1","title":"Bug","code":"T-1","team_id":"team1"}`))
		case r.URL.Path == "/content/documents/upload":
			uploaded.Store(true)
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "attachments") {
				t.Errorf("missing attachments in multipart")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "doc1", "title": "Bug"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client := pulseapi.New(srv.URL, "tok", "ws")
	r := runner.New(client)
	res, err := r.Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{{
			Title:        "Bug",
			BodyMarkdown: "h1. Details\n\nSomething broke",
			Priority:     importers.PriorityHigh,
			Type:         importers.TypeBug,
			Labels:       []string{"Type: Bug"},
			Status:       "In Progress",
		}},
		Labels: map[string]importers.Label{"Type: Bug": {Name: "Type: Bug"}},
	}, runner.Options{TeamID: "team1"})
	if err != nil {
		t.Fatal(err)
	}
	if !createdIssue.Load() || !uploaded.Load() {
		t.Fatalf("created=%v uploaded=%v res=%+v", createdIssue.Load(), uploaded.Load(), res)
	}
	if res.Created != 1 || res.MainDocs != 1 {
		t.Fatalf("got %+v", res)
	}
}

func TestAssigneeModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mode         runner.AssigneeMode
		selfID       string
		issueAssign  string
		wantAssignee string // empty means null/absent
	}{
		{name: "self", mode: runner.AssigneeSelf, selfID: "me", wantAssignee: "me"},
		{name: "mapped by email", mode: runner.AssigneeMapped, issueAssign: "alice@ex.com", wantAssignee: "u-alice"},
		{name: "mapped by name", mode: runner.AssigneeMapped, issueAssign: "Alice Wonder", wantAssignee: "u-alice"},
		{name: "mapped miss", mode: runner.AssigneeMapped, issueAssign: "Nobody", wantAssignee: ""},
		{name: "none", mode: runner.AssigneeNone, issueAssign: "Alice Wonder", wantAssignee: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotAssignee any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/users":
					_, _ = w.Write([]byte(`{"data":[{"id":"u-alice","email":"alice@ex.com","display_name":"Alice Wonder","first_name":"Alice","last_name":"Wonder"}]}`))
				case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"data":[]}`))
				case r.URL.Path == "/issues" && r.Method == http.MethodPost:
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					gotAssignee = body["assignee_id"]
					_, _ = w.Write([]byte(`{"id":"iss1","title":"T","team_id":"team1"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)

			_, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
				Issues: []importers.Issue{{
					Title:      "T",
					AssigneeID: tt.issueAssign,
					Priority:   importers.PriorityLow,
					Type:       importers.TypeTask,
				}},
			}, runner.Options{
				TeamID:     "team1",
				Assignee:   tt.mode,
				SelfUserID: tt.selfID,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := gotAssignee.(string)
			if got != tt.wantAssignee {
				t.Fatalf("assignee=%v (%T) want %q", gotAssignee, gotAssignee, tt.wantAssignee)
			}
		})
	}
}

func TestContinueOnError(t *testing.T) {
	t.Parallel()
	var creates atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/issues":
			n := creates.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"nope"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"iss2","title":"ok","team_id":"team1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{
			{Title: "bad", Priority: importers.PriorityLow, Type: importers.TypeTask},
			{Title: "good", Priority: importers.PriorityLow, Type: importers.TypeTask},
		},
	}, runner.Options{TeamID: "team1", ContinueOnError: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Created != 1 {
		t.Fatalf("got %+v", res)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors=%v", res.Errors)
	}
}

func TestAbortOnError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/issues":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	res, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{
			{Title: "a", Priority: importers.PriorityLow, Type: importers.TypeTask},
			{Title: "b", Priority: importers.PriorityLow, Type: importers.TypeTask},
		},
	}, runner.Options{TeamID: "team1"})
	if err == nil {
		t.Fatal("expected error")
	}
	if res == nil || res.Failed != 1 {
		t.Fatalf("res=%+v", res)
	}
}

func TestReuseExistingLabel(t *testing.T) {
	t.Parallel()
	var createdLabel atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"id":"existing","name":"Type: Bug","entity_type":"issue"}]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			createdLabel.Store(true)
			w.WriteHeader(500)
		case r.URL.Path == "/issues":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			ids, _ := body["label_ids"].([]any)
			if len(ids) != 1 || ids[0] != "existing" {
				t.Errorf("label_ids=%v", body["label_ids"])
			}
			_, _ = w.Write([]byte(`{"id":"iss1","title":"T","team_id":"team1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{{
			Title:    "T",
			Priority: importers.PriorityHigh,
			Type:     importers.TypeBug,
			Labels:   []string{"Type: Bug"},
		}},
		Labels: map[string]importers.Label{"Type: Bug": {Name: "Type: Bug"}},
	}, runner.Options{TeamID: "team1"})
	if err != nil {
		t.Fatal(err)
	}
	if createdLabel.Load() {
		t.Fatal("should reuse existing label")
	}
}

func TestSkipArchivedLabels(t *testing.T) {
	t.Parallel()
	var createdLabel atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"id":"old","name":"Type: Bug","entity_type":"issue","archived":true}]}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			createdLabel.Store(true)
			_, _ = w.Write([]byte(`{"id":"new","name":"Type: Bug","entity_type":"issue"}`))
		case r.URL.Path == "/issues":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			ids, _ := body["label_ids"].([]any)
			if len(ids) != 1 || ids[0] != "new" {
				t.Errorf("label_ids=%v", body["label_ids"])
			}
			_, _ = w.Write([]byte(`{"id":"iss1","title":"T","team_id":"team1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{{
			Title:    "T",
			Priority: importers.PriorityHigh,
			Type:     importers.TypeBug,
			Labels:   []string{"Type: Bug"},
		}},
		Labels: map[string]importers.Label{"Type: Bug": {Name: "Type: Bug"}},
	}, runner.Options{TeamID: "team1"})
	if err != nil {
		t.Fatal(err)
	}
	if !createdLabel.Load() {
		t.Fatal("expected create after skipping archived label")
	}
}

func TestLongTitleTruncated(t *testing.T) {
	t.Parallel()
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/issues":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotTitle, _ = body["title"].(string)
			_, _ = w.Write([]byte(`{"id":"iss1","title":"x","team_id":"team1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	long := strings.Repeat("标题", 120) // > 200 runes
	_, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{{Title: long, Priority: importers.PriorityLow, Type: importers.TypeTask}},
	}, runner.Options{TeamID: "team1"})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(gotTitle)) != 200 {
		t.Fatalf("title runes=%d", len([]rune(gotTitle)))
	}
}

func TestProjectIDAttached(t *testing.T) {
	t.Parallel()
	var gotProject any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/issues":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotProject = body["project_id"]
			_, _ = w.Write([]byte(`{"id":"iss1","title":"T","team_id":"team1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	_, err := runner.New(pulseapi.New(srv.URL, "tok", "ws")).Run(context.Background(), &importers.ImportResult{
		Issues: []importers.Issue{{Title: "T", Priority: importers.PriorityLow, Type: importers.TypeTask}},
	}, runner.Options{TeamID: "team1", ProjectID: "proj-9"})
	if err != nil {
		t.Fatal(err)
	}
	if gotProject != "proj-9" {
		t.Fatalf("project_id=%v", gotProject)
	}
}
