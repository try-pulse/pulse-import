package runner

import (
	"strings"
	"unicode/utf8"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

type AssigneeMode string

const (
	AssigneeSelf   AssigneeMode = "self"
	AssigneeMapped AssigneeMode = "mapped"
	AssigneeNone   AssigneeMode = "none"
)

type userIndex struct {
	byName  map[string]string
	byEmail map[string]string
}

func indexUsers(users []pulseapi.User) userIndex {
	idx := userIndex{byName: map[string]string{}, byEmail: map[string]string{}}
	for _, u := range users {
		if u.DisplayName != "" {
			idx.byName[strings.ToLower(u.DisplayName)] = u.ID
		}
		full := strings.TrimSpace(u.FirstName + " " + u.LastName)
		if full != "" {
			idx.byName[strings.ToLower(full)] = u.ID
		}
		if u.Email != "" {
			idx.byEmail[strings.ToLower(u.Email)] = u.ID
		}
	}
	return idx
}

type mapping struct {
	teamID       string
	projectID    string
	assignee     AssigneeMode
	selfUserID   string
	users        userIndex
	labelMapping map[string]string
}

func (m mapping) request(issue importers.Issue) pulseapi.CreateIssueRequest {
	title := strings.TrimSpace(issue.Title)
	if title == "" {
		title = "Untitled"
	}
	title = truncateRunes(title, 200)

	var labelIDs []string
	seen := map[string]bool{}
	for _, k := range issue.Labels {
		id := m.labelMapping[k]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		labelIDs = append(labelIDs, id)
		if len(labelIDs) >= 10 {
			break
		}
	}

	var assigneeID *string
	switch m.assignee {
	case AssigneeSelf:
		if m.selfUserID != "" {
			assigneeID = &m.selfUserID
		}
	case AssigneeMapped:
		if issue.AssigneeID != "" {
			key := strings.ToLower(issue.AssigneeID)
			if id, ok := m.users.byEmail[key]; ok {
				assigneeID = &id
			} else if id, ok := m.users.byName[key]; ok {
				assigneeID = &id
			}
		}
	}

	var projectID *string
	if m.projectID != "" {
		projectID = &m.projectID
	}

	return pulseapi.CreateIssueRequest{
		Title:        title,
		Status:       string(statusmap.Map(issue.Status)),
		Priority:     string(issue.Priority),
		Type:         string(issue.Type),
		TeamID:       m.teamID,
		ProjectID:    projectID,
		AssigneeID:   assigneeID,
		LabelIDs:     labelIDs,
		TimeEstimate: issue.Estimate,
	}
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func pickColor(name string) string {
	palette := []string{
		"#EB5757", "#F2C94C", "#27AE60", "#2D9CDB", "#9B51E0",
		"#F2994A", "#56CCF2", "#6FCF97", "#BB6BD9", "#828282",
	}
	h := 0
	for _, r := range name {
		h = (h*31 + int(r)) % len(palette)
	}
	return palette[h]
}
