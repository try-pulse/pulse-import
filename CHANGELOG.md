# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Import Jira epics as Pulse projects and file their issues into them (`--epics label` keeps the old label-only behaviour)
- Import Jira sub-tasks as Pulse sub-issues, flattening nesting deeper than Pulse's one level onto the top-most ancestor
- Import Jira comments, prefixed with the original author and date because Pulse attributes comments to the token holder
- Import due dates, story-point/original estimates (snapped to the team's estimate scale), components, affects-versions and sprints
- Import Jira `blocks` / `is blocked by` links, applied in a second pass once every issue exists
- Record Reporter, Creator, Created/Updated/Resolved, Resolution, Environment, Time spent and attachment links in the Main Doc — the fields Pulse's API cannot set
- Add a `Migrated` label to everything an import creates, so the imported set can be found afterwards
- `pulse-import rollback --state-file …` deletes exactly what an import created, children before parents
- Review step: status mapping, user mapping, unmapped source columns and identifier-alignment notes are printed before the confirmation prompt
- Interactive user-mapping step plus `--map-user "NAME=<id|email|skip>"`
- `--skip-status`, `--only-status` and `--skip-stale` filters
- `--concurrency` (default 4) with wave ordering that still guarantees parents are created before children
- `--assignee`, `--skip-comments`, `--skip-labels`, `--skip-relations`, `--strict-labels`, `--no-migrated-label`
- Jira wiki support for tables, nested and mixed lists, `{quote}`, strikethrough, underline, superscript/subscript, `{code:lang}`, user mentions and attachment macros

### Fixed

- **Accept the CSV Jira actually exports.** Jira's "all fields" export repeats `Comment`, `Attachment`, `Watchers`, `Component/s`, `Sprint` and issue-link columns; the importer rejected the file outright with `duplicate csv header`. Repeated columns are now read as multi-value, and only genuinely single-valued headers are refused
- **Imported Main Docs are now openable in Pulse.** The upload sent `application/octet-stream`; Pulse only opens `text/plain` or `application/json` in its editor, so every imported description rendered as an undownloadable file
- **Truncate titles, labels and comments by bytes, not runes.** Pulse validates these limits in bytes, so non-Latin text (for example Persian) passed the client check and was then rejected with a 400
- **Resolve projects by `title`.** The client read a non-existent `name` field, so `--project <name>` never matched and the interactive picker showed blank entries
- **Re-running an import now resumes.** The state file's identity included a hash of the resolved plan, so a re-run after Jira users joined the workspace — the documented way to improve user matching — failed with "different import plan" instead of resuming
- **An archived Pulse label no longer aborts the whole import.** Pulse's label uniqueness index spans archived rows, so creating a label whose name was held by an archived one returned 409; the archived label is now unarchived and reused
- **Match assignees against the target team, not the workspace.** Pulse rejects an assignee who is not a member of the issue's team or a parent team, so every affected create failed; `--self-assign` and `--map-user` are now validated in preflight
- A failed phase no longer marks an item complete, so a resume retries exactly the work that is still owed
- Report 403s as the missing Pulse permission with a way forward instead of a bare `pulse api 403`
- Offer only active workspace memberships when picking a workspace
- Keep fix-versions and affects-versions in separate label namespaces
- Map Jira `Lowest`/`Trivial` to `low` rather than `no_priority`, which meant "never triaged"
- Tolerate short records and blank trailing header cells in Jira exports
- Preserve the language on `{code:go}` fences, and stop rewriting Markdown `#` headings into list items

## [0.2.0] - 2026-07-30

### Added

- Two-phase preflight/execute pipeline with deterministic validation and final confirmation
- Crash-safe JSONL resume journal with explicit `--adopt` and `--retry-unknown` recovery
- Pulse API contract tests, cancellation propagation, accessible prompts, and focused Plate tests

### Changed

- Rewrite README for end-user install and usage (banner, install options, mapping, no internal/dev sections)
- Tighten GitHub repository metadata (homepage, topics, disable wiki/projects)
- Upgrade Cobra, Huh, progressbar, maintained Go YAML, and the minimum Go patch to 1.25.8
- Stop persisting access tokens; config now stores only API/workspace defaults
- Stream Jira CSV rows, validate stable issue keys, and convert Main Docs before the first Pulse write
- Make assignee, label, project, title, and Jira markup mapping deterministic and preflight-visible

### Removed

- Remove the insecure `--token` flag, `PULSE_API_KEY` alias, importer aliases, and legacy token-config migration
- Remove unused estimate/result compatibility fields and obsolete single-phase dry-run implementation

### Fixed

- Issue template security contact link (`try-pulse` org slug)
- Send the required `entity_type=issue` label query to the current Pulse API
- Return a non-zero exit for Main Doc and partial import failures
- Avoid automatic retries after ambiguous issue creates and duplicate CLI error output
- Report skipped CSV rows and ambiguous user/project mappings instead of silently guessing

## [0.1.2] - 2026-07-30

### Fixed

- Docker image build for `dockers_v2`: copy binary via `TARGETPLATFORM` (`linux/<arch>/pulse-import`)
- Keep nfpm packages out of the Docker build context (separate nfpm id)

### Changed

- Bump GitHub Actions to Node 24–compatible majors (`checkout@v7`, `setup-go@v7`, Docker Buildx/login `@v4`, labeler `@v6`)

## [0.1.1] - 2026-07-30

### Changed

- Migrate GoReleaser Docker config from deprecated `dockers` to `dockers_v2` (multi-arch `linux/amd64` + `linux/arm64`)

### Notes

- Tag `v0.1.1` release failed during Docker publish; use `v0.1.2`.

## [0.1.0] - 2026-07-30

### Added

- Initial Jira CSV → Pulse importer (issues + Main Docs via vendored `internal/platemd`)
- Interactive and non-interactive CLI (`cobra` + `huh`)
- Dry-run and continue-on-error modes
- Open-source repository hygiene: CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, issue/PR templates, Dependabot
- CI quality gates: `gofmt`, `go vet`, race tests with coverage, `golangci-lint`, `govulncheck`
- Tag-driven releases: semver tags → test job → GoReleaser (multi-platform archives, deb/rpm/apk, GHCR image)
- GoReleaser snapshot check on every PR; label sync workflow + `.github/labels.yml`
- `pulse-import --version` with release ldflags (version / commit / date)
- `make check`, `make cover`, `make vuln`, `make fmt-check`, `make release-dry`, `make tag-help`

### Changed

- Require Go 1.25+ (fixes `govulncheck` standard-library findings on older toolchains)
- Module / install path: `github.com/try-pulse/pulse-import`
