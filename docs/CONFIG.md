# Configuration File Reference

This document is the **format reference** for the JSON configuration files that the `extract`, `migrate`, `reset`, `predictive-report`, `structure`, and `mappings` commands accept via `--config`.

For the **how to use a config file in your migration** (per-command examples, step-by-step workflow, multi-server migration), see the relevant guide:

- [Using `migrate` — Migrate All Projects](MIGRATE.md)
- [Using `transfer` — Transfer One Project](TRANSFER.md)

---

## Why use a config file

Configuration files are useful when you want to:

- **Keep tokens out of your shell history** — store them in a file with restricted permissions.
- **Run the same migration repeatedly** — share the same config with your team or CI.
- **Review all settings in one place** — easier to audit than a long CLI invocation.

For security best-practices (`.gitignore`, file permissions, env-var substitution), see [SECURITY.md](SECURITY.md).

---

## Quick start

1. Copy an example config:
   ```bash
   cp examples/config.unified.example.json my-config.json
   ```
2. Edit the values (URL, tokens, organization key, etc.).
3. Pass it to a command:
   ```bash
   ./sonar-migration-tool extract --config my-config.json
   ```

---

## Unified config shape

`extract`, `migrate`, `reset`, and `predictive-report` all read the same JSON file. The recommended shape has one top-level block of defaults and one `source` / `target` sub-block per side of the migration. Each command silently ignores the block that isn't its own.

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

### Top-level fields (all optional)

| Field | Default | Description |
|---|---|---|
| `concurrency` | `10` | Default max parallel HTTP calls |
| `timeout` | `60` | Default HTTP request timeout in seconds |
| `export_directory` | `./migration-files` | Root directory for extract / migrate output |

The same `concurrency` and `timeout` fields exist inside `source` and `target` — when set there they override the top-level value for that command only.

### `source` block (consumed by `extract`)

| Field | Required | Description |
|---|---|---|
| `url` | ✅ | SonarQube Server base URL |
| `token` | ✅ | SonarQube Server user token |
| `extract_type` | | `"all"` (default) or the name of a specific extract task |
| `concurrency` / `timeout` | | Override the top-level defaults |
| `pem_file_path` / `key_file_path` / `cert_password` | | Client-side mTLS certificate, if required |
| `target_task` | | Stop extract at a specific task (dependencies still run) |
| `extract_id` | | Reuse an existing extract directory ID instead of generating a new one |
| `enterprise_key` / `organization_key` / `edition` | | Provisional — accepted but ignored today; reserved for future SQC-to-SQC migration |

### `target` block (consumed by `migrate` and `reset`)

| Field | Required | Description |
|---|---|---|
| `url` | ✅ | SonarQube Cloud base URL (e.g. `https://sonarcloud.io/`) |
| `token` | ✅ | SonarQube Cloud user token |
| `enterprise_key` | | Enterprise key used to scope enterprise endpoints |
| `edition` | | `"enterprise"` (default), `"team"`, or `"foss"` |
| `concurrency` / `timeout` | | Override the top-level defaults |
| `run_id` | | Resume an in-progress migrate run by ID |
| `target_task` | | Stop migrate at a specific task |
| `organization_key` | | Provisional — accepted but ignored today |

---

## Legacy config shapes

Three older config shapes are still parsed for backward compatibility — existing configs keep working:

- **Flat top-level keys** — e.g. `{ "url": "...", "token": "...", "export_directory": "..." }` (the original `extract-config.json` shape).
- **`extract` / `migrate` sub-objects** — `{ "extract": { ... }, "migrate": { ... } }`.
- **`sonarqube` + `sonarcloud` side-sectioned** — used by `transfer`: `{ "sonarqube": { ... }, "sonarcloud": { ... } }`. See [TRANSFER.md](TRANSFER.md) for an example.

When in doubt, copy the unified example (`examples/config.unified.example.json`) — it parses everywhere.

---

## Schema for editor autocomplete

A JSON Schema lives at [`schemas/config.schema.json`](../schemas/config.schema.json). Reference it in `.vscode/settings.json` or by adding a `"$schema"` pointer at the top of your config to get inline validation and autocomplete in your editor.

---

## Combining config files and CLI arguments

Command-line arguments **override** config-file values. This lets you:

- Keep common settings in a config file.
- Override specific values on the command line for a one-off run.

```bash
# Config has concurrency: 10. CLI forces 5.
./sonar-migration-tool extract --config my-config.json --concurrency 5
```

---

## Example config files

The repository includes these starter files in `examples/`:

- `config.unified.example.json` — recommended shape, used by `extract` / `migrate` / `reset` / `predictive-report`
- `config.example.json` — full reference with every documented field
- `config-extract.example.json` — minimal extract-only config
- `config-migrate.example.json` — minimal migrate-only config
- `migration-config.example.json` — `transfer`-style `sonarqube` + `sonarcloud` block

Copy and customize the one that matches your command.

---

## Troubleshooting

- **"Error loading config file"** — check the file path and JSON syntax (no trailing commas, proper quotes).
- **"Error: URL and TOKEN are required"** — make sure these fields are in the config file or passed as CLI arguments, and check for typos in field names.
- **Values not being applied** — remember that CLI arguments override config values. Confirm you're pointing at the right config file.

For broader issues, see [TROUBLESHOOTING.md](TROUBLESHOOTING.md).
