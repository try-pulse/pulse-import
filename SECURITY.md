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

- Treat `PULSE_ACCESS_TOKEN` as a secret. Never commit it.
- Config may be written to `~/.config/pulse-import/config.yaml`; it contains no token and is created with mode `0600`.
- Resume journals are created with mode `0600` and contain source/target identifiers, but no credentials.
- Imports create real Pulse issues; prefer `--dry-run` when testing against shared workspaces.
