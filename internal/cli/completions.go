package cli

import (
	"github.com/spf13/cobra"
)

func registerCompletions(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("importer", cobra.FixedCompletions(
		[]string{"jira-csv"}, cobra.ShellCompDirectiveNoFileComp,
	))
	_ = cmd.RegisterFlagCompletionFunc("assignee", cobra.FixedCompletions(
		[]string{"mapped", "self", "none"}, cobra.ShellCompDirectiveNoFileComp,
	))
	_ = cmd.RegisterFlagCompletionFunc("epics", cobra.FixedCompletions(
		[]string{"project", "label"}, cobra.ShellCompDirectiveNoFileComp,
	))
	_ = cmd.MarkFlagFilename("file", "csv")
	_ = cmd.MarkFlagFilename("state-file", "jsonl")
}

func registerRollbackCompletions(cmd *cobra.Command) {
	_ = cmd.MarkFlagFilename("state-file", "jsonl")
}
