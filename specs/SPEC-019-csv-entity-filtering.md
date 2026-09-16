---
spec_id: SPEC-019
title: CSV Entity Filtering & Dry-Run Workflow
status: draft
priority: P2
epic: "Migration Workflow"
depends_on: [SPEC-018]
estimated_effort: M
cloudvoyager_ref: "src/shared/mapping/, src/commands/migrate/"
---

# SPEC-019: CSV Entity Filtering & Dry-Run Workflow
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The CSV Entity Filtering & Dry-Run Workflow introduces a `--dry-run` flag on the migrate command that extracts all server-wide data and generates mapping CSVs without performing any actual migration. This enables a review-first workflow: the operator runs a dry run, inspects the generated CSVs, edits `Include` flags to exclude specific entities, adjusts mappings, and then re-runs the migration with the edited CSVs to migrate only the desired subset.

This is a direct harvest of CloudVoyager's dry-run capability, which was essential for enterprise customers who needed to perform selective migrations -- migrating only certain quality gates, specific groups, or a subset of projects -- without an all-or-nothing approach. The Include/Exclude pattern applies uniformly across all 9 CSV types (organizations, projects, groups, profiles, gates, portfolios, templates, global permissions, and users), providing a consistent filtering interface regardless of entity type.

The current Go tool already generates CSVs during the structure and mappings phases, but there is no dry-run mode that combines extraction + CSV generation into a single step, no `Include` column on CSVs for selective filtering, and no validation that edited CSVs still conform to the expected schema. This spec closes these gaps.

## Problem Statement

Enterprise migrations are rarely all-or-nothing. Operators commonly need to exclude deprecated quality gates, skip test groups, omit projects that are being decommissioned, or defer certain org-wide permission grants. The current tool requires the operator to manually delete rows from CSVs or skip entire phases, which is error-prone and undocumented. There is no first-class mechanism for "run everything up to the point of making changes, let me review, and then apply only what I approve."

Without a dry-run mode, operators must either trust that the extraction and mapping logic produces correct results (risky for first-time migrations) or perform the extract and structure commands manually, inspect intermediate files, and then run migrate with crossed fingers. The dry-run workflow replaces this ad-hoc process with a structured, repeatable approach.

## User Stories

- **As a** migration operator, **I want to** run a dry-run that generates all mapping CSVs without migrating anything, **so that** I can review and customize the migration plan before execution.
- **As a** migration operator, **I want to** set `Include=no` on specific entities in any CSV, **so that** those entities are excluded from migration without deleting them from the CSV.
- **As a** migration operator, **I want to** re-run the migration after editing CSVs, **so that** only the entities I approved are migrated.
- **As a** migration operator, **I want to** be warned if my edited CSVs have schema issues or missing data, **so that** I can fix problems before migration starts.
- **As a** migration operator, **I want to** see a summary of what will and will not be migrated before the migration begins, **so that** I can confirm the plan is correct.
- **As a** migration operator, **I want to** filter entities at every level (org-wide and per-project), **so that** I have granular control over the migration scope.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Add `--dry-run` flag to the migrate command | Must |
| FR-2 | In dry-run mode: extract all server-wide data, generate all 9 CSV types, then stop | Must |
| FR-3 | Add `Include` column to all 9 CSV types with default value "yes" | Must |
| FR-4 | When reading CSVs, filter out rows where `Include` is "no" (case-insensitive) | Must |
| FR-5 | Support `Include` values: "yes", "no", "true", "false", "1", "0" (normalized) | Should |
| FR-6 | Validate CSV schema on re-read: check required columns exist, types are correct | Must |
| FR-7 | Warn on removed rows vs initial generation (entity was in server but removed from CSV) | Should |
| FR-8 | Warn on added rows (entity in CSV that was not in the extraction) | Should |
| FR-9 | Validate that all referenced org keys exist in SonarQube Cloud before proceeding | Must |
| FR-10 | Print pre-migration summary: counts of included/excluded entities per CSV type | Must |
| FR-11 | Support filtering for quality gates: include/exclude specific gates by name | Must |
| FR-12 | Support filtering for quality profiles: include/exclude specific profiles by name+language | Must |
| FR-13 | Support filtering for groups: include/exclude specific groups by name | Must |
| FR-14 | Support filtering for global permissions: include/exclude specific permission grants | Must |
| FR-15 | Support filtering for permission templates: include/exclude specific templates | Must |
| FR-16 | Support filtering for portfolios: include/exclude specific portfolios | Must |
| FR-17 | Support filtering for user mappings: include/exclude specific user assignments | Must |
| FR-18 | Support filtering for projects: include/exclude specific projects (enhance existing) | Must |
| FR-19 | Support filtering for organizations: include/exclude entire organizations | Must |
| FR-20 | In dry-run mode, do not create any resources in SonarQube Cloud | Must |
| FR-21 | Generate a `dry-run-summary.json` report alongside CSVs | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Dry-run execution time | Same as extract + structure (no additional overhead) |
| NFR-2 | CSV reading performance | < 1 second for 10,000-row CSVs |
| NFR-3 | Validation performance | < 2 seconds for all 9 CSVs with 10,000 total rows |
| NFR-4 | Memory usage | Proportional to CSV size; stream large CSVs if > 100MB |
| NFR-5 | CSV format compatibility | Readable by Excel, Google Sheets, and standard Unix tools |

## Technical Design

### Architecture

The dry-run and filtering system touches several existing packages:

```
go/cmd/migrate.go              # Add --dry-run flag handling
go/internal/structure/
├── csv.go                     # Enhanced: Include column read/write for all CSV types
├── csv_validator.go           # NEW: Schema validation for edited CSVs
├── csv_filter.go              # NEW: Apply Include filters to loaded entities
└── csv_summary.go             # NEW: Generate pre-migration summary
go/internal/migrate/
├── migrate.go                 # Enhanced: skip migration in dry-run mode
└── planner.go                 # Enhanced: filter task inputs based on Include flags
```

### Key Algorithms

#### Include Column Filtering

```go
// IncludeFilter provides a generic mechanism to filter any CSV-backed entity
// by its Include column value.
type IncludeFilter struct {
    excludedCount map[string]int // CSV type -> count of excluded rows
    includedCount map[string]int // CSV type -> count of included rows
}

func (f *IncludeFilter) IsIncluded(includeValue string) bool {
    normalized := strings.TrimSpace(strings.ToLower(includeValue))
    switch normalized {
    case "no", "false", "0":
        return false
    case "yes", "true", "1", "":
        return true // empty defaults to included
    default:
        return true // unknown values default to included
    }
}

func FilterEntities[T any](entities []T, getInclude func(T) string, csvType string) ([]T, *FilterSummary) {
    var included []T
    summary := &FilterSummary{CSVType: csvType}
    for _, e := range entities {
        if isIncluded(getInclude(e)) {
            included = append(included, e)
            summary.IncludedCount++
        } else {
            summary.ExcludedCount++
        }
    }
    return included, summary
}
```

#### CSV Schema Validation

```go
type CSVSchema struct {
    RequiredColumns []string
    OptionalColumns []string
    ColumnTypes     map[string]ColumnType // "string", "bool", "int"
}

var schemas = map[string]CSVSchema{
    "organizations.csv": {
        RequiredColumns: []string{"sonarqube_org_key", "sonarcloud_org_key", "include"},
    },
    "projects.csv": {
        RequiredColumns: []string{"key", "name", "sonarqube_org_key", "include"},
        OptionalColumns: []string{"resolved_key", "branches"},
    },
    "groups.csv": {
        RequiredColumns: []string{"name", "sonarqube_org_key", "include"},
        OptionalColumns: []string{"member_count"},
    },
    "profiles.csv": {
        RequiredColumns: []string{"unique_key", "name", "language", "sonarqube_org_key", "include"},
    },
    "gates.csv": {
        RequiredColumns: []string{"name", "sonarqube_org_key", "include"},
    },
    "portfolios.csv": {
        RequiredColumns: []string{"key", "name", "include"},
    },
    "templates.csv": {
        RequiredColumns: []string{"unique_key", "name", "sonarqube_org_key", "include"},
    },
    "global-permissions.csv": {
        RequiredColumns: []string{"login", "permission", "org_key", "include"},
    },
    "users.csv": {
        RequiredColumns: []string{"server_login", "cloud_login", "include"},
    },
}

func ValidateCSV(filePath string, schemaName string) ([]ValidationIssue, error) {
    schema, ok := schemas[schemaName]
    if !ok {
        return nil, fmt.Errorf("unknown CSV schema: %s", schemaName)
    }

    reader, err := openCSV(filePath)
    if err != nil {
        return nil, err
    }

    headers := reader.Header()
    var issues []ValidationIssue

    // Check required columns
    for _, col := range schema.RequiredColumns {
        if !contains(headers, col) {
            issues = append(issues, ValidationIssue{
                Severity: "error",
                Message:  fmt.Sprintf("required column %q missing", col),
            })
        }
    }

    // Check for unknown columns (warning, not error)
    allKnown := append(schema.RequiredColumns, schema.OptionalColumns...)
    for _, h := range headers {
        if !contains(allKnown, h) {
            issues = append(issues, ValidationIssue{
                Severity: "warning",
                Message:  fmt.Sprintf("unknown column %q (will be ignored)", h),
            })
        }
    }

    return issues, nil
}
```

#### Row Diff Detection

```go
// DetectRowChanges compares generated CSV content against a user-edited CSV
// to identify added, removed, and modified rows.
func DetectRowChanges(generated, edited []map[string]string, keyColumn string) *RowDiff {
    diff := &RowDiff{}

    generatedKeys := map[string]bool{}
    for _, row := range generated {
        generatedKeys[row[keyColumn]] = true
    }

    editedKeys := map[string]bool{}
    for _, row := range edited {
        key := row[keyColumn]
        editedKeys[key] = true
        if !generatedKeys[key] {
            diff.Added = append(diff.Added, key)
        }
    }

    for _, row := range generated {
        key := row[keyColumn]
        if !editedKeys[key] {
            diff.Removed = append(diff.Removed, key)
        }
    }

    return diff
}
```

#### Pre-Migration Summary

```go
type MigrationPlan struct {
    Organizations FilterSummary
    Projects      FilterSummary
    Groups        FilterSummary
    Profiles      FilterSummary
    Gates         FilterSummary
    Portfolios    FilterSummary
    Templates     FilterSummary
    Permissions   FilterSummary
    UserMappings  FilterSummary
}

func (p *MigrationPlan) Print(w io.Writer) {
    table := tablewriter.NewWriter(w)
    table.SetHeader([]string{"Entity Type", "Included", "Excluded", "Total"})
    for _, s := range p.allSummaries() {
        table.Append([]string{
            s.CSVType,
            strconv.Itoa(s.IncludedCount),
            strconv.Itoa(s.ExcludedCount),
            strconv.Itoa(s.IncludedCount + s.ExcludedCount),
        })
    }
    table.Render()
}
```

### Data Flow

#### Dry-Run Workflow

1. **Operator runs**: `sonar-migration-tool migrate --dry-run --config migrate-config.json`
2. **Extract phase**: Tool extracts all server-wide data (quality gates, profiles, groups, permissions, etc.) from SQ Server
3. **Structure phase**: Tool generates organization and project mappings based on ALM bindings
4. **CSV generation**: Tool generates all 9 CSV types in `<export_directory>/`
5. **Dry-run summary**: Tool prints a table of entity counts per CSV type
6. **Stop**: Tool exits without creating any resources in SonarQube Cloud
7. **Operator reviews**: Opens CSVs in Excel or text editor, sets `Include=no` for entities to skip, adjusts mappings
8. **Operator re-runs**: `sonar-migration-tool migrate --config migrate-config.json` (no `--dry-run`)
9. **CSV read-back**: Tool reads edited CSVs, validates schema, applies Include filters
10. **Pre-migration summary**: Tool prints included/excluded counts, operator confirms
11. **Migration**: Tool migrates only included entities

#### Entity Filtering Data Flow (per CSV type)

```
Load CSV from disk
  → Parse headers, validate schema
  → Read all rows into typed structs
  → Apply IncludeFilter (exclude rows with Include=no)
  → Return filtered slice to migration task
```

#### Validation Data Flow

```
For each CSV file in export directory:
  → Validate schema (required columns present)
  → Detect row changes vs generated baseline (added/removed rows)
  → Validate org key references (all org keys exist in SC)
  → Validate foreign key references (project org keys exist in organizations.csv)
  → Collect ValidationIssue list
  → Print issues: errors (block migration), warnings (log and continue)
```

### Output Directory Structure

```
migration-output/
├── organizations.csv
├── projects.csv
├── groups.csv
├── profiles.csv
├── gates.csv
├── portfolios.csv
├── templates.csv
├── global-permissions.csv       # NEW (SPEC-018)
├── users.csv                    # NEW (SPEC-018)
├── .csv-baseline/               # Hidden: stores generated CSVs for diff detection
│   ├── organizations.csv
│   ├── projects.csv
│   └── ...
├── dry-run-summary.json         # Generated in dry-run mode
└── reports/                     # Report output subdirectory (see SPEC-022)
    ├── migration-report.md
    └── ...
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/organizations/search` | GET | Validate target org keys exist in SonarQube Cloud |
| (all extraction endpoints) | GET | Used during dry-run extraction phase (same as normal extract) |

## Acceptance Criteria

- [ ] AC-1: `--dry-run` flag on migrate command extracts data and generates CSVs without creating any SC resources
- [ ] AC-2: All 9 CSV types include an `Include` column with default value "yes"
- [ ] AC-3: Setting `Include=no` on a quality gate row excludes that gate from migration
- [ ] AC-4: Setting `Include=no` on a project row excludes that project from migration
- [ ] AC-5: Setting `Include=no` on a group row excludes that group from migration
- [ ] AC-6: Setting `Include=no` on a profile row excludes that profile from migration
- [ ] AC-7: Setting `Include=no` on a global permission row excludes that permission grant from migration
- [ ] AC-8: Setting `Include=no` on a user mapping row excludes that user mapping from migration
- [ ] AC-9: Setting `Include=no` on an organization row excludes all entities in that organization from migration
- [ ] AC-10: CSV schema validation catches missing required columns and reports errors
- [ ] AC-11: Row diff detection warns when rows were removed from the CSV vs the generated baseline
- [ ] AC-12: Pre-migration summary prints a table of included/excluded counts per entity type
- [ ] AC-13: Org key validation catches invalid org keys before migration starts
- [ ] AC-14: The dry-run summary JSON file is generated alongside CSVs
- [ ] AC-15: Include values are case-insensitive ("No", "NO", "no", "false", "0" all exclude)
- [ ] AC-16: Re-running without `--dry-run` reads the edited CSVs and migrates only included entities

## CloudVoyager Reference

| Area | Path |
|------|------|
| CSV Entity Filters | `src/shared/mapping/csv-entity-filters.js` |
| CSV Applier (per type) | `src/shared/mapping/csv-applier/` |
| CSV Generator | `src/shared/mapping/csv-generator/` |
| CSV Reader | `src/shared/mapping/csv-reader/` |
| CSV Tables (column defs) | `src/shared/mapping/csv-tables.js` |
| Migrate Command | `src/commands/migrate/` |

## Known Limitations

- The dry-run still makes read-only API calls to SQ Server for extraction; it is not a purely offline operation
- CSV diff detection requires storing a baseline copy of generated CSVs, which doubles disk usage for CSVs (negligible for typical sizes)
- Excluding an organization via `Include=no` in `organizations.csv` does not automatically set `Include=no` on all projects in that organization in `projects.csv`; the operator must exclude both (or the tool can add cascading behavior as a future enhancement)
- Very large CSVs (> 100,000 rows) may cause memory pressure during validation; streaming validation is a future optimization

## Open Questions

- Should excluding an organization cascade to all child entities (projects, groups, profiles assigned to that org)?
- Should there be a `--validate-only` flag that reads edited CSVs, runs all validations, and reports issues without starting migration?
- Should the pre-migration summary require explicit operator confirmation (interactive "proceed? y/n") or just print and continue?
- Should the tool preserve user-added comments in CSV files (lines starting with #) or strip them on re-read?
