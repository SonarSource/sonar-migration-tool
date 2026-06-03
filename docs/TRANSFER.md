# Using `transfer` — Transfer One Project

`transfer` is the **single-command** path. It chains the four phases of a migration — **extract → structure → mappings → migrate** — into one call, then writes a PDF summary on completion. Use it when you have one project (or a small, well-known set of projects) to move across.

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

Behind the scenes, `transfer` runs the same four phases as the manual workflow, in order:

1. **Extract** — connects to SonarQube Server and pulls the project's configuration.
2. **Structure** — assembles the extracted data into the project + org structure.
3. **Mappings** — generates the per-entity mapping CSVs (gates, profiles, groups, templates, portfolios).
4. **Migrate** — applies everything to SonarQube Cloud.

On success, a PDF migration summary is written into the export directory.

---

## Quick start

### With a config file

```bash
# From source
cd go && go run . transfer -c config.json

# Built binary
sonar-migration-tool transfer -c config.json
```

`config.json` shape (see [CONFIG.md](CONFIG.md) for the full reference):

```json
{
  "sonarqube": {
    "url": "https://sonarqube.example.com",
    "token": "sqp_xxx",
    "projectKey": "my-project"
  },
  "sonarcloud": {
    "token": "squ_xxx",
    "organization": "my-org"
  }
}
```

### With CLI flags

```bash
# From source
cd go && go run . transfer \
  --sq-url https://sonarqube.example.com \
  --sq-token sqp_xxx \
  --project-key my-project \
  --sc-token squ_xxx \
  --sc-org my-org

# Built binary
sonar-migration-tool transfer \
  --sq-url https://sonarqube.example.com \
  --sq-token sqp_xxx \
  --project-key my-project \
  --sc-token squ_xxx \
  --sc-org my-org
```

Omit `--project-key` to transfer **every** project visible to the token (in which case the rest of the manual workflow applies — see [MIGRATE.md](MIGRATE.md) for the per-project `organizations.csv` mapping step).

---

## Flags

| Flag | Description |
|------|-------------|
| `-c, --config` | Path to a JSON configuration file (see [CONFIG.md](CONFIG.md)) |
| `--sq-url` | SonarQube Server URL |
| `--sq-token` | SonarQube Server token |
| `--project-key` | Project key to transfer. Omit to transfer every project visible to the token. |
| `--sc-token` | SonarQube Cloud token |
| `--sc-org` | SonarQube Cloud organization key |
| `--sc-enterprise-key` | SonarQube Cloud enterprise key (defaults to `--sc-org`) |
| `--export-dir` | Working directory for intermediate files (default: `./migration-files/`) |
| `--include-scan-history` | Extract and import full issue/hotspot scan history. Significantly slower and larger. |

CLI flags override values from the config file when both are provided.

---

## Output

- **Intermediate files** — written to `--export-dir` (default `./migration-files/`). Same files as the manual workflow: `organizations.csv`, `gates.csv`, `profiles.csv`, `groups.csv`, `templates.csv`, `portfolios.csv`.
- **PDF summary** — written to the export directory on successful completion.
- **Stdout** — every command prints `See sonar-migration-tool output results in <directory>` when it finishes so you always know where to look.

For a full description of every output file, see the [Output Files Reference](MIGRATE.md#output-files-reference) in MIGRATE.md.

---

## After the transfer

1. Log in to SonarQube Cloud and confirm the project appears under the target organization.
2. Spot-check that the quality gate and quality profile are present.
3. If you did **not** pass `--include-scan-history`, re-scan the project in CI to seed historical data.
4. Update your CI/CD pipeline to point at SonarQube Cloud (`SONAR_TOKEN`, `SONAR_HOST_URL`).

For more on post-migration steps, see the [After you migrate](MIGRATE.md#after-you-migrate) section in MIGRATE.md.

---

## Troubleshooting

- **Token errors** — see the [Token permissions](MIGRATE.md#token-permissions) section in MIGRATE.md.
- **Org not found** — confirm `--sc-org` matches an existing organization in your SonarQube Cloud enterprise.
- **Anything else** — [TROUBLESHOOTING.md](TROUBLESHOOTING.md) has the full list of common errors.
