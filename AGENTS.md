# AGENTS.md — pulse-import

Instructions for AI coding agents working in this repository.

## What this is

Go CLI that imports issues into [Pulse](https://trypulse.tech) from external tools.
Module: `github.com/try-pulse/pulse-import`.

**v1 importer:** Jira CSV → Pulse issues + Main Docs (Plate JSON via vendored `internal/platemd`).

## Layout

```
cmd/pulse-import/     # main
internal/cli/         # cobra root + rollback, prompts, wizard, review output, user mapping
internal/cli/tui/     # terminal layout, theme, display-width helpers for interactive UX
internal/importers/   # Importer interface + jiracsv (header/row/parse/doc)
internal/importstate/ # append-only crash-safe resume journal (v2 phase ladder)
internal/runner/      # plan (prepare.go) + execute (execute.go) + mapping (map.go)
internal/pulseapi/    # HTTP client
internal/auth/        # token / config file
internal/jira2md/     # Jira wiki → Markdown (jira2md.go blocks, inline.go marks)
internal/statusmap/   # Jira status/priority/type → Pulse
internal/platemd/     # vendored fork (Markdown ↔ Plate); do not treat as upstream dep
internal/version/     # ldflags-injected Version/Commit/Date
testdata/jira/        # sample.csv (minimal) + all-fields.csv (real export shape)
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

## Pulse API constraints that are easy to get wrong

These are enforced server-side and have each caused a class of import failure.
Do not "simplify" the code that handles them.

1. **Length limits are counted in BYTES.** `pulse-api` validates issue/project
   titles with `len(string) > 200` and label names with `len(display) > 50`.
   Request binding checks rune counts, so a rune-based truncation still 400s for
   non-Latin text. Always use `pulseapi.TruncateForAPI` / `ExceedsAPIBytes`.
2. **A Main Doc must be uploaded as `text/plain`.** Pulse decides whether a
   document opens in the Plate editor from the stored `content_type`; only
   `text/plain` and `application/json` qualify. multipart's
   `CreateFormFile` default (`application/octet-stream`) makes every imported
   description an unopenable file.
3. **An assignee must be a member of the issue's team or a parent team.**
   `GET /teams/:id/members` is the correct roster: unlike `GET /users` it is not
   gated on the workspace-admin `users:read` permission, and unlike
   `GET /users/select` it carries email addresses.
4. **Label names are unique per (team, entity_type) including archived labels.**
   Creating a label whose name is held by an archived one returns 409, so it has
   to be unarchived and reused.
5. **A project carries `title`, not `name`.**
6. **Issue codes are allocated sequentially per team** and the API accepts no
   explicit code, so Jira identifiers can only line up in an empty team. Items
   are created in ascending source-key order for that reason.
7. **Pulse supports one level of sub-issues**, and a sub-issue inherits its
   project and cycle from its parent.
8. **Estimates must be a value in the team's scale** (`GET /teams` returns
   `estimate_settings`); anything else is rejected as `INVALID_ESTIMATE`.
9. **`reporter`, `creator` and `created_at` cannot be set.** They are recorded in
   the Main Doc instead. Do not "fix" this by inventing fields.
10. **`main_doc_id` is returned on both issues and projects**, which is what makes
    an ambiguous Main Doc upload reconcilable instead of producing an orphan
    document on the next run. It requires pulse-api ≥ the commit that added it to
    `ProjectResponse`.
11. **Cycles are leaf-team only, need both dates, and refuse issues once
    completed.** `POST /cycles` requires `start_date` strictly before `end_date`
    and rejects a team that has sub-teams; an issue cannot be assigned to a
    completed cycle. `GET /cycles/team/:id` returns a bare JSON array, and cycle
    names are **not** unique — which is why `ensureCycles` reuses by name
    best-effort and, unlike labels, has no 409 branch.

## Execution invariants

- Items are created in waves — projects, then top-level issues, then sub-issues —
  because wave N+1 needs ids from wave N. Concurrency runs inside a wave only.
- The per-item phase ladder is `creating → created → doc_uploaded → commented →
  linked`. A failed phase must leave the item at the phase it reached; carrying
  on to the next phase would mark it complete and lose the failed work forever.
- Relations are applied in a link pass after every wave, because they reference
  other imported issues.
- The state file identity must never include anything derived from the current
  Pulse contents (user or label lookups). Doing so breaks the documented
  "re-run after users join" flow.

## Out of scope for agents unless asked

- Publishing releases / pushing tags
- Changing license or module path
- Rewriting vendored `internal/platemd` against upstream unless fixing an import bug
