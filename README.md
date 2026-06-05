# Sonar Migration Tool
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Migrate your SonarQube Server to SonarQube Cloud — projects, configuration, source code, issues, and history.

The tool ships as a single static binary. No installer, no runtime dependencies. Download it, run one command, and your projects land in SonarQube Cloud with their full issue history intact.

---

## What gets migrated
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

| ✅ Migrated | ❌ NOT migrated |
|---|---|
| Projects, Quality Gates, Quality Profiles | User accounts (Cloud users are managed by the IdP) |
| Groups, Permissions, Permission Templates | CI/CD pipeline configuration (update `SONAR_HOST_URL` manually) |
| Project Settings, Webhooks, Links | |
| Portfolios (Enterprise) | |
| **Issues & Hotspots** with status, comments, and tags (project data, on by default) | |
| **Source Code** and measures (project data, on by default) | |
| **Issue Creation Dates** preserved via BackdateChangesets (project data, on by default) | |

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

| OS | File |
|---|---|
| macOS (Apple Silicon) | `sonar-migration-tool_darwin_arm64.tar.gz` |
| macOS (Intel) | `sonar-migration-tool_darwin_amd64.tar.gz` |
| Linux | `sonar-migration-tool_linux_amd64.tar.gz` |
| Windows | `sonar-migration-tool_windows_amd64.zip` |

Extract the archive. On macOS and Linux, make the binary executable:

```bash
chmod +x sonar-migration-tool
```

You can now run it from the same folder:

```bash
./sonar-migration-tool --help
```

---

## Step 2 — Open a terminal
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

You'll type one command and the tool does the rest. Open the right app for your OS:

- **macOS** — open **Terminal** (find it in Applications → Utilities, or press `⌘ Space` and type "Terminal").
- **Linux** — open your distro's terminal application.
- **Windows** — open **PowerShell** (press the Windows key and type "PowerShell").

---

## Step 3 — Run the migration
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

The tool ships with several commands. Pick the workflow that matches your situation.

### Migrating one project (or just a few)
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Use `transfer`. It runs the whole migration in a single command — extracting from SonarQube Server, mapping the configuration, importing source code and issues, and pushing everything to SonarQube Cloud — then writes a PDF summary you can hand to your team.

`transfer` shares the same `--config` file and the same direction-neutral CLI flags as `extract` / `migrate` / `reset` — `--source_*` for the SonarQube Server side, `--target_*` for the SonarQube Cloud side. Anything you don't pass on the CLI is read from the config file; CLI flags always win.

```bash
./sonar-migration-tool transfer \
  --source_url https://sonarqube.example.com \
  --source_token sqp_xxx \
  --project_key my-project \
  --target_token squ_xxx \
  --default_organization my-org
```

Or use a **config file** to keep tokens out of your shell history:

```bash
cp examples/config-transfer.example.json my-config.json
# Edit my-config.json with your SonarQube Server and SonarQube Cloud credentials
./sonar-migration-tool transfer -c my-config.json --project_key my-project
```

The config file uses the same unified shape as every other command — one top-level block of shared defaults plus `source` and `target` sub-objects. `concurrency`, `timeout`, `export_directory`, mTLS (`pem_file_path` / `key_file_path` / `cert_password`), and `--default_organization` / `--enterprise_key` are all honored either via the JSON file or as CLI overrides.

Add `--target_url` to target a different SonarQube Cloud instance (e.g. `--target_url https://sc-staging.io` for staging).

Full reference, more examples, and the config-file format:
👉 **[Using `transfer` — Transfer One Project](docs/TRANSFER.md)**

### Migrating many projects (or many SonarQube Server instances)
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Use `migrate` together with the underlying `extract` / `structure` / `mappings` commands. This gives you a chance to review and edit the mapping CSVs between phases — useful when projects need to land in different SonarQube Cloud organizations, or when you want to re-run individual steps after a failure.

```bash
./sonar-migration-tool extract <SQ_URL> <SQ_TOKEN>
# → edit organizations.csv to set sonarcloud_org_key per row
./sonar-migration-tool structure
./sonar-migration-tool mappings
./sonar-migration-tool migrate <SC_TOKEN> <SC_ENTERPRISE_KEY>
```

Or use a **config file** for extract and migrate:

```bash
cp examples/config.unified.example.json my-config.json
# Edit my-config.json with your source (SonarQube Server) and target (SonarQube Cloud) credentials
./sonar-migration-tool extract --config my-config.json
./sonar-migration-tool structure --config my-config.json
./sonar-migration-tool mappings --config my-config.json
./sonar-migration-tool migrate --config my-config.json
```

Full reference, flags, multi-server migration, and resume support:
👉 **[Using `migrate` — Migrate All Projects](docs/MIGRATE.md)**

### Want a guided experience?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

If you'd rather not pick phases yourself, run the interactive wizard — it asks you for the values it needs and runs the right commands for you:

```bash
./sonar-migration-tool wizard
```

If you're not sure which path fits, start with `transfer`. You can always re-run with `migrate` for more control.

---

## Step 4 — Verify in SonarQube Cloud
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Once the command finishes:

1. Log in to [sonarcloud.io](https://sonarcloud.io).
2. Open the target organization.
3. Spot-check that your project(s) are listed and the quality gate and quality profile are correct.
4. Unless you passed `--skip_project_data_migration`, verify that issues, hotspots, and their creation dates match the source. You can also run `./sonar-migration-tool regtest` for automated verification.
5. **Re-scan your projects in CI** to seed ongoing analysis. If you used `--skip_project_data_migration`, this first scan will be the baseline for all issue tracking.
6. Update your CI/CD pipeline to point at SonarQube Cloud (`SONAR_TOKEN` and `SONAR_HOST_URL`).

For the full post-migration checklist, see [After you migrate](docs/MIGRATE.md#after-you-migrate) in the MIGRATE guide.

---

## Prefer a visual interface?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

If you'd rather click through the migration in a browser instead of typing commands, run the GUI:

```bash
./sonar-migration-tool gui
```

It opens the same workflow in your default browser with progress bars, an event log, and CSV viewers for the mapping files.

---

## Something went wrong?
<!-- updated: 2026-06-04_01:13:00.000 by Claude -->

Most errors fall into a few common buckets — see [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the full list.

The single best first step is to look at the request log:

```
files/<run_id>/requests.log
```

It shows every API call the tool made and how the server responded.

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
