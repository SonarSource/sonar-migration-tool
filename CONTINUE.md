# CONTINUE.md — Agent Handoff for Issue #104
<!-- updated: 2026-05-27_20:02:00 -->

## Current State Summary

We are implementing GitHub Issue #104: full issue and hotspot migration from SonarQube Server to SonarQube Cloud. The plan is at `~/.claude/plans/fuzzy-wobbling-stream.md` with 6 work streams.

**What's proven working:**
- CloudVoyager successfully transferred `sonar-rules-to-eslint-mapping` from SQ → SC staging (12 issues, tags synced, comments synced, branch mapping `main` → `master`)
- Our ZIP packaging fix (Deflate + CreateHeader) is committed
- Our branch name mapping (query SC for main branch name) is committed
- SC staging is healthy — the "component deleted" issue from earlier sessions is resolved

**What's in progress:**
- We just ran `extract --include_scan_history` for our migration tool — output is in `files/05-27-2026-06/`
- We need to run `migrate` with `--include_scan_history` to test our `importScanHistory` against the same project
- CloudVoyager's project was deleted from SC to allow a clean test

---

## Critical Credentials

### SonarQube Server (localhost:9000)
- **Token**: `squ_8ded92a3e8c2196a1a13c98a3bc7c68d126c3508`
- **Version**: 2026.2.0.121184

### SonarCloud Staging (sc-staging.io)
- **Admin token** (can create/delete projects): `0cc36fcc226dea8b4df02a842724f6915866a53b`
- **Organization**: `open-digital-society-1`
- **Enterprise**: `joshua-quek-sc-staging-enterprise`
- **SC main branch default**: `master` (SQ uses `main`)

> **IMPORTANT**: The old `sqco_` token (`sqco_UEDBrTINKqaEshw3xFzwVKFXO8oHnPu3ENn1GPTQmITzX03FsSvoAdw7NQ8`) is scanner-only — NO admin/provision permissions. Use the admin token above for all operations.

Config at `config.json` has already been updated with the new admin token.

---

## Projects Available for Testing

### In SonarQube Server:
1. `angular-framework` — large project (~7308 components, ~12K issues) — good for stress testing
2. `sonar-rules-to-eslint-mapping` — small project (2 files, 12 issues, 0 hotspots) — **ideal for quick testing**

### In SonarCloud staging org `open-digital-society-1`:
- `pixel-agents-desktop-rust` — exists, has active analysis
- Other projects may exist but had "component deleted" issues earlier
- `sonar-rules-to-eslint-mapping` was **deleted** to allow our migration tool to test clean

---

## Exact Next Steps

### Step 1: Run our migration tool's migrate command
```bash
cd /Users/joshua.quek/Desktop/agents-sonar-migration-tool/sonar-migration-tool.worktrees/fix-issue-104-migrate-all-issues
./go/sonar-migration-tool migrate 0cc36fcc226dea8b4df02a842724f6915866a53b joshua-quek-sc-staging-enterprise \
  --export_directory ./files/ \
  --include_scan_history
```

This will:
1. Create the `sonar-rules-to-eslint-mapping` project in SC
2. Run `importScanHistory` — build protobuf report and submit to CE
3. If CE succeeds → we match CloudVoyager's functionality for scan history import

### Step 2: Verify CE result
```bash
# Check CE task status
curl -s -H "Authorization: Bearer 0cc36fcc226dea8b4df02a842724f6915866a53b" \
  "https://sc-staging.io/api/ce/activity?component=sonar-rules-to-eslint-mapping&ps=5" \
  | python3 -m json.tool

# Check issues created
curl -s -H "Authorization: Bearer 0cc36fcc226dea8b4df02a842724f6915866a53b" \
  "https://sc-staging.io/api/issues/search?componentKeys=sonar-rules-to-eslint-mapping&organization=open-digital-society-1&ps=20" \
  | python3 -m json.tool
```

### Step 3: If CE fails, compare against CloudVoyager
Use CloudVoyager's working transfer as reference:
```bash
cd "/Users/joshua.quek/Desktop/Active Projects/CloudVoyager Agents/CloudVoyager"
node src/index.js transfer -c config-eslint-mapping.json --verbose --force-restart
```
CloudVoyager config for this project is at `config-eslint-mapping.json` in the CloudVoyager directory.

### Step 4: Continue with Work Streams 4-6 (Issue & Hotspot Metadata Sync)
Once `importScanHistory` works, implement the metadata sync tasks per the plan:
- **Work Stream 4**: `tasks_issuesync.go` — match SQ→SC issues by composite key, sync status/comments/tags
- **Work Stream 5**: `tasks_hotspotsync.go` — match SQ→SC hotspots, sync status/comments
- **Work Stream 6**: Register sync tasks in `planner.go`

---

## Key Files Modified (Uncommitted)

```
M  config.json                                    — Updated SC token to admin token
M  go/internal/migrate/tasks_scanhistory.go       — Branch mapping, logging fixes (3 lines)
M  docs/TROUBLESHOOTING.md                        — CE debugging findings (24 lines)
```

Most branch-handling changes are in commit `1eeb9d8`:
> "fix: enhance scan history import and metadata handling with improved branch synchronization and ZIP compression"

---

## Key Technical Context

### CloudVoyager Reference
- Location: `/Users/joshua.quek/Desktop/Active Projects/CloudVoyager Agents/CloudVoyager`
- **MANDATORY**: Always reference CloudVoyager when implementing new features — it's the proven reference implementation
- Key files: `src/pipelines/sq-2025/` for the transfer pipeline, `src/services/` for API clients

### Branch Name Mapping (Implemented)
SQ main branch `main` ≠ SC main branch `master`. Our code:
1. Calls `e.Cloud.Branches.List()` to discover SC main branch name
2. Uses SC name in protobuf metadata (`BranchName`) and CE submit (`characteristic=branch=...`)
3. Keeps SQ name for filtering extracted data (issues, components, sources)

### ZIP Format (Fixed)
Must use `zip.Deflate` + `zw.CreateHeader(fh)` — NOT `zip.Store` + `zw.CreateRaw(fh)`. Java's `ZipInputStream` can't parse Store entries with data descriptors. See `docs/TROUBLESHOOTING.md` for full root cause analysis.

### CE Authentication
SC `sqco_` tokens and the admin token both use **Bearer** auth (not Basic). Our `authTransport` handles this correctly.

---

## CloudVoyager Transfer Results (Reference)
For `sonar-rules-to-eslint-mapping`, CloudVoyager produced:
- 3 components (1 PROJECT + 2 FILES)
- 12 issues across 2 components
- 446 active rules
- 2 source files, 2 changesets, 6 duplications
- ZIP size: ~14KB
- CE processing time: ~2.5s
- Metadata sync: 11/12 issues matched (1 filtered as no manual changes), tags + comments synced
