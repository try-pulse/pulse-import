package cli

import (
	"fmt"

	"github.com/try-pulse/pulse-import/internal/importers"
	"github.com/try-pulse/pulse-import/internal/importers/jiracsv"
)

type registration struct {
	ID    string
	Label string
	New   func(opts Options, epics jiracsv.EpicMode, p Prompter) (importers.Importer, error)
}

var registry = []registration{
	{
		ID:    "jira-csv",
		Label: "Jira (CSV export)",
		New:   newJiraCSV,
	},
}

func lookupImporter(id string) (*registration, error) {
	for i := range registry {
		r := &registry[i]
		if r.ID == id {
			return r, nil
		}
	}
	ids := make([]string, len(registry))
	for i, r := range registry {
		ids[i] = r.ID
	}
	return nil, fmt.Errorf("unknown importer %q (supported: %v)", id, ids)
}

func newJiraCSV(opts Options, epics jiracsv.EpicMode, p Prompter) (importers.Importer, error) {
	filePath := opts.File
	jiraURL := opts.JiraURL

	var err error
	if filePath == "" {
		filePath, err = p.Input("Path to Jira CSV export", "./jira-export.csv", validateFileExists)
		if err != nil {
			return nil, err
		}
	}
	filePath, err = absExisting(filePath)
	if err != nil {
		return nil, err
	}

	site, custom, err := resolveJiraURLs(jiraURL, p)
	if err != nil {
		return nil, err
	}

	return jiracsv.New(jiracsv.Options{
		FilePath:     filePath,
		JiraSiteName: site,
		CustomURL:    custom,
		Epics:        epics,
		SkipComments: opts.SkipComments,
	}), nil
}
