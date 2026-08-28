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
- `auth add-profile --server` and `auth setup --server` bind a profile to its own API base URL, so staging, self-hosted, and local profiles resolve without repeating the flag.
- `request` provides a raw escape hatch for unmodeled endpoints.
- `default-format` shows or persists the preferred default output format.
- `--json`, `--output-format`, and `-j`/`--jmespath` make automation and projection straightforward.
- Generated flags never shadow a global: a body field or parameter named after one (`raw`, `profile`, `output-format`, ...) is exposed as `--body-<name>` or `--param-<name>`.
- Grouped nouns like `prompts`, `files`, or `human-evals` feel closer to a product CLI than a path translator.
- String parameters with an `enum` list their values in `--help` and complete them in the shell.

## Schema Shaping

Bartolo will synthesize a decent CLI from a plain schema, but it gets significantly better when the schema carries product intent.

| Extension | Purpose |
| --- | --- |
| `x-cli-aliases` | Add command aliases for operations. |
| `x-cli-description` | Override CLI-facing help text. |
| `x-cli-group` | Force an operation into a higher-level noun. |
| `x-cli-hidden` | Hide a path or operation from normal help. |
| `x-cli-ignore` | Exclude a path, operation, or parameter entirely. |
| `x-cli-list-fields` | Set the default columns for an interactive collection response. |
| `x-cli-name` | Override a generated CLI name for an API, operation, or parameter. |
| `x-cli-no-validate` | Set to `true` to skip the client-side `enum`/`format` check for a parameter, for a schema that is stricter than the API it describes. |
| `x-cli-help-section` | Put a top-level command under a titled section in `--help`. |
| `x-cli-waiters` | Add polling-based waiter commands and follow-up flags. |

For example, a collection operation can define the default interactive
columns while leaving the complete response available through `--json` or a
pipe:

```yaml
paths:
  /widgets:
    get:
      x-cli-list-fields:
        - id
        - name
        - status
```

Without the extension the columns are inferred from the response: nested
objects and arrays are skipped, long values are truncated, and columns that do
not fit the terminal are dropped from the right.

Bartolo also groups operations automatically from:

- the first OpenAPI tag
- `x-cli-group`
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
		DefaultOutputFormat: "json",
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
