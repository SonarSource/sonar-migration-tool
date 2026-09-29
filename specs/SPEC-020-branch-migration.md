---
spec_id: SPEC-020
title: Branch Migration Strategy
status: draft
priority: P1
epic: "Migration Workflow"
depends_on: [SPEC-001, SPEC-002, SPEC-008]
estimated_effort: L
cloudvoyager_ref: "src/pipelines/*/transfer/, src/shared/state/checkpoint-journal"
---

# SPEC-020: Branch Migration Strategy
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Branch Migration Strategy enables migration of all branches from SonarQube Server to SonarQube Cloud, not just the main branch. SonarQube Server installations commonly have dozens of long-lived branches per project (develop, release/*, staging, etc.) each with their own issue corpus, measures, and analysis history. Migrating only the main branch loses all branch-specific data, which is unacceptable for teams that rely on branch-level quality gates and new code period analysis.

CloudVoyager's branch migration handles a critical constraint of SonarQube Cloud's Compute Engine: the main branch must be uploaded and successfully processed FIRST before any non-main branch can be uploaded. This creates an ordered dependency chain where main branch CE completion is a gate for all other branch uploads. The system also handles pull request branches (via `/api/project_pull_requests/list`), branch include/exclude lists, per-project branch control via CSV, and per-branch checkpoint tracking for resume support.

The current Go tool extracts branch metadata during the extract phase but does not migrate branch-specific data. This spec adds full branch lifecycle migration: extraction of per-branch issues, source code, SCM data, and measures; protobuf report generation per branch; ordered upload with CE task coordination; and per-branch metadata synchronization.

## Problem Statement

When migrating from SonarQube Server to SonarQube Cloud, the current tool only migrates configuration entities (projects, gates, profiles, groups, permissions) which are branch-agnostic. Historical data migration (when implemented via SPEC-001/002) will initially target only the main branch. However, enterprise teams routinely maintain 5-50 long-lived branches per project, each containing unique issues, distinct measure trends, and independent quality gate statuses. Losing this branch-specific data means losing the ability to track code quality evolution across release branches, compare branch quality, and maintain branch-level compliance requirements.

Additionally, SonarQube Cloud's CE has a strict ordering constraint: the main branch analysis must complete before any non-main branch analysis can be submitted. Violating this constraint causes CE task failures. The tool must enforce this ordering while maximizing parallelism for non-main branches.

## User Stories

- **As a** migration operator, **I want to** migrate all branches of a project, **so that** branch-specific issues, measures, and analysis history are preserved in SonarQube Cloud.
- **As a** migration operator, **I want to** exclude specific branches from migration, **so that** I can skip obsolete or short-lived branches.
- **As a** migration operator, **I want to** control which branches to migrate per project via CSV, **so that** I have fine-grained control over the migration scope.
- **As a** migration operator, **I want to** resume branch migration after interruption, **so that** completed branches are not re-uploaded.
- **As a** migration operator, **I want to** see per-branch progress during migration, **so that** I know which branches have been completed.
- **As a** migration operator, **I want to** migrate pull request branches, **so that** open PR analysis data is preserved.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Extract branch list per project via `/api/project_branches/list` | Must |
| FR-2 | Extract pull request branches per project via `/api/project_pull_requests/list` | Should |
| FR-3 | Upload main branch first; wait for CE task SUCCESS before uploading other branches | Must |
| FR-4 | If main branch CE task fails, abort all branch migrations for that project | Must |
| FR-5 | Support `syncAllBranches` config option (default: true) | Must |
| FR-6 | Support `excludeBranches` config option: array of branch name patterns to skip | Must |
| FR-7 | Support per-project branch control via `Branches` column in `projects.csv` | Must |
| FR-8 | Track per-branch completion in checkpoint journal (SPEC-017) | Must |
| FR-9 | Skip completed branches on resume | Must |
| FR-10 | Per-branch data extraction: issues, source code, SCM data, measures | Must |
| FR-11 | Per-branch protobuf report generation (SPEC-001) | Must |
| FR-12 | Per-branch metadata synchronization: issue status, assignments, comments (SPEC-008) | Must |
| FR-13 | Per-branch new code period extraction and configuration | Should |
| FR-14 | Support glob patterns in `excludeBranches` (e.g., `feature/*`, `bugfix/*`). Note: Go's `filepath.Match` does not support `**` or recursive glob patterns. For branch exclude patterns, use simple glob matching where `*` matches any characters except path separators. For patterns containing `/`, use `strings.Contains` or regex instead. | Should |
| FR-15 | Log per-branch progress: branch name, status, issue count, duration | Must |
| FR-16 | Support sequential or parallel upload of non-main branches (configurable) | Should |
| FR-17 | Detect and handle renamed main branch (SQ Server main != SC default branch) | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Branch extraction throughput | >= 1 branch/minute for average-size branches |
| NFR-2 | CE task polling interval | 5 seconds (configurable) |
| NFR-3 | CE task timeout | 30 minutes per branch (configurable) |
| NFR-4 | Max concurrent branch uploads | Configurable, default 1 (sequential) |
| NFR-5 | Memory usage | Per-branch processing; do not load all branches simultaneously |
| NFR-6 | Branch count support | Handle projects with 100+ branches |

## Technical Design

### Architecture

Branch migration integrates into the existing pipeline architecture:

```
go/internal/extract/
├── tasks_branches.go          # ENHANCED: Per-branch data extraction
├── tasks_issues.go            # ENHANCED: Branch-aware issue extraction
├── tasks_sources.go           # NEW: Per-branch source code extraction
├── tasks_measures.go          # ENHANCED: Per-branch measure extraction

go/internal/scanner/
├── report.go                  # ENHANCED: Accept branch parameter for report generation

go/internal/migrate/
├── tasks_branches.go          # NEW: Branch migration orchestration
├── branch_uploader.go         # NEW: Ordered branch upload with CE coordination
├── branch_sync.go             # NEW: Per-branch metadata synchronization

go/internal/checkpoint/
├── journal.go                 # ENHANCED: Per-branch phase tracking (SPEC-017)
```

### Key Algorithms

#### Branch Migration Orchestrator

```go
type BranchMigrator struct {
    project     structure.Project
    branches    []Branch
    journal     *checkpoint.CheckpointJournal
    uploader    *scanner.Uploader
    syncer      *MetadataSyncer
    logger      *slog.Logger
    concurrency int
}

type Branch struct {
    Name     string
    IsMain   bool
    Type     string // "LONG", "SHORT", "PULL_REQUEST"
    PRKey    string // For pull request branches
}

func (bm *BranchMigrator) MigrateAll(ctx context.Context) error {
    // Step 1: Identify main branch
    mainBranch := bm.findMainBranch()
    if mainBranch == nil {
        return fmt.Errorf("no main branch found for project %s", bm.project.Key)
    }

    // Step 2: Migrate main branch first (blocking)
    if err := bm.migrateBranch(ctx, mainBranch); err != nil {
        return fmt.Errorf("main branch migration failed: %w", err)
    }

    // Step 3: Wait for main branch CE task to complete
    if err := bm.waitForCE(ctx, mainBranch); err != nil {
        return fmt.Errorf("main branch CE failed: %w", err)
    }

    // Step 4: Migrate non-main branches (sequential or parallel)
    nonMainBranches := bm.filterNonMainBranches()
    if bm.concurrency <= 1 {
        for _, branch := range nonMainBranches {
            if err := bm.migrateBranchWithCE(ctx, &branch); err != nil {
                bm.logger.Error("Branch migration failed",
                    "branch", branch.Name, "error", err)
                // Continue with other branches; don't abort entire project
            }
        }
    } else {
        g, gCtx := errgroup.WithContext(ctx)
        g.SetLimit(bm.concurrency)
        for _, branch := range nonMainBranches {
            b := branch
            g.Go(func() error {
                return bm.migrateBranchWithCE(gCtx, &b)
            })
        }
        if err := g.Wait(); err != nil {
            bm.logger.Error("Some branch migrations failed", "error", err)
        }
    }

    return nil
}
```

#### Per-Branch Data Pipeline

```go
func (bm *BranchMigrator) migrateBranch(ctx context.Context, branch *Branch) error {
    // Check checkpoint journal for completion
    if bm.journal.IsBranchPhaseCompleted(branch.Name, "all") {
        bm.logger.Info("Branch already completed, skipping", "branch", branch.Name)
        return nil
    }

    phases := []struct {
        name string
        fn   func(context.Context, *Branch) error
    }{
        {"extract:issues", bm.extractIssues},
        {"extract:sources", bm.extractSources},
        {"extract:scm", bm.extractSCM},
        {"extract:measures", bm.extractMeasures},
        {"build_protobuf", bm.buildProtobuf},
        {"upload", bm.upload},
    }

    for _, phase := range phases {
        if bm.journal.IsBranchPhaseCompleted(branch.Name, phase.name) {
            bm.logger.Debug("Phase already completed, skipping",
                "branch", branch.Name, "phase", phase.name)
            continue
        }

        bm.journal.StartBranchPhase(branch.Name, phase.name)
        if err := phase.fn(ctx, branch); err != nil {
            bm.journal.FailBranchPhase(branch.Name, phase.name, err)
            return fmt.Errorf("phase %s failed for branch %s: %w",
                phase.name, branch.Name, err)
        }
        bm.journal.CompleteBranchPhase(branch.Name, phase.name)
    }

    return nil
}
```

#### CE Task Coordination

```go
func (bm *BranchMigrator) waitForCE(ctx context.Context, branch *Branch) error {
    task := bm.journal.GetUploadedCETask(branch.Name)
    if task == nil {
        return fmt.Errorf("no CE task found for branch %s", branch.Name)
    }

    pollInterval := 5 * time.Second
    timeout := 30 * time.Minute
    deadline := time.Now().Add(timeout)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }

        if time.Now().After(deadline) {
            return fmt.Errorf("CE task %s timed out after %v", task.TaskID, timeout)
        }

        status, err := bm.uploader.GetCETaskStatus(task.TaskID)
        if err != nil {
            bm.logger.Warn("Failed to check CE status, retrying",
                "taskId", task.TaskID, "error", err)
            time.Sleep(pollInterval)
            continue
        }

        switch status {
        case "SUCCESS":
            bm.logger.Info("CE task completed successfully",
                "branch", branch.Name, "taskId", task.TaskID)
            bm.journal.MarkBranchCompleted(branch.Name, task.TaskID)
            return nil
        case "FAILED":
            return fmt.Errorf("CE task %s failed for branch %s",
                task.TaskID, branch.Name)
        case "CANCELED":
            return fmt.Errorf("CE task %s was canceled for branch %s",
                task.TaskID, branch.Name)
        case "PENDING", "IN_PROGRESS":
            bm.logger.Debug("CE task still processing",
                "branch", branch.Name, "taskId", task.TaskID, "status", status)
        }

        time.Sleep(pollInterval)
    }
}
```

#### Branch Filtering

```go
type BranchFilter struct {
    SyncAllBranches bool
    ExcludePatterns []string // glob patterns
    CSVBranches     string   // from projects.csv Branches column
}

func (f *BranchFilter) ShouldMigrate(branch Branch) bool {
    if !f.SyncAllBranches && !branch.IsMain {
        return false // Only migrate main branch when syncAllBranches is off
    }

    // Always migrate main branch
    if branch.IsMain {
        return true
    }

    // Check CSV branch list
    if f.CSVBranches != "" && f.CSVBranches != "*" {
        allowedBranches := strings.Split(f.CSVBranches, ",")
        found := false
        for _, allowed := range allowedBranches {
            if strings.TrimSpace(allowed) == branch.Name {
                found = true
                break
            }
        }
        if !found {
            return false
        }
    }

    // Check exclude patterns
    for _, pattern := range f.ExcludePatterns {
        if matched, _ := filepath.Match(pattern, branch.Name); matched {
            return false
        }
    }

    return true
}
```

#### Branch-Aware Issue Extraction

```go
func extractBranchIssues(
    ctx context.Context,
    raw *common.RawClient,
    projectKey string,
    branchName string,
    outputDir string,
) (int, error) {
    writer := common.NewChunkWriter(
        filepath.Join(outputDir, "issues"),
        fmt.Sprintf("%s_%s", projectKey, sanitizeBranchName(branchName)),
    )
    defer writer.Close()

    params := url.Values{
        "componentKeys": {projectKey},
        "branch":        {branchName},
        "ps":            {"500"},
        "additionalFields": {"_all"},
    }

    count := 0
    err := common.Paginate(ctx, raw, "/api/issues/search", params, func(page json.RawMessage) error {
        var result struct {
            Issues []json.RawMessage `json:"issues"`
        }
        if err := json.Unmarshal(page, &result); err != nil {
            return err
        }
        for _, issue := range result.Issues {
            writer.Write(issue)
            count++
        }
        return nil
    })

    return count, err
}
```

### Data Flow

#### Per-Branch Migration Pipeline

```
For each project:
  1. List branches via /api/project_branches/list
  2. Apply branch filter (syncAllBranches, excludeBranches, CSV Branches column)
  3. Separate main branch from non-main branches
  4. MAIN BRANCH (blocking):
     a. Extract(main) → issues, sources, SCM, measures
     b. Build protobuf report(main)
     c. Upload(main) via POST /api/ce/submit
     d. Poll CE status until SUCCESS
  5. NON-MAIN BRANCHES (sequential or parallel):
     For each non-main branch:
       a. Extract(branch) → issues, sources, SCM, measures
       b. Build protobuf report(branch)
       c. Upload(branch) via POST /api/ce/submit
       d. Poll CE status until SUCCESS
       e. SyncMetadata(branch) → issue status, assignments, comments
```

#### File Organization Per Branch

```
files/<extract_id>/
├── branches/
│   └── <project_key>/
│       ├── main/
│       │   ├── issues/          # JSONL files
│       │   ├── sources/         # Source code files
│       │   ├── scm/             # SCM changeset data
│       │   └── measures/        # Metric snapshots
│       ├── develop/
│       │   ├── issues/
│       │   ├── sources/
│       │   ├── scm/
│       │   └── measures/
│       └── release-1.0/
│           └── ...
└── reports/
    └── <project_key>/
        ├── main.zip             # Protobuf report archive
        ├── develop.zip
        └── release-1.0.zip
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/project_branches/list` | GET | List all branches for a project |
| `/api/project_pull_requests/list` | GET | List pull request branches for a project |
| `/api/issues/search` | GET | Extract issues per branch (with `branch` param) |
| `/api/sources/raw` | GET | Extract source code per branch (with `branch` param) |
| `/api/measures/component_tree` | GET | Extract measures per branch (with `branch` param) |
| `/api/ce/submit` | POST | Upload protobuf report archive |
| `/api/ce/activity` | GET | Poll CE task status |
| `/api/new_code_periods/show` | GET | Get new code period per branch |
| `/api/new_code_periods/set` | POST | Set new code period per branch in SC |

### Configuration

```json
{
  "syncAllBranches": true,
  "excludeBranches": ["feature/*", "bugfix/*", "dependabot/*"],
  "branchUploadConcurrency": 1,
  "ceTaskPollIntervalSeconds": 5,
  "ceTaskTimeoutMinutes": 30
}
```

## Acceptance Criteria

- [ ] AC-1: With `syncAllBranches: true`, all branches of a project are migrated to SonarQube Cloud
- [ ] AC-2: Main branch is always uploaded and processed first before any non-main branch
- [ ] AC-3: If main branch CE task fails, all other branch migrations for that project are aborted
- [ ] AC-4: Branches matching `excludeBranches` patterns are skipped
- [ ] AC-5: Per-project branch control via CSV `Branches` column works correctly
- [ ] AC-6: Completed branches are tracked in checkpoint journal and skipped on resume
- [ ] AC-7: Per-branch progress is logged with branch name, status, issue count, and duration
- [ ] AC-8: Pull request branches are extracted and migrated when configured
- [ ] AC-9: Per-branch new code periods are extracted and set in SonarQube Cloud
- [ ] AC-10: Branch data is organized in separate directories per branch per project
- [ ] AC-11: Non-main branch upload failure does not abort other branch migrations
- [ ] AC-12: `syncAllBranches: false` migrates only the main branch
- [ ] AC-13: CE task polling respects the configured interval and timeout
- [ ] AC-14: Glob patterns in `excludeBranches` correctly match branch names (e.g., `feature/*` matches `feature/foo`)

## CloudVoyager Reference

| Area | Path |
|------|------|
| Transfer Pipeline (branch orchestration) | `src/pipelines/sq-10.4/transfer-pipeline/` |
| Transfer Branch (per-branch pipeline) | `src/pipelines/sq-10.4/transfer-branch/` |
| Branch Tracking (checkpoint) | `src/shared/state/checkpoint/helpers/branch-tracking.js` |
| Branch Phase Tracking | `src/shared/state/checkpoint/helpers/branch-phase-tracking.js` |
| Branch Verification | `src/shared/verification/checkers/branches/` |

### Partial Implementation Path

Branch migration requires the scanner protocol engine (SPEC-001). However, branch support for configuration-only migration (quality gates, profiles, permissions) can be implemented independently. Teams that only need configuration migration across branches can implement the branch listing, filtering, and per-branch config application without waiting for the full scanner protocol engine.

## Known Limitations

- SonarQube Cloud CE requires main branch analysis before any non-main branch; this creates a sequential bottleneck for the first branch upload per project
- Pull request branches may reference merge targets that no longer exist; the tool will skip PR branches with invalid targets
- Branch names containing `/` are sanitized to `_` in file paths, which may cause name collisions for branches like `release/1.0` and `release_1.0`; a collision detection step mitigates this
- Very large projects with 100+ branches will take proportionally longer; the tool logs estimated completion time based on completed branch throughput
- Short-lived branches that were deleted on SQ Server after extraction but before upload will fail at CE; these failures are logged but do not abort the migration

## Open Questions

- Should the tool support branch renaming during migration (e.g., migrating `master` as `main`)?
- Should pull request branches be included by default or require explicit opt-in?
- What is the right default for `branchUploadConcurrency`? CloudVoyager uses 1 (sequential) to avoid CE queue saturation, but parallel may be faster for SonarQube Cloud instances with higher CE capacity.
- Should the tool migrate branch analysis history (all past analyses per branch) or only the latest analysis? CloudVoyager migrates only the latest.
- How should the tool handle branches that exist in SQ Server but have never been analyzed (zero issues, zero measures)?
