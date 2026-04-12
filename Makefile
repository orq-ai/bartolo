SHELL := /bin/sh
.DEFAULT_GOAL := verify

TEST_ARGS ?=
GENERATOR_BIN := $(CURDIR)/.bartolo-test-bin

.PHONY: test smoke verify

test:
	@echo "==> go test $(TEST_ARGS) ./..."
	@go test $(TEST_ARGS) ./...

smoke:
	@echo "==> smoke: init, generate, tidy, and build a fresh example CLI"
	@set -e; \
		ROOT_DIR="$(CURDIR)"; \
		GENERATOR_BIN="$(GENERATOR_BIN)"; \
		SMOKE_DIR="$$(mktemp -d)"; \
		trap 'rm -f "$$GENERATOR_BIN"; rm -rf "$$SMOKE_DIR"' EXIT; \
		go build -o "$$GENERATOR_BIN" .; \
		cp example-cli/openapi.yaml "$$SMOKE_DIR/openapi.yaml"; \
		cd "$$SMOKE_DIR"; \
		go mod init example >/dev/null 2>&1; \
		go mod edit -replace github.com/orq-ai/bartolo="$$ROOT_DIR"; \
		go mod edit -require github.com/orq-ai/bartolo@v0.0.0; \
		"$$GENERATOR_BIN" init example; \
		"$$GENERATOR_BIN" generate openapi.yaml; \
		go mod tidy >/dev/null 2>&1; \
		go build ./...

verify: smoke test
