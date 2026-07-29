package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/schollz/progressbar/v3"
	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/platemd"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

type Options struct {
	TeamID          string
	ProjectID       string
	Assignee        AssigneeMode
	SelfUserID      string
	DryRun          bool
	ContinueOnError bool
}

type Result struct {
	Created       int
	MainDocs      int
	Failed        int
	Errors        []string
	TeamID        string
	SampleIssueID string
}

type Runner struct {
	API API
}

func New(client API) *Runner {
	return &Runner{API: client}
}

func (r *Runner) Run(ctx context.Context, data *importers.ImportResult, opts Options) (*Result, error) {
	if data == nil {
		return nil, fmt.Errorf("no import data")
	}
	if opts.TeamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if opts.Assignee == "" {
		opts.Assignee = AssigneeNone
	}

	api := r.API
	if opts.DryRun {
		api = dryClient{inner: r.API}
	}

	res := &Result{TeamID: opts.TeamID}

	users, err := api.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	labelMapping, err := ensureLabels(ctx, api, opts.TeamID, data.Labels)
	if err != nil {
		return nil, err
	}

	m := mapping{
		teamID:       opts.TeamID,
		projectID:    opts.ProjectID,
		assignee:     opts.Assignee,
		selfUserID:   opts.SelfUserID,
		users:        indexUsers(users),
		labelMapping: labelMapping,
	}

	bar := progressbar.NewOptions(len(data.Issues),
		progressbar.OptionSetDescription("Importing issues"),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(20),
		progressbar.OptionClearOnFinish(),
	)

	for idx, issue := range data.Issues {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		req := m.request(issue)

		created, err := api.CreateIssue(ctx, req)
		if err != nil {
			res.Failed++
			msg := fmt.Sprintf("issue %d %q: %v", idx+1, req.Title, err)
			res.Errors = append(res.Errors, msg)
			if !opts.ContinueOnError {
				_ = bar.Finish()
				return res, fmt.Errorf("%s", msg)
			}
			_ = bar.Add(1)
			continue
		}
		res.Created++
		if res.SampleIssueID == "" {
			res.SampleIssueID = created.ID
		}

		if strings.TrimSpace(issue.BodyMarkdown) != "" {
			plate, err := platemd.ToJSON(issue.BodyMarkdown, nil)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("main doc convert %s: %v", created.ID, err))
			} else if _, err := api.UploadMainDoc(ctx, created.ID, req.Title, plate); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("main doc upload %s: %v", created.ID, err))
			} else {
				res.MainDocs++
			}
		}
		_ = bar.Add(1)
	}
	_ = bar.Finish()
	return res, nil
}

func ensureLabels(ctx context.Context, api API, teamID string, labels map[string]importers.Label) (map[string]string, error) {
	existing, err := api.ListLabels(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	byName := map[string]string{}
	for _, l := range existing {
		if l.Archived || (l.EntityType != "" && l.EntityType != "issue") {
			continue
		}
		byName[strings.ToLower(l.Name)] = l.ID
	}

	out := map[string]string{}
	for key, lab := range labels {
		name := truncateRunes(lab.Name, 50)
		if name == "" {
			continue
		}
		if id, ok := byName[strings.ToLower(name)]; ok {
			out[key] = id
			continue
		}
		created, err := api.CreateLabel(ctx, teamID, pulseapi.CreateLabelRequest{
			Name: name, Color: pickColor(name), EntityType: "issue",
		})
		if err != nil {
			return nil, fmt.Errorf("create label %q: %w", name, err)
		}
		out[key] = created.ID
		byName[strings.ToLower(name)] = created.ID
	}
	return out, nil
}
