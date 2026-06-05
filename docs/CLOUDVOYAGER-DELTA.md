# CloudVoyager Delta — Full Bug & Logic Error Audit
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

This document catalogues every confirmed bug, logic error, and missing feature in the
migration tool compared to CloudVoyager. Items are ordered by **severity** (data-loss risk
first, then correctness, then missing capabilities).

Reference implementation: `/Users/joshua.quek/Desktop/Active Projects/CloudVoyager Agents/CloudVoyager/src/`
Migration tool: `go/internal/`

---

## P0 — Data-Loss / Silent Wrong-Data Bugs
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

### ~~BUG-01: `BackdateChangesets` and `toExtractedIssues` are dead code~~ **[FIXED]**
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Status**: FIXED — commits `e769b95` and `21d74e8` on branch `fix/history-creation-date-fix` (merged to main via PR #291). `BackdateChangesets` is now called with both native AND external issues. `extIssuesToExtracted()` was added as a helper that properly maps external issue creation dates. The date-map key mismatch (BUG-04/BUG-15) was also resolved as part of this fix.

~~**File**: [go/internal/migrate/tasks_scanhistory.go](../go/internal/migrate/tasks_scanhistory.go)~~
~~**Severity**: P0 — all migrated issues get wrong creation dates~~

~~**Progress note (2026-05-30)**: Commit `e71b690` wired up the original creation date flow for changeset construction, but `BackdateChangesets` and `toExtractedIssues` may still not be called from `importBranch()` in the final form. Verify the current state of the task before proceeding with the full fix.~~

~~`toExtractedIssues()` (line 644) and `BackdateChangesets()` (in
[go/internal/scanreport/backdate.go](../go/internal/scanreport/backdate.go)) both exist but
are **never called** from `importBranch()`. The changesets passed to CE are built with
`time.Now()` as every line's blame date.~~

~~**CloudVoyager**: calls `backdateChangesets()` which uses each issue's `creationDate` from
SQ to spread changeset timestamps realistically. This is what controls the "introduced date"
for each issue in SonarCloud's UI and new-code period logic.~~

~~**Impact**: Every migrated issue appears to have been introduced at migration time.
New-code-period rules (e.g. "new issues in last 30 days") will treat all historical issues
as new, breaking quality gate logic.~~

~~**Fix**: In `importBranch()`, after building `changesets`, call:~~
```go
extracted := toExtractedIssues(issues, e)
scanreport.BackdateChangesets(extracted, changesetsByComponent, now)
```
~~where `changesetsByComponent` is keyed by component string (needs a small adapter — current
`buildChangesetMap` returns `map[int32]*pb.Changesets`; backdate needs `map[string]*pb.Changesets`).~~

---

### BUG-02: `ActiveRuleInput` missing four fields — `ParamsByKey`, `CreatedAt`, `UpdatedAt`, `Impacts`
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/scanreport/builder.go:294](../go/internal/scanreport/builder.go#L294),
[go/internal/migrate/tasks_scanhistory.go:588](../go/internal/migrate/tasks_scanhistory.go#L588)

`ActiveRuleInput` struct only has `RuleRepo`, `RuleKey`, `Severity`, `QProfileKey`, `Language`.
`BuildActiveRules()` only sets `RuleRepository`, `RuleKey`, `Severity`, `QProfileKey` on the
protobuf message.

**CloudVoyager** sets all of these per active rule:
- `paramsByKey` — rule parameters map (defaults to `{}`)
- `createdAt` — timestamp when rule was activated
- `updatedAt` — timestamp of last update
- `impacts` — array of `{ softwareQuality, severity }` for Clean Code taxonomy (SQ 10.0+)

**Impact**: Rules migrated to SC lack parameter overrides (e.g. regex patterns, threshold
values). Impacts are missing entirely, so Clean Code taxonomy metrics (Maintainability,
Reliability, Security) won't reflect the source configuration.

**Fix**:
1. Add fields to `ActiveRuleInput`: `Params map[string]string`, `CreatedAt time.Time`,
   `UpdatedAt time.Time`, `Impacts []RuleImpact` (new struct `{SoftwareQuality string; Severity string}`)
2. Populate from extracted rule data in `loadExtractedActiveRules()` — SQ `/api/rules/search`
   includes `params` and `impacts` arrays, and the quality profiles rules endpoint includes timestamps.
3. Add corresponding fields to the `pb.ActiveRule` construction in `BuildActiveRules()`.

---

### ~~BUG-03: `ReferenceBranchName` never set in `MetadataInput` inside `importBranch`~~ **[FIXED — main; COMPLETED — non-main]**
<!-- updated: 2026-06-05_13:45:00 -->

**Status**: FIXED (main) — commit `1eeb9d8` added `ReferenceBranchName` to `MetadataInput`; `BuildMetadata` writes it to protobuf `Metadata` field 11 (`reference_branch_name`, = SonarCloud's `merge_branch_name`), defaulting to `BranchName` when unset. **COMPLETED (non-main)** — branch `fix/issue-104-migrate-multiple-branches`: that 2024 fix only ever set the field for the main branch (which self-references harmlessly because it sends no branch characteristic). Non-main branches kept self-referencing — exactly the `(For non-main branches it should be the main branch name.)` note that was struck through but never implemented. `importBranch` now sets `ReferenceBranchName` = the project's main branch for non-main branches (via `resolveMainTargetName` → `branchImportContext.MainTargetName` → `importBranchInput.ReferenceBranch`). **Verified live against SonarCloud staging**: this flips the CE from hard-rejection ("issue whilst processing the report") to **SUCCESS** for non-main branches whose names match the long-lived branch pattern (e.g. `release-3.x`, `reduce-tech-debt` went FAILED → SUCCESS). The main branch is unchanged (still imports 1292 issues). **See BUG-17** for the remaining branch-persistence limitation that this fix does not overcome.

~~**File**: [go/internal/migrate/tasks_scanhistory.go:200](../go/internal/migrate/tasks_scanhistory.go#L200)~~

~~`BuildMetadata()` accepts `ReferenceBranchName` (field in `MetadataInput`) and propagates it
to the protobuf `Metadata.ReferenceBranchName`. However `importBranch()` constructs
`MetadataInput` without setting this field — it is always empty string.~~

~~**CloudVoyager**: explicitly sets `referenceBranchName` to the SC branch name (same as
`branchName` for the main branch).~~

~~**Impact**: SC cannot determine the reference branch for new-code comparisons, breaking
"New Code" periods that rely on reference branch comparison.~~

~~**Fix**: Set `ReferenceBranchName: targetBranch` in the `MetadataInput` struct literal in
`importBranch()`. (For non-main branches it should be the main branch name.)~~

---

## P1 — Logic Errors (Wrong Behavior)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

### ~~BUG-04: `toExtractedIssues` date lookup keyed on `ruleRepo:ruleKey` instead of issue key~~ **[FIXED]**
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Status**: FIXED — resolved as part of the BUG-01 fix (commits `e769b95` and `21d74e8`, branch `fix/history-creation-date-fix`, merged to main via PR #291). `extIssuesToExtracted()` was added as a helper that properly maps external issue creation dates, eliminating the date-map key mismatch.

~~**File**: [go/internal/migrate/tasks_scanhistory.go:644](../go/internal/migrate/tasks_scanhistory.go#L644)~~

~~`toExtractedIssues()` builds `dateMap` keyed on `iss.RuleRepo+":"+iss.RuleKey` (line 666),
but then looks up the date using the same key. Multiple issues with the same rule on the same
file will all collapse to whichever creation date was last written — only the last one wins.~~

~~The correct key is the SQ **issue key** (the UUID), not the rule identifier.~~

~~**CloudVoyager**: Uses the per-issue `creationDate` timestamp from the SQ issue object
(keyed on `issue.key`).~~

~~**Impact**: Once BUG-01 is fixed and `BackdateChangesets` is actually called, all issues
sharing the same rule key on a component would be backdated to the same (wrong) date.~~

~~**Fix** in `toExtractedIssues()`:~~
~~- Build `dateMap[issueKey]` using `key := extractField(item.Data, "key")`~~
~~- When building `result`, look up date by the issue's actual key (need to add an `IssueKey`
  field to `IssueInput` or pass it separately).~~

---

### BUG-05: Issue assignment (`Assignee`) is loaded but never synced
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_issuesync.go:388](../go/internal/migrate/tasks_issuesync.go#L388)

`syncOnePair()` executes: transition → comments → tags. The `matchableIssue` struct has an
`Assignee` field which is populated from both extract data and Cloud API, and
`hasManualChanges()` counts a non-empty `Assignee` as a trigger for actionability. But
`syncOnePair()` never calls any assignment function.

**CloudVoyager**: calls `syncIssueAssignment()` per issue which:
- Consults `user-mappings.csv` to translate SQ login → SC login
- Skips users marked `include: false`
- Calls `POST /api/issues/assign`
- Tracks `assignmentFailed` separately with detailed error info

**Impact**: No assignee is set on any migrated issue in SC. Assignee data is silently
discarded.

**Fix**:
1. Add a `user-mappings.csv` structure (can be optional/empty by default)
2. Add `syncIssueAssignment()` to `syncOnePair()` between transition and comments
3. Track assignment separately in counters

---

### BUG-06: No source-link comment added to issues or hotspots
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Files**: [go/internal/migrate/tasks_issuesync.go](../go/internal/migrate/tasks_issuesync.go),
[go/internal/migrate/tasks_hotspotsync.go](../go/internal/migrate/tasks_hotspotsync.go)

Neither `syncOnePair()` nor `syncOneHotspot()` adds a source-link comment pointing back to
the original SQ issue/hotspot.

**CloudVoyager** adds:
- Issues: `Link to [Original issue](${sqBaseURL}/project/issues?id=${projectKey}&issues=${key}&open=${key})`
- Hotspots: `Link to [Original hotspot](${sqBaseURL}/security_hotspots?id=${projectKey}&hotspots=${key})`

**Impact**: No traceability from SC issue back to the original SQ issue. After migration,
engineers can't click through to the original SQ instance.

**Fix**: After comment sync, add one final comment with the SQ URL. Requires `ServerURL` to
be threaded into `syncOnePair` / `syncOneHotspot` (it's already in the `Executor` as
`e.ExtractMapping` ServerURLs).

---

### BUG-07: Issue comment format differs from CloudVoyager
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_issuesync.go:439](../go/internal/migrate/tasks_issuesync.go#L439)

Migration tool comment format:
```
[Migrated from ${login} on ${createdAt}]

${text}
```

CloudVoyager format:
```
[Migrated from SonarQube] ${login} (${createdAt}): ${text}
```

**Impact**: The migration tool's idempotency check for issues looks for the
`metadataSyncTag` tag (not the comment prefix), so this isn't a correctness bug. However
the format differs from the industry-established CloudVoyager convention, making
cross-tool reports inconsistent.

---

### ~~BUG-08: Hotspot sync misses `TO_REVIEW` state (reopening)~~ **[FIXED]**
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Status**: FIXED — hotspot `TO_REVIEW` comment sync was fixed (confirmed in TROUBLESHOOTING.md).

~~**File**: [go/internal/migrate/tasks_hotspotsync.go:266](../go/internal/migrate/tasks_hotspotsync.go#L266)~~

~~`buildHotspotPairs()` only includes pairs where `source.Status == "REVIEWED"`. Hotspots that
were reviewed in SQ but later reopened (`TO_REVIEW`) are never synced.~~

~~**CloudVoyager** handles `TO_REVIEW` as a status target — it re-opens hotspots that were
reviewed and then reverted.~~

~~**Impact**: Hotspots in `TO_REVIEW` state in SQ are left in whatever state CE imported them
(likely `TO_REVIEW` already), so this may be benign in practice. But the explicit logic gap
exists.~~

---

### BUG-09: Hotspot metadata-sync marker is incompatible with CloudVoyager's marker
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_hotspotsync.go:353](../go/internal/migrate/tasks_hotspotsync.go#L353)

Migration tool idempotency marker: `[Migrated from SonarQube]` (content of hotspot comments
is checked via prefix match).

CloudVoyager idempotency marker: `[Metadata Synchronized] This hotspot's metadata has been
synced from SonarQube.`

If CloudVoyager ran first on a project, then this tool runs — it won't find `[Migrated from
SonarQube]` in the SC hotspot comments and will add all the comments again as duplicates.

**Fix**: Check for both prefixes in `isAlreadyMigratedComment()`, or use the CloudVoyager
marker to maintain compatibility.

---

### BUG-10: No duplication data included in protobuf report
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/scanreport/packager.go](../go/internal/scanreport/packager.go),
[go/internal/migrate/tasks_scanhistory.go](../go/internal/migrate/tasks_scanhistory.go)

`ReportData` struct has no `Duplications` field. `PackageReport()` does not write any
`duplications-{ref}.pb` files. The extraction phase extracts duplicate data (it's part of
the component tree in SQ's API) but it's discarded.

**CloudVoyager**: includes `duplications-{ref}.pb` files per component via `buildDuplications()`.

**Impact**: Duplication metrics and the "Duplications" tab in SC will be empty after migration.

---

### BUG-11: No measures data included in protobuf report
<!-- updated: 2026-06-04_12:00:00 -->

**File**: [go/internal/migrate/tasks_scanhistory.go:218](../go/internal/migrate/tasks_scanhistory.go#L218)
**Tracking**: [GitHub Issue #106](https://github.com/sonar-solutions/sonar-migration-tool/issues/106) — **Implementation plan**: [PLAN-FIX-106.md](../PLAN-FIX-106.md)

`reportData.Measures` is initialized as `make(map[int32][]*pb.Measure)` (empty map) and
never populated. There is no `loadExtractedMeasures()` function. The protobuf infrastructure
(`BuildMeasures()`, `addMeasures()`, `Measure` proto message) is fully built and tested — only
the wiring in `importBranch()` and the extract metric key expansion are missing.

**CloudVoyager**: builds measures from `buildMeasures()` covering lines, complexity, coverage,
issues, bugs, etc. Encodes file-level measures into `measures-{ref}.pb` protobuf files grouped
by component ref. Uses a `STRING_METRICS` allowlist and int32/int64 range splitting.

**Impact**: All code metrics (lines of code, coverage, complexity, etc.) in SC will be zero
or absent after migration — only live scans will populate them.

**Fix summary** (see [PLAN-FIX-106.md](../PLAN-FIX-106.md) for full details):
1. Expand `projectComponentTreeTask()` to request ~35 metric keys (not just `ncloc`)
2. Fix `buildMeasureValue()` for LongValue and string metric handling
3. Add `loadExtractedComponentMeasures()` in migrate phase
4. Wire `BuildMeasures()` into `importBranch()` and populate `ReportData.Measures`

---

### BUG-16: Multi-branch migration ordering and reliability (6 sub-bugs)
<!-- updated: 2026-06-04_15:00:00 -->

**Status**: **FIXED** — branch `fix/issue-104-migrate-multiple-branches`. Six bugs related to multi-branch scan history import were identified and fixed in `tasks_scanhistory.go`:

1. **BUG-16a (Critical): Main branch not guaranteed first** — `sortBranchesMainFirst()` now ensures the main branch is always uploaded before non-main branches. Previously, branch ordering was non-deterministic, which could cause CE to reject non-main branch uploads if the main branch hadn't been imported yet.

2. **BUG-16b+c (Critical/High): No CE gate between main and non-main branches** — `importProjectBranches` was restructured into two phases: (1) import the main branch first and wait for CE SUCCESS, (2) only then import non-main branches. If the main branch CE task fails, all non-main branches are marked "skipped" and the project is aborted. This matches CloudVoyager's sequencing behavior.

3. **BUG-16d (Medium): No branch filtering** — Added `ExcludeBranches` config option (glob patterns via `filepath.Match`) to skip non-main branches during scan history import. Available as `--exclude-branches` CLI flag and `exclude_branches` JSON config key. The main branch is never excluded regardless of patterns.

4. **BUG-16e (Medium): No per-branch checkpoint/resume** — Added `loadCompletedBranches()` and `shouldSkipBranch()` to read existing `importScanHistory` results and skip branches that already succeeded on resume. Previously, resuming a failed run would re-import branches that had already completed successfully.

5. **BUG-16f (Medium): Project-level concurrency not properly managed** — Rewrote `runImportScanHistory` to use `errgroup.WithContext` + `g.SetLimit(cap(e.Sem))` for parallel project processing. Individual project failures no longer cancel other projects (settled semantics).

**Files changed**: `tasks_scanhistory.go`, `tasks_scanhistory_test.go` (12 new tests), `migrate.go`, `config_file.go`, `config_file_test.go`, `cmd/migrate.go`, `cmd/transfer.go`, `cmd/transfer_test.go`.

---

### ~~BUG-17: Non-main branches accepted by the CE but not persisted on SonarCloud~~ **[FIXED]**
<!-- updated: 2026-06-05_19:15:00 -->

**Status**: **FIXED** — branch `fix/issue-104-migrate-multiple-branches`. Root cause: the tool POSTed the report straight to `/api/ce/submit`, but a real SonarCloud scanner first performs a server-side **"Create analysis" handshake** that anchors the branch row and returns an `analysisUuid`, which it stamps into the report's `metadata.analysis_uuid` (proto **field 19**). The CE binds an uploaded report to a branch **solely** via that `analysis_uuid`; our reports omitted it (our vendored proto even *reserved* field 19), so the CE processed the report (task SUCCESS) but never created the branch.

**Fix** (captured from the real scanner via mitmproxy + confirmed by a live `201`):
- `scanner-report.proto`: un-reserved field 19 → `string analysis_uuid = 19;` (regenerated).
- `submit.go`: added `PreCreateAnalysis(ctx, client, AnalysisConfig)` → `POST {APIURL}/analysis/analyses` (the api host, **no `/api/v2` prefix**; Bearer; JSON `{organizationKey, projectKey, projectVersion, branchName, targetBranchName, branchType}`) → returns `{"id": <analysisUuid>, ...}`.
- `tasks_scanhistory.go` `buildBranchReport`: for **non-main** branches, calls the handshake (via `e.RawAPI.HTTPClient()` + `e.APIURL`) with **`branchType:"long"`** and stamps the returned `analysisUuid` into the metadata. The main branch needs no handshake. `/api/ce/submit` is unchanged (still `branchType=LONG`).
- **`branchType:"long"` for every migrated branch** so they are long-lived and keep full issue history — SonarCloud auto-prunes *short-lived* branches after ~30 days.

**Live verification** (SC staging, `open-digital-society-1_okorach-oss_sonar-tools`): non-main branches now **persist as LONG with full issue history** — `release-3.x` 1511 issues, `reduce-tech-debt` 1290, `my-test` 831 (all `type=LONG`); previously 0 / not listed. `develop` + `feat/add-ruff-linting` are gracefully skipped (source purged — see BUG-18).

The historical analysis below concluded this was an unfixable injection limitation — that was **wrong**; the missing piece was the create-analysis handshake.

~~(historical) **Status**: OPEN — strongly evidenced to be a **limitation of the report-injection approach on SonarCloud**, not a code bug fixable by tweaking the report alone.~~

**Symptom**: After the BUG-03 completion, a non-main branch report POSTed to `/api/ce/submit` with `characteristic=branch=<name>` + `characteristic=branchType=LONG` and metadata `reference_branch_name=<main>` returns CE task **SUCCESS**, but the branch never materializes:
- `/api/project_branches/list` shows only the main branch.
- `/api/project_analyses/search?branch=<name>` → `Component ... on branch '<name>' not found`.
- The branch reports 0 issues.

**Live evidence** (project `open-digital-society-1_okorach-oss_sonar-tools`, SC staging, branch `fix/issue-104-migrate-multiple-branches`):

| Branch | Long-lived pattern? | CE result | Persisted? |
|--------|---------------------|-----------|------------|
| `master` (main) | yes | SUCCESS | ✅ 1292 issues |
| `release-3.x` | yes (`release-.*`) | SUCCESS (was FAILED) | ❌ not found |
| `develop` | yes (`develop`) | **FAILED** | ❌ |
| `reduce-tech-debt` | no → SHORT | SUCCESS | ❌ (short-lived: PR-like, never persisted) |

**Findings**:
1. **`branchType=LONG` is correct.** The SC main branch reports `type=LONG` in `/api/project_branches/list`, and a real `sonar-scanner` submitting to sc-staging.io sends `characteristic=branchType=LONG` (Sonar support ticket #14210). Do **not** change it to `BRANCH`.
2. **Branch classification/persistence is server-side**, governed by the long-lived branch name pattern (`sonar.branch.longLivedBranches.regex`, here `(comma,branch|develop|main|master|release-.*|trunk)`) on first analysis — not by the injected `branchType`. Names that don't match are treated as short-lived (PR-like, auto-deleted, no overall-code branch).
3. **Raw report injection via `/api/ce/submit` is not a supported branch-creation path** (per Sonar internal guidance + public docs). The real scanner performs additional branch orchestration (fetches branch config from the server, computes the reference branch, ships real SCM/changeset data); a handcrafted report appears to omit whatever the CE needs to materialize the branch entity.
4. **CloudVoyager also never moved a non-main branch here** (`syncAllBranches:true` notwithstanding), consistent with this being inherent to the injection approach rather than tool-specific.

**Side effect to address**: the tool currently records branches in (2)/(3) as `status=success` in `importScanHistory` results even though no data lands — over-reporting. A post-submit `/api/project_branches/list` verification would make the reported status accurate.

---

### BUG-18: Report-validity hardening (the `develop` hard-failure) — **[FIXED]**
<!-- updated: 2026-06-05_14:40:00 -->

**Status**: FIXED — branch `fix/issue-104-migrate-multiple-branches`. While BUG-17 covers branches the CE *accepts* but doesn't persist, `develop` on `okorach-oss_sonar-tools` was *hard-rejected* by the CE (~6s, generic error) where the structurally similar `release-3.x` succeeded. Byte/proto-diffing the two built reports (offline, via a temporary `SMT_DUMP_REPORT_DIR` dump — since removed) isolated three problems in `develop`, all absent from `release-3.x`:

1. **Source text purged (root cause).** The `develop` branch has **line measures but no retrievable source text**. Both `/api/sources/raw` and `/api/sources/lines` (the endpoint the SonarQube UI uses to render a file) return **0 lines** for every develop file — e.g. `sonar/projects.py` reports `ncloc=1193` yet `sources/lines=0` — while `release-3.x` returns full source (projects.py: 1717 lines). All 96 of develop's extracted `source-*.txt` were 0 bytes (release-3.x: 942 KB). **Why:** develop was last analyzed **2025-05-01** (13+ months ago) vs release-3.x **2025-10-25** and master **2026-05-11**; SonarQube housekeeping purges source/SCM data for old/inactive branches while retaining aggregate measures and issues. The project's **Code list** in the UI still shows develop's LOC (13,942) because that is a *measure* — but opening any develop file shows an empty source view. This is a **source-side data condition (purged source), not an extraction bug** — confirmed against `/api/sources/lines`, the UI's own endpoint.
2. **Out-of-range issue lines.** With no source, component line counts fell back to **ncloc** (e.g. `sonar/tasks.py` declared 288 lines) while issues referenced physical lines up to 381 → 29 `range_exceeds_lines` → CE reject.
3. **Orphan rule.** A native `secrets:S6702` issue referenced a rule never extracted into `activerules.pb` (the `secrets` repo isn't among the project's active-rule repos) → CE reject.

**Fixes (all in `tasks_scanhistory.go`, with tests)** — defensive, so they also harden **main-branch** migration of any project with the same gaps:
- `fixComponentLineCounts` now raises each component's line count to at least the largest line any issue points at (`maxIssueEndLineByComponent`), never below it.
- `dropIssuesWithInactiveRules` drops native issues whose `(repo, key)` is not in the active-rule set (an issue on an unactivated rule aborts the whole report and can't be recreated anyway). Hotspots are exempt.
- **Purged-source skip**: a branch with findings but **zero** retrievable source text (`totalSourceLen == 0`) is now **skipped** with a clear status (`"source code not retrievable for this branch (line measures may remain, but source text is gone — likely purged by SonarQube housekeeping); re-analyze the branch on the source server to migrate it"`) instead of submitting a doomed report. Issues with no source cannot be anchored to lines, and the CE requires source.

**Net for `develop`**: the report is now structurally clean (0 out-of-range, 0 orphan rules — verified by proto-diff), but because its source text has been purged on the server, the tool now **skips it cleanly** with an actionable message rather than hard-failing. To actually migrate develop, re-analyze it on the source server first (which restores its source), then re-run. (Even a structurally valid `develop` report would not persist — see BUG-17.) `importBranch` was also refactored into `importBranch` + `buildBranchReport` + `fixComponentLineCounts` to keep cognitive complexity within the project bar.

---

## P2 — Missing Features (Present in CloudVoyager, Absent Here)
<!-- updated: 2026-06-04_15:00:00 -->

### FEAT-01: No user-mappings.csv support
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

CloudVoyager reads `user-mappings.csv` with columns `sqLogin`, `scLogin`, `include` to
translate SQ user logins to SC logins for both issue and hotspot assignment. Without this,
all assignee data is lost (BUG-05 is a symptom).

**Impact**: Issues land in SC unassigned regardless of SQ assignee.

---

### FEAT-02: No changelog extraction for issues
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/extract/tasks_scanhistory.go](../go/internal/extract/tasks_scanhistory.go)

The extraction phase fetches `getProjectIssuesFull` but does not include issue changelog
data (`/api/issues/search?additionalFields=_all` includes `changelog`). CloudVoyager
extracts the full changelog for each issue and uses it to replay all transitions.

**Impact**: Migration tool applies only the current final state as a single transition.
For complex workflows (e.g., OPEN → CONFIRMED → FALSE_POSITIVE), it only applies the last
transition. The `unconfirm` transition is never used. Changelog-based replay is more robust.

---

### FEAT-03: No `--dry-run` mode
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

CloudVoyager supports `--dry-run` which extracts and generates mappings without submitting
any data, allowing users to validate configuration before an irreversible migration.

**Impact**: Users must do a full migration to test their config, which creates SC projects
that need manual cleanup.

---

### FEAT-04: No post-migration verification command
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

CloudVoyager has a `verify` command that compares issue/hotspot counts between SQ and SC and
flags discrepancies.

**Impact**: Users have no automated way to confirm migration success. They must manually
spot-check.

---

### FEAT-05: No CE submission retry logic
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/scanreport/submit.go](../go/internal/scanreport/submit.go)

`SubmitReport()` makes a single attempt with no retry on failure. CloudVoyager performs
2 attempts with 3 activity-log checks at 3-second intervals.

**Impact**: Transient network errors during CE submission cause the entire branch import to
fail rather than retrying.

---

### FEAT-06: No hotspot assignment sync
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_hotspotsync.go](../go/internal/migrate/tasks_hotspotsync.go)

`syncOneHotspot()` only changes status and migrates comments. CloudVoyager also syncs hotspot
assignee via user mappings.

---

### FEAT-07: No `--only` selective migration flag
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

CloudVoyager's `--only` flag lets users run only specific steps:
`scan-data`, `scan-data-all-branches`, `quality-gates`, `quality-profiles`,
`org-wide-resources`, `sync-issues`, `sync-hotspots`.

The migration tool has `--target_task` but it targets a single task by exact name, not a
semantic group. Users can't easily say "only sync issues, skip everything else."

---

### ~~FEAT-08a: No project version migration~~ **[FIXED]**
<!-- updated: 2026-06-04_15:30:00 -->

**Status**: FIXED — Issue #102. The `getProjectVersions` extract task fetches the current project version per branch via `/api/navigation/component`. During scan history import, the extracted version is passed to both the protobuf metadata and the CE submit form. Falls back to `"1.0.0"` if not available (matching CloudVoyager behavior). Harvested from CloudVoyager's `resolve-source-project-version.js`. Additionally, `resolveProjectVersion` normalizes the SonarQube sentinel string `"not provided"` (returned when no `sonar.projectVersion` is configured) to empty string, so the `"1.0.0"` fallback triggers correctly.

~~CloudVoyager resolves the source project version via `resolve-source-project-version.js`
and passes `sonar.projectVersion` to both the protobuf metadata and the CE submit form.
The migration tool did not extract or set the project version.~~

~~**Impact**: Projects migrated to SonarQube Cloud had no project version set, losing version
tracking metadata.~~

---

### FEAT-08: Incremental transfer mode absent
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

CloudVoyager supports an incremental mode that tracks state between runs and only processes
changed data. The migration tool supports resuming an interrupted run but has no concept of
"migrate only new data since last run."

---

### FEAT-09: `syncIssueMetadata` writes no output file (inconsistency with `syncHotspotMetadata`)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_issuesync.go](../go/internal/migrate/tasks_issuesync.go)

`runSyncIssueMetadata()` uses `TaskCounter` only — no per-project results file is written.
`runSyncHotspotMetadata()` writes `results.1.jsonl` with per-project counts.

**Impact**: Analysis reports show no detail for issue sync. `analysis_report` command cannot
show per-project issue sync stats.

**Fix**: Add `w.WriteOne(record)` in `syncProjectIssues()` mirroring `syncHotspotMetadata`'s
pattern.

---

### ~~FEAT-10: `--url` default silently targets production~~ **[FIXED]**
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Status**: FIXED — `--target-url` flag was added to the transfer command (renamed from `--sc-url` in #295). The default no longer silently targets production. Users can pass `--target-url https://sc-staging.io` for staging.

~~**File**: [go/internal/migrate/migrate.go:190](../go/internal/migrate/migrate.go#L190)~~

~~When `--url` is omitted and no config file provides a URL, `applyDefaults()` sets
`cfg.URL = "https://sonarcloud.io/"`. This silently targets production. There is no warning.~~

~~**Impact**: A user testing against staging who forgets `--url` sends a real migration to
production SonarCloud.~~

~~**Fix**: Log a `WARN` when falling back to the default URL: `"no --url specified, defaulting
to https://sonarcloud.io/ — pass --url to target a different instance"`.~~

---

## P3 — Internal Quality / Robustness Gaps
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

### BUG-12: `getActiveProfileRules` not included in scan-history-only extract
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/extract/planner.go](../go/internal/extract/planner.go)

`TargetTasksWithScanHistory()` adds: `getProjectIssuesFull`, `getProjectComponentTree`,
`getProjectSourceCode`, `getProjectSCMData`, `getProjectHotspotsFull` — but NOT
`getActiveProfileRules` or `getQualityProfiles`.

`loadExtractedActiveRules()` reads from `getActiveProfileRules`. If a user runs a
scan-history-only extract without also running a full extract first, active rules
data will be absent and all `pbActiveRules` will be empty.

**Impact**: Protobuf reports won't include active rules, causing CE to use default rules
— incorrect quality gate behavior.

**Fix**: Add `getActiveProfileRules` (and its dependency `getQualityRules`) to
`TargetTasksWithScanHistory()`.

---

### BUG-13: Analysis date always `time.Now()` instead of extraction timestamp
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_scanhistory.go:198](../go/internal/migrate/tasks_scanhistory.go#L198)

```go
now := time.Now()
// ...
metadata := scanreport.BuildMetadata(scanreport.MetadataInput{
    AnalysisDate: now,
```

CloudVoyager: `analysisDate: new Date(inst.data.metadata.extractedAt).getTime()`

**Impact**: The SC project shows "last analyzed: <migration date>" instead of the actual
last analysis date from SQ. Minor UX issue but misleading.

**Fix**: Store `extractedAt` timestamp in `extract.json` during extraction, read it during
migrate, and use it for `AnalysisDate`.

---

### BUG-14: Concurrent hotspot comment addition without per-comment delay
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_hotspotsync.go:303](../go/internal/migrate/tasks_hotspotsync.go#L303)

Comments are added sequentially within `syncOneHotspot()` with no inter-comment delay.
CloudVoyager adds a 100ms delay between comment additions.

**Impact**: Rapid comment additions may trigger SC rate limiting, causing some comments to
fail. Unlikely to be critical but may cause partial failures on projects with many hotspot
comments.

---

### ~~BUG-15: `toExtractedIssues` builds incorrect key for date lookup~~ **[FIXED]**
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Status**: FIXED — resolved as part of the BUG-01/BUG-04 fix (commits `e769b95` and `21d74e8`, branch `fix/history-creation-date-fix`, merged to main via PR #291). `extIssuesToExtracted()` was added as a helper that properly maps external issue creation dates, eliminating the date-map key mismatch.

~~Already covered in BUG-04 but worth noting: the function is defined but its date-map logic
has a second bug independent of BUG-04:~~

~~When looking up `dateMap[iss.RuleRepo+":"+iss.RuleKey]`, it maps issue creation dates to a
**rule key**, not an issue key. Many issues can share the same rule. The date map overwrites
on each iteration, so only the last issue's date is preserved per rule. This would cause
systematic wrong dates for projects with multiple issues of the same rule.~~

---

## Summary Table
<!-- updated: 2026-06-04_15:00:00 -->

| ID | Severity | Area | Description |
|----|----------|------|-------------|
| BUG-01 | ~~P0~~ **FIXED** | Scan History | ~~`BackdateChangesets` never called~~ Fixed in commits `e769b95`/`21d74e8` (PR #291) |
| BUG-02 | P0 | Scan History | `ActiveRuleInput` missing `ParamsByKey`, `CreatedAt`, `UpdatedAt`, `Impacts` |
| BUG-03 | ~~P0~~ **FIXED** | Scan History | ~~`ReferenceBranchName` never set in `MetadataInput`~~ Fixed in commit `1eeb9d8` |
| BUG-04 | ~~P1~~ **FIXED** | Scan History | ~~`toExtractedIssues` date lookup keyed on rule key, not issue key~~ Fixed in commits `e769b95`/`21d74e8` (PR #291) |
| BUG-05 | P1 | Issue Sync | `Assignee` loaded but never synced |
| BUG-06 | P1 | Issue+Hotspot Sync | No source-link comment added back to SQ |
| BUG-07 | P2 | Issue Sync | Issue comment format differs from CloudVoyager convention |
| BUG-08 | ~~P1~~ **FIXED** | Hotspot Sync | ~~`TO_REVIEW` hotspots not synced~~ Fixed (confirmed in TROUBLESHOOTING.md) |
| BUG-09 | P1 | Hotspot Sync | Metadata marker incompatible with CloudVoyager marker |
| BUG-10 | P1 | Scan History | No duplication data in protobuf report |
| BUG-11 | P1 | Scan History | No measures data in protobuf report |
| FEAT-01 | P1 | Issue+Hotspot Sync | No `user-mappings.csv` support |
| FEAT-02 | P1 | Extract | No changelog extraction for issues |
| FEAT-03 | P2 | CLI | No `--dry-run` mode |
| FEAT-04 | P2 | CLI | No post-migration verification command |
| FEAT-05 | P2 | Scan History | No CE submission retry logic |
| FEAT-06 | P2 | Hotspot Sync | No hotspot assignment sync |
| FEAT-07 | P2 | CLI | No `--only` selective migration flag |
| FEAT-08a | ~~P2~~ **FIXED** | Scan History | ~~No project version migration~~ Fixed: Issue #102, harvested from CloudVoyager's `resolve-source-project-version.js` |
| FEAT-08 | P3 | CLI | No incremental transfer mode |
| FEAT-09 | P2 | Issue Sync | `syncIssueMetadata` writes no per-project output file |
| FEAT-10 | ~~P2~~ **FIXED** | CLI | ~~`--url` default silently targets production~~ Fixed: `--target-url` flag added to transfer command (renamed from `--sc-url` in #295) |
| BUG-12 | P1 | Extract | `getActiveProfileRules` missing from scan-history-only extract |
| BUG-13 | P2 | Scan History | Analysis date is migration time, not extraction timestamp |
| BUG-14 | P3 | Hotspot Sync | No inter-comment delay for rate-limit protection |
| BUG-15 | ~~P1~~ **FIXED** | Scan History | ~~`toExtractedIssues` date map uses wrong key~~ Fixed in commits `e769b95`/`21d74e8` (PR #291) |
| BUG-16a | ~~P0~~ **FIXED** | Scan History | ~~Main branch not guaranteed first in multi-branch import~~ Fixed: `sortBranchesMainFirst()` |
| BUG-16b+c | ~~P0~~ **FIXED** | Scan History | ~~No CE gate between main and non-main branch imports~~ Fixed: two-phase import with CE wait |
| BUG-16d | ~~P1~~ **FIXED** | Scan History | ~~No branch filtering support~~ Fixed: `--exclude-branches` glob patterns |
| BUG-16e | ~~P1~~ **FIXED** | Scan History | ~~No per-branch checkpoint/resume~~ Fixed: `loadCompletedBranches()` + `shouldSkipBranch()` |
| BUG-16f | ~~P1~~ **FIXED** | Scan History | ~~Project-level concurrency not properly managed~~ Fixed: `errgroup.WithContext` + `SetLimit` |

---

## Recommended Fix Order
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

1. ~~**BUG-01**~~ — ~~Wire up `BackdateChangesets` + fix date key (BUG-04/BUG-15 simultaneously)~~ **DONE** (commits `e769b95`/`21d74e8`, PR #291)
2. ~~**BUG-03**~~ — ~~Set `ReferenceBranchName` in `importBranch`~~ **DONE** (commit `1eeb9d8`)
3. **BUG-02** — Extend `ActiveRuleInput` + `BuildActiveRules` with missing fields
4. **BUG-11** — Add `loadExtractedMeasures()` and populate `reportData.Measures`
5. **BUG-10** — Add duplication support to `ReportData` and `PackageReport`
6. **BUG-05 + FEAT-01** — Add user-mappings.csv + `syncIssueAssignment()`
7. **BUG-06** — Add source-link comments to both issue and hotspot sync
8. **FEAT-09** — Add per-project output file to `syncIssueMetadata`
9. **BUG-12** — Add `getActiveProfileRules` to scan-history extract task list
10. ~~**FEAT-10**~~ — ~~Add URL default warning~~ **DONE** (`--target-url` flag added to transfer command (renamed from `--sc-url` in #295))
11. **FEAT-05** — Add CE submission retry
12. **BUG-13** — Use extraction timestamp for analysis date
13. **FEAT-02** — Extract issue changelog data
14. **BUG-09** — Fix hotspot marker compatibility
15. **FEAT-04** — Add verification command
