# Changelog

Notable changes to Bartolo up to the switch to automatic releases.

The project was restarted on 2026-04-09 as a new public release stream under the Bartolo name.

This file is frozen. From the first automatic release onward, notes live on the
[GitHub Releases](https://github.com/orq-ai/bartolo/releases) page, generated
from the merged pull requests. The entries below cover the versions that predate
that, including the two that were bumped in the source but never tagged.

## 2026-08-26 (v0.4.8, never tagged)

- **Never tagged.** See the v0.4.7 note below: neither bump was ever tagged, and
  both sections ship in the first automatic release.
- `auth list-profiles` no longer prints stored credentials. Any profile field whose name looks like a credential is now shown with its first and last four characters around a fixed-width `********` middle (`sk-o********mnop`), so keys no longer leak into terminal scrollback, CI logs, screen recordings and support transcripts. Which fields count is decided by the same predicate that decides whether `auth setup` prompts for a field without echo.
- `auth list-profiles` now goes through the response formatter instead of writing a table straight to stdout, so `--json` and `-o yaml` return the profile list in the requested format like every other command.
- Added interactive tables for generated collection commands. Use the
  operation-level `x-cli-list-fields` extension to choose and order the
  default columns; `--json`, explicit output formats, and piped output remain
  machine-readable.

  Without `x-cli-list-fields` the columns are picked from the response:
  nested objects and arrays are skipped because serializing them into a cell
  makes the table unreadable, cells are truncated, recognizable identifiers
  (`id`, `name`, `status`, ...) come first, and the remaining columns are
  dropped from the right until the table fits the terminal. An empty
  collection prints `No results.` instead of the raw envelope.

  Envelope fields print as a single line below the table, showing `<shown> of
  <total>` when the response carries a total. Paging bookkeeping (`limit`,
  `offset`, cursors) and type discriminators such as `"object": "list"` are
  left to `--json`.

  Collections wrapped in a key named after the resource (`domains`,
  `schedules`, `values`) are recognized as well as the conventional `data` and
  `items` envelopes, as long as the response has exactly one array of objects.
- Fixed a name collision that made a generated CLI fail to compile when two
  operations in a spec share an `operationId`.
- Added `x-cli-help-section`, which puts a top-level command under a titled `--help` section straight from the schema. Set it on a tag (or on a single operation, which wins) and the generated CLI calls `cli.HelpSection` before registering the command. Regenerating a spec with a new tag now lands the command in the right section instead of relying on a hand-maintained mapping in the consuming CLI. Once any command has a section, `cli.Execute` sweeps the rest into a trailing `Other` section, so cobra's `Additional Commands` block does not appear alongside named ones.

### Security

- **`--verbose` no longer prints secrets.** The HTTP header logger redacted nothing, so `Authorization: Bearer <key>` and cookies were written to the debug log. Sensitive headers now render as `[REDACTED]`, matched case-insensitively. The `--verbose` configuration dump also used its own inline secret check that missed `key`; it now uses the shared `looksSensitiveKey` predicate.
- **The credentials file is written atomically at `0600`.** `viper.WriteConfigAs` left the long-lived API-key file world-readable at `0644`. Writes now go to a temporary file in the same directory, chmoded to `0600`, and renamed over the target, so an interrupted write cannot destroy existing profiles. Unix only: Go's `os.Chmod` on Windows toggles the read-only attribute and nothing else, so there the file keeps whatever its directory's ACL grants.
- **API keys can be supplied from a file or stdin.** `auth add-profile` accepts `--<key>-file <path>` (use `-` for stdin) so CI pipelines can supply secrets without putting them on the command line. Positional values still work for backward compatibility but print a warning for sensitive keys. The interactive prompt rejects empty values.
- **A wider set of field names counts as a credential.** `passphrase`, `client_credential` and `signature` were typed in the clear, printed by `--verbose` and shown by `auth list-profiles`, because the shared predicate matched only `key`, `token`, `secret` and `password`. Matching is by substring and deliberately over-redacts: a benign field masked in a log costs nothing, a key printed to one cannot be taken back.
- **Destructive `delete` commands now confirm.** Generated delete commands prompt before running and gain a `--force` flag (no shorthand); a non-interactive shell without `--force` refuses rather than deleting silently. The prompt names the target (`widgets delete abc`), the gate runs after `--example`, and a prompt failure returns a non-zero exit code through the normal `ExecuteContext`/`ExitCodeFor` contract. The built-in `request` command is also gated for DELETE methods.
- **`--verbose` no longer prints a key carried in the URL.** An API key can be sent as a query parameter rather than a header (`apikey.LocationQuery`), and the request line was logged verbatim. Sensitive query parameters now render as `[REDACTED]`, matched by the same predicate as headers, which also grew the wire-only names a consumer-supplied auth handler can send (`X-Auth`, `X-Signature`, `X-Session`).
- **`auth add-profile` no longer hangs in CI.** A missing key value prompted whenever the positional argument was absent, and `survey` reads stdin directly, so an open-but-idle pipe blocked forever. The terminal check now happens before the prompt, so a pipe exits `2` with the usage message suggesting `--<key>-file` instead of the positional.
- **Survey prompts go to stderr.** All interactive prompts (confirmation, auth setup, add-profile) now render to stderr so they never corrupt piped stdout.
- **TTY detection uses `go-isatty` consistently.** The previous `x/term` dependency missed Cygwin/mintty terminals on Windows; all TTY checks now use the same `mattn/go-isatty` predicate the rest of the package already used.

  **`force` is reserved on every command, not just `Delete` ones.** A spec parameter or body field named `force` is exposed as `--param-force` / `--body-force` everywhere, including where no `--force` flag exists to collide with. Reserved names are deliberately uniform: one name resolves to one flag across the whole CLI, and the runtime redoes the resolution for CLIs generated before a fix, so a per-method rule would need both halves to agree about a command's method or the flag would be registered under one name and read under another.

### Breaking (runtime library)

- **`CredentialsFile` no longer embeds `*viper.Viper`.** The viper instance is unexported to prevent bypassing `writeCredentials` and creating a `0644` file. Accessor methods (`Set`, `GetStringMap`, `GetStringMapString`, `SetConfigName`, `AddConfigPath`, `ReadInConfig`) are provided. If your code calls promoted viper methods not in this list, add them to `CredentialsFile`.
- **`ConfirmDestructive` returns `error` instead of `bool`.** Generated delete commands now use `RunE` instead of `Run`. Returns `nil` to proceed, `*UsageError` for non-interactive refusal, `ErrDestructiveRefused` for an interactive No, or the prompt error.
- **`AddForceFlag` no longer registers the `-f` shorthand.** Consumer code claiming `-f` through `cli.AddFlag` no longer panics pflag at startup.
- **`resolveProfileValue` takes a `*cobra.Command` and returns `(string, error)`.** The old `(value, ok)` signature hid errors and used `exitFunc` to exit mid-handler.

## 2026-08-26 (v0.4.7, never tagged)

- **Never tagged.** The constant was bumped for v0.4.7 and again for v0.4.8, but
  no tag exists for either, so `go install ...@latest` never saw them. Both
  sections ship in the first automatic release instead. This is the drift the
  automatic releases exist to prevent.
- Added per-profile servers. `auth add-profile --server <url>` and `auth setup --server <url>` bind an API base URL to the profile, and generated commands resolve it automatically, so staging, self-hosted and localhost profiles no longer need the flag on every call. Only an explicit `--server` on that invocation binds one — an environment variable or a persisted default is never silently baked into a new profile. Profiles saved without a server keep using the generated default.
- Reordered server resolution so a more specific source always wins: an explicit `--server` flag, `<PREFIX>_SERVER` environment variable or programmatic override, then the active profile's bound server, then the `server set` default, then `server-index` and the generated default. Previously a `server set` from months earlier silently outranked every profile.

  **The `server set` default moved from the `server` key in `config.json` to `server-default`**, because sharing `server` with the flag is what made the persisted value outrank everything: viper cannot tell the two apart once merged. A config file written by an older version still resolves, and is rewritten the next time `server set` or `server clear` runs.

- **Breaking (runtime library)**: removed the deprecated `cli.InitCredentials`, `cli.ProfileKeys` and `cli.ProfileListKeys`. They were superseded by `cli.UseAuth`, which is what the generator has emitted for some time, and they carried a second copy of `add-profile` and `list-profiles` that never gained per-profile servers and still panicked on a profile missing a field. `cli.UseAuth` is the replacement.
- **Breaking (runtime library)**: `cli.RunAuthSetup` and `cli.saveAuthProfile` take a server argument. `cli.ResolveServer()` now applies the full precedence above; the short-lived `ResolveServerFor` is gone, since `RegisterServers` already gives the runtime the OpenAPI defaults. Generated clients call `ResolveServer()`, which also bounds-checks `server-index` — the inlined lookup they used before panicked on an out-of-range index.
- Added `cli.SelectedServer()`, the registered OpenAPI server at the configured index with no overrides applied.
- Surfaced servers in `auth list-profiles` (a column per profile) and in `doctor` (`config.profile_server`, `config.server_default`). `list-profiles` no longer panics on a profile that is missing a column.

  **Regenerate to pick up per-profile servers in API commands.** Generated client code is written into the consuming repo, so bumping the dependency alone updates the built-in `doctor`, `request` and `server` commands but leaves generated commands on the old inlined resolution — `doctor` would report the profile's server while requests still went to the default.

## 2026-08-13 (v0.4.6)

- Bumped Bartolo to v0.4.6.
- Added `cli.ExecuteContext`, which is `cli.Execute` with a caller-supplied context. A CLI that cancels in-flight requests on SIGINT/SIGTERM needs a context, and the only way to get one was `cli.Root.ExecuteContext(ctx)` — which skips the exit-code contract, since the unknown-subcommand check, the silenced-then-reprinted usage block and the usage exit code all live in `cli.Execute` rather than on the root command. Such a CLI printed the group's help and exited `0` on `<cli> <group> <unknown>`, and exited `1` instead of `2` on every usage error.

## 2026-08-07 (v0.4.5)

- Bumped Bartolo to v0.4.5.
- **Renamed the global JMESPath flag from `-q, --query` to `-j, --jmespath`.** `query` is the single most common field name in a request body, and the collision below was unfixable while the CLI owned the name. Endpoints now keep the obvious `--query` flag, and the filter every command advertises is `-j, --jmespath`. The `-q` shorthand is gone with the name it abbreviated.

  ```
  orq traces search --from ... --to ... -j 'data[0].trace_id'          # was -q
  orq traces search --from ... --to ... --jmespath 'data[0].trace_id'  # was --query
  orq traces search --from ... --to ... --query 'checkout'             # now the request body's query field
  ```

  The viper key and its environment variable follow the flag (`ORQ_QUERY` -> `ORQ_JMESPATH`).

  `--json` deliberately has no shorthand, and must not gain one: `-j` is easily read as short for `--json`, and because `--jmespath` takes a value a stray `-j` consumes the following argument rather than failing cleanly. A test guards this.

- Fixed generated parameter and body-field flags shadowing global flags. Cobra merges the root's persistent flags into a command with pflag's `AddFlagSet`, which skips any name the command already uses, so a body field named `query` did not just win `--query` — it removed the global `-q, --query` flag from that command entirely. `-q` failed with `unknown shorthand flag: 'q' in -q`, and `--query` silently sent the user's JMESPath expression to the server as a body field. The same applied to `--json`, `--output-format` (and `-o`), `--profile`, `--raw`, `--server` and `--verbose`, and a body field named `example`, `stdin`, `from-file` or `help` made pflag panic at startup.

  Renaming the global fixes `query` itself; the rest are handled generically. A colliding flag is now renamed rather than dropped: a body field is exposed as `--body-<name>` and a parameter as `--param-<name>`, both still reading and writing the original field name on the wire. The rename is listed in the command's long help, annotated on the flag, and printed as a generator warning. A parameter that collides with a body field on the same command is renamed too, since registering the same flag twice panics.

  `cli.ResolveGeneratedFlagName` applies the same renaming at runtime, so an existing CLI is fixed by bumping its `github.com/orq-ai/bartolo` dependency even before it is regenerated. Regenerate to also pick up the corrected help text.

- Reworked `--example` from a dead flag into a working one with new semantics: it now **prints** an example request body to stdout and exits `0` without sending a request, instead of using the example as the request body. Round-trip it: `<cli> <cmd> --example > body.json`, edit, `<cli> <cmd> --from-file body.json`. The old behavior had never worked in practice — examples were only extracted from media-type-level `example`/`examples` in the spec, which most operations lack, so nearly every command failed with "no generated body example is available".
- Added schema-based example synthesis: when an operation has no curated media-type-level example, one is generated by walking the request schema, using each property's `example`, then `default`, then first `enum` value, then a type/format-based placeholder. Required properties are always included; optional properties only when they carry an explicit example or default. Recursion is bounded (depth cap, first branch only for `oneOf`/`anyOf`, single array item, cycle detection).
- `--example` is now only registered on commands that actually have an example body; the generic `request` command no longer advertises it (it has no schema to synthesize from). Curated string/array media-type examples are no longer dropped.
- **Breaking (runtime library)**: `cli.GetBody` lost its `examples []string` parameter; new `cli.AddExampleFlag` and `cli.PrintBodyExample` back the flag. Generated code and the runtime version move together via regeneration.

- Fixed non-reproducible generator output. `getRequestInfo` picked a request body example and a media type by ranging over maps, so regenerating from an unchanged spec produced a different `Example` string on every run. Both are now selected in sorted order.

- Fixed error paths exiting `0`. Generated `main.go` discarded the error from `Root.Execute()`, so cobra-level failures (unknown command, unknown flag) printed an error and reported success, while operation-level failures exited `1` via `log.Fatal`. Added `cli.Execute`, which returns a process exit code: `0` on success, `2` for usage errors, `1` for runtime failures.
- Fixed `<cli> <group> <unknown>` printing the group's help and exiting `0`. Cobra only rejects unknown commands at the root, and short-circuits a command without a handler to "show help" before argument validation runs, so group commands now reject an unknown subcommand explicitly.
- Fixed error output going to stdout. `Root` used the deprecated `SetOutput`, which points both streams at stdout, so errors corrupted piped output. Errors now go to stderr and stdout stays clean.
- Fixed body-taking commands hanging forever on an idle stdin. Stdin was read whenever it was not a TTY, even when the body was already complete, so a command handed an open but idle pipe blocked indefinitely with no output and no timeout — which is exactly what CI steps, task runners and `subprocess.Popen` / `child_process.spawn` hand a child by default. Stdin is now only read when it can actually contribute: a redirect from a regular file is always read, a pipe is read when nothing else supplied the body, and a pipe with no data pending is skipped once `--from-file`, shorthand or the generated field flags have supplied one. `--stdin` still forces an unconditional blocking read. Added `cli.GetBodyWithFlags`, which resolves the body and applies the typed flags in one call so the decision can see every source; `cli.ApplyBodyFlags` is unchanged for existing callers.
- Fixed an invalid `-o` / `--output-format` value being silently ignored. An unknown format fell through to JSON and exited `0`, so a typo like `-o jsonn` produced output in a format the caller never asked for while reporting success. The value is now validated wherever it comes from — flag, environment variable, or config file — and an unknown one exits `2` with `--output-format: "jsonn" is not one of [json, yaml, toon]`, matching how generated body enum flags already report a bad value. Values are normalized case-insensitively, so `-o YAML` now renders YAML instead of quietly falling back to JSON. `default-format <bad>` and `bartolo init --default-format <bad>` are rejected the same way.

**Existing generated CLIs must update `main.go`** — Bartolo only writes it at `init` time and will not overwrite it on regeneration:

```diff
-	bartolocli.Root.Execute()
+	os.Exit(bartolocli.Execute())
```

**The stdin fix only takes effect once commands are regenerated.** Generated commands previously called `GetBody` before `ApplyBodyFlags`, so the body resolver could not see the typed flags; regeneration replaces both calls with a single `GetBodyWithFlags`.

## 2026-07-23 (v0.4.4)

- Released as v0.4.4 without a version bump — `bartoloVersion` stayed at `0.4.3` in the tagged commit. Consumers should pin v0.4.5 or later.
- Accepted bare strings for string-or-object union body flags. Union body fields with a string branch (e.g. `model: string | object`) now generate as "json-or-string": values starting with `{`, `[` or `"` are parsed as JSON, anything else passes through as a plain string, so `--model openai/gpt-4o` no longer fails with "invalid JSON".
- Unioned enum values across `oneOf` / `anyOf` branches in body flag generation. Discriminated-union bodies whose branches each carry a single-value `type` enum merged first-wins, so the generated flag only accepted the first branch's value. Colliding string enums are now unioned (deduped, branch order), and the enum is dropped entirely when one branch accepts any string.

## 2026-05-02 (v0.4.3)

- Bumped Bartolo to v0.4.3.
- Extended top-level `oneOf` / `anyOf` request bodies to union the properties of every branch into a single flag set. Endpoints like `chunking parse` (whose body is `oneOf` over multiple chunker strategies) now expose every strategy's fields as flags. Required fields are the intersection across branches; properties from earlier branches win on conflict.

## 2026-05-02 (v0.4.2)

- Bumped Bartolo to v0.4.2.
- Added a `json` body field flag fallback so nested objects, arrays of objects, and polymorphic unions are exposed as flags accepting a JSON value instead of being silently dropped. Endpoints like `deployments invoke` now expose every top-level field (e.g. `--messages`, `--invoke-options`, `--prefix-messages`, `--thread`, `--knowledge-filter`, `--identity`, `--documents`).

## 2026-05-02 (v0.4.1)

- Bumped Bartolo to v0.4.1.
- Added `bartolo version` command and `bartolo --version` flag so users can verify which generator binary they have on PATH.

## 2026-05-02 (v0.4.0)

- Bumped Bartolo to v0.4.0.
- Merged `allOf` compositions when extracting body field flags so endpoints whose top-level request schema is `allOf: [...]` (e.g. chat completions) now expose flags for every merged property instead of generating an empty flag list.

## 2026-05-02 (v0.3.0)

- Bumped Bartolo to v0.3.0.
- Expanded generated body field flags to cover nullable scalars (`string | null`, `type: [X, "null"]`, `anyOf` with null), repeatable arrays of scalars (`--tag a --tag b`), `additionalProperties` string maps (`--metadata key=value`), and string enums (with shell completion + value validation).
- Collapsed `anyOf` / `oneOf` shapes with a single non-null branch so they are exposed as flags instead of silently skipped.
- Nullable scalar flags accept the literal `null` to send an explicit JSON null.

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
