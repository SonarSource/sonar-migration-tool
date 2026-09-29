---
spec_id: SPEC-022
title: Comprehensive Reporting Suite
status: draft
priority: P2
epic: "Verification & Reporting"
depends_on: []
estimated_effort: M
cloudvoyager_ref: "src/shared/reports/"
---

# SPEC-022: Comprehensive Reporting Suite
<!-- updated: 2026-06-05_14:00:00 -->

## Implementation Status
<!-- updated: 2026-06-05_14:00:00 -->

**Partially implemented.** FR-2 (Executive Summary), FR-3 (Performance Report), and FR-5
(Markdown output) are partially fulfilled by a single consolidated
`migration_summary.{pdf,md}` artifact — **not** the separate per-report files described in
the Technical Design (`executive-summary.*`, `performance-report.*`). The migrate engine now
writes `migration_summary.pdf` and `migration_summary.md` (via `summary.GenerateReports`),
backed by machine-readable run instrumentation: `run_meta.json` (per-phase / per-task timing
and `overall_status`) and `run_events.jsonl`. These are produced on completion even when the
run fails (`overall_status=failed`), satisfying NFR-6 (graceful degradation) for this slice.

The **tee slog handler** described in the Log Management Data Flow is now implemented — it
fans every log record out to `run_events.jsonl` alongside the normal console/`requests.log`
output. The four level-segregated per-run log files (FR-11), the dedicated per-report
formats (FR-1/FR-4/FR-6/FR-7 across all three report types), the Quality Profile Diff Report
(FR-8), and the Server Info Reference Export (FR-9) remain unshipped.

## Overview

The sonar-migration-tool currently generates three report types (migration readiness via `internal/report/migration/`, maturity via `internal/report/maturity/`, and PDF summary via `internal/report/summary/`) in a limited set of formats. CloudVoyager provides a significantly richer reporting system with three distinct report types -- Migration Report, Executive Summary, and Performance Report -- each available in four output formats: JSON, Markdown, Plain Text, and PDF. This spec defines the enhancements needed to bring the Go tool's reporting capabilities to feature parity with CloudVoyager while building on the existing `internal/report/` package structure.

CloudVoyager's reporting pipeline is orchestrated by `src/shared/reports/index.js`, which writes all reports in a single pass after command execution completes. Text-based reports (JSON, Markdown, Plain Text) are written first via `write-text-reports.js`, followed by PDF reports via `write-pdf-reports.js`. Each report type has a dedicated formatter module (e.g., `format-markdown.js`, `format-pdf.js`, `format-markdown-executive.js`, `format-pdf-executive.js`, `format-performance.js`, `format-pdf-performance.js`). The shared helpers in `src/shared/reports/shared/` provide common computation functions: `computeProjectStats`, `computeOverallStatus`, `formatDuration`, `formatTimestamp`, `computeTotalDurationMs`, `computeTotalLoc`, `computeLocThroughput`, `getNewCodePeriodSkippedProjects`, `getProblemProjects`, and `formatNumber`.

In addition to the three core report types, CloudVoyager generates specialized reports including a Quality Profile Diff Report (comparing active rules per language between SonarQube Server and SonarQube Cloud) and a Server Info Reference export. The Go tool must also implement structured log management with per-run log directories and level-segregated log files.

## Problem Statement

The current reporting system has two critical gaps. First, it only outputs Markdown -- organizations needing formal migration documentation for compliance, audit, or executive review require PDF and JSON formats. PDF is essential for human-readable distribution; JSON is essential for programmatic consumption by CI/CD pipelines and dashboards. Second, the existing reports lack the depth of CloudVoyager's output: there is no executive summary for leadership, no performance timing analysis for capacity planning, and no quality profile diff report for verifying rule parity. Without these, migration operators must manually collate data from log files and API responses to produce comprehensive migration documentation.

## User Stories

- **As a** migration operator, **I want to** generate a detailed migration report in JSON, Markdown, Text, and PDF formats, **so that** I have comprehensive documentation of every project's migration status for audit and troubleshooting.
- **As a** technical lead, **I want to** generate an executive summary report, **so that** I can present high-level migration progress and outcomes to leadership without sharing raw technical details.
- **As a** performance engineer, **I want to** generate a performance report with timing breakdowns per pipeline stage, **so that** I can identify bottlenecks and plan capacity for future migration batches.
- **As a** quality engineer, **I want to** generate a quality profile diff report, **so that** I can verify rule parity between SonarQube Server and SonarQube Cloud after migration.
- **As a** migration operator, **I want** per-run log directories with level-segregated log files, **so that** I can quickly isolate errors and warnings from a specific migration run.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Generate Migration Report containing per-project transfer status (success/failure/skipped with reason), per-org resource migration summaries, issue and hotspot sync statistics, quality gate and profile migration results, permission and group creation results, and error details with full context | Must |
| FR-2 | Generate Executive Summary Report containing overall success rate, total projects migrated, total issues/hotspots synced, total LOC transferred, elapsed time, and throughput metrics | Must |
| FR-3 | Generate Performance Report containing per-stage timing breakdowns (extraction, build, upload, sync), per-project duration, concurrency utilization, and rate limit impact analysis | Must |
| FR-4 | Output each report type in JSON format (machine-readable, indented) | Must |
| FR-5 | Output each report type in Markdown format (human-readable, tables, headers) | Must |
| FR-6 | Output each report type in Plain Text format (no markup, fixed-width columns) | Must |
| FR-7 | Output each report type in PDF format using the existing fpdf library | Must |
| FR-8 | Generate Quality Profile Diff Report comparing active rules per language between SQ Server and SC, identifying missing rules, added rules, and parameter differences | Should |
| FR-9 | Generate Server Info Reference export containing system info, plugin versions, global settings, and webhook configurations | Should |
| FR-10 | Create per-run log directories under `migration-output/logs/` with ISO 8601 timestamps | Must |
| FR-11 | Produce four log files per run: `migration.log` (all levels), `migration.info.log`, `migration.warn.log`, `migration.error.log` | Must |
| FR-12 | Support `--verbose` flag and `LOG_LEVEL` environment variable to control log verbosity | Must |
| FR-13 | Accept `--report-format` flag to select output formats (default: all) | Should |
| FR-14 | Accept `--report-dir` flag to override default output directory | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Report generation must complete within 5 seconds for a 500-project migration | < 5s |
| NFR-2 | PDF generation must not increase binary size by more than 2 MB | < 2 MB delta |
| NFR-3 | JSON report must be valid JSON parseable by standard libraries | 100% valid |
| NFR-4 | PDF reports must render correctly on macOS, Windows, and Linux viewers | Cross-platform |
| NFR-5 | Log files must be flushed on every write to prevent data loss on crash | Immediate flush |
| NFR-6 | Reports must be generated even if migration partially failed (include error details) | Graceful degradation |

## Technical Design

### Architecture

The reporting system extends the existing `go/internal/report/` package hierarchy:

```
internal/report/
├── common/           # Existing: shared data loaders
├── maturity/         # Existing: maturity report
├── migration/        # Existing: migration readiness report
├── summary/          # Existing: PDF summary report
├── executive/        # NEW: executive summary report
│   ├── executive.go  # Data aggregation + computation
│   ├── json.go       # JSON formatter
│   ├── markdown.go   # Markdown formatter
│   ├── text.go       # Plain text formatter
│   └── pdf.go        # PDF formatter
├── performance/      # NEW: performance report
│   ├── performance.go
│   ├── json.go
│   ├── markdown.go
│   ├── text.go
│   └── pdf.go
├── profilediff/      # NEW: quality profile diff report
│   ├── diff.go       # Rule comparison engine
│   ├── json.go
│   └── markdown.go
├── serverinfo/       # NEW: server info reference export
│   └── export.go
├── formats/          # NEW: shared formatting utilities
│   ├── formatter.go  # ReportFormatter interface
│   ├── json.go       # Generic JSON writer
│   ├── markdown.go   # Markdown table/section helpers
│   ├── text.go       # Fixed-width text formatting
│   └── pdf.go        # fpdf wrapper with Sonar branding
├── orchestrator.go   # NEW: writeAllReports() entry point
└── types.go          # NEW: MigrationResults, ProjectResult, etc.
```

A `ReportFormatter` interface enables polymorphic report generation:

```go
type ReportFormatter interface {
    FormatMigrationReport(results *MigrationResults) ([]byte, error)
    FormatExecutiveSummary(results *MigrationResults) ([]byte, error)
    FormatPerformanceReport(results *MigrationResults) ([]byte, error)
}
```

Each format (JSON, Markdown, Text, PDF) implements this interface.

### Key Data Structures

```go
// MigrationResults is the top-level results container passed to all formatters.
type MigrationResults struct {
    RunID           string                  `json:"runId"`
    StartedAt       time.Time               `json:"startedAt"`
    CompletedAt     time.Time               `json:"completedAt"`
    Command         string                  `json:"command"`
    OverallStatus   string                  `json:"overallStatus"`
    Organizations   []OrgResult             `json:"organizations"`
    Performance     PerformanceMetrics      `json:"performance"`
    Config          ConfigSnapshot          `json:"config"`
}

type OrgResult struct {
    OrgKey          string          `json:"orgKey"`
    Status          string          `json:"status"`
    Projects        []ProjectResult `json:"projects"`
    GroupsCreated   int             `json:"groupsCreated"`
    GatesCreated    int             `json:"gatesCreated"`
    ProfilesCreated int             `json:"profilesCreated"`
    PermissionsSet  int             `json:"permissionsSet"`
    Errors          []ErrorDetail   `json:"errors,omitempty"`
}

type ProjectResult struct {
    ProjectKey      string          `json:"projectKey"`
    Status          string          `json:"status"`
    SkipReason      string          `json:"skipReason,omitempty"`
    IssueSync       SyncStats       `json:"issueSync"`
    HotspotSync     SyncStats       `json:"hotspotSync"`
    TransferPhase   PhaseTimings    `json:"transferPhase"`
    Errors          []ErrorDetail   `json:"errors,omitempty"`
}

type SyncStats struct {
    Total       int `json:"total"`
    Matched     int `json:"matched"`
    Transitioned int `json:"transitioned"`
    Failed      int `json:"failed"`
    Skipped     int `json:"skipped"`
}

// Note: Go's encoding/json serializes time.Duration as nanoseconds.
// For human-readable output, use int64 millisecond fields instead of time.Duration.
type PhaseTimings struct {
    ExtractionMs int64 `json:"extractionMs"`
    BuildMs      int64 `json:"buildMs"`
    UploadMs     int64 `json:"uploadMs"`
    SyncMs       int64 `json:"syncMs"`
    TotalMs      int64 `json:"totalMs"`
}

type ErrorDetail struct {
    Timestamp   time.Time `json:"timestamp"`
    Phase       string    `json:"phase"`
    Message     string    `json:"message"`
    RequestURL  string    `json:"requestUrl,omitempty"`
    StatusCode  int       `json:"statusCode,omitempty"`
    APIResponse string    `json:"apiResponse,omitempty"`
}

// Note: Go's encoding/json serializes time.Duration as nanoseconds.
// Use int64 millisecond fields for human-readable JSON output.
type PerformanceMetrics struct {
    TotalDurationMs int64              `json:"totalDurationMs"`
    TotalLOC        int64              `json:"totalLoc"`
    LOCPerSecond    float64            `json:"locPerSecond"`
    StageBreakdownMs map[string]int64  `json:"stageBreakdownMs"`
    ProjectTimings  []ProjectTiming    `json:"projectTimings"`
}
```

### Key Algorithms

**Overall Status Computation** (mirrors CloudVoyager `computeOverallStatus`):

```
function computeOverallStatus(results):
    if all projects have status == "success":
        return "success"
    if any project has status == "failed":
        if any project has status == "success":
            return "partial"
        return "failed"
    return "unknown"
```

**Project Stats Aggregation** (mirrors CloudVoyager `computeProjectStats`):

```
function computeProjectStats(results):
    stats = { total: 0, success: 0, failed: 0, skipped: 0 }
    for each org in results.organizations:
        for each project in org.projects:
            stats.total++
            stats[project.status]++
    return stats
```

**LOC Throughput Calculation** (mirrors CloudVoyager `computeLocThroughput`):

```
function computeLocThroughput(results):
    totalLOC = sum of all project LOC values
    totalSeconds = results.completedAt - results.startedAt (in seconds)
    if totalSeconds == 0: return 0
    return totalLOC / totalSeconds
```

**Quality Profile Diff Algorithm**:

```
function diffProfiles(serverRules, cloudRules):
    diffs = {}
    for each language in union(serverRules.keys, cloudRules.keys):
        serverSet = set of ruleKeys in serverRules[language]
        cloudSet = set of ruleKeys in cloudRules[language]
        diffs[language] = {
            missing: serverSet - cloudSet,    // in server, not in cloud
            added: cloudSet - serverSet,      // in cloud, not in server
            common: serverSet & cloudSet,
            serverCount: len(serverSet),
            cloudCount: len(cloudSet),
        }
    return diffs
```

### Data Flow

1. Migration command completes, producing a `MigrationResults` struct.
2. `orchestrator.WriteAllReports(results, outputDir)` is called.
3. Orchestrator creates the output directory (timestamped subdirectory).
4. Orchestrator iterates over enabled formats (JSON, Markdown, Text, PDF).
5. For each format, instantiate the corresponding `ReportFormatter`.
6. Call `FormatMigrationReport`, `FormatExecutiveSummary`, `FormatPerformanceReport`.
7. Write each output to the appropriate file path.
8. If Quality Profile Diff was collected, write `quality-profile-diff.json` and `.md`.
9. If Server Info Reference was collected, write `server-info-reference.json`.
10. Log all generated report file paths.

### Log Management Data Flow

1. At command startup, create a timestamped log directory: `migration-output/logs/YYYY-MM-DDTHH-mm-ss-FFFZ/`.
2. Initialize four `slog.Handler` instances, each filtering to its respective level(s).
3. Wrap Go's `slog.Default()` with a `tee` handler that fans out to all four file handlers plus stdout.
4. Each handler writes to its respective file with immediate flush (`os.File.Sync()`).
5. On command completion, close all file handles.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/qualityprofiles/search` | GET | Fetch active profiles per language (for profile diff) |
| `/api/rules/search` | GET | Fetch active rules per profile (for profile diff) |
| `/api/system/info` | GET | Fetch server info for reference export |
| `/api/plugins/installed` | GET | Fetch installed plugins for reference export |
| `/api/settings/values` | GET | Fetch global settings for reference export |
| `/api/webhooks/list` | GET | Fetch webhooks for reference export |

## Acceptance Criteria

- [ ] AC-1: Running `migrate` generates `migration-report.json`, `migration-report.md`, `migration-report.txt`, and `migration-report.pdf` in the output directory.
- [ ] AC-2: Running `migrate` generates `executive-summary.md`, `executive-summary.pdf` in the output directory.
- [ ] AC-3: Running `migrate` generates `performance-report.md`, `performance-report.pdf` in the output directory.
- [ ] AC-4: JSON reports parse without error using `encoding/json` and contain all required fields.
- [ ] AC-5: PDF reports open correctly in Preview (macOS), Adobe Reader (Windows), and Evince (Linux).
- [ ] AC-6: Each migration run creates a timestamped log directory with four log files.
- [ ] AC-7: `migration.error.log` contains only ERROR-level messages; `migration.warn.log` contains only WARN-level messages.
- [ ] AC-8: `--verbose` flag increases log output to DEBUG level across all log files.
- [ ] AC-9: Quality profile diff report correctly identifies missing, added, and common rules per language when source and target profiles are provided.
- [ ] AC-10: Server info reference export contains system version, edition, plugins, and global settings.
- [ ] AC-11: Reports generate successfully even when some projects have failed status (graceful degradation).
- [ ] AC-12: Report generation completes within 5 seconds for a 500-project result set.
- [ ] AC-13: `--report-format` flag selects output formats (e.g., `--report-format json`, `--report-format pdf`); default generates all formats.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Report orchestrator | `src/shared/reports/index.js` |
| Text report writer | `src/shared/reports/helpers/write-text-reports.js` |
| PDF report writer | `src/shared/reports/helpers/write-pdf-reports.js` |
| Migration report (Markdown) | `src/shared/reports/format-markdown.js` |
| Migration report (Text) | `src/shared/reports/format-text.js` |
| Migration report (PDF) | `src/shared/reports/format-pdf.js` |
| Executive summary (Markdown) | `src/shared/reports/format-markdown-executive.js` |
| Executive summary (PDF) | `src/shared/reports/format-pdf-executive.js` |
| Performance report (Markdown) | `src/shared/reports/format-performance.js` |
| Performance report (PDF) | `src/shared/reports/format-pdf-performance.js` |
| PDF helpers | `src/shared/reports/pdf-helpers.js` |
| PDF sections (migration) | `src/shared/reports/pdf-sections.js` |
| PDF sections (executive) | `src/shared/reports/pdf-exec-sections.js` |
| PDF sections (performance) | `src/shared/reports/pdf-perf-sections.js` |
| Performance tables | `src/shared/reports/perf-tables.js` |
| Shared helpers | `src/shared/reports/shared/` |

## Known Limitations

- The Go fpdf library does not support Unicode fonts by default; CJK characters in project names may render as `?`. Consider embedding a Unicode-capable font if international project names are expected.
- PDF generation is synchronous and single-threaded; very large result sets (5000+ projects) may take several seconds. Parallelizing PDF section generation is non-trivial due to page layout dependencies.
- The `slog` standard library does not natively support multi-writer fan-out; a custom `tee` handler must be implemented or a third-party library (e.g., `slog-multi`) adopted.
- Quality Profile Diff requires API access to both SQ Server and SC simultaneously, which may not be available in offline/air-gapped scenarios.

## Open Questions

- Should the `--report-format` flag accept a comma-separated list (e.g., `--report-format json,pdf`) or use repeated flags (`--report-format json --report-format pdf`)?
- Should the performance report include network-level metrics (bytes transferred, request counts) or only timing data?
- Should PDF reports include the Sonar logo and branding, and if so, how should the logo asset be embedded (compile-time embed via `//go:embed` or runtime asset path)?
- Should log rotation be implemented for long-running multi-batch migrations, or is per-run directory isolation sufficient?
