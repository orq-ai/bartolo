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
bartolo init my-cli

# Generate from either YAML or JSON
bartolo generate openapi.yaml
# or
bartolo generate openapi.json

go mod tidy
go build -o my-cli
./my-cli -o json doctor
./my-cli --help
```

The default output format is `table`, for every generated CLI: list commands
render a table on a terminal, and everything else — a piped or redirected run,
a non-list command, a response that is not a collection — serializes instead. A
list that cannot be tabulated at a terminal says so on stderr rather than
quietly serializing.

`bartolo init --serialization-format` picks what that serialization is (`json`
by default, since a pipe usually feeds a script; a CLI aimed at models might
pick `toon`). It does not decide whether tables render — that is a per-machine choice, and pinning anything but
`table` turns them off:

```sh
./my-cli default-format yaml   # no tables, YAML everywhere
./my-cli default-format table  # tables back
```

## What You Get

Every generated CLI starts with a useful operator surface:

- `doctor` shows config, auth source, and selected server.
- `auth profile add --server` and `auth setup --server` bind a profile to its own API base URL, so staging, self-hosted, and local profiles resolve without repeating the flag.
- `auth profile use`, `auth profile clear`, and `auth profile current` pick, drop, and show the active profile, so `--profile` does not have to be repeated.
- `auth profile remove <name>` (aliases `rm`, `delete`) deletes a stored profile without hand-editing `credentials.json`. Removing the profile chosen with `auth profile use` also drops that stored choice; a `--profile` flag or `<PREFIX>_PROFILE` naming it is the caller's to clear. It does not prompt, unlike a generated `DELETE`: nothing server-side is destroyed.
- There is no implicit profile: credentials come from the environment until one is named or chosen, and a profile is auto-selected whenever none is currently active — including right after `auth profile clear`.
- A profile in force is authoritative for its key and takes precedence over the environment; the server is not held to the same rule — a profile saved without `--server` still falls through to `<PREFIX>_SERVER`. See the generated CLI's own README for the exact resolution order, which differs for the key and the server.
- `request` provides a raw escape hatch for unmodeled endpoints.
- `default-format` shows or persists the preferred default output format.
- `-o table` (the default) renders list commands as a table on a terminal, and falls back to the CLI's serialization format elsewhere; `-o json|yaml|toon` picks a serialization for one run, and `--columns id,name` picks the table's columns.
- `-o`/`--output-format` and `-j`/`--jmespath` make automation and projection straightforward. `--raw` is not a format: it unwraps a scalar result, printing a string unquoted and a list of scalars one per line, which is what a shell wants back from a projection — and it suppresses the table, falling back to the serialized form.
- Generated flags never shadow a global: a body field or parameter named after one (`raw`, `profile`, `output-format`, ...) is exposed as `--body-<name>` or `--param-<name>`.
- Grouped nouns like `prompts`, `files`, or `human-evals` feel closer to a product CLI than a path translator.
- String parameters with an `enum` list their values in `--help` and complete them in the shell.

## MCP

Every generated CLI is also a [Model Context Protocol](https://modelcontextprotocol.io) server. Running `generate` emits an `mcp` subcommand that exposes the API's operations as MCP tools for clients such as Claude Code, Claude Desktop, and Cursor. Generated servers speak MCP **2026-07-28** (stdio and stateless Streamable HTTP) through the official Go SDK. Older clients still connect via SDK protocol negotiation.

What gets generated:

- An `mcp` subcommand on the CLI (`my-cli mcp`) that serves over stdio by default.
- One tool per eligible operation, named `<group>_<leaf>` (or just the leaf for ungrouped ops).
- A JSON Schema (2020-12) input schema per tool, derived from the operation's path/query/header params and JSON request body. Arguments are validated against it before any request is sent. `oneOf` is emitted as `anyOf`: OpenAPI unions rarely make their branches mutually exclusive, and several model providers accept only `anyOf`.
- Schemas are budgeted for the model's context: a top-level argument keeps up to 600 characters of description, nested ones their first sentence, and structures deeper than six levels keep only their type.
- An output schema from the first 2xx JSON response. Clients fail a tool call whose result does not match its output schema, after the API call has already happened, so the output schema describes shape only: no `required`, `enum`, `format`, bounds, or descriptions, four levels deep, and nested values may be `null`.
- Results returned as `structuredContent` (plus a JSON text block). A non-object response is wrapped as `{"result": ...}`. Upstream API errors surface as `isError` tool results with a structured `{status, body}` payload (body redacted like the CLI's) rather than protocol errors.
- Tool annotations derived from the HTTP method: `readOnlyHint` for GET/HEAD, `destructiveHint` for PUT/PATCH/DELETE, `idempotentHint` for GET/HEAD/PUT/DELETE.
- Cancelling a tool call (or dropping its HTTP request) aborts the upstream API request.
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

Serve over Streamable HTTP instead of stdio. HTTP is **stateless** (MCP 2026-07-28): each request is self-describing and there is no session, so any number of replicas can run behind a plain load balancer. `server/discover` and `tools/list` results carry a five-minute `ttlMs`, with `cacheScope: private` whenever inbound auth is on.

```sh
my-cli mcp --http 127.0.0.1:8484
```

Inbound auth for HTTP is selected with `--http-auth`:

- `none` (default) — allowed only on a loopback address unless `--http-insecure` is set.
- `bearer` — a static token read from the `<PREFIX>_MCP_TOKEN` env var, compared in constant time.
- `oauth` — an OAuth 2.1 resource server that validates JWTs against the authorization server's JWKS (`--http-oauth-issuer`, `--http-oauth-audience`, and optional `--http-oauth-jwks`/`--http-oauth-scopes`) and serves RFC 9728 protected-resource metadata at `/.well-known/oauth-protected-resource`.
- `forward` — for a hosted server that fronts its own API. Every request must carry the caller's API key (`Authorization: Bearer <key>`), and each tool call reaches the API with that key instead of the server's configured credentials, so the API applies the caller's own permissions, workspace scoping, and rate limits. The server needs no API credential of its own. Use it only when the inbound token is a credential for the upstream API itself, never for tokens issued to the MCP server.

Except under `forward`, the inbound HTTP credential only authorizes the request; it is never forwarded to the upstream API.

`GET /healthz` and `GET /readyz` answer `200 ok` without auth, for load balancers and orchestrator probes.

Cross-origin browser requests are refused with 403 unless their origin is listed in `--http-allowed-origins` (full origins, e.g. `https://app.example.com`). Same-origin requests and non-browser clients, which send no `Origin`, are unaffected.

### Filtering tools

Large APIs produce many tools. Narrow the exposed set with:

- `--tools a,b,c` — expose only these tools by exact name.
- `--groups g1,g2` — expose only tools in these command groups.
- `--read-only` — expose only tools annotated `readOnlyHint`.

The flags compose, so `--groups files --read-only` exposes the read-only tools within the `files` group. They apply to both `mcp` and `mcp list`.

### Protocol options and conformance

An optional `mcp` block in `.bartolo.json` sets the server's identity and icons, list caching, trace-context propagation, accepted protocol versions, and an MCP Server Card. `x-bartolo-mcp-header` on a parameter lets gateways route on its value. Generated servers run the official MCP conformance suite in CI (`make conformance`). [docs/mcp-conformance.md](docs/mcp-conformance.md) maps every SEP to how it is supported and verified, and documents each option.

### Custom MCP tools

Generated `cli/generated/mcp_tools.go` is overwritten on every `generate`. User-owned extras live in `cli/custom/Register`, which is never overwritten:

```go
import "github.com/orq-ai/bartolo/mcpserver"

func Register(root *cobra.Command) {
    mcpserver.Use(
        mcpserver.AddTools(searchCodebase),          // extra tool
        mcpserver.RemoveTools("legacy_delete"),      // drop a generated tool
        mcpserver.PatchTool("echo_echo", func(t *mcpserver.ToolDef) {
            t.Description = "Echo with extra logging"
            next := t.Handler
            t.Handler = func(ctx context.Context, args map[string]any) (any, error) {
                return next(ctx, args)
            }
        }),
    )
}
```

Two more options shape a hosted server:

- `mcpserver.KeepTools("agents_list", "agents_create", ...)` narrows the generated tools to an allow-list, for an API too large to expose whole. A listed name with no generated tool is an error, so an operation renamed in the API fails `mcp list` (and CI) instead of quietly disappearing. Tools from `AddTools` are always kept.
- `mcpserver.DiscoverTools()` keeps the tools `KeepTools` leaves out reachable instead of dropping them. Four meta-tools are listed after the kept ones: `search_operations` finds any operation, `describe_operation` returns its argument schema, `call_read_operation` runs a read-only one, and `call_operation` runs any. The whole API stays usable while the tool list — which the model reads on every turn — stays short. `--groups` and `--read-only` also bound what the meta-tools reach.
- `mcpserver.HTTPRoute("GET /v2/healthz", handler)` mounts an extra route next to the MCP endpoint — health-check aliases, discovery documents. These routes skip inbound auth and cross-origin protection.

`Use` is applied when the server starts (and for `mcp list`), so it works for both stdio and `--http`. Wrappers run after JSON Schema validation. An `AddTools` name collision, an invalid tool name, a missing handler, or a non-object input schema fails `mcp` and `mcp list` with an error naming the tool. A tool with no `InputSchema` takes no arguments.

### Excluded operations

Some operations are not bridged as tools and are reported (with reasons) in the generate summary:

- SSE-only operations (only `text/event-stream` responses) — operations offering JSON **and** SSE are included using the JSON media type.
- Multipart/binary request bodies (`multipart/form-data`, `application/octet-stream`).
- `deprecated: true` operations.
- Operations marked `x-bartolo-mcp-ignore` (or the legacy `x-mcp-ignore`).

Cookie parameters are not exposed as tool arguments, since generated clients cannot send them.

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
| `x-bartolo-help-section` | Put a top-level command under a titled section in `--help` (CLI only). |
| `x-bartolo-hidden` | Hide a path or operation from normal CLI help. |
| `x-bartolo-ignore` | Exclude a path, operation, or parameter entirely (from CLI **and** MCP). |
| `x-bartolo-list` | Set to `true` to mark an operation as a collection, or `false` to suppress automatic collection detection (CLI only). |
| `x-bartolo-list-fields` | Set and order the default columns for an interactive collection response; a non-empty list also marks the operation as a collection (CLI only). |
| `x-bartolo-name` | Override a generated name for an API, operation, or parameter. |
| `x-bartolo-no-validate` | Set to `true` to skip the client-side `enum`/`format` check for a parameter, for a schema that is stricter than the API it describes. Applies to CLI flags and MCP tool arguments alike. |
| `x-bartolo-waiters` | Add polling-based waiter commands and follow-up flags. |

**Surface-scoped:**

| Extension | Purpose |
| --- | --- |
| `x-bartolo-cli-ignore` | Exclude an operation from the CLI only. The operation still becomes an MCP tool (and its client func is still generated). |
| `x-bartolo-mcp-ignore` | Exclude an operation from the generated MCP tool surface only. It still generates a CLI command. |

**Legacy aliases:** the earlier `x-cli-*` names (`x-cli-aliases`, `x-cli-description`, `x-cli-group`, `x-cli-help-section`, `x-cli-hidden`, `x-cli-ignore`, `x-cli-list`, `x-cli-list-fields`, `x-cli-name`, `x-cli-no-validate`, `x-cli-waiters`) and `x-mcp-ignore` keep working with no runtime warning. Each maps to the matching shared `x-bartolo-*` key (`x-mcp-ignore` → `x-bartolo-mcp-ignore`). When both a canonical `x-bartolo-*` key and its legacy alias appear on the same element, the canonical value wins. `x-bartolo-ignore` always excludes everywhere, regardless of any surface-scoped flag also present.

Collection paths and responses are inferred automatically for GET operations.
Any method is also inferred as a collection when its 2xx JSON response is a
conventional page of rows: an array of objects under `data`, `items`, `results`,
`records`, `entries` or `servers`, next to a pagination field — `has_more`,
`next_page_token`, `total_count`, `count` and the rest of
`cli.PaginationEvidenceKeys`. A key counts as evidence only when it describes the
collection, so an echoed `limit` or `offset` does not classify an operation, even
though the table footer hides it. That covers the usual POST search and query endpoints without
annotation. Every part is required: an embeddings call returns a `data` array
with no cursor and stays serialized, and an array of plain strings is not
something a table can render. Only immediate properties are read, so an envelope
or a row schema composed with `allOf`/`oneOf` needs `x-cli-list` too.

Use `x-cli-list: true` for a collection that does not match, such as rows under a
resource-named key, or `x-cli-list-fields` to both mark it and set the columns. A
non-empty field list also marks the operation as a collection, while
`x-cli-list: false` disables all automatic inference. An empty
`x-cli-list-fields: []` marks nothing on its own, and combining `x-cli-list: false`
with declared columns is a contradiction that fails generation.

For example, a POST search can define the default interactive columns while
leaving the complete response available through `-o json` or a pipe:

```yaml
paths:
  /widgets/search:
    post:
      x-cli-list-fields:
        - id
        - name
        - status
```

Without `x-cli-list-fields` the columns are inferred from the response: nested
objects are skipped, a list shows its first three entries followed by `…`,
long values are truncated, and columns that do not fit the terminal are
dropped from the right. Declared or explicit columns can reach into nested
objects with a dotted path such as `model.id`. They can also project a field
from each object in one nested array, such as `settings.tools[].key`; elements
that are not objects or that lack the field are skipped, a field that is
present but null renders nothing while still confirming the column exists, and
the surviving values use the same three-entry list abbreviation.
A nested column is labelled by its last field, extended with parent fields
only as far as needed to keep headers distinct, so `pricing.input.cost` and
`pricing.output.cost` show as `INPUT . COST` and `OUTPUT . COST`; array
markers are not shown, and a literal key keeps its full name. An empty or
null array renders a blank cell and still validates the column, so a name is
only truly checked against a response whose arrays have elements. A selector
supports one `[]` and requires a field after it; a bracket the grammar does
not accept — an array index, a second `[]` — is reported with that rule. Use
`--jmespath` when a response needs indexing, filtering, aggregation, or other
restructuring. Because `[]` contains shell metacharacters, quote the complete
argument when selecting a projected column, for example
`--columns 'key,settings.tools[].key'`.

Bartolo also groups operations automatically from:

- the first OpenAPI tag
- `x-bartolo-group`
- the first stable path noun when tags are missing

That fallback matters for large real-world schemas where tagging is inconsistent.

### Help sections

`x-cli-help-section` on a tag (or on a single operation, which wins) sets the heading a
top-level command appears under in `--help`:

```yaml
tags:
  - name: Files
    x-cli-help-section: Storage
```

Sections are created in the order they are first seen. As soon as one command has a
section, every command without one — including `doctor`, `help`, and anything the
consuming CLI registers itself — is collected under a final `Other` section, so
Cobra's `Additional Commands` block never shows up next to named sections. Because the heading travels with the spec,
a newly tagged API lands in the right section on regeneration instead of needing an
entry in a hand-maintained table downstream.

## Customization

Generated CLIs keep a normal `main.go`, so you can still add middleware, flags, or auth behavior around the generated commands.

```go
package main

import (
	"os"

	"github.com/orq-ai/bartolo/cli"
)

func main() {
	cli.Init(&cli.Config{
		AppName:             "my-cli",
		EnvPrefix:           "MY_CLI",
		SerializationFormat: "json",
		Version:             "1.0.0",
	})

	registerGeneratedCommands()
	registerCustomCommands()
	os.Exit(cli.Execute())
}
```

`cli.Execute` runs the root command and returns the process exit code: `0` on
success, `2` for usage errors (unknown command, bad flag, wrong argument count),
and `1` for anything that failed while running. Passing it to `os.Exit` is what
makes failures visible to `set -e` scripts and CI. Calling `cli.Root.Execute()`
directly discards the error and always exits `0`.

A CLI that cancels in-flight requests on SIGINT/SIGTERM needs a context, and
calling `cli.Root.ExecuteContext(ctx)` for it loses the same contract — the
unknown-subcommand check and the usage exit code live in `cli.Execute`, not on
the root command. Use `cli.ExecuteContext(ctx)` instead, which is `cli.Execute`
with a caller-supplied context:

```go
os.Exit(cli.ExecuteContext(ctx))
```

### Suppressing prompts

Auth setup, the profile value prompts, and destructive confirmations all ask
before they act, and by default they ask whenever stdin is a terminal. A CLI
that offers a "never prompt" flag sets `cli.PromptAllowed` so the flag reaches
all three at once:

```go
func main() {
	cli.Init(&cli.Config{ /* ... */ })

	// After cli.Init: both calls need cli.Root, which Init creates.
	cli.AddGlobalFlag("no-input", "", "Never prompt; fail instead", false)
	cli.PromptAllowed = func() bool { return !viper.GetBool("no-input") }
}
```

It is a veto, not a replacement: a terminal is still required either way, so no
override can enable prompting against a pipe and hang on a question with nowhere
to appear. `cli.HasInteractiveInput` reports the terminal check if you want to
ask it yourself.

Without this, a CI job that sets such a flag still blocks on a prompt as soon as
it has a terminal — a `docker run -t` step, a self-hosted runner, or a developer
reproducing the job locally. No site hangs once it is in place: auth setup and
destructive confirmations return a usage error, and an optional profile key is
simply left unset, as pressing Enter would leave it.

## Local Development

Use the repo-level verification flow before publishing changes:

```sh
make smoke
make verify
```

- `make smoke` builds Bartolo, scaffolds a fresh temporary CLI, generates commands, and confirms the result builds.
- `make verify` runs smoke plus the full Go test suite.

## Releasing

Releases are automatic. When CI passes on `main`, the next version is derived
from the conventional-commit subjects since the last tag, that tag is pushed,
and a GitHub Release is published. Merges are squashed, so the PR title is what
counts, and CI rejects a title that is not a conventional commit:

| Subject contains | Result |
| --- | --- |
| `feat:` | minor release |
| `fix:` | patch release |
| `!:` after the type, or a `BREAKING CHANGE:` line anywhere in the body | minor release while Bartolo is 0.x (see below) |
| a `(deps)` scope — `chore(deps):`, `build(deps):` — but not `ci(deps):` | patch release |
| `docs:`, `chore:`, `ci:`, `refactor:`, `test:`, `style:`, `build:` | ships in the next release, triggers none |
| anything else, including `perf:` and a bare `revert:` | **fails the release run** |

Three things about that table are worth knowing before you write a title.

"Contains", not "starts with": svu matches `feat:` and `fix:` case-insensitively
*anywhere* in the subject. So `Revert "fix: ..."` — GitHub's own revert-button
subject — cuts a patch release, and a subject quoting another commit inherits
that commit's bump.

`(deps)` is special-cased by the release workflow, not by svu. Dependabot titles
its PRs `chore(deps): ...`, which svu would treat as release-neutral, but every
Go dependency is compiled into the binary `go install` serves — a patched
dependency that never reaches a tag is the failure govulncheck exists to catch.
So the workflow promotes a deps-scoped subject to a patch. `ci(deps):` is
excluded, because a GitHub Actions bump changes nothing a consumer installs.

The last row is the important one. Release-neutral has to mean *by intent*, not
*by accident*: `perf:` and a bare `revert:` are well-formed conventional commits
that ship user-visible change and bump nothing, which is exactly how a security
fix ends up sitting on `main` untagged. So the release run fails on any subject
whose type is not on the neutral list above. Title user-visible performance work
`fix:`, and land a revert either as `fix:` or with a forced release.

There is no version constant to bump. `bartolo version` reports the module
version recorded in the binary's build info, so a binary installed with
`go install github.com/orq-ai/bartolo@vX.Y.Z` reports `X.Y.Z`, and one built from
a checkout reports `devel`, or `devel+<commit>` when Go stamped the build with a
VCS revision (it does not in a git worktree, or under `-buildvcs=false`). The tag
is the only source of truth.

The arithmetic is [svu](https://github.com/caarlos0/svu)'s. `.svu.yml` sets
`v0: true`, which means a breaking change bumps the minor rather than cutting
v1.0.0 — correct for a pre-1.0 project, and it also caps svu's one rough edge:
it matches `BREAKING CHANGE` anywhere in a commit body, so a body that merely
quotes the phrase would otherwise release a major. Revisit both before tagging
v1.0.0.

A run of `docs:`/`chore:`/`ci:`-only merges accumulates on `main` without ever
being tagged, which is correct but invisible: GitHub Releases looks the same
whether everything is shipped or a month of maintenance is waiting. To ship
those, or to cut a major, run the `release` workflow manually with a
`force_level` — `svu major` overrides `v0: true`.

A release run that finds `main` already ahead of the commit it was fired for —
two PRs merged inside one CI cycle, so the older run finishes last — reports a
notice and exits green. Nothing is lost: the run for the newer commit releases
everything up to it, this commit included. A red release run always means a
release that should have happened did not.

Release notes are generated on the GitHub Release from the merged PRs.
`CHANGELOG.md` is frozen at the last hand-written entry and covers only the
versions that predate automation.

Preview what a merge would produce with `svu next`, which prints the next tag, or
the current one when nothing warrants a release. Install the same version the
release workflow pins — `go install github.com/caarlos0/svu/v3@v3.4.1`, matching
`SVU_VERSION` in `.github/workflows/release.yml` — so the preview and the
decision come from one binary.

## Positioning

Bartolo is not trying to be a generic SDK generator. It is focused on one thing: turning an OpenAPI document into a CLI that feels intentional enough to publish for real users and structured enough to be driven by tools like Codex and Claude.
