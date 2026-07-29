package jiracsv_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importers/jiracsv"
)

func TestImportSampleCSV(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "testdata", "jira", "sample.csv")
	imp := jiracsv.New(jiracsv.Options{
		FilePath:     path,
		JiraSiteName: "acme",
	})
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Issues) != 3 {
		t.Fatalf("want 3 issues, got %d", len(res.Issues))
	}
	first := res.Issues[0]
	if first.Title != "Fix login bug" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Priority != importers.PriorityHigh {
		t.Errorf("priority = %q", first.Priority)
	}
	if first.Type != importers.TypeBug {
		t.Errorf("type = %q", first.Type)
	}
	if first.URL != "https://acme.atlassian.net/browse/ENG-1" {
		t.Errorf("url = %q", first.URL)
	}
	if !strings.Contains(first.BodyMarkdown, "View original issue in Jira") {
		t.Errorf("body missing backlink: %s", first.BodyMarkdown)
	}
	if len(first.Labels) == 0 {
		t.Fatal("expected type label")
	}
	if _, ok := res.Users["Jane Doe"]; !ok {
		t.Fatal("expected Jane Doe user")
	}
	if imp.Name() != "Jira (CSV)" || imp.DefaultTeamName() != "Jira" {
		t.Fatalf("meta %q %q", imp.Name(), imp.DefaultTeamName())
	}
}

func TestOnPremURLAndSkipEmptySummary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "jira.csv")
	content := "Summary,Issue key,Issue Type,Status,Priority,Assignee,Description\n" +
		",SKIP-1,Bug,Done,Low,,\n" +
		"Keep me,KEEP-2,Task,To Do,Medium,Bob,hello\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	imp := jiracsv.New(jiracsv.Options{
		FilePath:  path,
		CustomURL: "https://jira.example.com/",
	})
	res, err := imp.Import()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("want 1 issue (empty summary skipped), got %d", len(res.Issues))
	}
	if res.Issues[0].URL != "https://jira.example.com/browse/KEEP-2" {
		t.Fatalf("url=%q", res.Issues[0].URL)
	}
	if !strings.Contains(res.Issues[0].BodyMarkdown, "hello") {
		t.Fatalf("body=%q", res.Issues[0].BodyMarkdown)
	}
}

func TestBOMAndCaseInsensitiveHeaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bom.csv")
	content := "\ufeffsummary,ISSUE KEY,issue type,status,priority,assignee,description\n" +
		"Bombed,BOM-1,Bug,Open,Highest,Ada,desc\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "x"}).Import()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("got %d", len(res.Issues))
	}
	if res.Issues[0].Title != "Bombed" {
		t.Fatalf("title=%q", res.Issues[0].Title)
	}
	if res.Issues[0].Priority != importers.PriorityUrgent {
		t.Fatalf("priority=%q", res.Issues[0].Priority)
	}
}

func TestReleaseLabelFromFixVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rel.csv")
	content := "Summary,Issue key,Issue Type,Status,Priority,Assignee,Description,Fix Version/s\n" +
		"Ship,REL-1,Story,Done,Low,,n/a,2.3.0\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).Import()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range res.Issues[0].Labels {
		if l == "Release: 2.3.0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("labels=%v", res.Issues[0].Labels)
	}
}

func TestMissingFile(t *testing.T) {
	t.Parallel()
	_, err := jiracsv.New(jiracsv.Options{FilePath: "/no/such/file.csv"}).Import()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRepeatedLabelsAndFixVersions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.csv")
	content := "Summary,Issue key,Issue Type,Status,Priority,Assignee,Description,Labels,Labels,Fix Version/s,Fix Version/s\n" +
		"Multi,M-1,Task,To Do,Medium,,body,frontend,urgent,1.0,1.1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).Import()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("got %d", len(res.Issues))
	}
	labels := map[string]bool{}
	for _, l := range res.Issues[0].Labels {
		labels[l] = true
	}
	for _, want := range []string{"frontend", "urgent", "Release: 1.0", "Release: 1.1"} {
		if !labels[want] {
			t.Fatalf("missing %q in %v", want, res.Issues[0].Labels)
		}
	}
}
