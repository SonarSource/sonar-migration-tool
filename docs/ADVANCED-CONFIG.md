# Advanced configuration reference

This page lists **every** option `sonar-migration-tool` accepts — JSON config fields and equivalent CLI flags side by side. The main [README](../README.md) covers the day-to-day flags; consult this page when you need the rarely-touched switches (mTLS, per-task resume, edition tuning, legacy config shapes).

## Contents

- [Configuration file shape](#configuration-file-shape)
- [Top-level fields](#top-level-fields)
- [`source` block — consumed by `extract`](#source-block--consumed-by-extract)
- [`target` block — consumed by `migrate` / `reset`](#target-block--consumed-by-migrate--reset)
- [Per-command CLI flags](#per-command-cli-flags)
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
| `transfer` | a dedicated shape (see below) |

Complete annotated example: [`examples/config.unified.example.json`](../examples/config.unified.example.json). Minimal example: [`examples/config.minimal.example.json`](../examples/config.minimal.example.json). JSON Schema for editor autocomplete: [`schemas/config.schema.json`](../schemas/config.schema.json).

---

## Top-level fields

All optional.

| Field | Default | Description |
|---|---|---|
| `concurrency` | `10` | Default max parallel HTTP calls. |
| `timeout` | `60` | Default HTTP request timeout in seconds. |
| `export_directory` | `./migration-files` | Root directory for extract / migrate output. |

`concurrency` and `timeout` can also be set inside `source` / `target` — those values override the top-level default for that command only.

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
| `include_scan_history` | | When true, pull full issue / source / SCM-blame data so `migrate` can replay history. |
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
| `skip_profiles` | | Skip quality profile migration. |
| `include_scan_history` | | Import scan history into the destination projects. |
| `organization_key` | | Provisional — accepted but ignored today. |

---

## Per-command CLI flags

CLI flags **override** the corresponding config field when both are set.

### `extract`

```bash
sonar-migration-tool extract [url] [token] [flags]
```

| Flag | Description |
|---|---|
| `-c, --config <path>` | Path to JSON configuration file. |
| `--export_directory <dir>` | Output directory (default `./migration-files`). |
| `--extract_type <name>` | Type of extract to run (default `all`). |
| `--extract_id <id>` | Resume a previous extraction. |
| `--target_task <task>` | Run a specific task (with its dependencies). |
| `--concurrency <n>` | Max concurrent requests. |
| `--timeout <s>` | Request timeout in seconds. |
| `--include_scan_history` | Pull full issue / source / SCM-blame data. |
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
sonar-migration-tool migrate [token] [enterprise_key] [flags]
```

| Flag | Description |
|---|---|
| `-c, --config <path>` | Path to JSON configuration file. |
| `--url <url>` | SonarQube Cloud URL (default `https://sonarcloud.io/`). |
| `--export_directory <dir>` | Directory containing the extract output. |
| `--edition <name>` | SonarQube Cloud license edition. |
| `--run_id <id>` | Resume a failed migration. |
| `--target_task <task>` | Run a specific migration task (with its dependencies). |
| `--skip_profiles` | Skip quality profile migration / provisioning. |
| `--include_scan_history` | Import scan history into SQC projects. |
| `--default_organization <key>` | SonarCloud org applied to every project when `organizations.csv` has no mapping defined. |
| `--concurrency <n>` | Max concurrent requests. |

### `reset`

```bash
sonar-migration-tool reset [token] [enterprise_key] --export_directory <dir>
```

Same flag set as `migrate`. Destructive — see the [README's Reset section](../README.md#reset).

### `transfer`

`transfer` takes a different config shape (one source + one project + one target):

```jsonc
{
  "sonarqube":  { "url": "https://...", "token": "sqp_xxx", "projectKey": "my-project" },
  "sonarcloud": { "token": "squ_xxx",   "organization": "my-sqc-org" }
}
```

| Flag | Description |
|---|---|
| `-c, --config <path>` | Path to JSON configuration file. |
| `--sq-url <url>` | SonarQube Server URL. |
| `--sq-token <token>` | SonarQube Server token. |
| `--project-key <key>` | Project key to transfer (omit to transfer all projects in the source server). |
| `--sc-token <token>` | SonarQube Cloud token. |
| `--sc-org <key>` | SonarQube Cloud organization key. |
| `--sc-enterprise-key <key>` | SonarQube Cloud enterprise key (defaults to `--sc-org`). |
| `--export-dir <dir>` | Working directory (default `./migration-files/`). |
| `--include-scan-history` | Extract + import full scan history. |

### `wizard` / `gui`

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

**Side-sectioned shape (used by `transfer`):**

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
