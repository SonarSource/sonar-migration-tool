# Using `transfer` — Transfer One Project
<!-- updated: 2026-06-05_10:50:51 -->

`transfer` is the **single-command**, **project-scoped** path. It chains the four phases of a migration — **extract → structure → mappings → migrate** — into one call, then writes a PDF summary on completion. Use it when you have one project (or a small, well-known set of projects) to move across.

Unlike a full `migrate`, `transfer` only touches the **specified project** and the entities it actually uses — its quality gate, its quality profiles, its permissions and project settings, and its complete issue and Security Hotspot history (including externally imported issues). Instance-wide entities such as portfolios, global settings, permission templates, and default gate/profile selection are **not** modified. See [What gets migrated](#what-gets-migrated) below.

If you need fine-grained control, want to review the intermediate files between phases, or are migrating many projects across multiple SonarQube Server instances, see [Using `migrate`](MIGRATE.md) instead.

---

## When to use it

- Migrating a single project from SonarQube Server to SonarQube Cloud.
- Quick one-off moves where you don't need to review intermediate files.
- Smoke-testing the tool against a known project before a larger migration.

If any of these sound like you, jump to [MIGRATE.md](MIGRATE.md) instead:

- Multiple SonarQube Server instances.
- You want to inspect or edit the mapping CSVs before pushing.
- You want to run the phases at different times (e.g., extract on a Friday, migrate on a Monday).
- You want to resume a partial migration after a failure.

---

## What it does
<!-- updated: 2026-06-05_10:50:51 -->

Behind the scenes, `transfer` runs the same four phases as the manual workflow, in order:

1. **Extract** — connects to SonarQube Server and pulls the project's configuration **and its full issue/hotspot scan history** (scan history is always included for `transfer`).
2. **Structure** — assembles the extracted data into the project + org structure.
3. **Mappings** — generates the per-entity mapping CSVs (gates, profiles, groups, templates, portfolios).
4. **Migrate** — applies the **project-scoped** subset to SonarQube Cloud: it runs only the tasks needed for the project, its quality gate and profiles, its permissions, and its issue/hotspot history. Their dependencies are resolved automatically; global/instance-wide tasks are skipped.

On success, a PDF migration summary is written into the export directory.

---

## What gets migrated
<!-- updated: 2026-06-05_12:26:41 -->

`transfer` migrates a **project-scoped** slice, not the whole instance.

**Included:**

- The specified **project** (created in the target organization).
- The **quality gate** the project uses, with its conditions.
- The **quality profiles** the project uses, with their rules restored (and any parent relationships).
- The project's **permissions** (group permissions), **settings**, **tags**, **links**, **webhooks**, and **new code period**.
- The project's complete **issue history** — both native SonarQube issues and **externally imported issues** (from third-party analyzers) — replayed via scan-history import, with triage state (status, resolution, assignee, comments, tags) synced afterward.
- The project's **Security Hotspots**, with their review status and comments synced.

**Not modified** (use the full [`migrate`](MIGRATE.md) command for these):

- Portfolios.
- Global settings, global webhooks, and the global new code period.
- Permission templates and default-template assignment.
- Organization-level and profile-level group permissions.
- Default quality gate / default quality profile selection.
- Rule tag and rule description updates.
- ALM / DevOps platform repository bindings.

> **Note on prerequisites.** A few global entities are created on the target only because the project depends on them — for example, the groups referenced by the project's group permissions, and the migration user/permissions used to perform the migration. These are created as needed so the project's own configuration resolves correctly.

> **Note on issue counts.** The target issue count is normally lower than the SonarQube Server total because issues that are **CLOSED** or resolved as **FIXED** have no SonarQube Cloud counterpart and are intentionally skipped (the scanner report only recreates active findings). Open issues plus triaged ones (won't-fix / false-positive / accepted) and all externally-imported issues are migrated. Security Hotspots transfer in full.

> **Known limitation — non-main branches.** Scan-history import reliably lands data only on the project's **main branch**. Non-main branches are submitted (and, after the reference-branch fix, most are now *accepted* by the SonarQube Cloud Compute Engine), but **SonarQube Cloud does not persist non-main branches created this way** — they do not appear under the project's branches and carry no issues. This is a limitation of recreating history by injecting a scanner report via `/api/ce/submit`: branch creation on SonarQube Cloud is driven by the long-lived branch name pattern and the regular scanner's branch orchestration, not by an injected report. The main-branch issues and hotspots are unaffected. See [CLOUDVOYAGER-DELTA.md](CLOUDVOYAGER-DELTA.md) (BUG-17) for the full investigation.

---

## Quick start

### With a config file

```bash
# From source
cd go && go run . transfer -c config.json

# Built binary
sonar-migration-tool transfer -c config.json
```

`config.json` uses the **same unified shape** as `extract` and `migrate` — one top-level block of shared defaults plus `source` and `target` sub-objects. See [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md) for the full reference.

Minimal form:

```json
{
  "source": {
    "url": "https://sonarqube.example.com",
    "token": "sqp_xxx"
  },
  "target": {
    "token": "squ_xxx",
    "default_organization": "my-org"
  }
}
```

Full form:

```json
{
  "concurrency": 25,
  "timeout": 60,
  "export_directory": "./migration-files",
  "project_key": "my-project",
  "source": {
    "url": "https://sonarqube.example.com",
    "token": "sqp_xxx",
    "pem_file_path": "/path/to/cert.pem",
    "key_file_path": "/path/to/cert.key",
    "cert_password": "optional"
  },
  "target": {
    "url": "https://sonarcloud.io/",
    "token": "squ_xxx",
    "default_organization": "my-org",
    "enterprise_key": "my-enterprise"
  }
}
```

### With CLI flags

```bash
# From source
cd go && go run . transfer \
  --source-url https://sonarqube.example.com \
  --source-token sqp_xxx \
  --project-key my-project \
  --target-token squ_xxx \
  --default_organization my-org

# Built binary
sonar-migration-tool transfer \
  --source-url https://sonarqube.example.com \
  --source-token sqp_xxx \
  --project-key my-project \
  --target-token squ_xxx \
  --default_organization my-org
```

Omit `--project-key` to transfer **every** project visible to the token (in which case the rest of the manual workflow applies — see [MIGRATE.md](MIGRATE.md) for the per-project `organizations.csv` mapping step).

---

## Flags

| Flag | Config key | Description |
|------|------------|-------------|
| `-c, --config` | — | Path to a JSON configuration file (see [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md)) |
| `--source-url` | `source.url` | SonarQube Server URL |
| `--source-token` | `source.token` | SonarQube Server token |
| `--project-key` | `project_key` | Project key to transfer. Omit to transfer every project visible to the token. |
| `--target-url` | `target.url` | SonarQube Cloud URL (default: `https://sonarcloud.io/`) |
| `--target-token` | `target.token` | SonarQube Cloud token |
| `--default_organization` | `target.default_organization` | SonarQube Cloud organization key |
| `--enterprise_key` | `target.enterprise_key` | SonarQube Cloud enterprise key (defaults to `--default_organization`) |
| `--export-dir` | `export_directory` | Working directory for intermediate files (default: `./migration-files/`) |
| `--concurrency` | `concurrency` | Max concurrent HTTP requests (default: `25`) |
| `--timeout` | `timeout` | HTTP request timeout in seconds |
| `--pem_file_path` | `source.pem_file_path` | Client mTLS PEM file for the source server |
| `--key_file_path` | `source.key_file_path` | Client mTLS key file for the source server |
| `--cert_password` | `source.cert_password` | Password for the source server mTLS client certificate |
| `--include-scan-history` | `include_scan_history` | Accepted for compatibility but has **no effect** for `transfer` — issue/hotspot scan history is **always** extracted and imported. |
| `--exclude-branches` | `target.exclude_branches` | Glob patterns for non-main branches to skip during scan history import. Repeatable. Main branch is never excluded. |

CLI flags override values from the config file when both are provided.

---

## Output

- **Intermediate files** — written to `--export-dir` (default `./migration-files/`). Same files as the manual workflow: `organizations.csv`, `gates.csv`, `profiles.csv`, `groups.csv`, `templates.csv`, `portfolios.csv`.
- **PDF summary** — written to the export directory on successful completion.
- **Stdout** — every command prints `See sonar-migration-tool output results in <directory>` when it finishes so you always know where to look.

For a full description of every output file, see the [Output Files Reference](MIGRATE.md#output-files-reference) in MIGRATE.md.

---

## After the transfer
<!-- updated: 2026-06-05_10:50:51 -->

1. Log in to SonarQube Cloud and confirm the project appears under the target organization.
2. Spot-check that the quality gate and quality profile are present.
3. Spot-check that issues and hotspots came across (compare counts against the source). Scan history is always imported, so a fresh re-scan is not required to seed historical data — though you should still run a normal analysis once your pipeline is repointed.
4. Update your CI/CD pipeline to point at SonarQube Cloud (`SONAR_TOKEN`, `SONAR_HOST_URL`).

For more on post-migration steps, see the [After you migrate](MIGRATE.md#after-you-migrate) section in MIGRATE.md.

---

## Troubleshooting

- **Token errors** — see the [Token permissions](MIGRATE.md#token-permissions) section in MIGRATE.md.
- **Org not found** — confirm `--default_organization` matches an existing organization in your SonarQube Cloud enterprise.
- **Anything else** — [TROUBLESHOOTING.md](TROUBLESHOOTING.md) has the full list of common errors.
