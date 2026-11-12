# Migrating large instances in tranches

A single-shot `extract` + `migrate` of a large SonarQube Server instance (thousands of projects, hundreds of portfolios, groups or permission templates) takes long, is hard to predict, and is likely to hit a roadblock that forces a rerun. This guide splits the migration into steps you can run, check and rerun one at a time.

The steps use two kinds of options:

- **`--objects`** picks *what kind* of entity a run handles: `settings`, `quality_profiles` (`qp`), `quality_gates` (`qg`), `groups`, `permission_templates` (`pt`), `projects`, `portfolios`.
- **Slicing regexps** pick *which* entities of that kind: `--project_key_regexp`, `--group_name_regexp`, `--permission_template_regexp` (alias `--pt_regexp`), `--portfolio_key_regexp` / `--portfolio_name_regexp`.

For the full flag reference, see [MIGRATE.md](MIGRATE.md#step-2--extract) and [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md#all-parameters-reference).

---

## Contents

- [The plan at a glance](#the-plan-at-a-glance)
- [How tranches work](#how-tranches-work)
- [Step 0 — Extract the metadata and map organizations](#step-0--extract-the-metadata-and-map-organizations)
- [Step 1 — Groups, by tranches](#step-1--groups-by-tranches)
- [Step 2 — Settings, quality profiles and quality gates](#step-2--settings-quality-profiles-and-quality-gates)
- [Step 3 — Permission templates](#step-3--permission-templates)
- [Step 4 — Projects, by tranches](#step-4--projects-by-tranches)
- [Step 5 — Portfolios, by tranches](#step-5--portfolios-by-tranches)
- [Step 6 — Sync issues (optional)](#step-6--sync-issues-optional)
- [Rerunning a step that failed](#rerunning-a-step-that-failed)
- [Rules to keep in mind](#rules-to-keep-in-mind)

---

## The plan at a glance

| Step | What | `extract` | `migrate` | Recommended size |
|---|---|---|---|---|
| 0 | Metadata of everything | `--skip_project_data_migration` | — | one run |
| 1 | Groups | — | `--objects groups --group_name_regexp …` | ≤ 500 groups per tranche |
| 2 | Settings, quality profiles, quality gates | — | `--objects settings,qp,qg` | one run |
| 3 | Permission templates | — | `--objects pt --pt_regexp …` | ≤ 200 templates per tranche |
| 4 | Projects | `--objects projects --project_key_regexp …` | `--objects projects --project_key_regexp …` | ≤ 100 projects per tranche |
| 5 | Portfolios | — | `--objects portfolios --portfolio_key_regexp …` | per portfolio family |
| 6 | Issue triage re-sync (optional) | — | `sync-issues --project_key_regexp …` | per project tranche |

Once step 4 is done, every project is in SonarQube Cloud and **CI pipelines can be switched** to SonarQube Cloud. Steps 5 and 6 can run in parallel with, or after, that switch.

Only step 0 requires editing `organizations.csv` (and `projects.csv` if you [map unbound projects](MAPPING-UNBOUND-PROJECTS.md)). Later extracts carry your values forward.

---

## How tranches work

A few properties of the tool make this plan work. Knowing them helps when you adapt it.

- **Extracts accumulate.** `migrate` reads, per server, the latest *full* extract plus every *partial* extract newer than it. An extract is partial when it uses `--objects`, `--project_key_regexp`, `--group_name_regexp`, `--permission_template_regexp` or a portfolio regexp. In a newer partial extract, a project's records replace the older ones.
- **Mapping CSVs are regenerated with carry-over.** Each extract that includes the `projects` category rebuilds `organizations.csv`, `projects.csv`, `groups.csv`, `templates.csv`, `profiles.csv`, `gates.csv` and `portfolios.csv` from the extracts merged so far. Before that it backs up `organizations.csv` and `projects.csv` to `<name>.csv.bak-<timestamp>` and writes your `sonarcloud_org_key` values back into the new files. An extract **without** `projects` in `--objects` does not touch the CSVs.
- **`migrate` slices within what was extracted.** `migrate --project_key_regexp`, `--group_name_regexp`, `--permission_template_regexp` and the portfolio regexps narrow what was extracted; they never widen past it.
- **A tranche can use what earlier tranches created.** A projects-only run binds each project to its quality profile and quality gate by looking them up by name in the target organization. A portfolios-only run attaches projects that earlier runs created. If something is missing, the run logs a `WARN` naming it and counts it as a failure.
- **Reruns are safe.** `migrate` reuses entities that already exist in the target instead of duplicating them, so rerunning a tranche, or two tranches that overlap, is harmless.
- **The final report is cumulative.** Every `migrate` run writes `migration_summary.pdf` / `.md` in the export directory covering every migrate run so far, with the newest result per entity, and listing the runs covered.

Slicing regexps are implicitly anchored (`^…$`): a plain key matches only itself, and `BANKING_.*` matches every key starting with `BANKING_`. They are case-sensitive; prefix a pattern with `(?i)` to ignore case.

---

## Step 0 — Extract the metadata and map organizations

Extract every entity, but not the project data (issues, source code, SCM blame), which is the heavy part and is extracted tranche by tranche in step 4.

```bash
sonar-migration-tool extract --config config.json --skip_project_data_migration
```

Then edit `organizations.csv` to set the `sonarcloud_org_key` of every row (see [MIGRATE.md — Step 3](MIGRATE.md#step-3--edit-organizationscsv)), and `projects.csv` if you send individual projects to other organizations.

Review the generated `groups.csv`, `templates.csv` and `portfolios.csv`: they list every entity the later steps will slice.

> **Do not run another full (unscoped) `extract` after step 4 has started.** A full extract hides every older partial extract, including the project data of the tranches already extracted. To refresh something, extract with `--objects`.

---

## Step 1 — Groups, by tranches

Groups come first: quality profile permissions, project permissions and permission templates all grant rights to groups by name, so the groups must already exist in SonarQube Cloud.

Run one `migrate` per tranche of no more than **500 groups**, with patterns that together cover every group name:

```bash
sonar-migration-tool migrate --config config.json --objects groups --group_name_regexp '[a-f].*'
sonar-migration-tool migrate --config config.json --objects groups --group_name_regexp '[g-m].*'
sonar-migration-tool migrate --config config.json --objects groups --group_name_regexp '[n-z].*'
# … until every group in groups.csv is covered (mind upper case, digits, other characters)
```

Each tranche also sets the organization-level permissions of the groups it creates. The built-in `Anyone` and `sonar-users` groups are always in scope.

---

## Step 2 — Settings, quality profiles and quality gates

These exist in low numbers and every project depends on them. Migrate them in one run:

```bash
sonar-migration-tool migrate --config config.json --objects settings,qp,qg
```

---

## Step 3 — Permission templates

With no more than **200 templates**, migrate the permission templates in one run:

```bash
sonar-migration-tool migrate --config config.json --objects pt
```

The run doesn't need `groups`: a template's group permissions go to the groups step 1 created, which the run looks up by name in the target organization.

With more, run one `migrate` per tranche of no more than **200 templates**, selected by template name with `--permission_template_regexp` (alias `--pt_regexp`), using patterns that together cover every name in `templates.csv`:

```bash
sonar-migration-tool migrate --config config.json --objects pt --pt_regexp '[A-M].*'
sonar-migration-tool migrate --config config.json --objects pt --pt_regexp '[N-Z].*'
# … until every template in templates.csv is covered (mind lower case, digits, other characters)
```

Step 0 already extracted every template, so this step needs no extract. Each tranche creates the matching templates and grants their group and user permissions. A template is made the default in the tranche that includes it, so once every template is covered, every default is set.

If a group a template refers to is not in the target organization (its step 1 tranche was skipped or failed), the run logs a `WARN` naming the group and counts a failure. `migration_summary.pdf` then shows the template as a partial migration and names the group. Migrate that group with its step 1 tranche, then rerun the template tranche.

---

## Step 4 — Projects, by tranches

Each tranche of no more than **100 projects** is an `extract` and a `migrate` with the same pattern. The extract pulls that tranche's project data; the migrate creates the projects, binds them to the profiles and gates of step 2, applies their settings and permissions, and imports their issues and history.

```bash
# Tranche 1
sonar-migration-tool extract --config config.json --objects projects --project_key_regexp 'BANKING_.*'
sonar-migration-tool migrate --config config.json --objects projects --project_key_regexp 'BANKING_.*'

# Tranche 2
sonar-migration-tool extract --config config.json --objects projects --project_key_regexp 'RETAIL_.*'
sonar-migration-tool migrate --config config.json --objects projects --project_key_regexp 'RETAIL_.*'

# … until every project in projects.csv is covered
```

Each tranche's extract regenerates the CSVs and carries your `sonarcloud_org_key` values forward, so you don't need to edit `organizations.csv` again. If you rely on `projects.csv` to send individual projects to other organizations, check after each extract that new rows have the value you want; existing rows are carried forward.

Check `migration_summary.pdf` after each tranche. When every tranche is done, **switch the CI pipelines** of the migrated projects to SonarQube Cloud (see [After you migrate](MIGRATE.md#after-you-migrate)).

---

## Step 5 — Portfolios, by tranches

Portfolios reference projects, so they come after step 4. Step 0 already extracted them, so this step only needs `migrate`:

```bash
sonar-migration-tool migrate --config config.json --objects portfolios --portfolio_key_regexp 'PF_FINANCE_.*'
sonar-migration-tool migrate --config config.json --objects portfolios --portfolio_key_regexp 'PF_OPS_.*'
```

`--portfolio_name_regexp` slices by name instead; the two are mutually exclusive. A portfolio project that doesn't exist in the target organization is left out with a `WARN`, and a run where none of them exist fails, asking you to migrate the projects first.

---

## Step 6 — Sync issues (optional)

`migrate` already imports each project's issues with their triage state. If developers keep triaging issues in SonarQube Server after their project was migrated, [`sync-issues`](SYNC-ISSUES.md) copies the newer triage state (status, comments, tags) to SonarQube Cloud. Run it by project tranche:

```bash
sonar-migration-tool sync-issues --config config.json --project_key_regexp 'BANKING_.*' --export_directory ./sync-files/
```

Use a separate `--export_directory`: `sync-issues` runs its own extract and regenerates `organizations.csv` and `projects.csv` without carrying your values forward.

---

## Rerunning a step that failed

- **Resume a failed run** with `--run_id <id>` (`migrate`) or `--extract_id <id>` (`extract`): completed tasks are skipped. A resumed sliced extract keeps its slice. See [Resuming failed operations](MIGRATE.md#resuming-failed-operations).
- **Rerun a tranche** with the same command: entities that already exist are reused.
- **Rerun only what failed** with a narrower pattern, for example the keys listed as failed in `migration_summary.pdf`. See [Projects skipped because a request for them failed](TROUBLESHOOTING.md#projects-skipped-because-a-request-for-them-failed).
- **Rate limits**: the tool backs off and retries on its own; see [SonarQube Cloud API rate limiting handling](ADVANCED-CONFIG.md#sonarqube-cloud-api-rate-limiting-handling). Lower `--api_max_rate_per_min` if a tranche keeps hitting them.

---

## Rules to keep in mind

- **Keep one export directory** for the whole migration: the merging of extracts, the CSV carry-over and the cumulative report all work within it.
- **Run one `migrate` at a time** against the same SonarQube Cloud organization.
- **Use `--objects` on every run after step 0.** A run without it migrates every category, including every project extracted so far.
- **Long pauses between tranches**: if a project is analyzed again on SonarQube Server after its tranche was extracted, extract that tranche again before migrating it.
- **Preview first**: [`predictive-report`](MIGRATE.md#predictive-report--preview-the-migration-before-you-commit) shows what the migration will produce from the extracted data, without calling SonarQube Cloud.
- **Undo a bad tranche** with [`reset`](MIGRATE.md#reset--undo-a-bad-migration), scoped with `--organization`.
