---
spec_id: SPEC-018
title: Multi-Organization Mapping (Enhanced)
status: draft
priority: P1
epic: "Migration Workflow"
depends_on: []
estimated_effort: M
cloudvoyager_ref: "src/shared/mapping/"
---

# SPEC-018: Multi-Organization Mapping (Enhanced)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Multi-Organization Mapping enhancement extends the current tool's basic organization mapping with intelligent DevOps-binding-aware auto-mapping, project key conflict resolution, and two additional CSV types (`global-permissions.csv` and `users.csv`). This is a direct harvest of CloudVoyager's mapping subsystem, which was designed for enterprise customers migrating SonarQube Server installations spanning multiple teams, business units, and DevOps platforms.

The current tool already supports multi-org mapping via `organizations.csv` and `projects.csv`, where projects are assigned to target SonarQube Cloud organizations based on their ALM (Application Lifecycle Management) bindings. CloudVoyager extends this with automatic grouping of projects by their DevOps binding (GitHub org, GitLab group, Azure DevOps team, Bitbucket workspace), automatic resolution of project key conflicts (which are globally unique in SonarQube Cloud), and a richer set of CSV mapping files that cover global permissions and user identity mapping.

The current tool supports 7 CSV types. This spec brings the total to 9 by adding `global-permissions.csv` (for org-wide permission grants) and `users.csv` (for SQ Server login to SC login mapping). It also enhances existing CSVs with an `Include` column for entity-level filtering (detailed in SPEC-019).

## Problem Statement

Enterprise SonarQube Server installations often have hundreds of projects bound to different DevOps platforms and organizations. The current tool requires manual organization assignment for every project, which is tedious and error-prone for large installations. Additionally, project keys in SonarQube Cloud are globally unique (not scoped to an organization), so key collisions can occur when projects from different servers are migrated to the same SonarQube Cloud instance. Without automatic conflict resolution, operators must manually rename colliding projects, which risks breaking downstream integrations.

Furthermore, the current tool does not map global permissions (admin, profile admin, gate admin, quality profile admin) or user identities (SQ Server login to SC login), leaving operators to manually recreate these assignments post-migration.

## User Stories

- **As a** migration operator, **I want to** auto-map projects to target organizations based on their DevOps bindings, **so that** I do not have to manually assign hundreds of projects.
- **As a** migration operator, **I want to** resolve project key conflicts automatically, **so that** migrations do not fail due to globally-unique key collisions in SonarQube Cloud.
- **As a** migration operator, **I want to** map global permissions via CSV, **so that** org-wide admin assignments are preserved during migration.
- **As a** migration operator, **I want to** map user identities between SQ Server and SC, **so that** issue assignments and permission grants transfer correctly.
- **As a** migration operator, **I want to** review and edit all mapping CSVs before migration, **so that** I have full control over what gets migrated and how.
- **As a** migration operator, **I want to** see project counts per organization in the CSV, **so that** I can verify the distribution looks correct before proceeding.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Extract ALM bindings per project via `/api/alm_settings/list_definitions` and `/api/alm_settings/get_binding` | Must |
| FR-2 | Group projects by their DevOps binding (GitHub org, GitLab group, Azure DevOps team, Bitbucket workspace) | Must |
| FR-3 | Auto-match each binding group to a target SonarQube Cloud organization | Must |
| FR-4 | Assign unbound projects to a configurable default organization | Must |
| FR-5 | Implement project key conflict resolution strategy: original key, then `{org}_{key}`, then reuse if owned | Must |
| FR-6 | Check project key availability via `/api/components/show` before migration | Must |
| FR-7 | Generate `global-permissions.csv` with columns: Login, Permission, OrgKey, Include | Must |
| FR-8 | Generate `users.csv` with columns: ServerLogin, ServerName, ServerEmail, CloudLogin, Include | Must |
| FR-9 | Add `Include` column (default: yes) to all 9 CSV types. Note: The `Include` column is defined here (SPEC-018) as part of CSV generation. SPEC-019 defines the filtering logic that reads and acts on this column. | Must |
| FR-10 | Add `Branches` column to `projects.csv` for per-project branch control | Should |
| FR-11 | Add membership count column to `groups.csv` | Should |
| FR-12 | Add inheritance info columns to `profiles.csv` | Should |
| FR-13 | Add condition detail columns to `gates.csv` | Should |
| FR-14 | Add project association columns to `portfolios.csv` | Should |
| FR-15 | Log all project key conflicts and resolutions in migration report | Must |
| FR-16 | Validate that target org keys exist in SonarQube Cloud before migration | Must |
| FR-17 | Support reading edited CSVs with modified `Include` values and custom mappings | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Auto-mapping speed | < 5 seconds for 1000 projects |
| NFR-2 | Key conflict check concurrency | Configurable, default 10 concurrent API checks |
| NFR-3 | CSV generation time | < 2 seconds for all 9 CSV types |
| NFR-4 | CSV file encoding | UTF-8 with BOM for Excel compatibility |
| NFR-5 | CSV max rows | Handle 10,000+ rows per file |
| NFR-6 | Memory usage | Proportional to project count; no full-corpus loading |

## Technical Design

### Architecture

The enhanced mapping system extends the existing `go/internal/structure/` package and adds new CSV types:

```
go/internal/structure/
├── structure.go          # Enhanced: auto-mapping via ALM bindings
├── csv.go                # Enhanced: read/write for all 9 CSV types
├── types.go              # Enhanced: new types for permissions, user mappings
├── alm_bindings.go       # NEW: Extract and group ALM bindings
├── key_resolver.go       # NEW: Project key conflict resolution
├── global_permissions.go # NEW: Global permission CSV generation
├── user_mappings.go      # NEW: User identity mapping CSV generation
└── validators.go         # NEW: CSV schema and org-key validation
```

### Key Algorithms

#### DevOps Binding Auto-Mapping

```go
type ALMBinding struct {
    ProjectKey   string
    ALMType      string // "github", "gitlab", "azure", "bitbucket"
    ALMSetting   string // Name of the ALM setting in SQ
    Repository   string // e.g., "my-org/my-repo"
    OrgOrGroup   string // Extracted: "my-org" from "my-org/my-repo"
}

func AutoMapProjectsToOrgs(bindings []ALMBinding, targetOrgs []Organization) map[string]string {
    // Step 1: Group projects by DevOps org/group
    groups := map[string][]string{} // orgOrGroup -> []projectKey
    unbound := []string{}
    for _, b := range bindings {
        if b.OrgOrGroup == "" {
            unbound = append(unbound, b.ProjectKey)
            continue
        }
        compositeKey := b.ALMType + ":" + b.OrgOrGroup
        groups[compositeKey] = append(groups[compositeKey], b.ProjectKey)
    }

    // Step 2: Match each group to a target org by ALM binding match
    projectToOrg := map[string]string{}
    for compositeKey, projectKeys := range groups {
        targetOrg := matchBindingToOrg(compositeKey, targetOrgs)
        for _, pk := range projectKeys {
            projectToOrg[pk] = targetOrg.SonarCloudOrgKey
        }
    }

    // Step 3: Assign unbound projects to default org
    if len(targetOrgs) == 0 {
        return nil // No target organizations configured; caller must validate this
    }
    defaultOrg := targetOrgs[0].SonarCloudOrgKey
    for _, pk := range unbound {
        projectToOrg[pk] = defaultOrg
    }

    return projectToOrg
}
```

#### Project Key Conflict Resolution

```go
type KeyResolution struct {
    OriginalKey  string
    ResolvedKey  string
    Strategy     string // "original", "prefixed", "reused"
    ConflictWith string // existing project that owns the key, if any
}

func ResolveProjectKey(
    projectKey string,
    targetOrgKey string,
    cloudClient *cloud.Client,
) (*KeyResolution, error) {
    // Strategy 1: Use original key
    owner, err := checkKeyOwnership(cloudClient, projectKey)
    if err != nil {
        return nil, err
    }
    if owner == "" {
        // Key is available globally
        return &KeyResolution{
            OriginalKey: projectKey,
            ResolvedKey: projectKey,
            Strategy:    "original",
        }, nil
    }
    if owner == targetOrgKey {
        // Key exists but is owned by target org (previous migration run)
        return &KeyResolution{
            OriginalKey: projectKey,
            ResolvedKey: projectKey,
            Strategy:    "reused",
        }, nil
    }

    // Strategy 2: Prefix with org key
    prefixedKey := targetOrgKey + "_" + projectKey
    owner2, err := checkKeyOwnership(cloudClient, prefixedKey)
    if err != nil {
        return nil, err
    }
    if owner2 == "" || owner2 == targetOrgKey {
        return &KeyResolution{
            OriginalKey:  projectKey,
            ResolvedKey:  prefixedKey,
            Strategy:     "prefixed",
            ConflictWith: owner,
        }, nil
    }

    // Strategy 3: Fail — both keys taken by other orgs
    return nil, fmt.Errorf(
        "project key %q and prefixed key %q are both taken by other organizations",
        projectKey, prefixedKey,
    )
}
```

#### CSV Schema for New Types

```go
// GlobalPermission represents a row in global-permissions.csv
type GlobalPermission struct {
    Login      string `csv:"login"`
    Permission string `csv:"permission"` // admin, profileadmin, gateadmin, scan, provisioning
    OrgKey     string `csv:"org_key"`
    Include    string `csv:"include"` // "yes" or "no"
}

// UserMapping represents a row in users.csv
type UserMapping struct {
    ServerLogin string `csv:"server_login"`
    ServerName  string `csv:"server_name"`
    ServerEmail string `csv:"server_email"`
    CloudLogin  string `csv:"cloud_login"`
    Include     string `csv:"include"` // "yes" or "no"
}
```

### Data Flow

#### Auto-Mapping Flow

1. **Extract ALM settings**: Call `/api/alm_settings/list_definitions` to get all configured ALM integrations
2. **Extract project bindings**: For each project, call the appropriate binding endpoint:
   - GitHub: `/api/alm_settings/get_binding?project={key}` (response includes `repository`)
   - GitLab: `/api/alm_settings/get_binding?project={key}` (response includes `repository`)
   - Azure DevOps: `/api/alm_settings/get_binding?project={key}` (response includes `repository`, `slug`)
   - Bitbucket: `/api/alm_settings/get_binding?project={key}` (response includes `repository`, `slug`)
3. **Parse org/group**: Extract the org/group portion from the repository identifier (e.g., `my-org` from `my-org/my-repo`)
4. **Group and match**: Group projects by parsed org/group, match each group to a target SC organization
5. **Generate organizations.csv**: Write org assignments with project counts
6. **Generate projects.csv**: Write project assignments with resolved keys and branch control
7. **User review**: Operator reviews and edits CSVs
8. **Read back**: On next run, read edited CSVs and apply customizations

#### Project Key Resolution Flow

1. **Collect all project keys** from the projects.csv
2. **Batch check** key availability via concurrent `/api/components/show` calls
3. **Resolve conflicts** using the three-strategy approach (original, prefixed, reused)
4. **Update projects.csv** with resolved keys in a `ResolvedKey` column
5. **Log conflicts** in the migration report with original key, resolved key, and strategy used

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/alm_settings/list_definitions` | GET | List all ALM integration configurations |
| `/api/alm_settings/get_binding` | GET | Get project's ALM binding (GitHub/GitLab/Azure/BB) |
| `/api/components/show` | GET | Check if a project key exists in SonarQube Cloud |
| `/api/permissions/users` | GET | Extract global permission grants per user |
| `/api/users/search` | GET | List users for user-mapping CSV generation |
| `/api/user_groups/search` | GET | List groups with membership counts |
| `/api/qualityprofiles/search` | GET | Get profiles with inheritance info |
| `/api/qualitygates/list` | GET | Get gates with condition details |

### CSV File Specifications

#### global-permissions.csv

| Column | Type | Description |
|--------|------|-------------|
| login | string | SQ Server user login |
| permission | string | Permission key (admin, profileadmin, gateadmin, scan, provisioning) |
| org_key | string | Target SC organization key |
| include | string | "yes" or "no" (default: "yes") |

#### users.csv

| Column | Type | Description |
|--------|------|-------------|
| server_login | string | SQ Server user login |
| server_name | string | Display name on SQ Server |
| server_email | string | Email on SQ Server |
| cloud_login | string | SC user login (empty for manual mapping) |
| include | string | "yes" or "no" (default: "yes") |

#### Enhanced projects.csv (new columns)

| Column | Type | Description |
|--------|------|-------------|
| ... | ... | (existing columns) |
| resolved_key | string | Resolved key after conflict check (new) |
| key_strategy | string | Resolution strategy used (new) |
| branches | string | Comma-separated branch names to migrate, or "*" for all (new) |
| include | string | "yes" or "no" (new) |

## Acceptance Criteria

- [ ] AC-1: Running structure generation auto-groups projects by DevOps binding and matches to target orgs
- [ ] AC-2: Projects without ALM bindings are assigned to the configured default organization
- [ ] AC-3: Project key conflicts are detected and resolved automatically with logging
- [ ] AC-4: `global-permissions.csv` is generated with all org-wide permission grants
- [ ] AC-5: `users.csv` is generated with all SQ Server users and empty cloud_login for manual mapping
- [ ] AC-6: All 9 CSV types have an `Include` column defaulting to "yes"
- [ ] AC-7: Editing `Include` to "no" in any CSV excludes that entity from migration
- [ ] AC-8: Target org keys are validated against SonarQube Cloud before migration proceeds
- [ ] AC-9: Project key conflict resolutions appear in the migration report
- [ ] AC-10: `projects.csv` includes a `Branches` column for per-project branch control
- [ ] AC-11: `groups.csv` shows membership count per group
- [ ] AC-12: Auto-mapping completes in under 5 seconds for 1000 projects
- [ ] AC-13: Manually edited CSV values (org assignments, key overrides, include flags) are preserved on re-read

## CloudVoyager Reference

| Area | Path |
|------|------|
| Organization Mapper | `src/shared/mapping/org-mapper/` |
| CSV Generator | `src/shared/mapping/csv-generator/` |
| CSV Reader | `src/shared/mapping/csv-reader/` |
| CSV Applier | `src/shared/mapping/csv-applier/` |
| CSV Entity Filters | `src/shared/mapping/csv-entity-filters.js` |
| CSV Tables | `src/shared/mapping/csv-tables.js` |

## Known Limitations

- User mapping requires manual cloud_login assignment; there is no API to auto-match SQ Server users to SC users by email because SC user APIs do not expose email addresses to non-admin callers
- Project key conflict resolution adds latency proportional to the number of projects (one API call per project); mitigated by concurrent checks
- ALM binding extraction requires `admin` permission on SQ Server; projects where the token lacks admin access will fall back to unbound behavior
- The `{org}_{key}` prefix strategy may produce keys that exceed SonarQube Cloud's 400-character key length limit for very long org+project key combinations

## Open Questions

- Should the tool auto-detect SC user logins by email matching where possible, or always leave `cloud_login` empty for manual mapping?
- Should there be a `--skip-key-check` flag to bypass the per-project key availability check for faster generation when the operator knows there are no conflicts?
- ~~Should the CSV encoding use UTF-8 with BOM (for Excel compatibility) or plain UTF-8 (for Unix tool compatibility)?~~ **Resolved: NFR-4 specifies UTF-8 with BOM for Excel compatibility.**
- How should the tool handle projects bound to ALM integrations that do not exist in the target SC organization (e.g., a GitHub org that SC does not have a binding for)?
