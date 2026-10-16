# Deprecated commands: `structure` and `mappings`

> **Deprecated.** Since issue [#503](https://github.com/SonarSource/sonar-migration-tool/issues/503), `extract` runs `structure` and `mappings` itself, right after a successful extract. A migration now takes two commands: `extract`, then `migrate`. Both commands below still work, and they log a deprecation `WARN` when they start and when they end. Use [`extract`](MIGRATE.md#step-2--extract) instead.

You only need these commands to rebuild the CSVs from extracts already on disk, without extracting again. For example, after you deleted a CSV by mistake. They overwrite `organizations.csv` and `projects.csv` with no backup, so save any edits first. `extract` makes that backup for you: it copies each existing file to `<name>.csv.bak-<timestamp>` before it rewrites it.

## `structure`

Reads the extracted data and generates an `organizations.csv` file.

```bash
sonar-migration-tool structure --export_directory ./files/

# Or reuse the extract config (export_directory is read from it)
sonar-migration-tool structure --config extract-config.json
```

| Flag | Description |
|---|---|
| `--export_directory` | Root directory containing the extract output (default: `./migration-files`) |
| `--config` | Path to a JSON config file (same shape as `extract --config`). `export_directory` is read from it; when exactly one SonarCloud organization is defined, its key pre-populates `sonarcloud_org_key`. `--export_directory` on the CLI overrides the config value. |

## `mappings`

Generates the per-entity mapping CSVs (gates, profiles, groups, templates, portfolios).

```bash
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
