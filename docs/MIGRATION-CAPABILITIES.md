# Migration Capabilities
<!-- updated: 2026-06-04_12:00:00 -->

This document describes everything the **sonar-migration-tool** migrates (and will migrate) when moving from SonarQube Server to SonarQube Cloud. It covers both the current capabilities and the planned roadmap features derived from [CloudVoyager](https://github.com/sonar-solutions/cloudvoyager) parity specifications.

---

## Current Capabilities (Implemented)
<!-- updated: 2026-06-04_12:00:00 -->

The following entities are fully migrated today:

| Entity | Description |
|--------|-------------|
| **Projects** | Project creation and configuration in SonarQube Cloud |
| **Quality Gates** | Quality gate definitions and conditions |
| **Quality Profiles** | Quality profile definitions (XML backup/restore) |
| **Groups** | User groups and membership |
| **Permissions** | Project-level and global permissions |
| **Permission Templates** | Reusable permission template definitions |
| **Portfolios** | Portfolio structure and project assignments |
| **Project Data** | Optional protobuf report injection for historical analysis data |

---

## Planned Capabilities (Roadmap)
<!-- updated: 2026-06-04_12:00:00 -->

The following capabilities are specified and planned for implementation, organized by priority phase.

### Phase 1: Core Data Migration (P0 -- Critical)
<!-- updated: 2026-06-04_12:00:00 -->

These close the fundamental gap between the current tool and full CloudVoyager parity. Without these, the tool cannot perform a complete, lossless migration.

#### Scanner Protocol Engine (SPEC-001)

The foundational subsystem that enables historical data injection into SonarQube Cloud **without requiring a real SonarScanner analysis**. It reconstructs the proprietary binary protobuf ZIP archive format that SonarQube Cloud's Compute Engine (CE) accepts for analysis results.

**What it migrates:**
- Programmatically generates scanner report archives (`.pb` protobuf + `.txt` files in ZIP format)
- Enables injection of issues, measures, source code, SCM blame data, and other artifacts directly into SonarQube Cloud's CE pipeline
- Every other data migration capability depends on this engine

#### Issues (SPEC-002)

Full migration of all code analysis issues from SonarQube Server to SonarQube Cloud.

**What it migrates:**
- All issue types: bugs, vulnerabilities, code smells, and security issues
- Issue severity levels
- Issue rule associations
- Issue locations (file, line, text range)
- Issue creation dates (preserved via changeset backdating)
- Issue flow locations (secondary locations and data flows)
- Issue messages and descriptions

#### Security Hotspots (SPEC-003)

Migration of security-sensitive code locations that require manual review.

**What it migrates:**
- All security hotspot records
- Hotspot rule associations and categories
- Hotspot locations (file, line, text range)
- Hotspot vulnerability probability ratings
- Hotspot creation dates

#### Source Code & SCM Data (SPEC-004)

Migration of source code snapshots and version control blame data.

**What it migrates:**
- Source code file contents (as they existed at analysis time)
- SCM blame/changeset data (author, revision, date per line)
- Line hashes for duplicate detection
- File-level metadata (language, encoding)
- Changeset backdating to preserve accurate issue creation dates within SonarQube Cloud

#### Measures & Metrics (SPEC-005)

Migration of project-level and component-level quality measures.

**What it migrates:**
- Code coverage metrics (line coverage, branch coverage, conditions)
- Complexity metrics (cyclomatic complexity, cognitive complexity)
- Size metrics (lines of code, statements, functions, classes, files)
- Duplication metrics (duplicated lines, duplicated blocks, duplicated files)
- Issue count metrics (bugs, vulnerabilities, code smells per severity)
- Maintainability, reliability, and security ratings
- Technical debt (remediation effort)
- New code period metrics

### Phase 2: Scale, Reliability & Version Compatibility (P0/P1)
<!-- updated: 2026-06-04_12:00:00 -->

These handle edge cases, scale challenges, and cross-version compatibility that CloudVoyager solved through production experience.

#### Large-Scale Issue Handling (SPEC-006)

Automatic handling of projects with more than 10,000 issues, which hit SonarQube Server's Elasticsearch pagination limit.

**What it enables:**
- Date-window bisection algorithm ("search slicing") to extract all issues regardless of count
- Recursive subdivision of date ranges until each window returns under 10,000 results
- Transparent large-scale extraction for both issues and hotspots
- Deduplication of results across overlapping date windows

#### Issue Metadata Synchronization (SPEC-008)

After issues are uploaded via scanner reports, this phase matches each SonarQube Cloud issue back to its Server counterpart and replicates the original lifecycle metadata.

**What it migrates:**
- **Issue statuses**: OPEN, CONFIRMED, FALSE_POSITIVE, WONTFIX, ACCEPTED, RESOLVED, CLOSED
- **Issue comments**: All comments with original author attribution (`[Migrated from SonarQube Server - @author]`)
- **Issue tags**: Custom tags applied by users
- **Issue assignments**: User assignments mapped via `users.csv`
- Pre-filtering skips issues with no manual changes (60-80% of typical enterprise issues), dramatically reducing API calls

#### Hotspot Metadata Synchronization (SPEC-009)

Equivalent to issue metadata sync but for security hotspots.

**What it migrates:**
- **Hotspot review statuses**: TO_REVIEW, REVIEWED/SAFE, REVIEWED/FIXED, REVIEWED/ACKNOWLEDGED
- **Hotspot review comments**: Comments from the review workflow
- **Hotspot assignments**: Reviewer assignments

#### User Mapping & Assignment (SPEC-010)

Maps SonarQube Server user identities to SonarQube Cloud user identities.

**What it migrates:**
- User login mappings via `users.csv` (SQ Server login → SC login)
- Issue assignee resolution across platforms
- Comment author attribution

#### Version-Specific Pipeline Architecture (SPEC-011)

Ensures correct migration across all SonarQube Server versions with version-specific extraction and encoding logic.

**What it supports:**
- Four version-specific pipelines for different SQ Server versions
- Version-aware API field extraction (fields differ between SQ 8.x, 9.x, 10.x)
- Automatic version detection and pipeline selection

#### Clean Code Attribute Mapping (SPEC-012)

Handles the mapping of Clean Code taxonomy attributes introduced in SonarQube 10.2+.

**What it migrates:**
- Clean Code attributes (INTENTIONAL, CONSISTENT, ADAPTABLE, RESPONSIBLE)
- Software quality impacts (MAINTAINABILITY, RELIABILITY, SECURITY)
- Impact severity levels (HIGH, MEDIUM, LOW)
- Backward-compatible mapping for pre-10.2 Server versions that lack these attributes

#### External Issues & Ad-Hoc Rules (SPEC-013)

Migration of issues from third-party analyzers and custom rules not in the standard SonarQube rule repository.

**What it migrates:**
- External issues (from tools like ESLint, Checkstyle, PMD, FindBugs, etc.)
- Ad-hoc rule definitions created for external issues
- External rule engine identifiers and metadata

### Phase 3: Performance & Workflow Enhancements (P1/P2)
<!-- updated: 2026-06-04_12:00:00 -->

Production-grade performance optimizations and advanced migration workflow features.

#### Parallel Sync & Worker Goroutines (SPEC-014)

High-concurrency parallel processing for all migration phases.

**What it enables:**
- Concurrent issue and hotspot metadata synchronization
- Worker pool architecture with configurable concurrency
- Per-worker semaphore-based rate limiting
- Round-robin work distribution for even load balancing

#### Auto-Tuning & Performance Optimization (SPEC-015)

Intelligent automatic configuration of concurrency settings based on the host machine's capabilities.

**What it enables:**
- CPU core and system memory detection (including container/cgroup awareness)
- Phase-specific concurrency tuning (extraction, sync, migration phases)
- Configuration precedence: CLI flags > config file > auto-tune > defaults
- Cross-platform support: macOS, Linux, Windows

#### Rate Limiting & API Resilience (SPEC-016)

Four-layer resilience system for handling API failures gracefully.

**What it enables:**
- **Layer 1**: Exponential backoff retry on 429/5xx errors (already implemented)
- **Layer 2**: Write request throttling to proactively avoid rate limits
- **Layer 3**: Human-readable connection error diagnostics (DNS, TLS, timeout, connection refused)
- **Layer 4**: CE task resilience with duplicate detection and automatic re-submission

#### Checkpoint & Resume System (SPEC-017)

Write-ahead journal with crash recovery for long-running migrations.

**What it enables:**
- Resumable migrations after interruptions (network failure, process crash, rate limits)
- Per-project checkpoint tracking
- Extraction result caching to avoid re-fetching data
- Strict resume mode for consistency validation

#### Multi-Organization Mapping (SPEC-018)

Support for migrating to multiple SonarQube Cloud organizations from a single SonarQube Server.

**What it enables:**
- Project-to-organization mapping rules
- Per-organization authentication tokens
- Enterprise key support for multi-org migrations
- Organization-level resource isolation (quality gates, profiles, groups)

#### CSV Entity Filtering & Dry-Run (SPEC-019)

Fine-grained control over which entities are included in or excluded from migration.

**What it enables:**
- CSV-based project inclusion/exclusion lists
- Dry-run mode to preview what would be migrated without making changes
- Entity filtering by project key patterns, tags, or custom criteria

#### Branch Migration Strategy (SPEC-020)
<!-- updated: 2026-06-05_19:20:00 -->

Migration of non-main branches and their associated analysis data.

**Status: Implemented.** Non-main branches migrate as **long-lived branches with full issue history** via SonarQube Cloud's "Create analysis" handshake (`POST {api-host}/analysis/analyses` → `analysisUuid` stamped into report `metadata.analysis_uuid`); see [CLOUDVOYAGER-DELTA.md](CLOUDVOYAGER-DELTA.md) BUG-17. All migrated branches are registered as long-lived so SonarQube Cloud's automatic pruning of short-lived branches (after ~30 days) never discards migrated history.

**What it migrates:**
- Per-branch issues, hotspots, and measures (each branch's full project data)
- All non-main branches as long-lived branches (short-lived/PR branches are not separately recreated; everything is migrated long-lived to preserve history)
- Configurable branch inclusion/exclusion patterns (`--exclude_branches`)
- Branches whose source is no longer retrievable on the server are skipped with a clear message

### Phase 4: Verification, Reporting & User Experience (P1/P2/P3)
<!-- updated: 2026-06-04_12:00:00 -->

Post-migration validation, comprehensive reporting, and enhanced user interfaces.

#### Migration Verification Pipeline (SPEC-021)

Read-only comparison between SonarQube Server and SonarQube Cloud to validate migration completeness.

**What it verifies:**
- Project existence and configuration parity
- Issue count comparison (total, by severity, by type)
- Hotspot count comparison
- Quality gate and quality profile parity
- Group and permission parity
- Measure/metric comparison
- Per-project verification report with match percentages

#### Comprehensive Reporting Suite (SPEC-022)
<!-- updated: 2026-06-05_14:00:00 -->

Multi-format reporting system for migration documentation.

**Partially shipped.** A consolidated `migration_summary.{pdf,md}` plus machine-readable run
instrumentation (`run_meta.json`, `run_events.jsonl`) now ships from the migrate engine,
delivering a first cut of several SPEC-022 functional requirements. The remaining FRs (the
fully separate per-report formats, Quality Profile Diff, Server Info Reference, and the
level-segregated log files) are still unshipped.

**What it generates:**
- **Migration Report**: Per-project transfer status, issue/hotspot sync statistics, error details (JSON, Markdown, Text, PDF) — _planned (FR-1); the consolidated `migration_summary.{pdf,md}` covers part of this today._
- **Executive Summary (FR-2)**: High-level success rates, total counts, elapsed time, throughput (Markdown, PDF) — **PARTIALLY shipped** via `migration_summary.{pdf,md}` (overall status, branch/throughput totals, elapsed time) backed by `run_meta.json`.
- **Performance Report (FR-3)**: Per-stage timing breakdowns, concurrency utilization, rate limit impact (Markdown, PDF) — **PARTIALLY shipped**: per-phase/per-task timings are captured in `run_meta.json` and surfaced in `migration_summary.{pdf,md}`; concurrency-utilization and rate-limit-impact analysis are still unshipped.
- **Markdown output (FR-5)**: Human-readable Markdown rendering — **PARTIALLY shipped** via `migration_summary.md` (written alongside the PDF by `summary.GenerateReports`); per-report Markdown for the dedicated report types is still unshipped.
- **Quality Profile Diff Report**: Rule comparison between Server and Cloud per language — _not yet shipped._
- **Server Info Reference Export**: System info, plugin versions, global settings — _not yet shipped._
- **Structured Logging**: Per-run log directories with level-segregated log files — _not yet shipped as four separate per-level files; a tee slog handler currently mirrors all events into a single `run_events.jsonl` stream._

#### Sync Metadata Command (SPEC-024)

Standalone command to re-synchronize issue and hotspot metadata without re-uploading scanner reports.

**What it enables:**
- Re-sync after interrupted metadata synchronization (skip extraction/upload phases)
- Selective sync: skip issue sync, hotspot sync, or quality profile sync independently
- Post-migration metadata updates when issues are triaged in SQ Server during phased migrations
- Idempotent re-runs with comment deduplication

#### Configuration & Validation (SPEC-025)

Schema-driven configuration validation with standalone utility commands.

**What it enables:**
- Upfront validation of all configuration before any API calls
- `validate` command for offline config checking (CI/CD friendly)
- `test` command for connectivity verification to both SQ Server and SC
- Unknown field warnings with "did you mean?" suggestions
- Environment variable overrides for tokens and URLs

#### Desktop Application (SPEC-023)

Electron-based desktop application wrapping the CLI and browser GUI.

**What it provides:**
- Native desktop installer (`.dmg`, `.exe`, `.AppImage`)
- Guided wizard UI for all migration operations
- Encrypted token storage at rest
- Real-time progress tracking with ETA estimation
- Checkpoint/resume detection on startup
- Migration history browser (last 50 runs)
- Results/report file browser

---

## Migration Summary Table
<!-- updated: 2026-06-04_12:00:00 -->

| Data Type | Current Status | Target Status |
|-----------|---------------|---------------|
| Projects | Migrated | Migrated |
| Quality Gates | Migrated | Migrated |
| Quality Profiles | Migrated | Migrated (XML backup/restore) |
| Groups & Permissions | Migrated | Migrated |
| Permission Templates | Migrated | Migrated |
| Portfolios | Migrated | Migrated |
| Project Data | Optional | Full protobuf report injection |
| **Issues** | **Not migrated** | **Full migration with metadata sync** |
| **Security Hotspots** | **Not migrated** | **Full migration with metadata sync** |
| **Source Code** | **Not migrated** | **Migrated via scanner protocol** |
| **SCM/Blame Data** | **Not migrated** | **Full changeset migration** |
| **Measures/Metrics** | **Not migrated** | **Full metric preservation** |
| **Clean Code Attributes** | **Not migrated** | **Mapped across versions** |
| **External Issues** | **Not migrated** | **Full migration with ad-hoc rules** |
| Issue Statuses | N/A | Synced (OPEN, CONFIRMED, FP, WONTFIX, etc.) |
| Issue Comments | N/A | Synced with author attribution |
| Issue Tags | N/A | Synced |
| Issue Assignments | N/A | Mapped via users.csv |
| Hotspot Review Status | N/A | Synced (TO_REVIEW, REVIEWED/SAFE, etc.) |
| Hotspot Comments | N/A | Synced |
| Issue Creation Dates | N/A | Preserved via changeset backdating |
| Branch Data | N/A | Configurable branch migration |
| User Mappings | N/A | CSV-based identity mapping |

---

## How Migration Works (High-Level)
<!-- updated: 2026-06-04_12:00:00 -->

The migration pipeline follows this sequence:

```
1. Configuration & Validation
   └── Load config, validate schema, test connectivity

2. Organization Setup
   └── Create/map groups, permissions, quality gates, quality profiles, permission templates

3. Per-Project Migration (parallelized across projects)
   ├── a. Data Extraction (from SonarQube Server)
   │   ├── Issues (with date-window bisection for >10K)
   │   ├── Security Hotspots
   │   ├── Source Code & SCM Blame Data
   │   └── Measures & Metrics
   │
   ├── b. Scanner Report Construction
   │   ├── Build protobuf messages from extracted data
   │   ├── Backdate changesets to preserve creation dates
   │   └── Package into ZIP archive
   │
   ├── c. Report Upload
   │   ├── Submit ZIP to SonarQube Cloud CE
   │   └── Poll CE task until SUCCESS
   │
   └── d. Metadata Synchronization
       ├── Wait for SC Elasticsearch indexing
       ├── Match issues by composite key (rule + component + line)
       ├── Sync statuses, comments, tags, assignments
       └── Sync hotspot review statuses and comments

4. Portfolio Migration
   └── Recreate portfolio structure and project assignments

5. Verification (optional)
   └── Read-only comparison of Server vs Cloud data

6. Reporting
   └── Generate migration, executive, and performance reports
```

---

## Version Compatibility
<!-- updated: 2026-06-04_12:00:00 -->

The tool supports migration from the following SonarQube Server versions:

| SQ Server Version | Support Level | Notes |
|-------------------|--------------|-------|
| 8.x | Planned | Legacy API format, no Clean Code attributes |
| 9.x | Planned | Intermediate API format |
| 10.0 - 10.1 | Planned | Pre-Clean Code taxonomy |
| 10.2+ | Planned | Full Clean Code attribute support |
| 2025.x | Planned | Latest format with IN_SANDBOX status handling |

Each version has a dedicated extraction/encoding pipeline to handle API differences.

---

## API Endpoints Used
<!-- updated: 2026-06-04_12:00:00 -->

### SonarQube Server (Source -- Read Only)

| Endpoint | Purpose |
|----------|---------|
| `/api/issues/search` | Extract issues with pagination |
| `/api/issues/changelog` | Fetch issue changelogs for pre-filtering |
| `/api/hotspots/search` | Extract security hotspots |
| `/api/sources/raw` | Extract source code |
| `/api/sources/scm` | Extract SCM blame data |
| `/api/measures/component` | Extract project measures |
| `/api/measures/search_history` | Extract measure history |
| `/api/rules/search` | Extract rule definitions |
| `/api/qualityprofiles/search` | Extract quality profiles |
| `/api/qualitygates/list` | Extract quality gates |
| `/api/user_groups/search` | Extract groups |
| `/api/permissions/*` | Extract permissions |
| `/api/project_branches/list` | List branches |
| `/api/system/info` | Server version detection |

### SonarQube Cloud (Target -- Read/Write)

| Endpoint | Purpose |
|----------|---------|
| `/api/ce/submit` | Submit scanner report ZIP |
| `/api/ce/task` | Poll CE task status |
| `/api/issues/search` | Fetch SC issues for matching |
| `/api/issues/do_transition` | Apply issue status transitions |
| `/api/issues/add_comment` | Add migrated comments |
| `/api/issues/set_tags` | Set issue tags |
| `/api/issues/assign` | Assign issues to users |
| `/api/hotspots/search` | Fetch SC hotspots for matching |
| `/api/hotspots/change_status` | Change hotspot review status |
| `/api/projects/create` | Create projects |
| `/api/qualitygates/*` | Create/configure quality gates |
| `/api/qualityprofiles/*` | Create/configure quality profiles |
| `/api/user_groups/*` | Create/manage groups |
| `/api/permissions/*` | Set permissions |

---

## References
<!-- updated: 2026-06-04_12:00:00 -->

- Full roadmap and spec index: [roadmap/README.md](../roadmap/README.md)
- Individual specs: [roadmap/specs/](../roadmap/specs/)
- SonarQube documentation: https://docs.sonarsource.com/llms.txt
- CloudVoyager reference: https://github.com/sonar-solutions/cloudvoyager
