# API Endpoints Used by sonar-migration-tool

All SonarQube Server and SonarQube Cloud API endpoints used by this tool.

## Deprecation Status (queried from sc-staging.io on 2026-05-19)

### Deprecated Endpoints We Use

| Endpoint | Deprecated Since | Notes |
|----------|-----------------|-------|
| `api/qualitygates/create` | 16 September, 2025 | |
| `api/qualitygates/create_condition` | 16 September, 2025 | |
| `api/qualitygates/destroy` | 16 September, 2025 | |
| `api/qualitygates/list` | 16 September, 2025 | |
| `api/qualitygates/select` | 16 September, 2025 | |
| `api/qualitygates/set_as_default` | 16 September, 2025 | |
| `api/qualitygates/show` | 16 September, 2025 | |

### Deprecated Parameters We Actually Send (ACTION REQUIRED)

| Endpoint | Param We Send | Deprecated Since | Changelog Message | Status |
|----------|---------------|-----------------|-------------------|--------|
| `api/issues/search` | `types` | 25 Aug, 2023 | Deprecated. Use `impactSoftwareQualities` instead | USED in extract tasks |
| `api/issues/search` | `severities` | 25 Aug, 2023 | Deprecated. Use `impactSeverities` instead | USED in extract tasks |
| `api/issues/search` | `statuses` | 03 Jul, 2024 | Deprecated. Use `issueStatuses` instead | USED in project data extract |
| `api/issues/search` | `resolutions` | 03 Jul, 2024 | Deprecated. Use `issueStatuses` instead | USED in extract tasks |
| `api/user_tokens/search` | `login` | 2026-02-24 | Deprecated and ignored. Tokens are always listed for the authenticated user | USED in server/tokens.go |

### Deprecated Parameters We Do NOT Send (no action needed)

| Endpoint | Deprecated Param | Deprecated Since | Changelog Message |
|----------|-----------------|-----------------|-------------------|
| `api/permissions/add_group` | `groupId` | 7 April, 2025 | Use `groupName` and `organization` instead. We already send both. |
| `api/permissions/add_group_to_template` | `groupId` | 7 April, 2025 | Use `groupName` and `organization` instead. We send `groupName`. |
| `api/projects/create` | `branch` | 7.8 | |
| `api/projects/search` | `qualifiers` | 8.0 | |
| `api/qualityprofiles/add_project` | `key`, `projectUuid` | 6.5–6.6 | |
| `api/qualityprofiles/backup` | `key` | 6.6 | |
| `api/qualityprofiles/change_parent` | `key`, `parentKey` | 6.6 | |
| `api/qualityprofiles/delete` | `key` | 6.6 | |
| `api/qualityprofiles/set_default` | `key` | 6.6 | |
| `api/rules/update` | `debt_sub_characteristic` | 5.5 | |

---

## SonarQube Server (Extract — Read Operations)

All endpoints are GET requests used during the `extract` phase to read data from a SonarQube Server instance. Implemented in `lib/sq-api-go/server/` and `go/internal/extract/`.

### System & Configuration

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/system/info` | Server version, edition, cluster info | — |
| `GET /api/server/version` | Server version string | — |
| `GET /api/plugins/installed` | Installed plugins list | — |
| `GET /api/settings/values` | Global and project settings | `component` (optional), `keys` (optional) |
| `GET /api/alm_settings/list` | ALM/DevOps integrations | — |
| `GET /api/alm_settings/get_binding` | Project ALM binding | `project` |

### Projects

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/projects/search` | List all projects (paginated) | `p`, `ps` |
| `GET /api/projects/license_usage` | License usage data | — |
| `GET /api/navigation/component` | Project details (name, qualifier, etc.) | `component` |
| `GET /api/components/show` | Project tags | `component` |
| `GET /api/components/search` | Search components | varies |
| `GET /api/project_links/search` | Project links | `projectKey` |
| `GET /api/project_branches/list` | Project branches | `project` |
| `GET /api/project_pull_requests/list` | Project pull requests | `project` |
| `GET /api/project_analyses/search` | Analysis history | `project`, `p`, `ps` |
| `GET /api/new_code_periods/list` | New code period settings | `project` |

### Quality Profiles

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/qualityprofiles/search` | List quality profiles | — |
| `GET /api/qualityprofiles/backup` | Export profile as XML | `language`, `qualityProfile` |
| `GET /api/qualityprofiles/search_groups` | Groups with profile access | `language`, `qualityProfile`, `p`, `ps` |
| `GET /api/qualityprofiles/search_users` | Users with profile access | `language`, `qualityProfile`, `p`, `ps` |

### Quality Gates

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/qualitygates/list` | List quality gates | — |
| `GET /api/qualitygates/show` | Gate details and conditions | `name` |
| `GET /api/qualitygates/search_groups` | Groups with gate access | `gateName`, `p`, `ps` |
| `GET /api/qualitygates/search_users` | Users with gate access | `gateName`, `p`, `ps` |

### Users & Groups

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/users/search` | List users (paginated) | `p`, `ps` |
| `GET /api/user_groups/users` | Members of a group | `name`, `p`, `ps` |
| `GET /api/user_tokens/search` | User tokens | `login` |
| `GET /api/permissions/groups` | Groups with project/global permissions | `p`, `ps` |
| `GET /api/permissions/users` | Users with project/global permissions | varies |

### Permission Templates

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/permissions/search_templates` | List permission templates | — |
| `GET /api/permissions/template_groups` | Groups in a template | `templateName`, `p`, `ps` |
| `GET /api/permissions/template_users` | Users in a template | `templateName`, `p`, `ps` |

### Rules

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/rules/search` | List rules (paginated) | `p`, `ps` |
| `GET /api/rules/show` | Rule details | `key` |
| `GET /api/rules/repositories` | Rule repositories | — |

### Issues & Measures

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/issues/search` | Search issues (paginated) | `components`, `p`, `ps` |
| `GET /api/hotspots/search` | Search security hotspots | `projectKey`, `p`, `ps` |
| `GET /api/measures/search` | Project measures | `projectKeys`, `metricKeys` |
| `GET /api/measures/component_tree` | Component-level measures | varies |
| `GET /api/sources/raw` | Source file content | varies |
| `GET /api/sources/scm` | SCM blame data | varies |
| `GET /api/ce/activity` | Compute engine activity | varies |

### Views & Portfolios

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/views/search` | List views/portfolios | `qualifiers`, `p`, `ps` |
| `GET /api/views/show` | View details | `key` |
| `GET /api/applications/show` | Application details | varies |

### Webhooks

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /api/webhooks/list` | List webhooks | — |

---

## SonarQube Cloud — Standard API (Migrate — Write Operations)

POST endpoints on `sonarcloud.io` used during the `migrate` phase. Implemented in `lib/sq-api-go/cloud/`.

### Projects

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/projects/create` | Create project | `project`, `name`, `organization`, `visibility`, `newCodeDefinitionType`, `newCodeDefinitionValue` |
| `POST /api/projects/delete` | Delete project | `project` |
| `POST /api/project_tags/set` | Set project tags | `project`, `tags` |
| `POST /api/project_branches/rename` | Rename branch | `project`, `name` |

### Quality Profiles

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/qualityprofiles/create` | Create profile | `name`, `language`, `organization` |
| `POST /api/qualityprofiles/restore` | Restore profile from XML backup | `organization` + multipart file |
| `POST /api/qualityprofiles/delete` | Delete profile | `language`, `qualityProfile`, `organization` |
| `POST /api/qualityprofiles/set_default` | Set default profile | `language`, `qualityProfile`, `organization` |
| `POST /api/qualityprofiles/change_parent` | Set profile inheritance | `language`, `qualityProfile`, `parentQualityProfile`, `organization` |
| `POST /api/qualityprofiles/add_project` | Assign profile to project | `language`, `qualityProfile`, `project`, `organization` |
| `POST /api/qualityprofiles/add_group` | Grant group access to profile | `language`, `qualityProfile`, `group`, `organization` |

### Quality Gates

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/qualitygates/create` | Create gate | `name`, `organization` |
| `POST /api/qualitygates/create_condition` | Add gate condition | `gateId`, `organization`, `metric`, `op`, `error` |
| `POST /api/qualitygates/destroy` | Delete gate | `id`, `organization` |
| `POST /api/qualitygates/select` | Assign gate to project | `gateId`, `projectKey`, `organization` |
| `POST /api/qualitygates/set_as_default` | Set default gate | `id`, `organization` |

### Groups

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/user_groups/create` | Create group | `name`, `organization`, `description` |
| `POST /api/user_groups/delete` | Delete group | `id` |
| `POST /api/user_groups/add_user` | Add user to group | `name`, `login`, `organization` |

### Permissions

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/permissions/add_group` | Grant group permission (org or project level) | `groupName`, `permission`, `organization`, `projectKey` (optional) |
| `POST /api/permissions/add_group_to_template` | Add group to permission template | `templateId`, `groupName`, `permission` |
| `POST /api/permissions/create_template` | Create permission template | `name`, `organization`, `description`, `projectKeyPattern` |
| `POST /api/permissions/delete_template` | Delete permission template | `templateId` |
| `POST /api/permissions/set_default_template` | Set default template | `templateId`, `qualifier` |

### Rules

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/rules/update` | Update rule tags or description | `key`, `tags`, `markdown_note` |

### Settings

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/settings/set` | Set project setting | `component`, `key`, `value` |

### DevOps Platform Bindings

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /dop-translation/project-bindings` | Bind project to DevOps repo | `projectId`, `repositoryId` (JSON body) |

---

## SonarQube Cloud — Enterprise API (Migrate — Read & Write)

Endpoints on `api.sonarcloud.io` for enterprise features. Implemented in `lib/sq-api-go/cloud/enterprises.go`.

### Read Operations (used during migrate)

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `GET /enterprises/enterprises` | List enterprises | — |
| `GET /api/projects/search` | Search Cloud projects | `organization`, `p`, `ps` |
| `GET /api/users/current` | Get migration user info | — |
| `GET /api/alm_integration/list_repositories` | List ALM repos for org | `organization` |
| `GET /api/qualityprofiles/search` | Search Cloud profiles (for dedup) | `organization` |
| `GET /api/qualitygates/list` | List Cloud gates (for dedup) | `organization` |
| `GET /api/user_groups/search` | Search Cloud groups (for dedup) | `organization` |
| `GET /api/permissions/search_templates` | Search Cloud templates (for dedup) | `organization` |

### Write Operations

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /enterprises/portfolios` | Create portfolio | `enterpriseId`, `name`, `description`, `selection` (JSON body) |
| `PATCH /enterprises/portfolios/{id}` | Update portfolio projects | `projects` (JSON body) |
| `DELETE /enterprises/portfolios/{id}` | Delete portfolio | — |

---

## Project Data Import (Submit)

Direct HTTP calls in `go/internal/scanreport/submit.go` for importing project data.

| Endpoint | Purpose | Parameters |
|----------|---------|------------|
| `POST /api/ce/submit` | Submit analysis report | multipart: `projectKey`, `projectName`, report zip file |
| `GET /api/ce/task` | Poll task status | `id` |
