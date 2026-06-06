# PLAN-FIX-203: Migrate Project Version (Issue #102)
<!-- updated: 2026-06-04_01:53:47 -->

> **GitHub Issue:** [#102 — `sonar-migration-tool` must migrate the project version](https://github.com/sonar-solutions/sonar-migration-tool/issues/102)
>
> **Goal:** After migration, the target project should reflect the same project version as the source project, on all migrated branches.

---

## Problem Statement
<!-- updated: 2026-06-04_01:53:47 -->

Every imported scan currently defaults to project version `"1.0.0"` because the migration never passes the real version through to the protobuf metadata or CE submission form. The plumbing already exists (`MetadataInput.ProjectVersion`, `SubmitConfig.ProjectVersion`) — it's just never populated.

The extraction phase already fetches project analyses via `/api/project_analyses/search` (which includes `ProjectVersion`), but this data is never loaded or used during project data import.

---

## CloudVoyager Reference Implementation
<!-- updated: 2026-06-04_01:53:47 -->

CloudVoyager solves this with a **per-branch version lookup** using `/api/navigation/component`:

### Flow
1. **Extract:** For each project + branch, call `GET /api/navigation/component?component={key}&branch={name}`
2. **Resolve:** `resolveSourceProjectVersion()` returns `response.data.version` or `null`
3. **Build metadata:** Pass version into protobuf metadata → `projectVersion: sourceProjectVersion || '1.0.0'`
4. **Submit:** Include as `sonar.projectVersion={version}` in the CE submission form properties

### Key files (CloudVoyager)
| File | Purpose |
|------|---------|
| `src/shared/utils/source-version/resolve-source-project-version.js` | Fetches version via `/api/navigation/component` per branch |
| `src/pipelines/sq-10.4/transfer-pipeline/helpers/transfer-branch.js` | Calls `resolveSourceProjectVersion()` per branch |
| `src/pipelines/sq-10.4/protobuf/builder/helpers/build-metadata.js` | Sets `projectVersion` in protobuf metadata |
| `src/pipelines/sq-10.4/sonarcloud/uploader/helpers/build-submit-form.js` | Sets `sonar.projectVersion` in CE submission |

### Key insight
Project version is **per-analysis metadata**, not a project setting. It's transmitted inside the scanner report (both protobuf metadata and `sonar.projectVersion` property). Each branch can have its own version. CloudVoyager resolves the **current** version per-branch, not historical versions.

---

## Current State (sonar-migration-tool)
<!-- updated: 2026-06-04_01:53:47 -->

### What exists
| Layer | Field exists? | Populated? | Notes |
|-------|:---:|:---:|-------|
| **Extract** (`getProjectAnalyses`) | Yes — `Analysis.ProjectVersion` | Extracted but never loaded during migration | `/api/project_analyses/search` returns `projectVersion` per analysis |
| **`MetadataInput`** (`builder.go:58`) | Yes — `ProjectVersion string` | Never set | Falls back to `"1.0.0"` at `builder.go:67` |
| **`SubmitConfig`** (`submit.go:24`) | Yes — `ProjectVersion string` | Never set | Falls back to `"1.0.0"` at `submit.go:151` |
| **`importBranchInput`** (`tasks_projectdata.go`) | No field | N/A | No way to carry version into `importBranch()` |

### Data flow gap
```
SonarQube /api/project_analyses/search → Analysis.ProjectVersion
    ↓
getProjectAnalyses extract task → NDJSON (version is stored)
    ↓
✗ MISSING: no loader reads analyses during migration
    ↓
importBranch() has no version to pass
    ↓
MetadataInput.ProjectVersion = "" → defaults to "1.0.0"
    ↓
SubmitConfig.ProjectVersion = "" → defaults to "1.0.0"
```

---

## Proposed Solution
<!-- updated: 2026-06-04_01:53:47 -->

### Approach: Add `/api/navigation/component` extraction + pass-through

Follow CloudVoyager's pattern — add a new extract task that fetches the **current** project version per branch via `/api/navigation/component`, then thread that version through the project data import pipeline.

**Why this over using existing `getProjectAnalyses` data:**
- `/api/project_analyses/search` returns analyses at the **project level** (not per-branch), making it unreliable for per-branch version resolution
- `/api/navigation/component?component={key}&branch={name}` returns the **current version for a specific branch**, which is exactly what CloudVoyager uses
- Simpler and more accurate — one API call per branch, no need to correlate analyses to branches

### Changes Required

#### 1. Add API type for navigation/component response

**File:** `lib/sq-api-go/types/` (new type or extend existing)

```go
type NavigationComponent struct {
    Key     string `json:"key"`
    Version string `json:"version"`
    // other fields exist but aren't needed
}
```

#### 2. Add extract task: `getProjectBranchVersions`

**File:** `go/internal/extract/tasks_misc.go` (or new file `tasks_versions.go`)

- **Dependencies:** `getProjects`, `getProjectBranches`
- **For each project + branch:** call `GET /api/navigation/component?component={projectKey}&branch={branchName}`
- **For main branch:** omit the `branch` parameter (or pass the main branch name)
- **Store:** `{ projectKey, branch, version }` records to the extract data store
- **Error handling:** If the endpoint returns no version or errors, log a warning and continue (version will fall back to `"1.0.0"`)

#### 3. Add loader function in project data import

**File:** `go/internal/migrate/tasks_projectdata.go`

Add a `loadExtractedBranchVersions()` function (similar to existing `loadExtractedQProfiles()`) that reads the extracted version data and returns a lookup map:

```go
// Returns map[projectKey+branch] → version string
func loadExtractedBranchVersions(e *Executor, serverKey string) map[string]string
```

#### 4. Thread version through `importBranchInput`

**File:** `go/internal/migrate/tasks_projectdata.go`

Add `ProjectVersion string` to the `importBranchInput` struct and populate it from the version lookup when constructing inputs in `importProjectData()`.

#### 5. Pass version to `BuildMetadata` and `SubmitConfig`

**File:** `go/internal/migrate/tasks_projectdata.go`

In `importBranch()`, set both:

```go
// In BuildMetadata call (~line 205):
metadata := scanreport.BuildMetadata(scanreport.MetadataInput{
    // ... existing fields ...
    ProjectVersion: input.ProjectVersion,  // ADD THIS
}, root.Ref)

// In SubmitConfig construction (~line 244):
cfg := scanreport.SubmitConfig{
    // ... existing fields ...
    ProjectVersion: input.ProjectVersion,  // ADD THIS
}
```

The existing fallback logic in `builder.go:67` and `submit.go:151` already handles the empty-string case by defaulting to `"1.0.0"`, so no changes needed there.

#### 6. Register task in version-specific pipelines

Ensure `getProjectBranchVersions` is registered in all relevant version pipelines (SQ 9.9, 10.0-10.3, 10.4-10.8, 2025.1+). The `/api/navigation/component` endpoint has been available since at least SQ 7.x, so it should work across all supported versions.

---

## File Change Summary
<!-- updated: 2026-06-04_01:53:47 -->

| File | Change | Complexity |
|------|--------|:---:|
| `lib/sq-api-go/types/` (new or existing) | Add `NavigationComponent` type | Low |
| `go/internal/extract/tasks_misc.go` | Add `getProjectBranchVersions` extract task | Medium |
| `go/internal/migrate/tasks_projectdata.go` | Add loader, add field to `importBranchInput`, pass version through | Medium |
| Pipeline registration files | Register new task in version pipelines | Low |

**Estimated scope:** ~100-150 lines of new code across 3-4 files. No changes to protobuf, builder, or submit logic — just populating existing fields.

---

## Testing Strategy
<!-- updated: 2026-06-04_01:53:47 -->

### Unit tests
- `getProjectBranchVersions` extract task: mock `/api/navigation/component` responses, verify correct storage
- `loadExtractedBranchVersions`: verify map construction from extracted data
- `importBranch`: verify `ProjectVersion` propagates to both `MetadataInput` and `SubmitConfig`
- Edge cases: missing version (falls back to `"1.0.0"`), empty string, API error

### Integration / regression tests
- Run full extract → migrate pipeline against a test SQ instance with known project versions
- Verify the CE submission form contains the correct `sonar.projectVersion` value (visible in `requests.log`)
- Verify the protobuf metadata contains the correct version (via `analysis_report` command inspection)
- Test across multiple branches with different versions
- Follow [REGRESSION-TESTING.md](docs/REGRESSION-TESTING.md) protocol

---

## Risks & Open Questions
<!-- updated: 2026-06-04_16:52:00 -->

1. **API availability across SQ versions:** `/api/navigation/component` should be available in all supported versions (9.9+), but needs verification for the `version` field specifically.
2. **Rate limiting:** One extra API call per project×branch during extraction. For large instances (hundreds of projects, many branches), this adds volume but each call is lightweight.
3. **Version semantics:** SonarQube Cloud may interpret `sonar.projectVersion` differently than Server. Need to verify the version appears correctly in the Cloud UI after import.
4. **Main branch handling:** CloudVoyager omits the `branch` param for the main branch. Need to confirm this works for all SQ versions, or whether we should pass the explicit main branch name.
5. **RESOLVED — `"not provided"` sentinel:** SonarQube returns the literal string `"not provided"` via `/api/navigation/component` when no `sonar.projectVersion` has been configured. `resolveProjectVersion()` now normalizes this to empty string so the `"1.0.0"` fallback triggers correctly. Verified via regression test against `okorach-oss_sonar-tools` on 2026-06-04.

---

## Related Issues & Docs
<!-- updated: 2026-06-04_01:53:47 -->

- [CLOUDVOYAGER-DELTA.md](docs/CLOUDVOYAGER-DELTA.md) — should be updated to mark this feature as addressed
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — project data section should document version propagation
- [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) — line 257 documents the hardcoded `"1.0.0"` default; update after fix
