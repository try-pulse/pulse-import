package jiracsv

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/try-pulse/pulse-import/internal/importers"
)

// EpicMode decides what a Jira epic row becomes in Pulse.
type EpicMode string

const (
	// EpicModeProject creates a Pulse project per epic and files its children
	// into it. This mirrors Linear's Jira mapping.
	EpicModeProject EpicMode = "project"
	// EpicModeLabel imports epics as ordinary issues and records the epic on
	// each child as a label instead.
	EpicModeLabel EpicMode = "label"
)

type Options struct {
	FilePath     string
	JiraSiteName string
	CustomURL    string
	// Epics selects the epic mapping. Defaults to EpicModeProject.
	Epics EpicMode
	// SkipComments drops Jira comments instead of importing them.
	SkipComments bool
}

type Importer struct {
	opts Options
}

func New(opts Options) *Importer {
	if opts.Epics == "" {
		opts.Epics = EpicModeProject
	}
	return &Importer{opts: opts}
}

func (i *Importer) Name() string { return "Jira (CSV)" }

// columnsRead lists every header the importer consumes, so anything else can be
// reported as knowingly ignored rather than silently dropped.
var columnsRead = map[string]bool{}

func init() {
	for _, name := range []string{
		"summary", "issue key", "issue id", "issue type", "status", "resolution",
		"priority", "assignee", "assignee id", "assignee email", "assignee email address",
		"reporter", "creator", "created", "updated", "resolved", "due date",
		"description", "environment", "labels", "component/s", "components",
		"fix version/s", "release", "affects version/s", "sprint", "parent",
		"parent id", "parent summary", "comment", "attachment",
		"original estimate", "time spent", "remaining estimate",
		"custom field (epic link)", "custom field (epic name)",
		"custom field (story points)", "custom field (story point estimate)",
		"story points", "outward issue link (blocks)", "inward issue link (blocks)",
	} {
		columnsRead[name] = true
	}
}

// parsed holds one CSV row after field extraction but before Jira's hierarchy
// has been resolved, which needs the whole file (a parent may appear after its
// child, and whether a parent is an epic decides project vs sub-issue).
type parsed struct {
	rowNumber int
	rowHash   string
	key       string
	issueID   string
	issueType string
	isEpic    bool
	isSubTask bool

	// parentRef is Jira's Parent column, which points at an epic for a story and
	// at a story for a sub-task.
	parentRef   string
	parentIDRef string
	epicLinkRef string

	// resolved* are filled in by link() once the whole file is known.
	resolvedParentKey string
	resolvedEpicKey   string

	summary string
	body    string
	status  string
	// resolvedStatus and statusOverridden come from status + resolution.
	resolvedStatus   string
	statusOverridden bool
	priority         importers.IssuePriority
	pulseType        importers.IssueType
	typeLabel        string

	assignee      string
	assigneeEmail string

	labels    []labelRef
	dueDate   *time.Time
	updatedAt *time.Time
	points    *float64
	estimateS *int
	comments  []importers.Comment
	blocks    []string
	blockedBy []string

	// doc carries the provenance fields gathered while parsing; the description
	// and epic/parent context are filled in once the hierarchy is resolved.
	doc *docBuilder
}

type labelRef struct {
	name string
	kind importers.LabelKind
}

func (i *Importer) Import(ctx context.Context) (*importers.ImportResult, error) {
	f, err := os.Open(i.opts.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer func() { _ = f.Close() }()

	sourceHash := sha256.New()
	cr := csv.NewReader(io.TeeReader(f, sourceHash))
	cr.LazyQuotes = true

	rawHeader, err := cr.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("csv is empty")
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	head, err := parseHeader(rawHeader)
	if err != nil {
		return nil, err
	}
	cr.FieldsPerRecord = -1

	result := &importers.ImportResult{
		Users:       map[string]importers.User{},
		Labels:      map[string]importers.Label{},
		StatusNames: map[string][]string{},
		SourcePath:  i.opts.FilePath,
		SourceURL:   i.sourceURL(),
	}
	result.IgnoredColumns = ignoredColumns(head)

	rows, err := i.readRows(ctx, cr, head, result)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("csv contains no importable issues")
	}

	i.link(rows, result)
	i.emit(rows, result)

	if len(result.Issues) == 0 && len(result.Projects) == 0 {
		return nil, fmt.Errorf("csv contains no importable issues")
	}
	result.SourceFingerprint = hex.EncodeToString(sourceHash.Sum(nil))
	return result, nil
}

func (i *Importer) readRows(
	ctx context.Context,
	cr *csv.Reader,
	head *header,
	result *importers.ImportResult,
) ([]*parsed, error) {
	var rows []*parsed
	seenKeys := map[string]int{}
	rowNumber := 1

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rowNumber++
			var parseError *csv.ParseError
			if errors.As(err, &parseError) {
				rowNumber = parseError.Line
			}
			return nil, fmt.Errorf("read row %d: %w", rowNumber, err)
		}
		rowNumber, _ = cr.FieldPos(0)

		if len(record) > len(head.names) {
			for _, extra := range record[len(head.names):] {
				if strings.TrimSpace(extra) != "" {
					return nil, fmt.Errorf(
						"read row %d: got %d fields but header has %d",
						rowNumber, len(record), len(head.names),
					)
				}
			}
			record = record[:len(head.names)]
		}
		// A short record is padded rather than rejected: Jira omits trailing
		// empty cells in some exports, and the columns that matter are indexed
		// by name, not position.
		for len(record) < len(head.names) {
			record = append(record, "")
		}

		rw := row{header: head, cells: record}
		summary := strings.TrimSpace(rw.first("summary"))
		if summary == "" {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticWarning, Row: rowNumber,
				Message: "skipped row with empty Summary",
			})
			continue
		}

		key := strings.TrimSpace(rw.first("issue key"))
		if key == "" {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticError, Row: rowNumber,
				Message: "Issue key is required for safe resume",
			})
		} else if firstRow, exists := seenKeys[strings.ToLower(key)]; exists {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticError, Row: rowNumber,
				Message: fmt.Sprintf("duplicate Issue key %q (first seen on row %d)", key, firstRow),
			})
		} else {
			seenKeys[strings.ToLower(key)] = rowNumber
		}

		rowBytes, _ := json.Marshal(record)
		rowHash := sha256.Sum256(rowBytes)
		row := i.parseRow(rw, rowNumber, hex.EncodeToString(rowHash[:]), key, summary, result)
		rows = append(rows, row)
	}
	return rows, nil
}

func (i *Importer) sourceURL() string {
	if i.opts.JiraSiteName != "" {
		return "https://" + strings.ToLower(i.opts.JiraSiteName) + ".atlassian.net"
	}
	return strings.TrimRight(i.opts.CustomURL, "/")
}

func (i *Importer) browseURL(issueKey string) string {
	base := i.sourceURL()
	if base == "" || issueKey == "" {
		return ""
	}
	return base + "/browse/" + url.PathEscape(issueKey)
}

// ignoredColumns lists the header names the importer never reads, so the plan
// can tell the user exactly which Jira data is being left behind.
func ignoredColumns(head *header) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range head.names {
		if strings.HasPrefix(name, "\x00") || columnsRead[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
