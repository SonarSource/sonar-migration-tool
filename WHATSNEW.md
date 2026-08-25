# What's New

# v1.1 - 2026-08-26

Changes introduced in `sonar-migration-tool` **1.1**, since the **1.0** release. Issue numbers refer to [GitHub issues](https://github.com/SonarSource/sonar-migration-tool/issues).

## New features

- Added **Standalone `sync-issues` command** — issue and hotspot status synchronization (custom tags, comments, resolutions) between a source SonarQube Server and a target SonarQube Cloud project can now be run on its own, without a full `migrate`/`transfer`. Useful when project data migration was skipped and a scan already repopulated the target project. ([#412](https://github.com/SonarSource/sonar-migration-tool/issues/412))
- **Hotspots migrated as issues** — since SonarQube Cloud dropped the new hotspots on July 1st, 2026, hotspots are now converted to issues on migration, the same way a real scanner analysis would, and tagged `sqs-hotspot`. ([#423](https://github.com/SonarSource/sonar-migration-tool/issues/423))
- **DevOps platform project binding migration** — project-level DevOps platform bindings (GitHub repo, GitLab project ID, Azure DevOps project/repo, Bitbucket Cloud slug) are now migrated to SonarQube Cloud when the target organization is bound. When binding isn't possible, the migration report marks the project "Partially Migrated" with an explanation. ([#122](https://github.com/SonarSource/sonar-migration-tool/issues/122))
- **ETA in logs and progress bar in GUI** — both the CLI and GUI now show an overall progress percentage and **estimated** time remaining while `extract` and `migrate` run. ([#519](https://github.com/SonarSource/sonar-migration-tool/issues/519), [#520](https://github.com/SonarSource/sonar-migration-tool/issues/520))
- **GUI beta** — the GUI was improved for stability and user friendliness. Now in Beta status. ([#516](https://github.com/SonarSource/sonar-migration-tool/issues/516))
- **Select projects by regexp for `transfer`** — the `transfer` command (and its config file equivalent) now accepts a regexp for `--project_key`, so a subset of projects can be selected without listing every key individually. ([#529](https://github.com/SonarSource/sonar-migration-tool/issues/529))
- Added **`--fast_sync` option** to `migrate` and `transfer` — skips issue/hotspot sync work for findings that are still in their original, untouched state (`OPEN` issues, `TO_REVIEW` hotspots with no tags or comments), significantly speeding up large migrations. ([#527](https://github.com/SonarSource/sonar-migration-tool/issues/527))
- **Identify migrated projects in SonarQube Cloud** — migrated projects now carry a `ci_name=sonar-migration-tool` analytics marker and a "SQS migrated project" link back to the original project's URL, making migrated projects easy to spot for support purposes. ([#418](https://github.com/SonarSource/sonar-migration-tool/issues/418))
- Adjusted `sonar-migration-tool` to align with SonarQube Cloud behavior change (Required `analytics.pb` file [#478](https://github.com/SonarSource/sonar-migration-tool/issues/478))

## Enhancements

- Migration report "run metadata" now breaks duration down by phase (global objects, project configuration, project data, issue sync) instead of a single total. ([#530](https://github.com/SonarSource/sonar-migration-tool/issues/530))
- Migration reports (predictive and actual) now show the source project key under the project name. ([#448](https://github.com/SonarSource/sonar-migration-tool/issues/448))
- A project migration that fails because its key already exists in another organization is now reported as "Failed" with an explicit error, instead of a misleading status. ([#525](https://github.com/SonarSource/sonar-migration-tool/issues/525))
- API calls now use a dedicated `sonar-migration-tool` user agent instead of the Go client default, making tool activity easier to spot in SonarQube Cloud logs. ([#479](https://github.com/SonarSource/sonar-migration-tool/issues/479))
- The release workflow builds and publishes Linux (AMD64/ARM64) binaries again. ([#481](https://github.com/SonarSource/sonar-migration-tool/issues/481))
- The PDF report's user/group permission listing now skips users and groups that have no global permissions, cutting noise on instances with many users. ([#475](https://github.com/SonarSource/sonar-migration-tool/issues/475))
- Adjusted for the new SonarQube CLoud requirement to includes and `analytics.pb` in `api/ce/submit` zip files ([#478](https://github.com/SonarSource/sonar-migration-tool/issues/478))

## Bug fixes

- Fixed intermittent hotspot sync results where some hotspots were dropped between runs. ([#539](https://github.com/SonarSource/sonar-migration-tool/issues/539))
- Silenced noisy `WARN` logs on the `extract` `getTasks` step caused by probing task types that don't exist on the source edition/version. ([#533](https://github.com/SonarSource/sonar-migration-tool/issues/533))
- Fixed Compute Engine rejecting migrated analysis reports with "mandatory file 'analytics.pb' is missing". ([#478](https://github.com/SonarSource/sonar-migration-tool/issues/478))
- Projects using a 3rd-party plugin for an unsupported programming language no longer silently end up with no issues or branches — the tool now detects and reports this. ([#474](https://github.com/SonarSource/sonar-migration-tool/issues/474))
- Fixed issue custom tags not being migrated for all issues during sync. ([#456](https://github.com/SonarSource/sonar-migration-tool/issues/456))
- Fixed missing source code syntax highlighting on migrated projects. ([#420](https://github.com/SonarSource/sonar-migration-tool/issues/420))
- Project binding migration now runs before migration report generation, so binding results are correctly reflected in the report. ([#506](https://github.com/SonarSource/sonar-migration-tool/issues/506))

## Other improvements

- Tool versioning now follows semver, read from `go/internal/version/version.go`, with an automated PR to bump the version after each release. ([#500](https://github.com/SonarSource/sonar-migration-tool/issues/500))
- Releases are now triggered manually instead of on every merge to `main`. ([#495](https://github.com/SonarSource/sonar-migration-tool/issues/495), [#494](https://github.com/SonarSource/sonar-migration-tool/issues/494))
- GitHub Actions workflows upgraded off deprecated Node.js 20. ([#509](https://github.com/SonarSource/sonar-migration-tool/issues/509))
- Binary assets (favicons, fonts, images) excluded from source code scanning. ([#511](https://github.com/SonarSource/sonar-migration-tool/issues/511))
- Documentation cleanup: removed stale internal planning files and references to running the tool from source in user-facing docs. ([#457](https://github.com/SonarSource/sonar-migration-tool/issues/457), [#458](https://github.com/SonarSource/sonar-migration-tool/issues/458))
- Updated wording in the migration tool datasheet regarding migration outcomes. ([#488](https://github.com/SonarSource/sonar-migration-tool/issues/488))
