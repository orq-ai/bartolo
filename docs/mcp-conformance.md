# MCP conformance

Bartolo-generated servers are checked against the official
[MCP conformance suite](https://github.com/modelcontextprotocol/conformance)
on every pull request: `make conformance` (CI job `mcp conformance`) generates
[`conformance/openapi.yaml`](../conformance/openapi.yaml) with every option of
the `mcp` block on, serves it over Streamable HTTP, and runs the suite's
**requirement sets** — exactly what a spec revision requires — for
`2025-11-25` and `2026-07-28`.

The suite targets an SDK's "everything" fixture server. Each check a
generated server cannot pass is listed, with its reason, in
`conformance/expected-failures-<revision>.yaml`: fixture prompts and resources
an API does not have, or features a generated server does not use by design
(below). Any other failing check fails CI, and so does a listed check that
starts passing. Three fixture tools in
[`conformance/custom`](../conformance/custom/register.go) exercise bartolo's own
result, error, and JSON Schema handling where the suite needs a specific tool
name.

| Requirement set | Passing checks | Baselined |
| --- | --- | --- |
| `2025-11-25` | 46 | 22 |
| `2026-07-28` | 125 | 29 |

Tasks (SEP-2663) and other extensions run but are not scored: SEP-1730 makes
extensions optional for every SDK tier.

## SEP support

**✅ Supported**, with how it is verified.

| SEP | | Verified by |
| --- | --- | --- |
| [2575](https://modelcontextprotocol.io/seps/2575-stateless-mcp) | Stateless MCP: `server/discover`, per-request `_meta` | conformance `server-stateless` (24 checks) |
| [2567](https://modelcontextprotocol.io/seps/2567-sessionless-mcp) | Sessionless: no `Mcp-Session-Id`, stateless HTTP | conformance `server-stateless`, `dns-rebinding-protection` |
| [2549](https://modelcontextprotocol.io/seps/2549-TTL-for-list-results) | `ttlMs` / `cacheScope` on list and discover results | conformance `caching` (7 checks); `mcp.list_ttl_seconds` |
| [2243](https://modelcontextprotocol.io/seps/2243-http-standardization) | `Mcp-Method` / `Mcp-Name` and `x-mcp-header` parameters | conformance `http-header-validation`, `http-custom-header-server-validation`; `x-bartolo-mcp-header` |
| [2106](https://modelcontextprotocol.io/seps/2106-json-schema-2020-12), [1613](https://modelcontextprotocol.io/seps/1613-establish-json-schema-2020-12-as-default-dialect-f) | Tool schemas are JSON Schema 2020-12, passed through intact | conformance `json-schema-2020-12`, `tools-list` |
| [2164](https://modelcontextprotocol.io/seps/2164-resource-not-found-error) | Resource not found is `-32602` | conformance `sep-2164-resource-not-found` |
| [1303](https://modelcontextprotocol.io/seps/1303-input-validation-errors-as-tool-execution-errors) | Invalid arguments are tool errors, not protocol errors | `mcpserver` tests; conformance `tools-call-error` |
| [986](https://modelcontextprotocol.io/seps/986-specify-format-for-tool-names) | Tool name format | generator sanitizes, runtime validates custom tools |
| [2577](https://modelcontextprotocol.io/seps/2577-deprecate-roots-sampling-and-logging) | Roots, sampling, logging unused; logging neither declared nor accepted | `mcpserver` tests |
| [2260](https://modelcontextprotocol.io/seps/2260-Require-Server-requests-to-be-associated-with-Client-requests) | Server requests tied to client requests | the server sends none |
| [1319](https://modelcontextprotocol.io/seps/1319-decouple-request-payload-from-rpc-methods-definiti) | Request payloads decoupled from methods | Go SDK |
| [985](https://modelcontextprotocol.io/seps/985-align-oauth-20-protected-resource-metadata-with-rf) | RFC 9728 protected-resource metadata | `--http-auth oauth`; `mcpserver` tests |
| [973](https://modelcontextprotocol.io/seps/973-expose-additional-metadata-for-implementations-res) | Server title, description, website, icons | `mcp.title`, `mcp.website_url`, `mcp.icons`; `mcpserver` tests |
| [2127](https://modelcontextprotocol.io/seps/2127-mcp-server-cards) | Server Card at `<endpoint>/server-card` | `mcp.server_card`; served cards validated against the official schema |
| [414](https://modelcontextprotocol.io/seps/414-request-meta) | W3C trace context from `_meta` (or HTTP headers) reaches the API | `mcp.trace_context` (default on); `mcpserver` tests |

**➖ Not used by design.** A generated server turns an API into tools.

- **2322, 1577, 1330, 1034, 1036.** It never asks the client for anything: no multi round-trip requests (2322), sampling (1577), or elicitation (1330, 1034, 1036).
- **Completion.** It has no prompts or resource templates to complete, and declares prompts and resources with none to list.
- **1699.** SSE polling is gone in `2026-07-28`, and the server is stateless.

**❌ Not implemented (optional extensions).** Extensions are optional (SEP-1730) and the server declares none.

- **Tasks** (2663, previously 1686).
- **MCP Apps** (1865).
- **Skills** (2640).
- **The extensions framework** (2133).

**Not server requirements.**

- **Client or authorization-server side:** 1046, 2468, 2207, 991, 990, 1024.
- **Process and governance:** 2596, 2484, 2149, 2148, 2085, 1850, 1730, 1302, 994, 932.

## Configuring the server: the `mcp` block

`.bartolo.json` takes an optional `mcp` block; `bartolo generate` validates it
with the checks the server runs at startup and renders it into the generated
`mcpOptions()`. Every field is optional.

```json
{
  "mcp": {
    "title": "Acme",
    "description": "Acme's API as MCP tools.",
    "website_url": "https://acme.example",
    "icons": [{ "src": "https://acme.example/icon.png", "mime_type": "image/png", "sizes": ["48x48"] }],
    "list_ttl_seconds": 300,
    "trace_context": true,
    "protocol_versions": ["2026-07-28", "2025-11-25"],
    "server_card": {
      "name": "example.acme/api",
      "endpoint": "/mcp",
      "public_url": "https://mcp.acme.example/mcp",
      "repository": "https://github.com/acme/mcp"
    }
  }
}
```

| Key | SEP | Default | |
| --- | --- | --- | --- |
| `title`, `description`, `website_url`, `icons` | 973 | none | Server identity shown by clients. Icons are `https:` URLs or `data:image/` URIs. |
| `list_ttl_seconds` | 2549 | `300` | How long clients may cache `server/discover` and `tools/list`. `0` means always refetch. |
| `trace_context` | 414 | `true` | Propagate `traceparent`, `tracestate`, `baggage` to the API. |
| `protocol_versions` | 2575 | all the SDK supports | Narrow the accepted versions, e.g. `["2026-07-28"]`. |
| `server_card.name` | 2127 | | Reverse-DNS `namespace/name`. Requires `description` (1-100 characters). |
| `server_card.endpoint` | 2127 | `/mcp` | The MCP path; the card is served at `<endpoint>/server-card`. |
| `server_card.public_url` | 2127 | from the request | The endpoint's public URL, else built from `X-Forwarded-Proto`/`X-Forwarded-Host`. |
| `server_card.repository` | 2127 | none | Source repository URL. |

The same options exist in code (`mcpserver.SetMetadata`, `SetListTTL`,
`SetTraceContext`, `SetProtocolVersions`, `PublishServerCard`) and can be
overridden from `cli/custom` through `mcpserver.Use`.

## Routing parameters through headers: `x-bartolo-mcp-header`

Mark an OpenAPI parameter or top-level request-body property to have MCP
clients mirror its value into an `Mcp-Param-<Name>` HTTP header (SEP-2243), so
gateways and load balancers can route on it without parsing the body:

```yaml
parameters:
  - name: region
    in: query
    schema: { type: string }
    x-bartolo-mcp-header: Region
```

The value must be a single `string`, `integer`, or `boolean` type and a valid
HTTP header name, unique within the operation; anything else fails
`bartolo generate`. The server rejects a request whose header and argument
disagree.
