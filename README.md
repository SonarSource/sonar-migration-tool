# SonarQube Migration Tool

`sonar-migration-tool` migrates a SonarQube Server (SQS) instance to a SonarQube Cloud (SQC) Enterprise. In a single run it lifts the SQS side of your setup — projects, portfolios, quality gates, quality profiles, groups, permissions, permission templates, AI Code Fix configuration, and (optionally) scan history — and recreates the matching configuration in one or more SQC organizations.

The tool ships as a single static binary. Grab the latest release from [GitHub Releases](https://github.com/sonar-solutions/sonar-migration-tool/releases) and you're ready to go — no runtime to install. If you want to build from source instead, see [BUILD.md](docs/BUILD.md).

## What gets migrated

| ✅ Migrated | ❌ Not migrated |
|---|---|
| Projects (key, name, visibility, tags, links, webhooks) | Issues and their history *(unless `--include_scan_history` is set)* |
| Quality Gates + conditions | Source code *(re-scan after migration)* |
| Quality Profiles + custom rules | Users *(re-invited on the SQC side)* |
| Groups, Permissions, Permission Templates | Applications *(no SQC counterpart)* |
| Portfolios | |
| Global settings *(SQS keys that have an SQC equivalent)* | |
| AI Code Fix configuration | |
| Scan history *(optional, behind `--include_scan_history`)* | |

## Compatibility

- **SonarQube Server**: 9.9+ tested end-to-end; SQS 6.3+ extracted with limited coverage. Authentication auto-detects the right scheme (Basic auth < 10, Bearer token ≥ 10).
- **Editions**: Community, Developer, Enterprise, Data Center. Edition-specific entities (applications, portfolios) are skipped gracefully on lower editions.
- **SonarQube Cloud**: any Enterprise plan with target organizations already created.

## Table of contents

1. [Quick start — single SQC organization](#quick-start--single-sqc-organization) — every SQS project lands in one SQC org. The shortest happy path.
2. [Quick start — multiple SQC organizations](#quick-start--multiple-sqc-organizations) — when projects map to several SQC orgs.
3. [Configuration file & top-level CLI options](#configuration-file--top-level-cli-options)
4. [Migration steps in detail](#migration-steps-in-detail) — [`extract`](#1-extract), [`structure`](#2-structure), [`mappings`](#3-mappings), [`migrate`](#4-migrate)
5. [Single-project transfer](#single-project-transfer) — `transfer`
6. [Interactive wizard](#interactive-wizard) — `wizard`
7. [Browser-based UI](#browser-based-ui) — `gui`
8. [Predictive report](#predictive-report) — preview what `migrate` will do, before doing it.
9. [Reset](#reset) — wipe an SQC enterprise back to empty.
10. [Post-migration checklist](#post-migration-checklist)
11. [Further reading](#further-reading)

---

## Quick start — single SQC organization

Use this when every SQS project should land in **one** target SQC organization (typically the case for small instances, or when you have at most one DevOps Platform configured on SQS). The whole migration runs in four commands.

> **Prerequisites**: an SQS admin token, an SQC token with enterprise+org admin permissions, and the target SQC organization already created.

```bash
# 1. Pull configuration off SQS.
sonar-migration-tool extract --config config.json

# 2. Group projects by their source organization (one row per SQS org).
sonar-migration-tool structure --config config.json

# 3. Generate mapping CSVs (gates, profiles, groups, templates, portfolios).
sonar-migration-tool mappings --config config.json

# 4. Apply to SQC. --default_organization fills in the SQC org for every project.
sonar-migration-tool migrate --config config.json --default_organization my-sqc-org
```

Minimal `config.json` (see [`examples/config.minimal.example.json`](examples/config.minimal.example.json)):

```jsonc
{
  "source": {
    "url":   "https://sonarqube.example.com",
    "token": "squ_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
  },
  "target": {
    "url":   "https://sonarcloud.io/",
    "token": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "enterprise_key": "your-enterprise"
  }
}
```

When `migrate` finishes you'll find `migration_summary.pdf` in your export directory describing every object that was created.

---

## Quick start — multiple SQC organizations

Use this when SQS projects must spread across **several** SQC organizations — typically because SQS has more than one DevOps Platform configured, or because you want to split projects by team / business unit on the SQC side.

Steps 1, 2, and 4 are identical to the single-org flow; the only difference is **between 2 and 3**: open `organizations.csv` and fill the target SQC org per row by hand.

```bash
# 1. Extract.
sonar-migration-tool extract --config config.json

# 2. Group by source organization. Produces migration-files/organizations.csv.
sonar-migration-tool structure --config config.json

# 2b. ── Manual step ──
#     Open migration-files/organizations.csv and fill the
#     sonarcloud_org_key column for every row. Use "SKIPPED" to leave
#     an SQS org out of the migration entirely.

# 3. Generate mappings (gates.csv, profiles.csv, groups.csv, templates.csv, portfolios.csv).
sonar-migration-tool mappings --config config.json

# 4. Migrate. Omit --default_organization — the per-row mapping wins.
sonar-migration-tool migrate --config config.json
```

You can also edit the other mapping CSVs (`gates.csv`, `profiles.csv`, etc.) between steps 3 and 4 to rename or skip individual entities — the comment block at the top of each file explains its columns.

---

## Configuration file & top-level CLI options

`extract`, `structure`, `mappings`, `migrate`, `reset`, and `predictive-report` all read the same JSON config file. The recommended shape is one top-level block of defaults plus a `source` sub-block (consumed by `extract`) and a `target` sub-block (consumed by `migrate` / `reset`). Each command silently ignores the block that isn't its own.

**Minimal example** ([`examples/config.minimal.example.json`](examples/config.minimal.example.json)):

```jsonc
{
  "source": {
    "url":   "https://sonarqube.example.com",
    "token": "squ_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
  },
  "target": {
    "url":   "https://sonarcloud.io/",
    "token": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "enterprise_key": "your-enterprise"
  }
}
```

**Complete example** ([`examples/config.unified.example.json`](examples/config.unified.example.json)) carries every supported field with its default. Use it as a starting template and delete what you don't need.

### Top-level fields (all optional)

| Field | Default | Description |
|---|---|---|
| `concurrency` | `10` | Default max parallel HTTP calls. |
| `timeout` | `60` | Default HTTP request timeout in seconds. |
| `export_directory` | `./migration-files` | Root directory for extract / migrate output. |

### Common CLI flags

Every command accepts these:

| Flag | Description |
|---|---|
| `-c, --config <path>` | JSON config file. |
| `--export_directory <dir>` | Override the config's `export_directory`. |
| `--debug` | Verbose request/response logging. |

A JSON Schema for editor autocomplete lives at [`schemas/config.schema.json`](schemas/config.schema.json) — wire it into your editor through `.vscode/settings.json` or add a `"$schema"` pointer at the top of your config.

For the complete field list (mTLS, per-task resume, edition tuning, etc.) see [docs/ADVANCED-CONFIG.md](docs/ADVANCED-CONFIG.md).

---

## Migration steps in detail

The diagram below shows the four-step pipeline. Steps 2 and 3 only produce / read CSV files locally — they make no API calls.

```
┌───────────┐   ┌───────────┐   ┌──────────┐   ┌─────────┐
│  EXTRACT  │──►│ STRUCTURE │──►│ MAPPINGS │──►│ MIGRATE │
│ (SQS API) │   │  (local)  │   │ (local)  │   │(SQC API)│
└───────────┘   └───────────┘   └──────────┘   └─────────┘
```

### 1. `extract`

Pulls every relevant configuration object off the SonarQube Server instance into JSONL files under `<export_directory>/<extract-id>/`. Idempotent — re-running with the same `extract_id` resumes from the last completed task.

```bash
sonar-migration-tool extract --config config.json
```

Selected flags:

| Flag | Description |
|---|---|
| `--extract_id <id>` | Resume a previous extract. |
| `--target_task <task>` | Run a single task (with its dependencies). |
| `--include_scan_history` | Also pull full issue / source / SCM-blame data so `migrate` can re-create historical scans. |

For multiple source SQS servers run `extract` once per server (different `extract_id`s). `structure` aggregates all of them.

### 2. `structure`

Walks every extract directory in `<export_directory>` and produces `organizations.csv` — one row per SQS organization the tool discovered.

```bash
sonar-migration-tool structure --config config.json
```

When the config file targets a single SonarCloud organization, the `sonarcloud_org_key` column is pre-filled. Otherwise leave it blank and edit before running `mappings`.

### 3. `mappings`

Produces the per-entity mapping CSVs based on `organizations.csv`:

- `gates.csv` — quality gates
- `profiles.csv` — quality profiles
- `groups.csv` — groups
- `templates.csv` — permission templates
- `portfolios.csv` — portfolios

```bash
sonar-migration-tool mappings --config config.json
```

Each file has columns to rename, skip (`SKIPPED`), or merge entities on the SQC side. Inspect them before running `migrate`.

### 4. `migrate`

Reads every CSV and the extract data, then applies the configuration to SonarQube Cloud.

```bash
sonar-migration-tool migrate --config config.json
```

Selected flags:

| Flag | Description |
|---|---|
| `--run_id <id>` | Resume a partially-completed migrate run. |
| `--target_task <task>` | Run a single migration task (with its dependencies). |
| `--default_organization <key>` | SQC org used for every project when `organizations.csv` has no per-row mapping. |
| `--skip_profiles` | Skip quality profile migration (useful for second-pass runs). |
| `--include_scan_history` | Replay extracted scan history into SQC projects. |

`migrate` writes a JSONL audit trail per task, a `requests.log` (every API call), and `migration_summary.pdf` (final human-readable report) under `<export_directory>/<run_id>/`.

---

## Single-project transfer

Use `transfer` when you only need to move **one** project end-to-end in a single command — useful for proof-of-concept runs, one-off migrations, or moving a stray project that was missed by the bulk run. It chains `extract → structure → mappings → migrate` internally for that single key.

```bash
sonar-migration-tool transfer \
  --source-url   https://sonarqube.example.com \
  --source-token sqp_xxx \
  --project-key  my-project \
  --target-token squ_xxx \
  --default_organization my-sqc-org
```

Or with a config file (same shape as `extract` / `migrate`):

```bash
sonar-migration-tool transfer -c config.json --project-key my-project
```

```jsonc
{
  "source": { "url": "https://...", "token": "sqp_xxx" },
  "target": { "token": "squ_xxx",  "default_organization": "my-sqc-org" }
}
```

`transfer` produces the same `migration_summary.pdf` as the full pipeline, restricted to the single project.

For the full reference — every flag, every config field, the order of operations — see [`docs/TRANSFER.md`](docs/TRANSFER.md).

---

## Interactive wizard

Prefer not to script anything? Run the wizard. It walks every step interactively and saves progress between steps so it can resume after an interruption.

```bash
sonar-migration-tool wizard --export_directory ./migration-files
```

The wizard covers the same four steps as the manual pipeline (extract → structure → org mapping → mappings → migrate) and additionally handles mTLS prompts and optional scan-history import. It's the recommended path for first-time users.

---

## Browser-based UI

The `gui` command starts a local HTTP server and opens the wizard in your default browser. Same workflow as the CLI wizard, plus inline CSV viewers, an event log, and a history view of past runs.

```bash
sonar-migration-tool gui --export_directory ./migration-files
```

| Flag | Description |
|---|---|
| `--export_directory <dir>` | Output directory (default `./migration-files`). |
| `--addr <host:port>` | HTTP bind address (default `localhost:0` — auto-assign port). |
| `--no-browser` | Don't automatically open the browser. |

---

## Predictive report

Generates the same PDF migration summary `migrate` produces, but **before** any SQC API call is made. It reads the output of `extract` and the user-edited mapping CSVs and predicts how the migration will go.

```bash
sonar-migration-tool predictive-report --config config.json
```

Output lands at `<export_directory>/predictive_migration_summary.pdf`. Two classes of outcomes can't be predicted ahead of time — SQC API errors and rate-limiting — so they don't appear in the Failed bucket.

---

## Reset

Wipes every project, portfolio, gate, profile, group, and permission template out of every organization in the target SQC enterprise. Used in test environments to start over from a clean slate.

```bash
sonar-migration-tool reset --config config.json
```

> **⚠️ Destructive.** Reset deletes content from your SonarQube Cloud enterprise. Always confirm `target.enterprise_key` and `target.url` in your config before running this.

---

## Post-migration checklist

1. **Verify the report**: open `migration_summary.pdf` and confirm every section ends in "Perfect" or "Near Perfect" — anything "Failed" or "Partial" needs follow-up.
2. **Confirm projects appear in SonarQube Cloud** and are linked to the right repositories.
3. **Confirm quality gates and quality profiles** are assigned to the right projects.
4. **Re-scan all projects** — historical scan data does not transfer unless `--include_scan_history` was used.
5. **Update CI/CD pipelines** to point to SonarQube Cloud — change `SONAR_TOKEN` to an SQC token and `SONAR_HOST_URL` to `https://sonarcloud.io/`.

---

## Further reading

- [`docs/ADVANCED-CONFIG.md`](docs/ADVANCED-CONFIG.md) — every config field and CLI flag in detail.
- [`docs/BUILD.md`](docs/BUILD.md) — building from source, running with `go run`, contributing.
- [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md) — common errors and how to recover.
- [`docs/MIGRATE.md`](docs/MIGRATE.md) — deep dive on the four-phase migration pipeline.
- [`docs/TRANSFER.md`](docs/TRANSFER.md) — deep dive on the single-command `transfer` workflow.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — internals: packages, task graph, JSONL schemas.
- [`docs/SECURITY.md`](docs/SECURITY.md) — token handling, mTLS, audit trail.

---

## License

See [LICENSE](LICENSE).
