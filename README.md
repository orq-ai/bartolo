# Bartolo

Bartolo turns an OpenAPI schema into a production-ready Go CLI.

It is designed for teams that want more than a thin REST wrapper. The generated CLIs are readable for humans, stable for agents, and practical to ship inside real product workflows.

## Why Bartolo

- Generates grouped commands from tags or `x-bartolo-group`, so large APIs feel like product CLIs instead of flat path dumps.
- Supports `openapi.yaml`, `openapi.yml`, and `openapi.json`.
- Handles common OpenAPI 3.1 schema shapes in addition to OpenAPI 3.0.
- Infers API key and bearer auth, including predictable env vars like `MY_CLI_API_KEY`.
- Ships generated CLIs with built-in `doctor`, `request`, and `default-format` commands.
- Persists config with Viper and exposes machine-friendly JSON output by default.
- Produces per-CLI README docs so downstream consumers immediately see auth, first-run, and grouped command flows.

## Quickstart

Install Bartolo:

```sh
go install github.com/orq-ai/bartolo@latest
```

If `bartolo` is not found afterwards, Go most likely installed it into
`$(go env GOBIN)` or, when `GOBIN` is unset, `$(go env GOPATH)/bin`.

For `zsh`, add Go's bin directory to your shell config:

```sh
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

Create a new generated CLI:

```sh
mkdir my-cli
cd my-cli

# Interactive: asks for the CLI name and default output format
bartolo init

# Or fully scripted
bartolo init my-cli --default-format json

# Generate from either YAML or JSON
bartolo generate openapi.yaml
# or
bartolo generate openapi.json

go mod tidy
go build -o my-cli
./my-cli --json doctor
./my-cli --help
```

Set a default output format for the generated CLI:

```sh
./my-cli default-format yaml
```

## What You Get

Every generated CLI starts with a useful operator surface:

- `doctor` shows config, auth source, and selected server.
- `request` provides a raw escape hatch for unmodeled endpoints.
- `default-format` shows or persists the preferred default output format.
- `--json`, `--output-format`, and `--query` make automation and projection straightforward.
- Grouped nouns like `prompts`, `files`, or `human-evals` feel closer to a product CLI than a path translator.

## MCP

Every generated CLI is also a [Model Context Protocol](https://modelcontextprotocol.io) server. Running `generate` emits an `mcp` subcommand that exposes the API's operations as MCP tools for clients such as Claude Code, Claude Desktop, and Cursor.

What gets generated:

- An `mcp` subcommand on the CLI (`my-cli mcp`) that serves over stdio by default.
- One tool per eligible operation, named `<group>_<leaf>` (or just the leaf for ungrouped ops).
- A JSON Schema (2020-12) input schema per tool, derived from the operation's path/query/header params and JSON request body, plus an output schema from the first 2xx JSON response.
- Results returned as `structuredContent` (plus a JSON text block). Upstream API errors surface as `isError` tool results with a structured `{status, body}` payload rather than protocol errors.
- Tool annotations derived from the HTTP method: `readOnlyHint` for GET/HEAD, `destructiveHint` for DELETE, `idempotentHint` for GET/HEAD/PUT/DELETE.
- Outbound auth reuses the CLI's existing runtime, so the same env vars and profiles authenticate MCP tool calls.

Inspect the tool manifest without a client:

```sh
my-cli mcp list
```

### stdio quickstart

Add the server to Claude Code:

```sh
claude mcp add my-cli -- my-cli mcp
```

Or configure Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "my-cli": {
      "command": "my-cli",
      "args": ["mcp"],
      "env": {
        "MY_CLI_API_KEY": "your-token"
      }
    }
  }
}
```

Replace `MY_CLI_API_KEY` with the auth env var your CLI reports under `my-cli doctor`.

### HTTP mode

Serve over Streamable HTTP instead of stdio:

```sh
my-cli mcp --http 127.0.0.1:8484
```

Inbound auth for HTTP is selected with `--http-auth`:

- `none` (default) — allowed only on a loopback address unless `--http-insecure` is set.
- `bearer` — a static token read from the `<PREFIX>_MCP_TOKEN` env var, compared in constant time.
- `oauth` — an OAuth 2.1 resource server that validates JWTs against the authorization server's JWKS (`--http-oauth-issuer`, `--http-oauth-audience`, and optional `--http-oauth-jwks`/`--http-oauth-scopes`) and serves RFC 9728 protected-resource metadata at `/.well-known/oauth-protected-resource`.

The inbound HTTP credential only authorizes the request; it is never forwarded to the upstream API.

### Filtering tools

Large APIs produce many tools. Narrow the exposed set with:

- `--tools a,b,c` — expose only these tools by exact name.
- `--groups g1,g2` — expose only tools in these command groups.
- `--read-only` — expose only tools annotated `readOnlyHint`.

The flags compose, so `--groups files --read-only` exposes the read-only tools within the `files` group. They apply to both `mcp` and `mcp list`.

### Excluded operations

Some operations are not bridged as tools and are reported (with reasons) in the generate summary:

- SSE-only operations (only `text/event-stream` responses) — operations offering JSON **and** SSE are included using the JSON media type.
- Multipart/binary request bodies (`multipart/form-data`, `application/octet-stream`).
- `deprecated: true` operations.
- Operations marked `x-bartolo-mcp-ignore` (or the legacy `x-mcp-ignore`).

Known v1 limitation: optional query/header parameters equal to their zero value (`false`, `0`, `""`) are dropped by the generated client runtime, so an MCP caller cannot send an explicit `false`/`0`/`""` for an optional param. Required params and body fields are unaffected.

## Generation Targets

A Bartolo project generates two surfaces from one schema: the **CLI** command tree and the **MCP** server. You choose which surfaces to generate at `init`, and the choice is recorded in `.bartolo.json` under `targets`. Later `generate` / `sync` runs honor that choice and never re-prompt.

The interactive wizard asks:

```
What should this project generate?
> CLI + MCP server (recommended)
  CLI only
  MCP server only
```

Or select targets non-interactively with `--targets` (comma-separated `cli` and/or `mcp`):

```sh
bartolo init my-cli --targets cli        # CLI only
bartolo init my-mcp --targets mcp        # MCP server only
bartolo init my-app --targets cli,mcp    # both (the default when --targets is omitted)
```

Behavior per target set:

- **cli + mcp** (default) — the full surface: CLI commands plus an `mcp` subcommand.
- **cli only** — no MCP server. `mcp_tools.go` is not emitted, the register wiring drops the `mcp` command, and `go mod tidy` prunes the MCP SDK dependency.
- **mcp only** — an MCP-first binary. Running the bare binary (`./my-mcp`, no arguments) serves the stdio transport directly, so it drops into an MCP client with no wrapper command. The `mcp`, `mcp list`, `doctor`, and `auth setup` subcommands stay available for inspection and credential bootstrap; no per-operation CLI commands are generated.

Legacy projects created before this feature have no `targets` in `.bartolo.json`. The next interactive `generate` / `sync` asks the question once and persists the answer; a non-interactive run defaults to both surfaces and prints a one-line warning.

### Separate CLI and MCP repos from one schema

To ship the CLI and the MCP server as independent binaries/repos from the same OpenAPI document, scaffold two projects pointed at the same schema and give each a single target:

```sh
# CLI repo
mkdir acme-cli && cd acme-cli
bartolo init acme-cli --targets cli
bartolo generate ../schema/openapi.yaml
cd ..

# MCP repo
mkdir acme-mcp && cd acme-mcp
bartolo init acme-mcp --targets mcp
bartolo generate ../schema/openapi.yaml
```

Each repo owns its own `.bartolo.json`, so re-running `bartolo sync` in either keeps that surface pinned to its recorded target.

## Schema Shaping

Bartolo will synthesize a decent CLI from a plain schema, but it gets significantly better when the schema carries product intent.

Bartolo's canonical extension namespace is `x-bartolo-*`. Shared extensions affect the whole generated surface (both the CLI and the MCP tools); the two surface-scoped extensions restrict a single surface.

**Shared (CLI + MCP):**

| Extension | Purpose |
| --- | --- |
| `x-bartolo-aliases` | Add command aliases for operations (rendered in the CLI). |
| `x-bartolo-description` | Override generated help/description text. |
| `x-bartolo-group` | Force an operation into a higher-level noun. |
| `x-bartolo-hidden` | Hide a path or operation from normal CLI help. |
| `x-bartolo-ignore` | Exclude a path, operation, or parameter entirely (from CLI **and** MCP). |
| `x-bartolo-name` | Override a generated name for an API, operation, or parameter. |
| `x-bartolo-waiters` | Add polling-based waiter commands and follow-up flags. |

**Surface-scoped:**

| Extension | Purpose |
| --- | --- |
| `x-bartolo-cli-ignore` | Exclude an operation from the CLI only. The operation still becomes an MCP tool (and its client func is still generated). |
| `x-bartolo-mcp-ignore` | Exclude an operation from the generated MCP tool surface only. It still generates a CLI command. |

**Legacy aliases:** the earlier `x-cli-*` names (`x-cli-aliases`, `x-cli-description`, `x-cli-group`, `x-cli-hidden`, `x-cli-ignore`, `x-cli-name`, `x-cli-waiters`) and `x-mcp-ignore` keep working with no runtime warning. Each maps to the matching shared `x-bartolo-*` key (`x-mcp-ignore` → `x-bartolo-mcp-ignore`). When both a canonical `x-bartolo-*` key and its legacy alias appear on the same element, the canonical value wins. `x-bartolo-ignore` always excludes everywhere, regardless of any surface-scoped flag also present.

Bartolo also groups operations automatically from:

- the first OpenAPI tag
- `x-bartolo-group`
- the first stable path noun when tags are missing

That fallback matters for large real-world schemas where tagging is inconsistent.

## Customization

Generated CLIs keep a normal `main.go`, so you can still add middleware, flags, or auth behavior around the generated commands.

```go
package main

import "github.com/orq-ai/bartolo/cli"

func main() {
	cli.Init(&cli.Config{
		AppName:             "my-cli",
		EnvPrefix:           "MY_CLI",
		DefaultOutputFormat: "json",
		Version:             "1.0.0",
	})

	registerGeneratedCommands()
	registerCustomCommands()
	cli.Root.Execute()
}
```

## Local Development

Use the repo-level verification flow before publishing changes:

```sh
make smoke
make verify
```

- `make smoke` builds Bartolo, scaffolds a fresh temporary CLI, generates commands, and confirms the result builds.
- `make verify` runs smoke plus the full Go test suite.

## Positioning

Bartolo is not trying to be a generic SDK generator. It is focused on one thing: turning an OpenAPI document into a CLI that feels intentional enough to publish for real users and structured enough to be driven by tools like Codex and Claude.
