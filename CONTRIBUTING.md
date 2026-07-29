# Contributing to pulse-import

Thanks for helping improve Pulse’s import CLI.

## Development setup

Requirements: Go 1.25+, `make`, and optionally [golangci-lint](https://golangci-lint.run/).

```bash
git clone https://github.com/try-pulse/pulse-import.git
cd pulse-import
make tidy
make test
make build
./bin/pulse-import --help
```

Useful targets:

| Target | What it does |
|--------|----------------|
| `make build` | Build `./bin/pulse-import` |
| `make test` | Race-detector tests |
| `make lint` | golangci-lint |
| `make vet` | `go vet` |
| `make fmt` | `gofmt -w` |
| `make tidy` | `go mod tidy` + `go mod vendor` |
| `make check` | fmt check + vet + lint + test |
| `make check-full` | `make check` + `govulncheck` |
| `make vuln` | `govulncheck` |
| `make release-dry` | GoReleaser snapshot (no publish) |
| `make tag-help` | Print release tagging commands |

This repo vendors dependencies (`vendor/`). After changing `go.mod`, run `make tidy` and commit `go.mod`, `go.sum`, and `vendor/`.

## Releases (maintainers)

Releases are **tag-driven** via GitHub Actions + [GoReleaser](https://goreleaser.com/).

1. Ensure `main` is green (workflow **ci**).
2. Update `CHANGELOG.md`: move items from **Unreleased** into a dated `[X.Y.Z]` section.
3. Commit the changelog on `main` and push.
4. Create an **annotated** semver tag and push it (leading `v` is required):

   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

   Pre-releases use a suffix, e.g. `v0.1.0-rc.1` (workflow matches `v*.*.*` and `v*.*.*-*`).

5. Workflow **release** will:
   - run format / vet / race tests / golangci-lint
   - run GoReleaser → GitHub Release `vX.Y.Z` with binaries for linux/darwin/windows × amd64/arm64, `checksums.txt`, and changelog

Do **not** create GitHub Releases by hand. Do **not** force-push tags that already published artifacts.

Local dry-run (no GitHub upload):

```bash
make release-dry
```

## Adding an importer

1. Implement `importers.Importer` under `internal/importers/<name>/`.
2. Register it in `internal/cli/registry.go`.
3. Add table-driven tests and a small fixture under `testdata/` when useful.
4. Document field mapping and flags in `README.md`.

## Pull requests

- Keep PRs focused and small when possible.
- Prefer [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `chore:`, `test:`, `ci:`).
- Include tests for behavioral changes.
- Run `make check` locally before opening a PR.
- Update `CHANGELOG.md` under **Unreleased** for user-facing changes.
- Do not commit secrets, real `.env` files, or live API tokens.

## Reporting bugs

Use the Bug report issue template. Include:

- `pulse-import --version` output
- OS / Go version (if building from source)
- Exact command (redact tokens)
- Expected vs actual behavior

## Security

Do not open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).

## Code of conduct

Please follow the [Code of Conduct](CODE_OF_CONDUCT.md).
