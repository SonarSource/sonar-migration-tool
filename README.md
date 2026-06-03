# SonarQube Migration Tool

Migrates SonarQube Server configurations to SonarQube Cloud — quality gates, quality profiles, groups, permissions, projects, portfolios, and scan history.

Built in Go as a single static binary. Download a pre-built binary from [GitHub Releases](https://github.com/sonar-solutions/sonar-migration-tool/releases) or build from source.

## Migration Workflow

```
┌───────────┐   ┌───────────┐   ┌───────────┐   ┌──────────┐   ┌──────────┐   ┌─────────┐
│  EXTRACT  │──►│ STRUCTURE │──►│ ORG MAP   │──►│ MAPPINGS │──►│ VALIDATE │──►│ MIGRATE │
│  Phase 1  │   │  Phase 2  │   │  Phase 3  │   │ Phase 4  │   │ Phase 5  │   │ Phase 6 │
└───────────┘   └───────────┘   └───────────┘   └──────────┘   └──────────┘   └─────────┘
```

---

## Prerequisites

- **Go 1.25+** (to build from source) or download a pre-built binary from [GitHub Releases](https://github.com/sonar-solutions/sonar-migration-tool/releases)
- **SonarQube Server token** with: Administer System, Administer Quality Gates, Administer Quality Profiles, Browse all projects
- **SonarQube Cloud token** with enterprise admin + organization admin permissions for all target orgs
- **SonarQube Cloud enterprise** with target organizations already created

### Building from Source

```bash
cd go
go build -o sonar-migration-tool .
```

Or run directly without building:

```bash
cd go
go run . <command> [args]
```

> **Note:** The default `--export_directory` is `./migration-files` (created in the current working directory). You can override it with the `--export_directory` flag or the `export_directory` field in the JSON config file. Every command prints `See sonar-migration-tool output results in <directory>` when it finishes so you always know where to look.

---

## What Gets Migrated

| Migrated | NOT Migrated |
|----------|-------------|
| Projects, Quality Gates, Quality Profiles | Issues and their history |
| Groups, Permissions, Permission Templates | Source code (re-scan after migration) |
| Portfolios | |
| Scan History (optional) | |

---

## Transfer (Simplest — Single Project)

> **Use this when you want to migrate one project in one command.**

```bash
# From source
cd go && go run . transfer \
  --sq-url https://sonarqube.example.com \
  --sq-token sqp_xxx \
  --project-key my-project \
  --sc-token squ_xxx \
  --sc-org my-org

# Built binary
sonar-migration-tool transfer --sq-url ... --sq-token ... --project-key ... --sc-token ... --sc-org ...

# Config file
sonar-migration-tool transfer -c config.json
```

`config.json` format:
```json
{
  "sonarqube": { "url": "https://...", "token": "sqp_xxx", "projectKey": "my-project" },
  "sonarcloud": { "token": "squ_xxx", "organization": "my-org" }
}
```

| Flag | Description |
|------|-------------|
| `-c, --config` | Config file path |
| `--sq-url` | SonarQube Server URL |
| `--sq-token` | SonarQube Server token |
| `--project-key` | Project key to transfer (omit to transfer all projects) |
| `--sc-token` | SonarQube Cloud token |
| `--sc-org` | SonarQube Cloud organization key |
| `--sc-enterprise-key` | SonarQube Cloud enterprise key (defaults to `--sc-org`) |
| `--export-dir` | Working directory for intermediate files (default: `./migration-files/`) |
| `--include-scan-history` | Extract and import full issue/hotspot scan history |

Chains extract → structure → mappings → migrate automatically. Generates a PDF summary on completion.

---

## Interactive Wizard (Recommended)

> **Recommended for most users. No scripting required.**

```bash
# From source
cd go && go run . wizard --export_directory ./files/

# Built binary
sonar-migration-tool wizard --export_directory ./files/
```

The wizard guides you through all six phases with prompts:

1. **Extract** — connects to SonarQube Server and extracts configuration
2. **Structure** — organizes extracted data into organizations and projects
3. **Org Mapping** — maps each Server organization to a SonarQube Cloud organization
4. **Mappings** — generates mapping files for gates, profiles, groups, templates, and portfolios
5. **Validate** — confirms all organizations are mapped and required files exist
6. **Migrate** — applies the configuration to SonarQube Cloud

Key features:
- **Resume support** — saves state and picks up from the last completed phase if interrupted
- **mTLS support** — prompts for client certificate details when needed
- **Scan history import** — optionally extracts and imports historical analysis data

---

## Browser-Based GUI

> **Same wizard workflow with a visual interface. Opens in your default browser.**

```bash
# From source
cd go && go run . gui --export_directory ./files/

# Built binary
sonar-migration-tool gui --export_directory ./files/
```

| Flag | Description |
|------|-------------|
| `--export_directory` | Output directory (default: `./migration-files`) |
| `--addr` | Address to bind HTTP server (default: `localhost:0` — auto-assigns port) |
| `--no-browser` | Don't automatically open the browser |

The GUI provides:
- Interactive wizard stepper with real-time progress
- Event log showing all operations as they happen
- Run history to browse and review past migrations
- CSV viewers for mapping files
- Migration and maturity report viewers
- Dark/light theme toggle

---

## Manual CLI Method

> For scripting, automation, or advanced users. Most users should use the wizard or GUI above.

All examples below show both forms. Use whichever matches your setup:
- **From source:** `cd go && go run . <command> [args]`
- **Built binary:** `sonar-migration-tool <command> [args]`

### Configuration

Commands can be configured via CLI flags, positional arguments, or a JSON config file (`--config path/to/config.json`). Config file values are overridden by CLI flags when both are provided.

#### Unified config file

`extract`, `migrate`, `reset`, and `predictive-report` all read the same JSON file. The recommended shape carries one top-level block of defaults and one `source` / `target` sub-block per side of the migration — `extract` reads `source`, `migrate` / `reset` read `target`, and each command silently ignores the block that isn't its own.

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

**Top-level fields** (all optional):

| Field | Default | Description |
|---|---|---|
| `concurrency` | `10` | Default max parallel HTTP calls. |
| `timeout` | `60` | Default HTTP request timeout in seconds. |
| `export_directory` | `./migration-files` | Root directory for extract / migrate output. |

The same `concurrency` and `timeout` fields exist inside `source` and `target` — when set there they override the top-level value for that command only.

**`source` block** (consumed by `extract`):

| Field | Required | Description |
|---|---|---|
| `url` | ✅ | SonarQube Server base URL. |
| `token` | ✅ | SonarQube Server user token. |
| `extract_type` | | `"all"` (default) or the name of a specific extract task. |
| `concurrency` / `timeout` | | Override the top-level defaults. |
| `pem_file_path` / `key_file_path` / `cert_password` | | Client-side mTLS certificate, if required. |
| `target_task` | | Stop extract at a specific task (dependencies still run). |
| `extract_id` | | Reuse an existing extract directory ID instead of generating a new one. |
| `enterprise_key` / `organization_key` / `edition` | | Provisional — accepted but ignored today; reserved for future SQC-to-SQC migration. |

**`target` block** (consumed by `migrate` and `reset`):

| Field | Required | Description |
|---|---|---|
| `url` | ✅ | SonarQube Cloud base URL (e.g. `https://sonarcloud.io/`). |
| `token` | ✅ | SonarQube Cloud user token. |
| `enterprise_key` | | Enterprise key used to scope enterprise endpoints. |
| `edition` | | `"enterprise"` (default), `"team"`, or `"foss"`. |
| `concurrency` / `timeout` | | Override the top-level defaults. |
| `run_id` | | Resume an in-progress migrate run by ID. |
| `target_task` | | Stop migrate at a specific task. |
| `organization_key` | | Provisional — accepted but ignored today. |

A full example lives at [`examples/config.unified.example.json`](examples/config.unified.example.json) and a JSON Schema for editor autocomplete at [`schemas/config.schema.json`](schemas/config.schema.json). Add the schema to your editor by referencing it in `.vscode/settings.json` or by adding a `"$schema"` pointer at the top of your config.

The three legacy shapes (flat top-level keys, `extract` / `migrate` sub-objects, and `sonarqube` + `sonarcloud` side-sectioned) still parse — existing configs keep working.

### 1. Extract

```bash
# From source
go run . extract <URL> <TOKEN> --export_directory ./files/ [--concurrency 25] [--timeout 60]

# Built binary
sonar-migration-tool extract <URL> <TOKEN> --export_directory ./files/ [--concurrency 25] [--timeout 60]
```

| Flag | Description |
|------|-------------|
| `--config` | Path to a JSON configuration file |
| `--extract_id` | Resume a previous extraction |
| `--target_task` | Run a specific task (with its dependencies) |
| `--concurrency` | Max concurrent requests (default: server-detected) |
| `--timeout` | Request timeout in seconds |
| `--extract_type` | Type of extract to run |
| `--export_directory` | Output directory (default: `./migration-files`) |
| `--include_scan_history` | Extract full issue data, source code, and SCM blame for scan history import |
| `--pem_file_path` | Client certificate PEM file (mTLS) |
| `--key_file_path` | Client certificate key file (mTLS) |
| `--cert_password` | Client certificate password (mTLS) |

For multiple servers: run `extract` once per server; `structure` aggregates all results.

### 2. Structure

```bash
# From source
go run . structure --export_directory ./files/

# Built binary
sonar-migration-tool structure --export_directory ./files/

# Or, reuse the extract config (export_directory is read from it)
sonar-migration-tool structure --config extract-config.json
```

| Flag | Description |
|------|-------------|
| `--export_directory` | Root directory containing the extract output (default: `./migration-files`) |
| `--config` | Path to JSON config file (same shape as `extract --config`). `export_directory` is read from it; when exactly one SonarCloud organization is defined, its key pre-populates `sonarcloud_org_key`. `--export_directory` on the CLI overrides the config value. |

Then edit `files/organizations.csv` — fill in `sonarcloud_org_key` for each row before continuing.

### 3. Mappings

```bash
# From source
go run . mappings --export_directory ./files/

# Built binary
sonar-migration-tool mappings --export_directory ./files/

# Or, reuse the extract config (export_directory is read from it)
sonar-migration-tool mappings --config extract-config.json
```

| Flag | Description |
|------|-------------|
| `--export_directory` | Root directory containing the extract output (default: `./migration-files`) |
| `--config` | Path to JSON config file (same shape as `extract --config`); `export_directory` is read from it. `--export_directory` on the CLI overrides the config value. |

Outputs `gates.csv`, `profiles.csv`, `groups.csv`, `templates.csv`, `portfolios.csv`.

### 4. Migrate

```bash
# From source
go run . migrate <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/ [--run_id <id>] [--skip_profiles]

# Built binary
sonar-migration-tool migrate <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/ [--run_id <id>] [--skip_profiles]
```

| Flag | Description |
|------|-------------|
| `--config` | Path to a JSON configuration file |
| `--run_id` | Resume a failed migration from the last completed task |
| `--target_task` | Run a specific migration task (with its dependencies) |
| `--skip_profiles` | Skip quality profile migration/provisioning |
| `--edition` | SonarQube Cloud license edition |
| `--url` | SonarQube Cloud URL (default: `https://sonarcloud.io/`) |
| `--concurrency` | Max concurrent requests |
| `--export_directory` | Directory containing SonarQube exports (default: `./migration-files`) |

---

## Additional Commands

**Analysis Report** — parse `requests.log` into a CSV summary of API call outcomes:
```bash
# From source
go run . analysis_report <RUN_ID> --export_directory ./files/

# Built binary
sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/
```

**Report** — generate a migration readiness or maturity report:
```bash
# From source
go run . report --report_type migration --export_directory ./files/

# Built binary
sonar-migration-tool report --report_type migration --export_directory ./files/
```

**Predictive Report** — generate the same PDF migration summary the
`migrate` step produces, but *before* migrating, from the output of
`extract` + `structure` and the user-edited mapping CSVs. Useful to
preview how the migration will go without touching SonarQube Cloud.

```bash
# From source
go run . predictive-report --export_directory ./files/

# Built binary
sonar-migration-tool predictive-report --export_directory ./files/

# Or read export_directory from the same JSON config used by extract / migrate (#246)
sonar-migration-tool predictive-report --config extract-config.json
```

Output: `<export_directory>/predictive_migration_summary.pdf`. The
`--config` flag accepts the same configuration file shape as `extract`
or `migrate` — only the `export_directory` field is read. An explicit
`--export_directory` flag overrides whatever the config file carries.

The Global Settings section is included with the SQS-only settings
predicted to be Skipped (Setting Key column, sorted alphabetically).
SonarQube Cloud API errors or rate-limiting cannot be predicted ahead
of time, so they have no row in the Failed bucket.

**Reset** — deletes all content in every org in the enterprise:
```bash
# From source
go run . reset <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/

# Built binary
sonar-migration-tool reset <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/
```

---

## Post-Migration

1. Verify projects appear in SonarQube Cloud and are linked to repositories
2. Verify quality gates and profiles are correct
3. Re-scan all projects — unless scan history was imported, historical data does not transfer
4. Update CI/CD pipelines to point to SonarQube Cloud (`SONAR_TOKEN` and `SONAR_HOST_URL`)

---

## Troubleshooting

**Token does not have sufficient permissions** — ensure the SonarQube Server token has Administer System, Administer Quality Gates, and Administer Quality Profiles permissions.

**Organization not found** — verify `sonarcloud_org_key` in `organizations.csv` matches an existing org in your enterprise.

**SSL / connection errors** — pass client certificate files directly:
```bash
sonar-migration-tool extract <URL> <TOKEN> \
  --pem_file_path ./certs/client.pem \
  --key_file_path ./certs/client.key
```

**Migration fails midway** — re-run the same `migrate` command with `--run_id <id>` (shown in output). Completed tasks are skipped automatically.

**Large instances** — reduce concurrency with `--concurrency 10` and increase timeout with `--timeout 120`.

---

## Best Practices

- Create a dedicated migration user in SonarQube Cloud with enterprise admin permissions
- Test with a subset first using `--target_task` to migrate specific entities
- Review CSV mappings before running migrate
- Monitor `files/<run_id>/requests.log` for API errors
- Use `--config` with a JSON file for repeatable, scripted migrations

---

## Version Support

Supports SonarQube Server 6.3+. Authentication auto-detects version (Basic auth < 10, Bearer token >= 10). Edition-aware: Community, Developer, Enterprise, Data Center.

---

## License

See [LICENSE](LICENSE) for details.
