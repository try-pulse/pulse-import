package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/pulseapi"
)

type AssigneeMode string

const (
	AssigneeSelf   AssigneeMode = "self"
	AssigneeMapped AssigneeMode = "mapped"
	AssigneeNone   AssigneeMode = "none"
)

// roster is the set of users Pulse will accept as an assignee for the target
// team: its members plus the members of its ancestor teams. Matching against
// the whole workspace instead would produce assignees the API rejects with
// "Assignee must be a member of the issue's team or one of its parent teams".
type roster struct {
	byID    map[string]pulseapi.TeamMember
	byEmail map[string][]string
	byName  map[string][]string
}

func newRoster(members []pulseapi.TeamMember) roster {
	r := roster{
		byID:    map[string]pulseapi.TeamMember{},
		byEmail: map[string][]string{},
		byName:  map[string][]string{},
	}
	for _, member := range members {
		if member.ID == "" {
			continue
		}
		r.byID[member.ID] = member
		addUnique(r.byEmail, member.Email, member.ID)
		addUnique(r.byName, strings.TrimSpace(member.FirstName+" "+member.LastName), member.ID)
		// Jira often exports only a display name; also index the local part of
		// the email, which is what many Jira instances use as the username.
		if local, _, found := strings.Cut(member.Email, "@"); found {
			addUnique(r.byName, local, member.ID)
		}
	}
	return r
}

func (r roster) has(userID string) bool {
	_, ok := r.byID[userID]
	return ok
}

func (r roster) displayName(userID string) string {
	member, ok := r.byID[userID]
	if !ok {
		return ""
	}
	if name := strings.TrimSpace(member.FirstName + " " + member.LastName); name != "" {
		return name
	}
	return member.Email
}

// findByEmailOrName looks a raw identifier up as an email first, then a name.
func (r roster) findByEmailOrName(value string) (ids []string) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	if matches := r.byEmail[value]; len(matches) > 0 {
		return matches
	}
	return r.byName[value]
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

// resolveUsers decides, once per source user, who they become in Pulse. Doing
// this up front (rather than per issue) is what makes the mapping reviewable
// before a single write.
func resolveUsers(
	mode AssigneeMode,
	selfUserID string,
	sourceUsers map[string]importers.User,
	manual map[string]string,
	people roster,
) map[string]UserResolution {
	out := make(map[string]UserResolution, len(sourceUsers))
	for key, user := range sourceUsers {
		resolution := UserResolution{
			SourceName:  user.Name,
			SourceEmail: user.Email,
			Rows:        user.Rows,
			State:       "unmatched",
		}

		switch mode {
		case AssigneeNone:
			resolution.State = "skipped"
			out[key] = resolution
			continue
		case AssigneeSelf:
			resolution.State = "self"
			resolution.Via = "self"
			resolution.PulseUserID = selfUserID
			resolution.PulseName = people.displayName(selfUserID)
			out[key] = resolution
			continue
		}

		// An explicit mapping always wins, including the skip sentinel.
		if target, ok := manual[key]; ok {
			if strings.EqualFold(target, SkipUser) {
				resolution.State = "skipped"
				resolution.Via = "manual"
				out[key] = resolution
				continue
			}
			if people.has(target) {
				resolution.State, resolution.Via = "matched", "manual"
				resolution.PulseUserID = target
				resolution.PulseName = people.displayName(target)
				out[key] = resolution
				continue
			}
			if matches := people.findByEmailOrName(target); len(matches) == 1 {
				resolution.State, resolution.Via = "matched", "manual"
				resolution.PulseUserID = matches[0]
				resolution.PulseName = people.displayName(matches[0])
				out[key] = resolution
				continue
			}
			resolution.State, resolution.Via = "unmatched", "manual"
			out[key] = resolution
			continue
		}

		// Email is the only identifier Jira and Pulse are guaranteed to agree
		// on, so it is tried first.
		candidates := []string{user.Email, user.Name, key}
		for _, candidate := range candidates {
			matches := people.findByEmailOrName(candidate)
			switch len(matches) {
			case 0:
				continue
			case 1:
				resolution.State = "matched"
				resolution.Via = "name"
				if strings.Contains(candidate, "@") {
					resolution.Via = "email"
				}
				resolution.PulseUserID = matches[0]
				resolution.PulseName = people.displayName(matches[0])
			default:
				resolution.State = "ambiguous"
			}
			break
		}
		out[key] = resolution
	}
	return out
}

// orderedUserResolutions returns the mapping sorted by how much work each user
// owns, so the review table and the interactive prompts lead with the names that
// matter.
func orderedUserResolutions(byKey map[string]UserResolution) []UserResolution {
	out := make([]UserResolution, 0, len(byKey))
	for _, resolution := range byKey {
		out = append(out, resolution)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Rows != out[b].Rows {
			return out[a].Rows > out[b].Rows
		}
		return strings.ToLower(out[a].SourceName) < strings.ToLower(out[b].SourceName)
	})
	return out
}

// normalizeLabelName fits a label into Pulse's 50-BYTE limit. The server counts
// bytes, so a rune-based cut still fails for non-Latin names; the hash suffix
// keeps two long names that share a prefix from colliding.
func normalizeLabelName(name string) (normalized string, changed bool) {
	name = strings.TrimSpace(name)
	if !pulseapi.ExceedsAPIBytes(name, pulseapi.MaxLabelBytes) {
		return name, false
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:4])
	head := pulseapi.TruncateForAPI(name, pulseapi.MaxLabelBytes-len(suffix))
	return head + suffix, true
}

// allowedEstimates mirrors Pulse's per-scale allow-list. Sending a value outside
// it is rejected with INVALID_ESTIMATE, so the mapping has to snap to the scale
// rather than pass Jira's number through.
func allowedEstimates(settings pulseapi.EstimateSettings) []int {
	var base, extras []int
	switch settings.ScaleType {
	case "fibonacci", "tshirt":
		base, extras = []int{1, 2, 3, 5, 8}, []int{13, 21}
	case "exponential":
		base, extras = []int{1, 2, 4, 8, 16}, []int{32, 64}
	default:
		// The hours scale has no fixed set.
		return nil
	}
	values := make([]int, 0, len(base)+len(extras)+1)
	if settings.AllowZero {
		values = append(values, 0)
	}
	values = append(values, base...)
	if settings.ExtendedScale {
		values = append(values, extras...)
	}
	return values
}

// mapEstimate converts a source estimate into a value the team's scale accepts.
// It returns ok=false when there is nothing to send.
func mapEstimate(
	settings pulseapi.EstimateSettings,
	storyPoints *float64,
	originalEstimateSeconds *int,
) (value int, note string, ok bool) {
	if !settings.Enabled {
		return 0, "", false
	}

	if settings.ScaleType == "hours" || settings.ScaleType == "" {
		// An hours-scale team wants Jira's time estimate, not its story points:
		// treating "5 points" as "5 hours" would invent data.
		if originalEstimateSeconds == nil || *originalEstimateSeconds <= 0 {
			return 0, "", false
		}
		hours := int(math.Round(float64(*originalEstimateSeconds) / 3600))
		if hours == 0 {
			if !settings.AllowZero {
				return 0, "", false
			}
			return 0, "", true
		}
		return hours, "", true
	}

	if storyPoints == nil {
		return 0, "", false
	}
	points := *storyPoints
	allowed := allowedEstimates(settings)
	if len(allowed) == 0 {
		return 0, "", false
	}
	if points == 0 {
		if settings.AllowZero {
			return 0, "", true
		}
		return 0, "", false
	}

	best := allowed[0]
	for _, candidate := range allowed {
		if candidate == 0 {
			continue
		}
		if math.Abs(float64(candidate)-points) < math.Abs(float64(best)-points) {
			best = candidate
		}
	}
	if float64(best) != points {
		note = fmt.Sprintf(
			"story points %s snapped to %d for the team's %s scale",
			trimFloat(points), best, settings.ScaleType,
		)
	}
	return best, note, true
}

func trimFloat(value float64) string {
	if value == math.Trunc(value) {
		return fmt.Sprintf("%d", int64(value))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

func stringPointer(value string) *string {
	return &value
}

func intPointer(value int) *int {
	return &value
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

// renderComment turns a source comment into the text Pulse will store. Pulse
// attributes every comment to the token holder and has no field for a historical
// timestamp, so the original author and date are carried in the body — otherwise
// an import would silently reassign every comment to whoever ran it.
func renderComment(comment importers.Comment) string {
	body := strings.TrimSpace(comment.Body)

	var attribution []string
	if author := strings.TrimSpace(comment.Author); author != "" {
		attribution = append(attribution, author)
	}
	if !comment.Created.IsZero() {
		attribution = append(attribution, comment.Created.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if len(attribution) == 0 {
		return body
	}

	header := fmt.Sprintf("**%s** wrote in Jira:", strings.Join(attribution, " · "))
	if body == "" {
		return header
	}
	return header + "\n\n" + body
}
