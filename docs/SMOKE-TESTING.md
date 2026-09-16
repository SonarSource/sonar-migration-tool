# Live smoke test suite

The smoke suite drives the **real built binary** against a **real SonarQube Server**
and a **real staging SonarQube Cloud organization**, then reports pass/fail per
command. It is the pre-PR gate: run it locally before opening a pull request.

It automates [REGRESSION-TESTING-PLAN.md](REGRESSION-TESTING-PLAN.md), which
describes the same two migration paths as a manual copy-paste protocol.

It lives in `go/smoke/` and is **deliberately not part of CI** — it needs live
credentials CI does not have, and it performs destructive resets. See
[.github/workflows/README.md](../.github/workflows/README.md).

---

## Quick start

```bash
make smoke-fast                              # Tier 0 only — no network, no credentials (~2s)
make smoke                                   # Tiers 0, 1, 3 — needs the source server (~30s)
SMOKE_ALLOW_DESTRUCTIVE=1 make smoke-full    # every tier, including the destructive path
```

Each target builds the binary first, so you are always testing the code you just
changed rather than a stale binary.

Credentials come from `config.json` in the repository root (the same unified
config shape the tool itself consumes — see
[`schemas/config.schema.json`](../schemas/config.schema.json)). When that file is
absent, the credential-dependent tiers **skip cleanly** instead of failing, so a
fresh checkout stays green.

---

## Tiers

| Tier | File | Needs | What it covers |
|---|---|---|---|
| 0 | `tier0_cli_test.go` | nothing | Every subcommand exists; every flag is still registered; every up-front validation error still fires with the right message and exit code; the credential scrubber actually redacts. |
| 1 | `tier1_source_test.go` | source SonarQube Server | `extract` (+ resume) → `structure` → `mappings` → `report` (both types) → `predictive-report`. Asserts the 7 CSVs, both markdown reports, and a valid PDF. Makes **no** SonarQube Cloud writes. |
| 2 | `tier2_full_test.go` | source + target, **destructive** | Path A (`transfer`) and Path B (`extract`→`structure`→`mappings`→`migrate`), each verified with `regtest`; plus idempotency, `sync-issues`, `analysis_report`, and report-accuracy assertions. |
| 3 | `tier3_gui_test.go` | nothing | The `gui` command's HTTP routes and WebSocket upgrade. |

`regtest` is the oracle for Tier 2: rather than hand-rolling equivalence checks,
the suite runs `regtest --format json` and asserts the verdict is `PASS`.

### Command coverage

All 13 subcommands have Tier 0 contract tests. 12 of 13 additionally get a real
functional run. The exception is `wizard` — see [Known gaps](#known-gaps).

---

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `SMOKE_CONFIG` | `<repo>/config.json` | Path to the credentials file. Point it elsewhere to test against another instance pair. |
| `SMOKE_PROJECT_KEY` | auto-discover | Source project key to exercise. **Effectively required**: the harness holds no tokens by design, so unauthenticated auto-discovery usually fails and the tier skips. |
| `SMOKE_ALLOW_DESTRUCTIVE` | unset | Must be exactly `1` to enable Tier 2. Without it Tier 2 skips. |
| `SMOKE_ALLOW_HOST` | `sc-staging.io` | Overrides the allowlisted target host. **Cannot** allowlist a production host. |
| `SMOKE_BINARY` | `go/sonar-migration-tool` | Binary to drive. Otherwise `go/sonar-migration-tool` is preferred over a copy at the repo root, because `make build` writes the former and a stale copy often sits at the latter. |
| `SMOKE_CLI_TIMEOUT` | `15m` | Per-invocation ceiling, as a Go duration (e.g. `45m`). |

---

## Safety model

Tier 2 calls `reset --yes`, which **deletes migrated entities** from the targeted
organizations. Five controls make it hard to point that at the wrong place:

1. **Production denylist.** `sonarcloud.io` and `sonarqube.us` (plus their `www.`
   and `api.` forms) are hard-denied in `assertHostAllowed`. `SMOKE_ALLOW_HOST`
   cannot override this — passing a denied host there is itself an error.
2. **Explicit opt-in.** Tier 2 skips unless `SMOKE_ALLOW_DESTRUCTIVE=1`.
3. **Checked before any network call.** The host check runs before the
   reachability preflight, so a misconfigured target is refused without the suite
   contacting it at all.
4. **Always organization-scoped.** `reset` is always passed
   `--organization <key>`, an anchored full-match pattern. The suite never issues
   a bare `reset`.
5. **Isolated output.** Every run uses a fresh `t.TempDir()`, never
   `./migration-files`, so your real run artifacts are untouched.

### Credential handling

- Tokens are **never placed on argv** — argv is readable by any local process via
  `ps`. The binary is given `--config <path>` and reads them itself.
- All captured output is passed through `scrubSecrets` before it is logged.
  It redacts `squ_`/`sqa_`/`sqp_` tokens, `Authorization:` lines and `bearer`
  values by pattern, **and** the literal token strings loaded from the config —
  because an opaque SonarQube Cloud token matches no recognisable pattern.
- `--debug` is never used by default: it logs request payloads.
- `TestTier0_SecretScrubbing` verifies the scrubber actually redacts. It exists
  because an early version matched only `Authorization: Bearer`, stopping at the
  space and leaking the token that followed.

---

## Output

Logs and a run summary are written to `.smoke/` (gitignored):

- `.smoke/<TestName>.log` — the exact command, exit code, duration and scrubbed
  output for each invocation. This is where to look first when something fails.
- `.smoke/report-<timestamp>.md` — the tier × command × status table, also printed
  to stdout at the end of the run.

The run exits non-zero if any check failed.

---

## Extending the suite

The suite only protects what it knows about, so it has to be updated alongside
the code. `CLAUDE.md` carries this as a mandatory directive:

| You changed | Update |
|---|---|
| Added/renamed/removed a command | `allCommands` **and** `expectedCommandCount` in `tier0_cli_test.go`, plus a functional case in the owning tier |
| Added/renamed/removed a flag | the `commandFlags` table in `tier0_cli_test.go` |
| Added a validation error or exit code | `TestTier0_ExitCodeMatrix` in `tier0_cli_test.go` |
| Added a written artifact | an assertion in the tier that produces it |
| Added a migration phase or task group | `TestTier2_PathB_FullPipeline` |
| Added a report field or total | `assertReportAccuracy` in `tier2_full_test.go` |

**Never guess an exit code or an error message.** Run the real binary, observe
what it returns, then encode that. Every expectation in Tier 0 was obtained this
way.

### Two structural details that are load-bearing

- **`go/smoke/doc.go` has no build tag, on purpose.** Every other file in the
  package is behind `//go:build smoke`. Without an untagged file the package has
  no buildable files under default tags and `go test ./...` fails with
  `build constraints exclude all Go files`.
- **The run summary lives in `zzz_summary_test.go` because of its name.** Go runs
  tests in source-file order. A summary test that must observe every other
  outcome has to be in the file that sorts last; putting it in `harness_test.go`
  makes it run *first*, where it records nothing and silently skips.

---

## Known gaps

Honest limits of the current suite:

- **`wizard` is not black-box tested.** Its CLI prompter uses `survey/v2`, which
  requires a real TTY — piped stdin makes it error rather than prompt. It is
  already coverage-excluded in `sonar-project.properties`
  (`**/cli_prompter.go`). The wizard *engine* is exercised through Tier 3's
  WebSocket path, because `WebPrompter` implements the same `Prompter` interface.
  Adding a pty dependency to close this is deliberately avoided.
- **Project-data and history migration are not covered.** Tier 2 always passes
  `--skip_project_data_migration`. `PollCETask`
  (`go/internal/scanreport/submit.go`) hardcodes a non-injectable 5-second poll,
  so exercising those paths costs at least 5 seconds per task. Covering them
  needs either an opt-in gate in the suite or an injectable interval in
  `PollCETask`.
- **Exit codes 2 and 3 are unasserted.** They exist
  (`common.NewExitError` in `internal/migrate/org_mapping.go`) but are only
  reachable once organization mapping is under way, which needs live data.
- **Tier 1 auto-discovery is unreliable.** Set `SMOKE_PROJECT_KEY` explicitly.

---

## Troubleshooting

| Symptom | Cause |
|---|---|
| `no binary found ... run 'make build' first` | Use the `make` targets; they build first. |
| `no config at ...` and the tier skips | Expected without `config.json`. Set `SMOKE_CONFIG`, or accept the skip. |
| `REFUSING TO RUN: target host ... is a production` | Working as intended. Point `target.url` at staging. |
| Tier 1 skips asking for a project key | Set `SMOKE_PROJECT_KEY`. |
| `... is not reachable` | The instance is down. The preflight fails fast so a later assertion failure is not mistaken for a code regression. |
| A `regtest` check failed | The run summary lists every non-matching check with its source and target values. |
