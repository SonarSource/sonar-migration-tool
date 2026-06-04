# Using `migrate` — Migrate All Projects

> **Quick decision guide**
> - Migrating **one** project and don't need to review intermediate files? Use **[`transfer`](TRANSFER.md)** — a single command that does everything.
> - Want the same multi-phase workflow but with prompts instead of typing each command? Use the **`wizard`** command — interactive and guided.
> - Want full control over each phase, multiple SonarQube Server instances, or to inspect / edit the mapping CSVs before pushing? You're in the right place — keep reading.

The `migrate` command is the **final phase** of a six-phase pipeline. You run it together with the underlying `extract`, `structure`, and `mappings` commands to move configuration from one or more SonarQube Server instances into SonarQube Cloud. Use it when you need:

- To migrate **many** projects from one or more SonarQube Server instances.
- To review and edit the per-entity mapping CSVs (`gates.csv`, `profiles.csv`, `groups.csv`, `templates.csv`, `portfolios.csv`) before pushing to SonarQube Cloud.
- To resume a failed migration from the last completed task without redoing successful work.
- To audit intermediate files for compliance or change management.
- To script the phases independently in CI/CD pipelines.

---

## Migration Workflow

```
┌───────────┐   ┌───────────┐   ┌───────────┐   ┌──────────┐   ┌──────────┐   ┌─────────┐
│  EXTRACT  │──►│ STRUCTURE │──►│ ORG MAP   │──►│ MAPPINGS │──►│ VALIDATE │──►│ MIGRATE │
│  Phase 1  │   │  Phase 2  │   │  Phase 3  │   │ Phase 4  │   │  Phase 5  │   │ Phase 6 │
└───────────┘   └───────────┘   └───────────┘   └──────────┘   └──────────┘   └─────────┘
```

> **Note on the diagram:** Phase 3 ("Org Map") is the human step of editing `organizations.csv` — the tool produces the file in Phase 2, you fill in the SonarQube Cloud org keys, and Phase 4 reads the result.

---

## Token permissions

| Token | Required permissions |
|---|---|
| **SonarQube Server** | Administer System, Quality Gates (read/write), Quality Profiles (read/write), Browse on all projects you want to migrate |
| **SonarQube Cloud** | Enterprise-level access, Admin on all target organizations |

For detailed token setup, see [SECURITY.md](SECURITY.md).

---

## Configuration

The `extract`, `migrate`, `reset`, and `predictive-report` commands all read the same JSON config file. The recommended shape carries one top-level block of defaults and one `source` / `target` sub-block per side of the migration. `extract` reads `source`; `migrate` / `reset` read `target`; each command silently ignores the block that isn't its own.

```jsonc
{
  "concurrency": 10,
  "timeout": 60,
  "export_directory": "./migration-files",

  "source": {
    "url":   "http://sonarqube.example.com",
    "token": "squ_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "extract_type": "all",
    "pem_file_path": null,
    "key_file_path": null,
    "cert_password": null,
    "target_task": null,
    "extract_id":  null
  },

  "target": {
    "url":   "https://sonarcloud.io/",
    "token": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "enterprise_key": "your-enterprise",
    "edition": "enterprise",
    "run_id": null,
    "target_task": null
  }
}
```

A full example lives at [`examples/config.unified.example.json`](../examples/config.unified.example.json) and a JSON Schema for editor autocomplete at [`schemas/config.schema.json`](../schemas/config.schema.json). Add the schema to your editor by referencing it in `.vscode/settings.json` or by adding a `"$schema"` pointer at the top of your config.

The three legacy shapes (flat top-level keys, `extract` / `migrate` sub-objects, and `sonarqube` + `sonarcloud` side-sectioned) still parse — existing configs keep working.

**Precedence:** CLI flags override values from the config file when both are provided. `--export_directory` on the CLI wins over `export_directory` in the config when explicitly set.

For deeper config reference, see [CONFIG.md](CONFIG.md).

---

## Step-by-step guide

All examples show both forms. Use whichever matches your setup:

- **From source:** `cd go && go run . <command> [args]`
- **Built binary:** `sonar-migration-tool <command> [args]`

> The default `--export_directory` is `./migration-files` (created in the current working directory). You can override it with the `--export_directory` flag or the `export_directory` field in the JSON config. Every command prints `See sonar-migration-tool output results in <directory>` when it finishes.

### Step 1 — Create a working directory

```bash
mkdir sonar-migration && cd sonar-migration
mkdir files
```

All subsequent commands assume you are running from inside this directory.

### Step 2 — Extract

Connect to SonarQube Server and export all the data needed for migration.

```bash
# From source
go run . extract <URL> <TOKEN> --export_directory ./files/ [--concurrency 25] [--timeout 60]

# Built binary
sonar-migration-tool extract <URL> <TOKEN> --export_directory ./files/ [--concurrency 25] [--timeout 60]
```

| Flag | Description |
|---|---|
| `--config` | Path to a JSON configuration file (see [CONFIG.md](CONFIG.md)) |
| `--extract_id` | Resume a previous extraction by its ID |
| `--target_task` | Run a specific task (with its dependencies) |
| `--concurrency` | Max concurrent requests (default: server-detected) |
| `--timeout` | Request timeout in seconds |
| `--extract_type` | Type of extract to run |
| `--export_directory` | Output directory (default: `./migration-files`) |
| `--include_scan_history` | Extract full issue data, source code, and SCM blame for scan history import |
| `--pem_file_path` | Client certificate PEM file (mTLS) |
| `--key_file_path` | Client certificate key file (mTLS) |
| `--cert_password` | Client certificate password (mTLS) |

For multiple servers, run `extract` once per server — `structure` aggregates the results.

### Step 3 — Structure

Reads the extracted data and generates an `organizations.csv` file.

```bash
# From source
go run . structure --export_directory ./files/

# Built binary
sonar-migration-tool structure --export_directory ./files/

# Or reuse the extract config (export_directory is read from it)
sonar-migration-tool structure --config extract-config.json
```

| Flag | Description |
|---|---|
| `--export_directory` | Root directory containing the extract output (default: `./migration-files`) |
| `--config` | Path to a JSON config file (same shape as `extract --config`). `export_directory` is read from it; when exactly one SonarCloud organization is defined, its key pre-populates `sonarcloud_org_key`. `--export_directory` on the CLI overrides the config value. |

### Step 4 — Edit `organizations.csv`

Open `files/organizations.csv` in any spreadsheet editor or text editor. Fill in the `sonarcloud_org_key` column with the key of the SonarQube Cloud organization where each group of projects should be migrated.

Example:

```csv
server_url,sonarcloud_org_key
http://localhost:9000,my-cloud-org-key
```

Save the file when you are done.

> **Shortcut for single-org migrations:** if every project on every server is going to land in the same SonarQube Cloud organization, you can skip this step and pass `--default_organization <org-key>` (or set `target.default_organization` in the config file) when running `migrate` in Step 6. The tool fills `sonarcloud_org_key` for every row in `organizations.csv` automatically. If you have already mapped any row by hand, the flag is ignored and a `WARN` is logged. (Issue #281.)

### Step 5 — Mappings

Generates the per-entity mapping CSVs (gates, profiles, groups, templates, portfolios).

```bash
# From source
go run . mappings --export_directory ./files/

# Built binary
sonar-migration-tool mappings --export_directory ./files/

# Or reuse the extract config
sonar-migration-tool mappings --config extract-config.json
```

| Flag | Description |
|---|---|
| `--export_directory` | Root directory containing the extract output (default: `./migration-files`) |
| `--config` | Path to JSON config file (same shape as `extract --config`); `export_directory` is read from it. `--export_directory` on the CLI overrides the config value. |

This produces:

- `gates.csv` — Quality Gate mappings
- `profiles.csv` — Quality Profile mappings
- `groups.csv` — Group mappings
- `templates.csv` — Permission Template mappings
- `portfolios.csv` — Portfolio mappings

You can review or edit any of these before proceeding.

### Step 6 — Migrate

Push everything to SonarQube Cloud. You'll need your SonarQube Cloud admin token and enterprise key.

```bash
# From source
go run . migrate <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/ [--run_id <id>] [--skip_profiles]

# Built binary
sonar-migration-tool migrate <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/ [--run_id <id>] [--skip_profiles]
```

| Flag | Description |
|---|---|
| `--config` | Path to a JSON configuration file |
| `--run_id` | Resume a failed migration from the last completed task |
| `--target_task` | Run a specific migration task (with its dependencies) |
| `--skip_profiles` | Skip quality profile migration/provisioning |
| `--default_organization` | SonarQube Cloud organization key applied to every project when `organizations.csv` has no mapping. Ignored (with a WARN) if any row already carries a `sonarcloud_org_key`. Useful for small instances where every SQS project migrates into one SQC org. |
| `--edition` | SonarQube Cloud license edition |
| `--url` | SonarQube Cloud URL (default: `https://sonarcloud.io/`) |
| `--concurrency` | Max concurrent requests |
| `--export_directory` | Directory containing SonarQube exports (default: `./migration-files`) |

---

## Multi-server migration

If you are migrating from multiple SonarQube Server instances:

1. **Extract from each server separately** — run `extract` once per server, each time pointing to a different URL and token.
2. **Run `structure`** — this step automatically aggregates data from all extractions into a single `organizations.csv`.
3. **Edit `organizations.csv`** — fill in the `sonarcloud_org_key` for each server row.
4. **Continue with `mappings` and `migrate`** as described above. The tool handles all servers in one pass.

---

## Resuming failed operations

If a step fails partway through, you can pick up where you left off:

```bash
# Resume an extraction
sonar-migration-tool extract <URL> <TOKEN> --extract_id <PREVIOUS_EXTRACT_ID> --export_directory ./files/

# Resume a migration
sonar-migration-tool migrate <TOKEN> <ENTERPRISE_KEY> --run_id <PREVIOUS_RUN_ID> --export_directory ./files/
```

The tool tracks which tasks have already completed and skips them automatically.

---

## Additional commands

### `report` — generate a migration readiness or maturity report

```bash
# From source
go run . report --report_type migration --export_directory ./files/

# Built binary
sonar-migration-tool report --report_type migration --export_directory ./files/
```

### `predictive-report` — preview the migration before you commit

Generates the same PDF migration summary the `migrate` step produces, but *before* migrating — from the output of `extract` + `structure` and the user-edited mapping CSVs. Useful to preview how the migration will go without touching SonarQube Cloud.

```bash
# From source
go run . predictive-report --export_directory ./files/

# Built binary
sonar-migration-tool predictive-report --export_directory ./files/

# Or read export_directory from the same JSON config used by extract / migrate
sonar-migration-tool predictive-report --config extract-config.json
```

Output: `<export_directory>/predictive_migration_summary.pdf`. The `--config` flag accepts the same configuration file shape as `extract` or `migrate` — only the `export_directory` field is read. An explicit `--export_directory` flag overrides whatever the config file carries.

The Global Settings section is included with the SQS-only settings predicted to be Skipped (Setting Key column, sorted alphabetically). SonarQube Cloud API errors or rate-limiting cannot be predicted ahead of time, so they have no row in the Failed bucket.

### `analysis_report` — summarize a migration run

Parse `requests.log` into a CSV summary of API call outcomes:

```bash
# From source
go run . analysis_report <RUN_ID> --export_directory ./files/

# Built binary
sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/
```

### `reset` — undo a bad migration

Deletes all content in every org in the enterprise:

```bash
# From source
go run . reset <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/

# Built binary
sonar-migration-tool reset <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/
```

> **Warning:** `reset` is destructive. It deletes all migrated projects, quality profiles, quality gates, and organization configurations.

---

## After you migrate

1. Verify projects appear in SonarQube Cloud and are linked to repositories.
2. Verify quality gates and profiles are correct.
3. Re-scan all projects — unless scan history was imported, historical data does not transfer.
4. Update CI/CD pipelines to point to SonarQube Cloud (`SONAR_TOKEN` and `SONAR_HOST_URL`).
5. Generate a final report for the records:
   ```bash
   sonar-migration-tool report --report_type migration --export_directory ./files/
   ```

---

## Tips for larger instances

- Create a dedicated migration user in SonarQube Cloud with enterprise admin permissions.
- Test with a subset first using `--target_task` to migrate specific entities.
- Review the CSV mappings before running `migrate`.
- Monitor `files/<run_id>/requests.log` for API errors.
- Use `--config` with a JSON file for repeatable, scripted migrations.
- For large instances (50,000+ projects), lower concurrency and increase timeout:
  ```bash
  sonar-migration-tool extract <URL> <TOKEN> --concurrency 10 --timeout 120 --export_directory ./files/
  ```

---

## Output files reference

| File | Description |
|---|---|
| `extract.json` | Metadata about the extraction (timestamps, server info, etc.) |
| `requests.log` | Log of all API requests made during extraction |
| `results.*.jsonl` | Raw extracted data in JSON Lines format (one file per entity) |
| `organizations.csv` | Server-to-organization mapping (you edit this) |
| `projects.csv` | List of all extracted projects |
| `gates.csv` | Quality Gate mappings |
| `profiles.csv` | Quality Profile mappings |
| `groups.csv` | Group mappings |
| `templates.csv` | Permission Template mappings |
| `portfolios.csv` | Portfolio mappings |
| `predictive_migration_summary.pdf` | Output of the `predictive-report` command |
| `<export_directory>/<run_id>/requests.log` | Per-run request log for migrations |

---

## Version support

Supports SonarQube Server 6.3+. Authentication auto-detects version (Basic auth < 10, Bearer token ≥ 10). Edition-aware: Community, Developer, Enterprise, Data Center.
