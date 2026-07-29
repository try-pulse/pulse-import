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
	"strings"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/jira2md"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

type Options struct {
	FilePath     string
	JiraSiteName string
	CustomURL    string
}

type Importer struct {
	opts Options
}

func New(opts Options) *Importer {
	return &Importer{opts: opts}
}

func (i *Importer) Name() string { return "Jira (CSV)" }

func (i *Importer) Import(ctx context.Context) (*importers.ImportResult, error) {
	f, err := os.Open(i.opts.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer func() { _ = f.Close() }()

	sourceHash := sha256.New()
	cr := csv.NewReader(io.TeeReader(f, sourceHash))
	cr.LazyQuotes = true

	header, err := cr.Read()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("csv is empty")
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	normalizedHeader, err := normalizeHeader(header)
	if err != nil {
		return nil, err
	}
	cr.FieldsPerRecord = -1

	result := &importers.ImportResult{
		Users:      map[string]importers.User{},
		Labels:     map[string]importers.Label{},
		SourcePath: i.opts.FilePath,
		SourceURL:  i.sourceURL(),
	}
	seenKeys := map[string]int{}
	rowNumber := 1
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec, err := cr.Read()
		if err == io.EOF {
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
		if len(rec) < len(normalizedHeader) {
			return nil, fmt.Errorf(
				"read row %d: got %d fields but header has %d",
				rowNumber, len(rec), len(normalizedHeader),
			)
		}
		if len(rec) > len(normalizedHeader) {
			for _, extra := range rec[len(normalizedHeader):] {
				if strings.TrimSpace(extra) != "" {
					return nil, fmt.Errorf(
						"read row %d: got %d fields but header has %d",
						rowNumber, len(rec), len(normalizedHeader),
					)
				}
			}
			rec = rec[:len(normalizedHeader)]
		}
		rw := makeRow(normalizedHeader, rec)
		summary := strings.TrimSpace(rw.first("summary"))
		if summary == "" {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticWarning, Row: rowNumber, Message: "skipped row with empty Summary",
			})
			continue
		}

		issueKey := strings.TrimSpace(rw.first("issue key"))
		if issueKey == "" {
			result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
				Level: importers.DiagnosticError, Row: rowNumber, Message: "Issue key is required for safe resume",
			})
		} else {
			folded := strings.ToLower(issueKey)
			if firstRow, exists := seenKeys[folded]; exists {
				result.Diagnostics = append(result.Diagnostics, importers.Diagnostic{
					Level: importers.DiagnosticError,
					Row:   rowNumber,
					Message: fmt.Sprintf(
						"duplicate Issue key %q (first seen on row %d)", issueKey, firstRow,
					),
				})
			} else {
				seenKeys[folded] = rowNumber
			}
		}

		browseURL := i.browseURL(issueKey)
		body := jira2md.Convert(rw.first("description"))
		if browseURL != "" {
			link := fmt.Sprintf("[View original issue in Jira](%s)", browseURL)
			if body != "" {
				body += "\n\n" + link
			} else {
				body = link
			}
		}

		issueType := rw.first("issue type")
		pulseType, typeLabel := statusmap.MapIssueType(issueType)
		labels := make([]string, 0)
		labelSeen := map[string]struct{}{}
		addLabel := func(name string) {
			name = strings.TrimSpace(name)
			key := strings.ToLower(name)
			if name == "" {
				return
			}
			if _, exists := labelSeen[key]; exists {
				return
			}
			labelSeen[key] = struct{}{}
			labels = append(labels, key)
			if _, exists := result.Labels[key]; !exists {
				result.Labels[key] = importers.Label{Name: name}
			}
		}
		addLabel(typeLabel)
		for _, value := range rw.all("labels") {
			addLabel(value)
		}
		releases := rw.all("release")
		if len(releases) == 0 {
			releases = rw.all("fix version/s")
		}
		if len(releases) == 0 {
			releases = rw.all("affects version/s")
		}
		for _, release := range releases {
			if release = strings.TrimSpace(release); release != "" {
				addLabel("Release: " + release)
			}
		}

		assignee := strings.TrimSpace(rw.first("assignee"))
		if strings.EqualFold(assignee, "unassigned") {
			assignee = ""
		}
		assigneeEmail := ""
		if assignee != "" {
			assigneeEmail = firstNonEmpty(
				rw.first("assignee email"),
				rw.first("assignee email address"),
			)
			assigneeEmail = strings.TrimSpace(assigneeEmail)
			result.Users[strings.ToLower(assignee)] = importers.User{Name: assignee, Email: assigneeEmail}
		}

		rowBytes, _ := json.Marshal(rec)
		rowHash := sha256.Sum256(rowBytes)
		result.Issues = append(result.Issues, importers.Issue{
			Key:           issueKey,
			SourceRow:     rowNumber,
			RowHash:       hex.EncodeToString(rowHash[:]),
			Title:         summary,
			BodyMarkdown:  body,
			Status:        rw.first("status"),
			AssigneeID:    assignee,
			AssigneeEmail: assigneeEmail,
			Priority:      statusmap.MapPriority(rw.first("priority")),
			Type:          pulseType,
			Labels:        labels,
		})
	}
	if len(result.Issues) == 0 {
		return nil, fmt.Errorf("csv contains no importable issues")
	}
	result.SourceFingerprint = hex.EncodeToString(sourceHash.Sum(nil))
	return result, nil
}

func normalizeHeader(header []string) ([]string, error) {
	if len(header) == 0 {
		return nil, fmt.Errorf("csv header is empty")
	}
	header[0] = strings.TrimPrefix(header[0], "\ufeff")
	out := make([]string, len(header))
	counts := map[string]int{}
	allowedRepeated := map[string]bool{
		"labels": true, "release": true, "fix version/s": true, "affects version/s": true,
	}
	for index, raw := range header {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			return nil, fmt.Errorf("csv header column %d is empty", index+1)
		}
		out[index] = name
		counts[name]++
		if counts[name] > 1 && !allowedRepeated[name] {
			return nil, fmt.Errorf("duplicate csv header %q", raw)
		}
	}
	if counts["summary"] == 0 {
		return nil, fmt.Errorf(`csv is missing required "Summary" header`)
	}
	if counts["issue key"] == 0 {
		return nil, fmt.Errorf(`csv is missing required "Issue key" header`)
	}
	return out, nil
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

type row map[string][]string

func (r row) first(name string) string {
	values := r[strings.ToLower(strings.TrimSpace(name))]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (r row) all(name string) []string {
	return r[strings.ToLower(strings.TrimSpace(name))]
}

func makeRow(header, record []string) row {
	rw := row{}
	for index, name := range header {
		if index >= len(record) {
			break
		}
		value := record[index]
		if strings.TrimSpace(value) == "" {
			continue
		}
		rw[name] = append(rw[name], value)
	}
	return rw
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
