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
		INSTALL_DIR="$$SMOKE_DIR/local-bin"; \
		trap 'rm -f "$$GENERATOR_BIN"; rm -rf "$$SMOKE_DIR"' EXIT; \
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
		printf '==> smoke: generated delete refuses without --force\n'; \
		set +e; "$$SMOKE_DIR/bin/example" widgets delete abc </dev/null >/dev/null 2>&1; \
		code=$$?; set -e; \
		test "$$code" -eq 2 || { echo "delete without --force exited $$code, want 2"; exit 1; }; \
		printf '==> smoke: generated CLI refuses a value the schema rules out\n'; \
		set +e; out=$$("$$SMOKE_DIR/bin/example" echo echo 24h --status bogus </dev/null 2>&1); \
		code=$$?; set -e; \
		test "$$code" -eq 2 || { echo "a bad --status exited $$code, want 2"; exit 1; }; \
		case "$$out" in *"is not one of"*) ;; *) echo "the error should name the rejected value, got: $$out"; exit 1;; esac; \
		case "$$out" in *Usage:*) echo "a rejected value should not print the usage block, got: $$out"; exit 1;; esac; \
		printf '==> smoke: generated CLI refuses an unparseable timestamp\n'; \
		set +e; out=$$("$$SMOKE_DIR/bin/example" echo echo banana </dev/null 2>&1); \
		code=$$?; set -e; \
		test "$$code" -eq 2 || { echo "a bad timestamp exited $$code, want 2"; exit 1; }; \
		case "$$out" in *"is not a timestamp"*) ;; *) echo "the error should name the rejected value, got: $$out"; exit 1;; esac; \
		case "$$out" in *24h*) ;; *) echo "the error should name the accepted forms, got: $$out"; exit 1;; esac

verify: smoke test
