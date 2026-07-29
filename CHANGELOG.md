# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
