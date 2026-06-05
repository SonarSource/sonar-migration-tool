---
spec_id: SPEC-021
title: Migration Verification Pipeline
status: draft
priority: P1
epic: "Verification & Reporting"
depends_on: [SPEC-002, SPEC-003, SPEC-005]
estimated_effort: L
cloudvoyager_ref: "src/shared/verification/"
---

# SPEC-021: Migration Verification Pipeline
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Migration Verification Pipeline is a read-only command that compares data between SonarQube Server and SonarQube Cloud after migration, producing a comprehensive report of matches, mismatches, and expected differences. This is a direct harvest of CloudVoyager's verification subsystem, which performs 34+ checks across organizations, projects, issues, hotspots, measures, configurations, and permissions. Additional checks may be added as new migration capabilities are implemented.

Verification is critical for enterprise migrations where compliance requirements (SOC2, ISO 27001, FedRAMP) demand evidence that historical data was transferred completely and accurately. Without an automated verification pipeline, operators must manually spot-check random samples, which provides no statistical confidence and misses systematic failures (e.g., all issues for a specific rule lost, all measures for a specific language wrong).

The verification pipeline makes NO modifications to either SonarQube Server or SonarQube Cloud. It is purely a comparison engine that reads from both systems, matches entities using composite keys, and reports discrepancies. The output is a structured JSON report that can be rendered as Markdown, PDF, or console summary.

## Problem Statement

After a migration, operators have no automated way to verify that data was transferred correctly. Manual verification is impractical for large installations: a 500-project migration with 200K issues per project means 100 million issues to verify. The current tool provides migration reports that track what was attempted and what succeeded at the API level, but not whether the resulting data in SonarQube Cloud matches the source data in SonarQube Server. A migration can "succeed" (all API calls returned 200) while still having data integrity issues (wrong issue counts, missing measures, incorrect quality gate assignments).

The verification pipeline closes this gap by providing automated, exhaustive comparison with configurable tolerance thresholds, entity-level matching, and a structured report format suitable for audit evidence.

## User Stories

- **As a** migration operator, **I want to** verify that all issues were migrated correctly, **so that** I have confidence the historical data is complete and accurate.
- **As a** migration operator, **I want to** verify that measures match between server and cloud, **so that** trend dashboards display consistent data after migration.
- **As a** migration operator, **I want to** generate a verification report for compliance auditors, **so that** I can demonstrate data integrity for SOC2/ISO 27001 requirements.
- **As a** migration operator, **I want to** verify only specific aspects of the migration, **so that** I can quickly check a particular area without running the full verification suite.
- **As a** migration operator, **I want to** understand which mismatches are expected vs unexpected, **so that** I can focus investigation on real problems.
- **As a** migration operator, **I want to** verify per-branch data, **so that** I know branch-level migrations completed correctly.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Implement `verify` command as a new top-level CLI command | Must |
| FR-2 | Make all verification checks read-only (no modifications to either system) | Must |
| FR-3 | Output structured JSON report with per-check pass/warn/fail status | Must |
| FR-4 | Render report as Markdown and PDF in addition to JSON | Should |
| FR-5 | Print console summary with total checks, passed, warned, failed | Must |
| FR-6 | Verify quality gates exist with correct conditions per organization | Must |
| FR-7 | Verify quality profiles exist with correct rule counts (custom profiles only) | Must |
| FR-8 | Verify user groups were created per organization | Must |
| FR-9 | Verify global permissions were granted correctly per organization | Must |
| FR-10 | Verify permission templates were created (reference check) | Must |
| FR-11 | Verify each project exists in SonarQube Cloud | Must |
| FR-12 | Verify all SQ Server branches exist in SonarQube Cloud per project | Must |
| FR-13 | Verify issue count matches within configurable tolerance per project per branch | Must |
| FR-14 | Verify issue status distribution matches per project | Must |
| FR-15 | Verify issue status history (transitions) is preserved | Should |
| FR-16 | Verify issue assignments match for mapped users | Should |
| FR-17 | Verify issue comments are present | Should |
| FR-18 | Verify issue tags match | Should |
| FR-19 | Verify hotspot count matches per project | Must |
| FR-20 | Verify hotspot status matches per project | Must |
| FR-21 | Verify hotspot review comments are present | Should |
| FR-22 | Verify 18 key measures per project within tolerance | Must |
| FR-23 | Verify quality gate assignment per project | Must |
| FR-24 | Verify quality profile assignment per language per project | Must |
| FR-25 | Verify project settings were migrated | Should |
| FR-26 | Verify project tags were migrated | Should |
| FR-27 | Verify project links were migrated | Should |
| FR-28 | Verify new code periods match per project | Should |
| FR-29 | Verify portfolios contain correct projects (reference check) | Should |
| FR-30 | Support `--only` flag to verify specific components (issues, measures, gates, etc.) | Must |
| FR-31 | Match issues using `rule|file|line` composite key | Must |
| FR-32 | Match hotspots using `ruleKey|file|line` composite key | Must |
| FR-33 | Classify expected differences as warnings, not failures | Must |
| FR-34 | Support configurable tolerance thresholds for numeric comparisons | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Verification throughput | >= 10 projects/minute for org-level checks |
| NFR-2 | Issue verification throughput | >= 500 issues/second for matching |
| NFR-3 | Measure comparison speed | < 1 second per project for 18 metrics |
| NFR-4 | Report generation time | < 5 seconds for JSON + Markdown |
| NFR-5 | Memory usage | Stream issue comparison; do not load all issues simultaneously |
| NFR-6 | Concurrency | Configurable parallel project verification (default 5) |
| NFR-7 | API rate limiting | Respect SQ Server and SC rate limits during verification |

## Technical Design

### Architecture

The verification pipeline introduces a new package:

```
go/internal/verify/
├── verify.go               # Pipeline orchestrator
├── config.go               # VerifyConfig, tolerance thresholds, component filters
├── report.go               # Report data structures and serialization
├── summary.go              # Console summary formatter
├── checkers/
│   ├── gates.go            # Quality gate verification
│   ├── profiles.go         # Quality profile verification
│   ├── groups.go           # User group verification
│   ├── permissions.go      # Global permission verification
│   ├── templates.go        # Permission template verification
│   ├── projects.go         # Project existence verification
│   ├── branches.go         # Branch existence verification
│   ├── issues.go           # Issue comparison (count, status, metadata)
│   ├── hotspots.go         # Hotspot comparison
│   ├── measures.go         # Measure comparison (18 metrics)
│   ├── project_config.go   # Settings, tags, links, new code period
│   └── portfolios.go       # Portfolio composition verification
├── matchers/
│   ├── issue_matcher.go    # rule|file|line composite key matching
│   └── hotspot_matcher.go  # ruleKey|file|line composite key matching
└── reporters/
    ├── json_reporter.go    # Structured JSON output
    ├── markdown_reporter.go # Human-readable Markdown
    └── pdf_reporter.go     # PDF for compliance audits

go/cmd/verify.go             # CLI command definition
```

### Key Algorithms

#### Issue Matching

```go
type IssueCompositeKey struct {
    Rule string
    File string
    Line int
}

func BuildIssueKey(issue Issue) IssueCompositeKey {
    return IssueCompositeKey{
        Rule: normalizeRule(issue.Rule),
        File: issue.Component, // relative file path
        Line: issue.TextRange.StartLine,
    }
}

func MatchIssues(sqIssues, scIssues []Issue) *MatchResult {
    // Build index of SC issues by composite key
    scIndex := map[IssueCompositeKey][]Issue{}
    for _, issue := range scIssues {
        key := BuildIssueKey(issue)
        scIndex[key] = append(scIndex[key], issue)
    }

    result := &MatchResult{}
    matchedSCKeys := map[string]bool{}

    for _, sqIssue := range sqIssues {
        key := BuildIssueKey(sqIssue)
        candidates, ok := scIndex[key]
        if !ok {
            result.UnmatchedSQ = append(result.UnmatchedSQ, sqIssue)
            continue
        }

        // Find best match among candidates (prefer same status)
        bestIdx := 0
        for i, c := range candidates {
            if !matchedSCKeys[c.Key] && c.Status == sqIssue.Status {
                bestIdx = i
                break
            }
        }

        if bestIdx < len(candidates) && !matchedSCKeys[candidates[bestIdx].Key] {
            result.Matched = append(result.Matched, MatchedPair{
                SQ: sqIssue,
                SC: candidates[bestIdx],
            })
            matchedSCKeys[candidates[bestIdx].Key] = true
        } else {
            result.UnmatchedSQ = append(result.UnmatchedSQ, sqIssue)
        }
    }

    // Find SC-only issues
    for _, scIssue := range scIssues {
        if !matchedSCKeys[scIssue.Key] {
            result.UnmatchedSC = append(result.UnmatchedSC, scIssue)
        }
    }

    return result
}
```

#### Rule Normalization

```go
// normalizeRule handles rule key format differences between SQ Server and SC.
// SQ Server: "java:S1234" or "squid:S1234" (legacy) or "external_eslint:no-unused-vars"
// SC: same format, but "squid:" prefix is replaced with "java:" in modern versions
func normalizeRule(rule string) string {
    // Normalize legacy "squid:" prefix to "java:"
    if strings.HasPrefix(rule, "squid:") {
        rule = "java:" + strings.TrimPrefix(rule, "squid:")
    }
    return rule
}
```

#### Measure Comparison

```go
var verifiedMetrics = []MetricDef{
    {Key: "ncloc", Name: "Lines of Code", Tolerance: 0},
    {Key: "complexity", Name: "Cyclomatic Complexity", Tolerance: 0},
    {Key: "violations", Name: "Issues", Tolerance: 0.05},         // 5% tolerance
    {Key: "bugs", Name: "Bugs", Tolerance: 0},
    {Key: "vulnerabilities", Name: "Vulnerabilities", Tolerance: 0},
    {Key: "code_smells", Name: "Code Smells", Tolerance: 0.05},
    {Key: "coverage", Name: "Coverage", Tolerance: 0.001},         // 0.1% tolerance
    {Key: "duplicated_lines_density", Name: "Duplicated Lines %", Tolerance: 0.001},
    {Key: "sqale_index", Name: "Technical Debt", Tolerance: 0},
    {Key: "reliability_rating", Name: "Reliability Rating", Tolerance: 0},
    {Key: "security_rating", Name: "Security Rating", Tolerance: 0},
    {Key: "sqale_rating", Name: "Maintainability Rating", Tolerance: 0},
    {Key: "security_hotspots", Name: "Security Hotspots", Tolerance: 0},
    {Key: "new_violations", Name: "New Issues", Tolerance: 0},
    {Key: "new_coverage", Name: "New Coverage", Tolerance: 0.001},
    {Key: "new_duplicated_lines_density", Name: "New Duplicated Lines %", Tolerance: 0.001},
    {Key: "cognitive_complexity", Name: "Cognitive Complexity", Tolerance: 0},
    {Key: "security_hotspots_reviewed", Name: "Hotspots Reviewed %", Tolerance: 0.001},
}

func CompareMeasures(sqMeasures, scMeasures map[string]float64) []MeasureCheck {
    var checks []MeasureCheck
    for _, metric := range verifiedMetrics {
        sqVal, sqExists := sqMeasures[metric.Key]
        scVal, scExists := scMeasures[metric.Key]

        check := MeasureCheck{
            Metric:   metric.Key,
            Name:     metric.Name,
            Expected: sqVal,
            Actual:   scVal,
        }

        if !sqExists && !scExists {
            check.Status = "skip"
        } else if !scExists {
            check.Status = "fail"
            check.Message = "metric missing in SonarQube Cloud"
        } else if !sqExists {
            check.Status = "warn"
            check.Message = "metric only exists in SonarQube Cloud"
        } else if metric.Tolerance == 0 {
            if sqVal == scVal {
                check.Status = "pass"
            } else {
                check.Status = "fail"
                check.Message = fmt.Sprintf("expected %v, got %v", sqVal, scVal)
            }
        } else {
            diff := math.Abs(sqVal - scVal)
            maxVal := math.Max(math.Abs(sqVal), 1.0)
            relDiff := diff / maxVal
            if relDiff <= metric.Tolerance {
                check.Status = "pass"
            } else {
                check.Status = "fail"
                check.Message = fmt.Sprintf("expected %v (±%.1f%%), got %v (diff: %.2f%%)",
                    sqVal, metric.Tolerance*100, scVal, relDiff*100)
            }
        }

        checks = append(checks, check)
    }
    return checks
}
```

#### Expected Differences Classification

```go
type ExpectedDifference struct {
    Category    string
    Description string
    Severity    string // "warning"
}

var expectedDifferences = []ExpectedDifference{
    {
        Category:    "issue_type",
        Description: "Manual issue type changes in SQ Server cannot be synced to SC (no API support)",
        Severity:    "warning",
    },
    {
        Category:    "issue_severity_override",
        Description: "Per-issue severity overrides in SQ Server are not API-syncable to SC",
        Severity:    "warning",
    },
    {
        Category:    "hotspot_assignment",
        Description: "Hotspot assignee field is read-only in SC API; assignments may differ",
        Severity:    "warning",
    },
    {
        Category:    "external_issue_count",
        Description: "External issues may differ if SC runs additional analyzers",
        Severity:    "warning",
    },
    {
        Category:    "new_code_measures",
        Description: "New code period measures may differ if the period definition changed",
        Severity:    "warning",
    },
}

func ClassifyDifference(checkName string, sqValue, scValue any) string {
    for _, ed := range expectedDifferences {
        if strings.Contains(checkName, ed.Category) {
            return "warn" // expected difference
        }
    }
    return "fail" // unexpected difference
}
```

### Data Flow

#### Verification Pipeline

```
1. Parse CLI flags (--token, --export_directory, --output_dir, --only)
2. Load org mapping from export directory (organizations.csv, projects.csv)
3. Create SQ Server and SC API clients
4. For each organization:
   a. Run org-level checkers (gates, profiles, groups, permissions, templates)
   b. For each project in the organization:
      i.   Check project exists in SC
      ii.  Check branches exist
      iii. For each branch:
           - Compare issue counts
           - Match issues by composite key
           - Compare matched issue metadata (status, assignments, comments, tags)
           - Compare hotspot counts and statuses
           - Compare 18 key measures
      iv.  Check quality gate assignment
      v.   Check quality profile assignments (per language)
      vi.  Check project settings, tags, links
      vii. Check new code period
   c. Check portfolios contain correct projects
5. Compute summary (total checks, pass, warn, fail)
6. Write reports (JSON, Markdown, optional PDF)
7. Print console summary
8. Exit with code 0 (all pass), 1 (any fail), or 2 (error)
```

#### Component Filtering (`--only`)

```
--only issues          → Run only issue verification
--only measures        → Run only measure verification
--only gates           → Run only quality gate verification
--only profiles        → Run only quality profile verification
--only groups          → Run only group verification
--only permissions     → Run only permission verification
--only branches        → Run only branch existence verification
--only config          → Run only project config verification
--only portfolios      → Run only portfolio verification
--only all             → Run all (default)
```

Multiple `--only` values can be combined: `--only issues --only measures`

### Report Structure

```json
{
  "tool_version": "1.5.0",
  "verification_date": "2026-05-26T12:00:00Z",
  "duration_seconds": 120,
  "summary": {
    "total_checks": 1234,
    "passed": 1200,
    "warned": 30,
    "failed": 4
  },
  "organizations": [
    {
      "org_key": "my-org",
      "checks": [
        { "name": "quality_gates", "status": "pass", "details": "3/3 gates verified" },
        { "name": "quality_profiles", "status": "pass", "details": "5/5 profiles verified" },
        { "name": "groups", "status": "pass", "details": "4/4 groups verified" },
        { "name": "global_permissions", "status": "warn", "details": "1 permission not verifiable" }
      ],
      "projects": [
        {
          "project_key": "my-project",
          "branches": ["main", "develop"],
          "checks": [
            { "name": "project_exists", "status": "pass" },
            { "name": "branch_count", "status": "pass", "expected": 2, "actual": 2 },
            { "name": "issue_count_main", "status": "pass", "expected": 500, "actual": 498 },
            { "name": "issue_status_distribution", "status": "pass" },
            { "name": "measures_ncloc", "status": "pass", "expected": 12500, "actual": 12500 },
            { "name": "measures_coverage", "status": "pass", "expected": 78.5, "actual": 78.5 },
            { "name": "quality_gate_assignment", "status": "pass", "expected": "Sonar way", "actual": "Sonar way" }
          ]
        }
      ]
    }
  ],
  "expected_differences": [
    { "category": "issue_severity_override", "count": 3, "description": "..." }
  ]
}
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/qualitygates/list` | GET | Fetch quality gates from SQ Server and SC |
| `/api/qualitygates/show` | GET | Fetch quality gate conditions |
| `/api/qualityprofiles/search` | GET | Fetch quality profiles from both systems |
| `/api/user_groups/search` | GET | Fetch groups from both systems |
| `/api/permissions/users` | GET | Fetch global permissions |
| `/api/components/show` | GET | Verify project exists in SC |
| `/api/project_branches/list` | GET | List branches for comparison |
| `/api/issues/search` | GET | Fetch issues from both systems for comparison |
| `/api/hotspots/search` | GET | Fetch hotspots from both systems |
| `/api/measures/component` | GET | Fetch measures from both systems |
| `/api/qualitygates/get_by_project` | GET | Verify gate assignment |
| `/api/qualityprofiles/search` | GET | Verify profile assignment (with `project` param) |
| `/api/project_tags/search` | GET | Verify project tags |
| `/api/project_links/search` | GET | Verify project links |
| `/api/new_code_periods/show` | GET | Verify new code period |
| `/api/views/show` | GET | Verify portfolio composition |

### CLI Interface

```bash
# Full verification
sonar-migration-tool verify \
  --source_url https://sq.example.com \
  --source_token squ_xxx \
  --cloud_token sqc_xxx \
  --export_directory ./files/ \
  --output_dir ./verify-output/

# Verify only issues and measures
sonar-migration-tool verify \
  --source_url https://sq.example.com \
  --source_token squ_xxx \
  --cloud_token sqc_xxx \
  --export_directory ./files/ \
  --only issues --only measures

# Verify with custom tolerance
sonar-migration-tool verify \
  --source_url https://sq.example.com \
  --source_token squ_xxx \
  --cloud_token sqc_xxx \
  --export_directory ./files/ \
  --issue_count_tolerance 0.02 \
  --measure_tolerance 0.01
```

## Acceptance Criteria

- [ ] AC-1: `verify` command exists and runs read-only checks against both SQ Server and SC
- [ ] AC-2: Verification makes ZERO write API calls to either system
- [ ] AC-3: JSON report is generated with per-check pass/warn/fail status
- [ ] AC-4: Markdown report is generated alongside JSON
- [ ] AC-5: Console summary prints total checks, passed, warned, failed
- [ ] AC-6: Quality gates are verified: existence, condition count, condition values
- [ ] AC-7: Quality profiles are verified: existence, rule count (custom profiles only)
- [ ] AC-8: User groups are verified: existence per organization
- [ ] AC-9: Global permissions are verified: permission grants match
- [ ] AC-10: Projects are verified: existence in correct SC organization
- [ ] AC-11: Branches are verified: all SQ Server branches exist in SC
- [ ] AC-12: Issues are matched by `rule|file|line` composite key
- [ ] AC-13: Issue count comparison within tolerance reports pass/fail correctly
- [ ] AC-14: Issue status distribution comparison detects mismatches
- [ ] AC-15: Hotspots are matched by `ruleKey|file|line` composite key
- [ ] AC-16: 18 key measures are compared with configurable tolerance
- [ ] AC-17: Quality gate and profile assignments are verified per project
- [ ] AC-18: Expected differences (type changes, severity overrides) are classified as warnings
- [ ] AC-19: `--only` flag filters verification to specific components
- [ ] AC-20: Exit code is 0 for all pass, 1 for any fail, 2 for error
- [ ] AC-21: Verification completes in under 2 minutes for a 100-project organization

## CloudVoyager Reference

| Area | Path |
|------|------|
| Verify Pipeline | `src/shared/verification/verify-pipeline/` |
| Issue Checker | `src/shared/verification/checkers/issues/` |
| Hotspot Checker | `src/shared/verification/checkers/hotspots/` |
| Measure Checker | `src/shared/verification/checkers/measures/` |
| Quality Gate Checker | `src/shared/verification/checkers/quality-gates/` |
| Quality Profile Checker | `src/shared/verification/checkers/quality-profiles/` |
| Branch Checker | `src/shared/verification/checkers/branches/` |
| Permission Checker | `src/shared/verification/checkers/permissions/` |
| Project Config Checker | `src/shared/verification/checkers/project-config/` |
| Portfolio Checker | `src/shared/verification/checkers/portfolios.js` |
| Report Writers | `src/shared/verification/reports/` |

## Known Limitations

- Issue matching by `rule|file|line` (3-field composite key) may produce false matches for rules that fire on the same line in the same file (e.g., two different code smells on line 10); this is inherent to the composite key approach and affects < 0.1% of issues in practice. The 3-field composite key (rule + file + line) provides sufficient matching for most cases. If collisions are detected (multiple issues with same rule on same line), a 4-field key adding StartOffset may be used as a tiebreaker (see SPEC-024 for the extended matching logic).
- Hotspot assignment verification is limited because SC's hotspot assignment API is read-only; assignment mismatches are always classified as warnings
- External issues may have different counts if SonarQube Cloud's built-in analyzers detect additional issues not present on SQ Server
- Measure comparison for `new_*` metrics depends on the new code period being identical; if the period differs, these measures will always mismatch
- Portfolio verification is a reference check only (correct projects listed); it does not verify computed portfolio-level measures
- Verification requires API tokens for both SQ Server (read) and SC (read); if the SQ Server is decommissioned before verification, the check cannot run

## Open Questions

- Should verification support a `--fix` mode that attempts to correct mismatches (e.g., re-syncing issue statuses)? This would violate the read-only guarantee but could be useful as a separate command.
- Should the tool store verification results in the checkpoint journal so that repeated verifications show improvement over time?
- What is the right default tolerance for issue count comparison? CloudVoyager uses 0% (exact match) but some customers accept 1-2% tolerance.
- Should verification run automatically after migration completes, or only when explicitly invoked?
- Should the PDF report include visual charts (issue distribution pie charts, measure comparison bar charts) or only tables?
