# Sonar Migration Tool — Product Roadmap
<!-- updated: 2026-05-26_01:00:00 -->

## Vision

**sonar-migration-tool** is the open-source successor to CloudVoyager — a production-proven SonarQube Server → SonarQube Cloud migration tool. This roadmap captures every capability from CloudVoyager as formal PRD specifications, providing a complete blueprint for any contributor or AI agent to build against.

The end state: a single Go binary that performs **complete, lossless migration** of an entire SonarQube Server installation to SonarQube Cloud — including issues, hotspots, source code, measures, quality configurations, permissions, and all metadata — with zero re-scanning required.

---

## Current State vs. Target State

| Capability | Current (sonar-migration-tool) | Target (CloudVoyager parity+) |
|---|---|---|
| Projects | Migrated | Migrated |
| Quality Gates | Migrated | Migrated |
| Quality Profiles | Migrated | Migrated (XML backup/restore) |
| Groups & Permissions | Migrated | Migrated |
| Permission Templates | Migrated | Migrated |
| Portfolios | Migrated | Migrated |
| **Issues** | **NOT migrated** | **Full migration with metadata sync** |
| **Hotspots** | **NOT migrated** | **Full migration with metadata sync** |
| **Source Code** | **NOT migrated** | **Migrated via scanner protocol** |
| **Measures/Metrics** | **NOT migrated** | **Full metric preservation** |
| **SCM/Blame Data** | **NOT migrated** | **Full changeset migration** |
| Project Data | Optional | Full protobuf report injection |
| Issue Creation Dates | N/A | Accurate via changeset backdating |
| >10K Issue Handling | N/A | Date-window bisection |
| Multi-version Support | Single pipeline | 4 version-specific pipelines |
| Desktop App | Browser GUI | Electron app + Browser GUI |
| Verification | None | Full read-only comparison |
| Checkpoint/Resume | Extract+Migrate resume | Write-ahead journal with crash recovery |
| Auto-tuning | None | CPU/memory-aware concurrency |

---

## Spec Index

### Epic 1: Core Data Migration (P0 — Critical)

These specs close the fundamental capability gap between the current tool and CloudVoyager.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-001](specs/SPEC-001-scanner-protocol-engine.md) | Scanner Protocol Engine | P0 | XL | Draft |
| [SPEC-002](specs/SPEC-002-issue-migration-pipeline.md) | Issue Migration Pipeline | P0 | XL | Draft |
| [SPEC-003](specs/SPEC-003-security-hotspot-migration.md) | Security Hotspot Migration | P0 | L | Draft |
| [SPEC-004](specs/SPEC-004-source-code-scm-migration.md) | Source Code & SCM Data Migration | P0 | XL | Draft |
| [SPEC-005](specs/SPEC-005-measures-metrics-migration.md) | Measures & Metrics Migration | P0 | L | Draft |

### Epic 2: Scale & Reliability (P0/P1)

These specs handle edge cases and scale challenges that CloudVoyager solved through hard-won production experience.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-006](specs/SPEC-006-large-scale-issue-handling.md) | Large-Scale Issue Handling (>10K) | P0 | L | Draft |
| [SPEC-007](specs/SPEC-007-issue-batch-distribution.md) | Issue Batch Distribution | P0 | L | Deprecated |
| [SPEC-008](specs/SPEC-008-issue-metadata-sync.md) | Issue Metadata Synchronization | P0 | XL | Draft |
| [SPEC-009](specs/SPEC-009-hotspot-metadata-sync.md) | Hotspot Metadata Synchronization | P0 | L | Draft |
| [SPEC-010](specs/SPEC-010-user-mapping.md) | User Mapping & Assignment | P1 | M | Draft |

### Epic 3: Version Compatibility & Correctness (P1)

These specs ensure the tool works correctly across all SonarQube Server versions and handles edge cases in rule/issue encoding.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-011](specs/SPEC-011-version-specific-pipelines.md) | Version-Specific Pipeline Architecture | P1 | XL | Draft |
| [SPEC-012](specs/SPEC-012-clean-code-attributes.md) | Clean Code Attribute Mapping | P1 | M | Draft |
| [SPEC-013](specs/SPEC-013-external-issues-adhoc-rules.md) | External Issues & Ad-Hoc Rules | P1 | L | Draft |

### Epic 4: Performance & Concurrency (P1)

These specs bring CloudVoyager's production-grade performance optimizations to the Go implementation.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-014](specs/SPEC-014-parallel-sync-worker-threads.md) | Parallel Sync & Worker Goroutines | P1 | L | Draft |
| [SPEC-015](specs/SPEC-015-auto-tuning.md) | Auto-Tuning & Performance Optimization | P1 | M | Draft |
| [SPEC-016](specs/SPEC-016-rate-limiting-resilience.md) | Rate Limiting & API Resilience | P1 | M | Draft |

### Epic 5: Migration Workflow (P1/P2)

These specs enhance the existing migration workflow with CloudVoyager's advanced orchestration features.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-017](specs/SPEC-017-checkpoint-resume.md) | Checkpoint & Resume System | P1 | L | Draft |
| [SPEC-018](specs/SPEC-018-multi-org-mapping.md) | Multi-Organization Mapping | P1 | M | Draft |
| [SPEC-019](specs/SPEC-019-csv-entity-filtering.md) | CSV Entity Filtering & Dry-Run | P2 | M | Draft |
| [SPEC-020](specs/SPEC-020-branch-migration.md) | Branch Migration Strategy | P1 | L | Draft |

### Epic 6: Verification & Reporting (P1/P2)

These specs add post-migration validation and enhanced reporting capabilities.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-021](specs/SPEC-021-migration-verification.md) | Migration Verification Pipeline | P1 | L | Draft |
| [SPEC-022](specs/SPEC-022-reporting-suite.md) | Comprehensive Reporting Suite | P2 | M | Draft |

### Epic 7: User Experience (P2/P3)

These specs enhance the user-facing experience with additional interfaces and convenience features.

| Spec | Title | Priority | Effort | Status |
|------|-------|----------|--------|--------|
| [SPEC-023](specs/SPEC-023-desktop-application.md) | Desktop Application (Electron) | P3 | XL | Draft |
| [SPEC-024](specs/SPEC-024-sync-metadata-command.md) | Sync Metadata Command | P2 | M | Draft |
| [SPEC-025](specs/SPEC-025-configuration-validation.md) | Configuration & Validation | P2 | M | Draft |

---

## Priority Definitions

| Priority | Definition | Timeline |
|----------|-----------|----------|
| **P0** | Must-have for feature parity. Without these, the tool cannot replace CloudVoyager. | Phase 1 |
| **P1** | Required for production readiness. Essential for enterprise adoption. | Phase 2 |
| **P2** | Important for usability and completeness. Enhances the migration experience. | Phase 3 |
| **P3** | Nice-to-have. Adds polish and additional interfaces. | Phase 4 |

## Effort Estimates

| Size | Description |
|------|-------------|
| **S** | < 1 day. Isolated change, well-understood. |
| **M** | 1–3 days. Moderate scope, some design decisions. |
| **L** | 3–7 days. Significant feature, multiple components. |
| **XL** | 1–3 weeks. Major feature, new subsystems, cross-cutting. |

---

## Dependency Graph

```
SPEC-001 (Scanner Protocol)
├── SPEC-002 (Issues) ──── SPEC-006 (>10K Handling) ──── SPEC-007 (Batch Distribution)
│   └── SPEC-008 (Issue Metadata Sync) ──── SPEC-014 (Parallel Sync)
│       └── SPEC-010 (User Mapping)
├── SPEC-003 (Hotspots)
│   └── SPEC-009 (Hotspot Metadata Sync)
├── SPEC-004 (Source Code & SCM)
├── SPEC-005 (Measures & Metrics)
├── SPEC-012 (Clean Code Attributes)
└── SPEC-013 (External Issues)

SPEC-011 (Version Pipelines) ── cross-cuts all extraction/encoding specs

SPEC-017 (Checkpoint/Resume) ── cross-cuts all migration phases
SPEC-016 (Rate Limiting) ── cross-cuts all API calls
SPEC-015 (Auto-Tuning) ── cross-cuts all concurrent operations

SPEC-018 (Multi-Org) ── enhances existing structure/mappings phase
SPEC-019 (CSV Filtering) ── enhances existing CSV workflow
SPEC-020 (Branch Migration) ── enhances SPEC-002, SPEC-004, depends on SPEC-008

SPEC-021 (Verification) ── requires SPEC-002, SPEC-003, SPEC-005
SPEC-022 (Reporting) ── consumes output from all migration specs
SPEC-024 (Sync Metadata) ── requires SPEC-008, SPEC-009
SPEC-023 (Desktop App) ── wraps CLI; no spec dependencies
SPEC-025 (Config Validation) ── enhances existing config system
```

---

## Implementation Strategy

### Phase 1: Core Data Migration (SPEC-001 through SPEC-009)
Build the scanner protocol engine and all data extractors/encoders. This is the foundation — everything else builds on top. Start with SPEC-001 (protocol engine), then build SPEC-002 (issues) and SPEC-004 (source code) in parallel, as they exercise different parts of the encoder.

### Phase 2: Version Compatibility & Performance (SPEC-011 through SPEC-016)
Once the core pipeline works for one SQ version, abstract into version-specific pipelines. Add performance optimizations (parallel sync, auto-tuning, rate limiting) to handle production-scale migrations.

### Phase 3: Workflow & Verification (SPEC-017 through SPEC-022)
Enhance the migration workflow with advanced checkpoint/resume, branch migration, and the verification pipeline. Add comprehensive reporting.

### Phase 4: Polish & Desktop (SPEC-023 through SPEC-025)
Build the Electron desktop app, add the sync-metadata command, and enhance configuration validation.

---

## How to Read These Specs

Each spec follows a consistent structure:

1. **Overview** — What the feature does and why it matters
2. **Problem Statement** — The gap this fills
3. **User Stories** — Who benefits and how
4. **Requirements** — Functional and non-functional requirements
5. **Technical Design** — Architecture, algorithms, API dependencies
6. **Acceptance Criteria** — Testable conditions for "done"
7. **CloudVoyager Reference** — Where to find the reference implementation
8. **Known Limitations** — Documented constraints and workarounds
9. **Open Questions** — Unresolved design decisions

Specs reference each other using `SPEC-XXX` identifiers. Dependencies are explicit — don't start a spec until its dependencies are at least in progress.

---

## CloudVoyager Reference

The original CloudVoyager implementation lives at:
```
/Users/joshua.quek/Desktop/Active Projects/CloudVoyager Agents/CloudVoyager/
```

Key reference directories:
- `src/pipelines/` — Version-specific extraction and encoding logic
- `src/shared/` — Cross-cutting utilities (state, mapping, reports, config)
- `src/commands/` — CLI command handlers
- `desktop/` — Electron application
- `docs/` — Comprehensive documentation (architecture, key capabilities, troubleshooting)

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
