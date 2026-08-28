# SonarQube Migration Report

- Run ID: 06-05-2026-01
- Generated: 2026-06-05 12:00:00
- Started: 2026-06-05 12:00:00
- Completed: 2026-06-05 12:01:30
- Total elapsed: 1m30s
- Global objects provisioning: 10s
- Projects configuration provisioning: 45s
- Project data migration: 15s
- Issue and Hotspot sync: 20s
- Overall status: partial

## Executive Summary
| Objects | Full Migration | Near Full Migration | Partial Migration | Failed | Skipped |
|:---|:---|:---|:---|:---|:---|
| Projects | 1 | 1 | 1 | 1 | 1 |
| Total | 1 | 1 | 1 | 1 | 1 |

## Projects
1 succeeded, 1 near full migration, 1 partial migration, 1 failed, 1 skipped (1 organization skipped)
| Name | Organization | Outcome | Details |
|:---|:---|:---|:---|
| Proj Perfect | org1 | Full Migration | New Project Key: **org1_perfect** |
| Proj Near | org1 | Near Full Migration | New Project Key: **org1_near**<br>new_security_rating_with_aica <= A --> new_security_rating <= A |
| Proj Partial | org1 | Partial Migration | New Project Key: **org1_partial**<br>The new-code definition (reference branch) was replaced by the org default |
| Proj Failed | org1 | Failed | create failed: boom \| already exists |
| Proj Skipped | org1 | Skipped | org was skipped by the wizard |

## Bottlenecks

### Phase Timings
| Phase | Tasks | Duration |
|:---|:---|:---|
| Phase 0 | 3 | 1m0s |
| Phase 1 | 2 | 30s |

### Slowest Tasks
| Task | Phase | Duration | OK |
|:---|:---|:---|:---|
| createProjects | 0 | 45s | Yes |
| importProjectData | 0 | 15s | No |

### Per-Branch CE
| Branch | Type | Status | Task Id |
|:---|:---|:---|:---|
| feature-x | LONG | skipped |  |
| main | LONG | submitted | AY-task-1 |

## Failure Ledger
| Entity Type | Name | Organization | HTTP | Cause | Error |
|:---|:---|:---|:---|:---|:---|
| Project | Proj Failed | org1 | 400 | Already present | already exists \| duplicate key |
| Setting | sonar.dbcleaner.x | org1 | 400 | Not supported on Cloud | Setting 'sonar.dbcleaner.x' cannot be set on a Project |
| Group | devs | org1 | 400 | Needs reporting | Value of parameter 'x' must be one of: [a, b] |

### Why these failed

**Not supported on Cloud** — 42048 failures

- **What happened:** no project-scope counterpart on Cloud
- **What to do:** none available; expected to be dropped
- **Examples:** sonar.dbcleaner.x

**Already present** — 1 failure

- **What happened:** the entity already exists on the target
- **What to do:** none needed
- **Examples:** Proj Failed

**Needs reporting** — 1 failure

- **What happened:** Cloud rejected the request for an unrecognised reason
- **What to do:** report this with the run id
- **Please report this** — it indicates a defect in the migration tool, not a limitation of SonarQube Cloud.
- **Examples:** devs

## Warnings, Retries & Skips

### Retries
| Method | Endpoint | Count | Max Attempt | Last Status |
|:---|:---|:---|:---|:---|
| POST | /api/ce/submit | 3 | 3 | 503 |

### Branch Skips
| Branch | Findings | Reason |
|:---|:---|:---|
| feature-x | 12 | skipping branch: source code not retrievable |

### Gate Condition Skips
| Gate | Metric | Action | Note |
|:---|:---|:---|:---|
| Backend QG | contains_ai_code | skipped | addGateConditions: source metric has no SonarQube Cloud equivalent |
| Backend QG | new_security_rating_with_aica | remapped | addGateConditions: source metric remapped |

### Metric Remaps
| Gate | Source Metric | Target Metric |
|:---|:---|:---|
| Backend QG | new_security_rating_with_aica | new_security_rating |

## Branch Project Data
| Branch | Type | Status | Issues | External Issues | Components | Active Rules | Zip Bytes | Task Id | Skip Reason |
|:---|:---|:---|:---|:---|:---|:---|:---|:---|:---|
| feature-x | LONG | skipped | 0 | 0 | 0 | 0 | 0 |  | skipping branch: source code not retrievable |
| main | LONG | submitted | 120 | 5 | 40 | 300 | 1,048,576 | AY-task-1 |  |

