package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importstate"
	"github.com/try-pulse/pulse-import/internal/platemd"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

// migratedLabelName marks everything an import created. Pulse has no
// server-side import id, so this label is the only way to select the imported
// set afterwards.
const migratedLabelName = "Migrated"

const migratedLabelKey = "migrated"

type API interface {
	ListTeamMembers(context.Context, string) ([]pulseapi.TeamMember, error)
	ListLabels(context.Context, string) ([]pulseapi.Label, error)
	ListArchivedLabels(context.Context, string) ([]pulseapi.Label, error)
	UnarchiveLabel(context.Context, string) (*pulseapi.Label, error)
	CreateLabel(context.Context, string, pulseapi.CreateLabelRequest) (*pulseapi.Label, error)
	CreateIssue(context.Context, pulseapi.CreateIssueRequest) (*pulseapi.Issue, error)
	UpdateIssue(context.Context, string, pulseapi.UpdateIssueRequest) (*pulseapi.Issue, error)
	GetIssue(context.Context, string) (*pulseapi.Issue, error)
	CreateProject(context.Context, pulseapi.CreateProjectRequest) (*pulseapi.Project, error)
	GetProject(context.Context, string) (*pulseapi.Project, error)
	UploadMainDoc(context.Context, string, string, string, []byte) (*pulseapi.Document, error)
	CreateComment(context.Context, pulseapi.CreateCommentRequest) (*pulseapi.Comment, error)
	CountComments(context.Context, string) (int64, error)
	CountTeamIssues(context.Context, string) (int64, error)
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
	if opts.LabelPolicy == "" {
		opts.LabelPolicy = LabelPolicyDrop
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if len(opts.TeamPath) == 0 {
		opts.TeamPath = []string{opts.TeamID}
	}

	plan := &Plan{
		Options:           opts,
		SourcePath:        data.SourcePath,
		SourceURL:         data.SourceURL,
		SourceFingerprint: data.SourceFingerprint,
		StatusMapping:     data.StatusNames,
		IgnoredColumns:    data.IgnoredColumns,
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

	people, err := r.loadRoster(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := validateAssigneeMode(opts, people, plan); err != nil {
		return nil, err
	}
	users := resolveUsers(opts.Assignee, opts.SelfUserID, data.Users, opts.UserMap, people)
	plan.UserMapping = orderedUserResolutions(users)

	if count, err := r.API.CountTeamIssues(ctx, opts.TeamID); err == nil {
		plan.TeamIssueCount = count
	}

	labels := collectLabels(data, opts)
	labelPlans, labelNames, err := r.prepareLabels(ctx, labels, plan)
	if err != nil {
		return nil, err
	}
	plan.Labels = labelPlans

	kept := filterIssues(data.Issues, opts, plan)
	referenced := referencedKeys(kept)
	r.prepareProjects(data, opts, plan, referenced, labelNames)
	r.prepareIssues(kept, opts, plan, users, labelNames)

	sortItems(plan.Items)
	plan.Hash = hashPlan(plan, labelNames)
	return plan, nil
}

// loadRoster gathers everyone Pulse would accept as an assignee: members of the
// target team and of every ancestor team.
func (r *Runner) loadRoster(ctx context.Context, opts Options) (roster, error) {
	if opts.Assignee == AssigneeNone {
		return newRoster(nil), nil
	}
	var members []pulseapi.TeamMember
	seen := map[string]bool{}
	for _, teamID := range opts.TeamPath {
		if teamID == "" || seen[teamID] {
			continue
		}
		seen[teamID] = true
		batch, err := r.API.ListTeamMembers(ctx, teamID)
		if err != nil {
			if pulseapi.IsForbidden(err) {
				return roster{}, &PermissionError{
					Action:     "reading team members for assignee mapping",
					Permission: "teams:read",
					Remedy:     "Re-run with --assignee none to import everything unassigned, or ask a workspace admin to run the import.",
					Cause:      err,
				}
			}
			return roster{}, fmt.Errorf("list members of team %s: %w", teamID, err)
		}
		members = append(members, batch...)
	}
	return newRoster(members), nil
}

// validateAssigneeMode fails fast on the two cases that would otherwise reject
// every single issue at create time.
func validateAssigneeMode(opts Options, people roster, plan *Plan) error {
	if opts.Assignee == AssigneeSelf {
		if opts.SelfUserID == "" || !people.has(opts.SelfUserID) {
			plan.Errors = append(plan.Errors, Diagnostic{Message: fmt.Sprintf(
				"--self-assign needs you to be a member of the target team (or one of its parent teams); "+
					"Pulse rejects an assignee outside team %s. Join the team, or use --assignee none",
				opts.TeamID,
			)})
		}
		return nil
	}
	for sourceKey, target := range opts.UserMap {
		if strings.EqualFold(target, SkipUser) {
			continue
		}
		if people.has(target) || len(people.findByEmailOrName(target)) == 1 {
			continue
		}
		plan.Errors = append(plan.Errors, Diagnostic{Message: fmt.Sprintf(
			"--map-user %q=%q does not resolve to a member of team %s or its parent teams; "+
				"Pulse would reject that assignee. Use a member's id or email, or map to %q",
			sourceKey, target, opts.TeamID, SkipUser,
		)})
	}
	return nil
}

// collectLabels adds the importer's labels plus the Migrated marker.
func collectLabels(data *importers.ImportResult, opts Options) map[string]importers.Label {
	labels := make(map[string]importers.Label, len(data.Labels)+1)
	if opts.SkipLabels {
		if opts.AddMigratedLabel {
			labels[migratedLabelKey] = importers.Label{
				Name: migratedLabelName, Kind: importers.LabelKindMigrated,
			}
		}
		return labels
	}
	for key, label := range data.Labels {
		labels[key] = label
	}
	if opts.AddMigratedLabel {
		labels[migratedLabelKey] = importers.Label{
			Name: migratedLabelName, Kind: importers.LabelKindMigrated,
		}
	}
	return labels
}

// filterIssues applies the status and staleness filters.
func filterIssues(issues []importers.Issue, opts Options, plan *Plan) []importers.Issue {
	kept := make([]importers.Issue, 0, len(issues))
	for _, issue := range issues {
		status := resolveStatus(issue)
		switch {
		case len(opts.OnlyStatuses) > 0 && !opts.OnlyStatuses[status]:
			plan.FilteredIssues++
			continue
		case opts.SkipStatuses[status]:
			plan.FilteredIssues++
			continue
		}
		if opts.StaleAfter > 0 && issue.UpdatedAt != nil &&
			opts.Now.Sub(*issue.UpdatedAt) > opts.StaleAfter {
			plan.FilteredIssues++
			continue
		}
		kept = append(kept, issue)
	}
	return kept
}

// resolveStatus applies a source resolution override over the mapped status.
func resolveStatus(issue importers.Issue) statusmap.PulseStatus {
	if issue.StatusOverride != "" {
		return statusmap.PulseStatus(issue.StatusOverride)
	}
	return statusmap.Map(issue.Status)
}

// referencedKeys is the set of parent and epic keys that survived filtering, so
// a project is only created when something still needs it.
func referencedKeys(issues []importers.Issue) map[string]bool {
	out := map[string]bool{}
	for _, issue := range issues {
		if issue.EpicKey != "" {
			out[strings.ToLower(issue.EpicKey)] = true
		}
		if issue.ParentKey != "" {
			out[strings.ToLower(issue.ParentKey)] = true
		}
	}
	return out
}

func (r *Runner) prepareProjects(
	data *importers.ImportResult,
	opts Options,
	plan *Plan,
	referenced map[string]bool,
	labelNames map[string]string,
) {
	for _, project := range data.Projects {
		// A forced --project pins every issue, so epic projects would be dead
		// weight; the epic is still recorded as a label on each child.
		if opts.ProjectID != "" {
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Key: project.Key, Row: project.SourceRow,
				Message: "--project was given, so this epic was not created as a Pulse project",
			})
			continue
		}
		if !referenced[strings.ToLower(project.Key)] {
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Key: project.Key, Row: project.SourceRow,
				Message: "epic has no imported children; it was not created as a Pulse project",
			})
			continue
		}

		title, ok := checkTitle(project.Title, project.Key, project.SourceRow, plan)
		if !ok {
			continue
		}
		item := PreparedItem{
			Key: project.Key, Kind: importstate.KindProject, Row: project.SourceRow,
			RowHash: project.RowHash, Title: title, Wave: 0,
		}
		item.Project = pulseapi.CreateProjectRequest{
			Title:  title,
			Status: projectStatusFor(project.Status),
			TeamID: opts.TeamID,
		}
		// Labels are deliberately not carried onto the project: Pulse scopes
		// labels by entity_type, and the labels gathered from the epic row are
		// issue labels. Attaching them here would be rejected as an entity-type
		// mismatch, and creating parallel project labels would double every
		// label in the team.
		item.PlateJSON = convertDoc(project.BodyMarkdown, project.Key, project.SourceRow, plan)
		plan.Items = append(plan.Items, item)
		plan.ProjectCount++
	}
}

// projectStatusFor maps the epic's workflow position onto Pulse's project
// lifecycle, which is a different vocabulary from issue status.
func projectStatusFor(sourceStatus string) string {
	switch statusmap.Map(sourceStatus) {
	case statusmap.Done:
		return "completed"
	case statusmap.InProgress, statusmap.QA, statusmap.Release:
		return "in_progress"
	case statusmap.Todo:
		return "ready"
	default:
		return "idea"
	}
}

func (r *Runner) prepareIssues(
	issues []importers.Issue,
	opts Options,
	plan *Plan,
	users map[string]UserResolution,
	labelNames map[string]string,
) {
	// Only keys that actually became projects can be used as a project link.
	projectKeys := map[string]bool{}
	for _, item := range plan.Items {
		if item.Kind == importstate.KindProject {
			projectKeys[strings.ToLower(item.Key)] = true
		}
	}
	issueKeys := map[string]bool{}
	for _, issue := range issues {
		issueKeys[strings.ToLower(issue.Key)] = true
	}

	for _, issue := range issues {
		title, ok := checkTitle(issue.Title, issue.Key, issue.SourceRow, plan)
		if !ok {
			continue
		}
		if strings.TrimSpace(issue.Key) == "" || strings.TrimSpace(issue.RowHash) == "" {
			plan.Errors = append(plan.Errors, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow,
				Message: "stable Issue key and row hash are required",
			})
			continue
		}

		item := PreparedItem{
			Key: issue.Key, Kind: importstate.KindIssue, Row: issue.SourceRow,
			RowHash: issue.RowHash, Title: title, Wave: 1,
		}

		labelKeys := issue.Labels
		if opts.SkipLabels {
			labelKeys = nil
		}
		if opts.AddMigratedLabel {
			labelKeys = append([]string{migratedLabelKey}, labelKeys...)
		}
		item.LabelKeys = resolveLabelKeys(labelKeys, labelNames, opts, plan, issue.Key, issue.SourceRow)

		assigneeID := assigneeFor(issue, users)
		var projectID *string
		switch {
		case opts.ProjectID != "":
			projectID = stringPointer(opts.ProjectID)
		case issue.EpicKey != "" && projectKeys[strings.ToLower(issue.EpicKey)]:
			item.EpicKey = issue.EpicKey
		}

		if issue.ParentKey != "" && !issueKeys[strings.ToLower(issue.ParentKey)] {
			plan.Warnings = append(plan.Warnings, Diagnostic{
				Key: issue.Key, Row: issue.SourceRow,
				Message: fmt.Sprintf(
					"parent %s is not part of this import (filtered out or absent); "+
						"%s will be created as a top-level issue",
					issue.ParentKey, issue.Key,
				),
			})
		}
		if issue.ParentKey != "" && issueKeys[strings.ToLower(issue.ParentKey)] {
			item.ParentKey = issue.ParentKey
			item.Wave = 2
			plan.SubIssueCount++
			// Pulse derives a sub-issue's project from its parent and rejects
			// nothing, but sending a project alongside a parent is misleading
			// because the server overwrites it.
			projectID = nil
			item.EpicKey = ""
		}

		item.Issue = pulseapi.CreateIssueRequest{
			Title:      title,
			Status:     string(resolveStatus(issue)),
			Priority:   string(issue.Priority),
			Type:       string(issue.Type),
			TeamID:     opts.TeamID,
			ProjectID:  projectID,
			AssigneeID: assigneeID,
			DueDate:    issue.DueDate,
		}
		if estimate, note, ok := mapEstimate(opts.EstimateSettings, issue.StoryPoints, issue.OriginalEstimateSeconds); ok {
			item.Issue.TimeEstimate = intPointer(estimate)
			plan.EstimatesSet++
			if note != "" {
				plan.Warnings = append(plan.Warnings, Diagnostic{
					Key: issue.Key, Row: issue.SourceRow, Message: note,
				})
			}
		} else if issue.StoryPoints != nil || issue.OriginalEstimateSeconds != nil {
			plan.EstimatesDropped++
		}

		item.PlateJSON = convertDoc(issue.BodyMarkdown, issue.Key, issue.SourceRow, plan)

		if !opts.SkipComments {
			for _, comment := range issue.Comments {
				text := pulseapi.TruncateForAPI(renderComment(comment), pulseapi.MaxTextBytes)
				if text == "" {
					continue
				}
				item.Comments = append(item.Comments, text)
			}
			plan.CommentCount += len(item.Comments)
		}

		if !opts.SkipRelations {
			for _, relation := range issue.Relations {
				if !issueKeys[strings.ToLower(relation.TargetKey)] {
					plan.Warnings = append(plan.Warnings, Diagnostic{
						Key: issue.Key, Row: issue.SourceRow,
						Message: fmt.Sprintf(
							"%s link to %q was dropped: the target is not part of this import",
							relation.Kind, relation.TargetKey,
						),
					})
					continue
				}
				switch relation.Kind {
				case importers.RelationBlocks:
					item.Blocks = append(item.Blocks, relation.TargetKey)
				case importers.RelationBlockedBy:
					item.BlockedBy = append(item.BlockedBy, relation.TargetKey)
				}
			}
			plan.RelationCount += len(item.Blocks) + len(item.BlockedBy)
		}

		plan.Items = append(plan.Items, item)
	}
}

func assigneeFor(issue importers.Issue, users map[string]UserResolution) *string {
	key := strings.ToLower(strings.TrimSpace(issue.AssigneeID))
	if key == "" {
		return nil
	}
	resolution, ok := users[key]
	if !ok || resolution.PulseUserID == "" {
		return nil
	}
	return stringPointer(resolution.PulseUserID)
}

// checkTitle enforces Pulse's title rules in BYTES, which is how the server
// counts them.
func checkTitle(raw, key string, row int, plan *Plan) (string, bool) {
	title := strings.TrimSpace(raw)
	if len([]rune(title)) < 2 {
		plan.Errors = append(plan.Errors, Diagnostic{
			Key: key, Row: row, Message: "title must contain at least 2 characters",
		})
		return "", false
	}
	if pulseapi.ExceedsAPIBytes(title, pulseapi.MaxTitleBytes) {
		truncated := pulseapi.TruncateForAPI(title, pulseapi.MaxTitleBytes)
		plan.Warnings = append(plan.Warnings, Diagnostic{
			Key: key, Row: row,
			Message: fmt.Sprintf(
				"title was truncated to Pulse's %d-byte limit (%d characters kept)",
				pulseapi.MaxTitleBytes, len([]rune(truncated)),
			),
		})
		return truncated, true
	}
	return title, true
}

func convertDoc(markdown, key string, row int, plan *Plan) []byte {
	if strings.TrimSpace(markdown) == "" {
		return nil
	}
	plate, err := platemd.ToJSON(markdown, nil)
	if err != nil {
		plan.Errors = append(plan.Errors, Diagnostic{
			Key: key, Row: row, Message: "convert main doc: " + err.Error(),
		})
		return nil
	}
	return plate
}

// resolveLabelKeys drops unknown labels and enforces Pulse's ten-label ceiling,
// keeping the most meaningful ones first.
func resolveLabelKeys(
	keys []string,
	labelNames map[string]string,
	opts Options,
	plan *Plan,
	issueKey string,
	row int,
) []string {
	resolved := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		if _, exists := labelNames[key]; !exists {
			continue
		}
		seen[key] = true
		resolved = append(resolved, key)
	}
	if len(resolved) <= 10 {
		return resolved
	}
	if opts.LabelPolicy == LabelPolicyError {
		plan.Errors = append(plan.Errors, Diagnostic{
			Key: issueKey, Row: row,
			Message: fmt.Sprintf("issue has %d labels; Pulse allows at most 10", len(resolved)),
		})
		return resolved
	}
	dropped := resolved[10:]
	plan.DroppedLabels += len(dropped)
	plan.Warnings = append(plan.Warnings, Diagnostic{
		Key: issueKey, Row: row,
		Message: fmt.Sprintf(
			"issue has %d labels; Pulse allows 10, so %s were dropped (use --strict-labels to fail instead)",
			len(resolved), strings.Join(dropped, ", "),
		),
	})
	return resolved[:10]
}

func (r *Runner) prepareLabels(
	ctx context.Context,
	labels map[string]importers.Label,
	plan *Plan,
) ([]LabelPlan, map[string]string, error) {
	if len(labels) == 0 {
		return nil, map[string]string{}, nil
	}
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

	// Pulse's label uniqueness index ignores the archived flag, so an archived
	// label with the same name makes a create fail with a 409 that used to abort
	// the whole import. Find those up front and plan to unarchive instead.
	archivedByName := map[string]string{}
	if archived, err := r.API.ListArchivedLabels(ctx, plan.Options.TeamID); err == nil {
		for _, label := range archived {
			if label.EntityType != "" && label.EntityType != "issue" {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(label.Name))
			if _, live := existingByName[key]; live {
				continue
			}
			archivedByName[key] = label.ID
		}
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
			plan.Errors = append(plan.Errors, Diagnostic{
				Message: fmt.Sprintf("label %q is empty", key),
			})
			continue
		}
		folded := strings.ToLower(name)
		if owner, exists := normalizedOwners[folded]; exists && owner != key {
			plan.Warnings = append(plan.Warnings, Diagnostic{Message: fmt.Sprintf(
				"labels %q and %q both become the Pulse label %q and will share it", owner, key, name,
			)})
			names[key] = names[owner]
			continue
		}
		normalizedOwners[folded] = key
		names[key] = name
		if changed {
			plan.Warnings = append(plan.Warnings, Diagnostic{Message: fmt.Sprintf(
				"label %q will be imported as %q (Pulse limit is %d bytes)",
				original, name, pulseapi.MaxLabelBytes,
			)})
		}

		labelPlan := LabelPlan{Key: key, Name: name}
		switch {
		case existingByName[folded] != "":
			labelPlan.ExistingID = existingByName[folded]
		case archivedByName[folded] != "":
			labelPlan.ArchivedID = archivedByName[folded]
			plan.Warnings = append(plan.Warnings, Diagnostic{Message: fmt.Sprintf(
				"label %q exists on this team but is archived; it will be unarchived and reused", name,
			)})
		default:
			labelPlan.Create = true
		}
		out = append(out, labelPlan)
	}
	return out, names, nil
}

// sortItems orders creation: projects first, then top-level issues, then
// sub-issues, and within a wave by ascending source key. Pulse allocates issue
// codes sequentially per team, so importing in source-key order is what lets the
// identifiers line up with Jira in a fresh team.
func sortItems(items []PreparedItem) {
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Wave != items[b].Wave {
			return items[a].Wave < items[b].Wave
		}
		aPrefix, aNumber := splitSourceKey(items[a].Key)
		bPrefix, bNumber := splitSourceKey(items[b].Key)
		if aPrefix != bPrefix {
			return aPrefix < bPrefix
		}
		if aNumber != bNumber {
			return aNumber < bNumber
		}
		return items[a].Key < items[b].Key
	})
}

// splitSourceKey splits "ENG-12" into ("eng", 12) so keys sort numerically
// rather than lexically (ENG-2 before ENG-10).
func splitSourceKey(key string) (string, int) {
	prefix, number, found := strings.Cut(key, "-")
	if !found {
		return strings.ToLower(key), 0
	}
	value := 0
	for _, r := range number {
		if r < '0' || r > '9' {
			return strings.ToLower(key), 0
		}
		value = value*10 + int(r-'0')
	}
	return strings.ToLower(prefix), value
}

func hashPlan(plan *Plan, labelNames map[string]string) string {
	type itemHash struct {
		Key       string
		Kind      importstate.Kind
		RowHash   string
		Issue     pulseapi.CreateIssueRequest
		Project   pulseapi.CreateProjectRequest
		LabelKeys []string
		ParentKey string
		EpicKey   string
		Comments  int
		HasDoc    bool
	}
	hashOptions := plan.Options
	if hashOptions.Assignee != AssigneeSelf {
		hashOptions.SelfUserID = ""
	}
	hashOptions.Concurrency = 0
	hashOptions.Now = time.Time{}
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
			Key: item.Key, Kind: item.Kind, RowHash: item.RowHash,
			Issue: item.Issue, Project: item.Project, LabelKeys: item.LabelKeys,
			ParentKey: item.ParentKey, EpicKey: item.EpicKey,
			Comments: len(item.Comments), HasDoc: len(item.PlateJSON) > 0,
		})
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
