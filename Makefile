GO      ?= go
GOBIN   ?= $(shell $(GO) env GOPATH)/bin
PKGS    := ./...
LAB_ADDR ?= 127.0.0.1:8089

.PHONY: all build test lint fmt lab e2e tidy clean

all: lint test build

build:
	$(GO) build $(PKGS)

test:
	$(GO) vet $(PKGS)
	$(GO) test -race -count=1 $(PKGS)

## lint: gofmt + goimports + vet (install goimports with: go install golang.org/x/tools/cmd/goimports@latest)
lint:
	@test -z "$$(gofmt -l . | grep -v '\.pb\.go$$')" || { echo "gofmt needed on:"; gofmt -l . | grep -v '\.pb\.go$$'; exit 1; }
	@test -z "$$($(GOBIN)/goimports -l . 2>/dev/null | grep -v '\.pb\.go$$')" || { echo "goimports needed on:"; $(GOBIN)/goimports -l . | grep -v '\.pb\.go$$'; exit 1; }
	$(GO) vet $(PKGS)

fmt:
	gofmt -w .
	$(GOBIN)/goimports -w .

## lab: run the local mock-app lab
lab:
	$(GO) run ./cmd/authlab -addr $(LAB_ADDR)

## e2e: browser end-to-end tests against the lab (needs Chrome)
e2e:
	AGENTBROWSER_E2E=1 $(GO) test -count=1 -timeout 20m -run 'E2E' ./e2e/...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin/ dist/
