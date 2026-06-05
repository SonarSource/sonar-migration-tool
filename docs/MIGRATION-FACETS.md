# SonarQube Server → SonarCloud — Migration Coverage
<!-- updated: 2026-06-05_21:12:00 -->

> **Reconciled against the actual Go code on 2026-06-05.** Every row below reflects what the running binary does (`go/`, `lib/sq-api-go/`), not spec/design intent. Claims that previously described unbuilt specs or dead code have been corrected or moved. Full claim-by-claim evidence (file:line) is in [MIGRATION-FACETS-AUDIT.md](MIGRATION-FACETS-AUDIT.md).

## ✅ What IS Migrated

The **Caveats / NOT carried** column is load-bearing — read it before relying on a row.

| Layer | Entity | Fields / Data Carried Over | Caveats / NOT carried | Mechanism | Spec |
|---|---|---|---|---|---|
| **Structure** | Projects | name, settings, links, tags | **visibility NOT migrated** — every project is forced `private`; **key is prefixed** to `{orgKey}_{sourceKey}`, not preserved verbatim | REST API | baseline |
| **Structure** | Quality Gates | gate definition + all conditions (metric, operator, threshold) + project assignment | — (fully accurate) | REST API | baseline |
| **Structure** | Quality Profiles | rule activations, inheritance chains (`change_parent`), custom rules (via server-side XML restore) | **template-instantiated custom rules are NOT migratable** (no Cloud template API) and are flagged by the analyzer; **unsupported-language profiles** (c++, grvy, ps…) are filtered out | REST API XML backup/restore | 011/018 |
| **Structure** | Groups | group **name** only | **membership is NOT migrated** (extracted but never applied); name is sourced from `groups.csv`, **not** the V2 `/authorizations/groups` path (that path is dead code) | REST API (`/api/user_groups/create`) | 011/018 |
| **Structure** | Permissions | **group** grants only — global/org, project-level, template, profile | **user-level grants are NOT applied** (Cloud has no user-permission API) | REST API (`add_group`, `add_group_to_template`) | baseline |
| **Structure** | Permission Templates | template definition + permission rules + **group** associations | **user associations NOT migrated** (no `add_user_to_template` on Cloud) | REST API | baseline |
| **Structure** | Portfolios (Enterprise) | structure + project composition (selection criteria) | **hierarchy is FLATTENED, not preserved** — Cloud has no portfolio nesting; subportfolios collapse into flat selection/project lists (perimeter may shift slightly) | REST API (`/enterprises/portfolios`) | baseline |
| **Structure** | Org-level Group Permissions | group → org permission grants | restricted to the Cloud-supported allowlist (`admin`, `scan`, …); **`profileadmin`/`gateadmin`/`provisioning` are dropped**; **user** org-perms NOT migrated | CSV (`organizations.csv`) + REST API | 018 |
| **Analysis Data** | Issues (native) | rule (repo+key), file/component, text_range (start/end line + offset), message, severity (→ `overriddenSeverity`, **incl. manual severity overrides**) | NOT populated for native issues: `msgFormatting`, flows/secondary locations, `codeVariants`, `overriddenImpacts` (MQR per-impact override), `quickFixAvailable`, `gap/effort/debt`. `type`/`cleanCodeAttribute`/`impacts` are external-issue-only fields | Protobuf scanner report → `/api/ce/submit` | 002 |
| **Analysis Data** | Security Hotspots | rule, message, text_range, vulnerabilityProbability (→ severity), creation date (via SCM backdating) | recreated as **generic issues** (no `SECURITY_HOTSPOT` type set); **`securityCategory`, original `author`, and update dates are NOT migrated** | Protobuf scanner report | 003 |
| **Analysis Data** | Source Code | raw file content; language; line count | content rides in `source-{ref}.txt`; **language + line count ride in `component-{ref}.pb`** (not the source file). Part of the project-data migration, which runs by default on both `migrate` and `transfer`; opt out with `--skip_project_data_migration` | `source-{ref}.txt` + `component-{ref}.pb` in report ZIP | 004 |
| **Analysis Data** | SCM / Blame Data | per-line **date** only, back-dated to oldest issue creation date | **per-line revision is a random-hex stub; author is a hardcoded stub** (`sonar-migration-tool@sonarcloud.io`) — original SCM identity is NOT preserved | Protobuf `Changesets` + `BackdateChangesets()` | 004 |
| **Analysis Data** | Issue Creation Dates | original creation dates preserved | — (fully accurate) | via SCM changeset backdating | 004 |
| **Analysis Data** | Active Rules | repo, key, severity, q-profile key (remapped SQ→Cloud) | **params, impacts, and timestamps are NOT migrated** on this path — params default to empty, timestamps fall back to the migration run-time | Protobuf `activerules.pb` | 001 |
| **Analysis Data** | External Issues (3rd-party analyzers) | engineId, ruleId, message, severity, type, textRange | filename is **`external-issues-{ref}.pb`** (hyphenated) | Protobuf `external-issues-{ref}.pb` | 013 |
| **Analysis Data** | Ad-Hoc Rules | engineId, ruleId, severity, type, cleanCodeAttribute | **`name` = rule key** (not a display name); **`description` = generic placeholder** `"Rule from {engine} plugin"`, not the source rule description | Protobuf `adhocrules.pb` | 013 |
| **Metadata Sync** | Issue Status | OPEN, CONFIRMED, REOPENED, RESOLVED, FALSE_POSITIVE, WONTFIX | **`FIXED` is excluded** from sync entirely; **`ACCEPTED` is mapped to the `wontfix` transition** → lands as WONTFIX on Cloud, not ACCEPTED | `/api/issues/do_transition` | 008/024 |
| **Metadata Sync** | Issue Comments | text (Markdown, HTML fallback) + original author + timestamp | prefix is **`[Migrated from {login} on {date}]`** (no literal "SonarQube Server", no `@`); dedup is **plain substring match, not hashing** | `/api/issues/add_comment` | 008/024 |
| **Metadata Sync** | Issue Tags | all custom tags | — (fully accurate) | `/api/issues/set_tags` | 008/024 |
| **Metadata Sync** | Hotspot Review Status | TO_REVIEW / REVIEWED + resolution SAFE / FIXED | **`ACKNOWLEDGED` is silently downgraded to `SAFE`**; TO_REVIEW makes no API call (Cloud default) | `/api/hotspots/change_status` | 009/024 |
| **Metadata Sync** | Hotspot Comments | review comments with author attribution | — (fully accurate) | `/api/hotspots/add_comment` | 009/024 |
| **Scope** | Branches | main + non-main **LONG** branches (main first, blocking CE gate); per-branch issues, source, SCM (backdated), reference-branch mapping (→ main), create-analysis handshake (analysis_uuid field 19) | **only LONG branches migrate** — SHORT branches are skipped, PULL_REQUEST branches are never submitted (all forced to `branchType=LONG`); **per-branch measures are NOT carried** (CE recomputes); **per-branch new-code-period is NOT migrated** | Per-branch report upload | 020 |
| **Scope** | New Code Periods | project / main-branch definition (NUMBER_OF_DAYS, PREVIOUS_VERSION) | uses **`/api/settings/set`** (`sonar.leak.period[.type]`) — **not** `/api/new_code_periods/set` (404s on Cloud); **per-branch overrides are skipped**; REFERENCE_BRANCH / SPECIFIC_ANALYSIS fall back to org default | `/api/settings/set` | 020 |
| **Scope** | Multi-Org Mapping | projects auto-grouped by ALM binding (GitHub/GitLab/Azure/Bitbucket) → Cloud orgs; DevOps-binding creation | **no key-conflict resolution** — key is always `{orgKey}_{key}`; on collision the existing project is verified-in-org and reused, else skipped (idempotent reuse, not resolution) | CSV (`organizations.csv`) + REST API | 018 |
| **Config** | Webhooks | project + global/server webhooks auto-recreated on Cloud (global fanned out to every migrated org); list-then-create idempotency | **webhook secret is NOT carried** (source API doesn't expose it) — migrated webhooks land unsecured; global fan-out runs on the `migrate` path only (`transfer` recreates project webhooks only) | `/api/webhooks/create` | — |

## ❌ What is NOT Migrated

| Entity / Data | Reason | Notes |
|---|---|---|
| User accounts | Cloud authentication is delegated to an IdP (SAML/Okta/GitHub/GitLab) | No user-creation call exists. **NOTE:** the previously-claimed `users.csv` login-mapping for assignment/comment attribution is **not implemented** — comments embed the raw Server login verbatim |
| **User Mapping (`users.csv`)** | Unimplemented (SPEC-010 is design intent only) | No `users.csv` is generated or loaded; no `user_mappings.go` exists |
| **Measures / Metrics** | Not transferred — recomputed by the Cloud CE on re-analysis | The scanner report ships an **empty** measures map; `BuildMeasures` is dead code. (Previously claimed as "60+ keys migrated" — false) |
| **Duplications** | Not transferred — recomputed by the Cloud CE on re-analysis | The `Duplication` protobuf types exist but are never fetched, built, or packaged |
| **Issue Assignments** | No syncable SonarCloud API (no `/api/issues/assign`) — **BUG-05** | Assignee is extracted but only used to decide whether a pair is "actionable"; it is never applied. (Previously listed as IS-MIGRATED — incorrect) |
| **Group membership** | Extracted but never applied | Only the group *name* is created on Cloud |
| **User-level permissions** (global, project, template) | SonarCloud has no user-permission API | Extracted, surfaced in reports as "not migrated", manual action required |
| Manual issue **type** changes | No syncable SonarCloud API | The matchable-issue model has no Type field. **NOTE:** not flagged as an "expected difference" — a type-count delta shows as a hard mismatch in regression checks |
| **Hotspot assignees** | SonarCloud exposes no hotspot-assign API | Must be reassigned manually |
| Active rule **parameters**, impacts, timestamps (scanner-report path) | Not populated by the active-rules builder — **BUG-02** | The XML restore path *does* carry params/impacts into the target profile definition; the scanner-report `activerules.pb` path does not |
| Plugins / non-SonarSource analyzers | Cloud runs a fixed analyzer set | 3rd-party issues migrate as external issues, but plugin code can't run |
| License keys | Cloud uses subscription model | Not applicable |
| Password reset tokens / local credentials | Cloud auth via IdP | Not applicable |
| Most global settings | Cloud API limitations | Only Cloud-supported settings migrate (some with changed semantics) |
| CI/CD pipeline configuration | Tool can't modify external systems | `SONAR_HOST_URL` / `SONAR_TOKEN` must be updated manually |
| `IN_SANDBOX` issue status (SQ 2025.1+) | No SonarCloud equivalent | **Omitted from the extraction query** on the live path (no warning emitted; the warn-and-skip code is in the unused `pipeline` package) |
| Closed issues (sync phase) | May not exist in Cloud after re-analysis | Skipped at the source-load stage, before status sync |
| Symbols / syntax-highlighting reference data | Lower-priority, optional (SPEC-004 FR-12/FR-13) | Endpoints never called; proto types unused |

## ⚠️ Known Gaps (real bugs / partial fidelity, verified in code)

| Item | Status | Spec |
|---|---|---|
| **BUG-02** — active rule **params (regex/thresholds), impacts, timestamps** missing from the scanner-report `activerules.pb` path (the XML restore path *does* carry them into the profile definition) | Real, partial | 001 / CLOUDVOYAGER-DELTA |
| **BUG-05** — issue **assignee** extracted but the assign call is never invoked (no Cloud assign API) | Real | 010 |
| **BUG-06** — no source-link / back-reference comment to the original Server issue/hotspot is added | Real | 008/009 |
| Hotspot resolution **ACKNOWLEDGED** is downgraded to SAFE on Cloud | Real | 009 |
| Issue status **FIXED** excluded from sync; **ACCEPTED** lands as WONTFIX | Real | 008 |
| External rule **descriptions** use a generic placeholder rather than rule-specific text | Real | 013 |
| SCM **author/revision** are stub values (only dates are real) | Real | 004 |
| Symbols / syntax-highlighting reference data | Optional, not implemented | 004 |
