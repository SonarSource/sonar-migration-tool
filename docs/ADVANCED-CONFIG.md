# Advanced configuration reference

This page lists **every** option `sonar-migration-tool` accepts — JSON config fields and equivalent CLI flags side by side. The main [README](../README.md) covers the day-to-day flags; consult this page when you need the rarely-touched switches (mTLS, per-task resume, edition tuning, legacy config shapes).

## Contents

- [Configuration file shape](#configuration-file-shape)
- [All parameters reference](#all-parameters-reference)
- [Top-level fields](#top-level-fields)
- [`source` block — consumed by `extract`](#source-block--consumed-by-extract)
- [`target` block — consumed by `migrate` / `reset`](#target-block--consumed-by-migrate--reset)
- [Project key renaming strategy](#project-key-renaming-strategy)
- [Per-command CLI flags](#per-command-cli-flags)
- [SonarQube Cloud API rate limiting handling](#sonarqube-cloud-api-rate-limiting-handling)
- [Legacy config shapes](#legacy-config-shapes)
- [Security tips](#security-tips)

---

## Configuration file shape

The recommended shape ("unified config") carries one top-level block of defaults plus `source` and `target` sub-blocks. Each command reads only what's relevant to it:

| Command | Reads |
|---|---|
| `extract` | top-level + `source` |
| `structure`, `mappings`, `predictive-report` | top-level only |
| `migrate`, `reset` | top-level + `target` |
| `transfer` | top-level + `source` + `target` |

Complete annotated example: [`examples/config.unified.example.json`](../examples/config.unified.example.json). Minimal example: [`examples/config.minimal.example.json`](../examples/config.minimal.example.json). JSON Schema for editor autocomplete: [`schemas/config.schema.json`](../schemas/config.schema.json).

---

## All parameters reference

Every parameter the tool accepts, its JSON location, the equivalent CLI flag, where it applies, its default, and whether it's mandatory. **CLI flags always override the config file.** The sections after this one give the same information grouped by block with extra prose; this table is the at-a-glance index. Authoritative source: [`schemas/config.schema.json`](../schemas/config.schema.json).

Only `source.url` / `source.token` (for `extract`) and `target.url` / `target.token` (for `migrate` / `reset`) are mandatory; everything else has a default or is optional.

### Top-level fields

| Parameter | CLI flag | Commands | Default | Required | Role |
|---|---|---|---|:--:|---|
| `concurrency` | `--concurrency` | all | `10` | No | Max parallel HTTP calls. Overridable per block via `source`/`target`. |
| `timeout` | `--timeout` | extract, transfer | `60` | No | HTTP request timeout in seconds. Overridable per block. |
| `export_directory` | `--export_directory` (`--export_dir` on `transfer`) | all | `./migration-files` | No | Root directory for extract / migrate output. |
| `skip_issue_sync` | `--skip_issue_sync` | extract, migrate, transfer | `false` | No | Skip the final per-issue / per-hotspot metadata sync (#299). Accepts `true`/`on`/`yes`/`1` (case-insensitive). CLI flag is a one-way override. |
| `skip_project_data_migration` | `--skip_project_data_migration` | extract, migrate, transfer | `false` | No | Skip the entire project-data migration (import + trailing sync). Implies `skip_issue_sync` (#303). |

### `source` block — SonarQube Server side (`extract`, `transfer`)

| Parameter | CLI flag | Default | Required | Role |
|---|---|---|:--:|---|
| `source.url` | `--source_url` | — | ✅ | SonarQube Server base URL. |
| `source.token` | `--source_token` | — | ✅ | SonarQube Server user token (admin scope). |
| `source.extract_type` | `--extract_type` | `all` | No | Extract scope: `all` or the name of a specific extract task. |
| `source.concurrency` | `--concurrency` | top-level | No | Override top-level concurrency for extract calls. |
| `source.timeout` | `--timeout` | top-level | No | Override top-level timeout for extract calls. |
| `source.pem_file_path` | `--pem_file_path` | `null` | No | mTLS client-certificate PEM file. |
| `source.key_file_path` | `--key_file_path` | `null` | No | mTLS private key matching the PEM. |
| `source.cert_password` | `--cert_password` | `null` | No | mTLS certificate password, if any. |
| `source.target_task` | `--target_task` | `null` | No | Stop extract at a specific task (dependencies still run). |
| `source.extract_id` | `--extract_id` | `null` | No | Reuse / resume an existing extract directory ID. |
| `source.enterprise_key`, `source.organization_key`, `source.edition` | — | `null` / `enterprise` | No | Provisional — accepted but ignored today; reserved for future SQC-to-SQC migration. |
| `source.run_id` | — | `null` | No | Ignored by `extract`; present for shape symmetry with `target`. |

### `target` block — SonarQube Cloud side (`migrate`, `reset`, `transfer`)

| Parameter | CLI flag | Default | Required | Role |
|---|---|---|:--:|---|
| `target.url` | `--target_url` | — | ✅ | SonarQube Cloud base URL (e.g. `https://sonarcloud.io/`). |
| `target.token` | `--target_token` | — | ✅ | SonarQube Cloud user token (enterprise + org admin scope). |
| `target.enterprise_key` | `--enterprise_key` | `null` | No¹ | SonarQube Cloud enterprise key. ¹Required for any enterprise-scoped endpoint (portfolios, org listings, etc.). |
| `target.edition` | `--edition` | `enterprise` | No | SonarQube Cloud edition: `enterprise`, `team`, or `foss`. |
| `target.concurrency` | `--concurrency` | top-level | No | Override top-level concurrency for migrate / reset calls. |
| `target.timeout` | `--timeout` | top-level | No | Override top-level timeout for migrate / reset calls. |
| `target.run_id` | `--run_id` | `null` | No | Resume an in-progress migrate run by ID. |
| `target.target_task` | `--target_task` | `null` | No | Stop migrate at a specific task (dependencies still run). |
| `target.default_organization` | `--default_organization` | `null` | No | SonarQube Cloud org applied to every project when `organizations.csv` has no per-row mapping. Ignored (with a WARN) when any row carries a `sonarcloud_org_key` (#281). |
| `target.project_key_pattern` | `--project_key_pattern` | `<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>` | No | Template for target project keys (#138). See [Project key renaming strategy](#project-key-renaming-strategy). |
| `target.skip_profiles` | `--skip_profiles` | `false` | No | Skip quality profile migration / provisioning. |
| `target.exclude_branches` | `--exclude_branches` | `[]` | No | Glob patterns (Go `filepath.Match`) for non-main branches to skip. The main branch is never excluded. Repeatable on the CLI. |
| `target.organization_key` | — | `null` | No | Provisional — accepted but ignored today. |

### CLI-only / global flags

These have no config-file field.

| Flag | Commands | Default | Role |
|---|---|---|---|
| `-c, --config <path>` | all | — | Path to the JSON configuration file. |
| `--project_key <key>` | transfer | all projects | Project key to transfer; omit to transfer every project. |
| `--debug` | all | off | Verbose request/response logging for troubleshooting. |
| `-h, --help` | all | — | Help for the command. |
| `-v, --version` | all | — | Print version and exit. |

---

## Top-level fields

All optional.

| Field | Default | Description |
|---|---|---|
| `concurrency` | `10` | Default max parallel HTTP calls. |
| `timeout` | `60` | Default HTTP request timeout in seconds. |
| `export_directory` | `./migration-files` | Root directory for extract / migrate output. |
| `skip_issue_sync` | `false` | When `true` (or `"on"` / `"yes"` / `1`), skip the final per-issue and per-hotspot metadata sync that runs after project data is replayed. Defaults to `false` so the sync happens. Accepted aliases are case-insensitive. Issue #299. |
| `skip_project_data_migration` | `false` | When `true` (or `"on"` / `"yes"` / `1`), skip the entire project-data migration: the project-data import AND the trailing issue + hotspot sync. Useful when customers cut over to SonarQube Cloud by re-scanning rather than importing historical state. Implies `skip_issue_sync` — there's nothing to sync against. Same FlexibleBool aliases. Issue #303. |

`concurrency` and `timeout` can also be set inside `source` / `target` — those values override the top-level default for that command only.

The CLI flags `--skip_issue_sync` and `--skip_project_data_migration` on `migrate` / `transfer` are the one-way equivalents of the config fields — passing a flag forces the matching opt-out on regardless of the config-file value.

---

## `source` block — consumed by `extract`

| Field | Required | Description |
|---|---|---|
| `url` | ✅ | SonarQube Server base URL. |
| `token` | ✅ | SonarQube Server user token (admin scope). |
| `extract_type` | | `"all"` (default) or the name of a specific extract task. |
| `concurrency` | | Override top-level default. |
| `timeout` | | Override top-level default. |
| `pem_file_path` | | Client-side mTLS PEM file (optional). |
| `key_file_path` | | Client-side mTLS key file (optional). |
| `cert_password` | | Client-side mTLS password (optional). |
| `target_task` | | Stop extract at a specific task (dependencies still run). |
| `extract_id` | | Reuse an existing extract directory ID instead of generating a new one — resume after a failure. |
| `enterprise_key` / `organization_key` / `edition` | | Provisional — accepted but ignored today; reserved for future SQC-to-SQC migration. |

---

## `target` block — consumed by `migrate` / `reset`

| Field | Required | Description |
|---|---|---|
| `url` | ✅ | SonarQube Cloud base URL (typically `https://sonarcloud.io/`). |
| `token` | ✅ | SonarQube Cloud user token with enterprise + org admin scope. |
| `enterprise_key` | | SonarCloud enterprise key (required for any enterprise-scoped endpoint — portfolios, org listings, etc.). |
| `edition` | | `"enterprise"` (default), `"team"`, or `"foss"`. |
| `concurrency` | | Override top-level default. |
| `timeout` | | Override top-level default. |
| `run_id` | | Resume an in-progress migrate run by ID. |
| `target_task` | | Stop migrate at a specific task. |
| `default_organization` | | SonarCloud org applied to every project when `organizations.csv` has no per-row mapping. Ignored (with a WARN) when any row already carries a `sonarcloud_org_key`. CLI `--default_organization` wins. |
| `project_key_pattern` | | Template for target project keys, built from `<ORIGINAL_PROJECT_KEY>` and `<ORGANIZATION_KEY>`. Default `<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>`. CLI `--project_key_pattern` wins. See [Project key renaming strategy](#project-key-renaming-strategy). |
| `skip_profiles` | | Skip quality profile migration. |
| `exclude_branches` | | Array of glob patterns (Go `filepath.Match` syntax) for non-main branches to skip during project data import. The main branch is never excluded regardless of patterns. Example: `["feature/*", "release/*"]`. |
| `organization_key` | | Provisional — accepted but ignored today. |

---

## Project key renaming strategy

When a project is migrated to SonarQube Cloud its key is rewritten according to
`target.project_key_pattern` (CLI: `--project_key_pattern`). The pattern is a template built from
two placeholders:

| Placeholder | Replaced with |
|---|---|
| `<ORIGINAL_PROJECT_KEY>` | the source project key (mandatory; `<PROJECT_KEY>` is accepted as an alias) |
| `<ORGANIZATION_KEY>` | the target SonarQube Cloud organization key |

**Default:** `<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>` — SonarQube Cloud's own convention, and the tool's
historical behavior. Leaving the field unset reproduces exactly what previous versions did.

### Examples

```text
<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>            →  myorg_my-project
ACME_CORP_<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>  →  ACME_CORP_myorg_my-project
<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>_migrated   →  myorg_my-project_migrated
ACME_CORP_<ORIGINAL_PROJECT_KEY>                     →  ACME_CORP_my-project
sqs_<ORIGINAL_PROJECT_KEY>_migrated                  →  sqs_my-project_migrated
<ORIGINAL_PROJECT_KEY>                               →  my-project          (keep unchanged)
```

### Validation rules

The pattern is validated before the migration starts; an invalid pattern aborts the run with a
clear error:

- It must contain `<ORIGINAL_PROJECT_KEY>` (a pattern with no placeholder is rejected).
- Only the two documented placeholders are allowed; any other `<…>` token is rejected.
- A pattern whose **only** placeholder is `<ORIGINAL_PROJECT_KEY>` is allowed in two forms: bare
  (`<ORIGINAL_PROJECT_KEY>`, i.e. keep the key unchanged) or with a **static prefix and/or postfix
  totalling at least 5 characters** (`acme_<ORIGINAL_PROJECT_KEY>` ✅,
  `MYCORP_<ORIGINAL_PROJECT_KEY>` ✅, `sqs_<ORIGINAL_PROJECT_KEY>_migrated` ✅,
  `AAA_<ORIGINAL_PROJECT_KEY>` ❌). A short generic affix is too collision-prone to allow.

### Organization-prefix collision guard

If the pattern does **not** include `<ORGANIZATION_KEY>` (so every project gets the same static prefix),
the tool checks that the prefix does not match an existing SonarQube Cloud organization key. A
match would make the renamed keys look organization-scoped while not being mapped through one, so
`migrate` aborts and asks you to disambiguate (for example by adding `<ORGANIZATION_KEY>`).

### Conflict & length reporting

Before keys are created the tool renders every target key and reports, in both the markdown and
PDF migration reports (an amber callout):

- **Collisions** — the same target key produced by more than one source project. Because SonarQube
  Cloud project keys are globally unique, colliding projects cannot all be created. This typically
  happens when a pattern without `<ORGANIZATION_KEY>` maps the same source key from two organizations.
- **Over-length keys** — any rendered key longer than the SonarQube limit of 400 characters, which
  the API would reject.

Permission-template and portfolio selection regexes are adapted automatically to whatever prefix
the chosen pattern produces, so they keep matching the renamed keys.

---

## Per-command CLI flags

CLI flags **override** the corresponding config field when both are set.

### `extract`

```bash
sonar-migration-tool extract --source_url <url> --source_token <token> [flags]
```

| Flag | Description |
|---|---|
| `-c, --config <path>` | Path to JSON configuration file. |
| `--source_url <url>` | SonarQube Server URL. (Legacy `--url` is still accepted but deprecated.) |
| `--source_token <token>` | SonarQube Server authentication token. (Legacy `--token` is still accepted but deprecated.) |
| `--export_directory <dir>` | Output directory (default `./migration-files`). |
| `--extract_type <name>` | Type of extract to run (default `all`). |
| `--extract_id <id>` | Resume a previous extraction. |
| `--target_task <task>` | Run a specific task (with its dependencies). |
| `--concurrency <n>` | Max concurrent requests. |
| `--timeout <s>` | Request timeout in seconds. |
| `--skip_project_data_migration` | Skip the issue / source / SCM-blame extract (extracted by default). |
| `--skip_issue_sync` | Drop the per-issue / per-hotspot sync metadata from the extract (no `additionalFields=_all`, no per-hotspot detail). Pair with migrate-side `--skip_issue_sync`. #398. |
| `--exclude_branches <pattern>` | Glob pattern for non-main branches to skip during project data import. Repeatable (pass multiple times for multiple patterns). Main branch is never excluded. |
| `--pem_file_path <path>` | mTLS PEM file. |
| `--key_file_path <path>` | mTLS key file. |
| `--cert_password <pw>` | mTLS password. |

### `structure` / `mappings` / `predictive-report`

```bash
sonar-migration-tool structure --config config.json
sonar-migration-tool mappings  --config config.json
sonar-migration-tool predictive-report --config config.json
```

| Flag | Description |
|---|---|
| `-c, --config <path>` | Path to JSON configuration file. |
| `--export_directory <dir>` | Override the config file's `export_directory`. |

### `migrate`

```bash
sonar-migration-tool migrate --target_token <token> --enterprise_key <key> [flags]
```

| Flag | Description |
|---|---|
| `-c, --config <path>` | Path to JSON configuration file. |
| `--target_token <token>` | SonarQube Cloud authentication token. (Legacy `--token` is still accepted but deprecated.) |
| `--enterprise_key <key>` | SonarQube Cloud enterprise key. |
| `--target_url <url>` | SonarQube Cloud URL (default `https://sonarcloud.io/`). (Legacy `--url` is still accepted but deprecated.) |
| `--export_directory <dir>` | Directory containing the extract output. |
| `--edition <name>` | SonarQube Cloud license edition. |
| `--run_id <id>` | Resume a failed migration. |
| `--target_task <task>` | Run a specific migration task (with its dependencies). |
| `--skip_profiles` | Skip quality profile migration / provisioning. |
| `--skip_issue_sync` | Skip the final per-issue / per-hotspot metadata sync (#299). One-way: setting this on the CLI forces the sync off, overriding the `skip_issue_sync` config field. |
| `--skip_project_data_migration` | Skip the entire project-data migration: `importProjectData` plus the trailing sync pair. Implies `--skip_issue_sync`. One-way override of the `skip_project_data_migration` config field. Issue #303. |
| `--exclude_branches <pattern>` | Glob pattern for non-main branches to skip during project data import. Repeatable. Main branch is never excluded. |
| `--default_organization <key>` | SonarCloud org applied to every project when `organizations.csv` has no mapping defined. |
| `--project_key_pattern <pattern>` | Template for target project keys (`<ORIGINAL_PROJECT_KEY>` / `<ORGANIZATION_KEY>`). Default `<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>`. See [Project key renaming strategy](#project-key-renaming-strategy). |
| `--concurrency <n>` | Max concurrent requests. |

### `reset`

```bash
sonar-migration-tool reset [token] [enterprise_key] --export_directory <dir>
```

Same flag set as `migrate`. Destructive — see the [README's Reset section](../README.md#reset).

### `transfer`

`transfer` reads the same unified shape as `extract` / `migrate` — both
`source` and `target` blocks plus the shared top-level defaults. The
project key is supplied on the CLI; everything else (credentials,
mTLS, timeouts, concurrency, export directory) can live in the config
file.

```jsonc
{
  "concurrency": 25,
  "timeout": 60,
  "export_directory": "./migration-files",
  "source": { "url": "https://...", "token": "sqp_xxx",
              "pem_file_path": "...", "key_file_path": "...", "cert_password": "..." },
  "target": { "url": "https://sonarcloud.io/", "token": "squ_xxx",
              "default_organization": "my-sqc-org",
              "enterprise_key": "my-enterprise" }
}
```

| Flag | Config key | Description |
|---|---|---|
| `-c, --config <path>` | — | Path to JSON configuration file. |
| `--source_url <url>` | `source.url` | SonarQube Server URL. |
| `--source_token <token>` | `source.token` | SonarQube Server token. |
| `--project_key <key>` | — | Project key to transfer (omit to transfer all projects). |
| `--target_url <url>` | `target.url` | SonarQube Cloud URL. |
| `--target_token <token>` | `target.token` | SonarQube Cloud token. |
| `--default_organization <key>` | `target.default_organization` | SonarQube Cloud organization key. |
| `--project_key_pattern <pattern>` | `target.project_key_pattern` | Template for target project keys. See [Project key renaming strategy](#project-key-renaming-strategy). |
| `--enterprise_key <key>` | `target.enterprise_key` | SonarQube Cloud enterprise key (defaults to `--default_organization`). |
| `--export_dir <dir>` | `export_directory` | Working directory (default `./migration-files/`). |
| `--concurrency <n>` | `concurrency` | Max concurrent HTTP requests. |
| `--timeout <s>` | `timeout` | HTTP request timeout in seconds. |
| `--pem_file_path <path>` | `source.pem_file_path` | Client mTLS PEM file. |
| `--key_file_path <path>` | `source.key_file_path` | Client mTLS key file. |
| `--cert_password <pw>` | `source.cert_password` | Client mTLS password. |
| `--skip_issue_sync` | top-level `skip_issue_sync` | Skip the final per-issue / per-hotspot metadata sync (#299). |
| `--skip_project_data_migration` | top-level `skip_project_data_migration` | Skip the entire project-data migration (importProjectData + trailing syncs). #303. |
| `--exclude_branches <pattern>` | `target.exclude_branches` | Glob pattern for non-main branches to skip during project data import. Repeatable. Main branch is never excluded. |

CLI flags always override the corresponding config-file value.

### `wizard` / `gui`

> **⚠️ Experimental:** The `gui` command is experimental. It may change between releases and can have rough edges. For production migrations, prefer the CLI commands.

| Flag | Description |
|---|---|
| `--export_directory <dir>` | Working directory (default `./migration-files`). |
| `--addr <host:port>` | (`gui` only) HTTP bind address (default `localhost:0`). |
| `--no-browser` | (`gui` only) Don't open the browser automatically. |

### Global flags

Available on every command:

| Flag | Description |
|---|---|
| `--debug` | Verbose request/response logging. |
| `-h, --help` | Help for the command. |
| `-v, --version` | Print version and exit. |

---

## SonarQube Cloud API rate limiting handling

The `migrate` step is highly API-intensive, and the production SonarQube Cloud platform
protects its API with rate limiting. A large migration will likely hit that limit. The tool
handles this automatically — there is nothing to configure — by retrying throttled requests
with backoff and pausing affected work until the limit clears. This section documents what it
does and what you will see in the logs.

### What gets retried

| Outcome | Retried? |
|---|---|
| `429 Too Many Requests` | Yes |
| `500`, `502`, `503`, `504` | Yes |
| Network / connection errors | Yes |
| Other `4xx` (e.g. `400`, `401`, `403`, `404`) | No — these indicate a caller mistake and fail immediately |

### Backoff schedules

The wait between attempts depends on why the request failed. Every wait also gets up to
**+50 % random jitter** added, so concurrent workers don't retry in lockstep.

| Trigger | Waits between attempts | Retries |
|---|---|---|
| `5xx` / network error | 100 ms, 200 ms, 400 ms | 3 |
| SonarQube Cloud `429` (application rate limit) | 5 s, 15 s, 30 s, 60 s, 120 s, 300 s | 6 |
| Cloudflare / unclassified `429` | 2 s | 1 (fail fast) |

A `429` may carry a `Retry-After` header; when present, the tool honors it (using the larger of
the header value and the scheduled wait), capped at **300 s** to guard against a misconfigured
proxy asking for an unreasonable delay.

For a sustained SonarQube Cloud rate limit, the worst-case pause for a single request before it
gives up is the sum of its schedule — roughly **8.8 minutes** (plus jitter). When a request
exhausts its schedule still seeing `429`, that request fails and the error is reported; the tool
does not abort the whole migration.

### Classification and coordinated pausing

Each `429` is classified by its body and headers as one of:

- **SonarQube Cloud rate limit** — the documented application limit (JSON error envelope). Uses
  the long schedule above.
- **Cloudflare rate limit** — identified by Cloudflare headers (`CF-Ray`, `CF-Mitigated`,
  `Server: cloudflare`) or its branded HTML error page. Fails fast, since this often needs
  operator attention rather than waiting.
- **Unknown 429** — anything else; surfaced in the report for review.

When a SonarQube Cloud rate limit is hit, a **shared gate** makes all concurrent workers pause on
the same window rather than each independently hammering an already-exhausted quota.

### What you'll see in the logs

When rate limiting is encountered, the tool logs the pause and, once a throttled request gets
through, logs that the migration has resumed:

```
WARN  API rate limiting hit — pausing requests and retrying with backoff  kind=sqc-rate-limit retryAfter=… waitChosen=…
INFO  API rate limiting cleared — migration resuming  pausedFor=… retries=…
```

These two lines are emitted **once per rate-limiting episode** — a burst of many throttled
requests produces a single pause/resume pair, not one line per request. Individual retries are
also logged at `WARN` as `retrying request` (with `attempt`/`maxAttempts`), and the first hit of
each kind logs a one-time `rate limiting detected` line including a snippet of the response body.

### Report artifact

If any `429` was observed during a run, the tool writes a `rate_limit_events.json` artifact into
the run directory (counts per kind, cumulative and longest pause, and a snapshot of the first
event of each kind). Clean runs produce no artifact.

The PDF migration report stays quiet about rate limiting that was handled gracefully — if the
tool paused and resumed without failing any task, there is **no banner**, regardless of how long
it paused. An amber rate-limit notice is shown only when rate limiting was *not* worked around:

- a task failed with `429` as its terminal status (re-run with `--run-id` to resume), or
- a non-standard `429` (Cloudflare / WAF / unclassified) was seen, which may need operator
  action even if it resolved on its own.

---

## Legacy config shapes

Three older shapes still parse so existing configs keep working. Prefer the unified shape for new setups.

**Flat shape (extract or migrate):**

```jsonc
{ "url": "...", "token": "...", "enterprise_key": "...", "export_directory": "./files" }
```

**Split-by-command shape:**

```jsonc
{
  "extract": { "url": "...", "token": "..." },
  "migrate": { "token": "...", "enterprise_key": "..." }
}
```

**Side-sectioned shape:**

```jsonc
{
  "sonarqube":  { "url": "...", "token": "..." },
  "sonarcloud": { "token": "...", "organization": "..." }
}
```

---

## Security tips

- **Never commit tokens.** Add config files containing real tokens to `.gitignore`.
- **Restrict permissions:** `chmod 600 config.json`.
- **Inject tokens from the environment:** generate the JSON config from a shell script that reads `$SQS_TOKEN` / `$SQC_TOKEN` at run time, and delete the rendered file after the migration.
- **Audit trail:** `migrate` writes a `requests.log` capturing every API call. Review it after a run, especially if errors occurred.
