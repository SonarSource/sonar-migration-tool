# Audit — global settings leak into per-project migration (`setProjectSettings`)

> **Status:** the six fixes for the settings-scope leak are implemented on branch
> `fix/project-settings-scope-rejection`, and fixes 1–2 plus the observability work are
> live-verified against SonarQube Cloud. Fix 3 is a diagnostic step for the customer's corpus, not
> code. The duplicate-gate merge (fix 5) is covered by unit tests and a committed fixture but was
> **not** exercised live — this dataset has no two source gates mapping to one cloud gate. See
> "Implementation" at the end for exactly what was and was not verified.

<!-- updated: 2026-08-28_00:00:00 -->

Triggered by a customer run (org `qbe-prod`, 1142 projects, tool `v1.1.0-SNAPSHOT`) that emitted
**42,048 identical warnings**. Code reviewed at `main` (`v1.2.0-SNAPSHOT`) — **all findings still
present**. Full write-up with log evidence lives outside this repo with the customer artifacts.

## Summary

<!-- updated: 2026-08-28_00:00:00 -->

`getProjectSettings` captures the source server's **global** settings table and `setProjectSettings`
re-writes it onto every project.

```
setProjectSettings items = 244,399 ; projects = 1,142
244,399 / 1,142 = 214.0096   ->   214 x 1142 = 244,388, excess 11
```

Every project carries the same 214 settings; ~11 genuine per-project overrides exist across the whole
estate. Of the 214: **49** are instance-scope-only on Cloud → `400 Setting 'X' cannot be set on a
Project`; the other **~165 succeed silently** and become permanent per-project overrides
(~188,000 estate-wide) that shadow any later org-level change.

## Findings

<!-- updated: 2026-08-28_00:00:00 -->

| # | Finding | Location |
| --- | --- | --- |
| 1 | **`filterNonInherited` is correct — do not change it.** `ValuesAction.setInherited(isDefault \|\| !isSet)` is byte-identical on branches 6.7→master, so a global value under a `component=` request *is* `inherited:true`. Verified live on 2026.3.1: 21,143 `inherited:true` vs 19 survivors across 86 projects. `"inherited":false` is never emitted (proto3 implicit presence), so absent = false and `ExtractBool` is right. `parentValue` is a **worse** signal: 145 inherited settings carry it, and 6 of 19 real overrides lack it. QBE's corpus is anomalous for another reason — see below. | [extract/tasks_projects.go:151-159](../../go/internal/extract/tasks_projects.go#L151-L159) |
| 2 | The `setProjectSettings` item loop has **no** scope / internal / SQS-only filter. `IsInternalSqsSetting`, `sqsOnlySettings`, `sqsOnlyPrefixes` all exist, name ~24 of the 49 keys, and are wired only into `setGlobalSettings`. | [migrate/tasks_associate.go:353-414](../../go/internal/migrate/tasks_associate.go#L353-L414) |
| 3 | On `hasDef == false` the global path skips with a Warn; the project path **POSTs anyway** via the extract-shape fallback. Same signal, opposite behaviour. | [migrate/tasks_settings_helpers.go:209-228](../../go/internal/migrate/tasks_settings_helpers.go#L209-L228) vs [tasks_setglobalsettings.go:733](../../go/internal/migrate/tasks_setglobalsettings.go#L733) |
| 4 | **Presence in `list_definitions` is not a settability test.** `SetAction.checkComponentQualifier` accepts *undeclared* keys on a project (`definition == null` → `SUPPORTED_QUALIFIERS.contains("TRK")`), and `hidden`/secured definitions are settable but unlisted — `sonar.links.scm` and `sonar.leak.period` are official `settings/set` examples in no definition list. The `Definition` proto has no `qualifiers`/`onQualifiers`/`scope` field on either product, so a blanket `!hasDef → skip` would drop real settings. Attempt-then-classify is the only sound test. | `SetAction`; [types/settings.go:28-33](../../lib/sq-api-go/types/settings.go#L28-L33) |
| 4b | `readCustomizedSQSGlobals` derives `defaultValue` from `list_definitions`, but 55 of 246 keys returned by `values` are absent from it (hidden/secured). Those get `defaultValue == ""` and are classified **"customized"** — how `sonaranalyzer-cs.*` and `sonar.cs.analyzer.dotnet.*` entered the set. It also lacks `IsInternalSqsSetting`/`partitionSQSOnlySettings` despite a comment claiming otherwise: a second latent leak path. | [tasks_settings_helpers.go:113](../../go/internal/migrate/tasks_settings_helpers.go#L113) |
| 5 | No `IsProjectLevelRejection` counterpart to `IsOrgLevelRejection`; `"cannot be set on a"` appears **0 times** in the repo. The first 400 teaches nothing, so 49 bad keys become 42,048 log lines. Match the stem only — the trailing word is the i18n label `qualifier.TRK=Project`. | [lib/sq-api-go/errors.go:110-116](../../lib/sq-api-go/errors.go#L110-L116) |
| 5b | Curated-list coverage of the 49 keys is **32/49**. 17 are covered by nothing: `sonar.authenticator.downcase`, `sonar.cpd.{abap,cobol}.minimum{Lines,Tokens}`, six `sonar.dbcleaner.*`, `sonar.filesize.limit`, `sonar.forceAuthentication`, `sonar.lf.*`, `sonar.sca.enabled`, `sonar.technicalDebt.developmentCost`. Reusing the lists alone stops 65%. | [tasks_setglobalsettings.go:72-190](../../go/internal/migrate/tasks_setglobalsettings.go#L72-L190) |
| 5c | Predict reports every uncurated global as green `applied` with detail *"Setting does not exist at global org level in SQC, will be applied for each project instead"* — it affirmatively promised the operation that produced the 400s. | [predict/global_settings.go:273-299](../../go/internal/predict/global_settings.go#L273-L299) |
| 6 | **`requests.log` has no producer.** The `projectBucketNearPerfect` matcher for `/api/settings/set` never fires because its only feeder, `collectProjectFailures`, opens `<runDir>/requests.log` — no code writes that file and no run directory on disk contains one. Seven report paths are dead: the **Failed** column in every section, the entire **Failure Ledger**, config partials, project failures, portfolio failures, #230 routing, and `final_analysis_report.csv`. The golden test fixture has a Failure Ledger only because the test synthesises the file. | [project_failures.go:77-81](../../go/internal/report/summary/project_failures.go#L77-L81) |
| 6b | No severity escalation exists. `TaskCounter.LogSummary` is hardcoded to `logger.Info` with no branch on `failed > 0`; per-item handlers `return nil`; `overall_status` is `"success"` and the exit code is 0. Exactly **three** `Error(` call sites exist in the non-test Go tree. `collect_runtime.go` decodes each event's `Level` and never reads it, so 42,181 WARNs yield a warning count of zero. | [helpers.go:527-541](../../go/internal/migrate/helpers.go#L527-L541) |
| 6c | `runPhase` starts an `errgroup` with **no `SetLimit`** — 13 phase-4 tasks × 25 = up to 325 in-flight — against a transport built as a bare `&http.Transport{}` literal: `MaxIdleConnsPerHost` defaults to 2, HTTP/2 disabled, no dial/TLS/response-header timeout, `Proxy` unset. Likely origin of the 36 `status=0` errors. | [migrate.go:462](../../go/internal/migrate/migrate.go#L462); [client.go:131](../../lib/sq-api-go/client.go#L131) |
| 6d | ALM: mapping HTTP 500 → "not bound" is **deliberate and correct** (#505, probed live), and `qbe-prod` genuinely is unbound. The real gaps are no binding pre-flight in `validateMigrateOrgs`, per-project re-logging of a per-org fact, 500 handled asymmetrically vs `getOrgRepos`, and `runMatchProjectRepos` registering no `TaskCounter` at all. | [tasks_alm.go:203-217,470](../../go/internal/migrate/tasks_alm.go#L203-L217) |
| 7 | `TestRunSetProjectSettingsDispatchesByShape` mounts an empty definitions registry and asserts the POSTs happen anyway — the test currently **codifies** finding 3. | [migrate/tasks_settings_test.go:91](../../go/internal/migrate/tasks_settings_test.go#L91) |

## Related defect — duplicate target quality gates

<!-- updated: 2026-08-28_00:00:00 -->

Same run, independent cause, accounts for all 16 `addGateConditions` failures.

`gates.csv` dedupes on the **source** org key ([structure/gates.go:70](../../go/internal/structure/gates.go#L70));
the CSV→JSONL load then joins source org → **cloud** org many-to-one
([migrate/helpers.go:405-411](../../go/internal/migrate/helpers.go#L405-L411)) with no re-dedupe. N rows
share one `cloud_gate_id` and fan out 25-wide, so N workers each run
`show → delete-all → create-all` on the same gate
([tasks_configure.go:326-352](../../go/internal/migrate/tasks_configure.go#L326-L352)). One wins every
race; the rest get `404` on delete and `400 already exists` on create.

Reproduces on committed fixtures: `files/2026-08-18-02/` — `🥉 3 - Corp base` appears 8× with
`cloud_gate_id 49476`; `run_events.jsonl` shows 23 × 404 and 28 × create failures in 2.1 s.

`forEachMigrateItemSerial` ([helpers.go:264-276](../../go/internal/migrate/helpers.go#L264-L276)) already
solves exactly this for `createProfiles` (#338), and the report layer already dedupes the same
duplication (#165, [report/summary/collect.go:685](../../go/internal/report/summary/collect.go#L685)) —
the migration path does not.

`addGateConditions` is also the only create-style task with no `IsAlreadyExists` tolerance
([errors.go:84-93](../../lib/sq-api-go/errors.go#L84-L93)).

### `Conversion = ')'` is not a data bug

The lone anomalous error, on `new_duplicated_lines_density`, is the **same** "already exists" 400.
SonarCloud renders that message with the metric display name `Duplicated Lines on New Code (%)`; the
`%)` reaches a server-side `String.format`, which throws `UnknownFormatConversionException:
Conversion = ')'`. No malformed threshold was ever sent. Worth reporting upstream to SonarQube Cloud.

## Recommended changes

<!-- updated: 2026-08-28_00:00:00 -->

1. **SDK (do first):** add `IsProjectLevelRejection` matching `"cannot be set on a"`, memoise rejected
   keys in `runSetProjectSettings`, and log once per key with a project count. Sound regardless of
   definition coverage; turns 55,958 doomed requests into 49. Also treat `already exists` **and** the
   literal `Conversion = ')'` as idempotent no-ops on `create_condition`.
2. **Migrate:** apply `IsInternalSqsSetting`, `resolveSQSOnlyHandler` and `IsSettingCustomized` on the
   project path (covers 32/49 without a request), and add the same prefix to `readCustomizedSQSGlobals`.
   Update the test in finding 7.
3. **Extract:** leave `filterNonInherited` alone. Diagnose the customer corpus instead —
   `GET api/settings/values?component=<project>` on their source: `inherited` present ⇒ their
   `migration-data` was not produced by this extract task; `inherited` absent with `parentValue` present
   ⇒ their database genuinely holds ~214 project-scope rows per project.
   **The first branch is the likely one.** Measured on five live instances (Server 9.9.8 / 10.7.0 /
   2026.5, CB 26.6, SonarCloud 8.0), `?component=` returns the full effective set — component count
   equals global count ±1 — with nearly all records `inherited: true`, so the filter reduces N to ~1
   (102→1, 122→1, 258→0, 167→1, 203→1). QBE's extract kept 214 of ~214: the filter removed nothing.
4. **Migrate:** fix the `defaultValue == ""` misclassification in `readCustomizedSQSGlobals` — treat a
   key missing from `list_definitions` as *unknown*, not as *default is empty*.
5. **Gates:** dedupe by `cloud_gate_id` after the org join, or use `forEachMigrateItemSerial`; tolerate
   `already exists` and 404-on-delete; add `gate` + threshold to the `addGateConditions failed` log.
6. **Reporting:** write `requests.log` (or re-point the seven consumers at `run_events.jsonl`) — until
   then the Failed bucket and Failure Ledger are dead code. Escalate a 100 %-failure task above `WARN`
   and out of `overall_status: success`.
7. **Concurrency:** give `runPhase` a `SetLimit`, and clone `http.DefaultTransport` instead of building
   a bare `&http.Transport{}`.
8. **ALM:** add a binding pre-flight to `validateMigrateOrgs`; aggregate the per-org verdict into one
   log line; register a `TaskCounter` in `runMatchProjectRepos`.

## Implementation
<!-- updated: 2026-08-28_09:15:00 -->

Branch `fix/project-settings-scope-rejection`. Build, `go vet` and both test suites green;
`gofmt` clean on every changed file.

| Fix | Change | Where |
| --- | --- | --- |
| 1 | `IsProjectLevelRejection` matching the invariant stem `"cannot be set on a"`, plus a per-key `settingKeyTally` memo so a rejected key is abandoned for the remaining projects and logged **once** with a project count | [errors.go](../../lib/sq-api-go/errors.go), [tasks_associate.go](../../go/internal/migrate/tasks_associate.go), [tasks_settings_helpers.go](../../go/internal/migrate/tasks_settings_helpers.go) |
| 2 | `skipProjectSettingKey` applies `IsInternalSqsSetting` + `resolveSQSOnlyHandler` on the project path, and on `readCustomizedSQSGlobals` (the second leak path) | [tasks_settings_helpers.go](../../go/internal/migrate/tasks_settings_helpers.go) |
| 3 | `warnIfProjectSettingsLookLikeGlobals` pre-flight detects the leaked-globals signature (high, near-uniform records/project) before any request | [tasks_settings_helpers.go](../../go/internal/migrate/tasks_settings_helpers.go) |
| 4 | `readCustomizedSQSGlobals` now skips `inherited` records (at global scope that means "using the default") and refuses to treat a missing definition as `defaultValue == ""` | [tasks_settings_helpers.go](../../go/internal/migrate/tasks_settings_helpers.go) |
| 5 | `mergeGateRecordsByCloudGate` folds all records targeting one cloud gate into one unit of work (union of conditions, OR-ed `was_preexisting`), via a new `forEachMigrateItemTransformed`; `already exists`, the mangled `Conversion = ')'`, and 404-on-delete are treated as idempotent; `gate`/`op`/`threshold` added to the failure log | [tasks_configure.go](../../go/internal/migrate/tasks_configure.go), [helpers.go](../../go/internal/migrate/helpers.go), [errors.go](../../lib/sq-api-go/errors.go) |
| 6 | `requests.log` producer (transport hook + buffered writer, secrets redacted); `LogSummary` escalates to WARN on any failure and ERROR at 100%; `runPhase` gains `SetLimit`; `http.DefaultTransport` is cloned so `MaxIdleConnsPerHost` and HTTP/2 are correct; `warnUnboundOrgs` ALM pre-flight | [requestlog.go](../../lib/sq-api-go/requestlog.go), [requestlog.go](../../go/internal/migrate/requestlog.go), [helpers.go](../../go/internal/migrate/helpers.go), [migrate.go](../../go/internal/migrate/migrate.go), [client.go](../../lib/sq-api-go/client.go), [org_mapping.go](../../go/internal/migrate/org_mapping.go) |

Also removed a pre-existing duplicated block in `go/internal/extract/extract_test.go` that made the
whole extract package fail to compile under `go test`.

### Two deliberate non-changes

- **`filterNonInherited` is untouched.** Measured on the live source (SonarQube Server 2026.3.1):
  `?component=<project>` returns 246 settings and **all 246 carry `inherited: true`**, so the filter
  correctly yields **zero** project records. Changing it would have broken real overrides.
- **The exit code and `overall_status` are unchanged.** A 100%-failed task now logs at ERROR, but the
  run still exits 0. Making a failed phase fail the process is a breaking change for pipelines that
  currently succeed, so it needs product sign-off.

### Live verification
<!-- updated: 2026-08-28_09:15:00 -->

Source `localhost:9000` (SonarQube Server 2026.3.1, 86 projects) → target `sc-staging.io`, org
`open-digital-society-1`.

Driving `setProjectSettings` against live SonarCloud with a QBE-shaped corpus (13 leaked-global
records for one project):

- 7 curated keys skipped with **zero** API calls (`reason=internal-sqs-setting` / `reason=sqs-only`)
- 5 uncurated global-only keys each attempted **exactly once**, classified by the real
  `400 Setting 'X' cannot be set on a Project`, then abandoned — one log line each
- 1 genuine setting (`sonar.exclusions`) applied
- `level=WARN msg="task summary" task=setProjectSettings succeeded=1 failed=5 total=6`
- target project non-inherited settings went 21 → 22 (only the intended probe) — **zero** global-only
  keys landed; probe reset afterwards, org left as found

Other fixes confirmed on live runs: `requests.log` produced (79 entries, 61 success / 18 failure, all
5 settings 400s captured with their keys, no secrets); `level=ERROR msg="task summary"
task=createProjects succeeded=0 failed=1` (previously INFO); ALM pre-flight fired for the unbound
`free-org-josh` before any work and stayed silent for the GitHub-bound `open-digital-society-1`;
`addGateConditions` cleared each gate once with **zero** 404-on-delete; no `status=0` errors.

**Not exercised live:** the duplicate-gate merge, because this dataset has no two source gates mapping
to one cloud gate. It is covered by unit tests and reproduces on the committed fixture
`files/2026-08-18-02/`. Project creation could not be exercised either — every org on this staging
instance currently refuses private projects, and the tool hardcodes `visibility: "private"`
([tasks_create.go:76](../../go/internal/migrate/tasks_create.go#L76)), which also blocks the
already-exists path because SonarCloud validates visibility before existence.

## Failure classification
<!-- updated: 2026-08-28_09:40:00 -->

Added after the fixes above, because the investigation kept hitting the same wall: a migration says
`failed=42048` and nothing says whether that is the tool being wrong or SonarQube Cloud legitimately
refusing something it cannot do.

`ClassifyFailure` ([failure_class.go](../../go/internal/migrate/failure_class.go)) returns one of four
classes plus a plain-language `why` and a `remediation`:

| Class | Meaning |
| --- | --- |
| `by-design` | Cloud cannot do this and never will. Working as intended. |
| `already-done` | The desired end state already holds. |
| `customer-environment-issue` | Permissions, subscription/quota, rate limiting, connectivity, TLS/DNS, 5xx. |
| `bug` | Rejected for an unrecognised reason — the payload is probably wrong. Reportable. |

An unrecognised 400 is deliberately a **bug**, not a shrug. Account-state messages (private projects not
permitted, org not bound, plan limits) are matched first so nobody chases a defect that is really a
subscription problem. Both `sqapi.APIError` and `common.HTTPError` normalize to the same verdict.

Severity follows the cause, not the count: a bug or a task that achieved nothing is ERROR; expected
limitations stay WARN however many there are. `TaskCounter` reports the per-cause breakdown
(`failed_by_design`, `failed_already_done`, `failed_customer_environment_issue`, `failed_bugs`,
`failed_unclassified`).

`logAPIWarn` carries the classification, which lifted all ~157 existing failure sites with no opt-in.
The 62 sites that paired `counter.Fail()` with `logAPIWarn(..., err, ...)` now go through one
`failAPI()` call — that pair had already drifted in a live run, logging an item as
`failure_class=customer-environment-issue` inside a summary claiming `failed_by_design=1`. `Fail()` keeps a separate
`unclassified` bucket rather than being folded into `by-design`, so the breakdown never claims a cause
it was not told.

Live-verified: by-design on the settings rejections, customer-environment-issue on the project-creation refusal, and
the per-item class matching the summary breakdown in both cases.
