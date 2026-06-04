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

### ~~BUG-03: `ReferenceBranchName` never set in `MetadataInput` inside `importBranch`~~ **[FIXED]**
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Status**: FIXED — commit `1eeb9d8`. `MetadataInput` now includes `ReferenceBranchName`; `BuildMetadata` sets it on the protobuf `Metadata` message, defaulting to `BranchName` if not explicitly provided. This matches CloudVoyager's behavior and resolves the CE processing rejection.

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
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**File**: [go/internal/migrate/tasks_scanhistory.go:218](../go/internal/migrate/tasks_scanhistory.go#L218)

`reportData.Measures` is initialized as `make(map[int32][]*pb.Measure)` (empty map) and
never populated. There is no `loadExtractedMeasures()` function.

**CloudVoyager**: builds measures from `buildMeasures()` covering lines, complexity, coverage,
issues, bugs, etc.

**Impact**: All code metrics (lines of code, coverage, complexity, etc.) in SC will be zero
or absent after migration — only live scans will populate them.

---

## P2 — Missing Features (Present in CloudVoyager, Absent Here)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

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

`loadExtractedActiveRules()` reads from `getActiveProfileRules`. If a user runs
`extract --include_scan_history` without also running a full extract first, active rules
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
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

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
| FEAT-08 | P3 | CLI | No incremental transfer mode |
| FEAT-09 | P2 | Issue Sync | `syncIssueMetadata` writes no per-project output file |
| FEAT-10 | ~~P2~~ **FIXED** | CLI | ~~`--url` default silently targets production~~ Fixed: `--target-url` flag added to transfer command (renamed from `--sc-url` in #295) |
| BUG-12 | P1 | Extract | `getActiveProfileRules` missing from scan-history-only extract |
| BUG-13 | P2 | Scan History | Analysis date is migration time, not extraction timestamp |
| BUG-14 | P3 | Hotspot Sync | No inter-comment delay for rate-limit protection |
| BUG-15 | ~~P1~~ **FIXED** | Scan History | ~~`toExtractedIssues` date map uses wrong key~~ Fixed in commits `e769b95`/`21d74e8` (PR #291) |

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
