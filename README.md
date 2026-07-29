# pulse-import

[![CI](https://github.com/try-pulse/pulse-import/actions/workflows/ci.yml/badge.svg)](https://github.com/try-pulse/pulse-import/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/try-pulse/pulse-import)](https://goreportcard.com/report/github.com/try-pulse/pulse-import)
[![Latest Release](https://img.shields.io/github/v/release/try-pulse/pulse-import?include_prereleases&sort=semver)](https://github.com/try-pulse/pulse-import/releases)

Import issues into [Pulse](https://trypulse.tech) from other tools.

**v1:** Jira CSV → Pulse issues + Main Docs (Plate JSON via vendored `internal/platemd`).

> Re-running an import creates **duplicate** issues. There is no server-side import id yet.

## Install

```bash
go install github.com/try-pulse/pulse-import/cmd/pulse-import@latest
```

Or download a release binary from [GitHub Releases](https://github.com/try-pulse/pulse-import/releases) / build from source:

```bash
git clone https://github.com/try-pulse/pulse-import.git
cd pulse-import
make build
./bin/pulse-import --version
```

Markdown → Plate conversion lives in `internal/platemd` (vendored in-tree) so `go install` works from this module alone.

## Quick start

```bash
export PULSE_ACCESS_TOKEN="<jwt>"   # or PULSE_API_KEY
pulse-import
```

You will be prompted for workspace, CSV path, Jira URL, team, and assignee strategy.

### Non-interactive

```bash
PULSE_ACCESS_TOKEN=… pulse-import \
  --yes \
  --importer jira-csv \
  --file ./jira-export.csv \
  --workspace <workspace-id> \
  --team <team-id-or-name> \
  --jira-url https://acme.atlassian.net \
  --self-assign
```

Useful flags: `--dry-run`, `--continue-on-error`, `--project`, `--api-url`, `--version`.

## Export from Jira

1. Open **Advanced issue search** and filter with JQL (e.g. `project = ENG`).
2. Export as CSV with **all fields**.
3. Run `pulse-import` and select the file.

## Field mapping (Jira CSV → Pulse)

| Jira | Pulse |
|------|-------|
| Summary | Issue title |
| Description | **Main Doc** body (Jira wiki → Markdown → Plate JSON) |
| Issue key | Backlink in Main Doc |
| Priority | `urgent` / `high` / `medium` / `low` / `no_priority` |
| Issue Type | Pulse `type` when mappable + label `Type: …` |
| Labels | Labels (repeated CSV columns supported) |
| Release / Fix Version | Label `Release: …` (multi-value columns supported) |
| Assignee | Matched by name/email when strategy is “mapped” |
| Status | Best-effort map to Pulse workflow |
| Story points | Skipped in v1 (no safe hours conversion) |

Issue `description` in Pulse is left empty (UI suffix only). The canonical writeup is the **Main Doc**, matching how the Pulse app documents issues.

## Architecture

```
Importer (jira-csv, …) → ImportResult → Runner → Pulse API
                                              ↘ MainDoc upload (internal/platemd)
```

Add a new source by implementing `importers.Importer` and registering it in `internal/cli/registry.go`.

## Auth

Paste a token or set an env var:

| Variable | Purpose |
|----------|---------|
| `PULSE_ACCESS_TOKEN` | JWT Bearer token (preferred) |
| `PULSE_API_KEY` | Alias for the same JWT |
| `PULSE_API_URL` | Override API base (default `https://api.trypulse.tech/api/v1`) |
| `PULSE_WORKSPACE_ID` | Default workspace |

Optional config file: `~/.config/pulse-import/config.yaml` (token/workspace persist with a console notice on save failure).

## Development

```bash
make tidy
make check
make build
./bin/pulse-import --help
./bin/pulse-import --version
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for PR expectations, how to add importers, and how maintainers cut releases (`git tag -a vX.Y.Z` → GitHub Actions → GoReleaser).

### CI / release overview

| Trigger | Workflow | What runs |
|---------|----------|-----------|
| Push / PR to `main` | `ci` | fmt, vet, race tests, golangci-lint, govulncheck, GoReleaser snapshot (no publish) |
| Push tag `vX.Y.Z` / `vX.Y.Z-*` | `release` | quality gates, then GoReleaser publish to GitHub Releases |
| Change to `.github/labels.yml` | `labels` | sync issue/PR labels |

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE) — see also [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
