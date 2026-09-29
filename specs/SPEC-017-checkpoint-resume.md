---
spec_id: SPEC-017
title: Checkpoint & Resume System (Enhanced)
status: draft
priority: P1
epic: "Migration Workflow"
depends_on: []
estimated_effort: L
cloudvoyager_ref: "src/shared/state/"
---

# SPEC-017: Checkpoint & Resume System (Enhanced)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Checkpoint & Resume System replaces the current basic resume mechanism (extract_id and run_id) with a production-grade write-ahead journal, advisory locking, extraction caching, and graceful shutdown coordination. This is a direct harvest of CloudVoyager's battle-tested state management subsystem, adapted for Go's concurrency model and file system primitives.

CloudVoyager's checkpoint system was born from real-world production failures: network interruptions mid-upload, process kills during multi-hour migrations, stale lock files from crashed containers, and version drift between resume attempts. Every feature in this spec exists because a customer hit the corresponding failure mode. The system provides crash-consistent state tracking so that any interrupted migration can resume from exactly where it left off, without re-extracting data, re-uploading completed branches, or duplicating CE tasks.

The current Go tool's resume support is limited to reusing an `extract_id` directory and a `run_id` directory. It has no concept of per-phase tracking, no lock file protection against concurrent runs, no extraction cache to avoid redundant API calls, and no graceful shutdown handler. This spec closes that gap entirely.

## Problem Statement

Migrations of large SonarQube Server installations can take hours or days. The current tool provides no protection against process interruption: if the tool is killed mid-migration, the operator must either start over (wasting hours of completed work) or manually determine which entities were already migrated and craft a targeted re-run. There is no lock file to prevent concurrent runs from corrupting state, no fingerprint validation to detect environment drift between resume attempts, and no extraction cache to avoid redundant API calls when only the upload phase needs to restart.

The impact is severe for enterprise customers with thousands of projects: a single network blip at hour 6 of an 8-hour migration means starting over from scratch, or worse, creating duplicate data in SonarQube Cloud from a naive re-run.

## User Stories

- **As a** migration operator, **I want to** resume an interrupted migration from where it left off, **so that** I do not lose hours of completed work after a crash or network failure.
- **As a** migration operator, **I want to** see the current progress of a checkpoint, **so that** I can estimate how much work remains after a resume.
- **As a** migration operator, **I want to** be protected from running two migration instances simultaneously, **so that** concurrent writes do not corrupt my migration state.
- **As a** migration operator, **I want to** force-restart a migration discarding all checkpoint data, **so that** I can start fresh when the checkpoint is corrupted or stale.
- **As a** migration operator, **I want to** gracefully interrupt a migration with CTRL+C, **so that** the checkpoint is saved cleanly before exit and I can resume later.
- **As a** migration operator, **I want to** avoid re-extracting data that was already extracted in a prior run, **so that** resume is fast even for large installations.
- **As a** migration operator, **I want to** be warned if the SonarQube Server version changed between runs, **so that** I know whether cached data may be stale.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Implement a write-ahead checkpoint journal persisted as JSON on disk | Must |
| FR-2 | Journal tracks per-phase completion status (pending, in_progress, completed, failed) | Must |
| FR-3 | Journal tracks per-branch status and per-branch-phase completion | Must |
| FR-4 | Journal tracks uploaded CE tasks with task ID and submission timestamp | Must |
| FR-5 | Journal includes a session fingerprint (SQ version, SQ URL, tool version, start time) | Must |
| FR-6 | Validate session fingerprint on resume; warn on version/URL drift, fail on project key mismatch | Must |
| FR-7 | Implement advisory lock file with PID, hostname, and start timestamp | Must |
| FR-8 | Detect stale locks by checking if the recorded PID is still alive on the same host | Must |
| FR-9 | Auto-release stale locks from dead processes; require `--force-unlock` for remote hosts | Must |
| FR-10 | Implement disk-backed extraction cache with gzip compression per phase per branch | Must |
| FR-11 | Cache aging: auto-purge caches older than `checkpoint.cacheMaxAgeDays` (default 7) | Should |
| FR-12 | Implement graceful shutdown coordinator: SIGINT/SIGTERM handler saves journal before exit | Must |
| FR-13 | First CTRL+C triggers graceful shutdown; second CTRL+C forces immediate exit | Must |
| FR-14 | Implement `--force-restart` flag to discard checkpoint and start fresh | Must |
| FR-15 | Implement `--force-fresh-extract` flag to clear extraction caches only | Must |
| FR-16 | Implement `--force-unlock` flag to force-release a stale lock | Must |
| FR-17 | Implement `--show-progress` flag to display checkpoint status and exit | Must |
| FR-18 | Upload deduplication: check CE activity for matching `scm_revision_id` before re-uploading | Should |
| FR-19 | Atomic journal writes: write to temp file, fsync, rename with backup rotation | Must |
| FR-20 | Migration journal: per-org, per-project step completion tracking | Must |
| FR-21 | Support `strictResume` config option that fails (instead of warns) on fingerprint mismatches | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Journal write latency | < 10ms per atomic write (fsync + rename) |
| NFR-2 | Lock acquisition time | < 100ms including stale detection |
| NFR-3 | Extraction cache compression ratio | >= 5:1 for typical JSON payloads |
| NFR-4 | Graceful shutdown window | Save journal within 2 seconds of first SIGINT |
| NFR-5 | Journal file size | < 1MB for migrations with 1000+ projects |
| NFR-6 | Cache disk usage | Proportional to extracted data; gzip reduces by ~80% |
| NFR-7 | Concurrent safety | All journal mutations protected by in-process mutex |

## Technical Design

### Architecture

The checkpoint system introduces a new package at `go/internal/checkpoint/` with the following structure:

```
go/internal/checkpoint/
├── journal.go          # CheckpointJournal — phase/branch tracking
├── migration_journal.go # MigrationJournal — per-org/project step tracking
├── lock.go             # LockFile — advisory PID-based lock
├── cache.go            # ExtractionCache — gzipped disk-backed cache
├── fingerprint.go      # Session fingerprint creation and validation
├── shutdown.go         # ShutdownCoordinator — SIGINT/SIGTERM handler
├── storage.go          # Atomic file I/O (temp + fsync + rename)
└── progress.go         # Progress display for --show-progress
```

Integration points with existing code:
- `go/cmd/extract.go` — Acquire lock, initialize journal, wrap extraction tasks with phase tracking, save cache
- `go/cmd/migrate.go` — Acquire lock, initialize migration journal, wrap migration tasks with step tracking
- `go/internal/extract/extract.go` — Check phase completion before running tasks, skip completed phases
- `go/internal/migrate/migrate.go` — Check project step completion before running tasks, skip completed steps
- `go/internal/wizard/wizard.go` — Integrate checkpoint progress into wizard resume logic

### Race Condition Analysis

- Journal writes protected by `sync.Mutex` -- all `Save()` calls serialized
- ShutdownCoordinator: `MarkInterrupted()` must acquire the SAME journal mutex before writing -- the journal mutex is shared between phase completion writes and the shutdown handler to prevent concurrent mutation
- `AtomicStorage.Save()` uses temp-file + fsync + rename -- atomic at filesystem level, but must be serialized at application level via the journal mutex
- Lock file uses advisory locking with PID -- no race between concurrent processes (only one holds the lock)
- Stale lock detection checks PID liveness -- safe because only the lock holder's PID is checked

### Key Algorithms

#### Atomic Journal Write

```go
func (s *AtomicStorage) Save(data any) error {
    // 1. Marshal to JSON with indentation
    bytes, err := json.MarshalIndent(data, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal journal: %w", err)
    }

    // 2. Write to temporary file in same directory
    tmpPath := s.path + ".tmp"
    f, err := os.Create(tmpPath)
    if err != nil {
        return fmt.Errorf("create temp: %w", err)
    }

    if _, err := f.Write(bytes); err != nil {
        f.Close()
        os.Remove(tmpPath)
        return fmt.Errorf("write temp: %w", err)
    }

    // 3. Fsync to ensure data reaches disk
    if err := f.Sync(); err != nil {
        f.Close()
        os.Remove(tmpPath)
        return fmt.Errorf("fsync: %w", err)
    }
    f.Close()

    // 4. Rotate backup: current → .bak
    bakPath := s.path + ".bak"
    os.Rename(s.path, bakPath) // ignore error if no prior file

    // 5. Atomic rename: tmp → current
    if err := os.Rename(tmpPath, s.path); err != nil {
        // Recovery: restore from backup
        os.Rename(bakPath, s.path)
        return fmt.Errorf("rename: %w", err)
    }
    return nil
}
```

#### Stale Lock Detection

```go
func (l *LockFile) isStale() (bool, error) {
    data, err := l.read()
    if err != nil {
        return false, err
    }

    // Same host: check if PID is still running
    currentHostname, _ := os.Hostname()
    if data.Hostname == currentHostname {
        process, err := os.FindProcess(data.PID)
        if err != nil {
            return true, nil // PID not found
        }
        // On Unix, FindProcess always succeeds; send signal 0 to check
        err = process.Signal(syscall.Signal(0))
        return err != nil, nil // error means process is dead
    }

    // Different host: cannot verify remotely, not stale unless forced
    return false, nil
}
```

#### Graceful Shutdown Coordinator

```go
func NewShutdownCoordinator(journals ...Saveable) *ShutdownCoordinator {
    sc := &ShutdownCoordinator{
        journals: journals,
        sigChan:  make(chan os.Signal, 2),
        done:     make(chan struct{}),
    }
    signal.Notify(sc.sigChan, syscall.SIGINT, syscall.SIGTERM)
    go sc.listen()
    return sc
}

func (sc *ShutdownCoordinator) listen() {
    // First signal: graceful shutdown
    <-sc.sigChan
    sc.mu.Lock()
    sc.interrupted = true
    sc.mu.Unlock()

    slog.Warn("Interrupt received, saving checkpoint and shutting down...")
    for _, j := range sc.journals {
        if err := j.MarkInterrupted(); err != nil {
            slog.Error("Failed to save journal on interrupt", "error", err)
        }
    }
    close(sc.done)

    // Second signal: force exit
    <-sc.sigChan
    slog.Error("Force exit requested")
    os.Exit(1)
}
```

#### Upload Deduplication

```go
func (j *CheckpointJournal) ShouldUpload(branch string, ceClient CEClient) (bool, error) {
    // Check journal for prior upload
    task := j.GetUploadedCETask(branch)
    if task == nil {
        return true, nil // never uploaded
    }

    // Verify CE task status
    status, err := ceClient.GetTaskStatus(task.TaskID)
    if err != nil {
        return true, nil // can't verify, re-upload to be safe
    }

    switch status {
    case "SUCCESS":
        return false, nil // already uploaded and processed
    case "PENDING", "IN_PROGRESS":
        return false, nil // still processing, wait
    case "FAILED", "CANCELED":
        return true, nil // failed, re-upload
    default:
        return true, nil
    }
}
```

### Data Flow

#### Checkpoint Journal Lifecycle

1. **Initialization**: On command start, attempt to load existing journal from `<export_dir>/.checkpoint-journal.json`
2. **Fingerprint validation**: Compare stored fingerprint with current environment; warn or fail on mismatches
3. **Lock acquisition**: Attempt to acquire `<export_dir>/.lock` with PID + hostname; detect stale locks
4. **Phase execution**: Before each phase, check journal; skip if already completed; mark in_progress on start
5. **Phase completion**: Mark phase completed with optional metadata (e.g., cache file path, item count)
6. **Branch tracking**: For multi-branch migration, track each branch independently within the journal
7. **Interrupt handling**: On SIGINT, mark journal as "interrupted" and save; on resume, continue from last in_progress phase
8. **Completion**: Mark journal status as "completed" with timestamp

#### Extraction Cache Lifecycle

1. **Check cache**: Before extracting a phase, check if `<cache_dir>/<phase>_<branch>.json.gz` exists and is within max age
2. **Cache hit**: Load gzipped JSON from disk, decompress, deserialize; skip API extraction entirely
3. **Cache miss**: Run extraction normally, then serialize result to gzipped JSON on disk
4. **Purge stale**: On startup, scan cache directory and remove files older than `cacheMaxAgeDays`
5. **Force fresh**: With `--force-fresh-extract`, delete entire cache directory before starting

#### Migration Journal Lifecycle

1. **Seed organizations**: Populate journal with organization keys from the org mapping CSV
2. **Org-wide resources**: Track quality gates, profiles, groups, permissions migration per org
3. **Per-project tracking**: For each project in each org, track individual migration steps (create project, upload branch, sync metadata, etc.)
4. **Step completion**: Each step is recorded with completion timestamp; on resume, completed steps are skipped
5. **Project completion**: When all steps for a project pass, mark project as completed
6. **Org completion**: When all org-wide resources and all projects in an org complete, mark org as completed

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/ce/activity` | GET | Check CE task status for upload deduplication |
| `/api/system/status` | GET | Retrieve SQ version for session fingerprint |

### Journal JSON Schema

```json
{
  "version": 2,
  "toolVersion": "1.5.0",
  "status": "in_progress",
  "createdAt": "2026-05-26T10:00:00Z",
  "updatedAt": "2026-05-26T10:30:00Z",
  "sessionFingerprint": {
    "sonarQubeVersion": "10.8.0",
    "sonarQubeUrl": "https://sq.example.com",
    "toolVersion": "1.5.0",
    "startedAt": "2026-05-26T10:00:00Z"
  },
  "phases": {
    "connection_test": { "status": "completed", "completedAt": "..." },
    "extract:issues": { "status": "completed", "cacheFile": "issues_main.json.gz", "itemCount": 50000 },
    "extract:sources": { "status": "in_progress", "startedAt": "..." },
    "build_protobuf": { "status": "pending" },
    "upload": { "status": "pending" }
  },
  "branches": {
    "main": { "status": "completed", "ceTaskId": "AY...", "phases": { ... } },
    "develop": { "status": "in_progress", "currentPhase": "extract:issues", "phases": { ... } }
  },
  "uploadedCeTasks": {
    "main": { "taskId": "AY...", "submittedAt": "...", "status": "SUCCESS" }
  }
}
```

### Migration Journal JSON Schema

```json
{
  "version": 1,
  "status": "in_progress",
  "createdAt": "2026-05-26T11:00:00Z",
  "organizations": {
    "my-org": {
      "status": "in_progress",
      "orgWideResources": "completed",
      "projects": {
        "my-project": {
          "status": "in_progress",
          "steps": {
            "create_project": { "status": "completed", "completedAt": "..." },
            "upload_main_branch": { "status": "completed", "completedAt": "..." },
            "wait_ce_main": { "status": "completed", "completedAt": "..." },
            "upload_develop_branch": { "status": "in_progress", "startedAt": "..." },
            "sync_metadata": { "status": "pending" }
          }
        }
      }
    }
  }
}
```

### Lock File JSON Schema

```json
{
  "version": 1,
  "pid": 12345,
  "hostname": "migration-host-01",
  "startedAt": "2026-05-26T10:00:00Z"
}
```

## Acceptance Criteria

- [ ] AC-1: Running a migration creates a `.checkpoint-journal.json` file in the export directory that tracks all phases
- [ ] AC-2: Killing the process (SIGINT) saves the journal; re-running resumes from the last incomplete phase
- [ ] AC-3: Running two instances simultaneously fails with a lock error on the second instance
- [ ] AC-4: A stale lock from a dead process on the same host is auto-released on next run
- [ ] AC-5: `--force-restart` deletes the journal and lock, starting a fresh migration
- [ ] AC-6: `--force-fresh-extract` deletes cached extraction data but preserves the journal
- [ ] AC-7: `--force-unlock` releases a lock even from a different host
- [ ] AC-8: `--show-progress` prints a summary table of phase statuses and exits without running anything
- [ ] AC-9: Extraction cache files are gzip-compressed and reduce resume startup time by >90% for cached phases
- [ ] AC-10: Session fingerprint warns on SQ version change and fails on project key mismatch
- [ ] AC-11: Second CTRL+C during graceful shutdown forces immediate exit
- [ ] AC-12: Upload deduplication prevents re-uploading branches that already have a SUCCESS CE task
- [ ] AC-13: Migration journal tracks per-org, per-project, per-step completion and skips completed steps on resume
- [ ] AC-14: Journal writes are atomic (temp + fsync + rename) and survive process crash during write
- [ ] AC-15: Cache files older than `cacheMaxAgeDays` are purged on startup

## CloudVoyager Reference

| Area | Path |
|------|------|
| Checkpoint Journal | `src/shared/state/checkpoint/` |
| Lock File | `src/shared/state/lock/` |
| Extraction Cache | `src/shared/state/extraction-cache/` |
| Migration Journal | `src/shared/state/migration-journal/` |
| Atomic Storage | `src/shared/state/storage.js` |
| Fingerprint Validation | `src/shared/state/checkpoint/helpers/validate-fingerprint.js` |
| Tracker (progress) | `src/shared/state/tracker/` |

## Known Limitations

- Lock file stale detection cannot verify remote PIDs; locks from different hosts require `--force-unlock`
- Extraction cache stores full JSON payloads which may consume significant disk space for very large installations (mitigated by gzip and max-age purging)
- Journal schema versioning requires forward-compatible reading; journals from future tool versions may not load in older versions
- The graceful shutdown coordinator cannot save journal if the process is killed with SIGKILL (kill -9)

## Open Questions

- Should the cache compression algorithm be configurable (gzip vs zstd)? Zstd offers better compression ratios and speed but adds a dependency.
- Should the journal support concurrent writers for future distributed migration scenarios, or is single-process always the assumption?
- What is the right default for `cacheMaxAgeDays` in the Go implementation? CloudVoyager uses 7 days.
- Should `--show-progress` output be machine-readable (JSON) in addition to human-readable (table)?
