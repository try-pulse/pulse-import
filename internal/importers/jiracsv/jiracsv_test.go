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

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func importCSV(t *testing.T, content string, opts ...func(*jiracsv.Options)) *importers.ImportResult {
	t.Helper()
	options := jiracsv.Options{FilePath: write(t, content), JiraSiteName: "acme"}
	for _, apply := range opts {
		apply(&options)
	}
	result, err := jiracsv.New(options).Import(context.Background())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

func issueByKey(t *testing.T, result *importers.ImportResult, key string) importers.Issue {
	t.Helper()
	for _, issue := range result.Issues {
		if issue.Key == key {
			return issue
		}
	}
	t.Fatalf("issue %s not imported; have %d issues", key, len(result.Issues))
	return importers.Issue{}
}

func TestImportSampleCSV(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "testdata", "jira", "sample.csv")
	imp := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"})
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
	if !strings.Contains(first.BodyMarkdown, "https://acme.atlassian.net/browse/ENG-1") {
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

// TestImportRealJiraAllFieldsExport is the regression guard for the header
// handling: Jira's own "all fields" export repeats Comment, Attachment,
// Watchers, Component/s, Sprint, Labels, versions and issue-link columns, and
// the README tells the user to produce exactly that file.
func TestImportRealJiraAllFieldsExport(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "testdata", "jira", "all-fields.csv")
	result, err := jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).
		Import(context.Background())
	if err != nil {
		t.Fatalf("real Jira all-fields export rejected: %v", err)
	}

	t.Run("epic becomes a project", func(t *testing.T) {
		if len(result.Projects) != 1 {
			t.Fatalf("want 1 project, got %d", len(result.Projects))
		}
		project := result.Projects[0]
		if project.Key != "ENG-1" || project.Title != "Checkout revamp" {
			t.Fatalf("project=%+v", project)
		}
		if !strings.Contains(project.BodyMarkdown, "Make checkout **fast**") {
			t.Errorf("project body lost the description: %s", project.BodyMarkdown)
		}
	})

	t.Run("epic children are filed into it", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		if story.EpicKey != "ENG-1" {
			t.Errorf("ENG-2 epic = %q, want ENG-1", story.EpicKey)
		}
		// ENG-4 references the epic through the legacy Epic Link custom field.
		if task := issueByKey(t, result, "ENG-4"); task.EpicKey != "ENG-1" {
			t.Errorf("ENG-4 epic = %q, want ENG-1", task.EpicKey)
		}
	})

	t.Run("sub-task becomes a sub-issue", func(t *testing.T) {
		sub := issueByKey(t, result, "ENG-3")
		if sub.ParentKey != "ENG-2" {
			t.Errorf("ENG-3 parent = %q, want ENG-2", sub.ParentKey)
		}
	})

	t.Run("nested sub-task is flattened onto the top-level ancestor", func(t *testing.T) {
		nested := issueByKey(t, result, "ENG-5")
		if nested.ParentKey != "ENG-2" {
			t.Errorf("ENG-5 parent = %q, want ENG-2 (re-parented from the sub-task ENG-3)", nested.ParentKey)
		}
		if !hasDiagnostic(result, "re-parented") {
			t.Error("expected a warning about the re-parenting")
		}
	})

	t.Run("missing parent is reported and dropped", func(t *testing.T) {
		orphan := issueByKey(t, result, "ENG-7")
		if orphan.ParentKey != "" {
			t.Errorf("ENG-7 parent = %q, want empty", orphan.ParentKey)
		}
		if !hasDiagnostic(result, "ENG-999") {
			t.Error("expected a warning naming the missing parent")
		}
	})

	t.Run("resolution closes an issue whose status still reads open", func(t *testing.T) {
		task := issueByKey(t, result, "ENG-4")
		if task.Status != "Open" {
			t.Fatalf("raw status = %q", task.Status)
		}
		if task.StatusOverride != "done" {
			t.Errorf("StatusOverride = %q, want done (resolution was \"Won't Do\")", task.StatusOverride)
		}
	})

	t.Run("multi-value columns all land", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		labels := map[string]bool{}
		for _, label := range story.Labels {
			labels[label] = true
		}
		for _, want := range []string{
			"performance", "checkout",
			"component: payments", "component: web",
			"release: 2.1", "release: 2.2",
			"affects: 2.0",
			"sprint: sprint 12", "sprint: sprint 13",
			"type: story",
		} {
			if !labels[want] {
				t.Errorf("missing label %q in %v", want, story.Labels)
			}
		}
	})

	t.Run("comments are parsed with author and timestamp", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		if len(story.Comments) != 3 {
			t.Fatalf("want 3 comments, got %d: %+v", len(story.Comments), story.Comments)
		}
		first := story.Comments[0]
		if first.Author != "5b10a2:jane" {
			t.Errorf("author = %q", first.Author)
		}
		if first.Created.IsZero() {
			t.Error("comment timestamp not parsed")
		}
		// The body itself contains a semicolon, which must not be treated as a
		// field separator.
		if !strings.Contains(first.Body, "not the API") {
			t.Errorf("comment body truncated at a semicolon: %q", first.Body)
		}
		if !strings.Contains(first.Body, "**DB**") {
			t.Errorf("comment body not converted from wiki markup: %q", first.Body)
		}
	})

	t.Run("blocks relations are captured in both directions", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		got := map[string]string{}
		for _, relation := range story.Relations {
			got[relation.TargetKey] = relation.Kind
		}
		if got["ENG-4"] != importers.RelationBlocks || got["ENG-9"] != importers.RelationBlocks {
			t.Errorf("outward links = %+v", story.Relations)
		}
		if got["ENG-3"] != importers.RelationBlockedBy {
			t.Errorf("inward link = %+v", story.Relations)
		}
	})

	t.Run("dates and estimates are parsed", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		if story.DueDate == nil || story.DueDate.Format("2006-01-02") != "2025-02-28" {
			t.Errorf("due date = %v", story.DueDate)
		}
		if story.UpdatedAt == nil || story.UpdatedAt.Format("2006-01-02") != "2025-02-06" {
			t.Errorf("updated = %v", story.UpdatedAt)
		}
		if story.StoryPoints == nil || *story.StoryPoints != 5 {
			t.Errorf("story points = %v", story.StoryPoints)
		}
		if story.OriginalEstimateSeconds == nil || *story.OriginalEstimateSeconds != 10800 {
			t.Errorf("original estimate = %v", story.OriginalEstimateSeconds)
		}
	})

	t.Run("provenance Pulse cannot store goes into the Main Doc", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		for _, want := range []string{
			"| Reporter | John Smith |",
			"| Creator | John Smith |",
			"| Created | 2025-01-02 11:00 UTC |",
			"| Environment | prod |",
			"| Time spent | 1h |",
			"| Original estimate | 3h |",
			"Sprint 12, Sprint 13",
			"[latency.png](https://acme.atlassian.net/secure/attachment/1/latency.png)",
			"[trace.txt](https://acme.atlassian.net/secure/attachment/2/trace.txt)",
		} {
			if !strings.Contains(story.BodyMarkdown, want) {
				t.Errorf("Main Doc missing %q; got:\n%s", want, story.BodyMarkdown)
			}
		}
	})

	t.Run("inline attachment macro links to the original file", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		want := "[latency.png](https://acme.atlassian.net/secure/attachment/1/latency.png)"
		if strings.Count(story.BodyMarkdown, want) < 2 {
			t.Errorf("expected the !latency.png! macro and the attachment list to link; got:\n%s", story.BodyMarkdown)
		}
	})

	t.Run("wiki table in the description survives", func(t *testing.T) {
		story := issueByKey(t, result, "ENG-2")
		if !strings.Contains(story.BodyMarkdown, "| Step | Time |") {
			t.Errorf("table lost: %s", story.BodyMarkdown)
		}
	})

	t.Run("empty summary row is skipped", func(t *testing.T) {
		for _, issue := range result.Issues {
			if issue.Key == "ENG-6" {
				t.Error("ENG-6 has no summary and must be skipped")
			}
		}
		if !hasDiagnostic(result, "empty Summary") {
			t.Error("expected a skip warning")
		}
	})

	t.Run("unmapped columns are reported rather than silently dropped", func(t *testing.T) {
		ignored := strings.Join(result.IgnoredColumns, ",")
		for _, want := range []string{"votes", "watchers", "work ratio", "security level", "status category"} {
			if !strings.Contains(ignored, want) {
				t.Errorf("expected %q among ignored columns, got %v", want, result.IgnoredColumns)
			}
		}
		for _, unwanted := range []string{"summary", "comment", "attachment"} {
			for _, got := range result.IgnoredColumns {
				if got == unwanted {
					t.Errorf("%q is mapped but reported as ignored", unwanted)
				}
			}
		}
	})

	t.Run("status names are collected for the review step", func(t *testing.T) {
		if got := result.StatusNames["in_progress"]; len(got) == 0 {
			t.Errorf("status names = %+v", result.StatusNames)
		}
		if got := result.StatusNames["done"]; len(got) == 0 {
			t.Errorf("status names = %+v", result.StatusNames)
		}
	})
}

func hasDiagnostic(result *importers.ImportResult, substring string) bool {
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic.Message, substring) {
			return true
		}
	}
	return false
}

func TestEpicModeLabel(t *testing.T) {
	t.Parallel()
	content := "Summary,Issue key,Issue Type,Custom field (Epic Link)\n" +
		"Checkout revamp,ENG-1,Epic,\n" +
		"Speed up,ENG-2,Story,ENG-1\n"
	result := importCSV(t, content, func(o *jiracsv.Options) { o.Epics = jiracsv.EpicModeLabel })

	if len(result.Projects) != 0 {
		t.Fatalf("label mode must not create projects, got %+v", result.Projects)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("want the epic imported as an issue too, got %d", len(result.Issues))
	}
	story := issueByKey(t, result, "ENG-2")
	found := false
	for _, label := range story.Labels {
		if label == "epic: checkout revamp" {
			found = true
		}
	}
	if !found {
		t.Errorf("labels = %v", story.Labels)
	}
}

func TestSkipComments(t *testing.T) {
	t.Parallel()
	content := "Summary,Issue key,Comment\nA,ENG-1,03/Jan/25 9:00 AM;jane;hello\n"
	result := importCSV(t, content, func(o *jiracsv.Options) { o.SkipComments = true })
	if len(result.Issues[0].Comments) != 0 {
		t.Fatalf("comments = %+v", result.Issues[0].Comments)
	}
}

func TestOnPremURLAndSkipEmptySummary(t *testing.T) {
	t.Parallel()
	content := "Summary,Issue key,Issue Type,Status,Priority,Assignee,Description\n" +
		",SKIP-1,Bug,Done,Low,,\n" +
		"Keep me,KEEP-2,Task,To Do,Medium,Bob,hello\n"
	res, err := jiracsv.New(jiracsv.Options{
		FilePath:  write(t, content),
		CustomURL: "https://jira.example.com/",
	}).Import(context.Background())
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

func TestHeaderValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "missing summary", content: "Issue key\nENG-1\n", wantErr: "Summary"},
		{name: "missing key header", content: "Summary\nHello\n", wantErr: "Issue key"},
		{
			name:    "repeated single-valued header",
			content: "Summary,Summary,Issue key\nA,B,ENG-1\n",
			wantErr: "single-valued",
		},
		{name: "extra non-empty field", content: "Summary,Issue key\nA,ENG-1,extra\n", wantErr: "row 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := jiracsv.New(jiracsv.Options{FilePath: write(t, tt.content), JiraSiteName: "acme"}).
				Import(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// Jira omits trailing empty cells in some exports, so a short record is padded
// rather than rejected — the mapped columns are looked up by name anyway.
func TestShortRecordsArePadded(t *testing.T) {
	t.Parallel()
	result := importCSV(t, "Summary,Issue key,Priority\nA,ENG-1\n")
	if len(result.Issues) != 1 {
		t.Fatalf("got %d issues", len(result.Issues))
	}
	if result.Issues[0].Priority != importers.PriorityNoPriority {
		t.Fatalf("priority=%q", result.Issues[0].Priority)
	}
}

// Trailing empty header cells are common in Jira exports and must not fail.
func TestBlankTrailingHeaderColumns(t *testing.T) {
	t.Parallel()
	result := importCSV(t, "Summary,Issue key,,\nA,ENG-1,,\n")
	if len(result.Issues) != 1 {
		t.Fatalf("got %d issues", len(result.Issues))
	}
}

func TestDuplicateAndMissingIssueKeysBecomeBlockingDiagnostics(t *testing.T) {
	t.Parallel()
	result := importCSV(t, "Summary,Issue key\nFirst,ENG-1\nSecond,eng-1\nMissing,\n")
	var errorCount int
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Level == importers.DiagnosticError {
			errorCount++
		}
	}
	if errorCount != 2 {
		t.Fatalf("diagnostics=%+v", result.Diagnostics)
	}
}

func TestBOMAndCaseInsensitiveHeaders(t *testing.T) {
	t.Parallel()
	result := importCSV(t,
		"\ufeffsummary,ISSUE KEY,issue type,status,priority,assignee,description\n"+
			"Bombed,BOM-1,Bug,Open,Highest,Ada,desc\n")
	if len(result.Issues) != 1 {
		t.Fatalf("got %d", len(result.Issues))
	}
	if result.Issues[0].Title != "Bombed" {
		t.Fatalf("title=%q", result.Issues[0].Title)
	}
	if result.Issues[0].Priority != importers.PriorityUrgent {
		t.Fatalf("priority=%q", result.Issues[0].Priority)
	}
}

func TestVersionLabelNamespacesAreDistinct(t *testing.T) {
	t.Parallel()
	result := importCSV(t,
		"Summary,Issue key,Fix Version/s,Affects Version/s\n"+
			"Ship,REL-1,2.3.0,2.2.0\n")
	labels := map[string]bool{}
	for _, label := range result.Issues[0].Labels {
		labels[label] = true
	}
	if !labels["release: 2.3.0"] {
		t.Errorf("labels=%v", result.Issues[0].Labels)
	}
	// An affects-version is not a fix-version; conflating them would claim the
	// bug ships in the release it was found in.
	if !labels["affects: 2.2.0"] {
		t.Errorf("labels=%v", result.Issues[0].Labels)
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
	result := importCSV(t,
		"Summary,Issue key,Issue Type,Status,Priority,Assignee,Description,Labels,Labels,Fix Version/s,Fix Version/s\n"+
			"Multi,M-1,Task,To Do,Medium,,body,frontend,urgent,1.0,1.1\n")
	labels := map[string]bool{}
	for _, label := range result.Issues[0].Labels {
		labels[label] = true
	}
	for _, want := range []string{"frontend", "urgent", "release: 1.0", "release: 1.1"} {
		if !labels[want] {
			t.Fatalf("missing %q in %v", want, result.Issues[0].Labels)
		}
	}
}

func TestPhysicalRowNumbersAfterMultilineField(t *testing.T) {
	t.Parallel()
	result := importCSV(t,
		"Summary,Issue key,Description\n"+
			"First,ENG-1,\"line one\nline two\"\n\n"+
			",ENG-2,missing summary\n")
	if result.Issues[0].SourceRow != 2 {
		t.Fatalf("first source row=%d", result.Issues[0].SourceRow)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Row != 5 {
		t.Fatalf("diagnostics=%+v", result.Diagnostics)
	}
}

func TestUserRowCountsFeedTheMappingStep(t *testing.T) {
	t.Parallel()
	result := importCSV(t,
		"Summary,Issue key,Assignee,Assignee Email\n"+
			"A,ENG-1,Jane Doe,jane@acme.com\n"+
			"B,ENG-2,Jane Doe,\n"+
			"C,ENG-3,Unassigned,\n")
	jane, ok := result.Users["jane doe"]
	if !ok {
		t.Fatalf("users=%+v", result.Users)
	}
	if jane.Rows != 2 {
		t.Errorf("Rows=%d want 2", jane.Rows)
	}
	if jane.Email != "jane@acme.com" {
		t.Errorf("Email=%q — an email present on any row must be kept", jane.Email)
	}
	if len(result.Users) != 1 {
		t.Errorf("\"Unassigned\" must not become a user: %+v", result.Users)
	}
}

func FuzzImportCSV(f *testing.F) {
	f.Add("Summary,Issue key\nHello,ENG-1\n")
	f.Add("Summary,Issue key,Description\nHello,ENG-1,\"multi\nline\"\n")
	f.Add("Summary,Issue key,Comment,Comment\nA,E-1,1;2;3,x\n")
	f.Add("Summary,Issue key,Parent\nA,E-1,E-1\n")
	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "fuzz.csv")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = jiracsv.New(jiracsv.Options{FilePath: path, JiraSiteName: "acme"}).
			Import(context.Background())
	})
}

// Jira writes CRLF line endings and quotes multi-line descriptions. Both have to
// survive, including the physical row numbers used in diagnostics.
func TestCRLFExportWithQuotedMultilineDescription(t *testing.T) {
	t.Parallel()
	content := "Summary,Issue key,Description,Labels,Labels\r\n" +
		"First issue,ENG-1,\"line one\r\nline two\",a,b\r\n" +
		"Second issue,ENG-2,plain,c,\r\n"
	result := importCSV(t, content)

	if len(result.Issues) != 2 {
		t.Fatalf("issues = %d", len(result.Issues))
	}
	first := issueByKey(t, result, "ENG-1")
	if !strings.Contains(first.BodyMarkdown, "line one") ||
		!strings.Contains(first.BodyMarkdown, "line two") {
		t.Errorf("multi-line description lost: %q", first.BodyMarkdown)
	}
	if strings.Contains(first.BodyMarkdown, "\r") {
		t.Error("carriage returns must be normalised out of the Main Doc")
	}
	labels := strings.Join(first.Labels, ",")
	if !strings.Contains(labels, "a") || !strings.Contains(labels, "b") {
		t.Errorf("labels = %v", first.Labels)
	}
}
