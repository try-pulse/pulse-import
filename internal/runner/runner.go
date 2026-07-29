package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/platemd"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

type API interface {
	ListUsers(context.Context) ([]pulseapi.User, error)
	ListLabels(context.Context, string) ([]pulseapi.Label, error)
	CreateLabel(context.Context, string, pulseapi.CreateLabelRequest) (*pulseapi.Label, error)
	CreateIssue(context.Context, pulseapi.CreateIssueRequest) (*pulseapi.Issue, error)
	GetIssue(context.Context, string) (*pulseapi.Issue, error)
	UploadMainDoc(context.Context, string, string, []byte) (*pulseapi.Document, error)
}

type Options struct {
	ImporterID  string
	APIURL      string
	WorkspaceID string
	TeamID      string
	ProjectID   string
	Assignee    AssigneeMode
	SelfUserID  string
}

type Diagnostic struct {
	Key     string
	Row     int
	Message string
}

type LabelPlan struct {
	Key        string
	Name       string
	ExistingID string
	Create     bool
}

type PreparedItem struct {
	Key       string
	Row       int
	RowHash   string
	Request   pulseapi.CreateIssueRequest
	LabelKeys []string
	PlateJSON []byte
}

type Plan struct {
	Options            Options
	SourcePath         string
	SourceURL          string
	SourceFingerprint  string
	Hash               string
	Items              []PreparedItem
	Labels             []LabelPlan
	Warnings           []Diagnostic
	Errors             []Diagnostic
	SkippedRows        int
	AssigneesMatched   int
	AssigneesUnmatched int
	AssigneesAmbiguous int
}

func (p *Plan) Valid() bool {
	return p != nil && len(p.Errors) == 0
}

func (p *Plan) MainDocCount() int {
	count := 0
	for _, item := range p.Items {
		if len(item.PlateJSON) > 0 {
			count++
		}
	}
	return count
}

func (p *Plan) LabelsToCreate() int {
	count := 0
	for _, label := range p.Labels {
		if label.Create {
			count++
		}
	}
	return count
}

type Progress struct {
	Completed int
	Total     int
	Key       string
}

type ExecuteOptions struct {
	StateFile       string
	ContinueOnError bool
	Adopt           map[string]string
	RetryUnknown    map[string]bool
	OnProgress      func(Progress)
}

type Result struct {
	CreatedIssues   int
	SkippedIssues   int
	FailedIssues    int
	CreatedMainDocs int
	FailedMainDocs  int
	Warnings        []string
	Errors          []string
}

type PartialError struct {
	Failures int
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("import completed with %d failure(s)", e.Failures)
}

type UnknownOutcomeError struct {
	Key       string
	StateFile string
	Cause     error
}

func (e *UnknownOutcomeError) Error() string {
	return fmt.Sprintf(
		"outcome for %s is unknown; inspect Pulse, then use --adopt or --retry-unknown (state: %s): %v",
		e.Key, e.StateFile, e.Cause,
	)
}

func (e *UnknownOutcomeError) Unwrap() error {
	return e.Cause
}

type Runner struct {
	API API
}

func New(client API) *Runner {
	return &Runner{API: client}
}

func (r *Runner) Prepare(ctx context.Context, data *importers.ImportResult, opts Options) (*Plan, error) {
	if data == nil {
		return nil, fmt.Errorf("no import data")
	}
	if strings.TrimSpace(opts.TeamID) == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if opts.ImporterID == "" {
		opts.ImporterID = "jira-csv"
	}
	if opts.Assignee == "" {
		opts.Assignee = AssigneeNone
	}
	plan := &Plan{
		Options:           opts,
		SourcePath:        data.SourcePath,
		SourceURL:         data.SourceURL,
		SourceFingerprint: data.SourceFingerprint,
	}
	for _, diagnostic := range data.Diagnostics {
		target := &plan.Warnings
		if diagnostic.Level == importers.DiagnosticError {
			target = &plan.Errors
		} else if strings.Contains(strings.ToLower(diagnostic.Message), "skipped") {
			plan.SkippedRows++
		}
		*target = append(*target, Diagnostic{Row: diagnostic.Row, Message: diagnostic.Message})
	}

	users := []pulseapi.User(nil)
	if opts.Assignee == AssigneeMapped {
		var err error
		users, err = r.API.ListUsers(ctx)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
	}
	userLookup := indexUsers(users)
	labelPlans, labelKeyNames, err := r.prepareLabels(ctx, data.Labels, plan)
	if err != nil {
		return nil, err
	}
	plan.Labels = labelPlans

	for _, issue := range data.Issues {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item := PreparedItem{Key: issue.Key, Row: issue.SourceRow, RowHash: issue.RowHash}
		title := strings.TrimSpace(issue.Title)
		switch {
		case utf8.RuneCountInString(title) < 2:
			plan.Errors = append(plan.Errors, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow, Message: "title must contain at least 2 characters",
			})
		case utf8.RuneCountInString(title) > 200:
			title = truncateRunes(title, 200)
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow, Message: "title was truncated to 200 characters",
			})
		}
		if strings.TrimSpace(issue.Key) == "" || strings.TrimSpace(issue.RowHash) == "" {
			plan.Errors = append(plan.Errors, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow, Message: "stable Issue key and row hash are required",
			})
		}
		if len(issue.Labels) > 10 {
			plan.Errors = append(plan.Errors, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow,
				Message: fmt.Sprintf("issue has %d labels; Pulse allows at most 10", len(issue.Labels)),
			})
		}
		for _, key := range issue.Labels {
			if _, exists := labelKeyNames[key]; !exists {
				plan.Errors = append(plan.Errors, Diagnostic{
					Key: issue.Key, Row: issue.SourceRow, Message: fmt.Sprintf("label %q has no definition", key),
				})
				continue
			}
			item.LabelKeys = append(item.LabelKeys, key)
		}

		assigneeID, assigneeState := resolveAssignee(opts.Assignee, opts.SelfUserID, issue, data.Users, userLookup)
		switch assigneeState {
		case "matched":
			plan.AssigneesMatched++
		case "unmatched":
			plan.AssigneesUnmatched++
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow,
				Message: fmt.Sprintf("assignee %q did not match a Pulse user", issue.AssigneeID),
			})
		case "ambiguous":
			plan.AssigneesAmbiguous++
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow,
				Message: fmt.Sprintf("assignee %q matches multiple Pulse users and will remain unassigned", issue.AssigneeID),
			})
		}
		var projectID *string
		if opts.ProjectID != "" {
			projectID = stringPointer(opts.ProjectID)
		}
		item.Request = pulseapi.CreateIssueRequest{
			Title:      title,
			Status:     string(statusmap.Map(issue.Status)),
			Priority:   string(issue.Priority),
			Type:       string(issue.Type),
			TeamID:     opts.TeamID,
			ProjectID:  projectID,
			AssigneeID: assigneeID,
		}
		if strings.TrimSpace(issue.BodyMarkdown) != "" {
			plate, err := platemd.ToJSON(issue.BodyMarkdown, nil)
			if err != nil {
				plan.Errors = append(plan.Errors, Diagnostic{
					Key: issue.Key, Row: issue.SourceRow, Message: "convert main doc: " + err.Error(),
				})
			} else {
				item.PlateJSON = plate
			}
		}
		plan.Items = append(plan.Items, item)
	}
	plan.Hash = hashPlan(plan, labelKeyNames)
	return plan, nil
}

func (r *Runner) prepareLabels(
	ctx context.Context,
	labels map[string]importers.Label,
	plan *Plan,
) ([]LabelPlan, map[string]string, error) {
	existing, err := r.API.ListLabels(ctx, plan.Options.TeamID)
	if err != nil {
		return nil, nil, fmt.Errorf("list labels: %w", err)
	}
	existingByName := map[string]string{}
	for _, label := range existing {
		if label.Archived || (label.EntityType != "" && label.EntityType != "issue") {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(label.Name))
		if previous, exists := existingByName[key]; exists && previous != label.ID {
			plan.Errors = append(plan.Errors, Diagnostic{
				Message: fmt.Sprintf("Pulse has multiple active issue labels named %q", label.Name),
			})
			continue
		}
		existingByName[key] = label.ID
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalizedOwners := map[string]string{}
	names := map[string]string{}
	out := make([]LabelPlan, 0, len(keys))
	for _, key := range keys {
		original := strings.TrimSpace(labels[key].Name)
		name, changed := normalizeLabelName(original)
		if name == "" {
			plan.Errors = append(plan.Errors, Diagnostic{Message: fmt.Sprintf("label %q is empty", key)})
			continue
		}
		folded := strings.ToLower(name)
		if owner, exists := normalizedOwners[folded]; exists && owner != key {
			plan.Errors = append(plan.Errors, Diagnostic{
				Message: fmt.Sprintf("labels %q and %q normalize to the same Pulse label %q", owner, key, name),
			})
			continue
		}
		normalizedOwners[folded] = key
		names[key] = name
		if changed {
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Message: fmt.Sprintf("label %q will be imported as %q", original, name),
			})
		}
		labelPlan := LabelPlan{Key: key, Name: name}
		if id := existingByName[folded]; id != "" {
			labelPlan.ExistingID = id
		} else {
			labelPlan.Create = true
		}
		out = append(out, labelPlan)
	}
	return out, names, nil
}

func hashPlan(plan *Plan, labelNames map[string]string) string {
	type itemHash struct {
		Key       string
		RowHash   string
		Request   pulseapi.CreateIssueRequest
		LabelKeys []string
		HasDoc    bool
	}
	hashOptions := plan.Options
	if hashOptions.Assignee != AssigneeSelf {
		hashOptions.SelfUserID = ""
	}
	payload := struct {
		Options     Options
		SourceURL   string
		Fingerprint string
		Labels      map[string]string
		Items       []itemHash
	}{
		Options: hashOptions, SourceURL: plan.SourceURL,
		Fingerprint: plan.SourceFingerprint, Labels: labelNames,
	}
	for _, item := range plan.Items {
		payload.Items = append(payload.Items, itemHash{
			Key: item.Key, RowHash: item.RowHash, Request: item.Request,
			LabelKeys: item.LabelKeys, HasDoc: len(item.PlateJSON) > 0,
		})
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *Runner) Execute(ctx context.Context, plan *Plan, opts ExecuteOptions) (*Result, error) {
	if plan == nil {
		return nil, fmt.Errorf("import plan is nil")
	}
	if !plan.Valid() {
		return nil, fmt.Errorf("import plan has %d validation error(s)", len(plan.Errors))
	}
	journal, err := importstate.Open(opts.StateFile, importstate.Identity{
		Importer: plan.Options.ImporterID, SourceURL: plan.SourceURL,
		SourceFingerprint: plan.SourceFingerprint, APIURL: plan.Options.APIURL,
		WorkspaceID: plan.Options.WorkspaceID, TeamID: plan.Options.TeamID,
		ProjectID: plan.Options.ProjectID, AssigneeMode: string(plan.Options.Assignee),
		PlanHash: plan.Hash,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = journal.Close() }()

	itemsByKey := map[string]PreparedItem{}
	for _, item := range plan.Items {
		itemsByKey[strings.ToLower(item.Key)] = item
	}
	if err := r.applyAdoptions(ctx, journal, itemsByKey, opts.Adopt, plan.Options.TeamID); err != nil {
		return nil, err
	}
	labelIDs, err := r.ensureLabels(ctx, plan)
	if err != nil {
		return nil, err
	}

	result := &Result{}
	for index, item := range plan.Items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := r.executeItem(ctx, journal, item, labelIDs, opts, result); err != nil {
			return result, err
		}
		if opts.OnProgress != nil {
			opts.OnProgress(Progress{Completed: index + 1, Total: len(plan.Items), Key: item.Key})
		}
	}
	failures := result.FailedIssues + result.FailedMainDocs
	if failures > 0 {
		return result, &PartialError{Failures: failures}
	}
	return result, nil
}

func (r *Runner) applyAdoptions(
	ctx context.Context,
	journal *importstate.Journal,
	items map[string]PreparedItem,
	adoptions map[string]string,
	teamID string,
) error {
	for rawKey, issueID := range adoptions {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		item, exists := items[key]
		if !exists {
			return fmt.Errorf("--adopt references unknown Jira key %q", rawKey)
		}
		state, exists := journal.Item(item.Key)
		if !exists || state.EffectiveStatus() != importstate.StatusUnknown {
			return fmt.Errorf("--adopt for %s requires an unknown state entry", item.Key)
		}
		issue, err := r.API.GetIssue(ctx, issueID)
		if err != nil {
			return fmt.Errorf("validate adopted issue %s: %w", issueID, err)
		}
		if issue.TeamID != teamID {
			return fmt.Errorf("adopted issue %s belongs to team %s, expected %s", issueID, issue.TeamID, teamID)
		}
		if strings.TrimSpace(issue.Title) != strings.TrimSpace(item.Request.Title) {
			return fmt.Errorf(
				"adopted issue %s title %q does not match planned title %q",
				issueID, issue.Title, item.Request.Title,
			)
		}
		status := importstate.StatusCreated
		mainDocID := ""
		if issue.MainDocID != nil && *issue.MainDocID != "" {
			status = importstate.StatusDocUploaded
			mainDocID = *issue.MainDocID
		}
		if err := journal.Mark(importstate.Item{
			Key: item.Key, RowHash: item.RowHash, Status: status,
			IssueID: issue.ID, MainDocID: mainDocID, Message: "adopted by user",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) ensureLabels(ctx context.Context, plan *Plan) (map[string]string, error) {
	mapping := map[string]string{}
	for _, label := range plan.Labels {
		if label.ExistingID != "" {
			mapping[label.Key] = label.ExistingID
			continue
		}
		created, err := r.API.CreateLabel(ctx, plan.Options.TeamID, pulseapi.CreateLabelRequest{
			Name: label.Name, Color: pickColor(label.Name), EntityType: "issue",
		})
		if err == nil {
			mapping[label.Key] = created.ID
			continue
		}
		if !pulseapi.IsAmbiguousWriteError(err) {
			return nil, fmt.Errorf("create label %q: %w", label.Name, err)
		}
		labels, listErr := r.API.ListLabels(ctx, plan.Options.TeamID)
		if listErr != nil {
			return nil, fmt.Errorf("create label %q had an unknown outcome: %w", label.Name, err)
		}
		for _, candidate := range labels {
			if !candidate.Archived && strings.EqualFold(candidate.Name, label.Name) {
				mapping[label.Key] = candidate.ID
				break
			}
		}
		if mapping[label.Key] == "" {
			return nil, fmt.Errorf("create label %q had an unknown outcome: %w", label.Name, err)
		}
	}
	return mapping, nil
}

func (r *Runner) executeItem(
	ctx context.Context,
	journal *importstate.Journal,
	item PreparedItem,
	labelIDs map[string]string,
	opts ExecuteOptions,
	result *Result,
) error {
	state, hasState := journal.Item(item.Key)
	if hasState && state.RowHash != item.RowHash {
		return fmt.Errorf("source row for %s changed since the state file was created", item.Key)
	}
	if hasState && state.Status == importstate.StatusDocUploaded {
		result.SkippedIssues++
		return nil
	}
	if hasState && state.Status == importstate.StatusDocUnknown {
		issue, err := r.API.GetIssue(ctx, state.IssueID)
		if err != nil {
			return &UnknownOutcomeError{Key: item.Key, StateFile: journal.Path(), Cause: err}
		}
		if issue.MainDocID != nil && *issue.MainDocID != "" {
			if err := journal.Mark(importstate.Item{
				Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusDocUploaded,
				IssueID: state.IssueID, MainDocID: *issue.MainDocID,
			}); err != nil {
				return err
			}
			result.SkippedIssues++
			return nil
		}
		state.Status = importstate.StatusCreated
	}
	if hasState && state.EffectiveStatus() == importstate.StatusUnknown {
		if !opts.RetryUnknown[strings.ToLower(item.Key)] {
			return &UnknownOutcomeError{
				Key: item.Key, StateFile: journal.Path(),
				Cause: fmt.Errorf("previous create attempt did not receive a definitive response"),
			}
		}
		hasState = false
	}

	issueID := state.IssueID
	if !hasState || state.Status == importstate.StatusFailed {
		if err := journal.Mark(importstate.Item{
			Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusCreating,
		}); err != nil {
			return err
		}
		request := item.Request
		for _, key := range item.LabelKeys {
			if id := labelIDs[key]; id != "" {
				request.LabelIDs = append(request.LabelIDs, id)
			}
		}
		created, err := r.API.CreateIssue(ctx, request)
		if err != nil {
			result.FailedIssues++
			result.Errors = append(result.Errors, fmt.Sprintf("%s: create issue: %v", item.Key, err))
			status := importstate.StatusFailed
			if pulseapi.IsAmbiguousWriteError(err) {
				status = importstate.StatusUnknown
			}
			if markErr := journal.Mark(importstate.Item{
				Key: item.Key, RowHash: item.RowHash, Status: status, Message: err.Error(),
			}); markErr != nil {
				return errors.Join(err, markErr)
			}
			if status == importstate.StatusUnknown {
				return &UnknownOutcomeError{Key: item.Key, StateFile: journal.Path(), Cause: err}
			}
			if !opts.ContinueOnError {
				return &PartialError{Failures: 1}
			}
			return nil
		}
		issueID = created.ID
		result.CreatedIssues++
		if err := journal.Mark(importstate.Item{
			Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusCreated, IssueID: issueID,
		}); err != nil {
			return err
		}
	} else {
		result.SkippedIssues++
	}

	if len(item.PlateJSON) == 0 {
		return journal.Mark(importstate.Item{
			Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusDocUploaded, IssueID: issueID,
		})
	}
	if hasState && issueID != "" {
		issue, err := r.API.GetIssue(ctx, issueID)
		if err != nil {
			return fmt.Errorf("check main doc for %s: %w", item.Key, err)
		}
		if issue.MainDocID != nil && *issue.MainDocID != "" {
			return journal.Mark(importstate.Item{
				Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusDocUploaded,
				IssueID: issueID, MainDocID: *issue.MainDocID,
			})
		}
	}
	document, err := r.API.UploadMainDoc(ctx, issueID, item.Request.Title, item.PlateJSON)
	if err == nil {
		result.CreatedMainDocs++
		return journal.Mark(importstate.Item{
			Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusDocUploaded,
			IssueID: issueID, MainDocID: document.ID,
		})
	}

	if pulseapi.IsAmbiguousWriteError(err) {
		issue, getErr := r.API.GetIssue(ctx, issueID)
		if getErr != nil {
			_ = journal.Mark(importstate.Item{
				Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusDocUnknown,
				IssueID: issueID, Message: err.Error(),
			})
			return &UnknownOutcomeError{Key: item.Key, StateFile: journal.Path(), Cause: errors.Join(err, getErr)}
		}
		if issue.MainDocID != nil && *issue.MainDocID != "" {
			result.CreatedMainDocs++
			return journal.Mark(importstate.Item{
				Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusDocUploaded,
				IssueID: issueID, MainDocID: *issue.MainDocID,
			})
		}
	}
	result.FailedMainDocs++
	result.Errors = append(result.Errors, fmt.Sprintf("%s: upload main doc: %v", item.Key, err))
	if markErr := journal.Mark(importstate.Item{
		Key: item.Key, RowHash: item.RowHash, Status: importstate.StatusCreated,
		IssueID: issueID, Message: err.Error(),
	}); markErr != nil {
		return markErr
	}
	if !opts.ContinueOnError {
		return &PartialError{Failures: 1}
	}
	return nil
}
