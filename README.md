# Sonar Migration Tool
<!-- updated: 2026-06-17 -->

Migrate your SonarQube Server to SonarQube Cloud — projects, configuration, source code, issues, and last analysis data on each branch.

The tool ships as a single static binary. No installer, no runtime dependencies. Download it, run one command, and your projects land in SonarQube Cloud with their full issue history intact.

---

## Contents

- [What gets migrated](#what-gets-migrated)
- [Before you start](#before-you-start)
- [Quick start — one SonarQube Server into one SonarQube Cloud organization](#quick-start--one-sonarqube-server-into-one-sonarqube-cloud-organization)
  - [Step 1 — Download the tool](#step-1--download-the-tool)
  - [Step 2 — Prepare a configuration file](#step-2--prepare-a-configuration-file)
  - [Step 3 — Open a terminal](#step-3--open-a-terminal)
  - [Step 4 — Run the migration](#step-4--run-the-migration)
  - [Step 5 — Verify in SonarQube Cloud](#step-5--verify-in-sonarqube-cloud)
- [Other scenarios](#other-scenarios)
  - [Migrating a single project (or just a few)](#migrating-a-single-project-or-just-a-few)
  - [Migrating many SonarQube Server instances into one SonarQube Cloud Enterprise](#migrating-many-sonarqube-server-instances-into-one-sonarqube-cloud-enterprise)
- [Prefer a visual interface?](#prefer-a-visual-interface)
- [Something went wrong?](#something-went-wrong)
- [All commands](#all-commands)
- [Configuration reference](#configuration-reference)
- [Want to go deeper?](#want-to-go-deeper)
- [License](#license)

---

## What gets migrated

### ✅ Migrated
* Projects, Quality Gates, Quality Profiles<br>
* Groups, Permissions, Permission Templates<br>
* Project Settings, Webhooks, Links<br>
* Portfolios (Enterprise)<br>
* Project data (Branches with Measures, Issues, Source files, Syntax highlighting, ...) (Optional)<br>
* Issues & Hotspots status, comments, and tags (optional)
* SCM blame authorship

### ❌ NOT migrated
* User accounts & auth
* User Permissions on users
* Analysis history
* Coverage and Duplication data
* Applications
* Portfolio hierarchies
* Issue assignments
* CI/CD pipelines

---

## Before you start

Make sure you have:

- A computer running **macOS, Linux, or Windows**.
- **Admin access** to your SonarQube Server.
- A **SonarQube Cloud** account with the target organizations already created.
- **Two admin tokens** — one for SonarQube Server, one for SonarQube Cloud. The exact permissions are listed in [the MIGRATE guide](docs/MIGRATE.md#token-permissions).

That's it. No Go install, no databases, no config files required for the simple path.

> **Note**: Throughout this guide, **SQS** = SonarQube Server and **SQC** = SonarQube Cloud.

---

## Quick start — one SonarQube Server into one SonarQube Cloud organization

This is the simplest and most common scenario: **migrate every project of a single SonarQube Server into a single SonarQube Cloud organization.** Follow the five steps below. For other layouts (one project only, or several servers into one Enterprise), see [Other scenarios](#other-scenarios).

### Step 1 — Download the tool

Go to the [**Releases** page](https://github.com/sonar-solutions/sonar-migration-tool/releases) and download the binary that matches your operating system:

| OS | Intel X64 | ARM 64 / Apple Silicon |
|---|---|---|
| Linux | `sonar-migration-tool-linux-amd64` | `sonar-migration-tool-linux-arm64` |
| macOS | `sonar-migration-tool-darwin-amd64` | `sonar-migration-tool-darwin-arm64` |
| Windows | `sonar-migration-tool-windows-amd64.exe` | `sonar-migration-tool-windows-arm64.exe` |

Rename the file and, on macOS and Linux, make the binary executable:

```bash
mv sonar-migration-tool-<OS>-<ARCH> sonar-migration-tool
chmod +x sonar-migration-tool
```

You can now run it from the same folder:

```bash
./sonar-migration-tool --help
```

### Step 2 — Prepare a configuration file

The minimal JSON configuration file lists only the mandatory connection details — everything else uses defaults:

```json
{
    "source": {
        "url": "<YOUR_SQS_URL>",
        "token": "<YOUR_SQS_TOKEN>"
    },
    "target": {
        "url": "<YOUR_SQC_URL>",
        "token": "<YOUR_SQC_TOKEN>",
        "enterprise_key": "<YOUR_SQC_ENTERPRISE_KEY>"
    }
}
```

Ready-to-copy examples ship in [`examples/`](examples/): [`config.minimal.example.json`](examples/config.minimal.example.json) (mandatory fields only) and [`config.unified.example.json`](examples/config.unified.example.json) (every option, annotated). For the full field-by-field reference, see [Configuration reference](#configuration-reference).

> **Note**: If you don't want to put tokens or URLs in the configuration file, pass them on the command line instead:
> - `--source_url` and `--source_token` for `extract`
> - `--target_url` and `--target_token` for `migrate` and `reset`
> - All above 4 options for `transfer`

### Step 3 — Open a terminal

- **macOS** — open **Terminal** (find it in Applications → Utilities, or press `⌘ Space` and type "Terminal").
- **Linux** — open your distro's terminal application.
- **Windows** — open **PowerShell** (press the Windows key and type "PowerShell").

### Step 4 — Run the migration

If you want to migrate all SQS projects in a single organization you can run the 4 phases in order, without any manual step. With `--default_organization`, every project goes to the **same** SonarQube Cloud organization, so you don't need to edit `organizations.csv` by hand:

```bash
# 1. Extract everything from SonarQube Server
./sonar-migration-tool extract

# 2. Group projects and generate the mapping files
./sonar-migration-tool structure
./sonar-migration-tool mappings

# 3. Push every project to a single SonarQube Cloud organization
./sonar-migration-tool migrate --default_organization <YOUR_SQC_ORG>
```

If your URLs and tokens are not in the config file, add them on the command line — `extract --source_url <YOUR_SQS_URL> --source_token <YOUR_SQS_TOKEN>` and `migrate --target_url <YOUR_SQC_URL> --target_token <YOUR_SQC_TOKEN>`. Add `--target_url https://sc-staging.io` to target a different SonarQube Cloud instance (e.g. staging).

The config file uses the same unified shape as every other command — one top-level block of shared defaults plus `source` and `target` sub-objects. `concurrency`, `timeout`, `export_directory`, mTLS (`pem_file_path`, `key_file_path`, `cert_password`), `--default_organization`, and `--enterprise_key` are all honored either via the JSON file or as CLI overrides.

Full reference, flags, and resume support: 👉 **[Using `migrate` — Migrate All Projects](docs/MIGRATE.md)**

#### Want a guided experience?

If you'd rather not pick phases yourself, run the interactive wizard — it asks you for the values it needs and runs the right commands for you:

```bash
./sonar-migration-tool wizard
```

### Step 5 — Verify in SonarQube Cloud

Once the command finishes:

1. Log in to [sonarcloud.io](https://sonarcloud.io) or [sonarqube.us](https://sonarqube.us).
2. Open the target organization.
3. Spot-check that your project(s) are listed and the quality gate and quality profile are correct.
4. Unless you passed `--skip_project_data_migration`, verify that issues, hotspots, and their creation dates match the source — and that non-main branches appear under **Branches** with their issues. You can also run `./sonar-migration-tool regtest` for automated verification.
5. Unless you passed `--skip_issue_sync`, verify that issues and hotspots marked as **false positive**, **accepted**, and **safe** respectively are in the same status on SonarQube Cloud.
6. **Re-scan your projects in CI** to seed ongoing analysis. If you used `--skip_project_data_migration`, this first scan will be the baseline for all issue tracking.
7. Update your CI/CD pipeline to point at SonarQube Cloud (`$SONAR_TOKEN`, `$SONAR_HOST_URL`, and `sonar.organization`).

For the full post-migration checklist, see [After you migrate](docs/MIGRATE.md#after-you-migrate) in the MIGRATE guide.

---

## Other scenarios

### Migrating a single project (or just a few)

Use `transfer`. It runs the whole migration in a single command — extracting from SonarQube Server, mapping the configuration, importing source code and issues, and pushing everything to SonarQube Cloud — then writes a PDF summary you can hand to your team.

`transfer` shares the same `--config` file and the same direction-neutral CLI flags as `extract` / `migrate` / `reset` — `--source_*` for the SonarQube Server side, `--target_*` for the SonarQube Cloud side. Anything you don't pass on the CLI is read from the config file; CLI flags always win.

```bash
./sonar-migration-tool transfer \
  --source_url <YOUR_SQS_URL> \
  --source_token <YOUR_SQS_TOKEN> \
  --project_key <YOUR_PROJECT_KEY> \
  --target_url https://sonarcloud.io \
  --target_token <YOUR_SQC_TOKEN> \
  --enterprise_key <YOUR_SQC_ENTERPRISE_KEY> \
  --default_organization <YOUR_SQC_ORG>
```

Full reference, more examples, and the config-file format: 👉 **[Using `transfer` — Transfer One Project](docs/TRANSFER.md)**

> **Note**: You may use https://sonarqube.us instead of https://sonarcloud.io to migrate to the US instance of SonarQube Cloud.

### Migrating many SonarQube Server instances into one SonarQube Cloud Enterprise

* Run `extract` for as many SonarQube Server instances as you have
* Run `structure` and `mappings` once
* Edit `organizations.csv` to map projects to SonarQube Cloud organizations (per row, column 2)
* Run `migrate`

```bash
./sonar-migration-tool extract --source_url <YOUR_SQS_1_URL> --source_token <YOUR_SQS_1_TOKEN>
./sonar-migration-tool extract --source_url <YOUR_SQS_2_URL> --source_token <YOUR_SQS_2_TOKEN>
...
./sonar-migration-tool extract --source_url <YOUR_SQS_n_URL> --source_token <YOUR_SQS_n_TOKEN>

./sonar-migration-tool structure
./sonar-migration-tool mappings

# → edit organizations.csv to set sonarcloud_org_key per row (column 2)

./sonar-migration-tool migrate --enterprise_key <YOUR_SQC_ENTERPRISE_KEY> --target_url <SQC_URL> --target_token <SQC_TOKEN>
```

---

## Prefer a visual interface?

> **⚠️ Experimental:** The GUI is experimental in the current version of `sonar-migration-tool`. It may change between releases and can have rough edges. For production migrations, prefer the CLI.

If you'd rather click through the migration in a browser instead of typing commands, run the GUI:

```bash
./sonar-migration-tool gui
```
It opens the same workflow in your default browser with progress bars, an event log, and CSV viewers for the mapping files.

---

## Something went wrong?

Most errors fall into a few common buckets — see [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the full list.
You may want to rerun the command with the extra `--debug` flag to get more troubleshooting logs.

---

## All commands

| Command | Purpose |
|---|---|
| `transfer` | One-command end-to-end migration (extract → structure → mappings → migrate → PDF report) |
| `extract` | Extract data from a SonarQube Server instance |
| `structure` | Group extracted projects into organizations |
| `mappings` | Generate entity mapping CSVs |
| `migrate` | Push configuration and data to SonarQube Cloud |
| `wizard` | Interactive guided migration (terminal) |
| `gui` | Browser-based guided migration |
| `report` | Generate a migration or maturity report |
| `predictive-report` | Generate a pre-migration PDF summary (no Cloud API calls) |
| `regtest` | Exhaustive post-migration regression verification |
| `reset` | Delete all migrated entities from a SonarQube Cloud organization |

---

## Configuration reference

Every command accepts the same unified JSON config — one top-level block of shared defaults plus `source` and `target` sub-objects — and equivalent CLI flags (CLI flags always override the config file). Two ready-to-copy examples ship in [`examples/`](examples/):

- [`config.minimal.example.json`](examples/config.minimal.example.json) — only the mandatory fields, everything else defaulted.
- [`config.unified.example.json`](examples/config.unified.example.json) — every available option, annotated.

For the complete list of **every configuration field and CLI flag** — its role, default, applicable commands, and whether it's mandatory — see the [**All parameters reference** in ADVANCED-CONFIG.md](docs/ADVANCED-CONFIG.md#all-parameters-reference).

---

## Want to go deeper?

- 📘 [Architecture overview](docs/ARCHITECTURE.md) — how the tool is built.
- ⚙️ [Advanced configuration reference](docs/ADVANCED-CONFIG.md) — every config field and CLI flag, plus legacy config shapes.
- 🧪 [Regression testing protocol](docs/REGRESSION-TESTING.md) — verify changes against live SonarQube + SonarQube Cloud.

---

## License

See [LICENSE](LICENSE) for details.
