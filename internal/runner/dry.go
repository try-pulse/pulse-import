package runner

import (
	"context"

	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

type API interface {
	ListUsers(ctx context.Context) ([]pulseapi.User, error)
	ListLabels(ctx context.Context, teamID string) ([]pulseapi.Label, error)
	CreateLabel(ctx context.Context, teamID string, req pulseapi.CreateLabelRequest) (*pulseapi.Label, error)
	CreateIssue(ctx context.Context, req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error)
	UploadMainDoc(ctx context.Context, issueID, title string, plateJSON []byte) (*pulseapi.Document, error)
}

type dryClient struct {
	inner API
}

func (d dryClient) ListUsers(ctx context.Context) ([]pulseapi.User, error) {
	return d.inner.ListUsers(ctx)
}

func (d dryClient) ListLabels(ctx context.Context, teamID string) ([]pulseapi.Label, error) {
	return d.inner.ListLabels(ctx, teamID)
}

func (d dryClient) CreateLabel(_ context.Context, _ string, req pulseapi.CreateLabelRequest) (*pulseapi.Label, error) {
	return &pulseapi.Label{ID: "dry-" + req.Name, Name: req.Name, EntityType: "issue"}, nil
}

func (d dryClient) CreateIssue(_ context.Context, req pulseapi.CreateIssueRequest) (*pulseapi.Issue, error) {
	return &pulseapi.Issue{ID: "dry-issue", Title: req.Title, TeamID: req.TeamID}, nil
}

func (d dryClient) UploadMainDoc(_ context.Context, issueID, title string, _ []byte) (*pulseapi.Document, error) {
	return &pulseapi.Document{ID: "dry-doc", Title: title}, nil
}
