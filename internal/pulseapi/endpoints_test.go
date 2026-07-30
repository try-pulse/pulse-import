package pulseapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

// TestEndpointContracts pins the request shape and response envelope of every
// endpoint the importer calls, since each one has a different wrapper in
// pulse-api and getting one wrong fails silently.
func TestEndpointContracts(t *testing.T) {
	t.Parallel()
	var (
		gotPaths   []string
		gotMethods []string
		gotBodies  = map[string]map[string]any{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.String())
		gotMethods = append(gotMethods, r.Method)
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotBodies[r.Method+" "+r.URL.Path] = body
		}
		encode := func(body any) {
			_ = json.NewEncoder(w).Encode(body)
		}
		switch {
		case r.URL.Path == "/teams/team-1/members":
			encode(map[string]any{"members": []pulseapi.TeamMember{
				{ID: "u1", Email: "a@b.c", FirstName: "Ada", LastName: "L", Role: "team_member"},
			}})
		case r.URL.Path == "/users/select":
			encode(map[string]any{
				"data":       []pulseapi.UserOption{{ID: "u1", Label: "Ada L"}},
				"pagination": map[string]any{"has_next": false},
			})
		case r.URL.Path == "/issues" && r.Method == http.MethodGet:
			encode(map[string]any{"data": []any{}, "pagination": map[string]any{"total": 7}})
		case r.URL.Path == "/comments" && r.Method == http.MethodGet:
			encode(map[string]any{"data": []any{}, "pagination": map[string]any{"total": 3}})
		case r.URL.Path == "/comments" && r.Method == http.MethodPost:
			encode(map[string]any{"message": "ok", "comment": map[string]string{"id": "comment-1"}})
		case r.URL.Path == "/projects" && r.Method == http.MethodPost:
			encode(map[string]any{"message": "ok", "project": map[string]string{
				"id": "project-1", "title": "Epic", "team_id": "team-1",
			}})
		case r.URL.Path == "/projects/project-1":
			encode(map[string]any{"project": map[string]string{
				"id": "project-1", "title": "Epic", "team_id": "team-1",
			}})
		case r.URL.Path == "/issues/issue-1" && r.Method == http.MethodPut:
			encode(pulseapi.Issue{ID: "issue-1", BlocksIDs: []string{"issue-2"}})
		case r.URL.Path == "/labels/label-1/unarchive":
			encode(pulseapi.Label{ID: "label-1", Name: "perf", Archived: false})
		case r.URL.Path == "/teams/team-1/labels":
			encode(map[string]any{"data": []pulseapi.Label{
				{ID: "label-1", Name: "perf", EntityType: "issue", Archived: true},
			}})
		case r.Method == http.MethodDelete:
			encode(map[string]string{"message": "deleted"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client := mustClient(t, srv.URL, "tok", "ws")
	ctx := context.Background()

	t.Run("team members carry ids and emails", func(t *testing.T) {
		members, err := client.ListTeamMembers(ctx, "team-1")
		if err != nil || len(members) != 1 {
			t.Fatalf("members=%+v err=%v", members, err)
		}
		if members[0].ID != "u1" || members[0].Email != "a@b.c" {
			t.Fatalf("member=%+v", members[0])
		}
	})

	t.Run("user options are workspace-wide", func(t *testing.T) {
		options, err := client.ListUserOptions(ctx)
		if err != nil || len(options) != 1 || options[0].Label != "Ada L" {
			t.Fatalf("options=%+v err=%v", options, err)
		}
	})

	t.Run("team issue count reads the pagination total", func(t *testing.T) {
		total, err := client.CountTeamIssues(ctx, "team-1")
		if err != nil || total != 7 {
			t.Fatalf("total=%d err=%v", total, err)
		}
	})

	t.Run("comment count reads the pagination total", func(t *testing.T) {
		total, err := client.CountComments(ctx, "issue-1")
		if err != nil || total != 3 {
			t.Fatalf("total=%d err=%v", total, err)
		}
	})

	t.Run("create comment unwraps the comment envelope", func(t *testing.T) {
		comment, err := client.CreateComment(ctx, pulseapi.CreateCommentRequest{
			TargetType: "issue", TargetID: "issue-1", Text: "hi",
		})
		if err != nil || comment.ID != "comment-1" {
			t.Fatalf("comment=%+v err=%v", comment, err)
		}
		body := gotBodies["POST /comments"]
		if body["target_type"] != "issue" || body["target_id"] != "issue-1" {
			t.Fatalf("body=%v", body)
		}
	})

	t.Run("create project unwraps the project envelope", func(t *testing.T) {
		project, err := client.CreateProject(ctx, pulseapi.CreateProjectRequest{
			Title: "Epic", TeamID: "team-1",
		})
		if err != nil || project.ID != "project-1" || project.Title != "Epic" {
			t.Fatalf("project=%+v err=%v", project, err)
		}
	})

	t.Run("get project accepts the wrapped payload", func(t *testing.T) {
		project, err := client.GetProject(ctx, "project-1")
		if err != nil || project.Title != "Epic" {
			t.Fatalf("project=%+v err=%v", project, err)
		}
	})

	t.Run("update issue sends only the relation fields", func(t *testing.T) {
		blocks := []string{"issue-2"}
		if _, err := client.UpdateIssue(ctx, "issue-1", pulseapi.UpdateIssueRequest{
			BlocksIDs: &blocks,
		}); err != nil {
			t.Fatal(err)
		}
		body := gotBodies["PUT /issues/issue-1"]
		if _, present := body["blocked_by_ids"]; present {
			t.Fatalf("an unset relation field must be omitted: %v", body)
		}
		if got, ok := body["blocks_ids"].([]any); !ok || len(got) != 1 {
			t.Fatalf("body=%v", body)
		}
	})

	t.Run("archived labels are queried explicitly", func(t *testing.T) {
		labels, err := client.ListArchivedLabels(ctx, "team-1")
		if err != nil || len(labels) != 1 {
			t.Fatalf("labels=%+v err=%v", labels, err)
		}
		var found bool
		for _, path := range gotPaths {
			if strings.Contains(path, "archived=true") && strings.Contains(path, "entity_type=issue") {
				found = true
			}
		}
		if !found {
			t.Fatalf("archived query missing from %v", gotPaths)
		}
	})

	t.Run("unarchive returns the restored label", func(t *testing.T) {
		label, err := client.UnarchiveLabel(ctx, "label-1")
		if err != nil || label.ID != "label-1" || label.Archived {
			t.Fatalf("label=%+v err=%v", label, err)
		}
	})

	t.Run("deletes are issued against the right paths", func(t *testing.T) {
		if err := client.DeleteIssue(ctx, "issue-1"); err != nil {
			t.Fatal(err)
		}
		if err := client.DeleteProject(ctx, "project-1"); err != nil {
			t.Fatal(err)
		}
		if err := client.DeleteDocument(ctx, "doc-1"); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"/issues/issue-1", "/projects/project-1", "/content/documents/doc-1"} {
			var found bool
			for index, path := range gotPaths {
				if path == want && gotMethods[index] == http.MethodDelete {
					found = true
				}
			}
			if !found {
				t.Errorf("no DELETE for %q in %v", want, gotPaths)
			}
		}
	})
}

func TestErrorClassifiers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status                        int
		notFound, conflict, forbidden bool
		ambiguous                     bool
	}{
		{status: http.StatusNotFound, notFound: true},
		{status: http.StatusConflict, conflict: true},
		{status: http.StatusForbidden, forbidden: true},
		{status: http.StatusBadRequest},
		{status: http.StatusInternalServerError, ambiguous: true},
		{status: http.StatusBadGateway, ambiguous: true},
	}
	for _, tt := range tests {
		err := &pulseapi.APIError{Status: tt.status}
		if pulseapi.IsNotFound(err) != tt.notFound {
			t.Errorf("IsNotFound(%d) = %v", tt.status, !tt.notFound)
		}
		if pulseapi.IsConflict(err) != tt.conflict {
			t.Errorf("IsConflict(%d) = %v", tt.status, !tt.conflict)
		}
		if pulseapi.IsForbidden(err) != tt.forbidden {
			t.Errorf("IsForbidden(%d) = %v", tt.status, !tt.forbidden)
		}
		if pulseapi.IsAmbiguousWriteError(err) != tt.ambiguous {
			t.Errorf("IsAmbiguousWriteError(%d) = %v", tt.status, !tt.ambiguous)
		}
	}
	// A transport error is always ambiguous: the request may have been accepted.
	if !pulseapi.IsAmbiguousWriteError(context.DeadlineExceeded) {
		t.Error("a transport error must be treated as ambiguous")
	}
	for _, classifier := range []func(error) bool{
		pulseapi.IsNotFound, pulseapi.IsConflict, pulseapi.IsForbidden,
	} {
		if classifier(context.DeadlineExceeded) {
			t.Error("a transport error is not an HTTP status")
		}
	}
	if pulseapi.IsAmbiguousWriteError(nil) {
		t.Error("nil is not an error")
	}
}
