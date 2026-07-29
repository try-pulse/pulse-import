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

1. In Jira: **Advanced issue search** → export CSV with **all fields**
2. Run:

```bash
export PULSE_ACCESS_TOKEN="<jwt>"
pulse-import
```

Prompts: workspace · CSV path · Jira URL · team · assignee strategy · final
preflight confirmation.

**Safe first pass** (parse, validate, and read current Pulse mappings; no API writes):

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
  --jira-url https://acme.atlassian.net \
  --self-assign
```

## Options

| Flag | Description |
|------|-------------|
| `--importer` | Importer id (`jira-csv`) |
| `--file` | Path to export CSV |
| `--workspace` | Workspace ID |
| `--team` | Target team (id or name) |
| `--project` | Optional project (id or name) |
| `--jira-url` | Jira Cloud / on-prem base URL |
| `--self-assign` | Assign all imported issues to you |
| `--dry-run` | Run complete preflight; create nothing |
| `--continue-on-error` | Continue after definitive item failures |
| `--yes` | Non-interactive (no prompts) |
| `--api-url` | Override API base URL |
| `--state-file` | Resume journal path; defaults beside the CSV |
| `--adopt KEY=ISSUE_ID` | Resolve an unknown create with an existing Pulse issue |
| `--retry-unknown KEY` | Explicitly retry an unknown create; may duplicate |
| `-v` / `--version` | Print version |

## Field mapping

| Jira | Pulse |
|------|-------|
| Summary | Issue title |
| Description | **Main Doc** (Jira wiki → Markdown → editor JSON) |
| Issue key | Backlink in the Main Doc |
| Priority | `urgent` · `high` · `medium` · `low` · `no_priority` |
| Issue Type | Pulse `type` when mappable + label `Type: …` |
| Labels | Labels (repeated CSV columns OK) |
| Release / Fix Version | Label `Release: …` |
| Assignee | Matched by name/email when strategy is “mapped” |
| Status | Best-effort map to Pulse workflow |
| Story points | Skipped in v1 |

Issue `description` in Pulse stays empty (UI chrome). The write-up is the **Main Doc**.

## Safe resume

> [!IMPORTANT]
> Pulse does not currently provide a server-side import id. `pulse-import`
> therefore writes a crash-safe JSONL journal beside the CSV before every
> create request. Re-running with the same CSV and target resumes completed
> work instead of creating it again.

If a connection fails while creating an issue, Pulse may have accepted the
request even though the CLI received no response. The item is marked `unknown`
and automatic retry stops:

```bash
# After checking the issue in Pulse:
pulse-import ... --adopt ENG-123=64f... --yes

# Only when you confirmed no issue exists:
pulse-import ... --retry-unknown ENG-123 --yes
```

Changing the source file, target workspace/team/project, or mapping options
requires a different state file. Imports created by versions without a journal
cannot be deduplicated reliably.

- Export CSV with **all fields** from Jira so mapping has what it needs.
- Jira issue keys must be present and unique.
- Pulse allows at most 10 labels per issue; preflight fails rather than dropping extras.
- Main Doc failures are reported as failures and produce a non-zero exit status.

## Help

| | |
|--|--|
| Product | [trypulse.tech](https://trypulse.tech) |
| Issues | [GitHub Issues](https://github.com/try-pulse/pulse-import/issues) |
| Security | [SECURITY.md](SECURITY.md) |
| License | [MIT](LICENSE) |
