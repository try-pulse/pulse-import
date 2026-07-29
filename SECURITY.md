# Security Policy

## Supported versions

Security fixes are applied to the latest release on `main` and the most recent
tagged `v*` release. Older tags are generally not patched.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security problems.

Prefer one of:

1. [GitHub private vulnerability reporting](https://github.com/try-pulse/pulse-import/security/advisories/new) (recommended)
2. Email [security@trypulse.tech](mailto:security@trypulse.tech) with a clear description and reproduction steps

Include:

- Affected version (`pulse-import --version`) or commit SHA
- Impact (token leak, unexpected API calls, path traversal, etc.)
- Proof of concept or steps to reproduce (no production credentials)

You should receive an acknowledgement within a few business days. We will
coordinate disclosure once a fix is available.

## Scope notes for this CLI

- Treat `PULSE_ACCESS_TOKEN` / `PULSE_API_KEY` as secrets. Never commit them.
- Config may be written to `~/.config/pulse-import/config.yaml` — protect that file.
- Imports create real Pulse issues; prefer `--dry-run` when testing against shared workspaces.
