package importstate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/importstate"
)

func identity() importstate.Identity {
	return importstate.Identity{
		Importer: "jira-csv", SourceURL: "https://acme.atlassian.net",
		SourceFingerprint: "source", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace", TeamID: "team", PlanHash: "plan",
	}
}

func TestJournalReplayAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "row", Status: importstate.StatusCreated, IssueID: "issue",
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}

	reopened, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	item, ok := reopened.Item("eng-1")
	if !ok || item.IssueID != "issue" || item.Status != importstate.StatusCreated {
		t.Fatalf("item=%+v ok=%v", item, ok)
	}
}

func TestCreatingReplaysAsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "row", Status: importstate.StatusCreating,
	}); err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	reopened, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	item, _ := reopened.Item("ENG-1")
	if item.EffectiveStatus() != importstate.StatusUnknown {
		t.Fatalf("effective status = %s", item.EffectiveStatus())
	}
}

func TestJournalRepairsIncompleteLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"version":1,"type":"item","key":"BROKEN"`)
	_ = file.Close()

	reopened, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Mark(importstate.Item{
		Key: "ENG-2", RowHash: "row-2", Status: importstate.StatusFailed,
	}); err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "BROKEN") {
		t.Fatalf("incomplete tail was not removed: %s", data)
	}
}

func TestJournalRejectsMismatchAndChangedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "row", Status: importstate.StatusFailed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "changed", Status: importstate.StatusFailed,
	}); err == nil {
		t.Fatal("expected changed-row error")
	}
	_ = journal.Close()

	other := identity()
	other.TeamID = "other"
	if _, err := importstate.Open(path, other); err == nil {
		t.Fatal("expected identity mismatch")
	}
}

func TestJournalRejectsCorruptMiddleLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{broken}\n")
	_, _ = file.WriteString(`{"version":1,"type":"item","time":"2026-01-01T00:00:00Z","key":"ENG-1","row_hash":"row","status":"failed"}` + "\n")
	_ = file.Close()
	if _, err := importstate.Open(path, identity()); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err=%v", err)
	}
}

func TestJournalValidationBranches(t *testing.T) {
	if _, err := importstate.Open("", identity()); err == nil {
		t.Fatal("expected empty path error")
	}
	if _, err := importstate.Open(t.TempDir(), identity()); err == nil {
		t.Fatal("expected directory read/open error")
	}

	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Path() != path {
		t.Fatalf("path=%q", journal.Path())
	}
	if err := journal.Mark(importstate.Item{Key: "", RowHash: "row"}); err == nil {
		t.Fatal("expected missing key error")
	}
	if err := journal.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "row", Status: "surprise",
	}); err == nil || !strings.Contains(err.Error(), "invalid state status") {
		t.Fatalf("invalid status err=%v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "row", Status: importstate.StatusFailed,
	}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed mark err=%v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "no header",
			content: "\n",
			want:    "no header",
		},
		{
			name:    "item before header",
			content: `{"version":1,"type":"item","time":"2026-01-01T00:00:00Z","key":"ENG-1","row_hash":"row","status":"failed"}` + "\n",
			want:    "invalid state item",
		},
		{
			name:    "unsupported version",
			content: strings.Replace(string(valid), `"version":1`, `"version":2`, 1),
			want:    "unsupported state version",
		},
		{
			name:    "unknown event",
			content: string(valid) + `{"version":1,"type":"mystery","time":"2026-01-01T00:00:00Z"}` + "\n",
			want:    "unknown state event",
		},
		{
			name:    "invalid item",
			content: string(valid) + `{"version":1,"type":"item","time":"2026-01-01T00:00:00Z"}` + "\n",
			want:    "invalid state item",
		},
		{
			name:    "invalid status",
			content: string(valid) + `{"version":1,"type":"item","time":"2026-01-01T00:00:00Z","key":"ENG-1","row_hash":"row","status":"surprise"}` + "\n",
			want:    "invalid state item",
		},
		{
			name:    "duplicate header",
			content: string(valid) + string(valid),
			want:    "invalid state header",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			badPath := filepath.Join(t.TempDir(), "state.jsonl")
			if err := os.WriteFile(badPath, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := importstate.Open(badPath, identity()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestJournalAppendsAfterValidTailWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.TrimSuffix(string(data), "\n"))
	// #nosec G703 -- path is created beneath t.TempDir and is test-controlled.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	longMessage := strings.Repeat("x", 700)
	if err := reopened.Mark(importstate.Item{
		Key: "ENG-1", RowHash: "row", Status: importstate.StatusFailed, Message: longMessage,
	}); err != nil {
		t.Fatal(err)
	}
	item, _ := reopened.Item("ENG-1")
	if len([]rune(item.Message)) != 500 || item.EffectiveStatus() != importstate.StatusFailed {
		t.Fatalf("item=%+v", item)
	}
	_ = reopened.Close()
	final, _ := os.ReadFile(path)
	if !strings.Contains(string(final), "\n{\"version\":1,\"type\":\"item\"") {
		t.Fatalf("events were not separated: %s", final)
	}
}
