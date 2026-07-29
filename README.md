<p align="center">
  <img src="assets/banner.svg" alt="pulse-import — Jira CSV to Pulse" width="820">
</p>

<p align="center">
  <strong>Import issues into <a href="https://trypulse.tech">Pulse</a> from other tools.</strong><br>
  v1: <code>Jira CSV → Pulse issues + Main Docs</code>
</p>

<p align="center">
  <a href="https://github.com/try-pulse/pulse-import/actions/workflows/ci.yml?query=branch%3Amain"><img src="https://github.com/try-pulse/pulse-import/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/try-pulse/pulse-import/releases/latest"><img src="https://img.shields.io/github/v/release/try-pulse/pulse-import?sort=semver&label=release" alt="Release"></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License"></a>
  <a href="https://github.com/try-pulse/pulse-import/pkgs/container/pulse-import"><img src="https://img.shields.io/badge/GHCR-pulse--import-blue?logo=github" alt="GHCR"></a>
</p>

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
| `PULSE_ACCESS_TOKEN` | JWT Bearer token (preferred) |
| `PULSE_API_KEY` | Same token, alternate name |
| `PULSE_WORKSPACE_ID` | Optional default workspace |
| `PULSE_API_URL` | Optional API base · default `https://api.trypulse.tech/api/v1` |

Optional config file: `~/.config/pulse-import/config.yaml`

> [!WARNING]
> Treat the token as a secret. Never commit it.

## Quick start

1. In Jira: **Advanced issue search** → export CSV with **all fields**
2. Run:

```bash
export PULSE_ACCESS_TOKEN="<jwt>"
pulse-import
```

Prompts: workspace · CSV path · Jira URL · team · assignee strategy.

**Safe first pass** (parse + map only):

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
| `--dry-run` | Map only — create nothing |
| `--continue-on-error` | Keep going after a failed issue |
| `--yes` | Non-interactive (no prompts) |
| `--token` | JWT (or use `PULSE_ACCESS_TOKEN`) |
| `--api-url` | Override API base URL |
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

## Important

> [!IMPORTANT]
> Re-running an import creates **duplicate** issues — there is no server-side import id yet. Prefer `--dry-run` on shared workspaces first.

- Export CSV with **all fields** from Jira so mapping has what it needs.

## Help

| | |
|--|--|
| Product | [trypulse.tech](https://trypulse.tech) |
| Issues | [GitHub Issues](https://github.com/try-pulse/pulse-import/issues) |
| Security | [SECURITY.md](SECURITY.md) |
| License | [MIT](LICENSE) |
