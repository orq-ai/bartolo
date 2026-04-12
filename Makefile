SHELL := /bin/sh
.DEFAULT_GOAL := verify

TEST_ARGS ?=
GENERATOR_BIN := $(CURDIR)/.bartolo-test-bin

.PHONY: test smoke verify

test:
	@echo "==> go test $(TEST_ARGS) ./..."
	@go test $(TEST_ARGS) ./...

smoke:
	@echo "==> smoke: init, generate, and verify a fresh example CLI"
	@set -e; \
		ROOT_DIR="$(CURDIR)"; \
		GENERATOR_BIN="$(GENERATOR_BIN)"; \
		SMOKE_DIR="$$(mktemp -d)"; \
		INSTALL_DIR="$$SMOKE_DIR/local-bin"; \
		trap 'rm -f "$$GENERATOR_BIN"; rm -rf "$$SMOKE_DIR"' EXIT; \
		go build -o "$$GENERATOR_BIN" .; \
		cp example-cli/openapi.yaml "$$SMOKE_DIR/openapi.yaml"; \
		cd "$$SMOKE_DIR"; \
		"$$GENERATOR_BIN" init example --bartolo-path "$$ROOT_DIR"; \
		"$$GENERATOR_BIN" generate openapi.yaml; \
		go build ./...; \
		make build >/dev/null; \
		INSTALL_DIR="$$INSTALL_DIR" make install-local >/dev/null; \
		test -x "$$SMOKE_DIR/bin/example"; \
		test -x "$$INSTALL_DIR/example"

verify: smoke test
