# Using `transfer` — Transfer One Project
<!-- updated: 2026-07-27_23:05:00 -->

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
<!-- updated: 2026-06-05_14:00:00 -->

Behind the scenes, `transfer` runs the same four phases as the manual workflow, in order:

1. **Extract** — connects to SonarQube Server and pulls the project's configuration **and its full issue/hotspot project data** (project data is always included for `transfer`).
2. **Structure** — assembles the extracted data into the project + org structure.
3. **Mappings** — generates the per-entity mapping CSVs (gates, profiles, groups, templates, portfolios).
4. **Migrate** — applies the **project-scoped** subset to SonarQube Cloud: it runs only the tasks needed for the project, its quality gate and profiles, its permissions, and its issue/hotspot history. Their dependencies are resolved automatically; global/instance-wide tasks are skipped.

On completion, a migration summary is written into the export directory as both `migration_summary.pdf` and `migration_summary.md`, alongside the run instrumentation files (`run_meta.json`, `run_events.jsonl`). These are written even when the run fails, so the summary can explain the failure.

---

## What gets migrated
<!-- updated: 2026-07-27_23:55:00 -->

`transfer` migrates a **project-scoped** slice, not the whole instance.

**Included:**

- The specified **project** (created in the target organization).
- The **quality gate** the project uses, with its conditions.
- The **quality profiles** the project uses, with their rules restored (and any parent relationships).
- The project's **permissions** (group permissions), **settings**, **tags**, **links**, **webhooks**, and **new code period**.
- The project's complete **issue history** — both native SonarQube issues and **externally imported issues** (from third-party analyzers) — replayed via project-data import, with triage state (status, resolution, assignee, comments, tags) synced afterward.
- The project's **Security Hotspots**, with their review status and comments synced.
- The project's **DevOps platform (ALM) binding** — the project is bound on SonarQube Cloud to the repository it was bound to on SonarQube Server (issue #122). Only the **project-level** binding is replicated: the organization's own DevOps platform binding needs secrets that cannot be migrated and is read-only input here. The binding is attempted only when the source project is bound **and** the target organization is itself bound to the same platform; otherwise the project is reported as a **partial migration** explaining why. See [What gets migrated → DevOps platform bindings](#devops-platform-alm-bindings).

**Not modified** (use the full [`migrate`](MIGRATE.md) command for these):

- Portfolios.
- Global settings, global webhooks, and the global new code period.
- Permission templates and default-template assignment.
- Organization-level and profile-level group permissions.
- Default quality gate / default quality profile selection.
- Rule tag and rule description updates.

> **Note on prerequisites.** A few global entities are created on the target only because the project depends on them — for example, the groups referenced by the project's group permissions, and the migration user/permissions used to perform the migration. These are created as needed so the project's own configuration resolves correctly.

> **Note on issue counts.** The target issue count is normally lower than the SonarQube Server total because issues that are **CLOSED** or resolved as **FIXED** have no SonarQube Cloud counterpart and are intentionally skipped (the scanner report only recreates active findings). Open issues plus triaged ones (won't-fix / false-positive / accepted) and all externally-imported issues are migrated. Security Hotspots transfer in full, but they arrive as **issues** (see above) — so they are *counted inside* the target issue total, and SonarQube Cloud's Security Hotspots view will be empty by design. Comparing a source hotspot count against a target hotspot count therefore always reads as total loss even when every hotspot migrated correctly; filter the target project by the `sqs-hotspot` tag instead.

> **Non-main branches.** Project-data import now migrates the project's **non-main branches too** — each is created on SonarQube Cloud as a **long-lived branch with its full issue history**. Before submitting a non-main branch's report, the tool performs SonarQube Cloud's **"Create analysis" handshake** (`POST {api-host}/analysis/analyses`) to register the branch and obtain an analysis id, which it embeds in the report so the Compute Engine binds the issues to the branch. All migrated branches are registered as **long-lived** so SonarQube Cloud's automatic pruning of short-lived branches (after ~30 days) never discards migrated history. A non-main branch is **skipped** only when the source server no longer has its source code (e.g. purged by housekeeping for an inactive branch) — re-analyze that branch on the source first to restore it.

### DevOps platform (ALM) bindings
<!-- updated: 2026-08-11_10:20:00 -->

`transfer` replicates the project's DevOps platform binding so the migrated project is linked to the
same repository on SonarQube Cloud. The identifier carried over per platform is:

| Platform | Identifier migrated |
| --- | --- |
| GitHub | Repository name (`owner/repo`) |
| GitLab | Project id |
| Azure DevOps | Project name + repository name |
| Bitbucket Cloud | Repository slug |

**Preconditions.** Two must both hold, and both are checked before any write:

1. The **source project is bound** on SonarQube Server (`GET /api/alm_settings/get_binding`). An unbound
   project is simply left unbound on the target — nothing is reported.
2. The **target organization is bound** to the same DevOps platform
   (`GET /api/alm_integration/show_bound_organization`). SonarQube Cloud can only bind a project to a
   repository of the DevOps organization its own organization is bound to.

When the source project was bound but the target organization is **not** bound to that platform, the
project's migration outcome becomes **Partial Migration** and the report's Details column reads
*"project binding was not possible because the org itself is not bound"*. The same happens, with a
different sentence, when the organization is bound but the repository does not exist in the bound
DevOps organization.

**Both preconditions are best-effort and never fail the migration** (issue #505). Reading them only
enables this optional extra, so any failure degrades to "no binding" and the run continues:

| What the target answers | Recorded as | Report Details |
| --- | --- | --- |
| `show_bound_organization` → HTTP 500 (SonarQube Cloud's **normal** answer for an org with no DevOps binding), 404 (no such org), 400/403 (token cannot administer it) | unbound | *"...because the org itself is not bound"* |
| `show_bound_organization` → any other failure (transport error, 502/503, ...) | binding **unknown** | *"...because the target organization's DevOps platform binding could not be read"* + the API error |
| `list_repositories` → HTTP 400 *"This organization is not bound to an ALM application"* / 403 / 404 | no repositories | the unbound-org sentence above (reported from the org binding) |
| `list_repositories` → any other failure | repositories **unknown** | *"...because the repositories of the bound DevOps organization could not be listed"* + the API error |

Only a cancelled or timed-out run still aborts these tasks. The distinction between "unbound" and
"unknown" is deliberate: before #505 an unbound org's HTTP 500 aborted the entire `migrate` run with
`phase 2: task getOrgBinding: ...`, and reporting an unread binding as "not bound" would state
something the tool never observed.

**On-premise DevOps platforms are never migrated.** SonarQube Cloud integrates only with the
**cloud** platforms — GitHub.com, GitLab.com, Azure DevOps Services and Bitbucket Cloud — so a
source project bound to GitHub Enterprise Server, self-managed GitLab or Bitbucket Server/Data
Center has no target equivalent. Cloud vs on-premise is decided from the source ALM setting's `url`
(its API endpoint: `api.github.com`, `gitlab.com`, `dev.azure.com`, `visualstudio.com` for Azure
DevOps Services accounts predating the rename, `bitbucket.org`). Such a project is reported as
**Partial Migration** with *"project binding was not possible because the source project is bound to
an on-premise DevOps platform, which SonarQube Cloud cannot integrate with"* — before #505 the
binding was dropped silently and the project was reported as fully migrated.

A project that is **not bound at all** on the source is still left unbound on the target with
nothing reported, which is the #122 behaviour.

---

## Quick start

### With a config file

```bash
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
sonar-migration-tool transfer \
  --source_url https://sonarqube.example.com \
  --source_token sqp_xxx \
  --project_key my-project \
  --target_token squ_xxx \
  --default_organization my-org
```

`--project_key` is always compiled as a full-match regex, implicitly anchored
with `^` and `$` (issue #529). A plain key like `my-project` matches only
itself, so single-project usage is unaffected. A pattern transfers every
source project whose key **fully** matches it in one run:

```bash
# Transfers every project whose key starts with "BANKING_" — not a key
# that merely contains "BANKING_" somewhere in the middle.
sonar-migration-tool transfer \
  --source_url https://sonarqube.example.com \
  --source_token sqp_xxx \
  --project_key "BANKING_.+" \
  --target_token squ_xxx \
  --default_organization my-org
```

Omit `--project_key` to transfer **every** project visible to the token (in which case the rest of the manual workflow applies — see [MIGRATE.md](MIGRATE.md) for the per-project `organizations.csv` mapping step).

---

## Flags
<!-- updated: 2026-09-02_12:28:13 -->

| Flag | Config key | Description |
|------|------------|-------------|
| `-c, --config` | — | Path to a JSON configuration file (see [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md)) |
| `--source_url` | `source.url` | SonarQube Server URL |
| `--source_token` | `source.token` | SonarQube Server token |
| `--project_key` | `project_key` | Project key (or regexp) to transfer. Always compiled as a full-match regex, implicitly anchored with `^` and `$` — a plain key matches only itself; a pattern like `BANKING_.+` transfers every project whose key starts with `BANKING_`. Omit to transfer every project visible to the token. |
| `--target_url` | `target.url` | SonarQube Cloud URL (default: `https://sonarcloud.io/`) |
| `--target_token` | `target.token` | SonarQube Cloud token |
| `--default_organization` | `target.default_organization` | SonarQube Cloud organization key |
| `--enterprise_key` | `target.enterprise_key` | SonarQube Cloud enterprise key (defaults to `--default_organization`) |
| `--export_dir` | `export_directory` | Working directory for intermediate files (default: `./migration-files/`) |
| `--concurrency` | `concurrency` | Max concurrent HTTP requests (default: `25`) |
| `--timeout` | `timeout` | HTTP request timeout in seconds |
| `--pem_file_path` | `source.pem_file_path` | Client mTLS PEM file for the source server |
| `--key_file_path` | `source.key_file_path` | Client mTLS key file for the source server |
| `--cert_password` | `source.cert_password` | Password for the source server mTLS client certificate |
| `--skip_project_data_migration` | top-level `skip_project_data_migration` | Skip the project-data migration (importProjectData + per-issue / per-hotspot sync). Defaults to off — project data is migrated by default. Issue #303. |
| `--exclude_branches` | `target.exclude_branches` | Glob patterns for non-main branches to skip during project data import. Repeatable. Main branch is never excluded. |
| `--unsupported_languages` | top-level or `target.unsupported_languages` | How to handle files whose language has no quality profile on the target — typically a language from a 3rd-party SonarQube Server plugin. `exclude` (default) drops those files from the analysis report so the rest of the project still migrates; `skip` does not migrate the project's issues/branches at all; `warn` submits the report unchanged. Issue #474. |
| `--migrate_history` | top-level `migrate_history` | **PoC.** Also migrate a bounded set of historical analysis snapshots (date + project-level measures only) per project's main branch, backdated on SonarQube Cloud. Defaults to off — no change to existing behavior unless set. Issue #554. |
| `--history_max_points` | top-level `history_max_points` | Max historical snapshots migrated per project when `--migrate_history` is set (default: `10`). |
| `--history_min_interval_days` | top-level `history_min_interval_days` | Minimum spacing, in days, enforced between two migrated historical snapshots when `--migrate_history` is set (default: `30`). |

CLI flags override values from the config file when both are provided.

### Unsupported programming languages (`--unsupported_languages`)
<!-- updated: 2026-07-27_23:05:00 -->

SonarQube Server can analyze languages SonarQube Cloud cannot. A language
contributed by a 3rd-party (non-SonarSource) plugin has no analyzer on the
Cloud side, and therefore no quality profile.

The analysis report `transfer` fabricates stamps every file with the language
the source server reported for it, while the report's metadata can only name
quality profiles that exist on the target. When a file's language has no target
profile, the SonarQube Cloud Compute Engine rejects the **entire** report:

```
Report contains a file with language 'lua' but no matching quality profile
```

The project, its permissions and its quality gate are created before the report
is submitted, so the result is a project that looks migrated but has **no issues
and no branches**.

`transfer` detects this before submitting and prints the affected languages, the
file count and an example path. Choose the handling with
`--unsupported_languages`:

| Mode | Behaviour |
|------|-----------|
| `exclude` (default) | Drops the unsupported-language files from the report. Everything else — the other files, their issues, measures and all branches — migrates. The project is reported as a **Partial Migration** with the languages and file count listed. |
| `skip` | Submits no report for the project. Its settings, permissions and quality gate still migrate; its issues and branches do not. Reported as skipped, with the reason. |
| `warn` | Submits the report unchanged (pre-#474 behaviour). The Compute Engine is expected to reject it; the rejection is reported as such rather than as a generic API error. |

```bash
# Migrate everything except the unsupported-language files (default)
sonar-migration-tool transfer -c config.json --project_key my-project

# Do not transfer this project's issues/branches at all
sonar-migration-tool transfer -c config.json --project_key my-project \
  --unsupported_languages skip
```

A failure to read the target organization's quality profiles disables the
detection entirely rather than treating every language as unsupported, so a
transient API error can never drop a project's files.

### Project history migration (`--migrate_history`) — PoC
<!-- updated: 2026-09-02_22:00:00 -->

**This is a proof-of-concept.** By default, `transfer` (and `migrate`) submit a
single scanner report per branch, dated "now" — the target's analysis history
starts the day it was migrated, even if the source project has years of prior
analyses. Issue #554 asks for a way to carry some of that history over.

`--migrate_history` opts into replaying a bounded set of the source project's
**main branch** historical analyses as separate, backdated entries on the
target, submitted before the regular current-snapshot import so each lands as
its own point in SonarQube Cloud's analysis history (`/api/project_analyses/search`),
not just a re-dated copy of the latest one.

Each historical entry carries only the project's own measures (`ncloc`,
`bugs`, `vulnerabilities`, `code_smells`, `coverage`,
`duplicated_lines_density`, `complexity`, `cognitive_complexity`,
`security_hotspots`, `comment_lines`, `classes`, `functions`) as recorded by
the source server at that analysis — no files, no issues. Per the issue's own
design, files/issues are only meaningful for the branch's *last* analysis
(SonarQube only keeps issues attached to the most recent analysis of a
branch), which is exactly what the existing, unchanged current-snapshot import
already migrates in full.

To bound how much history is walked, two flags cap the source's full analysis
list, oldest to newest, always dropping the single most recent analysis
(already covered by the current-snapshot import):

| Flag | Default | Meaning |
|------|---------|---------|
| `--history_max_points` | `10` | At most this many historical snapshots per project. When the source has more candidates than this after interval bounding, they are evenly resampled across the *whole* history span, not just the oldest end. |
| `--history_min_interval_days` | `30` | Two selected snapshots are never closer together than this. |

```bash
# Migrate the current snapshot as usual, plus up to 10 historical points
# at least 30 days apart (defaults)
sonar-migration-tool transfer -c config.json --project_key my-project \
  --migrate_history

# Denser history: up to 20 points, at least 7 days apart
sonar-migration-tool transfer -c config.json --project_key my-project \
  --migrate_history --history_max_points 20 --history_min_interval_days 7
```

**Known limitations (PoC):**

- **Main branch only.** Non-main branches keep today's single-snapshot
  behavior. Backdating a non-main branch would need the create-analysis
  handshake (see [TRANSFER-INTERNALS.md](TRANSFER-INTERNALS.md)) repeated per
  historical point, which this PoC does not implement.
- **Best-effort, not transactional.** If a historical submission is rejected
  by the Compute Engine (for example, on a re-run against a project that
  already has newer analyses on the target), history migration for that
  project stops and logs a warning — it never fails or blocks the regular
  current-snapshot import that follows it.
- **Not resume-safe.** Re-running a transfer that already replayed history
  for a project resubmits the same historical points again (duplicate history
  entries on the target), since completed history points aren't tracked the
  way branch completion is. Safe to run once per target project.
- **The target's own retention still applies.** A point older than SonarQube
  Cloud's housekeeping window is accepted by the Compute Engine and then
  pruned by the target, so it never appears on the Activity page. Observed
  live: a 2021 point reported a successful Compute Engine task but was absent
  from `/api/project_analyses/search` afterwards, while every later point
  persisted. Nothing the migration can do about it — set
  `--history_min_interval_days` / `--history_max_points` with the target's
  retention in mind rather than expecting very old points to survive.
- **Each historical entry carries one placeholder file.** A report holding a
  lone project component with a raw measure is rejected by the Compute Engine,
  so every historical analysis includes a single empty
  `__history_snapshot__.<ext>` component for the measures to attach to. Its
  language is chosen from the ones the *target organization* actually has a
  quality profile for. The file is never meant to be read, but it is part of
  the analysis.
- Requires both `extract` and `migrate` to have `--migrate_history` (or the
  `migrate_history` config key) set — `transfer` sets both automatically;
  running the two commands separately needs the flag on each.

---

## Output
<!-- updated: 2026-06-05_14:00:00 -->

- **Intermediate files** — written to `--export_dir` (default `./migration-files/`). Same files as the manual workflow: `organizations.csv`, `gates.csv`, `profiles.csv`, `groups.csv`, `templates.csv`, `portfolios.csv`.
- **`migration_summary.pdf`** — PDF migration summary, written to the export directory on completion.
- **`migration_summary.md`** — Markdown rendering of the same summary, written alongside the PDF on completion.
- **`run_meta.json`** — per-phase / per-task timing and `overall_status` (`success` | `partial` | `failed`) for the run, written to the run directory on completion (including failed runs, so the summary can explain the failure).
- **`run_events.jsonl`** — JSON Lines stream of run events (one record per line) mirrored from the logger by the tee slog handler; the summary collector parses these to build the report.
- **Stdout** — every command prints `See sonar-migration-tool output results in <directory>` when it finishes so you always know where to look.

For a full description of every output file, see the [Output Files Reference](MIGRATE.md#output-files-reference) in MIGRATE.md.

---

## After the transfer
<!-- updated: 2026-07-27_23:55:00 -->

1. Log in to SonarQube Cloud and confirm the project appears under the target organization.
2. Spot-check that the quality gate and quality profile are present.
3. Spot-check that issues came across (compare counts against the source). Former Security Hotspots are part of that issue count — filter the target project by the `sqs-hotspot` tag to see them. Do **not** compare against SonarQube Cloud's Security Hotspots view: it is empty by design, because Cloud no longer has hotspots. Project data is always imported, so a fresh re-scan is not required to seed historical data — though you should still run a normal analysis once your pipeline is repointed.
4. Update your CI/CD pipeline to point at SonarQube Cloud (`SONAR_TOKEN`, `SONAR_HOST_URL`).

For more on post-migration steps, see the [After you migrate](MIGRATE.md#after-you-migrate) section in MIGRATE.md.

---

## Troubleshooting

- **Token errors** — see the [Token permissions](MIGRATE.md#token-permissions) section in MIGRATE.md.
- **Org not found** — confirm `--default_organization` matches an existing organization in your SonarQube Cloud enterprise.
- **Anything else** — [TROUBLESHOOTING.md](TROUBLESHOOTING.md) has the full list of common errors.
