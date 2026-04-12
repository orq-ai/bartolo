# Changelog

All notable changes to Bartolo will be documented in this file.

The project was restarted on 2026-04-09 as a new public release stream under the Bartolo name.

## 2026-04-12

- Rebranded the project to `bartolo` with the module path `github.com/orq-ai/bartolo`.
- Added interactive `bartolo init`, including prompts for CLI name and default output format.
- Added generated CLI support for `default-format`, `doctor`, and raw `request`.
- Improved string escaping in generated Go code so large real-world specs compile cleanly.
- Added `make smoke` and `make verify` workflows for one-command local validation.
- Rewrote the root README around Bartolo's product positioning and operator workflow.

## 2026-04-11

- Added grouped command inference from tags, `x-cli-group`, and path-based fallbacks for untagged operations.
- Improved grouped verb synthesis for commands like `list`, `get`, `create-version`, and `query`.
- Added generated per-CLI README files with auth setup, first-run checks, and grouped command examples.
- Added predictable API key and bearer env var support for generated CLIs.

## 2026-04-10

- Added OpenAPI JSON input support alongside YAML.
- Added compatibility normalization for common OpenAPI 3.1 schema shapes such as numeric `exclusiveMinimum` and `exclusiveMaximum`.
- Added formatter and matcher regression tests around agent-oriented output paths.

## 2026-04-09

- Started Bartolo as an agent-friendly OpenAPI-to-CLI generator focused on publishable product CLIs instead of raw endpoint wrappers.
