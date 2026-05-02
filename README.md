# Bartolo

Bartolo turns an OpenAPI schema into a production-ready Go CLI.

It is designed for teams that want more than a thin REST wrapper. The generated CLIs are readable for humans, stable for agents, and practical to ship inside real product workflows.

## Why Bartolo

- Generates grouped commands from tags or `x-cli-group`, so large APIs feel like product CLIs instead of flat path dumps.
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

## Schema Shaping

Bartolo will synthesize a decent CLI from a plain schema, but it gets significantly better when the schema carries product intent.

| Extension | Purpose |
| --- | --- |
| `x-cli-aliases` | Add command aliases for operations. |
| `x-cli-description` | Override CLI-facing help text. |
| `x-cli-group` | Force an operation into a higher-level noun. |
| `x-cli-hidden` | Hide a path or operation from normal help. |
| `x-cli-ignore` | Exclude a path, operation, or parameter entirely. |
| `x-cli-name` | Override a generated CLI name for an API, operation, or parameter. |
| `x-cli-waiters` | Add polling-based waiter commands and follow-up flags. |

Bartolo also groups operations automatically from:

- the first OpenAPI tag
- `x-cli-group`
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
