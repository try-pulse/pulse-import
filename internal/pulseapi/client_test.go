package pulseapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

func TestClient_HeadersAndMe(t *testing.T) {
	t.Parallel()
	var gotAuth, gotWS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotWS = r.Header.Get("X-Workspace-ID")
		switch r.URL.Path {
		case "/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user": map[string]string{"id": "u1", "email": "a@b.c", "display_name": "Ada"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok123", "ws-9")
	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.ID != "u1" || me.DisplayName != "Ada" {
		t.Fatalf("me=%+v", me)
	}
	if gotAuth != "Bearer tok123" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotWS != "ws-9" {
		t.Fatalf("workspace=%q", gotWS)
	}
	if c.HTTP.Timeout == 0 {
		t.Fatal("expected default timeout")
	}
}

func TestClient_MeRequiresWrappedUser(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(pulseapi.User{ID: "u2", Email: "x@y.z"})
	}))
	t.Cleanup(srv.Close)

	_, err := pulseapi.New(srv.URL, "t", "").Me(context.Background())
	if err == nil {
		t.Fatal("expected empty-user error for bare payload")
	}
}

func TestClient_APIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"UNAUTHORIZED","message":"bad token"}`))
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "bad", "ws")
	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	apiErr, ok := err.(*pulseapi.APIError)
	if !ok {
		t.Fatalf("type %T", err)
	}
	if apiErr.Status != 401 || apiErr.Message != "bad token" {
		t.Fatalf("%+v", apiErr)
	}
}

func TestClient_CreateIssueAndUpload(t *testing.T) {
	t.Parallel()
	var issueBody map[string]any
	var uploadCT string
	var uploadHasFile bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/issues" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&issueBody)
			_ = json.NewEncoder(w).Encode(pulseapi.Issue{ID: "iss1", Title: "T", Code: "A-1", TeamID: "team1"})
		case r.URL.Path == "/content/documents/upload":
			uploadCT = r.Header.Get("Content-Type")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
				w.WriteHeader(400)
				return
			}
			_, uploadHasFile = r.MultipartForm.File["file"]
			atts := r.FormValue("attachments")
			if !strings.Contains(atts, "is_main_doc") {
				t.Errorf("attachments=%s", atts)
			}
			if !strings.Contains(atts, "iss1") {
				t.Errorf("attachments missing issue id: %s", atts)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "doc1", "title": "T"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok", "ws")
	issue, err := c.CreateIssue(context.Background(), pulseapi.CreateIssueRequest{
		Title:    "T",
		TeamID:   "team1",
		Status:   "backlog",
		Priority: "high",
		Type:     "bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if issue.ID != "iss1" {
		t.Fatalf("%+v", issue)
	}
	if issueBody["team_id"] != "team1" {
		t.Fatalf("body=%v", issueBody)
	}

	doc, err := c.UploadMainDoc(context.Background(), "iss1", "T", []byte(`[{"type":"p","children":[{"text":"hi"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID != "doc1" {
		t.Fatalf("%+v", doc)
	}
	if !strings.HasPrefix(uploadCT, "multipart/form-data") {
		t.Fatalf("content-type=%q", uploadCT)
	}
	if !uploadHasFile {
		t.Fatal("expected file part")
	}
}

func TestClient_UploadRequiresID(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"title": "no-id"})
	}))
	t.Cleanup(srv.Close)

	_, err := pulseapi.New(srv.URL, "tok", "ws").UploadMainDoc(context.Background(), "iss1", "T", []byte(`[]`))
	if err == nil || !strings.Contains(err.Error(), "missing document id") {
		t.Fatalf("err=%v", err)
	}
}

func TestClient_ListPagination(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch r.URL.Path {
		case "/teams":
			if page == "1" {
				items := make([]pulseapi.Team, 100)
				for i := range items {
					items[i] = pulseapi.Team{ID: "t", Name: "N"}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []pulseapi.Team{{ID: "last", Name: "Last"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok", "ws")
	teams, err := c.ListTeams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 101 {
		t.Fatalf("got %d teams", len(teams))
	}
}

func TestClient_CreateTeamWrapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"icon_code"`) {
			t.Errorf("missing icon defaults: %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"team": pulseapi.Team{ID: "t1", Name: "Jira"},
		})
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok", "ws")
	team, err := c.CreateTeam(context.Background(), "Jira")
	if err != nil {
		t.Fatal(err)
	}
	if team.ID != "t1" {
		t.Fatalf("%+v", team)
	}
}

func TestClient_ListEndpointsAndLabel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/workspaces/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"role":      "owner",
					"workspace": map[string]string{"id": "ws1", "slug": "acme", "name": "Acme"},
				}},
			})
		case r.URL.Path == "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []pulseapi.User{{ID: "u1", Email: "a@b.c", DisplayName: "Ada"}},
			})
		case r.URL.Path == "/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []pulseapi.Project{{ID: "p1", Name: "App", TeamID: "t1"}},
			})
		case r.URL.Path == "/teams/t1/labels" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []pulseapi.Label{{ID: "l1", Name: "bug", EntityType: "issue"}},
			})
		case r.URL.Path == "/teams/t1/labels" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(pulseapi.Label{ID: "l2", Name: "Type: Bug", EntityType: "issue"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok", "ws")
	ctx := context.Background()

	ws, err := c.ListMyWorkspaces(ctx)
	if err != nil || len(ws) != 1 || ws[0].Workspace.ID != "ws1" {
		t.Fatalf("workspaces=%v err=%v", ws, err)
	}
	users, err := c.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("users=%v err=%v", users, err)
	}
	projects, err := c.ListProjects(ctx)
	if err != nil || projects[0].ID != "p1" {
		t.Fatalf("projects=%v err=%v", projects, err)
	}
	labels, err := c.ListLabels(ctx, "t1")
	if err != nil || labels[0].ID != "l1" {
		t.Fatalf("labels=%v err=%v", labels, err)
	}
	lab, err := c.CreateLabel(ctx, "t1", pulseapi.CreateLabelRequest{
		Name: "Type: Bug", Color: "#EB5757", EntityType: "issue",
	})
	if err != nil || lab.ID != "l2" {
		t.Fatalf("create label=%v err=%v", lab, err)
	}
}

func TestClient_ListLabelsRequiresEnvelope(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]pulseapi.Label{{ID: "x", Name: "n", EntityType: "issue"}})
	}))
	t.Cleanup(srv.Close)

	_, err := pulseapi.New(srv.URL, "t", "w").ListLabels(context.Background(), "t1")
	if err == nil {
		t.Fatal("expected decode failure for bare array")
	}
}

func TestAPIError_Error(t *testing.T) {
	t.Parallel()
	err := &pulseapi.APIError{Status: 500, Message: "nope"}
	if err.Error() != "pulse api 500: nope" {
		t.Fatal(err.Error())
	}
	err2 := &pulseapi.APIError{Status: 418, Body: "teapot"}
	if err2.Error() != "pulse api 418: teapot" {
		t.Fatal(err2.Error())
	}
}

func TestClient_UploadContentWrapAndSanitize(t *testing.T) {
	t.Parallel()
	var uploadName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/issues":
			_ = json.NewEncoder(w).Encode(pulseapi.Issue{ID: "i9", Title: "T", TeamID: "t"})
		case "/content/documents/upload":
			_ = r.ParseMultipartForm(1 << 20)
			if f := r.MultipartForm.File["file"]; len(f) > 0 {
				uploadName = f[0].Filename
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": pulseapi.Document{ID: "d9", Title: "T"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok", "ws")
	issue, err := c.CreateIssue(context.Background(), pulseapi.CreateIssueRequest{Title: "T", TeamID: "t"})
	if err != nil || issue.ID != "i9" {
		t.Fatalf("%v %v", issue, err)
	}
	doc, err := c.UploadMainDoc(context.Background(), "i9", "Weird Title!!! 你好", []byte(`[]`))
	if err != nil || doc.ID != "d9" {
		t.Fatalf("%v %v", doc, err)
	}
	if uploadName == "" || !strings.HasSuffix(uploadName, ".json") {
		t.Fatalf("filename=%q", uploadName)
	}
}

func TestClient_RateLimitRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issues" {
			http.NotFound(w, r)
			return
		}
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"slow down"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(pulseapi.Issue{ID: "iss1", Title: "T", TeamID: "team1"})
	}))
	t.Cleanup(srv.Close)

	c := pulseapi.New(srv.URL, "tok", "ws")
	c.Backoff = func(int) time.Duration { return 0 }

	issue, err := c.CreateIssue(context.Background(), pulseapi.CreateIssueRequest{Title: "T", TeamID: "team1"})
	if err != nil {
		t.Fatal(err)
	}
	if issue.ID != "iss1" {
		t.Fatalf("%+v", issue)
	}
	if hits.Load() < 2 {
		t.Fatalf("expected retry, hits=%d", hits.Load())
	}
}
