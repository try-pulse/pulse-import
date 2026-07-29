package jiracsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/jira2md"
	"github.com/try-pulse/pulse-import/internal/statusmap"
)

type Options struct {
	FilePath     string
	JiraSiteName string // cloud slug (acme from acme.atlassian.net)
	CustomURL    string // on-prem base URL
}

type Importer struct {
	opts Options
}

func New(opts Options) *Importer {
	return &Importer{opts: opts}
}

func (i *Importer) Name() string            { return "Jira (CSV)" }
func (i *Importer) DefaultTeamName() string { return "Jira" }

func (i *Importer) Import() (*importers.ImportResult, error) {
	f, err := os.Open(i.opts.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer func() { _ = f.Close() }()

	rows, err := readCSV(f)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("csv is empty")
	}

	result := &importers.ImportResult{
		Users:    map[string]importers.User{},
		Labels:   map[string]importers.Label{},
		Statuses: map[string]importers.StatusMeta{},
	}

	for _, row := range rows {
		summary := row.first("summary")
		if strings.TrimSpace(summary) == "" {
			continue
		}

		issueKey := row.first("issue key")
		url := i.browseURL(issueKey)

		md := jira2md.Convert(row.first("description"))
		body := md
		if url != "" {
			link := fmt.Sprintf("[View original issue in Jira](%s)", url)
			if body != "" {
				body = body + "\n\n" + link
			} else {
				body = link
			}
		}

		issueType := row.first("issue type")
		pulseType, typeLabel := statusmap.MapIssueType(issueType)

		var labels []string
		addLabel := func(name string) {
			name = strings.TrimSpace(name)
			if name == "" {
				return
			}
			labels = append(labels, name)
			result.Labels[name] = importers.Label{Name: name}
		}
		if typeLabel != "" {
			addLabel(typeLabel)
		}
		for _, v := range row.all("labels") {
			addLabel(v)
		}
		releases := row.all("release")
		if len(releases) == 0 {
			releases = row.all("fix version/s")
		}
		if len(releases) == 0 {
			releases = row.all("affects version/s")
		}
		for _, rel := range releases {
			if strings.TrimSpace(rel) != "" {
				addLabel("Release: " + strings.TrimSpace(rel))
			}
		}

		assignee := strings.TrimSpace(row.first("assignee"))
		if assignee != "" && !strings.EqualFold(assignee, "unassigned") {
			result.Users[assignee] = importers.User{Name: assignee}
		} else {
			assignee = ""
		}

		status := row.first("status")
		if status != "" {
			result.Statuses[status] = importers.StatusMeta{Name: status}
		}

		result.Issues = append(result.Issues, importers.Issue{
			Title:        summary,
			BodyMarkdown: body,
			Status:       status,
			AssigneeID:   assignee,
			Priority:     statusmap.MapPriority(row.first("priority")),
			Type:         pulseType,
			Labels:       labels,
			URL:          url,
		})
	}

	return result, nil
}

func (i *Importer) browseURL(issueKey string) string {
	if issueKey == "" {
		return ""
	}
	if i.opts.JiraSiteName != "" {
		return fmt.Sprintf("https://%s.atlassian.net/browse/%s", i.opts.JiraSiteName, issueKey)
	}
	base := strings.TrimRight(i.opts.CustomURL, "/")
	if base == "" {
		return ""
	}
	return base + "/browse/" + issueKey
}

// Jira repeats headers for multi-value columns.
type row map[string][]string

func (r row) first(name string) string {
	vals := r[strings.ToLower(strings.TrimSpace(name))]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (r row) all(name string) []string {
	return r[strings.ToLower(strings.TrimSpace(name))]
}

func readCSV(r io.Reader) ([]row, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	for i := range header {
		header[i] = strings.ToLower(strings.TrimSpace(header[i]))
	}

	var out []row
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		rw := row{}
		for i, h := range header {
			if h == "" || i >= len(rec) {
				continue
			}
			v := rec[i]
			if strings.TrimSpace(v) == "" {
				continue
			}
			rw[h] = append(rw[h], v)
		}
		out = append(out, rw)
	}
	return out, nil
}
