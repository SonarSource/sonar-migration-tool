---
spec_id: SPEC-010
title: "User Mapping & Assignment"
status: draft
priority: P1
epic: "Scale & Reliability"
depends_on: [SPEC-008]
estimated_effort: M
cloudvoyager_ref: "src/shared/mapping/user-mappings/, src/shared/mapping/csv-tables/helpers/generate-user-mappings-csv.js, src/shared/mapping/csv-applier/helpers/apply-user-mappings-csv.js"
---

# SPEC-010: User Mapping & Assignment
<!-- updated: 2026-05-26_01:00:00 -->

## Overview
<!-- updated: 2026-05-26_01:00:00 -->

SonarQube Server and SonarQube Cloud maintain independent user registries. Users who exist in SonarQube Server may have different login identifiers in SonarQube Cloud (due to SSO, different authentication providers, or organizational naming conventions). The user mapping system bridges this gap by providing a CSV-based mapping file (`users.csv`) that translates SonarQube Server logins to SonarQube Cloud logins.

This mapping is consumed by the issue metadata sync (SPEC-008) when assigning migrated issues to their original owners, and by comment attribution when preserving the original author's identity in migrated comments. The CSV file is auto-generated during the extraction phase with SonarQube Server user data pre-populated, and the migration operator manually fills in the corresponding SonarQube Cloud logins before running the migration phase.

The user mapping system follows the same CSV-based mapping pattern already established in the tool for organizations (`organizations.csv`), projects (`projects.csv`), quality gates (`gates.csv`), and quality profiles (`profiles.csv`). It adds a new mapping file to the existing `mappings` command.

## Problem Statement
<!-- updated: 2026-05-26_01:00:00 -->

When migrating issue assignments from SonarQube Server to SonarQube Cloud, the tool must resolve user identity differences between the two platforms. A developer who logs in as `jsmith` on SonarQube Server might be `john.smith@company.com` on SonarQube Cloud (due to SSO integration with Google Workspace, Okta, or similar). Without a mapping mechanism:

- Issue assignments would fail with "user not found" errors.
- The tool would have no way to determine which SC user corresponds to which SQ Server user.
- Comment attribution would be lost.
- The migration operator would have no visibility into which users need mapping.

Additionally, some SonarQube Server users may not have accounts in SonarQube Cloud (contractors, former employees, service accounts). The mapping system must handle these cases gracefully by allowing operators to explicitly exclude users or leave them unmapped.

## User Stories
<!-- updated: 2026-05-26_01:00:00 -->

- **As a** migration operator, **I want** a pre-populated CSV file listing all SonarQube Server users who have issue assignments, **so that** I can see who needs mapping without manually querying the server.
- **As a** migration operator, **I want to** manually fill in the SonarQube Cloud login for each user, **so that** issue assignments are correctly transferred.
- **As a** migration operator, **I want to** exclude specific users from assignment sync (e.g., former employees), **so that** the tool doesn't attempt assignments that will fail.
- **As a** migration operator, **I want to** see a report of mapping statistics (mapped, unmapped, excluded), **so that** I can verify the mapping file is complete before running migration.
- **As a** migration operator, **I want** the tool to warn me about unmapped users but not fail, **so that** migration proceeds for all mappable users even if some are unmapped.

## Requirements
<!-- updated: 2026-05-26_01:00:00 -->

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Generate `users.csv` during the `mappings` command (Phase 3) | Must |
| FR-2 | CSV schema: `Include, SonarQube Login, SonarCloud Login, Display Name, Email, Issue Count` | Must |
| FR-3 | Auto-populate SQ Server columns from extracted user data and issue assignee data | Must |
| FR-4 | Sort users by issue count descending (highest-impact users first) | Must |
| FR-5 | Default `Include` to `yes` for all users | Must |
| FR-6 | `Include=no` excludes user from assignment sync (issues left unassigned) | Must |
| FR-7 | Warn on unmapped users (SonarCloud Login column empty, Include=yes) | Must |
| FR-8 | Warn on invalid SC logins (SC API will reject the assignment) | Should |
| FR-9 | Report mapping statistics: total users, mapped, unmapped, excluded, issue count per category | Must |
| FR-10 | Support multiple SQ Server logins mapping to the same SC login (allowed) | Must |
| FR-11 | Consume mapping in SPEC-008 issue assignment sync | Must |
| FR-12 | Consume mapping in SPEC-008 comment attribution (include original SQ author name) | Must |
| FR-13 | Validate SC logins against SC user list when `--validate-users` flag is set | Should |
| FR-14 | Re-generate CSV on subsequent `mappings` runs without overwriting manual edits (merge strategy) | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | CSV generation time for 10K users | < 5 seconds |
| NFR-2 | CSV file size for 10K users | < 1 MB |
| NFR-3 | Mapping lookup during sync: O(1) per issue | Hash map |
| NFR-4 | UTF-8 support for display names | Full Unicode |

## Technical Design
<!-- updated: 2026-05-26_01:00:00 -->

### Architecture

The user mapping system has two components:

1. **Generation** (`go/internal/structure/user_mappings.go`): Generates the CSV file during the `mappings` command, alongside existing CSV generation for gates, profiles, groups, etc.

2. **Application** (`go/internal/migrate/user_mappings.go`): Loads and applies the CSV mapping during issue metadata sync.

```
go/internal/structure/
    user_mappings.go       # NEW: generateUserMappingsCsv()
    user_mappings_test.go  # NEW: Unit tests

go/internal/migrate/
    user_mappings.go       # NEW: loadUserMappings(), lookupSCUser()
    user_mappings_test.go  # NEW: Unit tests
```

### Key Algorithms

#### CSV Generation

```go
const userMappingsCSVFile = "users.csv"

var userMappingsHeader = []string{
    "Include",
    "SonarQube Login",
    "SonarCloud Login",
    "Display Name",
    "Email",
    "Issue Count",
}

// UserMappingRow represents a single row in the users CSV.
type UserMappingRow struct {
    Include       string // "yes" or "no"
    SQLogin       string
    SCLogin       string // empty until user fills in
    DisplayName   string
    Email         string
    IssueCount    int
}

// GenerateUserMappingsCsv creates the users.csv file.
// It reads extracted user data and issue assignee data to produce a
// pre-populated mapping file sorted by issue count (highest first).
func GenerateUserMappingsCsv(store *common.DataStore, outputDir string) error {
    // 1. Load extracted users
    users, err := loadExtractedUsers(store)
    if err != nil {
        return fmt.Errorf("loading users: %w", err)
    }

    // 2. Load extracted issues to count assignments
    assigneeCounts, err := countAssignees(store)
    if err != nil {
        return fmt.Errorf("counting assignees: %w", err)
    }

    // 3. Build rows: one per unique assignee (not all users)
    rows := buildUserMappingRows(users, assigneeCounts)

    // 4. Sort by issue count descending
    sort.Slice(rows, func(i, j int) bool {
        return rows[i].IssueCount > rows[j].IssueCount
    })

    // 5. Write CSV
    return writeUserMappingsCsv(filepath.Join(outputDir, userMappingsCSVFile), rows)
}

func buildUserMappingRows(users map[string]types.User, assigneeCounts map[string]int) []UserMappingRow {
    var rows []UserMappingRow

    // Include all users who have at least one issue assigned
    for login, count := range assigneeCounts {
        user, found := users[login]
        row := UserMappingRow{
            Include:    "yes",
            SQLogin:    login,
            IssueCount: count,
        }
        if found {
            row.DisplayName = user.Name
            row.Email = user.Email
        }
        rows = append(rows, row)
    }

    return rows
}

func countAssignees(store *common.DataStore) (map[string]int, error) {
    counts := make(map[string]int)

    // Read from multiple issue-related JSONL task outputs
    for _, taskName := range []string{"issues", "accepted-issues"} {
        rawItems, err := store.ReadAll(taskName)
        if err != nil {
            continue // task may not exist
        }
        for _, raw := range rawItems {
            var issue struct {
                Assignee string `json:"assignee"`
            }
            if json.Unmarshal(raw, &issue) == nil && issue.Assignee != "" {
                counts[issue.Assignee]++
            }
        }
    }

    return counts, nil
}
```

#### CSV Application (Mapping Loader)

```go
// UserMapping holds the resolved mapping for a single SQ Server user.
type UserMapping struct {
    SCLogin string // empty if unmapped
    Include bool   // false if explicitly excluded
    SQName  string // display name for comment attribution
}

// UserMappings holds all loaded mappings for O(1) lookup.
type UserMappings struct {
    byLogin map[string]*UserMapping
    stats   UserMappingStats
}

type UserMappingStats struct {
    Total    int
    Mapped   int
    Unmapped int
    Excluded int
}

// LoadUserMappings reads and parses the users.csv file.
func LoadUserMappings(csvPath string) (*UserMappings, error) {
    f, err := os.Open(csvPath)
    if err != nil {
        if os.IsNotExist(err) {
            slog.Warn("No users.csv found; all assignments will be skipped",
                "path", csvPath)
            return &UserMappings{byLogin: make(map[string]*UserMapping)}, nil
        }
        return nil, fmt.Errorf("opening user mappings: %w", err)
    }
    defer f.Close()

    reader := csv.NewReader(f)
    records, err := reader.ReadAll()
    if err != nil {
        return nil, fmt.Errorf("parsing CSV: %w", err)
    }

    um := &UserMappings{byLogin: make(map[string]*UserMapping)}

    for i, record := range records {
        if i == 0 {
            continue // skip header
        }
        if len(record) < 4 {
            continue // malformed row
        }

        include := strings.TrimSpace(strings.ToLower(record[0]))
        sqLogin := strings.TrimSpace(record[1])
        scLogin := strings.TrimSpace(record[2])
        displayName := strings.TrimSpace(record[3])

        if sqLogin == "" {
            continue
        }

        mapping := &UserMapping{
            SCLogin: scLogin,
            Include: include != "no",
            SQName:  displayName,
        }

        um.byLogin[sqLogin] = mapping
        um.stats.Total++

        switch {
        case !mapping.Include:
            um.stats.Excluded++
        case scLogin != "":
            um.stats.Mapped++
        default:
            um.stats.Unmapped++
        }
    }

    // Log statistics
    slog.Info("User mappings loaded",
        "total", um.stats.Total,
        "mapped", um.stats.Mapped,
        "unmapped", um.stats.Unmapped,
        "excluded", um.stats.Excluded)

    if um.stats.Unmapped > 0 {
        slog.Warn("Unmapped users will have issues left unassigned",
            "count", um.stats.Unmapped)
    }

    return um, nil
}

// Lookup returns the mapping for a SQ Server login.
// Returns nil if the login is not in the mapping file.
func (um *UserMappings) Lookup(sqLogin string) *UserMapping {
    return um.byLogin[sqLogin]
}

// SCLoginFor returns the SC login for a SQ Server login, or empty string
// if unmapped, excluded, or not found.
func (um *UserMappings) SCLoginFor(sqLogin string) string {
    m := um.byLogin[sqLogin]
    if m == nil || !m.Include || m.SCLogin == "" {
        return ""
    }
    return m.SCLogin
}

// DisplayNameFor returns the SQ Server display name for comment attribution.
func (um *UserMappings) DisplayNameFor(sqLogin string) string {
    m := um.byLogin[sqLogin]
    if m == nil {
        return sqLogin // fallback to login
    }
    if m.SQName != "" {
        return m.SQName
    }
    return sqLogin
}

// Stats returns the mapping statistics.
func (um *UserMappings) Stats() UserMappingStats {
    return um.stats
}
```

#### Integration with Issue Sync (SPEC-008)

```go
// In issuesync/assignments.go

func syncAssignment(ctx context.Context, client *cloud.Client, scIssueKey string,
    sqAssignee string, mappings *UserMappings) error {

    if sqAssignee == "" {
        return nil // no assignment to sync
    }

    scLogin := mappings.SCLoginFor(sqAssignee)
    if scLogin == "" {
        slog.Debug("No SC mapping for assignee, leaving unassigned",
            "sqAssignee", sqAssignee, "issueKey", scIssueKey)
        return nil // not an error, just unmapped
    }

    return client.Issues().Assign(ctx, scIssueKey, scLogin)
}

// In issuesync/comments.go

func formatMigratedComment(originalText, sqAuthorLogin string, mappings *UserMappings) string {
    authorName := mappings.DisplayNameFor(sqAuthorLogin)
    return fmt.Sprintf("[Migrated from SonarQube Server - @%s]\n\n%s", authorName, originalText)
}
```

#### CSV Merge Strategy (Re-generation)

```go
// MergeUserMappingsCsv merges newly generated rows with existing CSV data,
// preserving manual edits (SCLogin, Include) for existing rows and adding
// new users discovered in subsequent extractions.
func MergeUserMappingsCsv(existingPath string, newRows []UserMappingRow) ([]UserMappingRow, error) {
    existing, err := readExistingMappings(existingPath)
    if err != nil {
        if os.IsNotExist(err) {
            return newRows, nil // no existing file, use new rows as-is
        }
        return nil, err
    }

    // Build lookup of existing rows by SQ login
    existingByLogin := make(map[string]UserMappingRow, len(existing))
    for _, row := range existing {
        existingByLogin[row.SQLogin] = row
    }

    // Merge: preserve existing edits, update issue counts, add new users
    merged := make([]UserMappingRow, 0, len(newRows)+len(existing))
    seen := make(map[string]bool, len(newRows))

    for _, newRow := range newRows {
        seen[newRow.SQLogin] = true
        if existingRow, found := existingByLogin[newRow.SQLogin]; found {
            // Preserve manual edits (SCLogin, Include), update metadata
            merged = append(merged, UserMappingRow{
                Include:     existingRow.Include,     // preserve
                SQLogin:     newRow.SQLogin,
                SCLogin:     existingRow.SCLogin,     // preserve
                DisplayName: newRow.DisplayName,       // update from server
                Email:       newRow.Email,             // update from server
                IssueCount:  newRow.IssueCount,        // update count
            })
        } else {
            // New user not in existing CSV
            merged = append(merged, newRow)
        }
    }

    // When regenerating the CSV, users present in the existing file but absent
    // from the new extraction should be PRESERVED (not dropped), since their
    // mapping may still be needed for previously extracted issues.
    for _, existingRow := range existing {
        if !seen[existingRow.SQLogin] {
            merged = append(merged, existingRow)
        }
    }

    return merged, nil
}
```

### Data Flow

```
GENERATION (mappings command, Phase 3):
1. Read extracted users from JSONL (files/<extract_id>/users/)
2. Read extracted issues from JSONL (files/<extract_id>/issues/, accepted-issues/)
3. Count assignments per SQ login
4. Build UserMappingRow for each assignee
5. Sort by issue count descending
6. If users.csv exists, merge (preserve manual edits)
7. Write users.csv to output directory

APPLICATION (migrate command, Phase 4):
1. Load users.csv into UserMappings map
2. Log statistics (total/mapped/unmapped/excluded)
3. Issue sync (SPEC-008) calls mappings.SCLoginFor(sqAssignee)
4. Comment sync (SPEC-008) calls mappings.DisplayNameFor(sqAuthor)
5. Assignment sync calls cloud.Issues().Assign() with resolved SC login
```

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/users/search` | GET | Fetch SQ Server users during extraction (already implemented) |
| `/api/issues/search` | GET | Count issue assignments per user during CSV generation |
| `/api/issues/assign` | POST | Assign SC issue to mapped SC user |
| `/api/users/search` | GET | (Optional) Validate SC logins against SC user list |

### CSV File Example

```csv
Include,SonarQube Login,SonarCloud Login,Display Name,Email,Issue Count
yes,jsmith,john.smith@company.com,John Smith,jsmith@company.com,1247
yes,agarcia,,Ana Garcia,agarcia@company.com,832
no,buildbot,,CI Bot,ci@company.com,456
yes,mwilson,margaret.wilson,Margaret Wilson,mwilson@company.com,231
yes,rjones,,Robert Jones,rjones@company.com,89
```

In this example:
- `jsmith` is mapped to `john.smith@company.com` in SC
- `agarcia` is unmapped (SC login empty); their issues will be left unassigned with a warning
- `buildbot` is excluded (`Include=no`); their issues will be left unassigned silently
- `mwilson` is mapped to `margaret.wilson` in SC
- `rjones` is unmapped; their issues will be left unassigned with a warning

### Validation (Optional, `--validate-users`)

```go
func ValidateUserMappings(ctx context.Context, scClient *cloud.Client, mappings *UserMappings) []string {
    var warnings []string

    for sqLogin, m := range mappings.byLogin {
        if !m.Include || m.SCLogin == "" {
            continue
        }

        // Check if SC login exists
        exists, err := scClient.Users().Exists(ctx, m.SCLogin)
        if err != nil {
            warnings = append(warnings, fmt.Sprintf(
                "Failed to validate SC login %q (mapped from %q): %v",
                m.SCLogin, sqLogin, err))
            continue
        }
        if !exists {
            warnings = append(warnings, fmt.Sprintf(
                "SC login %q (mapped from %q) does not exist in SonarCloud",
                m.SCLogin, sqLogin))
        }
    }

    return warnings
}
```

## Acceptance Criteria
<!-- updated: 2026-05-26_01:00:00 -->

- [ ] AC-1: `mappings` command generates `users.csv` with correct schema and pre-populated SQ Server data.
- [ ] AC-2: Users are sorted by issue count descending in the generated CSV.
- [ ] AC-3: CSV includes only users who have at least one issue assignment (no bloating with inactive users).
- [ ] AC-4: `Include` column defaults to `yes` for all users.
- [ ] AC-5: Setting `Include=no` excludes the user from assignment sync (issues left unassigned, no warning).
- [ ] AC-6: Empty `SonarCloud Login` with `Include=yes` generates a warning during migration.
- [ ] AC-7: Filled `SonarCloud Login` with `Include=yes` results in correct issue assignment via SC API.
- [ ] AC-8: Comment attribution uses `DisplayNameFor()` to include the original author's name.
- [ ] AC-9: Multiple SQ logins can map to the same SC login without error.
- [ ] AC-10: Re-running `mappings` command merges new users without overwriting manual SC login edits.
- [ ] AC-11: Mapping statistics are logged: total, mapped, unmapped, excluded.
- [ ] AC-12: Missing `users.csv` file results in a warning (not an error) and all assignments are skipped.
- [ ] AC-13: Unit tests cover: CSV generation, CSV parsing, merge strategy, lookup, comment formatting.
- [ ] AC-14: `--validate-users` flag validates SC logins against the SC user API and reports invalid logins.

## CloudVoyager Reference
<!-- updated: 2026-05-26_01:00:00 -->

| Area | Path |
|------|------|
| CSV generation | `src/shared/mapping/csv-tables/helpers/generate-user-mappings-csv.js` |
| CSV application | `src/shared/mapping/csv-applier/helpers/apply-user-mappings-csv.js` |
| CSV reader utility | `src/shared/mapping/csv-reader.js` |
| Migration command (CSV consumption) | `src/commands/migrate/` |
| Issue sync assignment | `src/shared/utils/issue-sync/` (implicit via assignee mapping) |

### Key Differences from CloudVoyager

1. **CSV schema**: CloudVoyager uses `Include, SonarQube Login, SonarCloud Login, Display Name, Email, Issue Count`. The Go implementation uses the same schema for compatibility.
2. **Merge strategy**: CloudVoyager does not implement CSV merge (re-generation overwrites). The Go implementation adds merge to preserve manual edits, improving the operator experience.
3. **Validation**: CloudVoyager does not validate SC logins before migration. The Go implementation adds optional `--validate-users` flag.
4. **Issue count source**: CloudVoyager counts from extracted issues. Go version reads from JSONL DataStore (same data, different access pattern).
5. **Integration pattern**: CloudVoyager uses a Map returned from `applyUserMappingsCsv()`. Go uses a `UserMappings` struct with typed methods.

## Known Limitations
<!-- updated: 2026-05-26_01:00:00 -->

- **Manual process**: The operator must manually fill in `SonarCloud Login` values. There is no automatic user matching because SQ Server and SC use fundamentally different identity providers.
- **No bidirectional sync**: If a user is renamed in SC after the mapping file is created, the mapping becomes stale. The tool does not detect or update stale mappings.
- **Group-based mapping not supported**: Users must be mapped individually. There is no mechanism to map all members of an SQ group to an SC group.
- **Service accounts**: SQ Server service accounts (used for CI/CD) typically don't have SC equivalents. These should be excluded via `Include=no`.
- **Case sensitivity**: SQ Server logins may be case-insensitive while SC logins are case-sensitive. The mapping lookup is case-sensitive; operators must ensure correct casing.

## Open Questions
<!-- updated: 2026-05-26_01:00:00 -->

- **Q1**: Should we attempt fuzzy matching of SQ Server users to SC users (by email or display name) to pre-populate the SC login column?
- **Q2**: Should the CSV include users who only appear in comments (not just assignees)? This would enable more accurate comment attribution.
- **Q3**: Should we support a bulk mapping rule (e.g., "all SQ logins are the same as SC logins" or "append @domain.com to all SQ logins")?
- **Q4**: ~~Should the merge strategy handle users removed from SQ Server (present in existing CSV but not in new extraction)? Currently they would be dropped.~~ **Resolved**: The merge strategy now preserves users present in the existing file but absent from the new extraction.
- **Q5**: Should we provide a web UI or interactive CLI prompt for the mapping step instead of requiring manual CSV editing?

## References
<!-- updated: 2026-05-26_01:00:00 -->

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt
