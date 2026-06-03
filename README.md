# Sonar Migration Tool

Move your SonarQube Server configuration over to SonarQube Cloud — projects, quality gates, quality profiles, groups, permissions, and portfolios.

The tool ships as a single static binary. No installer, no runtime dependencies. Download it, run one command, and your configuration lands in SonarQube Cloud.

---

## What gets migrated

| ✅ Migrated | ❌ NOT migrated |
|---|---|
| Projects, Quality Gates, Quality Profiles | Issues and their history |
| Groups, Permissions, Permission Templates | Source code (you re-scan after migration) |
| Portfolios | |
| Scan History (optional — pass `--include-scan-history`) | |

---

## Before you start

Make sure you have:

- A computer running **macOS, Linux, or Windows**.
- **Admin access** to your SonarQube Server.
- A **SonarQube Cloud** account with the target organizations already created.
- **Two admin tokens** — one for SonarQube Server, one for SonarQube Cloud. The exact permissions are listed in [the MIGRATE guide](docs/MIGRATE.md#token-permissions).

That's it. No Go install, no databases, no config files required for the simple path.

---

## Step 1 — Download the tool

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

You'll type one command and the tool does the rest. Open the right app for your OS:

- **macOS** — open **Terminal** (find it in Applications → Utilities, or press `⌘ Space` and type "Terminal").
- **Linux** — open your distro's terminal application.
- **Windows** — open **PowerShell** (press the Windows key and type "PowerShell").

---

## Step 3 — Pick your migration path

The tool ships with two ways to run. They do the same job — pick the one that matches what you're doing.

### I just want to migrate one project

Use the `transfer` command. It runs the whole migration as a single command.

👉 **[Using `transfer` — Transfer One Project](docs/TRANSFER.md)**

### I'm migrating many projects (or want more control)

Use the `migrate` command. It runs the migration in clear, separate steps so you can review the output of each one before moving on.

👉 **[Using `migrate` — Migrate All Projects](docs/MIGRATE.md)**

If you're not sure which one fits, start with `transfer` — you can always re-run with `migrate` if you need to.

---

## Step 4 — Verify in SonarQube Cloud

Once the command finishes:

1. Log in to [sonarcloud.io](https://sonarcloud.io).
2. Open the target organization.
3. Spot-check that your project(s) are listed and the quality gate and quality profile are correct.
4. **Re-scan your projects in CI** to seed fresh analysis data. (Unless you used `--include-scan-history`, historical issues don't transfer.)
5. Update your CI/CD pipeline to point at SonarQube Cloud (`SONAR_TOKEN` and `SONAR_HOST_URL`).

For the full post-migration checklist, see [After you migrate](docs/MIGRATE.md#after-you-migrate) in the MIGRATE guide.

---

## Prefer a visual interface?

If you'd rather click through the migration in a browser instead of typing commands, run the GUI:

```bash
./sonar-migration-tool gui
```

It opens the same workflow in your default browser with progress bars, an event log, and CSV viewers for the mapping files.

---

## Something went wrong?

Most errors fall into a few common buckets — see [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) for the full list.

The single best first step is to look at the request log:

```
files/<run_id>/requests.log
```

It shows every API call the tool made and how the server responded.

---

## Want to go deeper?

- 📘 [Architecture overview](docs/ARCHITECTURE.md) — how the tool is built.
- ⚙️ [Configuration file format](docs/CONFIG.md) — use a JSON file instead of CLI flags.
- 🔐 [Security best practices](docs/SECURITY.md) — keeping your tokens safe.
- 🧪 [Regression testing protocol](docs/REGRESSION-TESTING.md) — verify changes against live SonarQube + SonarQube Cloud.
- 🐛 [CloudVoyager delta audit](docs/CLOUDVOYAGER-DELTA.md) — known behavior differences from the reference implementation.

---

## License

See [LICENSE](LICENSE) for details.
