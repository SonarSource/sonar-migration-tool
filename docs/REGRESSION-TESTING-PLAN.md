# Live Regression Testing Plan
<!-- updated: 2026-06-04_16:00:00 -->

After any fix or feature, run **both** migration paths against real SonarQube Server and SonarCloud instances, then verify **everything** migrated correctly. "Everything" means: projects, issues (all statuses/severities/types/resolutions), hotspots, quality profiles, quality gates, groups, permission templates, settings, new code periods, custom rules, project permissions, ALM bindings, portfolios, measures, and extract file integrity.

Both paths must be tested because they exercise different code paths:
- **Transfer** (`transfer`) — single-command, auto-populates CSVs, targets one project via `--project_key`
- **Full migration** (`extract` → `structure` → `mappings` → `migrate`) — multi-step, manual CSV review, migrates all projects

---

## Common Steps

### 1. Build
```bash
cd go && go build -o ../sonar-migration-tool ./main.go && cd ..
```

### 2. Health check
```bash
SQ_URL=$(jq -r '.source.url' transfer-config.json)
SQ_TOKEN=$(jq -r '.source.token' transfer-config.json)
SC_URL=$(jq -r '.target.url' transfer-config.json)
SC_TOKEN=$(jq -r '.target.token' transfer-config.json)
curl -sf -u "${SQ_TOKEN}:" "${SQ_URL}/api/system/status" | jq .status
curl -sf -H "Authorization: Bearer ${SC_TOKEN}" "${SC_URL}/api/system/status" | jq .status
```
Both must return `"UP"`. If not, stop — servers are down.

---

## Path A: Transfer (Single Project)
<!-- updated: 2026-06-04_16:00:00 -->

Tests the `transfer` command, which chains extract → structure → mappings → migrate in one call for a single project.

### A1. Clean slate
```bash
./sonar-migration-tool reset --config transfer-config.json
```

### A2. Run transfer
```bash
rm -rf ./migration-files/
./sonar-migration-tool transfer --config transfer-config.json --project_key <PROJECT_KEY> 2>&1 | tee /tmp/smt-transfer.log
```
Must exit 0. If not, inspect the log and fix.

### A3. Run regtest
```bash
./sonar-migration-tool regtest --config transfer-config.json --format table --verbose 2>&1 | tee /tmp/smt-regtest-transfer.log
```
Exit code 0 = **ALL PASS**. Exit code 1 = failures exist — read the table, fix, loop back to A2.

### A4. Scan logs for silent failures
```bash
grep -Ei "panic|fatal" /tmp/smt-transfer.log
grep -Ei "error|fail" /tmp/smt-transfer.log | grep -v "INFO"
```

---

## Path B: Full Migration (All Projects)
<!-- updated: 2026-06-04_16:00:00 -->

Tests the multi-step flow that migrates every project visible to the token.

### B1. Clean slate
```bash
./sonar-migration-tool reset --config config.json
```

### B2. Extract
```bash
rm -rf ./migration-files/
./sonar-migration-tool extract --config config.json 2>&1 | tee /tmp/smt-extract.log
```
Must exit 0.

### B3. Structure
```bash
./sonar-migration-tool structure --config config.json 2>&1 | tee /tmp/smt-structure.log
```
Must exit 0. Review `migration-files/organizations.csv` — confirm `sonarcloud_org_key` is populated for every organization.

### B4. Mappings
```bash
./sonar-migration-tool mappings --config config.json 2>&1 | tee /tmp/smt-mappings.log
```
Must exit 0. Spot-check `gates.csv`, `profiles.csv`, `groups.csv`, `templates.csv`, `portfolios.csv` — ensure mappings look correct.

### B5. Migrate
```bash
./sonar-migration-tool migrate --config config.json 2>&1 | tee /tmp/smt-migrate.log
```
Must exit 0.

### B6. Run regtest
```bash
./sonar-migration-tool regtest --config config.json --format table --verbose 2>&1 | tee /tmp/smt-regtest-migrate.log
```
The `regtest` command compares SonarQube Server vs SonarCloud across **27 check functions / 44+ checks**: project count and identity, issue totals and distributions (5 statuses, 5 severities, 3 types, 3 resolutions per project), hotspot totals and status distribution, quality profile count/rules/defaults/inheritance, quality gate count/conditions/default/associations, group count/membership, permission template count/permissions, global+project settings, new code periods, custom rules, project group permissions (6 types), ALM bindings, portfolios, measures (12 metrics per project), and extract file completeness.

Exit code 0 = **ALL PASS**. Exit code 1 = failures exist — read the table, fix, loop back to B5.

### B7. Scan logs for silent failures
```bash
grep -Ei "panic|fatal" /tmp/smt-extract.log /tmp/smt-structure.log /tmp/smt-mappings.log /tmp/smt-migrate.log
grep -Ei "error|fail" /tmp/smt-extract.log /tmp/smt-structure.log /tmp/smt-mappings.log /tmp/smt-migrate.log | grep -v "INFO"
```

---

## Loop
<!-- updated: 2026-06-04_16:00:00 -->

If any check failed or logs show errors in either path: fix the code, rebuild (step 1), re-run the failing path. Repeat until **both** paths produce regtest exit 0 with zero log errors. That is the only stop condition.
