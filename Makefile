# The gate (docs/spec.md §9): `make check` runs every step, and CI runs the same. Each step is
# also a target of its own.

GOLANGCI_LINT_VERSION := 2.15.0
GOVULNCHECK_VERSION := v1.9.0

BIN := node_modules/.bin

.PHONY: check gofmt vet lint test vuln tsc oxlint oxfmt bun-test

check: gofmt vet lint test vuln tsc oxlint oxfmt bun-test

gofmt:
	@out="$$(find . -name node_modules -prune -o -name '*.go' -print | xargs -r gofmt -l)"; \
	if [ -n "$$out" ]; then echo "gofmt: these files need formatting:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint:
	@found="$$(golangci-lint version --short 2>/dev/null)"; \
	if [ "$$found" != "$(GOLANGCI_LINT_VERSION)" ]; then \
		echo "lint: golangci-lint $(GOLANGCI_LINT_VERSION) required, found $${found:-none}"; exit 1; \
	fi
	golangci-lint run

test:
	go test -race -shuffle=on ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

node_modules: package.json bun.lock
	bun install --frozen-lockfile
	@touch node_modules

tsc: node_modules
	$(BIN)/tsc -b

oxlint: node_modules
	$(BIN)/oxlint --type-aware

oxfmt: node_modules
	$(BIN)/oxfmt --check

bun-test: node_modules
	bun test --conditions=@tscomfy/source --pass-with-no-tests
