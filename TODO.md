# Bartolo TODO

This product-quality pass is complete.

## Completed

- [x] Split generated projects into `cmd/<app>`, `cli/generated`, and `cli/custom`.
- [x] Stop emitting one giant generated file when the schema produces multiple command groups.
- [x] Keep generated code isolated so reruns do not overwrite user-owned custom commands.
- [x] Replace the old registration stub with a clearer generated/custom extension contract.
- [x] Remove ugly temporary-style generated filenames from the scaffold.

- [x] Add interactive `auth setup` / `auth login` flow for generated CLIs.
- [x] Improve `doctor` with safe `--fix` support for missing auth.
- [x] Add shell completion generation for bash, zsh, fish, and powershell.
- [x] Add persisted server management commands for generated CLIs.
- [x] Improve request body UX with `--from-file`, `--stdin`, `--example`, and clearer input help.
- [x] Generate typed top-level body flags for simple request schemas while keeping shorthand/stdin for complex bodies.

- [x] Emit per-generated-CLI examples and a better starter README.
- [x] Persist Bartolo/project metadata such as app version, Bartolo version, and last spec path in `.bartolo.json`.
- [x] Add `bartolo sync` / `bartolo upgrade` for refreshing scaffold-owned files and regenerating from the saved spec.
- [x] Keep generated projects stocked with local developer defaults such as make targets, install scripts, env example, and editor defaults.

- [x] Expand smoke coverage to validate init, generate, sync, build, install, and shell completions.
- [x] Keep `make verify` and `make smoke` as the single-command quality gates.
- [x] Add targeted tests for auth/profile persistence, input helpers, JSON fixture generation, server commands, and completions.

- [x] Move the old `orq/` fixture into `testdata/orq/openapi.json`.
- [x] Delete the `orq/` folder once equivalent JSON-schema coverage exists elsewhere in the repo.
