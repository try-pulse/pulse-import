package importstate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Version 2 records per-item progress through the create → doc → comments →
// links ladder and drops the plan hash from the journal identity.
const Version = 2

type Status string

const (
	StatusCreating       Status = "creating"
	StatusCreated        Status = "created"
	StatusDocUploaded    Status = "doc_uploaded"
	StatusDocUnknown     Status = "doc_unknown"
	StatusCommented      Status = "commented"
	StatusCommentUnknown Status = "comment_unknown"
	StatusLinked         Status = "linked"
	StatusFailed         Status = "failed"
	StatusUnknown        Status = "unknown"
)

// Kind distinguishes the two entity families an import creates.
type Kind string

const (
	KindIssue   Kind = "issue"
	KindProject Kind = "project"
)

// Identity is what makes a state file belong to one import. It deliberately
// excludes anything derived from the current Pulse workspace contents — user
// and label lookups change between runs, and folding them in here made a
// re-run (the documented way to pick up users who joined since the first
// attempt) fail with "different import plan" instead of resuming.
type Identity struct {
	Importer          string `json:"importer"`
	SourceURL         string `json:"source_url"`
	SourceFingerprint string `json:"source_fingerprint"`
	APIURL            string `json:"api_url"`
	WorkspaceID       string `json:"workspace_id"`
	TeamID            string `json:"team_id"`
	ProjectID         string `json:"project_id,omitempty"`
}

type Item struct {
	Key       string `json:"key"`
	Kind      Kind   `json:"kind,omitempty"`
	RowHash   string `json:"row_hash"`
	Status    Status `json:"status"`
	IssueID   string `json:"issue_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	MainDocID string `json:"main_doc_id,omitempty"`
	// Comments is how many of the row's comments have been posted.
	Comments int    `json:"comments,omitempty"`
	Message  string `json:"message,omitempty"`
}

// EffectiveStatus resolves an interrupted write. A `creating` record means the
// process died between "about to POST" and "got a response", so the outcome is
// unknown and must not be retried automatically.
func (i Item) EffectiveStatus() Status {
	if i.Status == StatusCreating {
		return StatusUnknown
	}
	return i.Status
}

// EntityID is the id of whatever the item created.
func (i Item) EntityID() string {
	if i.Kind == KindProject {
		return i.ProjectID
	}
	return i.IssueID
}

// phaseOrder ranks the happy-path statuses so progress can only move forward.
var phaseOrder = map[Status]int{
	StatusCreating:    0,
	StatusCreated:     1,
	StatusDocUploaded: 2,
	StatusCommented:   3,
	StatusLinked:      4,
}

// Phase returns how far an item has progressed, or -1 for a status that is not
// on the happy path (failed, unknown, and the two ambiguous variants).
func Phase(status Status) int {
	if rank, ok := phaseOrder[status]; ok {
		return rank
	}
	return -1
}

// Complete reports whether nothing further is owed for this item.
func (i Item) Complete() bool {
	return i.Status == StatusLinked
}

type event struct {
	Version  int       `json:"version"`
	Type     string    `json:"type"`
	Time     time.Time `json:"time"`
	Identity *Identity `json:"identity,omitempty"`
	Item
}

// Journal is an append-only record of what an import has already done. Every
// method is safe for concurrent use: the executor writes from several workers.
type Journal struct {
	mu           sync.Mutex
	path         string
	file         *os.File
	identity     Identity
	items        map[string]Item
	order        []string
	needsNewline bool
}

func Open(path string, identity Identity) (*Journal, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("state file path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	j := &Journal{path: path, identity: identity, items: map[string]Item{}}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if repaired, ok := repairIncompleteTail(data); ok {
			if err := os.Truncate(path, int64(len(repaired))); err != nil {
				return nil, fmt.Errorf("repair incomplete state tail: %w", err)
			}
			data = repaired
		}
		if err := j.replay(data); err != nil {
			return nil, err
		}
		if j.identity != identity {
			return nil, fmt.Errorf(
				"state file %s belongs to a different import: it was written for "+
					"importer=%q source=%q team=%q project=%q, but this run targets "+
					"importer=%q source=%q team=%q project=%q. Point --state-file at a new "+
					"path, or re-run with the original source and target to resume",
				path,
				j.identity.Importer, j.identity.SourceFingerprint, j.identity.TeamID, j.identity.ProjectID,
				identity.Importer, identity.SourceFingerprint, identity.TeamID, identity.ProjectID,
			)
		}
	case os.IsNotExist(err):
	default:
		return nil, fmt.Errorf("read state file: %w", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state file: %w", err)
	}
	j.file = file
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure state file: %w", err)
	}
	if len(data) == 0 {
		if err := j.append(event{
			Version:  Version,
			Type:     "header",
			Time:     time.Now().UTC(),
			Identity: &identity,
		}); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return j, nil
}

func repairIncompleteTail(data []byte) ([]byte, bool) {
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data, false
	}
	start := bytes.LastIndexByte(data, '\n') + 1
	var ev event
	if json.Unmarshal(bytes.TrimSpace(data[start:]), &ev) == nil {
		return data, false
	}
	return data[:start], true
}

func (j *Journal) replay(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	hasTrailingNewline := data[len(data)-1] == '\n'
	reader := bufio.NewReader(bytes.NewReader(data))
	lineNumber := 0
	headerSeen := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 && readErr == io.EOF {
			break
		}
		lineNumber++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			if readErr == io.EOF && !hasTrailingNewline {
				j.needsNewline = true
				break
			}
			return fmt.Errorf("state file is corrupt at line %d: %w", lineNumber, err)
		}
		if ev.Version != Version {
			return fmt.Errorf(
				"state file %s was written by pulse-import with state version %d; this build writes "+
					"version %d. Finish that import with the older binary, or delete the state file "+
					"and re-import into an empty team",
				j.path, ev.Version, Version,
			)
		}
		switch ev.Type {
		case "header":
			if headerSeen || lineNumber != 1 || ev.Identity == nil {
				return fmt.Errorf("invalid state header at line %d", lineNumber)
			}
			headerSeen = true
			j.identity = *ev.Identity
		case "item":
			if !headerSeen || ev.Key == "" || ev.RowHash == "" || !validStatus(ev.Status) {
				return fmt.Errorf("invalid state item at line %d", lineNumber)
			}
			j.put(ev.Item)
		default:
			return fmt.Errorf("unknown state event %q at line %d", ev.Type, lineNumber)
		}
		if readErr == io.EOF {
			j.needsNewline = !hasTrailingNewline
			break
		}
	}
	if !headerSeen {
		return fmt.Errorf("state file has no header")
	}
	return nil
}

func (j *Journal) put(item Item) {
	folded := strings.ToLower(item.Key)
	if _, exists := j.items[folded]; !exists {
		j.order = append(j.order, folded)
	}
	j.items[folded] = item
}

func (j *Journal) Item(key string) (Item, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[strings.ToLower(strings.TrimSpace(key))]
	return item, ok
}

// Items returns every recorded item in the order it was first written, which is
// creation order — the order rollback must undo.
func (j *Journal) Items() []Item {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Item, 0, len(j.order))
	for _, key := range j.order {
		out = append(out, j.items[key])
	}
	return out
}

// Identity returns the target this state file was opened for.
func (j *Journal) Identity() Identity {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.identity
}

func (j *Journal) Mark(item Item) error {
	if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.RowHash) == "" {
		return fmt.Errorf("state item key and row hash are required")
	}
	if !validStatus(item.Status) {
		return fmt.Errorf("invalid state status %q", item.Status)
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	folded := strings.ToLower(item.Key)
	if previous, exists := j.items[folded]; exists {
		if previous.RowHash != item.RowHash {
			return fmt.Errorf("source row for %s changed since the state file was created", item.Key)
		}
		// Ids are only ever learned, never unlearned: a later phase that does
		// not carry them must not erase what an earlier phase recorded.
		if item.IssueID == "" {
			item.IssueID = previous.IssueID
		}
		if item.ProjectID == "" {
			item.ProjectID = previous.ProjectID
		}
		if item.MainDocID == "" {
			item.MainDocID = previous.MainDocID
		}
		if item.Kind == "" {
			item.Kind = previous.Kind
		}
		if item.Comments < previous.Comments {
			item.Comments = previous.Comments
		}
	}
	if item.Kind == "" {
		item.Kind = KindIssue
	}
	item.Message = truncate(item.Message, 500)

	ev := event{Version: Version, Type: "item", Time: time.Now().UTC(), Item: item}
	if err := j.append(ev); err != nil {
		return err
	}
	j.put(item)
	return nil
}

// append must be called with the mutex held, except from Open before the
// journal is shared.
func (j *Journal) append(ev event) error {
	if j.file == nil {
		return fmt.Errorf("state journal is closed")
	}
	if j.needsNewline {
		if _, err := j.file.Write([]byte("\n")); err != nil {
			return fmt.Errorf("repair state tail: %w", err)
		}
		j.needsNewline = false
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := j.file.Write(data); err != nil {
		return fmt.Errorf("append state: %w", err)
	}
	// fsync on every record is the point: the journal has to survive the crash
	// that happens between the POST and its response.
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync state: %w", err)
	}
	return nil
}

func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

func (j *Journal) Path() string {
	return j.path
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func validStatus(status Status) bool {
	switch status {
	case StatusCreating, StatusCreated, StatusDocUploaded, StatusDocUnknown,
		StatusCommented, StatusCommentUnknown, StatusLinked, StatusFailed, StatusUnknown:
		return true
	default:
		return false
	}
}

// ReadIdentity extracts the header identity from raw journal bytes without
// binding the journal to a target. Rollback needs this: the state file is the
// authority on where an import went, so it must be read before it can be
// validated against anything.
func ReadIdentity(data []byte) (Identity, error) {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Identity{}, fmt.Errorf("state file is empty")
	}
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return Identity{}, fmt.Errorf("state file header is not valid JSON: %w", err)
	}
	if ev.Type != "header" || ev.Identity == nil {
		return Identity{}, fmt.Errorf("state file has no header record")
	}
	if ev.Version != Version {
		return Identity{}, fmt.Errorf(
			"state version %d is not supported by this build (expected %d)", ev.Version, Version)
	}
	return *ev.Identity, nil
}
