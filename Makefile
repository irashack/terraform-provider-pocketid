# Local checks and CI use the same entry points. No target deploys to a live instance.
BINARY_NAME := terraform-provider-pocketid
POCKETID_VERSION ?= 2.17.0
GO := go
LINT := $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
DOCS := $(GO) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0

.DEFAULT_GOAL := help
.PHONY: help build test test-scripts test-coverage fmt fmt-check vet lint check docs docs-check test-acc test-acc-matrix test-acc-provider test-acc-supported vuln actionlint release-check clean
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "%-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build a development binary in bin/ (does not install it)
	$(GO) build -o bin/$(BINARY_NAME) .

test: ## Run unit tests with the race detector
	$(GO) test -race . ./internal/...

test-scripts: ## Run the fixture script's unit tests (no container)
	python3 -m unittest discover -s scripts -p 'test_*.py'

test-coverage: ## Write a local unit-coverage report
	$(GO) test -race -coverprofile=coverage.out . ./internal/...

fmt: ## Format Go source
	gofmt -w internal main.go

fmt-check: ## Require formatted Go source
	@test -z "$$(gofmt -l internal main.go)" || { gofmt -l internal main.go; exit 1; }

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run pinned golangci-lint (Go downloads it on first use)
	$(LINT) run ./...

check: fmt-check vet test test-scripts build lint ## Run the core checks used by CI

docs: ## Generate resource and provider documentation
	$(DOCS) generate --provider-name=terraform-provider-pocketid

docs-check: docs ## Require generated docs to match the checkout
	git diff --exit-code -- docs
	@test -z "$$(git ls-files --others --exclude-standard docs)"

# Acceptance packages share one fixture, so they run one after another (-p 1):
# a client created by one package could otherwise crowd another's list reads.
ACC_PACKAGES := ./internal/provider ./internal/datasources

test-acc: ## Run client (resource and data sources), application-config and API-contract acceptance on one disposable official image
	python3 scripts/disposable-pocketid.py $(POCKETID_VERSION) -- $(GO) test -v -count=1 -p 1 -timeout 15m $(ACC_PACKAGES) -tags=acc -run '^TestAcc(Resource(Client|ApplicationConfig)|ClientDataSources|API_)'

test-acc-matrix: ## Run test-acc (client, application-config and API-contract acceptance) on every fixture version
	@for version in 2.14.0 2.15.0 2.16.0 2.17.0; do $(MAKE) test-acc POCKETID_VERSION=$$version || exit $$?; done

test-acc-provider: ## Run the full provider and data-source acceptance suites on one disposable official image
	python3 scripts/disposable-pocketid.py $(POCKETID_VERSION) -- $(GO) test -v -count=1 -p 1 -timeout 20m $(ACC_PACKAGES) -tags=acc

# The supported servers, 2.14.0 through 2.17.0: test-acc-supported runs the
# full provider and data-source suites on each. CI runs the full suites on
# 2.16.0 and 2.17.0 and test-acc on 2.14.0 and 2.15.0 to keep its run time
# down; run this target before a release.
SUPPORTED_POCKETID_VERSIONS := 2.14.0 2.15.0 2.16.0 2.17.0

test-acc-supported: ## Run test-acc-provider on every supported Pocket ID version (2.14.0 to 2.17.0)
	@for version in $(SUPPORTED_POCKETID_VERSIONS); do $(MAKE) test-acc-provider POCKETID_VERSION=$$version || exit $$?; done

vuln: ## Check reachable Go vulnerabilities
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

actionlint: ## Validate GitHub Actions syntax and expressions
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

release-check: ## Validate GoReleaser configuration (requires GoReleaser 2.18.1)
	goreleaser check

clean: ## Remove local build and coverage output
	rm -rf bin coverage.out
