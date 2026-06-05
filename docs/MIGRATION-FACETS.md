# SonarQube Server → SonarCloud — Migration Coverage

## ✅ What IS Migrated

| Layer | Entity | Fields / Data Carried Over | Mechanism | Spec |
|---|---|---|---|---|
| **Structure** | Projects | key, name, settings, links, visibility, tags | REST API | baseline |
| **Structure** | Quality Gates | gate definition + all conditions (metric, operator, threshold) + project assignment | REST API | baseline |
| **Structure** | Quality Profiles | rule activations, inheritance chains, custom rules (XML backup/restore) | REST API | 011/018 |
| **Structure** | Groups | group name + membership | REST API (V2 `/authorizations/groups` for 2025.1+, fallback `/user_groups/search`) | 011/018 |
| **Structure** | Permissions | global + project-level grants per user/group | REST API | baseline |
| **Structure** | Permission Templates | template definition + permission rules + group/user associations | REST API | baseline |
| **Structure** | Portfolios (Enterprise) | structure, hierarchy, project composition | REST API | baseline |
| **Structure** | Global Permissions | login, permission (admin/profileadmin/gateadmin/scan/provisioning), org_key | CSV + REST API | 018 |
| **Analysis Data** | Issues | rule (repo+key), file/component, text_range (start/end line, start/end offset), message, msgFormatting, flows/secondary locations, severity, overriddenSeverity, type, cleanCodeAttribute, impacts, overriddenImpacts, codeVariants, gap/effort/debt, quickFixAvailable | Protobuf scanner report → `/api/ce/submit` | 002 |
| **Analysis Data** | Security Hotspots | rule, message, text_range, securityCategory, vulnerabilityProbability (→severity), author, creation/update dates | Protobuf scanner report | 003 |
| **Analysis Data** | Source Code | raw file content, language, line count | `source-{ref}.txt` in report ZIP | 004 |
| **Analysis Data** | SCM / Blame Data | per-line revision, author, date (back-dated to oldest issue creation date) | Protobuf `Changesets` + `backdateChangesets()` | 004 |
| **Analysis Data** | Issue Creation Dates | original creation dates preserved | Via SCM changeset backdating | 004 |
| **Analysis Data** | Measures / Metrics | 60+ keys: ncloc, lines, complexity, cognitive_complexity, violations, bugs, vulnerabilities, code_smells, security_hotspots, coverage (line/branch), duplicated_lines/blocks/files + density, tests/failures/errors, sqale_index/rating, reliability/security/security_review ratings, remediation efforts, all `new_*` leak-period variants | Protobuf `Measure` (type-aware) | 005 |
| **Analysis Data** | Duplications | origin position + duplicate references (other-file ref + line range) | Protobuf `Duplication` | 001/005 |
| **Analysis Data** | Active Rules | repo, key, severity, params, timestamps, q-profile key, impacts | Protobuf `activerules.pb` | 001 |
| **Analysis Data** | External Issues (3rd-party analyzers) | engineId, ruleId, message, severity, type, textRange | Protobuf `externalissues-{ref}.pb` | 013 |
| **Analysis Data** | Ad-Hoc Rules | engineId, ruleId, name, description, severity, type, cleanCodeAttribute | Protobuf `adhocrules.pb` | 013 |
| **Metadata Sync** | Issue Status | OPEN, CONFIRMED, REOPENED, RESOLVED/FIXED, FALSE_POSITIVE, ACCEPTED, WONTFIX | `/api/issues/do_transition` | 008/024 |
| **Metadata Sync** | Issue Comments | text + original author + timestamp, prefixed `[Migrated from SonarQube Server - @author]`, hash-deduped | `/api/issues/add_comment` | 008/024 |
| **Metadata Sync** | Issue Tags | all custom tags | `/api/issues/set_tags` | 008/024 |
| **Metadata Sync** | Issue Assignments | assignee mapped Server→Cloud login via users.csv | `/api/issues/assign` | 008/010 |
| **Metadata Sync** | Hotspot Review Status | TO_REVIEW / REVIEWED + resolution SAFE/FIXED/ACKNOWLEDGED | `/api/hotspots/change_status` | 009/024 |
| **Metadata Sync** | Hotspot Comments | review comments with author attribution | `/api/hotspots/add_comment` | 009/024 |
| **Metadata Sync** | User Mapping | Server login → Cloud login (+ display name, email, include flag) | users.csv | 010 |
| **Scope** | Branches | main + non-main (LONG/SHORT/PULL_REQUEST); per-branch issues, source, SCM, measures, new-code-period; reference-branch mapping | Per-branch report upload (main first) | 020 |
| **Scope** | New Code Periods | per-branch definition | `/api/new_code_periods/set` | 020 |
| **Scope** | Multi-Org Mapping | projects auto-grouped by ALM binding (GitHub/GitLab/Azure/Bitbucket) → Cloud orgs, key-conflict resolution | CSV + REST API | 018 |

## ❌ What is NOT Migrated

| Entity / Data | Reason | Notes |
|---|---|---|
| User accounts | Cloud authentication is delegated to an IdP (SAML/Okta/GitHub/GitLab) | Only login *mapping* is done via users.csv for assignment/comment attribution |
| Manual issue **type** changes | No syncable SonarCloud API | Flagged as expected difference in verification |
| Per-issue **severity overrides** | No syncable SonarCloud API | Flagged as expected difference in verification |
| **Hotspot assignees** | SonarCloud exposes no hotspot-assign API | Must be reassigned manually |
| Webhooks | SonarCloud webhook model differs | Extracted but require manual recreation |
| Plugins / non-SonarSource analyzers | Cloud runs a fixed analyzer set | 3rd-party issues migrate as external issues, but plugin code can't run |
| License keys | Cloud uses subscription model | Not applicable |
| Password reset tokens / local credentials | Cloud auth via IdP | Not applicable |
| Most global settings | Cloud API limitations | Only Cloud-supported settings migrate (some with changed semantics) |
| CI/CD pipeline configuration | Tool can't modify external systems | `SONAR_HOST_URL` / `SONAR_TOKEN` must be updated manually |
| `IN_SANDBOX` issue status (SQ 2025.1+) | No SonarCloud equivalent | Logged as warning, skipped |
| Closed issues (sync phase) | May not exist in Cloud after re-analysis | Skipped during status sync |

## ⚠️ Known Gaps (planned/partial in roadmap, not yet fully wired)

| Item | Status | Spec |
|---|---|---|
| Active rule **parameters** (regex, thresholds), impacts, timestamps in profile migration | Missing (BUG-02) | 011 |
| Issue assignee sync actually invoked | Extracted but not applied (BUG-05) | 010 |
| Source-link comment back to original Server issue/hotspot | Missing (BUG-06) | 008/009 |
| Symbols / syntax-highlighting reference data | Lower-priority, optional | 004 |
| External rule descriptions (rule-specific vs generic) | Generic placeholder used | 013 |
