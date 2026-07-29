.PHONY: build test vet lint tidy fmt fmt-check cover vuln check check-full release-dry tag-help

MODULE := github.com/try-pulse/pulse-import
# Prefer tag (v0.1.0); strip leading v to match GoReleaser's {{.Version}}.
# Never leave VERSION empty — cobra omits --version when Version == "".
GIT_DESC := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION  ?= $(if $(GIT_DESC),$(GIT_DESC),dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

build:
	go build -mod=vendor -ldflags "$(LDFLAGS)" -o bin/pulse-import ./cmd/pulse-import

test:
	go test -mod=vendor -race -count=1 ./...

cover:
	go test -mod=vendor -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vet:
	go vet -mod=vendor ./...

fmt:
	gofmt -w $(shell go list -f '{{.Dir}}' ./...)

fmt-check:
	@dirs=$$(go list -f '{{.Dir}}' ./...); \
	test -z "$$(gofmt -l $$dirs)" || (echo "gofmt needed:" && gofmt -l $$dirs && exit 1)

tidy:
	go mod tidy
	go mod vendor

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...

check: fmt-check vet lint test

check-full: check vuln

# Local dry-run of the release pipeline (no GitHub publish).
release-dry:
	GOFLAGS=-mod=vendor goreleaser release --snapshot --clean --skip=publish

# Print the exact commands to cut a release (does not tag/push).
tag-help:
	@echo "1. Update CHANGELOG.md (move Unreleased → [X.Y.Z] - $$(date -u +%Y-%m-%d))"
	@echo "2. Commit on main"
	@echo "3. Tag + push (semver, leading v required):"
	@echo "     git tag -a vX.Y.Z -m \"vX.Y.Z\""
	@echo "     git push origin vX.Y.Z"
	@echo "4. GitHub Actions workflow 'release' runs tests then GoReleaser"
	@echo "5. Assets appear under https://github.com/try-pulse/pulse-import/releases"
