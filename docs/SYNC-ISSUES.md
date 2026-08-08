# Using `sync-issues` — Sync Issue & Hotspot Triage State

`sync-issues` is a **standalone**, **triage-state-only** command. It syncs the manual triage work done on SonarQube Server — issue status changes, resolutions, tags, severities, and comments, plus Security Hotspot review status and comments — onto the matching, already-migrated projects on SonarQube Cloud. It does not touch project configuration (quality gates, quality profiles, permissions, settings) and does not create projects: the target project must already exist on SonarQube Cloud.

Use it when you need to (re-)apply triage state independently of a full migration, without re-running `migrate` or `transfer`.

---

## When to use it

- After `migrate --skip_project_data_migration` — the migration created the project's configuration only; once a real SonarQube Cloud scan has populated the project with its own analysis (and its own issue/hotspot keys), run `sync-issues` to bring over the triage decisions made on SonarQube Server.
- As a **periodic re-sync** — triage work continues on SonarQube Server after a migration (new false-positives marked, new comments added, tags updated); `sync-issues` can be run again and again to keep SonarQube Cloud's triage state current, without repeating a full migration.
- When `migrate`/`transfer` already ran project-data import but you want to re-apply triage state on demand (for example, after catching up a backlog of manual reviews on the source).

If you need to migrate configuration or issue/hotspot **history** (not just triage state) for the first time, see [MIGRATE.md](MIGRATE.md) or [TRANSFER.md](TRANSFER.md) instead — both already run this same sync as their final step.

---

## What it does

`sync-issues` always runs its own lightweight extract from SonarQube Server — it does **not** require a prior `extract` run. The extract pulls only issues and Security Hotspots that carry a manual triage signal: a non-default status, a custom tag, a manually-set severity, or one or more comments. Findings with no triage signal are not extracted, since there is nothing to sync.

For each extracted finding, the command:

1. Resolves the SonarQube Cloud target project from the source project key (see [Target project resolution](#target-project-resolution) below).
2. Looks up the corresponding issue or hotspot on the target project using the [matching algorithm](#matching-algorithm) below.
3. When exactly one match qualifies, applies the source's status, resolution, tags, severity, and comments to the matched Cloud finding, and records the sync (see [Idempotency & traceability](#idempotency--traceability)).
4. When no match or several ambiguous matches are found, skips that finding — it is not modified, and no error is raised for it individually.

---

## Project scope

By default, `sync-issues` targets **every project visible on the source SonarQube Server token** — the sync fans out across the whole instance. Pass one or more `--project_key` flags to narrow it to specific projects:

```bash
sonar-migration-tool sync-issues \
  --project_key my-project \
  --project_key another-project \
  ...
```

Projects whose organization has no SonarQube Cloud mapping (see [Target project resolution](#target-project-resolution)) are silently excluded; they are not created.

---

## Target project resolution

`sync-issues` resolves each source project's target SonarQube Cloud key with the **same `project_key_pattern` convention** used by `migrate` and `transfer`:

- The target organization for a project comes from `structure`'s `organizations.csv` mapping (server → organization), falling back to `--default_organization` when no per-server mapping is set.
- The target project key is rendered from `--project_key_pattern` (default `<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>`), the same template used across the tool — see [Project key renaming strategy in ADVANCED-CONFIG.md](ADVANCED-CONFIG.md#project-key-renaming-strategy) for the full placeholder reference and validation rules.
- `sync-issues` runs its own `extract` + `structure` pass internally (producing `organizations.csv` / `projects.csv` in `--export_dir`), so you don't need to run either command yourself first. It does not run `mappings` — the profile/gate/group/template/portfolio mapping CSVs it produces aren't needed for a triage-state-only sync.

If a rendered target key does not exist on SonarQube Cloud, its findings are logged as lookup failures rather than synced (it was presumably never migrated, or was migrated under a different pattern — pass `--project_key_pattern` explicitly if your migration used a non-default one).

---

## Quick start

### With CLI flags

```bash
# From source
cd go && go run . sync-issues \
  --source_url https://sonarqube.example.com \
  --source_token sqp_xxx \
  --target_token squ_xxx \
  --default_organization my-org

# Built binary
sonar-migration-tool sync-issues \
  --source_url https://sonarqube.example.com \
  --source_token sqp_xxx \
  --target_token squ_xxx \
  --default_organization my-org
```

Omitting `--project_key` syncs every project visible to the source token.

### With a config file

```bash
# From source
cd go && go run . sync-issues -c config.json

# Built binary
sonar-migration-tool sync-issues -c config.json
```

`config.json` uses the **same unified shape** as `extract`, `migrate`, and `transfer` — one top-level block of shared defaults plus `source` and `target` sub-objects. See [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md) for the full reference.

```json
{
  "concurrency": 25,
  "timeout": 60,
  "export_directory": "./migration-files",
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
    "project_key_pattern": "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>"
  }
}
```

---

## Flags

| Flag | Config key | Description |
|------|------------|--------------|
| `-c, --config` | — | Path to a JSON configuration file (see [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md)) |
| `--source_url` | `source.url` | SonarQube Server URL |
| `--source_token` | `source.token` | SonarQube Server token |
| `--project_key` | `project_key` | Project key to sync. Repeatable. Omit to sync every project visible to the source token. |
| `--target_url` | `target.url` | SonarQube Cloud URL (default: `https://sonarcloud.io/`) |
| `--target_token` | `target.token` | SonarQube Cloud token |
| `--default_organization` | `target.default_organization` | SonarQube Cloud organization key, used as a fallback when `organizations.csv` has no mapping for a source project |
| `--project_key_pattern` | `target.project_key_pattern` | Template for target project keys, built from `<ORIGINAL_PROJECT_KEY>` and `<ORGANIZATION_KEY>` (default: `<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>`) |
| `--export_dir` | `export_directory` | Working directory for intermediate files (default: `./migration-files/`) |
| `--concurrency` | `concurrency` | Max concurrent HTTP requests (default: `25`) |
| `--timeout` | `timeout` | HTTP request timeout in seconds |
| `--pem_file_path` | `source.pem_file_path` | Client mTLS PEM file for the source server |
| `--key_file_path` | `source.key_file_path` | Client mTLS key file for the source server |
| `--cert_password` | `source.cert_password` | Password for the source server mTLS client certificate |
| `--debug` | — | Verbose troubleshooting logs |

CLI flags override values from the config file when both are provided.

> **No `--skip_issue_sync` / `--skip_project_data_migration` flags.** Unlike `migrate` and `transfer`, `sync-issues` has nothing else to skip — syncing triage state is its entire job. There is no configuration or project-data import to opt out of.

---

## Matching algorithm

Because the source code analyzed on SonarQube Server may differ slightly from the code analyzed on SonarQube Cloud (different scanner version, minor code drift, or line-shifting changes), `sync-issues` does not assume an exact line-number match between a source finding and its Cloud counterpart. Instead it uses a two-phase matcher, adapted from the same approach used by [sonar-tools' `findings.py`](https://github.com/okorach/sonar-tools/blob/593226eb1492c54d1bfa282eb7ed4ba5bb44cea3/sonar/findings.py#L421) (`strictly_identical_to` / `almost_identical_to`):

### Phase 1 — exact match

A candidate on the target project is an exact match when its **rule**, **file**, **line**, and **message** all match the source finding exactly.

- If **exactly one** exact match is found, the sync proceeds.
- If **several** exact matches are found, the finding is **skipped as ambiguous** — the tool cannot tell which target finding corresponds to the source one.

### Phase 2 — approximate match

When no exact match is found, the matcher falls back to a **scored approximate match** against every candidate on the same file (or, if the file itself doesn't match exactly, against the project's candidates more broadly). Each candidate earns points, up to a maximum of **7**, across:

| Signal | Points |
|---|---|
| Message — exact match | full credit |
| Message — fuzzy match (edit-distance similarity) below the exact threshold | partial credit |
| File | credit if the file matches |
| Line | credit if the line matches (or is within a small offset) |
| Type / rule offset | credit if the rule or finding type matches |
| Severity | credit if the severity matches |
| Author | credit if the reporting author matches |

A candidate must score **at least 6 out of 7** to qualify as an approximate match.

- If **exactly one** candidate qualifies, the sync proceeds against that candidate.
- If **zero** or **several** candidates qualify, the finding is **skipped** — no match is confident enough (or more than one is equally plausible) to sync safely.

This means `sync-issues` never guesses when there's ambiguity: a skipped finding is left untouched on SonarQube Cloud, and can be revisited manually or on a later run once the ambiguity is resolved (e.g. after a newer scan narrows the candidate set).

---

## Idempotency & traceability

Every finding that syncs successfully is marked the same way `migrate` and `transfer` already mark project-data-imported findings — this behavior is not new, `sync-issues` just makes it reachable without a full migration:

- The synced Cloud issue or hotspot gets a **`metadata-synchronized`** tag, so re-running `sync-issues` (or a later `migrate`/`transfer`) can recognize it was already synced.
- Comments copied from the source are prefixed **`[Migrated from …]`** so their origin is clear alongside any comments added natively on SonarQube Cloud.
- A one-line **`Link to [Original issue]`** (for issues) or **`Link to [Original hotspot]`** (for hotspots) comment is added on the Cloud finding, linking back to the corresponding finding on SonarQube Server.

Because of this, `sync-issues` is safe to run repeatedly — on a schedule, after every SonarQube Server triage session, or on demand — without duplicating comments or re-tagging findings that are already in sync.

---

## Output

- **Intermediate files** — written to `--export_dir` (default `./migration-files/`): the extract data plus `organizations.csv` / `projects.csv` from the internal `structure` pass.
- **Stdout** — a final summary of projects synced and issue/hotspot counts (actionable, synced, skipped), followed by `Sync complete.`.
- Skipped projects (no resolvable target) and skipped findings (ambiguous or no match) are logged so you can review them; pass `--debug` for verbose per-finding detail.

---

## Troubleshooting

- **Project skipped, no target found** — confirm the project was actually migrated, and that `--project_key_pattern` / `--default_organization` match what was used at migration time. See [Project key renaming strategy](ADVANCED-CONFIG.md#project-key-renaming-strategy).
- **Finding skipped as ambiguous** — several candidates scored high enough on the same file; this is expected when a file contains multiple very similar findings (e.g. duplicated code blocks). Re-run after a fresh Cloud scan reduces line drift, or resolve the ambiguity manually on SonarQube Cloud.
- **Token errors** — see the [Token permissions](MIGRATE.md#token-permissions) section in MIGRATE.md.
- **Anything else** — [TROUBLESHOOTING.md](TROUBLESHOOTING.md) has the full list of common errors.
