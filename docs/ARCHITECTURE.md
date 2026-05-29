# Architecture

## Overview

sonar-migration-tool is a Go CLI application built with [Cobra](https://github.com/spf13/cobra). It compiles to a single static binary with no runtime dependencies. Its purpose is to migrate configurations from SonarQube Server to SonarQube Cloud.

## Project Structure

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
│   │   └── analysis_report.go   # API call outcome summary
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
│       │   ├── markdown.go      # Markdown rendering
│       │   └── jsonpath.go      # JSON path extraction
│       └── analysis/            # API call analysis (requests.log → CSV)
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

Both `extract` and `migrate` use a typed task engine with topological sort planning.

### How It Works

1. **Task registration** — Each task is a Go function with typed dependencies and a defined execution function. Tasks declare which other tasks they depend on.

2. **Dependency resolution** — `common.Planner` builds a directed acyclic graph (DAG) from task dependencies and produces an ordered execution plan using topological sort.

3. **Phased execution** — Tasks are grouped into phases where all tasks in a phase can run concurrently. Each phase completes before the next begins.

4. **Data flow** — Tasks read input from a `DataStore` (which loads JSONL files from previous tasks) and write output via a `ChunkWriter` (which produces JSONL files for downstream tasks).

### Extract Tasks (67 tasks)
<!-- updated: 2026-05-26_17:30:00 -->

Organized by category in `go/internal/extract/tasks_*.go`:
- **System** — Server version, edition, plugins
- **Projects** — Project list, tags, settings, branches, pull requests
- **Profiles** — Quality profiles, rules, inheritance
- **Gates** — Quality gates, conditions, associations
- **Users/Groups** — Users, groups, memberships
- **Templates** — Permission templates, associated groups/users
- **Views** — Portfolios, applications (Enterprise+ only)
- **Issues** — Accepted issues, safe hotspots
- **Scan History** — `getProjectIssuesFull` (issues with comments/tags/flows), `getProjectHotspotsFull` (hotspots with review details), component trees, source code, SCM data (requires `--include-scan-history`)
- **Webhooks** — Global and project-level webhooks

### Migrate Tasks (44+ tasks)
<!-- updated: 2026-05-26_17:30:00 -->

Organized by category in `go/internal/migrate/tasks_*.go`:
- **Create** — Projects, groups, quality gates, quality profiles, permission templates, portfolios
- **Configure** — Gate conditions, project settings, new code periods, default profiles/gates
- **Associate** — Profile-to-project, gate-to-project, group memberships
- **Permissions** — Template permissions, project permissions
- **Rules** — Custom rule activation
- **ALM** — DevOps platform binding detection
- **Scan History** — Import scan reports via reconstructed protobuf format (native issues, external issues via ExternalIssue protobuf, hotspots mapped to issues)
- **Issue Metadata Sync** — `syncIssueMetadata`: two-phase task that waits for Cloud indexing, matches source→cloud issues by composite key (rule|filePath|line), then syncs status transitions (with fallback transition paths), comments, and tags per matched pair. Idempotent via `metadata-synchronized` tag. Requires `--include-scan-history`.
- **Hotspot Metadata Sync** — `syncHotspotMetadata`: same two-phase pattern, matches source→cloud hotspots by composite key, syncs REVIEWED status/resolution and comments. Idempotent. Requires `--include-scan-history`.
- **Delete/Reset** — Cleanup tasks for the `reset` command

## Data Flow

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
<!-- updated: 2026-05-26_17:30:00 -->

The `lib/sq-api-go/` module provides typed Go methods for SonarQube Server and Cloud APIs. Key features:

- **Dual client** — `NewServerClient()` for Server, `NewCloudClient()` for Cloud
- **Version-aware auth** — Basic auth (Server < 10), Bearer token (Server 10+, Cloud)
- **mTLS support** — Client certificate authentication
- **Automatic pagination** — Handles `p`/`ps` pagination parameters
- **Retry with backoff** — 3 attempts with exponential backoff
- **Cloud API clients** — `IssuesClient` and `HotspotsClient` in `cloud/` provide typed methods for Cloud issue/hotspot search, transitions, comments, and tags

## Version-Specific Pipeline Architecture (SPEC-011)
<!-- updated: 2026-05-29_02:30:00 -->

`go/internal/pipeline/` implements four version-specific extraction pipelines selected once at startup via a version router. No runtime version branching occurs inside the extraction or build phases.

```
go/internal/pipeline/
├── pipeline.go      # Pipeline interface + normalized types (Issue, Hotspot, Measure, Group)
├── helpers.go       # Shared paginated HTTP helpers (issues, hotspots, metrics, groups)
├── router.go        # DetectPipeline(): calls /api/server/version, parses, selects
├── router_test.go   # Version parsing, routing, interface compliance, parameter tests
├── sq99.go          # SQ 9.9 LTS — statuses param, 15-key batching
├── sq100.go         # SQ 10.0-10.3 — statuses param, 15-key batching
├── sq104.go         # SQ 10.4-10.8 — issueStatuses param, 15-key batching
└── sq2025.go        # SQ 2025.1+ — issueStatuses, no batching, V2 groups, IN_SANDBOX filter
```

**Key behaviors per pipeline:**

| Feature | SQ 9.9 | SQ 10.0-10.3 | SQ 10.4-10.8 | SQ 2025.1+ |
|---------|--------|-------------|-------------|-----------|
| Issue param | `statuses` | `statuses` | `issueStatuses` | `issueStatuses` |
| Metric batching | 15 keys | 15 keys | 15 keys | None (single request) |
| Groups API | `/api/user_groups/search` | same | same | V2 + standard fallback |
| IN_SANDBOX | N/A | N/A | N/A | Logged + skipped |
| Clean Code | SPEC-012 stub | Native | Native | Native |

**Forward compatibility:** An unknown major version ≥ 11 falls back to the SQ 10.4 pipeline with a `WARN` log. An error is returned for versions < 9.9.

All four pipelines implement the `Pipeline` interface; compile-time checks (`var _ Pipeline = (*SQ99Pipeline)(nil)`) enforce this.

## Version Detection
<!-- updated: 2026-05-29_02:30:00 -->

The tool auto-detects SonarQube Server version and edition:

- **Server < 10:** Basic authentication (username:token)
- **Server >= 10:** Bearer token authentication
- **Version-specific pipelines:** `pipeline.DetectPipeline()` calls `GET /api/server/version` (plain-text response) and selects one of four pipeline implementations. Authentication is injected automatically via the `authTransport` RoundTripper inside the `sqapi.Client`'s HTTP client.
- **Edition-aware:** Tasks are filtered by edition — portfolio-related tasks only run on Enterprise and Data Center editions
- **Edition detection fallback:** When `/api/system/info` returns 403 (non-admin token), edition detection falls back to `/api/navigation/global` to extract the edition from the response

## Configuration

Commands accept flags, positional arguments, or a JSON config file (`--config path/to/config.json`). CLI flags override config file values. See `docs/CONFIG.md` for details.

## Testing

- **Framework:** stdlib `testing` + `net/http/httptest` for HTTP mocking
- **Run tests:** `cd go && go test ./... -count=1`
- **Coverage:** `cd go && go test ./... -coverprofile=coverage.out`

## Roadmap: Data Migration Specs
<!-- updated: 2026-05-26_17:30:00 -->

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

### Issue #104: Migrate All Issues (Implementation Status)
<!-- updated: 2026-05-27_08:00:00 -->

Full end-to-end issue and hotspot migration pipeline. Current status by phase:

| Phase | Task | Status | Notes |
|-------|------|--------|-------|
| Extract | `getProjectIssuesFull` | Complete | Extracts issues with comments, tags, flows. Live-verified against SQ Enterprise 2026.2.0 |
| Extract | `getProjectHotspotsFull` | Complete | Extracts hotspots with review details. Live-verified against SQ Enterprise 2026.2.0 |
| Scan History Import | Protobuf report builder | Complete | Native issues, external issues (via ExternalIssue protobuf classification), hotspots mapped to issues |
| Migrate | `syncIssueMetadata` | Complete | Composite key matching (rule\|filePath\|line), fallback status transitions, comment sync, tag sync, idempotent via `metadata-synchronized` tag |
| Migrate | `syncHotspotMetadata` | Complete | Composite key matching, REVIEWED status/resolution sync, comment sync, idempotent |
| Cloud API | `IssuesClient` | Complete | `lib/sq-api-go/cloud/` — search, transitions, comments, tags |
| Cloud API | `HotspotsClient` | Complete | `lib/sq-api-go/cloud/` — search, status changes, comments |
| Infrastructure | Edition detection fallback | Complete | `/api/navigation/global` fallback when `/api/system/info` returns 403 (non-admin tokens) |
| Testing | Unit tests + race detector | Passing | All unit tests pass, race detector clean |

**ReferenceBranchName fix:** `MetadataInput` now includes a `ReferenceBranchName` field. `BuildMetadata` sets `ReferenceBranchName` on the protobuf `Metadata` message, defaulting to `BranchName` if not explicitly provided. This matches CloudVoyager's behavior where `referenceBranchName` is always set (defaults to `branchName`). Without this field, SonarCloud's Compute Engine rejected the protobuf report with a generic processing error.
