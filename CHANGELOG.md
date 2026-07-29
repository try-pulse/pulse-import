# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-07-30

### Changed

- Migrate GoReleaser Docker config from deprecated `dockers` to `dockers_v2` (multi-arch `linux/amd64` + `linux/arm64`)

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
