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
		SMOKE_ROOT="$$ROOT_DIR/.tmp-smoke"; \
		mkdir -p "$$SMOKE_ROOT"; \
		SMOKE_DIR="$$(mktemp -d "$$SMOKE_ROOT/cli.XXXXXX")"; \
		MCP_DIR="$$(mktemp -d "$$SMOKE_ROOT/mcp.XXXXXX")"; \
		INSTALL_DIR="$$SMOKE_DIR/local-bin"; \
		trap 'rm -f "$$GENERATOR_BIN"; rm -rf "$$SMOKE_DIR" "$$MCP_DIR"' EXIT; \
		go build -o "$$GENERATOR_BIN" .; \
		cp example-cli/openapi.yaml "$$SMOKE_DIR/openapi.yaml"; \
		cd "$$SMOKE_DIR"; \
		"$$GENERATOR_BIN" init example --bartolo-path "$$ROOT_DIR"; \
		"$$GENERATOR_BIN" generate openapi.yaml; \
		"$$GENERATOR_BIN" sync openapi.yaml; \
		go build ./...; \
		make build >/dev/null; \
		INSTALL_DIR="$$INSTALL_DIR" make install-local >/dev/null; \
		make completions >/dev/null; \
		test -f "$$SMOKE_DIR/cmd/example/main.go"; \
		test -f "$$SMOKE_DIR/cli/generated/register.go"; \
		test -f "$$SMOKE_DIR/cli/custom/register.go"; \
		test -f "$$SMOKE_DIR/examples/README.md"; \
		test -f "$$SMOKE_DIR/.gitignore"; \
		test -f "$$SMOKE_DIR/.editorconfig"; \
		test -f "$$SMOKE_DIR/.gitattributes"; \
		test -f "$$SMOKE_DIR/.env.example"; \
		test -f "$$SMOKE_DIR/completions/example.bash"; \
		test -f "$$SMOKE_DIR/completions/_example"; \
		test -f "$$SMOKE_DIR/completions/example.fish"; \
		test -f "$$SMOKE_DIR/completions/example.ps1"; \
		test -x "$$SMOKE_DIR/scripts/build.sh"; \
		test -x "$$SMOKE_DIR/scripts/install-local.sh"; \
		test -x "$$SMOKE_DIR/bin/example"; \
		test -x "$$INSTALL_DIR/example"; \
		test -f "$$SMOKE_DIR/cli/generated/mcp_tools.go"; \
		"$$SMOKE_DIR/bin/example" mcp list | grep -q '"inputSchema"'; \
		{ printf '%s\n' \
			'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
			'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
			'{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; sleep 2; } \
			| "$$SMOKE_DIR/bin/example" mcp 2>/dev/null | grep -q '"tools"'; \
		echo "==> smoke: mcp-only target (--targets mcp)"; \
		cp "$$ROOT_DIR/example-cli/openapi.yaml" "$$MCP_DIR/openapi.yaml"; \
		cd "$$MCP_DIR"; \
		"$$GENERATOR_BIN" init mcponly --bartolo-path "$$ROOT_DIR" --targets mcp; \
		"$$GENERATOR_BIN" generate openapi.yaml; \
		go build ./...; \
		test -f "$$MCP_DIR/cli/generated/mcp_tools.go"; \
		test ! -f "$$MCP_DIR/cli/generated/root_commands.go"; \
		go build -o "$$MCP_DIR/bin/mcponly" ./cmd/mcponly; \
		{ printf '%s\n' \
			'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
			'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
			'{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; sleep 2; } \
			| "$$MCP_DIR/bin/mcponly" 2>/dev/null | grep -q '"tools"'

verify: smoke test
