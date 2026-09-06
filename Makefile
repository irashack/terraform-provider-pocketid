# Local checks and CI use the same entry points. No target deploys to a live instance.
BINARY_NAME := terraform-provider-pocketid
POCKETID_VERSION ?= 2.14.0
GO := go
LINT := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
DOCS := $(GO) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0

.DEFAULT_GOAL := help
.PHONY: help build test test-coverage fmt fmt-check vet lint check docs docs-check test-acc test-acc-matrix test-acc-provider vuln actionlint release-check clean
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "%-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build a development binary in bin/ (does not install it)
	$(GO) build -o bin/$(BINARY_NAME) .

test: ## Run unit tests with the race detector
	$(GO) test -race ./internal/...

test-coverage: ## Write a local unit-coverage report
	$(GO) test -race -coverprofile=coverage.out ./internal/...

fmt: ## Format Go source
	gofmt -w internal main.go

fmt-check: ## Require formatted Go source
	@test -z "$$(gofmt -l internal main.go)" || { gofmt -l internal main.go; exit 1; }

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run pinned golangci-lint (Go downloads it on first use)
	$(LINT) run ./...

check: fmt-check vet test build lint ## Run the core checks used by CI

docs: ## Generate resource and provider documentation
	$(DOCS) generate --provider-name=terraform-provider-pocketid

docs-check: docs ## Require generated docs to match the checkout
	git diff --exit-code -- docs
	@test -z "$$(git ls-files --others --exclude-standard docs)"

test-acc: ## Run client acceptance on one disposable official image
	python3 scripts/disposable-pocketid.py $(POCKETID_VERSION) -- $(GO) test -v -count=1 -timeout 15m ./internal/provider -tags=acc -run '^TestAccResourceClient'

test-acc-matrix: ## Run client acceptance on the tested Pocket ID versions
	@for version in 2.9.0 2.13.0 2.14.0; do $(MAKE) test-acc POCKETID_VERSION=$$version || exit $$?; done

test-acc-provider: ## Run broader 2.14 acceptance; application-config failures remain tracked
	python3 scripts/disposable-pocketid.py 2.14.0 -- $(GO) test -v -count=1 -timeout 20m ./internal/provider -tags=acc -skip '^TestAccResourceApplicationConfig'

vuln: ## Check reachable Go vulnerabilities
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

actionlint: ## Validate GitHub Actions syntax and expressions
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

release-check: ## Validate GoReleaser configuration (requires GoReleaser 2.18.1)
	goreleaser check

clean: ## Remove local build and coverage output
	rm -rf bin coverage.out
