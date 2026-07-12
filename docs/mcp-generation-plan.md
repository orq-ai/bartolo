# Bartolo MCP Generation — Implementation Plan

Status: validated against the codebase at v0.4.3, against a compile-and-run spike of
`github.com/modelcontextprotocol/go-sdk` v1.6.1, and adversarially reviewed against the source
(all mechanical claims carry verified file:line evidence; refuted claims were corrected).
Written to be executed phase-by-phase by implementation agents; each phase is an independent,
verifiable PR.

## Goal

`bartolo generate openapi.yaml` additionally emits an MCP server surface inside the generated
CLI. Every generated CLI gains a `mcp` subcommand:

```sh
my-cli mcp                  # serve MCP over stdio (default, for Claude Desktop/Code, Cursor…)
my-cli mcp --http :8484     # serve MCP over Streamable HTTP
my-cli mcp list             # print the tool manifest as JSON (debugging / smoke tests)
```

Quality bar:

- Every tool has a real JSON Schema (2020-12) input schema derived from the OpenAPI operation
  (path/query/header params + request body), and an output schema derived from the 2xx response.
- Results are returned as `structuredContent` + serialized-JSON text content (spec 2025-06-18+).
- API errors surface as `isError` tool results with structured `{status, headers, body}` — never
  protocol errors, never a bare string.
- Server-side input validation against the input schema before any HTTP call.
- Tool annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`) derived from the HTTP method.
- Outbound auth reuses the existing runtime (API key / bearer / env vars / profiles) and gains
  OAuth2 wiring (client credentials + auth code/PKCE) from the spec's `securitySchemes`.
- Inbound auth for HTTP transport: none (loopback default), static bearer, or OAuth 2.1 resource
  server with RFC 9728 protected-resource metadata.
- End-to-end tests drive the generated server with a real MCP client (go-sdk) in CI.

Addendum (post-v1): generation is now target-scoped — `.bartolo.json` records `targets` (`cli`/`mcp`), so a project can emit CLI-only, MCP-only (an MCP-first binary whose bare invocation serves stdio), or both; `OpenAPI.TargetMCP` gates `HasMCPTools`.

## Verified constraints (do not re-derive; these were checked)

| Fact | Evidence |
| --- | --- |
| Generator is a single `package main`, `main.go` (2772 lines), templates embedded via `//go:embed templates/*` | `main.go:30-31` |
| IR structs: `Operation` (`main.go:62-85`), `Param` (`main.go:48-59`), `BodyField` (`main.go:89-96`), `OpenAPI` (`main.go:195-210`) | — |
| `ProcessAPI` (`main.go:224-545`) builds the IR; `writeGeneratedCommandFiles` (`main.go:2589`) renders `generated_client.tmpl`, `generated_register.tmpl`, `generated_root_commands.tmpl`, `generated_group_commands.tmpl` into `cli/generated/` | — |
| Generated client funcs: `func <Public><GoName>(<requiredParams as string args...>, params *viper.Viper[, body string]) (*gentleman.Response, <ReturnType>, error)` — the `body string` arg exists **only when the op has a request body** (`.CanHaveBody`), and ALL required params are `string` args regardless of schema type; errors for non-2xx are `errors.Errorf("HTTP %d: %s", …)` (`generated_client.tmpl:97,168`) | `templates/generated_client.tmpl:41,112` |
| Optional query/header params are read via typed viper getters keyed by `Param.CLIName` and are **silently dropped when equal to the zero value** (`if x != TypeNil` guard, `generated_client.tmpl:67-68,138`) — an explicit `false`/`0`/`""` cannot be transmitted today | verified |
| `item.Operations()` is a Go map — method order within a path is nondeterministic; only path keys are sorted (`main.go:264-268`) and only group ops get a late sort (`main.go:470-472`) | verified |
| All three existing auth inits register with an EMPTY type name: `apikey/apikey.go:160`, `oauth/clientcredentials.go:97`, `oauth/authcode.go:214` all call `cli.UseAuth("", …)`; a second empty-type registration **panics** (`cli/credentials.go:244-245`), and with >1 typed handlers `resolveAuthHandler` returns nil for profile-less (env-var-only) users (`cli/credentials.go:134-147`) | verified |
| Spec parsing: `kin-openapi` v0.135.0, OpenAPI 3.0 + partial 3.1 normalization (`normalizeOpenAPI31Data`, `main.go:1765-1835`) | `go.mod:9` |
| Auth generation today: only single-scheme `apiKey`/`http bearer` → `apikey.Init` / `apikey.InitBearer` (`getAuthInit` `main.go:1478-1500`); OAuth2/OIDC/multi-scheme → nothing (`main.go:1525`) | — |
| Runtime auth model: `cli.AuthHandler` + `cli.UseAuth` + gentleman middleware (`cli/credentials.go:21-131`); `oauth/` package already implements auth-code+PKCE, client-credentials, refresh, token cache | — |
| `cli/agent.go` is doctor/request/server commands — NOT AI-agent code. No MCP code exists anywhere. | grep-verified |
| Orq fixture: OpenAPI 3.1, 142 operations, bearer-only auth, **3 SSE-only ops** (`/v2/deployments/stream`, `/v2/agents/stream-run`, `/v2/agents/{key}/stream-task`; 4 more ops offer JSON+SSE and stay included), 5 multipart ops, heavy `oneOf`/`anyOf`/`enum` | `testdata/orq/openapi.json` |
| go-sdk v1.6.1 requires **go ≥ 1.25**; repo `go.mod` is at 1.23 — must bump (root + `templates/go.mod.tmpl`) | spike |
| go-sdk API (spike-compiled and run): `mcp.NewServer(&mcp.Implementation{…}, nil)`; `(*Server).AddTool(&mcp.Tool{Name, Description, InputSchema, OutputSchema, Annotations}, ToolHandler)` where `Tool.InputSchema any` accepts `json.RawMessage`; `ToolHandler = func(ctx, *CallToolRequest) (*CallToolResult, error)`; `req.Params.Arguments` is raw JSON; `CallToolResult{Content, StructuredContent, IsError}`; `mcp.StdioTransport{}`; `mcp.NewStreamableHTTPHandler(getServer, opts)`; `mcp.NewInMemoryTransports()` for tests; validation via `github.com/google/jsonschema-go/jsonschema` (`Schema.Resolve(nil)` → `Resolved.Validate(instance)`); inbound bearer middleware `auth.RequireBearerToken(verifier, opts)`; RFC 9728 types in `oauthex` | spike in scratchpad `mcpspike/main.go` |
| `Server.AddTool` (non-generic) does NOT validate inputs — we must validate ourselves (generic `mcp.AddTool[In,Out]` does, but requires Go types; we have runtime schemas) | go-sdk `mcp/server.go:238`, `mcp/protocol.go:1311-1317` |
| stdio purity risk: in MCP mode nothing may write to stdout except the protocol. `cli.Formatter` writes to stdout; verbose `LogMiddleware` pretty-prints to stdout (`cli/http.go:76-147`); zerolog console writer target must be audited (`cli/cli.go:83-84`) | — |

## Architecture

```
bartolo repo
├── mcpserver/                  NEW runtime package (imported only by generated MCP code,
│   ├── mcpserver.go            keeping go-sdk out of the `cli` package import graph)
│   ├── command.go              cobra `mcp` command factory
│   ├── httpauth.go             inbound auth for --http (bearer / OAuth RS)
│   └── *_test.go
├── mcpschema.go                NEW generator-side OpenAPI→JSON-Schema converter + tool builder
├── main.go                     IR extensions + wiring into ProcessAPI/writeGeneratedCommandFiles
└── templates/
    └── generated_mcp.tmpl      NEW → cli/generated/mcp_tools.go

generated CLI
└── cli/generated/
    ├── mcp_tools.go            []mcpserver.ToolDef: schemas as raw JSON + handler closures
    └── register.go             Register() additionally mounts mcpserver.Command(...)
```

Decisions locked in (validated, do not re-litigate):

1. **MCP lives inside the generated CLI binary** as a `mcp` subcommand. One artifact, shares
   config/auth/profiles/doctor. No separate server module.
2. **Official SDK** `github.com/modelcontextprotocol/go-sdk` v1.6.x (spec 2025-11-25 capable).
   Not mark3labs/mcp-go: official SDK is tier-1 maintained, has structured output, streamable
   HTTP, and auth helpers.
3. **Raw-schema registration** (`Server.AddTool` + `json.RawMessage` schemas + self-validation
   via jsonschema-go), NOT the generic typed `mcp.AddTool[In,Out]`. We already have precise
   OpenAPI schemas; generating Go structs per operation would lose `oneOf`/`enum` fidelity and
   bloat generated code. Spike proved this path works including validation-failure results.
4. **New runtime package `mcpserver`**, not additions to `cli` package: existing generated CLIs
   that merely re-`go mod tidy` must not pull go-sdk transitively through `cli`.
5. **Handler closures call the existing generated client funcs** (`OpenapiEcho(params, body)`
   style) so `HandleBefore`/`HandleAfter` middleware, auth middleware, server resolution, and
   `--profile` all behave identically to CLI invocations.

## Tool surface design

### Naming

`<group>_<leaf>` with slugs converted to `snake_case` (`prompts_list`, `files_upload`); ungrouped
ops use the leaf alone. Must match `^[a-zA-Z0-9_-]{1,128}$`. Deterministic collision handling:
append `_2`, `_3`… after explicitly sorting all collected ops by `(path, method)` —
`item.Operations()` is a map, so per-path method order is NOT deterministic today; do not rely on
insertion order. Reserve the name `mcp` in group slugging (a group slugged `mcp` would collide
with the new root subcommand in cobra). `Tool.Title` = OpenAPI `summary` (fallback: humanized
name). `Tool.Description` = summary + "\n\n" + description, truncated at 1024 chars on a word
boundary (LLM context hygiene; full text stays in the CLI help).

### Input schema

One JSON object schema per tool, built at generation time from the **dereferenced OpenAPI
schemas** (not from the flattened `BodyField` list — MCP deserves full fidelity):

- Path params → required top-level properties (wire name, slugged to `snake_case` property name).
- Query/header params → top-level properties, required iff `required: true`.
- Request body (JSON media type):
  - object body → its top-level properties are flattened into the tool schema; `required` carried
    over. On a name collision with a param property, the param keeps the name and the body
    property is prefixed `body_`.
  - non-object body (array/primitive/pure union) → a single `body` property carrying the schema.
- `additionalProperties: false` at the top level.
- Every property keeps `description`, `enum`, `format`, `default`, `minimum`/`maximum`, `items`,
  nested `properties`/`required`, `oneOf`/`anyOf`/`allOf`.

A generated **routing table** (`[]mcpserver.ArgRoute{Prop, WireName, In, Type}` with
`In ∈ {path, query, header, body}`) tells the handler closure where each top-level property goes.
Nested body content below the top level is passed through verbatim into the JSON body.

Known v1 limitation (inherited from the CLI runtime, document in the generated README): optional
query/header params equal to their zero value (`false`, `0`, `""`) are dropped by the generated
client funcs (`if x != TypeNil` guard, `generated_client.tmpl:67-68`). An MCP caller cannot send
an explicit `false`/`0` for an optional query param in v1. Acceptable for v1; the clean later fix
is switching the client-func guard to viper's `IsSet`-style presence tracking, which is a CLI
behavior change and out of scope here.

### Schema conversion rules (OpenAPI 3.0/3.1 → JSON Schema 2020-12)

Implemented in `mcpschema.go` as a pure function over `*openapi3.Schema`:

- `nullable: true` → `"type": ["T", "null"]`.
- 3.1 `type: [T, "null"]` arrays pass through (kin-openapi + the existing 3.1 normalizer already
  load these; see `effectiveBodySchema` `main.go:1424-1476` for prior art on null-branch collapse).
- Drop: all `x-*` extensions, `xml`, `discriminator`, `example`/`examples` (keep one `examples`
  entry if present — cheap few-shot help), `readOnly` properties are dropped from **input**
  schemas, `writeOnly` dropped from **output** schemas.
- `$ref` handling: inline with a depth limit of 8 and a cycle guard. The guard MUST track visited
  `*openapi3.Schema` pointers (the `.Value` field), **path-scoped** (add on descent, remove on
  unwind) — kin-openapi gives every `$ref` site its own `*openapi3.SchemaRef` wrapper and only
  shares the `Value` pointer (loader.go `component.Value = resolved.Value`), so a visited-set of
  `*SchemaRef` never detects cycles, and a non-path-scoped `*Schema` set would falsely flag
  diamond-shaped reuse. On cycle or depth overflow emit `{"type": "object"}` plus the original
  description.
  No `$defs` in v1 (client compatibility varies); revisit if manifest size becomes a problem.
- Unsupported/absent schema → `{}` (permissive), never a generation failure.

### Output schema + results

- Output schema from the operation's first 2xx JSON response schema, converted the same way.
  Non-object top-level (arrays etc.) → wrapped as `{"type":"object","properties":{"result": S},
  "required":["result"]}`; the handler wraps the decoded value identically (structuredContent
  MUST be a JSON object per spec).
- Handler returns `CallToolResult{Content: [text JSON], StructuredContent: decoded}` — text block
  always present for backward compat, matching spec guidance.
- No 2xx JSON schema in the spec → omit `OutputSchema`, still return text + structuredContent
  when the response parses as an object.

### Errors

New typed error in the runtime, replacing the stringly error in generated client funcs:

```go
// cli/http.go
type HTTPError struct {
    StatusCode int
    Headers    http.Header
    Body       []byte
}
func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.StatusCode, string(e.Body)) }
```

`generated_client.tmpl` returns `&cli.HTTPError{...}` on ≥400 (same rendered message, so CLI UX
is unchanged and `main_test.go` stays green). The MCP handler maps:

- `*cli.HTTPError` → `isError: true`, text `"HTTP <code>: <body>"`, `StructuredContent:
  {"status": N, "body": <parsed-or-raw>}`.
- schema validation failure → `isError: true`, text `"invalid arguments: <detail>"` (no HTTP call made).
- transport/other error → `isError: true` with the error string.
- Handler panics recovered → `isError: true` (never crash the server on one bad tool call).

Protocol-level errors are reserved for unknown tool / malformed JSON-RPC (SDK handles those).

### Annotations

| Method | readOnlyHint | destructiveHint | idempotentHint |
| --- | --- | --- | --- |
| GET/HEAD | true | — | true |
| PUT | false | false | true |
| POST/PATCH | false | false | false |
| DELETE | false | true | true |

`openWorldHint: false` for all (closed API domain).

### Operation filtering

> Addendum: the extensions below were renamed to the canonical `x-bartolo-*` namespace (`x-cli-ignore` → `x-bartolo-ignore`, `x-mcp-ignore` → `x-bartolo-mcp-ignore`, plus a new CLI-only `x-bartolo-cli-ignore`); the legacy `x-cli-*`/`x-mcp-ignore` names still work. See the README "Schema Shaping" section for the full map.

- Generation-time excludes (recorded with reasons, printed in the generate summary and listed in
  a README section): `x-cli-ignore`/`x-mcp-ignore`, `deprecated: true`, SSE-only operations
  (only `text/event-stream` content — 7 in the Orq spec), multipart/binary request bodies
  (5 in the Orq spec). Ops that offer JSON **and** SSE use the JSON media type and are included.
- `x-cli-hidden` ops ARE included (hidden is a help-noise flag, not a capability restriction) unless
  also `x-mcp-ignore`.
- Runtime narrowing on the `mcp` command: `--tools a,b,c` (exact names), `--groups g1,g2`,
  `--read-only` (only `readOnlyHint` tools). Useful because 142 tools is a lot for some clients.
- Streaming/multipart support is explicitly Phase 7+ (not in scope): SSE tools would need
  progress notifications; multipart needs a file-path/base64 story.

## Auth design

### Outbound (MCP server → upstream API) — reuse + extend

Works today via the generated `initGeneratedRuntime()` → `apikey.Init`/`InitBearer` and the
`cli.UseAuth` middleware; env vars (`<PREFIX>_API_KEY`, …) and `--profile` behave identically
under `mcp`. Two gaps to close in the generator (`getAuthInit`, `main.go:1478-1540`):

1. **OAuth2 / OIDC schemes** (currently silently dropped): for `type: oauth2` emit
   `oauth.InitClientCredentials(tokenURL, oauth.Scopes(...))` when the spec declares a
   `clientCredentials` flow, else `oauth.InitAuthCode(clientID?, authorizeURL, tokenURL,
   oauth.Scopes(...))` for `authorizationCode` (PKCE is already implemented in
   `oauth/authcode.go`). Client ID/secret come from profiles (`auth setup`) — the runtime already
   prompts and stores them. `openIdConnect` → document as manual (discovery doc fetch is out of
   scope for v1). Note for stdio MCP: client-credentials works headless; auth-code opens a
   browser on first call — acceptable (token cached in `~/.<app>/cache.json`), but README must
   say "run `my-cli auth setup` + one CLI call before wiring into an MCP client".
2. **Multiple schemes** (`main.go:1525` bails when != 1). This needs runtime work first — the
   naive "emit several Init calls" **panics today**:
   - `apikey.Init`, `apikey.InitBearer`, `oauth.InitClientCredentials`, and `oauth.InitAuthCode`
     all hardcode `cli.UseAuth("", handler)` (`apikey/apikey.go:160`,
     `oauth/clientcredentials.go:97`, `oauth/authcode.go:214`); a second empty-type registration
     hits `panic("register auth type names to use multi-auth")` (`cli/credentials.go:244-245`).
     Add typed variants (`apikey.InitWithType(typeName, name, in, extra...)`,
     `oauth.WithType(typeName)`-style option or `InitClientCredentialsTyped`) and have the
     generator emit those with the security-scheme name as the type.
   - With >1 typed handlers, a user with no profile (env-var-only, the common case) gets
     "no authentication handler configured" — `resolveAuthHandler` (`cli/credentials.go:134-147`)
     only falls back when exactly one handler is registered. Add a default-handler mechanism to
     `cli` (e.g. `cli.SetDefaultAuthType(typeName)`, emitted by the generator for the first
     scheme in the spec's top-level `security` list) so profile-less invocations keep working.
   `auth setup` already supports type selection once handlers are typed.

Never forward an inbound MCP bearer token upstream (confused-deputy rule from the MCP auth spec).
The two credential planes stay fully separate.

### Inbound (MCP client → server)

- **stdio** (default): no auth — process-level trust, per spec.
- **`--http addr`**: three modes via `--http-auth`:
  - `none` (default only when addr is loopback; refuse to bind non-loopback without auth unless
    `--http-auth none --i-know-what-im-doing` style override flag is set).
  - `bearer`: static token from `<PREFIX>_MCP_TOKEN` env; constant-time compare
    (`crypto/subtle`), implemented as an `http.Handler` wrapper.
  - `oauth`: OAuth 2.1 resource server: JWT validation (flags/env: issuer, audience, JWKS URL,
    required scopes) through go-sdk `auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
    ResourceMetadataURL, Scopes})`, plus serving RFC 9728 metadata at
    `/.well-known/oauth-protected-resource` using `oauthex.ProtectedResourceMetadata`
    (`authorization_servers`, `bearer_methods_supported`, `resource`). JWKS-based JWT
    verification needs one small dependency (`github.com/MicahParks/keyfunc/v3` +
    `github.com/golang-jwt/jwt/v5`, or `lestrrat-go/jwx/v3` — implementer's choice, pin latest).
  - `Origin` header check + localhost binding guidance per MCP transport security notes.

## stdio purity (critical correctness detail)

In `mcp` (stdio) mode, stdout carries JSON-RPC. Verified state + required measures:

- Already safe: zerolog writes to `Stderr` (`cli/cli.go:84`), and `--verbose` request/response
  dumps in `LogMiddleware` go through zerolog too (`cli/http.go:111,143`) — stderr, no action
  needed.
- `DefaultFormatter.Format` writes to the package var `cli.Stdout` at call time
  (`cli/formatter.go:169,173`), so `mcpserver.Command` setting `cli.Stdout = cli.Stderr` before
  serving IS effective for any stray formatter output.
- `Root.SetOutput(Stdout)` captures the writer value at Init time (`cli/cli.go:125`) —
  reassigning `cli.Stdout` later does NOT redirect cobra usage/error output. `mcpserver.Command`
  must call `SetOut(cli.Stderr)`/`SetErr(cli.Stderr)` on the root command as well.
- **Real bug to fix**: `oauth/authcode.go:113-114` does `fmt.Println("Open your browser…")` /
  `fmt.Println(url)` straight to `os.Stdout` — under stdio MCP with auth-code auth, the first
  tool call corrupts the JSON-RPC stream. Change to `fmt.Fprintln(cli.Stderr, …)` (Phase 4).
- Add a test that captures stdout during an in-process stdio session and asserts every line is
  valid JSON-RPC.

## Phased implementation (each phase = one PR, independently verifiable)

### Phase 0 — Housekeeping (small)
- Bump `go.mod` `go 1.23.0` → `go 1.25.0`; same in `templates/go.mod.tmpl`.
- `go get github.com/modelcontextprotocol/go-sdk@v1.6.1 github.com/google/jsonschema-go@latest`.
- Delete dead code: `bindata.go` (unreferenced go-bindata relic), `templates/commands.tmpl`
  (legacy monolith, only referenced by bindata), `.travis.yml` (Go 1.11-era; GH Actions is the CI).
- Acceptance: `make verify` green; `git grep -l bindata` empty.

### Phase 1 — Runtime package `mcpserver` (core)
Files: `mcpserver/mcpserver.go`, `mcpserver/command.go`, `mcpserver/mcpserver_test.go`.

```go
package mcpserver

type ArgRoute struct {
    Prop     string // property name in the tool input schema
    WireName string // OpenAPI param name / body property name
    In       string // "path" | "query" | "header" | "body"
    Type     string // "string" | "int64" | "float64" | "bool" | "json"
}

type ToolDef struct {
    Name, Title, Description string
    Group                    string
    InputSchema              json.RawMessage
    OutputSchema             json.RawMessage // nil → omit
    ReadOnly, Destructive, Idempotent bool
    Routes                   []ArgRoute
    Handler                  func(ctx context.Context, args map[string]any) (any, error)
}

func Command(appName, version string, tools func() []ToolDef) *cobra.Command
```

- `Command` builds the `mcp` cobra command (+ `mcp list` subcommand printing the manifest JSON).
- Registration per tool: unmarshal `InputSchema` into `jsonschema.Schema`, `Resolve(nil)` once at
  startup, wrap handler with: raw-args unmarshal → `Resolved.Validate` → route args →
  `Handler(ctx, args)` → result mapping (structuredContent + text; `*cli.HTTPError` → isError;
  panic recovery). Exactly the spike pattern.
- Serve: default `server.Run(ctx, &mcp.StdioTransport{})` with signal-aware ctx;
  `--http` deferred to Phase 5 (flag exists, returns "not yet supported" error).
- Server instructions (`ServerOptions.Instructions`): template string with app name + auth env
  var guidance (passed in from generated code).
- stdio purity measures from the section above.
- Tests (in-memory transports): list tools; happy call; validation-error call; `HTTPError`
  mapping; panic recovery; `--read-only`/`--tools` filtering; stdout-purity test.
- Acceptance: `go test ./mcpserver/` green; package has NO import of `cli/generated` anything.

### Phase 2 — Generator: schema converter + IR (core)
Files: `mcpschema.go` (new, package main), `mcpschema_test.go`, edits in `main.go`.

- `convertSchema(ref *openapi3.SchemaRef, mode inputMode|outputMode) map[string]any` implementing
  the conversion rules table above (depth limit, cycle guard, readOnly/writeOnly stripping).
- `buildToolSpec(op *Operation, openapiOp *openapi3.Operation) (*ToolSpec)` producing: tool name,
  title, description, input schema JSON, output schema JSON, routes, annotations, excluded+reason.
- IR: add to `Operation` (`main.go:62-85`): `MCP *MCPTool` where
  `MCPTool{Name, Title, Description string; InputSchema, OutputSchema string; Routes []ArgRoute;
  ReadOnly, Destructive, Idempotent bool; Excluded bool; ExcludedReason string}`.
  Populate inside `ProcessAPI` next to the existing per-op processing (`main.go:289-475`).
  Keep raw `*openapi3.Operation` access there — do NOT try to rebuild schemas from `BodyField`.
- Tool-name collision pass after all ops collected (deterministic order — paths are already
  sorted, `main.go:264-268`).
- Tests: table tests mirroring `TestBodyFieldTypeCoversCommonShapes` style
  (`main_internal_test.go:327`) covering: nullable scalars, enums, `oneOf`/`anyOf`/`allOf`,
  `$ref` cycles, 3.1 type arrays, param+body collision (`body_` prefix), non-object body,
  readOnly stripped from input, SSE-only exclusion, multipart exclusion, deprecated exclusion,
  naming collisions, annotation mapping per method.
- A fixture-scale test: load `testdata/orq/openapi.json`, assert ≥120 included tools, exactly
  **3 SSE-only exclusions** (the 4 JSON+SSE ops such as `/v2/router/chat/completions` must be
  PRESENT, using the JSON media type), 5 multipart exclusions, every input schema round-trips
  through `jsonschema.Schema` `Resolve` without error (this is the strongest schema-correctness
  gate).
- Acceptance: `go test ./...` green.

### Phase 3 — Templates + wiring (core)
Files: `templates/generated_mcp.tmpl` (new), edits: `templates/generated_register.tmpl`,
`main.go` (`writeGeneratedCommandFiles` `main.go:2589-2625`, `commandTemplateFuncs`
`main.go:2571`), `example-cli/` regenerated.

- `generated_mcp.tmpl` → `cli/generated/mcp_tools.go`:
  - `func mcpTools() []mcpserver.ToolDef` — one entry per included op. Schemas embedded as
    backtick raw strings (add a `rawJSON` template func that compacts JSON and guards against
    backticks in content — escape via `` + "`" + `` splicing).
  - Handler closures per op: pull routed args out of `args map[string]any`:
    - required params → the client func's leading args, which are ALL `string` typed regardless
      of schema type (`generated_client.tmpl:41`) — stringify numbers/bools
      (`strconv.FormatFloat`/`FormatBool`, no `fmt.Sprintf("%v")` float artifacts);
    - optional query/header → `viper.New()` keyed by `Param.CLIName` (what
      `params.Get<Type>(...)` reads);
    - body-routed props + `body` passthrough → build `map[string]any`, `json.Marshal` → the
      `body string` arg — which exists ONLY when `.CanHaveBody`; the closure template must
      branch on `CanHaveBody` or generated code for GET/DELETE ops won't compile;
    then call the existing generated client func (`{{.PublicGoName}}{{.GoName}}`), return
    `decoded, err`.
  - Numeric coercion helper in `mcpserver` (JSON numbers arrive as `float64`; viper handles
    conversion for query/header; body passthrough keeps raw values).
- `generated_register.tmpl`: `Register(root)` additionally calls
  `root.AddCommand(mcpserver.Command(appName, version, mcpTools))`. App name/version verified
  available at that point: `viper.Set("app-name", …)` happens in `cli.Init` (`cli/cli.go:231`)
  and `main.tmpl` calls `Init` before `generated.Register`. The template needs two new imports:
  `viper` and the `mcpserver` package (it currently imports only `bartolocli` + `cobra`).
- Regenerate `example-cli` (note: `example-cli/openapi.go` is a curated committed sample in the
  old single-file layout; either regenerate it into the same single-file shape by hand-porting the
  mcp_tools content, or — better — migrate example-cli to the current scaffold layout in this
  phase so it's actually representative; migrating is preferred, adjust `main_test.go` build path).
- Acceptance: `make smoke` green (extend smoke: after build, run `./example mcp list` and assert
  it prints ≥1 tool with a `"inputSchema"` key); `TestGenerateFromJSONFixtureBuildsCLI`
  (`main_internal_test.go:271`) still builds the Orq CLI **with** MCP files.

### Phase 4 — Outbound OAuth2 + multi-scheme (auth)
Files: `main.go` (`getAuthInit`/`getRequiredSecurityScheme` `main.go:1478-1540`), tests,
`templates/readme.tmpl` auth section, `oauth/authcode.go` (timeout fix).

- Emit `oauth.InitClientCredentials` / `oauth.InitAuthCode` per the auth design section; emit
  multiple `UseAuth`-backed inits when the spec has several schemes.
- Runtime prerequisites in this phase (see auth design section): typed registration variants for
  `apikey`/`oauth` inits + `cli.SetDefaultAuthType` default-handler mechanism — without these,
  multi-scheme emission panics at startup and env-var-only users regress.
- `getAuthDocFromSpec` (`main.go:2315-2344`) extended so the generated README documents OAuth
  profile setup (`auth setup`, client-id/client-secret keys). Note its current `default:` branch
  labels anything unrecognized as "API key" — new `oauth.Init*` emissions would produce an
  actively WRONG README until this is extended; treat as part of this phase, not optional polish.
- Fix the known runtime gaps in `oauth/authcode.go`: add a timeout (5 min) to the local-server
  code wait (`authcode.go:119-120` — bare `<-codeChan` today), and change the
  `fmt.Println("Open your browser…")` / `fmt.Println(url)` at `authcode.go:113-114` to write to
  `cli.Stderr` (stdio-MCP stream purity; see stdio purity section).
- Tests: unit tests for the new emit paths (specs with oauth2-CC, oauth2-authcode, apiKey+bearer
  combos); generated-code compile test with a two-scheme spec fixture.
- Acceptance: `make verify` green; Orq fixture (bearer) output unchanged.

### Phase 5 — Streamable HTTP transport + inbound auth
Files: `mcpserver/httpserver.go`, `mcpserver/httpauth.go`, tests.

- `--http addr` → `http.Server` with `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server
  { return server }, nil)`; graceful shutdown on SIGINT/SIGTERM; `Origin` allow-list flag.
- `--http-auth bearer|oauth|none` per the auth design section; RFC 9728 metadata endpoint in
  `oauth` mode; refuse non-loopback bind with `none` absent explicit override.
- Stateless-friendly: one shared `*mcp.Server` (tools are stateless closures; per-session state
  isn't used).
- Tests: httptest + go-sdk `StreamableClientTransport` round trip; 401 + `WWW-Authenticate`
  containing `resource_metadata` in oauth mode; static bearer accept/reject; metadata endpoint JSON.
- Acceptance: `go test ./mcpserver/` green.

### Phase 6 — End-to-end validation + docs
Files: `main_test.go`, `Makefile`, `templates/readme.tmpl`, `README.md`, `CHANGELOG.md`,
`templates/env.example.tmpl`.

- E2E test (extends the existing pattern at `main_test.go:18-81`): reuse the `:8005` echo server;
  build the example CLI; connect a go-sdk client via `mcp.CommandTransport` to `example mcp`;
  assert: `ListTools` exposes `echo` with the expected input schema (JSONEq), `CallTool` returns
  `structuredContent` matching the echo response, invalid args → `isError` without hitting the
  HTTP server, upstream 4xx (add an error route to the test server) → `isError` with
  `{"status": …}` structured payload, and auth env var (`EXAMPLE_API_KEY` style) flows through to
  the upstream request header when the fixture spec declares bearer auth.
- `make smoke` additions: `example mcp list` manifest assertion (done in Phase 3) + a scripted
  stdio round trip (`printf` an `initialize` + `tools/list` JSON-RPC into `example mcp` and grep
  the response) as a no-Go-test-harness sanity check.
- Docs: README "MCP" section (what's generated, client config snippets for Claude Code /
  Claude Desktop, auth modes, filtering flags, excluded-operations note); generated
  `readme.tmpl` gains the same per-CLI section including the concrete `command`/`args` JSON
  config block; CHANGELOG entry; `env.example.tmpl` gains `<PREFIX>_MCP_TOKEN` comment.
- Optional manual check for the implementer: `npx @modelcontextprotocol/inspector ./example mcp`.

## Non-goals for v1 (explicit)

- SSE/streaming operations as streaming tools (excluded; revisit with progress notifications).
- Multipart/file-upload tools (excluded; needs file-path vs base64 design).
- MCP resources/prompts primitives (tools only; the spec-as-resource idea is a nice later add).
- `$defs`-based schema sharing; OIDC discovery for outbound auth; DCR for inbound OAuth.
- Elicitation, sampling, tasks (2025-11-25 experimental) — none needed for API bridging v1.

## Risks / gotchas for implementers

1. **go.mod bump ripples**: go 1.25 directive + go-sdk deps land in every generated CLI's
   `go.mod` via `go mod tidy`. Keep `templates/go.mod.tmpl` in lockstep.
2. **Template backtick escaping** for embedded JSON schemas — the `rawJSON` helper must split on
   backticks; test with a schema containing a backtick in a description (Orq spec has markdown
   descriptions).
3. **`json` template collisions**: `text/template` — remember schemas must be compacted at
   generation time, not template-escaped as Go strings with `escapeStr` (which would bloat and
   break on newlines in descriptions).
4. **Generated client funcs with required params**: signature order is (requiredParams...,
   params[, body]) — `body` only when `.CanHaveBody`, and every required param is a `string`
   arg even for integer/boolean schemas. The closure template must mirror
   `generated_client.tmpl:41` exactly.
5. **Viper key names**: query/header args must be set under `Param.CLIName` (what
   `params.GetString(...)` reads), not the wire name.
6. **Orq spec scale**: 142 ops × embedded schemas ≈ a large `mcp_tools.go`. `go/format.Source`
   on multi-MB files is fine, but keep schemas compact (no indent). If the file exceeds ~5 MB,
   split per group like `<group>_commands.go` does.
7. **Do not break the CLI surface**: `HTTPError` must render the exact `"HTTP %d: %s"` string;
   `main_test.go` and smoke assert existing behavior.
8. **`example-cli` layout migration** (Phase 3) changes `main_test.go`'s build target — update the
   `go build -o … ./example-cli` path accordingly.

## Definition of done (whole feature)

- `make verify` green (smoke now covers `mcp list` + stdio round trip).
- E2E Go test: real MCP client ↔ generated server ↔ real HTTP API, covering success, validation
  error, upstream error, and auth propagation.
- Orq fixture: generated CLI builds; ≥120 tools; all schemas resolve under jsonschema-go;
  exclusion report lists the 3 SSE-only + 5 multipart ops; the 4 JSON+SSE ops are present as
  JSON tools.
- Docs updated (root README, generated README template, CHANGELOG).
