package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer is a small stand-in for the Pulse HTTP API, wired to the real
// endpoints, envelopes and field names the client depends on. It exists so the
// CLI tests exercise the actual request path — headers, query strings, multipart
// upload — rather than a hand-rolled interface.
type fakeServer struct {
	*httptest.Server

	mu       sync.Mutex
	Members  []map[string]any
	Labels   []map[string]any
	Issues   map[string]map[string]any
	Projects map[string]map[string]any
	MainDocs map[string]string
	Comments map[string][]string
	Deleted  []string
	Calls    map[string]int
	// UploadContentTypes records the part Content-Type of every Main Doc upload:
	// Pulse decides editor-openability from it.
	UploadContentTypes []string
	UploadFilenames    []string
	IssueTitles        []string

	// FailIssueCreate makes POST /issues fail once with this status.
	FailIssueCreate int
	nextID          int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		Issues:   map[string]map[string]any{},
		Projects: map[string]map[string]any{},
		MainDocs: map[string]string{},
		Comments: map[string][]string{},
		Calls:    map[string]int{},
	}
	f.Members = []map[string]any{
		{"id": "user-me", "first_name": "Me", "last_name": "Myself", "email": "me@example.com"},
		{"id": "user-jane", "first_name": "Jane", "last_name": "Doe", "email": "jane@acme.com"},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls[name]
}

func (f *fakeServer) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s-%d", prefix, f.nextID)
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls[r.Method+" "+r.URL.Path]++

	write := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}

	switch {
	case r.URL.Path == "/auth/me":
		write(http.StatusOK, map[string]any{
			"user": map[string]string{"id": "user-me", "email": "me@example.com", "display_name": "Me"},
		})

	case r.URL.Path == "/workspaces/me":
		write(http.StatusOK, map[string]any{"data": []map[string]any{{
			"role": "owner", "status": "active",
			"workspace": map[string]string{"id": "workspace-1", "slug": "acme", "name": "Acme"},
		}}})

	case r.URL.Path == "/teams":
		write(http.StatusOK, map[string]any{"data": []map[string]any{{
			"id": "team-1", "name": "Engineering",
			"estimate_settings": map[string]any{"enabled": false, "scale_type": "hours"},
		}}})

	case r.URL.Path == "/teams/team-1/members":
		write(http.StatusOK, map[string]any{"members": f.Members})

	case r.URL.Path == "/projects" && r.Method == http.MethodGet:
		projects := make([]map[string]any, 0, len(f.Projects))
		for _, project := range f.Projects {
			projects = append(projects, project)
		}
		write(http.StatusOK, map[string]any{"data": projects})

	case r.URL.Path == "/projects" && r.Method == http.MethodPost:
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		project := map[string]any{
			"id": f.id("project"), "title": req["title"], "team_id": req["team_id"],
		}
		f.Projects[project["id"].(string)] = project
		write(http.StatusCreated, map[string]any{"message": "ok", "project": project})

	case r.URL.Path == "/issues" && r.Method == http.MethodGet:
		write(http.StatusOK, map[string]any{
			"data":       []any{},
			"pagination": map[string]any{"total": len(f.Issues)},
		})

	case r.URL.Path == "/issues" && r.Method == http.MethodPost:
		if f.FailIssueCreate != 0 {
			status := f.FailIssueCreate
			f.FailIssueCreate = 0
			write(status, map[string]string{"code": "INVALID_ISSUE", "message": "refused"})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		issue := map[string]any{
			"id": f.id("issue"), "title": req["title"], "team_id": req["team_id"],
			"status": req["status"], "priority": req["priority"], "type": req["type"],
		}
		for _, field := range []string{"project_id", "parent_id", "assignee_id"} {
			if value, ok := req[field]; ok {
				issue[field] = value
			}
		}
		f.Issues[issue["id"].(string)] = issue
		f.IssueTitles = append(f.IssueTitles, issue["title"].(string))
		write(http.StatusCreated, issue)

	case strings.HasPrefix(r.URL.Path, "/issues/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(r.URL.Path, "/issues/")
		issue, ok := f.Issues[id]
		if !ok {
			write(http.StatusNotFound, map[string]string{"code": "ISSUE_NOT_FOUND"})
			return
		}
		copied := map[string]any{}
		for key, value := range issue {
			copied[key] = value
		}
		if doc := f.MainDocs[id]; doc != "" {
			copied["main_doc_id"] = doc
		}
		write(http.StatusOK, copied)

	case strings.HasPrefix(r.URL.Path, "/issues/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(r.URL.Path, "/issues/")
		issue, ok := f.Issues[id]
		if !ok {
			write(http.StatusNotFound, map[string]string{"code": "ISSUE_NOT_FOUND"})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		for key, value := range req {
			issue[key] = value
		}
		write(http.StatusOK, issue)

	case strings.HasPrefix(r.URL.Path, "/issues/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/issues/")
		if _, ok := f.Issues[id]; !ok {
			write(http.StatusNotFound, map[string]string{"code": "ISSUE_NOT_FOUND"})
			return
		}
		delete(f.Issues, id)
		f.Deleted = append(f.Deleted, "issue:"+id)
		write(http.StatusOK, map[string]string{"message": "deleted"})

	case strings.HasPrefix(r.URL.Path, "/projects/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/projects/")
		delete(f.Projects, id)
		f.Deleted = append(f.Deleted, "project:"+id)
		write(http.StatusOK, map[string]string{"message": "deleted"})

	case r.URL.Path == "/teams/team-1/labels" && r.Method == http.MethodGet:
		archived := r.URL.Query().Get("archived") == "true"
		out := make([]map[string]any, 0)
		for _, label := range f.Labels {
			if label["archived"] == archived {
				out = append(out, label)
			}
		}
		write(http.StatusOK, map[string]any{"data": out})

	case r.URL.Path == "/teams/team-1/labels" && r.Method == http.MethodPost:
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		name, _ := req["name"].(string)
		for _, label := range f.Labels {
			if strings.EqualFold(label["name"].(string), name) {
				write(http.StatusConflict, map[string]string{"code": "DUPLICATE_NAME"})
				return
			}
		}
		label := map[string]any{
			"id": f.id("label"), "name": name, "entity_type": "issue",
			"team_id": "team-1", "archived": false,
		}
		f.Labels = append(f.Labels, label)
		write(http.StatusCreated, label)

	case strings.HasSuffix(r.URL.Path, "/unarchive") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/labels/"), "/unarchive")
		for _, label := range f.Labels {
			if label["id"] == id {
				label["archived"] = false
				write(http.StatusOK, label)
				return
			}
		}
		write(http.StatusNotFound, map[string]string{"code": "NOT_FOUND"})

	case r.URL.Path == "/content/documents/upload":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			write(http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST"})
			return
		}
		if files := r.MultipartForm.File["file"]; len(files) > 0 {
			f.UploadContentTypes = append(f.UploadContentTypes, files[0].Header.Get("Content-Type"))
			f.UploadFilenames = append(f.UploadFilenames, files[0].Filename)
		}
		var attachments []struct {
			EntityType string `json:"entity_type"`
			EntityID   string `json:"entity_id"`
			IsMainDoc  bool   `json:"is_main_doc"`
		}
		_ = json.Unmarshal([]byte(r.FormValue("attachments")), &attachments)
		docID := f.id("doc")
		for _, attachment := range attachments {
			if attachment.IsMainDoc {
				f.MainDocs[attachment.EntityID] = docID
			}
		}
		write(http.StatusCreated, map[string]any{
			"message": "ok",
			"content": map[string]string{"id": docID, "title": r.FormValue("title")},
		})

	case strings.HasPrefix(r.URL.Path, "/content/documents/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/content/documents/")
		f.Deleted = append(f.Deleted, "document:"+id)
		write(http.StatusOK, map[string]string{"message": "deleted"})

	case r.URL.Path == "/comments" && r.Method == http.MethodPost:
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		target, _ := req["target_id"].(string)
		text, _ := req["text"].(string)
		f.Comments[target] = append(f.Comments[target], text)
		write(http.StatusCreated, map[string]any{
			"message": "ok", "comment": map[string]string{"id": f.id("comment")},
		})

	case r.URL.Path == "/comments" && r.Method == http.MethodGet:
		target := r.URL.Query().Get("target_id")
		write(http.StatusOK, map[string]any{
			"data":       []any{},
			"pagination": map[string]any{"total": len(f.Comments[target])},
		})

	default:
		write(http.StatusNotFound, map[string]string{"code": "NOT_FOUND", "message": r.URL.Path})
	}
}
