package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

func (r *Runner) Execute(ctx context.Context, plan *Plan, opts ExecuteOptions) (*Result, error) {
	if plan == nil {
		return nil, fmt.Errorf("import plan is nil")
	}
	if !plan.Valid() {
		return nil, fmt.Errorf("import plan has %d validation error(s)", len(plan.Errors))
	}
	journal, err := importstate.Open(opts.StateFile, importstate.Identity{
		Importer:          plan.Options.ImporterID,
		SourceURL:         plan.SourceURL,
		SourceFingerprint: plan.SourceFingerprint,
		APIURL:            plan.Options.APIURL,
		WorkspaceID:       plan.Options.WorkspaceID,
		TeamID:            plan.Options.TeamID,
		ProjectID:         plan.Options.ProjectID,
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

	exec := &executor{
		runner:  r,
		plan:    plan,
		opts:    opts,
		journal: journal,
		result:  &Result{},
	}
	if exec.labelIDs, err = r.ensureLabels(ctx, plan); err != nil {
		return exec.result, err
	}

	if err := exec.run(ctx); err != nil {
		return exec.result, err
	}
	failures := exec.result.FailedIssues + exec.result.FailedMainDocs +
		exec.result.FailedComments + exec.result.FailedLinks
	if failures > 0 {
		return exec.result, &PartialError{Failures: failures}
	}
	return exec.result, nil
}

type executor struct {
	runner   *Runner
	plan     *Plan
	opts     ExecuteOptions
	journal  *importstate.Journal
	labelIDs map[string]string

	mu       sync.Mutex
	result   *Result
	finished int
}

// run walks the waves in order. Creation order is a hard requirement, not an
// optimisation: a sub-issue needs its parent's id and an issue needs its
// project's id, so wave N+1 may not start until wave N has fully landed.
func (e *executor) run(ctx context.Context) error {
	waves := map[int][]PreparedItem{}
	order := []int{}
	for _, item := range e.plan.Items {
		if _, seen := waves[item.Wave]; !seen {
			order = append(order, item.Wave)
		}
		waves[item.Wave] = append(waves[item.Wave], item)
	}
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if order[j] < order[i] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}

	for _, wave := range order {
		if err := e.runWave(ctx, waves[wave]); err != nil {
			return err
		}
	}
	return e.runLinkPass(ctx)
}

func (e *executor) runWave(ctx context.Context, items []PreparedItem) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(e.plan.Options.Concurrency)
	for _, item := range items {
		item := item
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			err := e.processItem(groupCtx, item)
			e.reportProgress(item.Key, "create")
			if errors.Is(err, errItemIncomplete) {
				return nil
			}
			return err
		})
	}
	return group.Wait()
}

func (e *executor) reportProgress(key, phase string) {
	e.mu.Lock()
	e.finished++
	completed := e.finished
	e.mu.Unlock()
	if e.opts.OnProgress != nil {
		e.opts.OnProgress(Progress{
			Completed: completed, Total: e.plan.TotalWrites(), Key: key, Phase: phase,
		})
	}
}

func (e *executor) addResult(mutate func(*Result)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	mutate(e.result)
}

// errItemIncomplete stops one item's phase ladder without stopping the run. It
// is what --continue-on-error means: the item stays at the phase it reached, so a
// later resume picks up exactly the work that is still owed. Treating a failed
// phase as "carry on to the next one" would mark the item complete and lose the
// failed phase forever.
var errItemIncomplete = errors.New("item did not complete")

// itemFailure records a definitive per-item failure and decides whether the run
// continues.
func (e *executor) itemFailure(message string, bump func(*Result)) error {
	e.addResult(func(result *Result) {
		bump(result)
		result.Errors = append(result.Errors, message)
	})
	if !e.opts.ContinueOnError {
		return &PartialError{Failures: 1}
	}
	return errItemIncomplete
}

func (e *executor) warn(message string) {
	e.addResult(func(result *Result) {
		result.Warnings = append(result.Warnings, message)
	})
}

// processItem drives one item as far along the create → doc → comments ladder as
// it still needs to go, resuming from whatever the journal already recorded.
func (e *executor) processItem(ctx context.Context, item PreparedItem) error {
	state, hasState := e.journal.Item(item.Key)
	if hasState && state.RowHash != item.RowHash {
		return fmt.Errorf("source row for %s changed since the state file was created", item.Key)
	}
	if hasState && state.Complete() {
		e.addResult(func(result *Result) { result.SkippedIssues++ })
		return nil
	}

	if hasState {
		resolved, err := e.resolveAmbiguous(ctx, item, state)
		if err != nil {
			return err
		}
		state = resolved
	}

	entityID := state.EntityID()
	created := false
	if !hasState || entityID == "" || state.Status == importstate.StatusFailed {
		id, err, fatal := e.create(ctx, item)
		if fatal != nil {
			return fatal
		}
		if err != nil {
			return err
		}
		entityID, created = id, true
	} else {
		e.addResult(func(result *Result) { result.SkippedIssues++ })
	}

	if err := e.uploadDoc(ctx, item, entityID, created); err != nil {
		return err
	}
	if err := e.postComments(ctx, item, entityID); err != nil {
		return err
	}
	// Relations reference issues that may not exist yet, so an item that owes
	// links stays one phase short until the link pass.
	if !item.NeedsLinkPass() {
		return e.mark(item, entityID, importstate.StatusLinked, "")
	}
	return nil
}

// resolveAmbiguous turns an interrupted write into a definite state, or refuses
// to guess. A create whose response never arrived may or may not have happened,
// and silently retrying it is how imports produce duplicates.
func (e *executor) resolveAmbiguous(
	ctx context.Context,
	item PreparedItem,
	state importstate.Item,
) (importstate.Item, error) {
	switch state.Status {
	case importstate.StatusDocUnknown:
		docID, err := e.mainDocID(ctx, item.Kind, state.EntityID())
		if err != nil {
			return state, &UnknownOutcomeError{Key: item.Key, StateFile: e.journal.Path(), Cause: err}
		}
		if docID != "" {
			state.Status, state.MainDocID = importstate.StatusDocUploaded, docID
			if err := e.journal.Mark(state); err != nil {
				return state, err
			}
			return state, nil
		}
		state.Status = importstate.StatusCreated
		return state, nil

	case importstate.StatusCommentUnknown:
		// We are the only writer during an import, so the comment count is a
		// reliable witness for whether the interrupted POST landed.
		if state.Kind != importstate.KindProject && state.IssueID != "" {
			if total, err := e.runner.API.CountComments(ctx, state.IssueID); err == nil &&
				int(total) > state.Comments {
				state.Comments = int(total)
			}
		}
		state.Status = importstate.StatusDocUploaded
		// The reconciled count has to be persisted before the comment phase runs:
		// that phase reads the journal, not this local copy, and would otherwise
		// re-post the comment whose response was lost.
		if err := e.journal.Mark(state); err != nil {
			return state, err
		}
		return state, nil
	}

	if state.EffectiveStatus() == importstate.StatusUnknown {
		if !e.opts.RetryUnknown[strings.ToLower(item.Key)] {
			return state, &UnknownOutcomeError{
				Key: item.Key, StateFile: e.journal.Path(),
				Cause: fmt.Errorf("previous create attempt did not receive a definitive response"),
			}
		}
		// The user has confirmed nothing was created; start this item over.
		return importstate.Item{Key: item.Key, RowHash: item.RowHash}, nil
	}
	return state, nil
}

// create makes the issue or project. It returns ("", nil, nil) when a definitive
// failure was recorded and the run may continue.
func (e *executor) create(ctx context.Context, item PreparedItem) (string, error, error) {
	request := item.Issue
	if item.Kind == importstate.KindIssue {
		resolved, err := e.resolveReferences(item, &request)
		if err != nil {
			if markErr := e.mark(item, "", importstate.StatusFailed, err.Error()); markErr != nil {
				return "", nil, markErr
			}
			return "", e.itemFailure(
				fmt.Sprintf("%s: %v", item.Key, err),
				func(result *Result) { result.FailedIssues++ },
			), nil
		}
		request = resolved
		for _, key := range item.LabelKeys {
			if id := e.labelIDs[key]; id != "" {
				request.LabelIDs = append(request.LabelIDs, id)
			}
		}
	}

	if err := e.mark(item, "", importstate.StatusCreating, ""); err != nil {
		return "", nil, err
	}

	var (
		id  string
		err error
	)
	if item.Kind == importstate.KindProject {
		var project *pulseapi.Project
		project, err = e.runner.API.CreateProject(ctx, item.Project)
		if project != nil {
			id = project.ID
		}
	} else {
		var issue *pulseapi.Issue
		issue, err = e.runner.API.CreateIssue(ctx, request)
		if issue != nil {
			id = issue.ID
		}
	}

	if err != nil {
		status := importstate.StatusFailed
		if pulseapi.IsAmbiguousWriteError(err) {
			status = importstate.StatusUnknown
		}
		if markErr := e.mark(item, "", status, err.Error()); markErr != nil {
			return "", nil, errors.Join(err, markErr)
		}
		if status == importstate.StatusUnknown {
			return "", nil, &UnknownOutcomeError{Key: item.Key, StateFile: e.journal.Path(), Cause: err}
		}
		if pulseapi.IsForbidden(err) {
			return "", nil, &PermissionError{
				Action:     fmt.Sprintf("creating %s %s", item.Kind, item.Key),
				Permission: permissionFor(item.Kind),
				Remedy:     "Ask a workspace admin or a manager of the target team to run the import.",
				Cause:      err,
			}
		}
		return "", e.itemFailure(
			fmt.Sprintf("%s: create %s: %v", item.Key, item.Kind, err),
			func(result *Result) { result.FailedIssues++ },
		), nil
	}

	e.addResult(func(result *Result) {
		if item.Kind == importstate.KindProject {
			result.CreatedProjects++
			return
		}
		result.CreatedIssues++
	})
	return id, nil, e.mark(item, id, importstate.StatusCreated, "")
}

func permissionFor(kind importstate.Kind) string {
	if kind == importstate.KindProject {
		return "projects:create"
	}
	return "issues:create"
}

// resolveReferences fills in the parent and project ids, which only exist once
// the referenced items have been created.
func (e *executor) resolveReferences(item PreparedItem, request *pulseapi.CreateIssueRequest) (pulseapi.CreateIssueRequest, error) {
	out := *request
	if item.ParentKey != "" {
		parent, ok := e.journal.Item(item.ParentKey)
		if !ok || parent.IssueID == "" {
			return out, fmt.Errorf(
				"parent %s was not created, so %s cannot be imported as its sub-issue; "+
					"re-run to retry both once the parent succeeds",
				item.ParentKey, item.Key,
			)
		}
		out.ParentID = stringPointer(parent.IssueID)
		return out, nil
	}
	if item.EpicKey != "" {
		project, ok := e.journal.Item(item.EpicKey)
		if !ok || project.ProjectID == "" {
			return out, fmt.Errorf(
				"project for epic %s was not created, so %s cannot be filed into it; "+
					"re-run to retry both once the project succeeds",
				item.EpicKey, item.Key,
			)
		}
		out.ProjectID = stringPointer(project.ProjectID)
	}
	return out, nil
}

func (e *executor) uploadDoc(ctx context.Context, item PreparedItem, entityID string, freshlyCreated bool) error {
	if len(item.PlateJSON) == 0 {
		return e.mark(item, entityID, importstate.StatusDocUploaded, "")
	}
	state, hasState := e.journal.Item(item.Key)
	if hasState && importstate.Phase(state.Status) >= importstate.Phase(importstate.StatusDocUploaded) {
		return nil
	}
	// An item that already existed may already carry its Main Doc from an
	// earlier run that died before the journal was updated.
	if !freshlyCreated {
		if docID, err := e.mainDocID(ctx, item.Kind, entityID); err == nil && docID != "" {
			return e.mark(item, entityID, importstate.StatusDocUploaded, "")
		}
	}

	document, err := e.runner.API.UploadMainDoc(ctx, string(item.Kind), entityID, item.Title, item.PlateJSON)
	if err == nil {
		e.addResult(func(result *Result) { result.CreatedMainDocs++ })
		state, _ := e.journal.Item(item.Key)
		state.Key, state.RowHash = item.Key, item.RowHash
		state.Kind, state.Status, state.MainDocID = item.Kind, importstate.StatusDocUploaded, document.ID
		e.setEntityID(&state, item.Kind, entityID)
		return e.journal.Mark(state)
	}

	if pulseapi.IsAmbiguousWriteError(err) {
		docID, getErr := e.mainDocID(ctx, item.Kind, entityID)
		if getErr != nil {
			if markErr := e.mark(item, entityID, importstate.StatusDocUnknown, err.Error()); markErr != nil {
				return markErr
			}
			return &UnknownOutcomeError{
				Key: item.Key, StateFile: e.journal.Path(), Cause: errors.Join(err, getErr),
			}
		}
		if docID != "" {
			e.addResult(func(result *Result) { result.CreatedMainDocs++ })
			return e.mark(item, entityID, importstate.StatusDocUploaded, "")
		}
	}
	if err := e.mark(item, entityID, importstate.StatusCreated, err.Error()); err != nil {
		return err
	}
	return e.itemFailure(
		fmt.Sprintf("%s: upload main doc: %v", item.Key, err),
		func(result *Result) { result.FailedMainDocs++ },
	)
}

func (e *executor) postComments(ctx context.Context, item PreparedItem, entityID string) error {
	if len(item.Comments) == 0 || item.Kind == importstate.KindProject {
		return e.mark(item, entityID, importstate.StatusCommented, "")
	}
	state, _ := e.journal.Item(item.Key)
	if importstate.Phase(state.Status) >= importstate.Phase(importstate.StatusCommented) {
		return nil
	}

	posted := state.Comments
	for index := posted; index < len(item.Comments); index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := e.runner.API.CreateComment(ctx, pulseapi.CreateCommentRequest{
			TargetType: "issue", TargetID: entityID, Text: item.Comments[index],
		})
		if err == nil {
			posted = index + 1
			if markErr := e.markComments(item, entityID, posted, importstate.StatusDocUploaded); markErr != nil {
				return markErr
			}
			continue
		}

		if pulseapi.IsAmbiguousWriteError(err) {
			total, countErr := e.runner.API.CountComments(ctx, entityID)
			if countErr == nil && int(total) > index {
				posted = index + 1
				if markErr := e.markComments(item, entityID, posted, importstate.StatusDocUploaded); markErr != nil {
					return markErr
				}
				continue
			}
			if markErr := e.markComments(item, entityID, posted, importstate.StatusCommentUnknown); markErr != nil {
				return markErr
			}
			return &UnknownOutcomeError{Key: item.Key, StateFile: e.journal.Path(), Cause: err}
		}

		if markErr := e.markComments(item, entityID, posted, importstate.StatusDocUploaded); markErr != nil {
			return markErr
		}
		return e.itemFailure(
			fmt.Sprintf("%s: create comment %d/%d: %v", item.Key, index+1, len(item.Comments), err),
			func(result *Result) { result.FailedComments++ },
		)
	}

	e.addResult(func(result *Result) { result.CreatedComments += posted - state.Comments })
	return e.markComments(item, entityID, posted, importstate.StatusCommented)
}

// runLinkPass applies issue relations once every item has an id.
func (e *executor) runLinkPass(ctx context.Context) error {
	var pending []PreparedItem
	for _, item := range e.plan.Items {
		if item.Kind != importstate.KindIssue || !item.NeedsLinkPass() {
			continue
		}
		state, ok := e.journal.Item(item.Key)
		if !ok || state.IssueID == "" || state.Complete() {
			continue
		}
		if importstate.Phase(state.Status) < importstate.Phase(importstate.StatusCommented) {
			continue
		}
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		return nil
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(e.plan.Options.Concurrency)
	for _, item := range pending {
		item := item
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				return err
			}
			err := e.linkItem(groupCtx, item)
			e.reportProgress(item.Key, "link")
			if errors.Is(err, errItemIncomplete) {
				return nil
			}
			return err
		})
	}
	return group.Wait()
}

func (e *executor) linkItem(ctx context.Context, item PreparedItem) error {
	state, _ := e.journal.Item(item.Key)
	blocks, missingBlocks := e.resolveKeys(item.Blocks)
	blockedBy, missingBlockedBy := e.resolveKeys(item.BlockedBy)
	for _, key := range append(missingBlocks, missingBlockedBy...) {
		e.warn(fmt.Sprintf("%s: link to %s was dropped because that issue was not created", item.Key, key))
	}
	if len(blocks) == 0 && len(blockedBy) == 0 {
		return e.mark(item, state.IssueID, importstate.StatusLinked, "")
	}

	request := pulseapi.UpdateIssueRequest{}
	if len(blocks) > 0 {
		request.BlocksIDs = &blocks
	}
	if len(blockedBy) > 0 {
		request.BlockedByIDs = &blockedBy
	}
	if _, err := e.runner.API.UpdateIssue(ctx, state.IssueID, request); err != nil {
		// A failed link is not ambiguous in a way that can duplicate anything:
		// the update is idempotent, so a re-run simply applies it again.
		return e.itemFailure(
			fmt.Sprintf("%s: apply issue links: %v", item.Key, err),
			func(result *Result) { result.FailedLinks++ },
		)
	}
	e.addResult(func(result *Result) { result.LinkedIssues++ })
	return e.mark(item, state.IssueID, importstate.StatusLinked, "")
}

func (e *executor) resolveKeys(keys []string) (ids []string, missing []string) {
	seen := map[string]bool{}
	for _, key := range keys {
		state, ok := e.journal.Item(key)
		if !ok || state.IssueID == "" {
			missing = append(missing, key)
			continue
		}
		if seen[state.IssueID] {
			continue
		}
		seen[state.IssueID] = true
		ids = append(ids, state.IssueID)
	}
	return ids, missing
}

func (e *executor) mainDocID(ctx context.Context, kind importstate.Kind, entityID string) (string, error) {
	if entityID == "" {
		return "", fmt.Errorf("no entity id recorded")
	}
	if kind == importstate.KindProject {
		project, err := e.runner.API.GetProject(ctx, entityID)
		if err != nil {
			return "", err
		}
		if project.MainDocID == nil {
			return "", nil
		}
		return *project.MainDocID, nil
	}
	issue, err := e.runner.API.GetIssue(ctx, entityID)
	if err != nil {
		return "", err
	}
	if issue.MainDocID == nil {
		return "", nil
	}
	return *issue.MainDocID, nil
}

func (e *executor) setEntityID(state *importstate.Item, kind importstate.Kind, id string) {
	if id == "" {
		return
	}
	if kind == importstate.KindProject {
		state.ProjectID = id
		return
	}
	state.IssueID = id
}

func (e *executor) mark(item PreparedItem, entityID string, status importstate.Status, message string) error {
	state := importstate.Item{
		Key: item.Key, Kind: item.Kind, RowHash: item.RowHash,
		Status: status, Message: message,
	}
	e.setEntityID(&state, item.Kind, entityID)
	return e.journal.Mark(state)
}

func (e *executor) markComments(item PreparedItem, entityID string, posted int, status importstate.Status) error {
	state := importstate.Item{
		Key: item.Key, Kind: item.Kind, RowHash: item.RowHash,
		Status: status, Comments: posted,
	}
	e.setEntityID(&state, item.Kind, entityID)
	return e.journal.Mark(state)
}

func (r *Runner) applyAdoptions(
	ctx context.Context,
	journal *importstate.Journal,
	items map[string]PreparedItem,
	adoptions map[string]string,
	teamID string,
) error {
	for rawKey, entityID := range adoptions {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		item, exists := items[key]
		if !exists {
			return fmt.Errorf("--adopt references unknown source key %q", rawKey)
		}
		state, exists := journal.Item(item.Key)
		if !exists || state.EffectiveStatus() != importstate.StatusUnknown {
			return fmt.Errorf("--adopt for %s requires an unknown state entry", item.Key)
		}

		if item.Kind == importstate.KindProject {
			project, err := r.API.GetProject(ctx, entityID)
			if err != nil {
				return fmt.Errorf("validate adopted project %s: %w", entityID, err)
			}
			if project.TeamID != teamID {
				return fmt.Errorf("adopted project %s belongs to team %s, expected %s",
					entityID, project.TeamID, teamID)
			}
			if strings.TrimSpace(project.Title) != strings.TrimSpace(item.Project.Title) {
				return fmt.Errorf("adopted project %s title %q does not match planned title %q",
					entityID, project.Title, item.Project.Title)
			}
			if err := journal.Mark(importstate.Item{
				Key: item.Key, Kind: importstate.KindProject, RowHash: item.RowHash,
				Status: importstate.StatusCreated, ProjectID: project.ID, Message: "adopted by user",
			}); err != nil {
				return err
			}
			continue
		}

		issue, err := r.API.GetIssue(ctx, entityID)
		if err != nil {
			return fmt.Errorf("validate adopted issue %s: %w", entityID, err)
		}
		if issue.TeamID != teamID {
			return fmt.Errorf("adopted issue %s belongs to team %s, expected %s",
				entityID, issue.TeamID, teamID)
		}
		if strings.TrimSpace(issue.Title) != strings.TrimSpace(item.Issue.Title) {
			return fmt.Errorf("adopted issue %s title %q does not match planned title %q",
				entityID, issue.Title, item.Issue.Title)
		}
		status := importstate.StatusCreated
		mainDocID := ""
		if issue.MainDocID != nil && *issue.MainDocID != "" {
			status = importstate.StatusDocUploaded
			mainDocID = *issue.MainDocID
		}
		if err := journal.Mark(importstate.Item{
			Key: item.Key, Kind: importstate.KindIssue, RowHash: item.RowHash,
			Status: status, IssueID: issue.ID, MainDocID: mainDocID, Message: "adopted by user",
		}); err != nil {
			return err
		}
	}
	return nil
}

// ensureLabels resolves every planned label to an id before the first issue is
// created, so a permissions or naming problem surfaces while nothing has been
// written yet.
func (r *Runner) ensureLabels(ctx context.Context, plan *Plan) (map[string]string, error) {
	mapping := map[string]string{}
	for _, label := range plan.Labels {
		if label.ExistingID != "" {
			mapping[label.Key] = label.ExistingID
			continue
		}
		// Pulse's uniqueness index spans archived labels, so reusing the
		// archived one is the only way to get this name back.
		if label.ArchivedID != "" {
			restored, err := r.API.UnarchiveLabel(ctx, label.ArchivedID)
			if err != nil {
				return nil, labelPermissionError(fmt.Errorf(
					"unarchive label %q: %w", label.Name, err,
				), err)
			}
			mapping[label.Key] = restored.ID
			continue
		}

		created, err := r.API.CreateLabel(ctx, plan.Options.TeamID, pulseapi.CreateLabelRequest{
			Name: label.Name, Color: pickColor(label.Name), EntityType: "issue",
		})
		if err == nil {
			mapping[label.Key] = created.ID
			continue
		}

		// A 409 means the name is taken by a label the plan did not see —
		// most often one archived between preflight and now, or created
		// concurrently. Look again, archived included, before giving up.
		if pulseapi.IsConflict(err) {
			if id, found := r.findLabel(ctx, plan.Options.TeamID, label.Name); found {
				mapping[label.Key] = id
				continue
			}
		}
		if !pulseapi.IsAmbiguousWriteError(err) {
			return nil, labelPermissionError(fmt.Errorf("create label %q: %w", label.Name, err), err)
		}
		if id, found := r.findLabel(ctx, plan.Options.TeamID, label.Name); found {
			mapping[label.Key] = id
			continue
		}
		return nil, fmt.Errorf("create label %q had an unknown outcome: %w", label.Name, err)
	}
	return mapping, nil
}

// findLabel looks a label up by name across live and archived labels, restoring
// an archived one so it can be used.
func (r *Runner) findLabel(ctx context.Context, teamID, name string) (string, bool) {
	if labels, err := r.API.ListLabels(ctx, teamID); err == nil {
		for _, candidate := range labels {
			if !candidate.Archived && strings.EqualFold(candidate.Name, name) {
				return candidate.ID, true
			}
		}
	}
	archived, err := r.API.ListArchivedLabels(ctx, teamID)
	if err != nil {
		return "", false
	}
	for _, candidate := range archived {
		if !strings.EqualFold(candidate.Name, name) {
			continue
		}
		restored, err := r.API.UnarchiveLabel(ctx, candidate.ID)
		if err != nil {
			return "", false
		}
		return restored.ID, true
	}
	return "", false
}

func labelPermissionError(wrapped, cause error) error {
	if !pulseapi.IsForbidden(cause) {
		return wrapped
	}
	return &PermissionError{
		Action:     "creating team labels",
		Permission: "labels:create",
		Remedy: "By default Pulse lets a manager of the team or a workspace owner/admin manage labels, " +
			"but workspaces with custom roles can differ — what matters is the labels:create permission. " +
			"Re-run with --skip-labels to import without labels, or ask an admin to run the import.",
		Cause: wrapped,
	}
}
