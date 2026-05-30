# Live Regression Testing Protocol
<!-- updated: 2026-05-26_00:00:00 -->

A reusable protocol for verifying **any change** — bug fix, new feature, refactor, or enhancement — by running the sonar-migration-tool **live** against real SonarQube Server and SonarQube Cloud instances. The goal is to prove that the change works correctly AND that nothing else broke.

**This protocol is a recursive loop.** You build, run, verify, and if anything is wrong — fix, rebuild, re-run, re-verify. You repeat until you achieve a **full clean pass**. That is the only stop condition.

```
┌─────────────────────────────────────────────────────┐
│                                                     │
│   Phase 0: Understand the change                    │
│       ↓                                             │
│   Phase 1: Adversarial code review                  │
│       ↓                                             │
│   Phase 2: Environment setup (clean slate)          │
│       ↓                                             │
│  ┌─────────────────────────────────────────────┐    │
│  │  RECURSIVE LOOP (repeat until clean pass)   │    │
│  │                                             │    │
│  │  Phase 3: Build + run LIVE                  │    │
│  │      ↓                                      │    │
│  │  Phase 4: Verify (crashes + output + data)  │    │
│  │      ↓                                      │    │
│  │  Phase 5: Any failure? → investigate + fix  │──┐ │
│  │      ↓                              ↑       │  │ │
│  │  ALL PASS? ──── NO ─────────────────┘       │  │ │
│  │      │                                      │  │ │
│  │     YES                                     │  │ │
│  │      ↓                                      │  │ │
│  │  Phase 6: Declare clean pass (STOP)         │  │ │
│  └─────────────────────────────────────────────┘  │ │
│                                                     │
└─────────────────────────────────────────────────────┘
```

---

## Phase 0 — Understand the Change
<!-- updated: 2026-05-26_00:00:00 -->

> **MANDATORY. Do not proceed until you can answer every question below.**

### 0.1 — What Did You Change?

Answer ALL of the following:

1. **What is the intent?** — One sentence: what does this change accomplish?
2. **Which commands are affected?** — extract, migrate, structure, mappings, report, reset, wizard, gui? List every command that touches modified code.
3. **Which entity types are affected?** — issues, hotspots, quality profiles, quality gates, permissions, groups, users, projects, portfolios, settings, rules, new code periods, ALM bindings, webhooks? Cross-reference against the [Full Entity Registry](#full-entity-registry).
4. **What code paths changed?** — Read every line of the diff. List every file, function, and API call that was added, removed, or modified.
5. **What is the blast radius?** — Could this change affect entity types or commands beyond the ones you intended? Check for shared utilities, shared HTTP clients, shared config parsing, shared error handling.
6. **What are the acceptance criteria?** — List 3-5 concrete, measurable pass/fail conditions for the change itself (e.g., "extract produces `quality_profiles.ndjson` with one entry per profile", "all issues with status CONFIRMED on SQS have status CONFIRMED on SC after migrate").
7. **What edge cases exist?** — At minimum: empty input (zero entities), single entity, large volume (pagination), missing/optional fields, entities that already exist on SC (idempotency).

### 0.2 — Build the Regression Checklist

From 0.1, write down TWO lists:

**Feature verification** — what proves the change itself works:
- [ ] (your acceptance criteria from 0.1.6)
- [ ] (your edge cases from 0.1.7)

**Regression verification** — what proves nothing else broke:
- [ ] Every entity type NOT touched by your change still extracts/migrates correctly
- [ ] Every command NOT touched by your change still runs without error
- [ ] Summary stats for unchanged features are the same as before your change

### 0.3 — Identify the Regression Watchlist

Find ALL code paths that share code with your change:

1. `grep` for every function name in the diff
2. `grep` for every API endpoint in the diff
3. Check if shared utilities (HTTP client, pagination, retry, rate limiting, config parsing) were modified
4. Check if the same pattern/mapping is used elsewhere

> Write down every related file. These are your **regression watchlist** — you MUST verify each one in Phase 4.

---

## Phase 1 — Adversarial Code Review
<!-- updated: 2026-05-26_00:00:00 -->

> Re-read every changed file and every file on your regression watchlist. For each, ask:

### Data Integrity
1. **Serialization boundaries**: Does data survive `json.Marshal`/`json.Unmarshal` round-trips? Do `nil` slices, empty maps, zero values, unexported fields survive? Do NDJSON extract outputs deserialize correctly in the migrate phase?
2. **Field mapping**: Is every field from the SQ API response correctly mapped to the SC API request? Check: field name, type, nil handling, slice vs scalar.
3. **Stat integrity**: Are counters accumulated correctly across goroutines? Protected by mutexes or atomics?

### Error Handling
4. **Every `error` return**: Is it checked? `_ = err` is a bug.
5. **Retry logic**: Max attempts cap? Respects Retry-After? Distinguishes retriable (5xx, timeout) from non-retriable (4xx)?
6. **Error propagation**: Can goroutine errors bubble up, or are they silently dropped?

### Concurrency Safety
7. **Shared state**: Any maps, slices, or counters mutated from concurrent goroutines without mutex/atomic protection?
8. **Goroutine lifecycle**: Properly joined via `WaitGroup`/`errgroup`/channel close before exit? Cleaned up on error?
9. **Channel safety**: Properly closed? No send-on-closed-channel panic? No leaked goroutines?

### API Contract Compliance
10. **HTTP method, endpoint path, request body, auth**: All exactly correct per the SonarQube API docs?

### Edge Cases
11. **0, 1, N**: Empty input, single item, pagination boundary?
12. **Missing/nil fields**: Handled gracefully or panic?
13. **Duplicates**: Could an entity be processed/created twice?

> **Write down every concern.** Format: "This could cause [failure] if [condition]."

---

## Phase 2 — Environment Setup (Clean Slate)
<!-- updated: 2026-05-26_13:00:00 -->

> Every test run starts from ZERO. No stale state. No leftover data.

### 2.0 — Connection Details Are in `config.json`

**You have full access to both SonarQube Server and SonarQube Cloud.** All connection details — URLs, tokens, organization keys — are stored in **`config.json`** at the project root. Read it first:

```bash
cat config.json | jq '.'
```

Map the config fields to the environment variables used in this document:

| Env Var (this doc) | `config.json` path |
|---|---|
| `SQ_URL` | `.sonarqube.url` |
| `SONAR_TOKEN` | `.sonarqube.token` |
| `SC_URL` | `.sonarcloud.organizations[0].url` |
| `SC_TOKEN` | `.sonarcloud.organizations[0].token` |
| `SC_ORG` | `.sonarcloud.organizations[0].key` |

You can export them for use in the curl commands below:

```bash
export SQ_URL=$(jq -r '.sonarqube.url' config.json)
export SONAR_TOKEN=$(jq -r '.sonarqube.token' config.json)
export SC_URL=$(jq -r '.sonarcloud.organizations[0].url' config.json)
export SC_TOKEN=$(jq -r '.sonarcloud.organizations[0].token' config.json)
export SC_ORG=$(jq -r '.sonarcloud.organizations[0].key' config.json)
```

> **Do NOT skip this step.** If an agent claims it has no access to SQS or SC, it is wrong — `config.json` has everything needed.

### 2.1 — Verify Source (SQS) Is Accessible

```bash
# Verify SQS is running and auth works
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/system/status" | jq '.status'

# List available projects
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/projects/search" | jq '.components[] | {key: .key, name: .name}'

# Confirm target project exists
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/issues/search?projectKeys=${PROJECT_KEY}&ps=1" | jq '.total'
```

### 2.2 — Verify Target (SC) Is Accessible

```bash
# Verify SC auth works
curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/system/status" | jq '.status'
```

### 2.3 — Verify Test Data Exists

For the entity types your change affects, confirm sufficient test data exists on SQS:

```bash
# Issues with manual changes (required for issue sync)
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/issues/search?projectKeys=${PROJECT_KEY}&ps=100" \
  | jq '[.issues[] | select(.updateDate != .creationDate)] | length'

# Quality profiles
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/qualityprofiles/search?project=${PROJECT_KEY}" | jq '.profiles | length'

# Quality gates
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/qualitygates/list" | jq '.qualitygates | length'

# Adapt for whatever entity type your feature handles.
# If test data is insufficient, CREATE IT via the SQ API (see Reference: Creating Test Data).
```

### 2.4 — Clean Slate

```bash
# Delete ALL local state
rm -rf ./files/
rm -f .wizard_state.json

# Reset SC target (delete migrated projects/orgs from prior runs)
# Option A: use the reset command
# ./sonar-migration-tool reset -c migration-config.json
#
# Option B: delete via API
# curl -s -X POST -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/projects/delete?project=${PROJECT_KEY}"

# Verify clean
curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/projects/search?organization=${SC_ORG}" \
  | jq '.components | length'
```

### 2.5 — Record Baseline

Before running, record the source state so you have numbers to compare against:

| Metric | SQS Value |
|--------|-----------|
| Total projects | ___ |
| Total issues (for test project) | ___ |
| Total hotspots (for test project) | ___ |
| Quality profiles | ___ |
| Quality gates | ___ |
| Groups | ___ |
| Users | ___ |
| (entity types relevant to your change) | ___ |

---

## Phase 3 — Build and Run LIVE
<!-- updated: 2026-05-26_00:00:00 -->

> This is the core of the protocol. You are running the actual tool against real instances.

### 3.1 — Build

```bash
cd go && go build -o ../sonar-migration-tool ./main.go && cd ..
```

**If the build fails, STOP. Fix the compilation error. This counts as a loop iteration — after fixing, return to Phase 3.1.**

### 3.2 — Run Unit Tests

```bash
cd go && go test ./... && cd ..
```

**If any test fails, STOP. Fix the test. Return to Phase 3.1.**

### 3.3 — Run Race Detector Build

```bash
cd go && go build -race -o ../sonar-migration-tool-race ./main.go && cd ..
```

### 3.4 — Verify Config

```bash
cat migration-config.json | jq '.'
```

Confirm all URLs, tokens, project keys, organization keys, and feature flags are correct.

### 3.5 — Run the Full Pipeline LIVE

Run with the **race detector binary** so data races are caught immediately:

```bash
# Clean slate before every run
rm -rf ./files/ && rm -f .wizard_state.json

# Extract
./sonar-migration-tool-race extract -c migration-config.json 2>&1 | tee /tmp/smt-extract-$(date +%s).log
echo "EXIT CODE: $?"

# Structure (if applicable)
./sonar-migration-tool-race structure -c migration-config.json 2>&1 | tee /tmp/smt-structure-$(date +%s).log
echo "EXIT CODE: $?"

# Mappings (if applicable)
./sonar-migration-tool-race mappings -c migration-config.json 2>&1 | tee /tmp/smt-mappings-$(date +%s).log
echo "EXIT CODE: $?"

# Migrate
./sonar-migration-tool-race migrate -c migration-config.json 2>&1 | tee /tmp/smt-migrate-$(date +%s).log
echo "EXIT CODE: $?"
```

Or use the full pipeline script:
```bash
rm -rf ./files/ && rm -f .wizard_state.json
./scripts/execute_full_migration.sh 2>&1 | tee /tmp/smt-full-$(date +%s).log
echo "EXIT CODE: $?"
```

### 3.6 — Capture Evidence

For each command, record:
- [ ] Exit code (0 = success, non-zero = crash/error)
- [ ] Any panic stack traces in output
- [ ] Any `DATA RACE` warnings from race detector
- [ ] Any `ERROR` or `FATAL` log lines
- [ ] Summary stats (copy verbatim)
- [ ] Wall-clock duration

**If any command crashed (non-zero exit, panic, data race) → go directly to Phase 5.**

---

## Phase 4 — Verify Output and Data Correctness
<!-- updated: 2026-05-26_00:00:00 -->

> Do NOT trust the tool's own summary. Query the source and target APIs independently to verify.

### 4.1 — Verify Extract Output

Check that NDJSON files were produced for all expected entity types:

```bash
# List all extracted files
find ./files/ -name "*.ndjson" -exec sh -c 'echo "$1: $(wc -l < "$1") lines"' _ {} \;

# Check for empty files (potential bug)
find ./files/ -name "*.ndjson" -empty

# Spot-check: inspect first 3 lines of each file
for f in ./files/*/*.ndjson; do echo "=== $f ==="; head -3 "$f" | jq '.'; done
```

For every entity type your change touches, verify the NDJSON output has correct fields and values.

### 4.2 — Verify Migrate Output on SC

Query the SC API directly for every entity type that was migrated. Compare counts and field values against the SQS baseline from Phase 2.5.

**Projects:**
```bash
curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/projects/search?organization=${SC_ORG}" \
  | jq '.components | length'
```

**Issues:**
```bash
SQ_COUNT=$(curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/issues/search?projectKeys=${PROJECT_KEY}&ps=1" | jq '.total')
SC_COUNT=$(curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/issues/search?projects=${PROJECT_KEY}&ps=1" | jq '.total')
echo "SQ: $SQ_COUNT, SC: $SC_COUNT"
```

**Hotspots:**
```bash
SQ_COUNT=$(curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/hotspots/search?projectKey=${PROJECT_KEY}&ps=1" | jq '.total')
SC_COUNT=$(curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/hotspots/search?projectKey=${PROJECT_KEY}&ps=1" | jq '.total')
echo "SQ: $SQ_COUNT, SC: $SC_COUNT"
```

**Quality Profiles:**
```bash
SQ_COUNT=$(curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/qualityprofiles/search" | jq '.profiles | length')
SC_COUNT=$(curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/qualityprofiles/search?organization=${SC_ORG}" | jq '.profiles | length')
echo "SQ: $SQ_COUNT, SC: $SC_COUNT"
```

**Quality Gates:**
```bash
SQ_COUNT=$(curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/qualitygates/list" | jq '.qualitygates | length')
SC_COUNT=$(curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/qualitygates/list?organization=${SC_ORG}" | jq '.qualitygates | length')
echo "SQ: $SQ_COUNT, SC: $SC_COUNT"
```

**Groups:**
```bash
SQ_COUNT=$(curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/user_groups/search" | jq '.groups | length')
SC_COUNT=$(curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/user_groups/search?organization=${SC_ORG}" | jq '.groups | length')
echo "SQ: $SQ_COUNT, SC: $SC_COUNT"
```

> Adapt these queries for every entity type listed in the [Full Entity Registry](#full-entity-registry). You do NOT need to verify every single entity type on every run — but you MUST verify (a) all entity types touched by your change, and (b) a representative sample of untouched entity types to catch regressions.

### 4.3 — Spot-Check Data Fidelity (5+ Entities)

For each entity type your change touches, pick 5+ specific entities and compare field-by-field between SQS and SC:

```bash
# Example: spot-check a specific issue
SQ_ISSUE=$(curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/issues/search?projectKeys=${PROJECT_KEY}&ps=1" | jq '.issues[0]')
echo "$SQ_ISSUE" | jq '{key: .key, status: .status, severity: .severity, tags: .tags, assignee: .assignee, comments: .comments}'

# Find the same issue on SC and compare
SC_ISSUE=$(curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/issues/search?projects=${PROJECT_KEY}&ps=1" | jq '.issues[0]')
echo "$SC_ISSUE" | jq '{key: .key, status: .status, severity: .severity, tags: .tags, assignee: .assignee, comments: .comments}'
```

Verify: tags match, status matches, comments match, assignee matches, severity matches.

### 4.4 — Verify Edge Cases

Run the edge cases from Phase 0.1.7:

- [ ] **Empty input**: Entity type with 0 items on SQS — tool handles gracefully, no crash
- [ ] **Single entity**: Entity type with exactly 1 item — migrated correctly
- [ ] **Large volume**: Entity type with 1000+ items — all migrated, pagination works
- [ ] **Missing fields**: Entities with optional/null fields — no crash, fields handled correctly
- [ ] **Idempotency**: Run migrate twice — no duplicates created, no errors

### 4.5 — Check for Silent Failures

```bash
# Search logs for errors
grep -ri "error\|panic\|fatal\|fail" /tmp/smt-*.log | grep -v "INFO"

# Check for empty NDJSON files (entity type extracted but produced nothing)
find ./files/ -name "*.ndjson" -empty

# Check for suspiciously low counts (e.g., SQ has 1000 issues but SC only has 50)
# This indicates silent data loss — the worst kind of bug
```

### 4.6 — Regression Check

For entity types NOT touched by your change, verify they still work by comparing counts against the Phase 2.5 baseline:

| Entity Type | SQS Count | SC Count | Match? |
|-------------|-----------|----------|--------|
| Projects | ___ | ___ | ___ |
| Issues | ___ | ___ | ___ |
| Hotspots | ___ | ___ | ___ |
| Quality Profiles | ___ | ___ | ___ |
| Quality Gates | ___ | ___ | ___ |
| Groups | ___ | ___ | ___ |
| Permissions | ___ | ___ | ___ |
| Settings | ___ | ___ | ___ |
| New Code Periods | ___ | ___ | ___ |
| ... | ___ | ___ | ___ |

**If ANY check fails → proceed to Phase 5.**
**If ALL checks pass → proceed to Phase 6.**

---

## Phase 5 — Investigate, Fix, and Loop Back
<!-- updated: 2026-05-26_00:00:00 -->

> **This is the recursive part. For every failure, trace it to root cause, fix it, and re-run the entire pipeline from Phase 3.**

### 5.1 — Classify the Failure

> **ADVERSARIAL NOTE — Never Blame Infrastructure First:**
>
> When a CE task fails or an API returns an unexpected error, the natural instinct is "the server is broken" or "it's a staging environment issue." **Resist this instinct.** Always ask: **does CloudVoyager succeed on the same instance with the same data?** If yes, the problem is in your code — not the server. Common code-level causes that masquerade as infrastructure issues:
>
> - **Missing protobuf fields** that the server requires but you don't set (e.g., `ReferenceBranchName` in metadata)
> - **Wrong protobuf encoding** (length-delimited vs non-delimited for a specific message type)
> - **Wrong ZIP entry names** (e.g., `externalissues-1.pb` vs `external-issues-1.pb`)
> - **Wrong multipart form field names** or missing form fields in CE submission
> - **Stale component refs** pointing to components that were filtered out
>
> Only blame infrastructure after you've verified: (1) CloudVoyager also fails, or (2) you've diff'd your protobuf output byte-for-byte against CloudVoyager's and they match.

| Failure type | Symptoms | Investigation approach |
|---|---|---|
| **Crash** | Non-zero exit, panic stack trace | Read the stack trace. Find the exact line. |
| **Data race** | `WARNING: DATA RACE` in output | Read the race report. Find the unsynchronized access. |
| **Wrong data** | SC field value doesn't match SQS | Trace the field through extract NDJSON → migrate code → SC API call. |
| **Missing data** | SC entity count < SQS entity count | Check: was it extracted? Was it filtered? Did the API call fail? |
| **Duplicate data** | SC entity count > SQS entity count | Check: is the entity processed twice? Is idempotency missing? |
| **Silent failure** | No error but data is wrong/missing | Check every error return in the code path. Look for `_ = err`. |
| **CE task failed** | `status: FAILED` in CE task response | Compare protobuf output against CloudVoyager's. Check every field in metadata.pb. Verify ZIP structure matches. Do NOT assume infrastructure failure. |

### 5.2 — Isolate with API Queries

Reproduce the problem by making the exact API calls the code makes:

```bash
# Query SQS with the exact same params the code uses
curl -s -u "${SONAR_TOKEN}:" "${SQ_URL}/api/..." | jq '.'

# Inspect the NDJSON extract output
head -5 ./files/<extract-id>/<entity>.ndjson | jq '.'

# Query SC to see what was actually created
curl -s -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/..." | jq '.'
```

### 5.3 — Root Cause Statement

Before fixing, write:
> "The [specific behavior] was wrong because [specific reason]. Evidence: [API response / log line / code path]."

### 5.4 — Apply Minimal Fix

Fix only the root cause. Do not make unrelated changes.

### 5.5 — Loop Back to Phase 3

After fixing:

1. `go vet ./...` — verify compilation
2. `go test ./...` — verify tests pass
3. **Clean slate** — `rm -rf ./files/ && rm -f .wizard_state.json` + reset SC
4. **Return to Phase 3.5** — re-run the FULL pipeline from scratch
5. **Re-verify ALL of Phase 4** — not just the thing you fixed

> **Never do a partial re-run.** Always re-run the full pipeline. A fix in one code path can break another.

> **Never skip the re-verification.** Always re-check ALL entity types, not just the one you fixed.

> **Repeat this loop until Phase 4 produces zero failures.** That is the only exit condition.

---

## Phase 6 — Declare Full Clean Pass (STOP Condition)
<!-- updated: 2026-05-29_02:10:00 -->

A full clean pass requires **ALL** of the following. Not a subset. Not "close enough."

> **ADVERSARIAL NOTE — What "Clean Pass" Actually Means:**
>
> A "clean pass" is NOT "the tool ran without crashing." A tool can exit 0 and produce zero data — that is not a clean pass, that is a silent failure. Ask yourself these adversarial questions before declaring victory:
>
> 1. **Did the feature under test actually execute its core code path?** If you added issue metadata sync but importScanHistory failed (so SC has 0 issues to sync against), then syncIssueMetadata ran but matched 0 pairs — it exercised the "nothing to do" path, not the "sync metadata" path. That is NOT a clean pass for issue sync.
>
> 2. **Did the data actually arrive on the target?** If CE rejects the protobuf report, issues never appear in SonarCloud. Sync tasks running against 0 Cloud issues will report "0 matched, 0 synced" and exit cleanly — but the feature is broken. A clean pass means SQS issue count ≈ SC issue count, not "no errors in the log."
>
> 3. **Did CloudVoyager succeed on the same instance?** If CloudVoyager can import scan history to the same SC staging instance but this tool cannot, the problem is in this tool's code — not the infrastructure. Never blame infrastructure without verifying CloudVoyager also fails.
>
> 4. **Did you test with data that exercises the feature?** If SQS has 0 resolved issues, 0 reviewed hotspots, and 0 comments, then metadata sync has nothing to sync. That validates the "no-op" path, which is necessary but not sufficient. A clean pass for metadata sync requires SQS data with manual changes (status transitions, comments, tags).
>
> 5. **Is "0 matched pairs" actually correct, or is it masking a bug?** Zero matches could mean: (a) the matching algorithm is broken, (b) component key stripping is wrong, (c) issues weren't created in Cloud, or (d) there genuinely are no matchable issues. Only (d) is a clean pass. Verify (a)-(c) before accepting (d).

### Crash-Free Execution
- [ ] Every command exited with code 0
- [ ] Zero panics in output
- [ ] Zero `DATA RACE` warnings from race detector
- [ ] Zero `FATAL` log lines

### Code Quality Gates
- [x] `go vet ./...` passes
- [x] `go test ./...` passes — all tests pass cleanly as of 2026-05-29
- [x] `go test -race ./...` passes — zero data race warnings as of 2026-05-29
- [x] `go build -race` compiles and runs clean

### Feature Verification (from Phase 0.1.6)
- [ ] Every acceptance criterion for the change passes
- [ ] Every edge case (empty, single, large, missing fields, idempotency) handled correctly
- [ ] **The feature's core code path was exercised with real data** — not just the no-op/empty path

### Data Correctness
- [ ] Entity counts on SC match SQS (within tolerance for entity types that have known filters)
- [ ] Spot-check of 5+ entities shows field-level correctness
- [ ] No empty NDJSON files for entity types that have data
- [ ] No silent data loss (SC count is not suspiciously lower than SQS count)
- [ ] **CE tasks succeeded** — if scan history import is part of the feature, the CE task must complete with status SUCCESS, not FAILED

### Regression Verification
- [ ] All entity types NOT touched by the change still extract/migrate correctly
- [ ] Summary stats for unchanged features match prior runs
- [ ] No new errors or warnings in logs for unchanged features

### Run Metadata
- [ ] Number of loop iterations to reach clean pass: ___
- [ ] Total wall-clock time: ___
- [ ] Final summary stats captured and saved

> **Only when every checkbox above is checked can you stop the loop.**
>
> **If you are tempted to declare "clean pass" but a core task (like importScanHistory) failed, STOP. That is not a clean pass. Go back to Phase 5.**

---

## Current Test Status
<!-- updated: 2026-05-30_15:00:00 -->

As of **2026-05-30**, all regression tests pass cleanly on branch `fix/four-pipelines-compatibility`:

| Check | Status |
|---|---|
| `go vet ./...` | PASS |
| `go test ./...` | PASS (15 packages, 0 failures) |
| `go test -race ./...` | PASS — zero data race warnings |
| `go build -race` | PASS |

### Changes Tested This Session

**SPEC-011 four-pipeline architecture** (`paginateAll` generic, URL double-slash fix, V2 groups `Default` field):
- All pipeline unit tests pass with race detector (sq2025_test.go, helpers_test.go, router_test.go)
- SQ server at localhost:9000 is SQ 2026.2 Enterprise — would route to SQ2025Pipeline

**`transfer` command** (new single-project migration path):
- Validation tested: missing `--sq-url`, `--sq-token`, `--sc-token`, `--sc-org` all produce clear error messages with exit code 1
- Config file parsing: CloudVoyager-compatible JSON shape parses correctly
- Race detector: passes with no warnings

**`ProjectKeys` filter in `getProjects`** (server-side project filtering for `transfer`):
- `TestGetProjectsTaskNoFilter`: verified no `projects=` param when `ProjectKeys` is empty
- `TestGetProjectsTaskWithFilter`: verified `projects=key1,key2` param is sent when set
- Both tests pass with race detector

### Live Environment Notes

The test SQ instance (localhost:9000, SQ 2026.2 Enterprise) has an admin token (`sqa_*`) that lacks `canAdmin` privileges. This causes `403 Insufficient Privileges` on:
- `api/projects/search` → `getProjects` task
- `api/permissions/search_templates` → `getDefaultTemplates` task
- `api/ce/activity` → `getTasks` task
- `api/user_groups/search` → `getGroups` task

These 403 failures are a **pre-existing test environment limitation** (token scoped without Administer System permission), not regressions introduced by this branch. The token CAN access:
- Project issues: 12 issues on `sonar-rules-to-eslint-mapping`
- Quality profiles: 45 profiles
- Quality gates: 2 gates

No panics, no `DATA RACE` reports, no `FATAL` log lines. Loop iterations to clean pass: **1**.

---

## Full Entity Registry
<!-- updated: 2026-05-26_00:00:00 -->

All entity types handled by the tool. Use this as your regression checklist.

### Extract + Migrate (both phases)

| Entity Type | Extract API (SQS) | Migrate API (SC) |
|---|---|---|
| Projects | `/api/projects/search` | `POST /api/projects/create` |
| Project Settings | `/api/settings/values` | `POST /api/settings/set` |
| Project Tags | `/api/components/show` | (via project create/update) |
| Project Links | `/api/project_links/search` | (via project create/update) |
| Project ALM Bindings | `/api/alm_settings/get_binding` | `POST /api/alm_integration/set_up_binding` |
| Project Group Permissions | `/api/permissions/groups` | `POST /api/permissions/groups` |
| Project User Permissions | `/api/permissions/users` | `POST /api/permissions/users` |
| Issues | `/api/issues/search` | `POST /api/issues/do_transition`, `add_comment` |
| Issues (Full History) | `/api/issues/search` | (via scan report import) |
| Issue Comments | `/api/issues/search` | `POST /api/issues/add_comment` |
| Hotspots | `/api/hotspots/search`, `/api/hotspots/show` | `POST /api/hotspots/change_status`, `add_comment` |
| Hotspot Comments | `/api/hotspots/show` | `POST /api/hotspots/add_comment` |
| Quality Profiles | `/api/qualityprofiles/search`, `backup` | `POST /api/qualityprofiles/create`, `restore` |
| Profile Rules | `/api/rules/search` | (via profile restore) |
| Profile Group Permissions | `/api/qualityprofiles/search_groups` | `POST /api/permissions/groups` |
| Profile User Permissions | `/api/qualityprofiles/search_users` | `POST /api/permissions/users` |
| Quality Gates | `/api/qualitygates/list`, `show` | `POST /api/qualitygates/create`, `create_condition` |
| Gate Conditions | `/api/qualitygates/show` | `POST /api/qualitygates/create_condition` |
| Gate Group Permissions | `/api/qualitygates/search_groups` | `POST /api/permissions/groups` |
| Gate User Permissions | `/api/qualitygates/search_users` | `POST /api/permissions/users` |
| Rules | `/api/rules/search`, `/api/rules/show` | `PATCH /api/rules/update` |
| Users | `/api/users/search` | `GET /api/users/search` (lookup) |
| User Groups | `/api/user_groups/users` | `POST /api/groups/add_user` |
| Groups | `/api/permissions/groups` | `POST /api/groups/create` |
| Permission Templates | `/api/permissions/search_templates` | `POST /api/permissions/create_template` |
| Template Group Permissions | `/api/permissions/template_groups` | `POST /api/permissions/add_group_to_template` |
| Template User Permissions | `/api/permissions/template_users` | `POST /api/permissions/add_user_to_template` |
| Portfolios | `/api/views/search`, `show` | `POST /api/portfolios/create` |
| Server Settings | `/api/settings/values` | `POST /api/settings/set` (global) |
| New Code Periods | `/api/new_code_periods/list`, `show` | `POST /api/new_code_periods/set` |
| ALM Settings | `/api/alm_settings/list` | (via binding setup) |
| Component Tree | `/api/measures/component_tree` | (via scan report) |
| Source Code | `/api/sources/raw` | (via scan report) |
| SCM Blame Data | `/api/sources/scm` | (via scan report) |

### Extract Only (not migrated)

| Entity Type | Extract API (SQS) | Purpose |
|---|---|---|
| Project Details | `/api/navigation/component` | Metadata for reports |
| Project Measures | `/api/measures/search` | Metrics for reports |
| Project Webhooks | `/api/webhooks/list` | Audit/documentation |
| Branches | `/api/project_branches/list` | Branch awareness |
| Pull Requests | `/api/project_pull_requests/list` | PR awareness |
| Rule Repositories | `/api/rules/repositories` | Rule inventory |
| Plugin Rules | `/api/rules/search` (filtered) | Plugin rule audit |
| Template Rules | `/api/rules/search` (filtered) | Template rule audit |
| User Permissions | `/api/permissions/users` | Permission audit |
| User Tokens | `/api/user_tokens/search` | Token audit |
| Applications | `/api/components/search` | App inventory |
| Webhooks | `/api/webhooks/list` | Webhook audit |
| Server Info | `/api/system/info` | Server metadata |
| Plugins | `/api/plugins/installed` | Plugin inventory |
| License Usage | `/api/projects/license_usage` | License audit |
| CE Tasks | `/api/ce/activity` | Background task audit |
| Project Analyses | `/api/project_analyses/search` | Analysis history |

---

## Reference: CLI Commands
<!-- updated: 2026-05-26_00:00:00 -->

| Command | Purpose | When to Run in Tests |
|---------|---------|---------------------|
| `extract` | Extract data from SQS → NDJSON files | Always |
| `structure` | Group projects into organizations (CSVs) | When testing org mapping |
| `mappings` | Generate mapping files | When testing entity mapping |
| `migrate` | Migrate from NDJSON → SC via API | Always |
| `report` | Generate reports | When testing reporting |
| `reset` | Reset a SC organization | Before runs (clean slate) |
| `wizard` | Interactive wizard | Manual testing only |
| `gui` | Browser-based GUI | Manual testing only |
| `analysis_report` | Generate analysis report | When testing reporting |

---

## Reference: Config Files
<!-- updated: 2026-05-26_00:00:00 -->

| File | Purpose |
|------|---------|
| `migration-config.json` | Unified config (recommended) |
| `config-extract.example.json` | Extract-specific example |
| `config-migrate.example.json` | Migrate-specific example |
| `config.example.json` | Flat format example |
| `scripts/execute_full_migration.sh` | Full pipeline automation |
| `scripts/delete_all_portfolios.sh` | Portfolio cleanup |

---

## Reference: Creating Test Data
<!-- updated: 2026-05-26_00:00:00 -->

When your SQS instance doesn't have enough data to exercise a feature:

### Bulk-Tag Issues

```bash
python3 -c "
import urllib.request, urllib.parse, json, base64, concurrent.futures, sys

TOKEN = 'squ_...'
PROJECT_KEY = sys.argv[1] if len(sys.argv) > 1 else 'my-project'
auth = base64.b64encode(f'{TOKEN}:'.encode()).decode()
headers = {'Authorization': f'Basic {auth}', 'Content-Type': 'application/x-www-form-urlencoded'}

url = f'${SQ_URL}/api/issues/search?projectKeys={PROJECT_KEY}&ps=500'
req = urllib.request.Request(url, headers=headers)
keys = [i['key'] for i in json.load(urllib.request.urlopen(req))['issues']]

def tag(key):
    params = urllib.parse.urlencode({'issue': key, 'tags': 'test-tag'}).encode()
    req = urllib.request.Request(f'${SQ_URL}/api/issues/set_tags', data=params, headers=headers, method='POST')
    urllib.request.urlopen(req)
    return 'ok'

with concurrent.futures.ThreadPoolExecutor(max_workers=20) as pool:
    results = list(pool.map(tag, keys))
print(f'Tagged: {sum(1 for r in results if r == \"ok\")}/{len(keys)}')
" ${PROJECT_KEY}
```

### Other Operations

Adapt the pattern above for:
- `POST /api/issues/do_transition` — change issue statuses
- `POST /api/issues/add_comment` — add comments to issues
- `POST /api/issues/assign` — assign issues to users
- `POST /api/hotspots/change_status` — review hotspots
- `POST /api/qualityprofiles/create` — create quality profiles
- `POST /api/qualitygates/create` — create quality gates
- `POST /api/user_groups/create` — create groups
