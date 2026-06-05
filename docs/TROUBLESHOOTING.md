# Troubleshooting
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

This guide covers common issues when using the SonarQube to SonarCloud migration tool.

---

## Finding Logs
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- **Request logs**: `./files/<extract_id>/requests.log` -- Detailed log of every HTTP request and response. Check here first when diagnosing issues.
- **Analysis report**: Run `sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/` to generate a CSV summary of a migration run.

---

## Common Errors
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

### "Token does not have sufficient permissions"
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Cause**: The admin token does not have the required permissions.

**Solution**:

- **SonarQube Server** token needs: Administer System, Quality Gates, Quality Profiles
- **SonarCloud** token needs: Enterprise admin privileges, Organization admin for all target organizations

---

### "Organization not found"
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Cause**: The `sonarcloud_org_key` in `organizations.csv` does not match any organization in SonarCloud.

**Solution**:

1. Log in to SonarCloud and verify the organization exists.
2. Check for typos in the org key.
3. Make sure the organization is part of your SonarCloud enterprise.

---

### "Request timeout"
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Cause**: Large datasets or slow network can exceed the default timeout.

**Solution**: Increase the timeout:

```bash
sonar-migration-tool extract <URL> <TOKEN> --timeout 120 --export_directory ./files/
```

---

### "Connection refused" or SSL errors
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Cause**: Network connectivity issues or certificate problems.

**Solution**:

1. Verify the SonarQube URL is accessible (try opening it in a browser or using `curl`).
2. For self-signed certificates, use mTLS options:

```bash
sonar-migration-tool extract <URL> <TOKEN> \
  --pem_file_path ./certs/client.pem \
  --key_file_path ./certs/client.key \
  --export_directory ./files/
```

---

### Migration task fails midway
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Cause**: API error, rate limiting, or network interruption.

**Solution**: Resume using the `--run_id` flag:

```bash
sonar-migration-tool migrate <TOKEN> <ENTERPRISE_KEY> --run_id <RUN_ID> --export_directory ./files/
```

Completed tasks are skipped automatically.

---

### No Projects Extracted
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- Verify your token has admin-level permissions.
- Confirm projects exist in your SonarQube instance.
- Review `requests.log` for API errors.

---

### Authentication Errors
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- Verify tokens are valid and not expired.
- **SonarQube Server < 10**: Uses Basic authentication.
- **SonarQube Server >= 10**: Uses Bearer token authentication.
- SonarCloud tokens must belong to a user with enterprise admin permissions.

---

### API Rate Limiting
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

Reduce concurrency and increase timeout:

```bash
sonar-migration-tool extract <URL> <TOKEN> --concurrency 5 --timeout 120 --export_directory ./files/
```

---

## Reducing Memory Usage
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

For large instances (50,000+ projects), lower the concurrency:

```bash
sonar-migration-tool extract <URL> <TOKEN> --concurrency 10 --export_directory ./files/
```

---

## Resetting After a Bad Migration
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

Use the `reset` command to delete all migrated content in the SonarCloud enterprise:

```bash
sonar-migration-tool reset <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/
```

**Warning: This is destructive.** It deletes all migrated projects, quality profiles, quality gates, and organization configurations.

---

## CE Task "Issue whilst processing the report" (importScanHistory)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

When `importScanHistory` CE tasks fail on SonarCloud with "There was an issue whilst processing the report", the following causes have been identified:

### ROOT CAUSE: Go ZIP Data Descriptors (FIXED)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Symptom**: All CE tasks fail within ~150ms with "There was an issue whilst processing the report" and `hasScannerContext: false`.

**Root cause**: Go's `archive/zip` `Writer.Create()` uses *streaming mode* by default — it sets the data descriptor flag (bit 3 / 0x0008) in the local file header and places CRC32 and sizes AFTER the file data. SonarCloud's Compute Engine (Java) uses `java.util.zip.ZipInputStream`, which cannot parse ZIP entries that use the `Store` method combined with data descriptors. However, Java's ZIP parser *does* handle data descriptors correctly for `Deflate`-compressed entries.

**Evidence chain**:
1. Real sonar-scanner report → CE SUCCESS (1255ms)
2. Real scanner files re-zipped with `zip` command → CE parsed it (hasScannerContext: true)
3. Real scanner files re-zipped with Go `zip.Writer` (`zip.Store` + `zw.CreateRaw(fh)`) → CE FAILED (hasScannerContext: false), even with correct CRC32 and sizes pre-computed in the local header
4. Go `zip.Writer` with `zip.Deflate` + `zw.CreateHeader(fh)` → CE SUCCESS

**Fix**: Changed `addBytes()` in `packager.go` to use `zip.Deflate` compression method with `zw.CreateHeader(fh)` instead of `zip.Store` with `zw.CreateRaw(fh)`. The `Store` + `CreateRaw` approach was attempted first (pre-computing CRC32 and sizes to avoid data descriptors) but CE still failed. The working fix uses `Deflate` + `CreateHeader`, which lets Go's zip writer handle compression and emit data descriptors — Java's `ZipInputStream` handles data descriptors correctly for Deflate-compressed entries, just not for Store entries.

**Verification**: After the fix, CE errors changed from the generic "issue processing the report" to legitimate business logic errors (e.g., "no matching quality profile for language 'js'"), confirming the ZIP is now parsed correctly.

### Known Differences from CloudVoyager (Informational)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

These differences have been investigated. They do NOT cause CE processing failures but represent areas of reduced fidelity:

1. **Component filtering**: Our code filters to only include components WITH source code (`filterComponentsWithSource` in `tasks_scanhistory.go`). CloudVoyager includes ALL FIL components even without source. If many components lack source, this could produce a much smaller component set.

2. **ActiveRule fields**: Our `BuildActiveRules` only sets `RuleRepo`, `RuleKey`, `Severity`, and `QProfileKey`. CloudVoyager also sets `ParamsByKey`, `CreatedAt`, `UpdatedAt`, and `Impacts`. Our proto schema supports all these fields but we don't populate them.

3. **Duplications**: CloudVoyager includes `duplications-{ref}.pb` files. We do not include any duplication data.

4. **Analysis date**: We use `time.Now()`. CloudVoyager uses the extraction timestamp. Unlikely to cause failures.

5. ~~**Issue date backdating (BackdateChangesets)**~~: **RESOLVED.** Creation dates are now preserved for both native and external issues via `BackdateChangesets` wiring and `IssueInput.CreationDate` / `ExternalIssueInput.CreationDate` fields. See "Issue Date Preservation (FIXED)" section above.

### Branch Name Mapping (FIXED)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Symptom**: CE fails with "Invalid branch type 'SHORT'. Branch 'main' already exists with type 'LONG'" when the SQ main branch name differs from the SC main branch name.

**Root cause**: The migration tool was using the SQ branch name (`main`) in the protobuf metadata and CE submit, but the SC project's main branch was named `master`.

**Fix**: Added CloudVoyager-pattern branch name mapping in `tasks_scanhistory.go`:
1. `collectBranchInfo()` now returns `branchInfo` structs with `IsMain` flag (from SQ extracted data)
2. Before importing, queries SC via `e.Cloud.Branches.List()` to discover the actual SC main branch name
3. Uses the SC main branch name in the protobuf metadata (`BranchName`) and CE submit (`characteristic=branch=...`), while keeping the SQ branch name for filtering extracted data (issues, components, sources)

### "Component has been deleted by end-user during analysis"
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Symptom**: ALL CE tasks fail with "Component has been deleted by end-user during analysis", even for projects not involved in migration.

**Root cause**: This is a SonarCloud staging environment issue, not a code issue. All projects in the organization entered a soft-deleted state. The projects still appear via the search API but CE treats them as deleted.

**Solution**: Re-create the projects on the SC staging environment, or use a fresh organization/enterprise. This error is NOT caused by our ZIP format, protobuf content, or submission logic.

### Issue Date Preservation (FIXED)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Symptom**: All migrated issues appear introduced at migration time; SonarCloud new-code-period logic treats all historical issues as new.

**Root cause** (two bugs, both required fixing):

1. `BackdateChangesets` existed in `scanreport/backdate.go` but was never called from `importBranch()` — dead code.
2. `toExtractedIssues()` built a date-map keyed by issue key (e.g. `AQAB2...`) but looked it up by `ruleRepo:ruleKey` (e.g. `python:S1234`) — mismatched keys meant every `CreationDate` was zero.

**Fix**:
1. Added `Key string` and `CreationDate time.Time` fields to `IssueInput` and `ExternalIssueInput` in `builder.go`.
2. `loadExtractedIssues()` and `loadExtractedHotspots()` now populate these from the raw JSON (`key` and `creationDate` fields).
3. `toExtractedIssues()` simplified — reads `Key` and `CreationDate` directly from `IssueInput` (no second extract re-read).
4. `importBranch()` now builds a component-key-keyed alias map and calls `BackdateChangesets(extracted, changesetsByKey, now)` after building changesets.

**Verification**: After fix, migrated issues on SC show their original SonarQube creation dates.

### Issue Comment Deduplication (FIXED)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Symptom**: If `syncIssueMetadata` ran multiple times (e.g., first run had a tag-sync failure), user comments would be duplicated on the Cloud issue.

**Root cause**: `syncIssueComments()` had no idempotency check. If comments were added successfully but the final `syncIssueTags()` call failed, the `metadata-synchronized` tag was never set, causing the pair to be retried. On retry, comments were added again.

**Fix**: Added `isAlreadyMigratedIssueComment()` in `tasks_issuesync.go` — mirrors the hotspot pattern. Before adding a comment, it checks whether a Cloud comment with identical text already exists. The `cloudComments` field from `pair.cloud` is passed to `syncIssueComments()` for this check.

### Hotspot TO_REVIEW Comment Sync (FIXED)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

**Symptom**: User comments on TO_REVIEW hotspots were not migrated.

**Root cause**: `buildHotspotPairs()` filtered to only `REVIEWED` hotspot pairs, discarding all TO_REVIEW hotspots even when they had user comments.

**Fix**: Updated filter condition to include any pair that needs status sync (source is REVIEWED) OR needs comment sync (source has any comments), regardless of status.

### Resolved Issues
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- **ReferenceBranchName**: Previously not set in `MetadataInput`. Now set correctly, defaulting to `BranchName` — matches CloudVoyager behavior.
- **ZIP data descriptors**: Fixed via `Deflate` + `CreateHeader` (see root cause above).
- **Branch name mapping**: Fixed to query SC main branch name (see above).
- **Issue date preservation**: Fixed via `BackdateChangesets` wiring + `IssueInput.CreationDate` (see above).
- **Issue comment deduplication**: Fixed via `isAlreadyMigratedIssueComment` idempotency guard (see above).
- **Hotspot TO_REVIEW comment sync**: Fixed via inclusive actionable-pair filter (see above).

### Confirmed NON-issues
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- **ZIP entry names**: Our code uses `external-issues-{ref}.pb` (with hyphens) at `packager.go`. CloudVoyager's working `report-packager` also uses hyphens. These match.
- **Submit endpoint**: Both use `/api/ce/submit`.
- **Multipart form structure**: Both use the same form fields: `report`, `projectKey`, `organization`, `characteristic` (branch/branchType), and `properties`.
- **context-props.pb**: Both include an empty `context-props.pb`.
- **Auth method**: `sqco_` tokens require Bearer auth (not Basic). Our `authTransport` correctly uses Bearer.
- **Metadata fields**: Comprehensive comparison against CloudVoyager's `build-metadata.js` shows all fields match: analysisDate (epoch ms), organizationKey, projectKey, rootComponentRef, branchName, branchType (BRANCH=1), referenceBranchName, scmRevisionId (random 40-char hex), projectVersion ("1.0.0"), qprofilesPerLanguage, analyzedIndexedFileCountPerType.
- **Protobuf content**: Issue, ExternalIssue, AdHocRule, ActiveRule, Component, and Changesets messages all use correct field numbers per the proto schema. Length-delimited encoding (varint prefix) matches the Java CE parser expectations.
- **TextRange zero-offset encoding (NOT a rejection cause)**: A decode-diff of `/tmp/reportdiff/cv` (accepted) vs `/tmp/reportdiff/ours` (rejected) showed CV serializes `start_offset=0` explicitly (`18 00`) for 193 external issues, while ours omits field 3 (implicit-presence default). This is **byte-difference only, decode-equivalent, and harmless**. SonarSource's canonical `scanner_report.proto` (verified on `SonarSource/sonarqube` branches 10.6/10.7) is `syntax = "proto3"` with `message TextRange { int32 start_offset = 3; ... }` — plain `int32`, NOT proto2 `optional`. Our `scanner-report.proto:289-294` matches it exactly. Under proto3 implicit presence, the CE's reader cannot distinguish "field absent" from "field present == 0"; `TextRange.getStartOffset()` returns `0` in both cases. Changing our proto to `optional int32` would diverge from the canonical SonarSource schema and change nothing the CE observes. The same-issue side-by-side (ruff/D104) confirmed every other field is byte-identical.

### Measures Files Absent (data-fidelity gap, NOT a rejection cause)
<!-- updated: 2026-06-05_12:30:00 by Claude -->

The CV report ships 143 `measures-{ref}.pb` files (aggregate metrics: `reliability_rating`, `security_rating`, `sqale_rating`, `ncloc`, `complexity`, `coverage`, `line_coverage`, `cognitive_complexity`, `branch_coverage`, `code_smells`, `sqale_index`, `violations`, plus a few `bugs`/`security_hotspots`/`vulnerabilities`). Our report ships **zero** because `tasks_scanhistory.go:310` sets `Measures: make(map[int32][]*pb.Measure)` and never calls `scanreport.BuildMeasures` (the builder exists at `builder.go:306-320` but is unwired). This does **NOT** cause the CE rejection: `ScannerReportReader.readComponentMeasures` guards with `if (fileExists(file))` and returns `emptyCloseableIterator()` when a `measures-N.pb` is missing — missing measures are explicitly tolerated and the CE recomputes aggregates server-side. This is tracked separately as issue #106 / PLAN-FIX-106 (measure fidelity), and is the correct place to wire `BuildMeasures` using the CV `buildMeasures` logic (per-FIL-qualifier component, typed value via int/long/double/string detection).
- **CV redundant filenames (`externalissues-N.pb` no-dash, `adhoerules.pb` typo)**: CV's known-good zip contains BOTH naming conventions because two CV uploader code paths run (`add-protobuf-files.js` emits the no-dash/typo names; `add-source-and-ext-files.js`/`add-optional-files.js` emit the canonical `external-issues-N.pb`/`adhocrules.pb`). The SonarQube CE `FileStructure.Domain` reads only the canonical hyphenated `external-issues-` and the `adHocRules()` standalone `adhocrules.pb`. The no-dash/typo files are dead weight the CE ignores; ours correctly omits them. Not a difference that matters.

---

## Branch Migration Ordering and Failures
<!-- updated: 2026-06-05_19:20:00 -->

When `importScanHistory` migrates a project with multiple branches, the main branch is imported first (its CE task is awaited) so the project is established, then each **non-main** branch is migrated as a **long-lived branch with its full issue history**. Before uploading a non-main branch's report, the tool performs SonarQube Cloud's "Create analysis" handshake (`POST {api-host}/analysis/analyses`) to register the branch and obtain an analysis id, which it embeds in the report (`metadata.analysis_uuid`) so the CE binds the issues to that branch. Without the handshake the CE accepts the report (task SUCCESS) but never creates the branch.

### Main branch first; non-main branches migrate as long-lived

The tool sorts branches main-first, imports the main branch and waits for CE SUCCESS, then imports each non-main branch (each preceded by the create-analysis handshake). If the main branch CE task fails, the remaining branches are skipped. Every migrated branch is registered as **long-lived** so SonarQube Cloud's automatic pruning of short-lived branches (after ~30 days) never discards migrated history.

**Symptom**: A non-main branch CE task fails with "Invalid branch type 'SHORT'. Branch '\<name\>' already exists with type 'LONG'."

**Cause / fix**: The tool requests `branchType=long` for every migrated branch, so a fresh target avoids this. It can still occur if the **target branch already exists with a conflicting type** from an earlier/partial run — delete that branch on SonarQube Cloud (`POST /api/project_branches/delete?project=<key>&branch=<name>`) and re-run.

**Symptom**: A non-main branch is reported as `skipped: source code not retrievable ...`.

**Cause**: The source server no longer has that branch's source text (purged by housekeeping for an inactive branch — line measures may remain). Re-analyze the branch on the source server to restore its source, then re-run.

### Excluding branches from migration

Use the `--exclude_branches` flag (or `exclude_branches` in the JSON config) to skip specific non-main branches during scan history import. This accepts glob patterns compatible with Go's `filepath.Match`:

```bash
# Exclude all feature branches and release branches
sonar-migration-tool migrate ... --exclude_branches "feature/*" --exclude_branches "release/*"

# Or in config.json
{
  "target": {
    "exclude_branches": ["feature/*", "release/*"]
  }
}
```

The main branch is **never** excluded, regardless of patterns. See [ADVANCED-CONFIG.md](ADVANCED-CONFIG.md) for the full config reference.

### Resuming after a branch failure

The tool tracks per-branch completion status. When resuming a failed migration with `--run_id`, branches that already succeeded are automatically skipped. Only failed or not-yet-attempted branches are retried.

### Project-level parallelism

Multiple projects are imported in parallel (bounded by concurrency). A failure in one project does not cancel or affect other projects — each project's branches are processed independently.

---

## Getting Help
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

1. Check `files/*/requests.log` for detailed error information.
2. Generate an analysis report: `sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/`
3. Search the [repository issues](https://github.com/sonar-solutions/sonar-migration-tool/issues).
4. Open a new issue with: the command you ran (redact tokens), the full error message, and relevant log excerpts.
