SHELL := /bin/bash

GO                   := GOFLAGS=-mod=vendor GOPRIVATE=github.com/moia-dev go
PACKAGES              = $(shell $(GO) list ./...)
SYSTEM                = $(shell uname -s | tr A-Z a-z)_$(shell uname -m | sed "s/x86_64/amd64/")
GOLANGCI_LINT_VERSION = 1.21.0

golangci-lint:
	curl -sSLf \
		https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(shell echo $(SYSTEM) | tr '_' '-').tar.gz \
		| tar xzOf - golangci-lint-$(GOLANGCI_LINT_VERSION)-$(shell echo $(SYSTEM) | tr '_' '-')/golangci-lint > golangci-lint && chmod +x golangci-lint

.PHONY: lint
lint: golangci-lint
	@./golangci-lint run

.PHONY: test
test:
	$(GO) test -count=1 -cover $(PACKAGES)
