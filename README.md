<p align="center">
  <img src="assets/banner.png" alt="pulse-import — Jira CSV to Pulse" width="820">
</p>

<p align="center">
  <strong>Import issues into <a href="https://trypulse.tech">Pulse</a> from other tools.</strong><br>
  v1: <code>Jira CSV → Pulse issues + Main Docs</code>
</p>

<table align="center">
  <tr>
    <td><a href="https://github.com/try-pulse/pulse-import/actions/workflows/ci.yml?query=branch%3Amain"><img src="https://github.com/try-pulse/pulse-import/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a></td>
    <td><a href="https://github.com/try-pulse/pulse-import/releases/latest"><img src="https://img.shields.io/github/v/release/try-pulse/pulse-import?sort=semver&label=release" alt="Release"></a></td>
    <td><a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.25.8+-00ADD8?logo=go&logoColor=white" alt="Go"></a></td>
    <td><a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License"></a></td>
    <td><a href="https://github.com/try-pulse/pulse-import/pkgs/container/pulse-import"><img src="https://img.shields.io/badge/GHCR-pulse--import-blue?logo=github" alt="GHCR"></a></td>
  </tr>
</table>

---

## Install

<details open>
<summary><strong>Release binary</strong> (recommended)</summary>

Download for your OS from [GitHub Releases](https://github.com/try-pulse/pulse-import/releases/latest), then:

```bash
chmod +x pulse-import && sudo mv pulse-import /usr/local/bin/
pulse-import --version
```

Also published: `deb` / `rpm` / `apk` packages on each release.

</details>

<details>
<summary><strong>Go</strong></summary>

```bash
go install github.com/try-pulse/pulse-import/cmd/pulse-import@latest
```

</details>

<details>
<summary><strong>Docker</strong></summary>

```bash
docker pull ghcr.io/try-pulse/pulse-import:latest
docker run --rm -e PULSE_ACCESS_TOKEN -v "$PWD:/data" \
  ghcr.io/try-pulse/pulse-import:latest --help
```

</details>

## Authenticate

| Variable | Purpose |
|----------|---------|
| `PULSE_ACCESS_TOKEN` | JWT Bearer token |
| `PULSE_WORKSPACE_ID` | Optional default workspace |
| `PULSE_API_URL` | Optional API base · default `https://api.trypulse.tech/api/v1` |

Optional config file: `~/.config/pulse-import/config.yaml`. It stores only the
API URL and workspace ID; access tokens are never saved.

> [!WARNING]
> Treat the token as a secret. The CLI accepts it only through
> `PULSE_ACCESS_TOKEN`; it is never persisted or accepted as a command-line flag.

## Quick start

1. In Jira: **Advanced issue search** → **Export Excel CSV (all fields)**
2. Run:

```bash
export PULSE_ACCESS_TOKEN="<jwt>"
pulse-import
```

Prompts: workspace · CSV path · Jira URL · team · project · assignee strategy ·
user mapping for names that did not match · final preflight confirmation.

**Safe first pass** — parses, validates, and prints the full write plan and every
mapping decision. Creates nothing:

```bash
pulse-import --dry-run
```

### Non-interactive

```bash
PULSE_ACCESS_TOKEN=… pulse-import \
  --yes \
  --importer jira-csv \
  --file ./jira-export.csv \
  --workspace <workspace-id> \
  --team <team-id-or-name> \
  --jira-url https://acme.atlassian.net
```

### Undo an import

Every run writes a state journal recording exactly what it created. `rollback`
deletes precisely that and nothing else:

```bash
pulse-import rollback --state-file ./jira-export.csv.pulse-import.state.jsonl
```

Labels are never deleted — they may already be attached to work that was not
imported.

## Options

| Flag | Description |
|------|-------------|
| `--importer` | Importer id (`jira-csv`) |
| `--file` | Path to the export CSV |
| `--workspace` | Workspace ID |
| `--team` | Target team (id or name) |
| `--project` | Pin every issue to one project (id or name); disables epic→project mapping |
| `--jira-url` | Jira Cloud or on-prem base URL |
| `--assignee` | `mapped` (default) · `self` · `none` |
| `--self-assign` | Shorthand for `--assignee self` |
| `--map-user` | `--map-user "Jane Doe=<pulse-user-id\|email\|skip>"` (repeatable) |
| `--epics` | `project` (default) or `label` |
| `--skip-comments` | Do not import comments |
| `--skip-labels` | Do not create or attach labels |
| `--skip-relations` | Do not import blocks / blocked-by links |
| `--strict-labels` | Fail instead of dropping labels past Pulse's limit of 10 per issue |
| `--no-migrated-label` | Do not add the `Migrated` label |
| `--skip-status` | Skip issues mapping to these Pulse statuses (comma separated) |
| `--only-status` | Import only issues mapping to these Pulse statuses |
| `--skip-stale` | Skip issues not updated within N days (or a duration like `4320h`) |
| `--concurrency` | Parallel Pulse writes (default 4; `1` disables parallelism) |
| `--dry-run` | Run the complete preflight; create nothing |
| `--continue-on-error` | Continue after a definitive per-item failure |
| `--yes` | Non-interactive (no prompts) |
| `--api-url` | Override the API base URL |
| `--state-file` | Resume journal path; defaults beside the CSV |
| `--adopt KEY=ID` | Resolve an unknown create with an existing Pulse entity |
| `--retry-unknown KEY` | Explicitly retry an unknown create; may duplicate |
| `-v` / `--version` | Print version |

## Field mapping

| Jira | Pulse |
|------|-------|
| Summary | Issue title |
| Description | **Main Doc** (Jira wiki → Markdown → editor JSON) |
| Issue type | Pulse `type` when mappable, plus a `Type: …` label |
| Epic | **Project**, with its issues filed into it (`--epics label` for a label instead) |
| Parent / Sub-task | Parent / sub-issue (Pulse allows one level; deeper nesting is flattened onto the top-most ancestor) |
| Status | Pulse workflow status, best effort |
| Resolution | Forces `done` when a resolution is set but the status does not say so |
| Priority | `urgent` · `high` · `medium` · `low` · `no_priority` |
| Assignee | Matched against the target team's members by email, then name |
| Labels | Labels |
| Component/s | Label `Component: …` |
| Fix Version/s | Label `Release: …` |
| Affects Version/s | Label `Affects: …` |
| Sprint | Label `Sprint: …` |
| Comments | Comments, prefixed with the original author and date |
| Due date | Due date |
| Story points / Original estimate | Estimate, snapped to the team's estimate scale |
| Blocks / is blocked by | Blocking relations, applied after every issue exists |
| Attachments | Links to the original files, in the Main Doc |
| Issue key, Reporter, Creator, Created, Updated, Resolved, Environment, Time spent | Recorded in the Main Doc |
| — | `Migrated` label on everything the import created |

Issue `description` in Pulse stays empty (it is a short UI suffix, not a body).
The write-up is the **Main Doc**.

Anything the export carries that Pulse has no field for is listed in the plan
under *Not imported*, so nothing is dropped silently.

### What Pulse cannot store

`Reporter`, `Creator` and the original `Created` date cannot be set through the
API — Pulse stamps them from the access token and the clock. They are written
into the Main Doc instead, so the information survives the migration even though
the fields cannot.

## Matching Jira identifiers

Pulse allocates issue codes sequentially per team and the API accepts no explicit
code, so identifiers can only line up when the target team starts empty. The
importer creates issues in ascending source-key order to make that work, and the
plan warns when the team already holds issues.

## Safe resume

> [!IMPORTANT]
> Pulse does not provide a server-side import id. `pulse-import` therefore
> writes a crash-safe JSONL journal beside the CSV before every create request.
> Re-running with the same CSV and target resumes completed work instead of
> creating it again — including after Jira users have joined your Pulse
> workspace, which is the supported way to improve user matching.

If a connection fails while creating an issue, Pulse may have accepted the
request even though the CLI received no response. The item is marked `unknown`
and automatic retry stops:

```bash
# After checking the issue in Pulse:
pulse-import ... --adopt ENG-123=64f... --yes

# Only when you confirmed no issue exists:
pulse-import ... --retry-unknown ENG-123 --yes
```

Changing the source file or the target workspace/team/project requires a
different state file.

- Export the CSV with **all fields** from Jira so mapping has what it needs.
- Jira issue keys must be present and unique.
- Pulse allows at most 10 labels per issue; extra labels are dropped least-first
  with a warning (`--strict-labels` fails instead).
- Creating labels needs team-manager or workspace-admin rights in Pulse; use
  `--skip-labels` if you do not have them.
- Main Doc, comment and link failures are reported and produce a non-zero exit.

## Help

| | |
|--|--|
| Product | [trypulse.tech](https://trypulse.tech) |
| Issues | [GitHub Issues](https://github.com/try-pulse/pulse-import/issues) |
| Security | [SECURITY.md](SECURITY.md) |
| License | [MIT](LICENSE) |
