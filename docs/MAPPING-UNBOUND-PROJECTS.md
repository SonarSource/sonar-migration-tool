# Mapping unbound SonarQube Server projects

`organizations.csv` maps one SonarQube Server DevOps binding to one SonarQube Cloud organization. Every project that was never bound on SonarQube Server shares a single row — the one keyed on the server URL — so by default they all land in the same organization. That is usually not what you want: the projects were never bound, but they still belong to different teams, and those teams already have their own SonarQube Cloud organizations.

`projects.csv` carries a `sonarcloud_org_key` column, the second column in the file, for exactly this. Fill it in for a project and that project migrates into the organization you named, whatever `organizations.csv` says. Leave it empty and the project follows `organizations.csv` as before. (Issue [#612](https://github.com/SonarSource/sonar-migration-tool/issues/612).)

## How to use it

1. Run `extract`, then `structure`, then `mappings` as usual.
2. Edit `organizations.csv` and fill in `sonarcloud_org_key` for each row, or plan to pass `--default_organization`. **You still need this.** The per-project column is an override, not a replacement: a run with no organization mapping at all still stops with exit code 2 and asks you to review `organizations.csv`.
3. Open `projects.csv` in a spreadsheet or text editor. For each project you want to send somewhere else, put the target organization key in the `sonarcloud_org_key` cell. Leave every other cell empty.
4. Run `migrate`.

Example — three projects, two of them redirected:

```csv
key,sonarcloud_org_key,name,gate_name,...
payments-api,acme-payments,Payments API,Sonar way,...
billing-web,acme-billing,Billing Web,Sonar way,...
legacy-batch,,Legacy Batch,Sonar way,...
```

`payments-api` migrates into `acme-payments`, `billing-web` into `acme-billing`, and `legacy-batch` into whatever `organizations.csv` maps its server URL to.

Re-running `structure` rewrites `projects.csv` from the extract and clears the column, the same way it rewrites `organizations.csv`. Edit the file after `structure`, not before.

**`transfer` cannot be used for this.** `transfer` runs `structure` itself, inside the same command and immediately before it migrates, so it rewrites `projects.csv` and any override you had put there is gone before it is read. Use the step-by-step `migrate` workflow above instead. If you have already run `transfer`, its export directory is usable as-is: edit `projects.csv` and run `migrate` against that directory.

## Bound projects cannot be redirected

A project bound to a DevOps platform that SonarQube Cloud also supports — GitHub.com, GitLab.com, Bitbucket Cloud, Azure DevOps Services — has to land in the organization that carries the matching platform binding. Its own project binding and its pull-request decoration can only be recreated there, so the `organizations.csv` row derived from that binding is the only correct destination.

For such a project the override is ignored. The project migrates to its binding-derived organization and the run logs a `WARN` naming the project, the organization that was ignored, and the organization it went to instead:

```
level=WARN msg="projects.csv: organization override ignored because the project is bound to a DevOps platform — a bound project must stay in the organization that carries the matching platform binding, and will migrate there instead" project=payments-api ignored_organization=acme-payments organization=acme-github alm=github
```

A project bound to an **on-premise** platform (GitHub Enterprise Server, self-managed GitLab, Bitbucket Server / Data Center) is a different case. Those platforms have no SonarQube Cloud counterpart, so the binding cannot be migrated and nothing pins the project to a particular organization. The override applies to them, exactly as it does to a project with no binding at all. The `is_cloud_binding` column in `projects.csv` tells you which case a project is in.

## What the override does and does not move

The override moves the project and everything scoped to it: the project itself, its key (including the `<ORGANIZATION_KEY>` part when `--project_key_pattern` uses it), its settings, its new code definition, its branches, its project data, and its issue and hotspot metadata sync.

It does **not** move the organization-scoped objects. Quality profiles, quality gates, groups and permission templates are mapped per source organization in their own CSVs and still go where `organizations.csv` sends them. So before you redirect a project, check that the destination organization already has the quality profiles and quality gate that project needs, or the project will fall back to that organization's defaults. If it does not, map that organization in `organizations.csv` too so the objects get created there.

## Checking your work before you migrate

Two commands read the column, so you can verify the routing without writing anything to SonarQube Cloud:

- `predictive-report` predicts each project in the organization the override sends it to, bound-project refusals included. It makes no SonarQube Cloud API calls.
- `migrate` checks every override organization against SonarQube Cloud before it creates anything. A key that does not exist, or that the token cannot see, stops the run with exit code 3 and an error naming both the key and `projects.csv` — rather than a 404 half way through.

`reset` also reads the column, so a migration that dispatched projects across extra organizations can still be undone: those organizations appear in the reset candidate list alongside the `organizations.csv` ones.

## Related

- [Using `migrate`](MIGRATE.md) — the full workflow, including `organizations.csv`
- [Using `transfer`](TRANSFER.md) — the single-command equivalent, which cannot carry an override (see above)
