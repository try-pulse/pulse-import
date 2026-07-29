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
	"time"
)

const Version = 1

type Status string

const (
	StatusCreating    Status = "creating"
	StatusCreated     Status = "created"
	StatusDocUploaded Status = "doc_uploaded"
	StatusDocUnknown  Status = "doc_unknown"
	StatusFailed      Status = "failed"
	StatusUnknown     Status = "unknown"
)

type Identity struct {
	Importer          string `json:"importer"`
	SourceURL         string `json:"source_url"`
	SourceFingerprint string `json:"source_fingerprint"`
	APIURL            string `json:"api_url"`
	WorkspaceID       string `json:"workspace_id"`
	TeamID            string `json:"team_id"`
	ProjectID         string `json:"project_id,omitempty"`
	AssigneeMode      string `json:"assignee_mode"`
	PlanHash          string `json:"plan_hash"`
}

type Item struct {
	Key       string `json:"key"`
	RowHash   string `json:"row_hash"`
	Status    Status `json:"status"`
	IssueID   string `json:"issue_id,omitempty"`
	MainDocID string `json:"main_doc_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (i Item) EffectiveStatus() Status {
	if i.Status == StatusCreating {
		return StatusUnknown
	}
	return i.Status
}

type event struct {
	Version  int       `json:"version"`
	Type     string    `json:"type"`
	Time     time.Time `json:"time"`
	Identity *Identity `json:"identity,omitempty"`
	Item
}

type Journal struct {
	path         string
	file         *os.File
	identity     Identity
	items        map[string]Item
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
			return nil, fmt.Errorf("state file belongs to a different import plan or target")
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
			return fmt.Errorf("unsupported state version %d at line %d", ev.Version, lineNumber)
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
			j.items[strings.ToLower(ev.Key)] = ev.Item
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

func (j *Journal) Item(key string) (Item, bool) {
	item, ok := j.items[strings.ToLower(strings.TrimSpace(key))]
	return item, ok
}

func (j *Journal) Mark(item Item) error {
	if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.RowHash) == "" {
		return fmt.Errorf("state item key and row hash are required")
	}
	if !validStatus(item.Status) {
		return fmt.Errorf("invalid state status %q", item.Status)
	}
	if previous, exists := j.Item(item.Key); exists && previous.RowHash != item.RowHash {
		return fmt.Errorf("source row for %s changed since the state file was created", item.Key)
	}
	item.Message = truncate(item.Message, 500)
	ev := event{Version: Version, Type: "item", Time: time.Now().UTC(), Item: item}
	if err := j.append(ev); err != nil {
		return err
	}
	j.items[strings.ToLower(item.Key)] = item
	return nil
}

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
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync state: %w", err)
	}
	return nil
}

func (j *Journal) Close() error {
	if j == nil || j.file == nil {
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
	case StatusCreating, StatusCreated, StatusDocUploaded, StatusDocUnknown, StatusFailed, StatusUnknown:
		return true
	default:
		return false
	}
}
