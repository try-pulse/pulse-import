package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

type AssigneeMode string

const (
	AssigneeSelf   AssigneeMode = "self"
	AssigneeMapped AssigneeMode = "mapped"
	AssigneeNone   AssigneeMode = "none"
)

type userIndex struct {
	byName  map[string][]string
	byEmail map[string][]string
}

func indexUsers(users []pulseapi.User) userIndex {
	index := userIndex{byName: map[string][]string{}, byEmail: map[string][]string{}}
	for _, user := range users {
		addUnique(index.byName, user.DisplayName, user.ID)
		addUnique(index.byName, strings.TrimSpace(user.FirstName+" "+user.LastName), user.ID)
		addUnique(index.byEmail, user.Email, user.ID)
	}
	return index
}

func addUnique(index map[string][]string, key, id string) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" || id == "" {
		return
	}
	for _, existing := range index[key] {
		if existing == id {
			return
		}
	}
	index[key] = append(index[key], id)
}

func resolveAssignee(
	mode AssigneeMode,
	selfUserID string,
	issue importers.Issue,
	sourceUsers map[string]importers.User,
	users userIndex,
) (id *string, state string) {
	switch mode {
	case AssigneeSelf:
		if selfUserID == "" {
			return nil, "unmatched"
		}
		return stringPointer(selfUserID), "matched"
	case AssigneeNone:
		return nil, "none"
	case AssigneeMapped:
	default:
		return nil, "none"
	}
	sourceKey := strings.ToLower(strings.TrimSpace(issue.AssigneeID))
	if sourceKey == "" {
		return nil, "none"
	}
	sourceUser := sourceUsers[sourceKey]
	email := strings.TrimSpace(issue.AssigneeEmail)
	if email == "" {
		email = strings.TrimSpace(sourceUser.Email)
	}
	if email == "" && strings.Contains(sourceKey, "@") {
		email = sourceKey
	}
	if email != "" {
		switch matches := users.byEmail[strings.ToLower(email)]; len(matches) {
		case 1:
			return stringPointer(matches[0]), "matched"
		case 0:
		default:
			return nil, "ambiguous"
		}
	}
	name := strings.TrimSpace(sourceUser.Name)
	if name == "" {
		name = issue.AssigneeID
	}
	switch matches := users.byName[strings.ToLower(name)]; len(matches) {
	case 1:
		return stringPointer(matches[0]), "matched"
	case 0:
		return nil, "unmatched"
	default:
		return nil, "ambiguous"
	}
}

func stringPointer(value string) *string {
	return &value
}

func normalizeLabelName(name string) (normalized string, changed bool) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) <= 50 {
		return name, false
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:4])
	return string([]rune(name)[:50-len([]rune(suffix))]) + suffix, true
}

func truncateRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max])
}

func pickColor(name string) string {
	palette := []string{
		"#EB5757", "#F2C94C", "#27AE60", "#2D9CDB", "#9B51E0",
		"#F2994A", "#56CCF2", "#6FCF97", "#BB6BD9", "#828282",
	}
	hash := 0
	for _, r := range name {
		hash = (hash*31 + int(r)) % len(palette)
	}
	return palette[hash]
}
