# Architecture
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

## Overview
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

sonar-migration-tool is a Go CLI application built with [Cobra](https://github.com/spf13/cobra). It compiles to a single static binary with no runtime dependencies. Its purpose is to migrate configurations, source code, issues, and history from SonarQube Server to SonarQube Cloud.

## Project Structure
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

The repository contains two Go modules:

```
sonar-migration-tool/
├── go/                          # Migration tool (main binary)
│   ├── main.go                  # Entry point
│   ├── cmd/                     # Cobra command definitions
│   │   ├── root.go              # Root command + global flags
│   │   ├── wizard.go            # Interactive guided migration
│   │   ├── extract.go           # Phase 1: Extract from SonarQube Server
│   │   ├── structure.go         # Phase 2: Generate org/project structure
│   │   ├── mappings.go          # Phase 3: Generate entity mappings
│   │   ├── migrate.go           # Phase 4: Push to SonarQube Cloud
│   │   ├── reset.go             # Delete all migrated content
│   │   ├── report.go            # Maturity/migration reports
│   │   ├── analysis_report.go   # API call outcome summary
│   │   ├── gui.go               # Browser-based GUI server
│   │   ├── predictive_report.go # Pre-migration PDF summary
│   │   ├── transfer.go          # Single-command project transfer
│   │   └── regtest.go           # Exhaustive post-migration regression verification
│   └── internal/
│       ├── common/              # Shared utilities
│       │   ├── rawclient.go     # HTTP client with auth + retry
│       │   ├── store.go         # DataStore (JSONL read/write)
│       │   ├── writer.go        # ChunkWriter (batched JSONL output)
│       │   ├── planner.go       # Topological sort task planner
│       │   ├── edition.go       # SonarQube edition detection
│       │   └── helpers.go       # CSV, JSON, string utilities
│       ├── extract/             # Server data extraction (67 tasks)
│       │   ├── extract.go       # Orchestrator
│       │   ├── planner.go       # Task dependency graph
│       │   └── tasks_*.go       # Typed extraction tasks by category
│       ├── structure/           # Org/project/profile/gate mapping
│       │   ├── structure.go     # Structure command logic
│       │   ├── csv.go           # CSV load/export
│       │   └── types.go         # Organization, Project, etc.
│       ├── migrate/             # Cloud migration (44+ tasks)
│       │   ├── migrate.go       # Orchestrator
│       │   ├── planner.go       # Task dependency graph
│       │   ├── reset.go         # Delete/reset tasks
│       │   └── tasks_*.go       # Typed migration tasks by category
│       ├── wizard/              # Interactive wizard (6 phases)
│       │   ├── wizard.go        # Phase loop + resume logic
│       │   ├── phases.go        # Phase implementations
│       │   ├── state.go         # WizardState persistence (JSON)
│       │   ├── prompter.go      # Prompter interface
│       │   ├── cli_prompter.go  # Terminal UI (survey library)
│       │   └── helpers.go       # Phase sequence, validation
│       ├── regtest/             # Exhaustive post-migration regression verification
│       │   ├── suite.go         # Test suite orchestrator (parallel check runner)
│       │   ├── checks.go        # 43 check functions covering all entity types
│       │   ├── helpers.go       # API query helpers and result constructors
│       │   └── report.go        # Report formatting (table, JSON, markdown)
│       ├── pipeline/            # Version-specific extraction pipelines (SPEC-011)
│       │   ├── pipeline.go      # Pipeline interface + normalized types
│       │   ├── helpers.go       # Shared paginated HTTP helpers
│       │   ├── router.go        # Version detection + pipeline selection
│       │   ├── sq99.go          # SQ 9.9 LTS pipeline
│       │   ├── sq100.go         # SQ 10.0-10.3 pipeline
│       │   ├── sq104.go         # SQ 10.4-10.8 pipeline
│       │   └── sq2025.go        # SQ 2025.1+ pipeline
│       ├── report/              # Report generation
│       │   ├── common/          # Data loaders (JSONL → report rows)
│       │   ├── maturity/        # SonarQube maturity report
│       │   ├── migration/       # Migration readiness report
│       │   ├── summary/         # Predictive report summary (collect.go, pdf.go, types.go)
│       │   ├── markdown.go      # Markdown rendering
│       │   └── jsonpath.go      # JSON path extraction
│       ├── analysis/            # API call analysis (requests.log → CSV)
│       ├── gui/                 # WebSocket-backed browser GUI server
│       ├── predict/             # Predictive report engine (no Cloud API calls)
│       ├── scanreport/          # Protobuf scan report builder + submitter
│       └── version/             # Tool name + version constants
├── lib/
│   └── sq-api-go/               # Typed SonarQube API binding library
│       ├── client.go            # Client factory (Server + Cloud)
│       ├── auth.go              # Auth strategies (Basic, Bearer, mTLS)
│       ├── pagination.go        # Generic paginator
│       ├── retry.go             # Exponential backoff
│       ├── server/              # SonarQube Server API methods
│       ├── cloud/               # SonarQube Cloud API methods
│       └── types/               # Shared response structs
├── examples/                    # JSON config file examples
├── scripts/                     # Automation scripts
└── docs/                        # Documentation
```

## Task Engine
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

Both `extract` and `migrate` use a typed task engine with topological sort planning.

### How It Works
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

1. **Task registration** — Each task is a Go function with typed dependencies and a defined execution function. Tasks declare which other tasks they depend on.

2. **Dependency resolution** — `common.Planner` builds a directed acyclic graph (DAG) from task dependencies and produces an ordered execution plan using topological sort.

3. **Phased execution** — Tasks are grouped into phases where all tasks in a phase can run concurrently. Each phase completes before the next begins.

4. **Data flow** — Tasks read input from a `DataStore` (which loads JSONL files from previous tasks) and write output via a `ChunkWriter` (which produces JSONL files for downstream tasks).

### Extract Tasks (68 tasks)
<!-- updated: 2026-06-04_15:30:00 -->

Organized by category in `go/internal/extract/tasks_*.go`:
- **System** — Server version, edition, plugins
- **Projects** — Project list, tags, settings, branches, pull requests
- **Profiles** — Quality profiles, rules, inheritance
- **Gates** — Quality gates, conditions, associations
- **Users/Groups** — Users, groups, memberships
- **Templates** — Permission templates, associated groups/users
- **Views** — Portfolios, applications (Enterprise+ only)
- **Issues** — Accepted issues, safe hotspots
- **Project Data** — `getProjectIssuesFull` (issues with comments/tags/flows), `getProjectHotspotsFull` (hotspots with review details), `getProjectVersions` (current project version per branch via `/api/navigation/component`), component trees (using `FIL,UTS` qualifiers for files and unit test source files), source code, SCM data. External issues (ruff, pylint, flake8, etc.) are extracted alongside native issues. Runs by default; skipped when `--skip_project_data_migration` is set.
- **Webhooks** — Global and project-level webhooks

### Migrate Tasks (44+ tasks)
<!-- updated: 2026-06-05_19:20:00 -->

Organized by category in `go/internal/migrate/tasks_*.go`:
- **Create** — Projects, groups, quality gates, quality profiles, permission templates, portfolios
- **Configure** — Gate conditions, project settings, new code periods, default profiles/gates
- **Associate** — Profile-to-project, gate-to-project, group memberships
- **Permissions** — Template permissions, project permissions
- **Rules** — Custom rule activation
- **ALM** — DevOps platform binding detection
- **Project Data** — Import scan reports via reconstructed protobuf format (native issues, external issues via ExternalIssue protobuf, hotspots mapped to issues). BackdateChangesets mechanism preserves original issue creation dates; external issues are included in changeset backdating alongside native issues. Project version (`sonar.projectVersion`) is migrated from SonarQube Server to SonarQube Cloud: the extracted version is set in both the protobuf metadata and the CE submit form, falling back to `"1.0.0"` if unavailable (matching CloudVoyager behavior). Harvested from CloudVoyager's `resolve-source-project-version.js`. **Multi-branch handling:** Branches are sorted main-first via `sortBranchesMainFirst()`. The main branch is imported first and its CE task awaited; each non-main branch is then migrated as a **long-lived branch with full issue history** — `buildBranchReport` first performs SonarQube Cloud's "Create analysis" handshake (`PreCreateAnalysis` → `POST {api-host}/analysis/analyses` with `branchType=long`) to register the branch and obtain an `analysisUuid`, which is stamped into `metadata.analysis_uuid` (proto field 19) so the CE binds the report to the branch (without it the CE accepts the report but creates no branch). If the main branch CE task fails, remaining branches are skipped; a branch whose source is no longer retrievable on the server is also skipped with a clear message. Supports `ExcludeBranches` glob patterns to skip non-main branches, and per-branch checkpoint/resume via `loadCompletedBranches()`/`shouldSkipBranch()`. Project-level concurrency uses `errgroup.WithContext` + `SetLimit`.
- **Issue Metadata Sync** — `syncIssueMetadata`: two-phase task that waits for Cloud indexing, matches source→cloud issues by composite key (rule|filePath|line), then syncs status transitions (with fallback transition paths), comments, and tags per matched pair. Idempotent via `metadata-synchronized` tag. Runs by default; skipped when `--skip_project_data_migration` is set.
- **Hotspot Metadata Sync** — `syncHotspotMetadata`: same two-phase pattern, matches source→cloud hotspots by composite key, syncs REVIEWED status/resolution and comments. Idempotent. Runs by default; skipped when `--skip_project_data_migration` is set.
- **Global Settings** — Migrates only SQS-supported settings; `sonar.dbcleaner.branchesToKeepWhenInactive` is migrated as a regex on SonarQube Cloud
- **Delete/Reset** — Cleanup tasks for the `reset` command

## Data Flow
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

```
SonarQube Server API
    | extract (typed tasks → JSONL)
    v
JSONL files in files/<extract_id>/
    | structure
    v
organizations.csv + projects.csv
    | (user fills in sonarcloud_org_key)
    | mappings
    v
gates.csv, profiles.csv, groups.csv, templates.csv, portfolios.csv
    | migrate (typed tasks → Cloud API)
    v
SonarQube Cloud API
```

## API Binding Library (sq-api-go)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

The `lib/sq-api-go/` module provides typed Go methods for SonarQube Server and Cloud APIs. Key features:

- **Dual client** — `NewServerClient()` for Server, `NewCloudClient()` for Cloud
- **Version-aware auth** — Basic auth (Server < 10), Bearer token (Server 10+, Cloud)
- **mTLS support** — Client certificate authentication
- **Automatic pagination** — Handles `p`/`ps` pagination parameters
- **Retry with backoff** — 3 attempts with exponential backoff
- **Cloud API clients** — `IssuesClient` and `HotspotsClient` in `cloud/` provide typed methods for Cloud issue/hotspot search, transitions, comments, and tags

## Version-Specific Pipeline Architecture (SPEC-011)
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

`go/internal/pipeline/` implements four version-specific extraction pipelines selected once at startup via a version router. No runtime version branching occurs inside the extraction or build phases.

```
go/internal/pipeline/
├── pipeline.go       # Pipeline interface + normalized types (Issue, Hotspot, Measure, Group)
├── helpers.go        # Shared paginated HTTP helpers + paginateAll[T] generic
├── helpers_test.go   # paginateAll tests + fetchAllMetrics batch + fetch error path tests
├── router.go         # DetectPipeline(): calls /api/server/version, parses, selects
├── router_test.go    # Version parsing, routing, interface compliance, parameter tests
├── shared.go         # standardPipeline: common fields + all shared extraction methods
├── shared_test.go    # Tests for all standardPipeline extraction methods (issues, hotspots, metrics, groups, EnrichCleanCode)
├── sq99.go           # SQ 9.9 LTS — config only (Version method)
├── sq100.go          # SQ 10.0-10.3 — config only (Version method)
├── sq104.go          # SQ 10.4-10.8 — config only (Version method)
├── sq2025.go         # SQ 2025.1+ — overrides ExtractIssues (IN_SANDBOX) + ExtractGroups (V2)
└── sq2025_test.go    # V2 groups fallback tests (200/404/5xx) + IN_SANDBOX filter test
```

**Inheritance model:** All four pipelines embed `standardPipeline`, which holds `issueSearchParam`, `issueStatusValues`, and `metricBatchSize` as data fields set per-version in the constructor. `standardPipeline` provides promoted implementations of `IssueSearchParam()`, `IssueStatusValues()`, `SupportsMetricBatching()`, `ExtractIssues()`, `ExtractMetrics()`, `ExtractHotspots()`, `ExtractGroups()`, and `EnrichCleanCode()`. SQ 9.9, 10.0, and 10.4 define only their constructor and `Version()` method — all extraction logic is inherited. SQ 2025 overrides `ExtractIssues` (to filter IN_SANDBOX) and `ExtractGroups` (to use the V2 API with fallback).

**Key behaviors per pipeline:**

| Feature | SQ 9.9 | SQ 10.0-10.3 | SQ 10.4-10.8 | SQ 2025.1+ |
|---------|--------|-------------|-------------|-----------|
| Issue param | `statuses` | `statuses` | `issueStatuses` | `issueStatuses` |
| Metric batching | 15 keys | 15 keys | 15 keys | None (single request) |
| Groups API | `/api/user_groups/search` | same | same | V2 + standard fallback |
| IN_SANDBOX | N/A | N/A | N/A | Logged + skipped |
| Clean Code | SPEC-012 stub | Native | Native | Native |

**Forward compatibility:** An unknown major version ≥ 11 falls back to the SQ 10.4 pipeline with a `WARN` log. An error is returned for versions < 9.9.

All four pipelines implement the `Pipeline` interface; compile-time checks (`var _ Pipeline = (*SQ99Pipeline)(nil)`) enforce this. Since SQ 9.9, 10.0, and 10.4 inherit all extraction methods from `standardPipeline`, their source files contain only a constructor and `Version()` — all runtime behavior is defined in `shared.go` and `helpers.go`.

**URL construction:** All helpers use `client.BaseURL() + "api/..."` (no leading `/`). `BaseURL()` always returns a URL ending with `/` (enforced by `normalizeBaseURL` in sq-api-go), so prepending a `/` would produce double-slash paths (`//api/...`) that Go's `httptest` server does not normalize.

**V2 groups note:** The `/api/v2/authorizations/groups` response omits `membersCount`. The `id` field is a UUID string (incompatible with `Group.ID int`, left zero); `managed` has no `Group` field and is discarded; `default` IS captured and propagated to `Group.Default`. The standard-API fallback is triggered by any V2 error (not just 404), intentionally ensuring callers get groups even when the V2 endpoint is temporarily unavailable.

## Transfer Command (Single-Project Migration)
<!-- updated: 2026-06-05_14:00:00 -->

`go/cmd/transfer.go` provides a single-command migration path that chains the four
manual phases automatically so users never touch a CSV file. Flag names are defined
as package-level constants (e.g. `flagSourceURL = "source_url"`) to avoid duplicated
string literals. Config resolution is handled by `resolveTransferConfig()` — it calls
`loadTransferFileDefaults()` (which reuses `extract.LoadExtractConfigFile` and
`migrate.LoadMigrateConfigFile` so transfer accepts the same unified config shape as
the other actions, issue #295) and then applies CLI overrides via
`applyFlagString`/`applyFlagInt`/`applyFlagBool` helpers. Validation lives in
`validateTransferConfig()`, keeping `runTransfer` focused on four-phase orchestration.

```bash
# Flags
sonar-migration-tool transfer \
  --source_url https://sonarqube.example.com --source_token sqp_xxx \
  --project_key my-project \
  --target_token squ_xxx --default_organization my-org

# Config file
sonar-migration-tool transfer -c config.json --project_key my-project
```

**config.json** (same unified shape as `extract` / `migrate`):
```json
{
  "source": { "url": "...", "token": "..." },
  "target": { "url": "...", "token": "...",
              "default_organization": "...", "enterprise_key": "..." }
}
```

`--project_key` is optional and lives on the CLI only. When provided, the
`/api/projects/search` call is filtered server-side via the `projects=` param, so only
the target project and its data are extracted. When omitted, all projects on the server
are migrated.

**Execution sequence:**
1. `extract.RunExtract` — sets `ExtractConfig.ProjectKeys` when `--project_key` is given
2. `structure.RunStructure(dir, defaultOrg)` — pre-populates `sonarcloud_org_key` in organizations.csv
3. `structure.RunMappings(dir)` — generates gates/profiles/groups/templates CSVs
4. `migrate.RunMigrate` — pushes everything to SonarQube Cloud
5. `summary.GenerateReports` — collects run instrumentation once and writes **both** `migration_summary.pdf` and `migration_summary.md` to the run directory. (`summary.GeneratePDFReport` is retained as a back-compat wrapper that returns only the PDF path.)

`--enterprise_key` is optional and defaults to `--default_organization`. Set it explicitly
only when the SonarCloud enterprise key differs from the organization key (typically only
needed for portfolio migration in large Enterprise deployments).

### Run Instrumentation & Reporting
<!-- updated: 2026-06-05_14:00:00 -->

The migrate engine instruments every run so the summary report can explain what happened —
including when the run fails.

- **Tee slog handler → `run_events.jsonl`** — a `slog.Handler` is teed onto the default
  logger so that, in addition to the normal console/`requests.log` output, every log record
  is mirrored into `run_events.jsonl` in the run directory. The file is JSON Lines: one
  object per line (written with a `json.Encoder`), each shaped as
  `{time:RFC3339Nano, level:INFO|WARN|ERROR, message:string, attrs:object}` where `attrs`
  is the flattened set of slog attributes for that record. The summary collector parses
  these events back out (matching on known `message` strings and attribute keys) to
  reconstruct per-branch packaging, CE submissions, the create-analysis handshake, retries,
  skipped branches, and quality-gate metric remaps.
- **Per-phase / per-task timing → `run_meta.json`** — the engine records the wall-clock
  duration of each phase and each task as it executes, then writes a single
  `run_meta.json` object (via `json.MarshalIndent`, two-space indent) to the run directory.
  It carries `started_at` / `completed_at` (RFC3339), an `overall_status`
  (`success` | `partial` | `failed`), a `phases` array (`{index, tasks, duration_seconds}`),
  and a `tasks` array (`{phase, name, duration_seconds, ok, err}`).
- **Failed runs still report** — `run_meta.json` is written on **every** run, not only
  successful ones. When the migration fails, the engine still emits `run_meta.json` with
  `overall_status=failed` (and per-task `ok:false` / `err` fields populated), so
  `GenerateReports` can render a `migration_summary.{pdf,md}` that explains the failure
  instead of producing nothing. This satisfies SPEC-022 NFR-6 (graceful degradation).

The reporting types live in two packages. `package migrate` (file `eventlog.go`) defines the
on-disk JSON shapes (`LogEvent`, `PhaseTiming`, `TaskTiming`, `RunMeta`) with JSON struct
tags matching the field names above. `package summary` (file `types.go`) defines the
in-memory aggregation appended to `MigrationSummary` (timing, failure rows, a warning ledger
covering retries / branch skips / gate-condition skips / metric remaps, per-branch stats, and
throughput totals), and `generate.go` exposes `GenerateReports(runDir, outputDir, exportDir)`
which collects once and renders both the PDF and Markdown outputs.

## Version Detection
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

The tool auto-detects SonarQube Server version and edition:

- **Server < 10:** Basic authentication (username:token)
- **Server >= 10:** Bearer token authentication
- **Version-specific pipelines:** `pipeline.DetectPipeline()` calls `GET /api/server/version` (plain-text response) and selects one of four pipeline implementations. Authentication is injected automatically via the `authTransport` RoundTripper inside the `sqapi.Client`'s HTTP client.
- **Edition-aware:** Tasks are filtered by edition — portfolio-related tasks only run on Enterprise and Data Center editions
- **Edition detection fallback:** When `/api/system/info` returns 403 (non-admin token), edition detection falls back to `/api/navigation/global` to extract the edition from the response

## Configuration
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

Commands accept flags, positional arguments, or a JSON config file (`--config path/to/config.json`). CLI flags override config file values. See [`docs/ADVANCED-CONFIG.md`](ADVANCED-CONFIG.md) for the full reference.

## Browser-Based GUI
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

`go/internal/gui/` implements a local HTTP server that serves a React-based single-page application for running the full wizard workflow in a browser.

- **WebSocket event stream** — real-time progress events pushed from the Go backend to the browser as each migration phase runs
- **Wizard stepper** — same 6-phase sequence as the CLI wizard
- **Run history** — lists past extraction/migration run IDs with their status
- **CSV viewers** — displays mapping files (`organizations.csv`, `gates.csv`, etc.) in-browser
- **Report viewers** — renders migration and maturity reports
- **Dark/light theme** — persisted in browser localStorage

The `gui` command (`go/cmd/gui.go`) starts the server on `localhost:0` (auto-assigned port) and opens the browser automatically. Pass `--no-browser` to suppress auto-open, or `--addr` to bind to a specific address.

## Predictive Report Engine
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

`go/internal/predict/` generates a PDF migration summary from local data only — no SonarQube Cloud API calls are made. The engine reads from files produced by `extract`, `structure`, and `mappings` and predicts how the migration will go.

### Architecture
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- **`BuildPredictiveRun`** — reads JSONL extract files and mapping CSVs; produces a `PredictiveRun` struct
- **`CollectSummary`** — walks `PredictiveRun` and classifies each entity using a five-status taxonomy
- **`RenderPDF`** — writes the summary to `<export_directory>/predictive_migration_summary.pdf`

Report summary logic lives in `go/internal/report/summary/` (`collect.go`, `pdf.go`, `types.go`).

### Five-Status Taxonomy
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

| Status | Color | Meaning |
|--------|-------|---------|
| Succeeded | Green | Entity will migrate without issues |
| Near Perfect | Yellow | Minor gaps detected (e.g. unsupported settings) |
| Partial | Amber | Some sub-entities will fail |
| Failed | Red | Entity cannot migrate as-is |
| Skipped | Grey | Entity explicitly excluded |

### Limitations
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

Two outcome classes cannot be predicted ahead of time and are omitted from the predictive report:

- **SonarQube Cloud API errors** — rate limiting, auth failures, or transient network errors
- **Global settings** — the list of SQC-supported settings is discovered dynamically at migrate time

## Testing
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- **Framework:** stdlib `testing` + `net/http/httptest` for HTTP mocking
- **Run tests:** `cd go && go test ./... -count=1`
- **Coverage:** `cd go && go test ./... -coverprofile=coverage.out`

## Roadmap: Data Migration Specs
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

The `roadmap/specs/` directory contains 25 PRD specifications for harvesting data migration features from CloudVoyager (the Node.js predecessor). These specs provide a complete blueprint for building full SonarQube Server → Cloud migration — including issues, hotspots, source code, measures, and all metadata — via a reconstructed SonarScanner protobuf report format.

See [roadmap/README.md](../roadmap/README.md) for the full spec index, dependency graph, priority matrix, and implementation strategy.

| Epic | Specs | Priority | Summary |
|------|-------|----------|---------|
| Core Data Migration | SPEC-001 through SPEC-005 | P0 | Scanner protocol engine, issues, hotspots, source code, measures |
| Scale & Reliability | SPEC-006 through SPEC-010 | P0/P1 | >10K handling, batch distribution, metadata sync, user mapping |
| Version Compatibility | SPEC-011 through SPEC-013 | P1 | Multi-version pipelines, Clean Code attributes, external issues |
| Performance | SPEC-014 through SPEC-016 | P1 | Parallel sync, auto-tuning, rate limiting |
| Migration Workflow | SPEC-017 through SPEC-020 | P1/P2 | Checkpoint/resume, multi-org mapping, CSV filtering, branches |
| Verification & Reporting | SPEC-021, SPEC-022 | P1/P2 | Migration verification, comprehensive reporting |
| User Experience | SPEC-023 through SPEC-025 | P2/P3 | Desktop app, sync-metadata command, config validation |

### Issue #102: Project Version Migration
<!-- updated: 2026-06-04_15:30:00 -->

The migration tool now migrates `sonar.projectVersion` from SonarQube Server to SonarQube Cloud during project data import. The `getProjectVersions` extract task fetches the current project version per branch via `/api/navigation/component`. During project data import, the extracted version is passed to both the protobuf metadata and the CE submit form. Falls back to `"1.0.0"` if the version is not available (matching CloudVoyager behavior). This feature was harvested from CloudVoyager's `resolve-source-project-version.js`. Runs by default; skipped when `--skip_project_data_migration` is set.

### Issue #104: Migrate All Issues (Implementation Status)
<!-- updated: 2026-06-04_15:30:00 -->

Full end-to-end issue and hotspot migration pipeline. Current status by phase:

| Phase | Task | Status | Notes |
|-------|------|--------|-------|
| Extract | `getProjectIssuesFull` | Complete | Extracts issues with comments, tags, flows. Live-verified against SQ Enterprise 2026.2.0 |
| Extract | `getProjectHotspotsFull` | Complete | Extracts hotspots with review details. Live-verified against SQ Enterprise 2026.2.0 |
| Project Data Import | Protobuf report builder | Complete | Native issues, external issues (via ExternalIssue protobuf classification), hotspots mapped to issues |
| Migrate | `syncIssueMetadata` | Complete | Composite key matching (rule\|filePath\|line), fallback status transitions, comment sync, tag sync, idempotent via `metadata-synchronized` tag |
| Migrate | `syncHotspotMetadata` | Complete | Composite key matching, REVIEWED status/resolution sync, comment sync, idempotent |
| Cloud API | `IssuesClient` | Complete | `lib/sq-api-go/cloud/` — search, transitions, comments, tags |
| Cloud API | `HotspotsClient` | Complete | `lib/sq-api-go/cloud/` — search, status changes, comments |
| Infrastructure | Edition detection fallback | Complete | `/api/navigation/global` fallback when `/api/system/info` returns 403 (non-admin tokens) |
| Testing | Unit tests + race detector | Passing | All unit tests pass, race detector clean |

**ReferenceBranchName fix:** `MetadataInput` now includes a `ReferenceBranchName` field. `BuildMetadata` sets `ReferenceBranchName` on the protobuf `Metadata` message, defaulting to `BranchName` if not explicitly provided. This matches CloudVoyager's behavior where `referenceBranchName` is always set (defaults to `branchName`). Without this field, SonarCloud's Compute Engine rejected the protobuf report with a generic processing error.
