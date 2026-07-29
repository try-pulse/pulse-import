# AGENTS.md — pulse-import

Instructions for AI coding agents working in this repository.

## What this is

Go CLI that imports issues into [Pulse](https://trypulse.tech) from external tools.
Module: `github.com/try-pulse/pulse-import`.

**v1 importer:** Jira CSV → Pulse issues + Main Docs (Plate JSON via vendored `internal/platemd`).

## Layout

```
cmd/pulse-import/     # main
internal/cli/         # cobra root, prompts, importer registry
internal/importers/   # Importer interface + jiracsv
internal/importstate/ # append-only crash-safe resume journal
internal/runner/      # maps ImportResult → Pulse API (+ Main Doc)
internal/pulseapi/    # HTTP client
internal/auth/        # token / config file
internal/jira2md/     # Jira wiki → Markdown
internal/statusmap/   # Jira status → Pulse workflow
internal/platemd/     # vendored fork (Markdown ↔ Plate); do not treat as upstream dep
internal/version/     # ldflags-injected Version/Commit/Date
testdata/             # fixtures
vendor/               # committed modules (required for offline / reproducible go install)
```

## Commands

```bash
make tidy          # go mod tidy && go mod vendor
make test          # race tests
make lint          # golangci-lint (needs binary installed)
make check         # fmt-check + vet + lint + test
make check-full    # check + govulncheck
make build         # ./bin/pulse-import with version ldflags
make release-dry   # goreleaser snapshot (no publish)
make tag-help      # how to cut a GitHub release
```

Always pass `-mod=vendor` (Makefile / CI already do). After dependency changes, commit `vendor/`.

Releases: annotated tag `vX.Y.Z` (or `vX.Y.Z-rc.1`) on `main` → `.github/workflows/release.yml` → GoReleaser. Do not hand-craft GitHub Releases.

## Conventions

- Add importers by implementing `importers.Importer` and registering in `internal/cli/registry.go`.
- Prefer table-driven tests; keep fixtures under `testdata/`.
- Never commit secrets or real `.env` values; use `.env.example`.
- User-facing changes: update `README.md` and `CHANGELOG.md` (Unreleased).
- Conventional commits preferred (`feat:`, `fix:`, `docs:`, `chore:`, `ci:`, `test:`).
- Do not create feature branches unless the human asks; commit on the current branch when asked to commit.

## Auth / API

Env vars: `PULSE_ACCESS_TOKEN`, optional `PULSE_API_URL`, `PULSE_WORKSPACE_ID`.
Default API: `https://api.trypulse.tech/api/v1`. Config file: `~/.config/pulse-import/config.yaml`
stores only non-secret API/workspace defaults; tokens are accepted only from
`PULSE_ACCESS_TOKEN` or an interactive prompt.

Writes use a JSONL state journal for resume. Ambiguous creates must be resolved
with `--adopt` or explicitly retried with `--retry-unknown`; never add heuristic
title/search deduplication.

## Out of scope for agents unless asked

- Publishing releases / pushing tags
- Changing license or module path
- Rewriting vendored `internal/platemd` against upstream unless fixing an import bug
