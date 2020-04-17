SHELL := /bin/bash

include moia-mk-templates/go.mk

PACKAGES              = $(shell $(GO) list ./...)

.PHONY: test
test:
	$(GO) test -count=1 -cover $(PACKAGES)
