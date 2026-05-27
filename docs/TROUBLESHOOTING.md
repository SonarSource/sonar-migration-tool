# Troubleshooting

This guide covers common issues when using the SonarQube to SonarCloud migration tool.

---

## Finding Logs

- **Request logs**: `./files/<extract_id>/requests.log` -- Detailed log of every HTTP request and response. Check here first when diagnosing issues.
- **Analysis report**: Run `sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/` to generate a CSV summary of a migration run.

---

## Common Errors

### "Token does not have sufficient permissions"

**Cause**: The admin token does not have the required permissions.

**Solution**:

- **SonarQube Server** token needs: Administer System, Quality Gates, Quality Profiles
- **SonarCloud** token needs: Enterprise admin privileges, Organization admin for all target organizations

---

### "Organization not found"

**Cause**: The `sonarcloud_org_key` in `organizations.csv` does not match any organization in SonarCloud.

**Solution**:

1. Log in to SonarCloud and verify the organization exists.
2. Check for typos in the org key.
3. Make sure the organization is part of your SonarCloud enterprise.

---

### "Request timeout"

**Cause**: Large datasets or slow network can exceed the default timeout.

**Solution**: Increase the timeout:

```bash
sonar-migration-tool extract <URL> <TOKEN> --timeout 120 --export_directory ./files/
```

---

### "Connection refused" or SSL errors

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

**Cause**: API error, rate limiting, or network interruption.

**Solution**: Resume using the `--run_id` flag:

```bash
sonar-migration-tool migrate <TOKEN> <ENTERPRISE_KEY> --run_id <RUN_ID> --export_directory ./files/
```

Completed tasks are skipped automatically.

---

### No Projects Extracted

- Verify your token has admin-level permissions.
- Confirm projects exist in your SonarQube instance.
- Review `requests.log` for API errors.

---

### Authentication Errors

- Verify tokens are valid and not expired.
- **SonarQube Server < 10**: Uses Basic authentication.
- **SonarQube Server >= 10**: Uses Bearer token authentication.
- SonarCloud tokens must belong to a user with enterprise admin permissions.

---

### API Rate Limiting

Reduce concurrency and increase timeout:

```bash
sonar-migration-tool extract <URL> <TOKEN> --concurrency 5 --timeout 120 --export_directory ./files/
```

---

## Reducing Memory Usage

For large instances (50,000+ projects), lower the concurrency:

```bash
sonar-migration-tool extract <URL> <TOKEN> --concurrency 10 --export_directory ./files/
```

---

## Resetting After a Bad Migration

Use the `reset` command to delete all migrated content in the SonarCloud enterprise:

```bash
sonar-migration-tool reset <TOKEN> <ENTERPRISE_KEY> --export_directory ./files/
```

**Warning: This is destructive.** It deletes all migrated projects, quality profiles, quality gates, and organization configurations.

---

## CE Task "Issue whilst processing the report" (importScanHistory)
<!-- updated: 2026-05-27_18:00:00 -->

When `importScanHistory` CE tasks fail on SonarCloud with "There was an issue whilst processing the report", the following causes have been identified:

### ROOT CAUSE: Go ZIP Data Descriptors (FIXED)
<!-- updated: 2026-05-27_20:00:00 -->

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

These differences have been investigated. They do NOT cause CE processing failures but represent areas of reduced fidelity:

1. **Component filtering**: Our code filters to only include components WITH source code (`filterComponentsWithSource` in `tasks_scanhistory.go`). CloudVoyager includes ALL FIL components even without source. If many components lack source, this could produce a much smaller component set.

2. **ActiveRule fields**: Our `BuildActiveRules` only sets `RuleRepo`, `RuleKey`, `Severity`, and `QProfileKey`. CloudVoyager also sets `ParamsByKey`, `CreatedAt`, `UpdatedAt`, and `Impacts`. Our proto schema supports all these fields but we don't populate them.

3. **Duplications**: CloudVoyager includes `duplications-{ref}.pb` files. We do not include any duplication data.

4. **Analysis date**: We use `time.Now()`. CloudVoyager uses the extraction timestamp. Unlikely to cause failures.

### Branch Name Mapping (FIXED)
<!-- updated: 2026-05-27_19:00:00 -->

**Symptom**: CE fails with "Invalid branch type 'SHORT'. Branch 'main' already exists with type 'LONG'" when the SQ main branch name differs from the SC main branch name.

**Root cause**: The migration tool was using the SQ branch name (`main`) in the protobuf metadata and CE submit, but the SC project's main branch was named `master`.

**Fix**: Added CloudVoyager-pattern branch name mapping in `tasks_scanhistory.go`:
1. `collectBranchInfo()` now returns `branchInfo` structs with `IsMain` flag (from SQ extracted data)
2. Before importing, queries SC via `e.Cloud.Branches.List()` to discover the actual SC main branch name
3. Uses the SC main branch name in the protobuf metadata (`BranchName`) and CE submit (`characteristic=branch=...`), while keeping the SQ branch name for filtering extracted data (issues, components, sources)

### "Component has been deleted by end-user during analysis"
<!-- updated: 2026-05-27_19:00:00 -->

**Symptom**: ALL CE tasks fail with "Component has been deleted by end-user during analysis", even for projects not involved in migration.

**Root cause**: This is a SonarCloud staging environment issue, not a code issue. All projects in the organization entered a soft-deleted state. The projects still appear via the search API but CE treats them as deleted.

**Solution**: Re-create the projects on the SC staging environment, or use a fresh organization/enterprise. This error is NOT caused by our ZIP format, protobuf content, or submission logic.

### Resolved Issues

- **ReferenceBranchName**: Previously not set in `MetadataInput`. Now set correctly, defaulting to `BranchName` — matches CloudVoyager behavior.
- **ZIP data descriptors**: Fixed via `Deflate` + `CreateHeader` (see root cause above).
- **Branch name mapping**: Fixed to query SC main branch name (see above).

### Confirmed NON-issues

- **ZIP entry names**: Our code uses `external-issues-{ref}.pb` (with hyphens) at `packager.go`. CloudVoyager's working `report-packager` also uses hyphens. These match.
- **Submit endpoint**: Both use `/api/ce/submit`.
- **Multipart form structure**: Both use the same form fields: `report`, `projectKey`, `organization`, `characteristic` (branch/branchType), and `properties`.
- **context-props.pb**: Both include an empty `context-props.pb`.
- **Auth method**: `sqco_` tokens require Bearer auth (not Basic). Our `authTransport` correctly uses Bearer.
- **Metadata fields**: Comprehensive comparison against CloudVoyager's `build-metadata.js` shows all fields match: analysisDate (epoch ms), organizationKey, projectKey, rootComponentRef, branchName, branchType (BRANCH=1), referenceBranchName, scmRevisionId (random 40-char hex), projectVersion ("1.0.0"), qprofilesPerLanguage, analyzedIndexedFileCountPerType.
- **Protobuf content**: Issue, ExternalIssue, AdHocRule, ActiveRule, Component, and Changesets messages all use correct field numbers per the proto schema. Length-delimited encoding (varint prefix) matches the Java CE parser expectations.

---

## Getting Help

1. Check `files/*/requests.log` for detailed error information.
2. Generate an analysis report: `sonar-migration-tool analysis_report <RUN_ID> --export_directory ./files/`
3. Search the [repository issues](https://github.com/sonar-solutions/sonar-migration-tool/issues).
4. Open a new issue with: the command you ran (redact tokens), the full error message, and relevant log excerpts.
