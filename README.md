# Sonar Migration Tool
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Migrate your SonarQube Server to SonarQube Cloud — projects, configuration, source code, issues, and history.

The tool ships as a single static binary. No installer, no runtime dependencies. Download it, run one command, and your projects land in SonarQube Cloud with their full issue history intact.

---

## What gets migrated
<!-- updated: 2026-06-05_19:25:00 -->

### ✅ Migrated
* Projects, Quality Gates, Quality Profiles<br> 
* Groups, Permissions, Permission Templates<br>
* Project Settings, Webhooks, Links<br>
* Portfolios (Enterprise)<br>
* Project data (Branches with Source files, Measures, Issues...) (Optional)<br>
* Issues & Hotspots status, comments, and tags (optional)

### ❌ NOT migrated
* User accounts & auth
* User Permissions on users
* Analysis history
* Applications
* Portfolio hierarchies
* Issue assignments
* CI/CD pipelines
* SCM blame authorship

---

## Before you start
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Make sure you have:

- A computer running **macOS, Linux, or Windows**.
- **Admin access** to your SonarQube Server.
- A **SonarQube Cloud** account with the target organizations already created.
- **Two admin tokens** — one for SonarQube Server, one for SonarQube Cloud. The exact permissions are listed in [the MIGRATE guide](docs/MIGRATE.md#token-permissions).

That's it. No Go install, no databases, no config files required for the simple path.

---

## Step 1 — Download the tool
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Go to the [**Releases** page](https://github.com/sonar-solutions/sonar-migration-tool/releases) and download the binary that matches your operating system:

| OS | Intel X64 | ARM 64 / Apple Silicon |
|---|---|---|
| Linux | sonar-migration-tool-linux-amd64 | sonar-migration-tool-linux-arm64 |
| macOS | sonar-migration-tool-darwin-amd64 | sonar-migration-tool-darwin-arm64 |
| Windows | sonar-migration-tool-windows-amd64.exe | sonar-migration-tool-windows-arm64.exe |

Rename the file and, on macOS and Linux, make the binary executable:

```bash
mv sonar-migration-tool-<OS>-<ARCH> sonar-migration-tool
chmod +x sonar-migration-tool
```


You can now run it from the same folder:

```bash
./sonar-migration-tool --help
```

---

## Step 2 — Prepare a configuration file
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

The minimal JSON configuration file should look like this
```json
{
    "concurrency": 10,
    "timeout": 60,
    "source": {
        "url": "<YOUR_SQS_URL>",
        "token": "<YOUR_SQS_TOKEN>",
    },
    "target": {
        "url": "<YOUR_SQC_URL>",
        "token": "<YOUR_SQC_TOKEN>",
        "enterprise_key": "<YOUR_SQC_ENTERPRISE_KEY>",
        "edition": "enterprise",
    }
}
```
**Note**: If you don't want to disclose tokens or URL in the configuration file, you can pass them on the tool command line (as `--url` and `--token` parameters)
**Note**: SQS = SonarQube Server, SQC = SonarQube Cloud


## Step 2 — Open a terminal
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

- **macOS** — open **Terminal** (find it in Applications → Utilities, or press `⌘ Space` and type "Terminal").
- **Linux** — open your distro's terminal application.
- **Windows** — open **PowerShell** (press the Windows key and type "PowerShell").

---
## Step 3 — Run the migration, for all projects
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Or use a **config file** to keep tokens out of your shell history:

```bash
# Example with URL and Token passed on the command line if not in the configuration file
./sonar-migration-tool extract --url <YOUR_SQS_URL> --token <YOUR_SQS_TOKEN>
./sonar-migration-tool structure
./sonar-migration-tool mappings

# → edit organizations.csv to set sonarcloud_org_key per row

./sonar-migration-tool migrate --url <YOUR_SQC_URL> --token <YOUR_SQC_TOKEN>
```
**Note**: SQS = SonarQube Server, SQC = SonarQube Cloud


The config file uses the same unified shape as every other command — one top-level block of shared defaults plus `source` and `target` sub-objects. `concurrency`, `timeout`, `export_directory`, mTLS (`pem_file_path` / `key_file_path` / `cert_password`), and `--default_organization` / `--enterprise_key` are all honored either via the JSON file or as CLI overrides.

Add `--url` to target a different SonarQube Cloud instance (e.g. `--target_url https://sc-staging.io` for staging).

Full reference, flags, multi-server migration, and resume support:
👉 **[Using `migrate` — Migrate All Projects](docs/MIGRATE.md)**

### Want a guided experience?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

If you'd rather not pick phases yourself, run the interactive wizard — it asks you for the values it needs and runs the right commands for you:

```bash
./sonar-migration-tool wizard
```
---

## Step 4 — Verify in SonarQube Cloud
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Once the command finishes:

1. Log in to [sonarcloud.io](https://sonarcloud.io) or  [sonarqube.us](https://sonarqube.us).
2. Open the target organization.
3. Spot-check that your project(s) are listed and the quality gate and quality profile are correct.
4. Unless you passed `--skip_project_data_migration`, verify that issues, hotspots, and their creation dates match the source — and that non-main branches appear under **Branches** with their issues. You can also run `./sonar-migration-tool regtest` for automated verification.
4. Unless you passed `--skip_issue_sync`, verify that issues, hotspots marked as **false positive**, **accepted** and **safe** respectively, are in same status on SonarQube Cloud.
5. **Re-scan your projects in CI** to seed ongoing analysis. If you used `--skip_project_data_migration`, this first scan will be the baseline for all issue tracking.
6. Update your CI/CD pipeline to point at SonarQube Cloud (`SONAR_TOKEN`, `SONAR_HOST_URL`, and `sonar.organization`).

For the full post-migration checklist, see [After you migrate](docs/MIGRATE.md#after-you-migrate) in the MIGRATE guide.

---

### Migrating one project (or just a few)
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Use `transfer`. It runs the whole migration in a single command — extracting from SonarQube Server, mapping the configuration, importing source code and issues, and pushing everything to SonarQube Cloud — then writes a PDF summary you can hand to your team.

`transfer` shares the same `--config` file and the same direction-neutral CLI flags as `extract` / `migrate` / `reset` — `--source_*` for the SonarQube Server side, `--target_*` for the SonarQube Cloud side. Anything you don't pass on the CLI is read from the config file; CLI flags always win.

```bash
./sonar-migration-tool transfer \
  --source_url <YOUR_SQS_URL> \
  --source_token <YOUR_SQS_TOKEN> \
  --project_key <YOUR_PROJECT_KEY> \
  --target_url https://sonarcloud.io \
  --target_token <YOUR_SQC_TOKEN> \
  --enterprise_key <YOUR_SQC_ENTERPRISE_KEY>
  --default_organization <YOUR_SQC_ORG>
```

**Note**: SQS = SonarQube Server, SQC = SonarQube Cloud

Full reference, more examples, and the config-file format:
👉 **[Using `transfer` — Transfer One Project](docs/TRANSFER.md)**

**Note**: You may use https://sonarqube.us instead https://sonarcloud.io to migrate to the US instance of SonarQube Cloud

---

## Prefer a visual interface?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

`sonar-migration-tool` with a GUI. It is **experimental** in the current version of the tool.

If you'd rather click through the migration in a browser instead of typing commands, run the GUI:

```bash
./sonar-migration-tool gui
```
It opens the same workflow in your default browser with progress bars, an event log, and CSV viewers for the mapping files.


## Something went wrong?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Most errors fall into a few common buckets — see [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the full list.
You may want to rerun the command with the extra `--debug` flag to get more troubleshooting logs.

---

### Migrating many SonarQube Server instances into one SonarQube Cloud Enterprise
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

* Run `extract` for as many SonarQube Server instances as you have
* Run `structure` and `mappings` once
* Edit the `organizations.csv` to define your project mapping in the SonarQube Cloud organizations
* Run `migration` 

```bash
./sonar-migration-tool extract --url <YOUR_SQS_1_URL> --token <YOUR_SQS_1_TOKEN>
./sonar-migration-tool extract --url <YOUR_SQS_2_URL> --token <YOUR_SQS_n_TOKEN>
...
./sonar-migration-tool extract --url <YOUR_SQS_n_URL> --token <YOUR_SQS_n_TOKEN>

./sonar-migration-tool structure
./sonar-migration-tool mappings

# → edit organizations.csv to set sonarcloud_org_key per row (column 2)

./sonar-migration-tool migrate --enterprise_key <YOUR_SQC_ENTERPRISE_KEY> --url <SQC_URL> --token <SQC_TOKEN>
```
**Note**: SQS = SonarQube Server, SQC = SonarQube Cloud

---

## All commands
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

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

## Want to go deeper?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

- 📘 [Architecture overview](docs/ARCHITECTURE.md) — how the tool is built.
- ⚙️ [Configuration file format](docs/CONFIG.md) — use a JSON file instead of CLI flags.
- 🔐 [Security best practices](docs/SECURITY.md) — keeping your tokens safe.
- 🧪 [Regression testing protocol](docs/REGRESSION-TESTING.md) — verify changes against live SonarQube + SonarQube Cloud.
- 🐛 [CloudVoyager delta audit](docs/CLOUDVOYAGER-DELTA.md) — known behavior differences from the reference implementation.

---

## License
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

See [LICENSE](LICENSE) for details.
