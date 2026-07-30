package importstate_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/try-pulse/pulse-import/internal/importstate"
)

func identity() importstate.Identity {
	return importstate.Identity{
		Importer: "jira-csv", SourceURL: "https://acme.atlassian.net",
		SourceFingerprint: "source", APIURL: "https://api.example.com/api/v1",
		WorkspaceID: "workspace", TeamID: "team",
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
	_, _ = file.WriteString(`{"version":2,"type":"item","key":"BROKEN"`)
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
	_, _ = file.WriteString(`{"version":2,"type":"item","time":"2026-01-01T00:00:00Z","key":"ENG-1","row_hash":"row","status":"failed"}` + "\n")
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
			content: `{"version":2,"type":"item","time":"2026-01-01T00:00:00Z","key":"ENG-1","row_hash":"row","status":"failed"}` + "\n",
			want:    "invalid state item",
		},
		{
			name: "state written by a different build",
			// A version bump has to be reported, not silently misread: the item
			// shape changed between v1 and v2.
			content: strings.Replace(string(valid), `"version":2`, `"version":3`, 1),
			want:    "state version 3",
		},
		{
			name:    "unknown event",
			content: string(valid) + `{"version":2,"type":"mystery","time":"2026-01-01T00:00:00Z"}` + "\n",
			want:    "unknown state event",
		},
		{
			name:    "invalid item",
			content: string(valid) + `{"version":2,"type":"item","time":"2026-01-01T00:00:00Z"}` + "\n",
			want:    "invalid state item",
		},
		{
			name:    "invalid status",
			content: string(valid) + `{"version":2,"type":"item","time":"2026-01-01T00:00:00Z","key":"ENG-1","row_hash":"row","status":"surprise"}` + "\n",
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
	if !strings.Contains(string(final), "\n{\"version\":2,\"type\":\"item\"") {
		t.Fatalf("events were not separated: %s", final)
	}
}

// The journal must not forget an id just because a later phase record does not
// repeat it. Losing the issue id after the comment phase would make rollback
// unable to delete what it created.
func TestMarkKeepsLearnedIDsAndProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	steps := []importstate.Item{
		{Key: "ENG-1", RowHash: "row", Status: importstate.StatusCreated, IssueID: "issue-1"},
		{Key: "ENG-1", RowHash: "row", Status: importstate.StatusDocUploaded, MainDocID: "doc-1"},
		{Key: "ENG-1", RowHash: "row", Status: importstate.StatusDocUploaded, Comments: 2},
		{Key: "ENG-1", RowHash: "row", Status: importstate.StatusCommented},
		{Key: "ENG-1", RowHash: "row", Status: importstate.StatusLinked},
	}
	for _, step := range steps {
		if err := journal.Mark(step); err != nil {
			t.Fatalf("mark %s: %v", step.Status, err)
		}
	}

	item, ok := journal.Item("ENG-1")
	if !ok {
		t.Fatal("item missing")
	}
	if item.IssueID != "issue-1" {
		t.Errorf("IssueID = %q, want issue-1", item.IssueID)
	}
	if item.MainDocID != "doc-1" {
		t.Errorf("MainDocID = %q, want doc-1", item.MainDocID)
	}
	if item.Comments != 2 {
		t.Errorf("Comments = %d, want 2 (a later record must not lower it)", item.Comments)
	}
	if !item.Complete() {
		t.Errorf("status %q should be complete", item.Status)
	}
}

func TestPhaseOrdering(t *testing.T) {
	ladder := []importstate.Status{
		importstate.StatusCreating,
		importstate.StatusCreated,
		importstate.StatusDocUploaded,
		importstate.StatusCommented,
		importstate.StatusLinked,
	}
	for i := 1; i < len(ladder); i++ {
		if importstate.Phase(ladder[i]) <= importstate.Phase(ladder[i-1]) {
			t.Fatalf("%s must rank above %s", ladder[i], ladder[i-1])
		}
	}
	for _, offLadder := range []importstate.Status{
		importstate.StatusFailed, importstate.StatusUnknown,
		importstate.StatusDocUnknown, importstate.StatusCommentUnknown,
	} {
		if importstate.Phase(offLadder) != -1 {
			t.Errorf("%s is not on the happy path and must rank -1", offLadder)
		}
	}
}

func TestItemsAreReturnedInCreationOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	for _, key := range []string{"ENG-3", "ENG-1", "ENG-2"} {
		if err := journal.Mark(importstate.Item{
			Key: key, RowHash: "row-" + key, Status: importstate.StatusCreated, IssueID: "id-" + key,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Re-marking must not reorder: rollback deletes in reverse creation order so
	// a sub-issue goes before its parent.
	if err := journal.Mark(importstate.Item{
		Key: "ENG-3", RowHash: "row-ENG-3", Status: importstate.StatusLinked,
	}); err != nil {
		t.Fatal(err)
	}

	var keys []string
	for _, item := range journal.Items() {
		keys = append(keys, item.Key)
	}
	want := []string{"ENG-3", "ENG-1", "ENG-2"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", keys, want)
	}
}

func TestConcurrentMarksAreSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	journal, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })

	const workers = 24
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("ENG-%d", i)
			_ = journal.Mark(importstate.Item{
				Key: key, RowHash: "row", Status: importstate.StatusCreated, IssueID: "id-" + key,
			})
		}(i)
	}
	wg.Wait()

	if got := len(journal.Items()); got != workers {
		t.Fatalf("recorded %d items, want %d", got, workers)
	}
	// Every record must still be a whole, parseable line.
	_ = journal.Close()
	reopened, err := importstate.Open(path, identity())
	if err != nil {
		t.Fatalf("journal is not parseable after concurrent writes: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := len(reopened.Items()); got != workers {
		t.Fatalf("replayed %d items, want %d", got, workers)
	}
}

func TestReadIdentity(t *testing.T) {
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
	got, err := importstate.ReadIdentity(data)
	if err != nil {
		t.Fatal(err)
	}
	if got != identity() {
		t.Fatalf("identity = %+v", got)
	}
	if _, err := importstate.ReadIdentity(nil); err == nil {
		t.Fatal("expected an error for empty input")
	}
	if _, err := importstate.ReadIdentity([]byte("{not json}\n")); err == nil {
		t.Fatal("expected an error for a corrupt header")
	}
}
