package jiracsv

import (
	"fmt"
	"sort"
	"strings"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/jira2md"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

// parseRow extracts every mapped field from one CSV record.
func (i *Importer) parseRow(
	rw row,
	rowNumber int,
	rowHash, key, summary string,
	result *importers.ImportResult,
) *parsed {
	p := &parsed{
		rowNumber: rowNumber,
		rowHash:   rowHash,
		key:       key,
		issueID:   strings.TrimSpace(rw.first("issue id")),
		summary:   summary,
		doc:       &docBuilder{Key: key, BrowseURL: i.browseURL(key)},
	}

	p.issueType = strings.TrimSpace(rw.first("issue type"))
	p.isEpic = statusmap.IsEpicType(p.issueType)
	p.isSubTask = statusmap.IsSubTaskType(p.issueType)
	p.pulseType, p.typeLabel = statusmap.MapIssueType(p.issueType)

	p.parentRef = strings.TrimSpace(rw.first("parent"))
	p.parentIDRef = strings.TrimSpace(rw.first("parent id"))
	p.epicLinkRef = strings.TrimSpace(rw.custom("Epic Link"))

	p.status = strings.TrimSpace(rw.first("status"))
	resolution := strings.TrimSpace(rw.first("resolution"))
	mapped, overridden := statusmap.MapWithResolution(p.status, resolution)
	p.resolvedStatus, p.statusOverridden = string(mapped), overridden
	if p.status != "" {
		result.StatusNames[string(mapped)] = appendUnique(result.StatusNames[string(mapped)], p.status)
	}

	p.priority = statusmap.MapPriority(rw.first("priority"))

	// Attachments are resolved before the description so `!file.png!` macros in
	// the body can be turned into links to the original files.
	attachments := map[string]string{}
	for _, cell := range rw.all("attachment") {
		name, url := jiraAttachment(cell)
		if name == "" {
			continue
		}
		p.doc.Attachments = append(p.doc.Attachments, attachment{Name: name, URL: url})
		if url != "" {
			attachments[strings.ToLower(name)] = url
		}
	}
	p.body = jira2md.ConvertWithAttachments(rw.first("description"), attachments)

	i.parseAssignee(rw, p, result)
	i.parseLabels(rw, p)
	i.parseDates(rw, p)
	i.parseEstimates(rw, p)
	i.parseRelations(rw, p)
	if !i.opts.SkipComments {
		i.parseComments(rw, p)
	}
	i.parseProvenance(rw, p, resolution)
	return p
}

func (i *Importer) parseAssignee(rw row, p *parsed, result *importers.ImportResult) {
	assignee := strings.TrimSpace(rw.first("assignee"))
	if strings.EqualFold(assignee, "unassigned") {
		assignee = ""
	}
	if assignee == "" {
		return
	}
	p.assignee = assignee
	p.assigneeEmail = strings.TrimSpace(rw.first("assignee email", "assignee email address"))

	folded := strings.ToLower(assignee)
	existing := result.Users[folded]
	existing.Name = assignee
	existing.Rows++
	if existing.Email == "" {
		existing.Email = p.assigneeEmail
	}
	result.Users[folded] = existing
}

func (i *Importer) parseLabels(rw row, p *parsed) {
	add := func(name string, kind importers.LabelKind) {
		if name = strings.TrimSpace(name); name != "" {
			p.labels = append(p.labels, labelRef{name: name, kind: kind})
		}
	}

	add(p.typeLabel, importers.LabelKindType)
	for _, value := range rw.all("labels") {
		add(value, importers.LabelKindJira)
	}
	for _, value := range rw.all("component/s", "components") {
		add("Component: "+value, importers.LabelKindComponent)
	}
	// Fix versions describe where the work lands; affects versions describe
	// where the bug was seen. They are different facts and must not share a
	// label namespace.
	for _, value := range rw.all("fix version/s", "release") {
		add("Release: "+value, importers.LabelKindFixVersion)
	}
	for _, value := range rw.all("affects version/s") {
		add("Affects: "+value, importers.LabelKindAffectsVersion)
	}
	for _, value := range rw.all("sprint") {
		add("Sprint: "+value, importers.LabelKindSprint)
	}
}

func (i *Importer) parseDates(rw row, p *parsed) {
	if due, ok := parseJiraTime(rw.first("due date")); ok {
		p.dueDate = &due
	}
	if updated, ok := parseJiraTime(rw.first("updated")); ok {
		p.updatedAt = &updated
	}
}

func (i *Importer) parseEstimates(rw row, p *parsed) {
	if points, ok := parseNumber(rw.custom("Story Points", "Story point estimate")); ok && points >= 0 {
		p.points = &points
	}
	if seconds, ok := parseSeconds(rw.first("original estimate")); ok && seconds > 0 {
		p.estimateS = &seconds
	}
}

// jiraLinkColumns maps Jira's issue-link header suffixes onto Pulse relations.
// Only the blocks family has a Pulse equivalent; other link types (relates to,
// duplicates, clones) have no field to land in and are reported as ignored.
func (i *Importer) parseRelations(rw row, p *parsed) {
	for _, value := range rw.all("outward issue link (blocks)") {
		p.blocks = appendUnique(p.blocks, value)
	}
	for _, value := range rw.all("inward issue link (blocks)") {
		p.blockedBy = appendUnique(p.blockedBy, value)
	}
}

func (i *Importer) parseComments(rw row, p *parsed) {
	for _, cell := range rw.all("comment") {
		author, created, body := jiraComment(cell)
		if strings.TrimSpace(body) == "" {
			continue
		}
		p.comments = append(p.comments, importers.Comment{
			Author:  author,
			Created: created,
			Body:    jira2md.Convert(body),
		})
	}
	// Jira exports comments oldest-first per column order; keep that so the
	// Pulse thread reads chronologically even when a timestamp is missing.
	sort.SliceStable(p.comments, func(a, b int) bool {
		if p.comments[a].Created.IsZero() || p.comments[b].Created.IsZero() {
			return false
		}
		return p.comments[a].Created.Before(p.comments[b].Created)
	})
}

// parseProvenance records the Jira fields Pulse has nowhere to put.
func (i *Importer) parseProvenance(rw row, p *parsed, resolution string) {
	p.doc.add("Issue type", p.issueType)
	p.doc.add("Status", p.status)
	p.doc.add("Resolution", resolution)
	p.doc.add("Reporter", rw.first("reporter"))
	p.doc.add("Creator", rw.first("creator"))
	if created, ok := parseJiraTime(rw.first("created")); ok {
		p.doc.addTime("Created", created, true)
	}
	if updated, ok := parseJiraTime(rw.first("updated")); ok {
		p.doc.addTime("Updated", updated, true)
	}
	if resolved, ok := parseJiraTime(rw.first("resolved")); ok {
		p.doc.addTime("Resolved", resolved, true)
	}
	p.doc.addList("Sprint", rw.all("sprint"))
	p.doc.add("Environment", jira2md.Convert(rw.first("environment")))
	if p.points != nil {
		p.doc.add("Story points", formatPoints(*p.points))
	}
	if p.estimateS != nil {
		p.doc.add("Original estimate", formatSeconds(*p.estimateS))
	}
	if spent, ok := parseSeconds(rw.first("time spent")); ok {
		p.doc.add("Time spent", formatSeconds(spent))
	}
}

// link resolves Jira's hierarchy across the whole file: which rows are epics,
// which parent reference points at an epic (a project in Pulse) versus a story
// (a sub-issue), and flattens nesting Pulse does not support.
func (i *Importer) link(rows []*parsed, result *importers.ImportResult) {
	byKey := make(map[string]*parsed, len(rows))
	byIssueID := make(map[string]*parsed, len(rows))
	for _, r := range rows {
		if r.key != "" {
			byKey[strings.ToLower(r.key)] = r
		}
		if r.issueID != "" {
			byIssueID[r.issueID] = r
		}
	}
	lookup := func(ref string) *parsed {
		if ref = strings.TrimSpace(ref); ref == "" {
			return nil
		}
		if found, ok := byKey[strings.ToLower(ref)]; ok {
			return found
		}
		return byIssueID[ref]
	}

	epicsAsProjects := i.opts.Epics == EpicModeProject

	for _, r := range rows {
		// Jira's Parent column points at the epic for a story and at the story
		// for a sub-task, so the referenced row's type decides its meaning.
		var parentRow *parsed
		for _, ref := range []string{r.parentRef, r.parentIDRef} {
			if parentRow = lookup(ref); parentRow != nil {
				break
			}
		}
		epicRow := lookup(r.epicLinkRef)
		if epicRow == nil && parentRow != nil && parentRow.isEpic {
			epicRow, parentRow = parentRow, nil
		}

		switch {
		case epicRow != nil:
			r.resolvedEpicKey = epicRow.key
			if !epicsAsProjects {
				r.labels = append(r.labels, labelRef{
					name: "Epic: " + epicRow.summary, kind: importers.LabelKindEpic,
				})
			}
		case r.epicLinkRef != "":
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticWarning, Row: r.rowNumber,
				Message: fmt.Sprintf(
					"epic %q is not in this export; %s will be imported without a project",
					r.epicLinkRef, r.key,
				),
			})
		}

		if parentRow == nil {
			if r.isSubTask && (r.parentRef != "" || r.parentIDRef != "") {
				result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
					Level: importers.DiagnosticWarning, Row: r.rowNumber,
					Message: fmt.Sprintf(
						"parent %q is not in this export; %s will be imported as a top-level issue",
						firstNonEmpty(r.parentRef, r.parentIDRef), r.key,
					),
				})
			}
			continue
		}

		// Pulse supports exactly one level of nesting, so a sub-task of a
		// sub-task is re-parented onto the highest non-sub-task ancestor.
		ancestor, hops := topLevelAncestor(parentRow, lookup)
		if ancestor == nil {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticWarning, Row: r.rowNumber,
				Message: fmt.Sprintf(
					"could not resolve a top-level parent for %s; importing it as a top-level issue", r.key,
				),
			})
			continue
		}
		if hops > 0 {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticWarning, Row: r.rowNumber,
				Message: fmt.Sprintf(
					"Pulse supports one level of sub-issues; %s was re-parented from %s to %s",
					r.key, parentRow.key, ancestor.key,
				),
			})
		}
		if ancestor.key == r.key {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticWarning, Row: r.rowNumber,
				Message: fmt.Sprintf("%s references itself as its parent; the link was dropped", r.key),
			})
			continue
		}
		if ancestor.isEpic {
			r.resolvedEpicKey = ancestor.key
			continue
		}
		r.resolvedParentKey = ancestor.key
	}
}

// topLevelAncestor walks up Jira's parent chain to the first row that is not
// itself a sub-task, guarding against cycles in malformed exports.
func topLevelAncestor(start *parsed, lookup func(string) *parsed) (*parsed, int) {
	seen := map[*parsed]bool{}
	current := start
	hops := 0
	for current != nil && !seen[current] {
		if !current.isSubTask {
			return current, hops
		}
		seen[current] = true
		var next *parsed
		for _, ref := range []string{current.parentRef, current.parentIDRef} {
			if next = lookup(ref); next != nil {
				break
			}
		}
		if next == nil {
			// A sub-task whose own parent is missing is the best anchor we have.
			return current, hops
		}
		current = next
		hops++
	}
	return nil, hops
}

// emit turns resolved rows into the importer's output model.
func (i *Importer) emit(rows []*parsed, result *importers.ImportResult) {
	epicsAsProjects := i.opts.Epics == EpicModeProject

	register := func(refs []labelRef) []string {
		keys := make([]string, 0, len(refs))
		seen := map[string]bool{}
		for _, ref := range refs {
			key := strings.ToLower(ref.name)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
			if existing, ok := result.Labels[key]; !ok || ref.kind < existing.Kind {
				result.Labels[key] = importers.Label{Name: ref.name, Kind: ref.kind}
			}
		}
		return keys
	}

	for _, r := range rows {
		r.doc.Description = r.body
		if epicsAsProjects && r.isEpic {
			result.Projects = append(result.Projects, importers.Project{
				Key:          r.key,
				SourceRow:    r.rowNumber,
				RowHash:      r.rowHash,
				Title:        r.summary,
				BodyMarkdown: r.doc.build(),
				Status:       r.resolvedStatus,
				Labels:       register(r.labels),
			})
			continue
		}

		issue := importers.Issue{
			Key:                     r.key,
			SourceRow:               r.rowNumber,
			RowHash:                 r.rowHash,
			Title:                   r.summary,
			BodyMarkdown:            r.doc.build(),
			Status:                  r.status,
			AssigneeID:              r.assignee,
			AssigneeEmail:           r.assigneeEmail,
			Priority:                r.priority,
			Type:                    r.pulseType,
			Labels:                  register(r.labels),
			ParentKey:               r.resolvedParentKey,
			EpicKey:                 r.resolvedEpicKey,
			DueDate:                 r.dueDate,
			StoryPoints:             r.points,
			OriginalEstimateSeconds: r.estimateS,
			UpdatedAt:               r.updatedAt,
			Comments:                r.comments,
		}
		issue.IsEpic = r.isEpic
		if r.statusOverridden {
			issue.StatusOverride = r.resolvedStatus
		}
		for _, target := range r.blocks {
			issue.Relations = append(issue.Relations, importers.Relation{
				Kind: importers.RelationBlocks, TargetKey: target,
			})
		}
		for _, target := range r.blockedBy {
			issue.Relations = append(issue.Relations, importers.Relation{
				Kind: importers.RelationBlockedBy, TargetKey: target,
			})
		}
		result.Issues = append(result.Issues, issue)
	}
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
