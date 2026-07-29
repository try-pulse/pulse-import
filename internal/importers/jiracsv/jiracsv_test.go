package jiracsv_test

import (
	"context"
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
	res, err := imp.Import(context.Background())
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
	if !strings.Contains(first.BodyMarkdown, "https://acme.atlassian.net/browse/ENG-1") ||
		!strings.Contains(first.BodyMarkdown, "View original issue in Jira") {
		t.Errorf("body missing backlink: %s", first.BodyMarkdown)
	}
	if len(first.Labels) == 0 {
		t.Fatal("expected type label")
	}
	if _, ok := res.Users["jane doe"]; !ok {
		t.Fatal("expected Jane Doe user")
	}
	if first.Key != "ENG-1" || first.RowHash == "" || res.SourceFingerprint == "" {
		t.Fatalf("missing stable source identity: issue=%+v fingerprint=%q", first, res.SourceFingerprint)
	}
	if imp.Name() != "Jira (CSV)" {
		t.Fatalf("meta %q", imp.Name())
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
	res, err := imp.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("want 1 issue (empty summary skipped), got %d", len(res.Issues))
	}
	if len(res.Diagnostics) != 1 || !strings.Contains(res.Diagnostics[0].Message, "empty Summary") {
		t.Fatalf("diagnostics=%+v", res.Diagnostics)
	}
	if !strings.Contains(res.Issues[0].BodyMarkdown, "https://jira.example.com/browse/KEEP-2") {
		t.Fatalf("body=%q", res.Issues[0].BodyMarkdown)
	}
	if !strings.Contains(res.Issues[0].BodyMarkdown, "hello") {
		t.Fatalf("body=%q", res.Issues[0].BodyMarkdown)
	}
}

func TestHeaderAndIssueKeyValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "missing summary", content: "Issue key\nENG-1\n", wantErr: "Summary"},
		{name: "missing key header", content: "Summary\nHello\n", wantErr: "Issue key"},
		{name: "duplicate scalar header", content: "Summary,Summary,Issue key\nA,B,ENG-1\n", wantErr: "duplicate"},
		{name: "wrong field count", content: "Summary,Issue key\nA,ENG-1,extra\n", wantErr: "row 2"},
		{name: "missing field", content: "Summary,Issue key,Priority\nA,ENG-1\n", wantErr: "row 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "input.csv")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).
				Import(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDuplicateAndMissingIssueKeysBecomeBlockingDiagnostics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.csv")
	content := "Summary,Issue key\nFirst,ENG-1\nSecond,eng-1\nMissing,\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).
		Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var errors int
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Level == importers.DiagnosticError {
			errors++
		}
	}
	if errors != 2 {
		t.Fatalf("diagnostics=%+v", result.Diagnostics)
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

	res, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "x"}).Import(context.Background())
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
	res, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range res.Issues[0].Labels {
		if l == "release: 2.3.0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("labels=%v", res.Issues[0].Labels)
	}
}

func TestMissingFile(t *testing.T) {
	t.Parallel()
	_, err := jiracsv.New(jiracsv.Options{FilePath: "/no/such/file.csv"}).Import(context.Background())
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
	res, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).Import(context.Background())
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
	for _, want := range []string{"frontend", "urgent", "release: 1.0", "release: 1.1"} {
		if !labels[want] {
			t.Fatalf("missing %q in %v", want, res.Issues[0].Labels)
		}
	}
}

func TestPhysicalRowNumbersAfterMultilineField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "multiline.csv")
	content := "Summary,Issue key,Description\n" +
		"First,ENG-1,\"line one\nline two\"\n\n" +
		",ENG-2,missing summary\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).
		Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Issues[0].SourceRow != 2 {
		t.Fatalf("first source row=%d", result.Issues[0].SourceRow)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Row != 5 {
		t.Fatalf("diagnostics=%+v", result.Diagnostics)
	}
}

func FuzzImportCSV(f *testing.F) {
	f.Add("Summary,Issue key\nHello,ENG-1\n")
	f.Add("Summary,Issue key,Description\nHello,ENG-1,\"multi\nline\"\n")
	f.Fuzz(func(t *testing.T, content string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "fuzz.csv")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).
			Import(context.Background())
	})
}
