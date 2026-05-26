# Manual Migration Guide

## Overview

This guide walks you through migrating from SonarQube Server to SonarQube Cloud using the manual (step-by-step) approach. The manual method gives you more control over the process by letting you run each phase separately, inspect intermediate files, and make adjustments along the way.

If you prefer a guided experience, use the `wizard` command instead. See the [Interactive Wizard Alternative](#interactive-wizard-alternative) section at the bottom of this guide.

---

## Prerequisites

- **Go 1.25+** (to build from source) or download a pre-built binary from [GitHub Releases](https://github.com/sonar-solutions/sonar-migration-tool/releases)
- **SonarQube Server admin token** with:
  - Administer System
  - Quality Gates (read/write)
  - Quality Profiles (read/write)
  - Browse on all projects you want to migrate
- **SonarQube Cloud enterprise account with an admin token** with:
  - Enterprise-level access
  - Admin access to all target organizations in SonarQube Cloud

### Token Permissions Summary

| Token              | Required Permissions                                                        |
|--------------------|-----------------------------------------------------------------------------|
| SonarQube Server   | Administer System, Quality Gates, Quality Profiles, Browse (all projects)   |
| SonarQube Cloud    | Enterprise-level access, Admin on all target organizations                  |

---

## Migration Workflow

```
EXTRACT --> STRUCTURE --> MAPPINGS --> MIGRATE
```

1. **Extract** -- Pull data out of your SonarQube Server instance.
2. **Structure** -- Generate an organization structure file from the extracted data.
3. **Mappings** -- Create entity mapping files (gates, profiles, groups, etc.).
4. **Migrate** -- Push everything into SonarQube Cloud.

---

## Step-by-Step Guide

All examples below show both forms. Use whichever matches your setup:
- **From source:** `cd go && go run . <command> [args]`
- **Built binary:** `sonar-migration-tool <command> [args]`

> **Note:** The default `--export_directory` is `/app/files/`. When running natively, always pass `--export_directory` to point to a local directory (e.g., `./files/`).

### Step 1: Create a Working Directory

```bash
mkdir sonar-migration && cd sonar-migration
mkdir files
```

All subsequent commands assume you are running them from inside this directory.

---

### Step 2: Extract Data from SonarQube Server

This step connects to your SonarQube Server and exports all the data needed for migration.

```bash
# From source
cd go && go run . extract http://localhost:9000 YOUR_TOKEN --export_directory ../files/

# Built binary
sonar-migration-tool extract http://localhost:9000 YOUR_TOKEN --export_directory ./files/
```

#### Extract Options

| Option              | Description                                              | Default |
|---------------------|----------------------------------------------------------|---------|
| `--concurrency`     | Number of concurrent API requests                        | auto    |
| `--timeout`         | Request timeout in seconds                               | 60      |
| `--extract_id`      | Resume a previously started extraction by its ID         | --      |
| `--extract_type`    | Type of extraction to perform                            | --      |
| `--target_task`     | Run a specific extraction task only                      | --      |
| `--pem_file_path`   | Path to a PEM client certificate file (mTLS)             | --      |
| `--key_file_path`   | Path to a private key file (mTLS)                        | --      |
| `--cert_password`   | Password for the client certificate (mTLS)               | --      |
| `--project_key`     | Comma-separated list of project keys to scope the extract to. See [Single-Project / Selective Re-Migration](#single-project--selective-re-migration). | --      |

---

### Step 3: Generate Organization Structure

This step reads the extracted data and generates an `organizations.csv` file.

```bash
# From source
cd go && go run . structure --export_directory ../files/

# Built binary
sonar-migration-tool structure --export_directory ./files/
```

---

### Step 4: Edit organizations.csv

Open `files/organizations.csv` in any spreadsheet editor or text editor. Fill in the `sonarcloud_org_key` column with the key of the SonarQube Cloud organization where each group of projects should be migrated.

Example:

```csv
server_url,sonarcloud_org_key
http://localhost:9000,my-cloud-org-key
```

Save the file when you are done.

---

### Step 5: Generate Entity Mappings

This step generates mapping files that control how quality gates, quality profiles, groups, permission templates, and portfolios are migrated.

```bash
# From source
cd go && go run . mappings --export_directory ../files/

# Built binary
sonar-migration-tool mappings --export_directory ./files/
```

This produces the following files in your `files/` directory:

- `gates.csv` -- Quality Gate mappings
- `profiles.csv` -- Quality Profile mappings
- `groups.csv` -- Group mappings
- `templates.csv` -- Permission Template mappings
- `portfolios.csv` -- Portfolio mappings

You can review or edit any of these files before proceeding.

---

### Step 6: Run the Migration

Push everything to SonarQube Cloud. You will need your SonarQube Cloud admin token and enterprise key.

```bash
# From source
cd go && go run . migrate YOUR_CLOUD_TOKEN YOUR_ENTERPRISE_KEY --export_directory ../files/

# Built binary
sonar-migration-tool migrate YOUR_CLOUD_TOKEN YOUR_ENTERPRISE_KEY --export_directory ./files/
```

#### Migrate Options

| Option              | Description                                               | Default                   |
|---------------------|-----------------------------------------------------------|---------------------------|
| `--url`             | SonarQube Cloud URL                                       | https://sonarcloud.io/    |
| `--edition`         | Target edition                                            | --                        |
| `--concurrency`     | Number of concurrent API requests                         | 25                        |
| `--run_id`          | Resume a previously started migration by its run ID       | --                        |
| `--target_task`     | Run a specific migration task only                        | --                        |
| `--skip_profiles`   | Skip migrating quality profiles                           | --                        |
| `--project_key`     | Comma-separated list of project keys to scope the migration to. See [Single-Project / Selective Re-Migration](#single-project--selective-re-migration). | --                        |

---

### Step 7: Verify

Once the migration is complete:

1. **Log in to SonarQube Cloud** and check:
   - Quality Gates -- Are they all present and configured correctly?
   - Quality Profiles -- Do they match what you had on SonarQube Server?
   - Groups -- Are all user groups created with the right memberships?
   - Projects -- Are all projects visible and assigned to the correct organization?

2. **Generate a migration report:**

   ```bash
   # From source
   cd go && go run . report --report_type migration --export_directory ../files/

   # Built binary
   sonar-migration-tool report --report_type migration --export_directory ./files/
   ```

3. **Generate an analysis report** for a specific migration run:

   ```bash
   # From source
   cd go && go run . analysis_report <RUN_ID> --export_directory ../files/

   # Built binary
   sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/
   ```

---

## Single-Project / Selective Re-Migration

`extract` and `migrate` both accept a `--project_key` flag whose value is a **comma-separated list of SonarQube project keys**. When set, the run is scoped to just those projects. When omitted, every project is processed (today's default). Two common workflows:

### Test the tool against one project before a full migration

Before running a full migration against every project on your instance, validate the tool end-to-end against a single representative project. Smaller blast radius, faster iteration:

```bash
# 1. Extract just one project from SonarQube Server.
sonar-migration-tool extract http://localhost:9000 YOUR_SQS_TOKEN \
  --project_key okorach-oss_sonar-tools \
  --export_directory ./files/

# 2. Generate the org-structure CSV and fill in sonarcloud_org_key as usual.
sonar-migration-tool structure --export_directory ./files/
#   edit organizations.csv → set sonarcloud_org_key

# 3. Generate entity mappings.
sonar-migration-tool mappings --export_directory ./files/

# 4. Migrate just that one project to SonarQube Cloud.
sonar-migration-tool migrate YOUR_CLOUD_TOKEN YOUR_ENTERPRISE_KEY \
  --project_key okorach-oss_sonar-tools \
  --export_directory ./files/
```

Only that project is extracted from SQS and pushed to SQC, so the round trip stays fast and your target organization stays clean for repeat tests. Use `reset` (see "Additional Commands" in the [README](../README.md)) between runs to wipe SQC if you need a fresh baseline.

### Selectively re-migrate a few failed projects after a full run

When a full migration succeeded for most of your projects but failed for a handful, you don't need to re-run the whole pipeline. Re-extract and re-migrate just the offenders by listing them:

```bash
sonar-migration-tool extract http://localhost:9000 YOUR_SQS_TOKEN \
  --project_key projA,projB,projC \
  --export_directory ./files/

sonar-migration-tool migrate YOUR_CLOUD_TOKEN YOUR_ENTERPRISE_KEY \
  --project_key projA,projB,projC \
  --export_directory ./files/
```

`createProjects` is idempotent — a project that already exists on SonarQube Cloud is reported as `already exists` and the downstream tasks (profiles, gates, settings, …) proceed normally against the existing project key.

### Flag semantics

- Whitespace around each key is tolerated and trimmed: `--project_key "projA, projB , projC"` parses cleanly.
- Empty tokens are dropped: `projA,,projB` parses as `{projA, projB}`.
- Duplicates collapse silently.
- The same value can come from a JSON config file as `"project_key": "projA,projB,projC"`.
- Empty / unset → no filter (today's behaviour: every project).

### What's NOT in scope for `--project_key` (yet)

Issue #98 also asks for fuller per-project data migration — issue status, user comments, custom tags, hotspots, external (third-party) issues, and MQR-mode impacts. The scan-history side of that pipeline (files, SCM backdating, basic issues, scanner-report submission) already runs when you pass `--include_scan_history`, but the metadata sync to SonarCloud's REST API for transitions / comments / tags / hotspots ships in a follow-up PR.

---

## Multi-Server Migration

If you are migrating from multiple SonarQube Server instances:

1. **Extract from each server separately** -- Run `extract` once per server, each time pointing to a different URL and token.
2. **Run `structure`** -- This step automatically aggregates data from all extractions into a single `organizations.csv`.
3. **Edit `organizations.csv`** -- Fill in the `sonarcloud_org_key` for each server row.
4. **Continue with `mappings` and `migrate`** as described above. The tool handles all servers in one pass.

---

## Resuming Failed Operations

If a step fails partway through, you can pick up where you left off:

- **Resuming an extraction:**

  ```bash
  sonar-migration-tool extract http://localhost:9000 YOUR_TOKEN --extract_id <PREVIOUS_EXTRACT_ID> --export_directory ./files/
  ```

- **Resuming a migration:**

  ```bash
  sonar-migration-tool migrate YOUR_CLOUD_TOKEN YOUR_ENTERPRISE_KEY --run_id <PREVIOUS_RUN_ID> --export_directory ./files/
  ```

The tool tracks which tasks have already been completed and skips them automatically.

---

## Output Files Reference

| File                     | Description                                                    |
|--------------------------|----------------------------------------------------------------|
| `extract.json`           | Metadata about the extraction (timestamps, server info, etc.)  |
| `requests.log`           | Log of all API requests made during extraction                 |
| `results.*.jsonl`        | Raw extracted data in JSON Lines format (one file per entity)  |
| `organizations.csv`      | Server-to-organization mapping (you edit this)                 |
| `projects.csv`           | List of all extracted projects                                 |
| `gates.csv`              | Quality Gate mappings                                          |
| `profiles.csv`           | Quality Profile mappings                                       |
| `groups.csv`             | Group mappings                                                 |
| `templates.csv`          | Permission Template mappings                                   |
| `portfolios.csv`         | Portfolio mappings                                             |

---

## Interactive Wizard Alternative

If you would rather not run each command separately, the `wizard` command provides an interactive, guided experience:

```bash
# From source
cd go && go run . wizard --export_directory ./files/

# Built binary
sonar-migration-tool wizard --export_directory ./files/
```

The wizard includes:

- **Resume support** -- Picks up where you left off if interrupted
- **Client certificate prompts** -- Prompts for mTLS details when needed
- **Progress display** -- Real-time progress for each phase
- **Validation** -- Validates inputs before proceeding to each step
