# MIGRATION-FACETS.md — Adversarial Code Audit
<!-- updated: 2026-06-05_21:03:00 -->

Adversarial verification of every claim in [MIGRATION-FACETS.md](MIGRATION-FACETS.md) against the **actual Go code** (`go/`, `lib/sq-api-go/`) — not the specs, not the doc's own wording. Each of the 45 claims was independently verified by one agent, then a second independent agent tried to *overturn* that verdict from the code. 89 agents, ~4.9M tokens.

> **Verdict definitions** — CONFIRMED: accurate as stated. PARTIALLY_CONFIRMED: core true, but a listed field/mechanism/detail is wrong, missing, or overstated. CONTRADICTED: materially wrong (e.g. listed as migrated but isn't, or listed as *not* migrated but is).

## Headline
<!-- updated: 2026-06-05_21:03:00 -->

| Verdict | Count |
|---|---|
| ✅ CONFIRMED | 14 |
| ⚠️ PARTIALLY_CONFIRMED | 22 |
| ❌ CONTRADICTED | 9 |
| **Total audited** | **45** |

**Bottom line: the doc is NOT accurate as written.** Only ~31% of claims are fully correct. The dominant failure mode is that the doc **describes the SPECs' design intent rather than what the code actually does** — many cited mechanisms exist only as *dead code* (the `go/internal/pipeline` package, `BuildMeasures`, `NewCodePeriodsClient.Set`, the `Duplication` protobuf types) or as draft specs that were never implemented (`users.csv`, key-conflict resolution).

### Two internal self-contradictions the audit caught
<!-- updated: 2026-06-05_21:03:00 -->

1. **Issue Assignments**: row 28 lists them as ✅ *IS MIGRATED* via `/api/issues/assign`, but row 58 lists **BUG-05** "assignee extracted but not applied" as a known gap. The code confirms **BUG-05 is the truth** — there is no `/api/issues/assign` call or `Assign` method anywhere. The IS-MIGRATED row contradicts the doc's own gap list.
2. **Per-issue severity overrides**: row 15 lists `overriddenSeverity` as carried in the scanner report (true), while row 42 lists severity overrides as ❌ *NOT migrated*. The override **is** migrated via the `overriddenSeverity` protobuf field, so the NOT-MIGRATED row is wrong.

---

## ❌ CONTRADICTED — materially wrong (9)
<!-- updated: 2026-06-05_21:03:00 -->

| Claim | What the doc says | What the code does |
|---|---|---|
| **Groups** | name + membership via V2 `/authorizations/groups` (fallback `/user_groups/search`) | Only **name** migrates (from `groups.csv` → `Groups.Create`). **Membership is NOT migrated.** The cited V2/fallback path lives only in `go/internal/pipeline` (`sq2025.go`), which is **dead code** (no importers, `ExtractGroups` never called). Live extraction uses `api/permissions/groups` (`extract/tasks_users.go:80`) — neither cited endpoint. |
| **Global Permissions** | login + permission(admin/profileadmin/gateadmin/scan/provisioning) + org_key via CSV | No `global-permissions.csv`, no `GlobalPermission` struct exist. **User global perms are NOT migrated** (`summary/collect.go:152` logs "were not migrated"). Only **group** org-perms migrate, restricted by an allowlist (`helpers.go:183`) that drops `profileadmin`/`gateadmin`/`provisioning` — only `admin`+`scan` survive. |
| **Measures / Metrics** | 60+ keys via Protobuf `Measure` (type-aware) | **NO measures migrate at all.** `ReportData.Measures` is hardcoded empty (`tasks_projectdata.go:441`). `BuildMeasures` is **dead code** (test-only caller). Extraction requests only ~12 keys (feeding a local markdown report). "60+ keys" is fabricated. |
| **Duplications** | origin + duplicate refs via Protobuf `Duplication` | The `Duplication`/`Duplicate` proto types are **generated-but-unused**. Nothing fetches, builds, or packages duplication data. **Not migrated** — should be a gap, not IS-MIGRATED. |
| **Issue Assignments** | sync via `/api/issues/assign` + `users.csv` mapping | **No `/api/issues/assign`, no `Assign` method** anywhere. Assignee is extracted only to flag a pair as "actionable"; `syncOnePair` does transition+comments+tags only. (= confirmed BUG-05.) |
| **User Mapping** | Server→Cloud login (+name, email, include flag) via `users.csv` | **`users.csv` is never generated or loaded.** `user_mappings.go` files and all named symbols don't exist. SPEC-010 is unimplemented design intent. |
| **New Code Periods** | per-branch via `/api/new_code_periods/set` | Live code uses **`/api/settings/set`** (`sonar.leak.period`); code comment says `/api/new_code_periods/set` "does NOT exist on SonarCloud — calls 404". `NewCodePeriodsClient.Set` is **dead code**. Per-branch NCD is explicitly **skipped** (issue #134); only project/main-level NCD migrates. |
| **Per-issue severity overrides (NOT migrated)** | not migrated; flagged in verification | Override severity **IS migrated** via `overriddenSeverity` proto field 4 (`builder.go:189`), live-wired into the uploaded report. And **no verification flagging exists** (regtest uses exact-match). |
| **Webhooks (NOT migrated)** | extracted, require manual recreation | Webhooks **ARE auto-recreated** via `setProjectWebhooks` + `setGlobalWebhooks` → `Cloud.Webhooks.Create` (`api/webhooks/create`), both wired into the DAG. Only the webhook **secret** isn't carried (source API doesn't expose it). |

---

## ⚠️ PARTIALLY_CONFIRMED — core true, details overstated/wrong (22)
<!-- updated: 2026-06-05_21:03:00 -->

| Claim | Accurate part | Inaccurate / overstated part |
|---|---|---|
| **Projects** | name, settings, links, tags migrate via REST | **visibility** hardcoded to `private` (`tasks_create.go:76`), never extracted; **key** transformed to `{orgKey}_{key}`, not verbatim |
| **Quality Profiles** | rule activations + inheritance via XML backup/restore | "custom rules" conditional — **template-instantiated custom rules are un-migratable** and flagged by the tool's own analyzer; unsupported-language profiles filtered out |
| **Permissions** | **group** grants migrate (global/project/template/profile) | **user grants NOT applied** — extracted then skipped (`collect.go:149`: "Cloud does not support user permissions via API") |
| **Permission Templates** | definition + **group** rules/associations | **user associations NOT migrated** (no `AddUserToTemplate`) |
| **Portfolios** | structure + project composition via REST | **hierarchy is FLATTENED, not preserved** — Cloud has no portfolio hierarchy; nesting collapsed to a flat project list |
| **Issues** | mechanism + rule, component, text_range, message, severity(→overriddenSeverity) | For **native** issues, NOT populated: `msgFormatting`, flows/secondary locations, `codeVariants`, `overriddenImpacts`, `quickFixAvailable`, `gap/effort/debt`. `type`/`cleanCodeAttribute`/`impacts` aren't even fields on the native Issue proto (external-issue path only). ~5 of ~14 listed fields are real. |
| **Security Hotspots** | rule, message, text_range, vulnProbability(→severity); creation date via backdating | `securityCategory`, `author` (hardcoded stub), and update dates **NOT migrated**; hotspots flattened to **generic issues** (no SECURITY_HOTSPOT type set) |
| **Source Code** | content, language, line count all migrate | Mechanism misattributed: language + line count are in **`component-{ref}.pb`**, not `source-{ref}.txt`. Part of the project-data migration (default-on on both `migrate` and `transfer`; opt out via `--skip_project_data_migration`) |
| **SCM / Blame** *(direct check)* | **date** back-dated to issue creation date via `BackdateChangesets()` — real & wired | **per-line revision** is `randomHex(20)` stub (`builder.go:73`); **author** is a hardcoded stub (`stubAuthor`). Only the date is genuine SCM data |
| **Active Rules** | repo, key, severity, q-profile key migrate (+ key remapped) | **impacts, params, timestamps NOT migrated** — params always empty map, timestamps fall back to migration run-time |
| **External Issues** | all 6 fields migrate via protobuf — accurate | Filename wrong: code writes **`external-issues-{ref}.pb`** (hyphen), not `externalissues-{ref}.pb` |
| **Ad-Hoc Rules** | 6 fields encoded correctly | **name** = rule key (not display name); **description** = generated literal `"Rule from {engine} plugin"`, not source description |
| **Issue Status** | mechanism + 5 states map correctly | **FIXED excluded** entirely from sync (`tasks_issuesync.go:536`); **ACCEPTED maps to `wontfix`** → lands as WONTFIX on Cloud, not ACCEPTED |
| **Issue Comments** | text, author, timestamp, `add_comment` mechanism | Prefix is **`[Migrated from {login} on {date}]`**, not `[Migrated from SonarQube Server - @author]`; dedup is **substring match, not hash-deduped** |
| **Hotspot Review Status** | SAFE/FIXED sync correctly | **ACKNOWLEDGED silently downgraded to SAFE** (`tasks_hotspotsync.go:135`) |
| **Branches** | main + non-main **LONG** migrate (main-first), issues/source/SCM/ref-branch/analysis handshake | **SHORT skipped, PULL_REQUEST never submitted** (all forced to LONG); **measures empty**; per-branch **NCD skipped**; SCM author/revision stubbed |
| **Multi-Org Mapping** | auto-group by ALM binding → orgs via CSV+REST | **"key-conflict resolution" does NOT exist** — always `orgKey_key` + idempotent reuse; no resolver, no availability check |
| **User accounts (NOT migrated)** | core correct — no user creation | substitute mechanism wrong: `users.csv` login-mapping/attribution **not implemented**; comment embeds raw SQ login |
| **Manual type changes (NOT migrated)** | core correct — no type sync | "flagged as expected difference in verification" **false** — regtest treats a type-count diff as a hard mismatch |
| **IN_SANDBOX (NOT migrated)** | core correct — not migrated | "logged as warning, skipped" **false on the live path** — the warn+skip is in dead `pipeline` code; live path silently omits the status from the query |
| **Closed issues (NOT migrated)** | outcome correct — not synced | skip is at **load stage** (`tasks_issuesync.go:533`), not "during status sync" (status logic would `resolve` CLOSED if it ever reached it) |
| **BUG-02 rule params (KNOWN GAP)** | **real** for the scanner-report active-rules path (params/impacts/timestamps missing there) | overstated: the **XML restore path DOES carry** params/impacts into the target profile definition; cited spec-011 is the wrong spec |

---

## ✅ CONFIRMED — accurate as stated (14)
<!-- updated: 2026-06-05_21:03:00 -->

- **Quality Gates** — definition + conditions (metric/op/error) + project assignment via REST. ✓
- **Issue Creation Dates** — preserved via SCM changeset backdating. ✓
- **Issue Tags** — `set_tags`. ✓
- **Hotspot Comments** — `add_comment` with attribution. ✓
- **Hotspot assignees (NOT)** — no hotspot-assign API; not migrated. ✓
- **Plugins (NOT)** — extracted/reported but not installed on Cloud. ✓
- **License keys (NOT)** — not applicable. ✓
- **Passwords/local creds (NOT)** — not applicable. ✓
- **Most global settings (NOT)** — allowlist-filtered. ✓
- **CI/CD config (NOT)** — tool never modifies external systems. ✓
- **BUG-05 assignee not invoked (GAP)** — real; assignee extracted, never applied. ✓
- **BUG-06 source-link comment missing (GAP)** — real; no back-link in either comment builder. ✓
- **Symbols/highlighting (GAP)** — proto types unused; endpoints never called. ✓
- **External rule descriptions generic (GAP)** — `"Rule from {engine} plugin"` placeholder. ✓

---

## Cross-cutting root causes
<!-- updated: 2026-06-05_21:03:00 -->

1. **Doc tracks SPEC intent, not implementation.** Groups V2, `users.csv`, key-conflict resolution, 60+ measures, per-branch NCD via `/api/new_code_periods/set`, Duplications — all are spec-described but unbuilt or built differently.
2. **Dead code masquerades as live coverage.** `go/internal/pipeline` (V2 groups, IN_SANDBOX warn-skip), `BuildMeasures`, `NewCodePeriodsClient.Set`, `Duplication`/`Symbol` proto types all exist but are never reached by the running binary.
3. **Native-vs-external issue conflation.** Many "Issues" fields (`type`, `cleanCodeAttribute`, `impacts`, ad-hoc descriptions) apply only to the external-issue path, not native issues.
4. **Stub SCM identity.** Only issue *dates* are real; per-line *revision* and *author* are synthetic stubs.

## Recommended doc fixes (priority order)
<!-- updated: 2026-06-05_21:03:00 -->

1. **Move to NOT-MIGRATED / GAP:** Measures, Duplications, Issue Assignments, User Mapping, Groups *membership*.
2. **Move to IS-MIGRATED:** Webhooks (auto-recreated; note secret caveat); Per-issue severity overrides (via `overriddenSeverity`).
3. **Correct mechanisms:** New Code Periods → `/api/settings/set` (not `new_code_periods/set`); External Issues filename → `external-issues-{ref}.pb`; Source language/lines → `component-{ref}.pb`.
4. **Add field-level caveats:** Issues (native field coverage), Active Rules (no params/impacts/timestamps), Branches (LONG-only, no measures), Permissions/Templates (group-only), Portfolios (flattened), Projects (visibility forced private, key prefixed).
5. **Fix status fidelity notes:** FIXED not synced, ACCEPTED→WONTFIX, hotspot ACKNOWLEDGED→SAFE.
6. **Fix the two self-contradictions** (Assignments, severity overrides).
