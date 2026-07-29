package pulseapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

func mustClient(t testing.TB, baseURL, token, workspaceID string) *pulseapi.Client {
	t.Helper()
	client, err := pulseapi.New(baseURL, token, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClient_HeadersAndMe(t *testing.T) {
	t.Parallel()
	var gotAuth, gotWS, gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotWS = r.Header.Get("X-Workspace-ID")
		gotUserAgent = r.Header.Get("User-Agent")
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

	c := mustClient(t, srv.URL, "tok123", "ws-9")
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
	if !strings.HasPrefix(gotUserAgent, "pulse-import/") {
		t.Fatalf("user-agent=%q", gotUserAgent)
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

	_, err := mustClient(t, srv.URL, "t", "").Me(context.Background())
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

	c := mustClient(t, srv.URL, "bad", "ws")
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

	c := mustClient(t, srv.URL, "tok", "ws")
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

	_, err := mustClient(t, srv.URL, "tok", "ws").UploadMainDoc(context.Background(), "iss1", "T", []byte(`[]`))
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

	c := mustClient(t, srv.URL, "tok", "ws")
	teams, err := c.ListTeams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 101 {
		t.Fatalf("got %d teams", len(teams))
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
			if r.URL.Query().Get("entity_type") != "issue" || r.URL.Query().Get("archived") != "false" {
				t.Errorf("label query = %s", r.URL.RawQuery)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
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

	c := mustClient(t, srv.URL, "tok", "ws")
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

func TestClient_GetIssue(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/issues/issue-1" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		docID := "doc-1"
		_ = json.NewEncoder(w).Encode(pulseapi.Issue{
			ID: "issue-1", TeamID: "team-1", MainDocID: &docID,
		})
	}))
	t.Cleanup(srv.Close)
	issue, err := mustClient(t, srv.URL, "token", "workspace").GetIssue(context.Background(), "issue-1")
	if err != nil || issue.MainDocID == nil || *issue.MainDocID != "doc-1" {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
}

func TestClient_InvalidBaseURLAndBoundedError(t *testing.T) {
	t.Parallel()
	if _, err := pulseapi.New("ftp://example.com", "token", ""); err == nil {
		t.Fatal("expected invalid URL error")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 80<<10)))
	}))
	t.Cleanup(srv.Close)
	client := mustClient(t, srv.URL, "token", "")
	client.Backoff = func(int) time.Duration { return 0 }
	_, err := client.Me(context.Background())
	var apiErr *pulseapi.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err=%T %v", err, err)
	}
	if len(apiErr.Body) > (65<<10) || !strings.Contains(apiErr.Body, "truncated") {
		t.Fatalf("error body length=%d", len(apiErr.Body))
	}
}

func TestClient_ListLabelsRequiresEnvelope(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]pulseapi.Label{{ID: "x", Name: "n", EntityType: "issue"}})
	}))
	t.Cleanup(srv.Close)

	_, err := mustClient(t, srv.URL, "t", "w").ListLabels(context.Background(), "t1")
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

	c := mustClient(t, srv.URL, "tok", "ws")
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

	c := mustClient(t, srv.URL, "tok", "ws")
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

func TestClient_WriteDoesNotRetryServerError(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"upstream failed"}`))
	}))
	t.Cleanup(srv.Close)
	client := mustClient(t, srv.URL, "token", "workspace")
	client.Backoff = func(int) time.Duration { return 0 }
	_, err := client.CreateIssue(context.Background(), pulseapi.CreateIssueRequest{Title: "Valid", TeamID: "team"})
	if err == nil || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
	if !pulseapi.IsAmbiguousWriteError(err) {
		t.Fatal("500 write must be classified as ambiguous")
	}
}

func TestClient_ReadRetriesServerError(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(pulseapi.Issue{ID: "issue-1", TeamID: "team"})
	}))
	t.Cleanup(srv.Close)
	client := mustClient(t, srv.URL, "token", "workspace")
	client.Backoff = func(int) time.Duration { return 0 }
	issue, err := client.GetIssue(context.Background(), "issue-1")
	if err != nil || issue.ID != "issue-1" || hits.Load() != 2 {
		t.Fatalf("issue=%+v err=%v hits=%d", issue, err, hits.Load())
	}
}

func TestAmbiguousWriteClassification(t *testing.T) {
	t.Parallel()
	if pulseapi.IsAmbiguousWriteError(&pulseapi.APIError{Status: http.StatusBadRequest}) {
		t.Fatal("400 is definitive")
	}
	if !pulseapi.IsAmbiguousWriteError(errors.New("connection reset")) {
		t.Fatal("network error is ambiguous")
	}
	if pulseapi.IsAmbiguousWriteError(nil) {
		t.Fatal("nil error is not ambiguous")
	}
}

func TestCurrentPulseAPIContractFixtures(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("testdata", "contracts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ""
		switch r.URL.Path {
		case "/auth/me":
			key = "auth_me"
		case "/workspaces/me":
			key = "workspaces"
		case "/teams":
			key = "teams"
		case "/projects":
			key = "projects"
		case "/users":
			key = "users"
		case "/teams/team-1/labels":
			key = "labels"
		case "/issues", "/issues/issue-1":
			key = "issue"
		case "/content/documents/upload":
			key = "document"
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixtures[key])
	}))
	t.Cleanup(server.Close)
	client := mustClient(t, server.URL, "token", "workspace-1")
	ctx := context.Background()
	if user, err := client.Me(ctx); err != nil || user.ID != "user-1" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	if workspaces, err := client.ListMyWorkspaces(ctx); err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces=%+v err=%v", workspaces, err)
	}
	if teams, err := client.ListTeams(ctx); err != nil || len(teams) != 1 {
		t.Fatalf("teams=%+v err=%v", teams, err)
	}
	if projects, err := client.ListProjects(ctx); err != nil || len(projects) != 1 {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
	if users, err := client.ListUsers(ctx); err != nil || len(users) != 1 {
		t.Fatalf("users=%+v err=%v", users, err)
	}
	if labels, err := client.ListLabels(ctx, "team-1"); err != nil || len(labels) != 1 {
		t.Fatalf("labels=%+v err=%v", labels, err)
	}
	if issue, err := client.CreateIssue(ctx, pulseapi.CreateIssueRequest{Title: "Fix login", TeamID: "team-1"}); err != nil || issue.ID != "issue-1" {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	if issue, err := client.GetIssue(ctx, "issue-1"); err != nil || issue.ID != "issue-1" {
		t.Fatalf("issue=%+v err=%v", issue, err)
	}
	if document, err := client.UploadMainDoc(ctx, "issue-1", "Fix login", []byte(`[]`)); err != nil || document.ID != "document-1" {
		t.Fatalf("document=%+v err=%v", document, err)
	}
}
