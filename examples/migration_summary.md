# SonarQube Migration Report

- Run ID: 2026-06-28-06
- Generated: 2026-06-28 22:43:55
- Started: 2026-06-28 22:27:20
- Completed: 2026-06-28 22:43:55
- Total elapsed: 16m35.053s
- Overall status: success

## Executive Summary
| Objects | Full Migration | Near Full Migration | Partial Migration | Failed | Skipped |
|:---|:---|:---|:---|:---|:---|
| Quality Gates | 1 | 1 | 1 | 0 | 9 |
| Quality Profiles | 8 | 17 | 1 | 0 | 67 |
| Permission Templates | 9 | 0 | 0 | 0 | 2 |
| Groups | 32 | 0 | 0 | 0 | 10 |
| Portfolios | 15 | 2 | 6 | 0 | 5 |
| Projects | 64 | 7 | 6 | 0 | 1 |
| Global Settings | 110 | 4 | 2 | 0 | 252 |
| Total | 239 | 31 | 16 | 0 | 346 |

## Quality Gates
1 succeeded, 1 near full migration, 1 partial migration, 0 failed, 9 skipped (2 built-in, 7 unused)
| Name | Organization | Outcome | Details |
|:---|:---|:---|:---|
| Sonar way w/o coverage | latest-gh | Full Migration |  |
| ß test QG | latest-gh | Near Full Migration | Some metrics were mapped to the closest SonarQube Cloud equivalents:<br>new_software_quality_reliability_issues > 0 --> new_reliability_rating <= A<br>new_software_quality_security_issues > 0 --> new_security_rating <= A |
| 0 - Corp Platinum | latest-unbound | Partial Migration | Some metrics were mapped to the closest SonarQube Cloud equivalents:<br>software_quality_blocker_issues > 0 --> reliability_rating <= D<br>software_quality_blocker_issues > 0 --> security_rating <= D<br>Some conditions were dropped because the source metric has no SonarQube Cloud equivalent:<br>new_software_quality_reliability_remediation_effort > 0 |
| Sonar way |  | Skipped | Built-in, not migrated |
| Sonar way for AI Code |  | Skipped | Built-in, not migrated |
| 🥇 1 - Corp Gold |  | Skipped | Not used by any migrated project |
| 🥈 2 - Corp Silver |  | Skipped | Not used by any migrated project |
| 🥉 3 - Corp base |  | Skipped | Not used by any migrated project |
| Bad - Absolute nbr of issues |  | Skipped | Not used by any migrated project |
| Bad - Irrelevant metrics |  | Skipped | Not used by any migrated project |
| Bad - Twice the same criteria |  | Skipped | Not used by any migrated project |
| Sonar way + SCA |  | Skipped | Not used by any migrated project |

## Quality Profiles
8 succeeded, 17 near full migration, 1 partial migration, 0 failed, 67 skipped (6 organization skipped, 54 built-in, 7 unused)
| Name | Organization | Outcome | Details |
|:---|:---|:---|:---|
| php / All rules | latest-gh | Full Migration |  |
| js / security-max | latest-gl | Full Migration |  |
| php / All rules | latest-others | Full Migration |  |
| js / security-max | latest-others | Full Migration |  |
| php / All rules | latest-unbound | Full Migration |  |
| js / security-max | latest-unbound | Full Migration |  |
| js / security-max | latest-gh | Full Migration |  |
| php / All rules | latest-gl | Full Migration |  |
| java / Security Max | latest-gl | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: java:Don_t_be_rude |
| xml / Xpath instantiated | latest-gl | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: xml:XML_Xpath_instantiated_rule |
| dart / Corp Way | latest-others | Near Full Migration | Since SQC does not support parent profile rules disabled in child profiles, the following rules were enabled in the profile: dart:S1161 |
| dart / Critical projects | latest-others | Near Full Migration | Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: dart:S7103 |
| dart / Corp Way | latest-gh | Near Full Migration | Since SQC does not support parent profile rules disabled in child profiles, the following rules were enabled in the profile: dart:S1161 |
| java / Security Max | latest-others | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: java:Don_t_be_rude |
| xml / Xpath instantiated | latest-others | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: xml:XML_Xpath_instantiated_rule |
| dart / Critical projects | latest-gh | Near Full Migration | Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: dart:S7103 |
| dart / Corp Way | latest-unbound | Near Full Migration | Since SQC does not support parent profile rules disabled in child profiles, the following rules were enabled in the profile: dart:S1161 |
| dart / Critical projects | latest-unbound | Near Full Migration | Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: dart:S7103 |
| py / Olivier Way | latest-unbound | Near Full Migration | Permissions granted to 1 user have been dropped in the migration<br>Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: python:InequalityUsage, python:S1128, python:S6740<br>Since SQC does not support prioritized rules, the following rules were migrated in the profile as regular rules: python:S6740 |
| java / Security Max | latest-unbound | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: java:Don_t_be_rude |
| xml / Xpath instantiated | latest-unbound | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: xml:XML_Xpath_instantiated_rule |
| java / Security Max | latest-gh | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: java:Don_t_be_rude |
| xml / Xpath instantiated | latest-gh | Near Full Migration | Because rule templates and instantiated rules are not supported in SQC, the following rules were not migrated: xml:XML_Xpath_instantiated_rule |
| dart / Corp Way | latest-gl | Near Full Migration | Since SQC does not support parent profile rules disabled in child profiles, the following rules were enabled in the profile: dart:S1161 |
| dart / Critical projects | latest-gl | Near Full Migration | Because rules custom severities are not supported in SQC, the following rules were reverted to their default severities: dart:S7103 |
| java / Green IT | latest-unbound | Partial Migration | Because SQC does not support 3rd party plugins, the following 3rd party rules were removed from the quality profile: creedengo-java:GCI1, creedengo-java:GCI2, creedengo-java:GCI27, creedengo-java:GCI28, creedengo-java:GCI3, creedengo-java:GCI32, creedengo-java:GCI5, creedengo-java:GCI67, creedengo-java:GCI69, creedengo-java:GCI72, creedengo-java:GCI74, creedengo-java:GCI76, creedengo-java:GCI77, creedengo-java:GCI78, creedengo-java:GCI79, creedengo-java:GCI82, creedengo-java:GCI94 |
| java / Security Max | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| dart / Corp Way | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| dart / Critical projects | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| js / security-max | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| xml / Xpath instantiated | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| php / All rules | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| abap / Sonar way |  | Skipped | Built-in, not migrated |
| ansible / Sonar way |  | Skipped | Built-in, not migrated |
| apex / Sonar way |  | Skipped | Built-in, not migrated |
| azurepipelines / Sonar way |  | Skipped | Built-in, not migrated |
| azureresourcemanager / Sonar way |  | Skipped | Built-in, not migrated |
| c / Sonar way |  | Skipped | Built-in, not migrated |
| cloudformation / Sonar way |  | Skipped | Built-in, not migrated |
| cobol / Sonar way |  | Skipped | Built-in, not migrated |
| cpp / Mission critical |  | Skipped | Built-in, not migrated |
| cpp / Sonar MISRA C++:2023 Compliance |  | Skipped | Built-in, not migrated |
| cpp / Sonar way |  | Skipped | Built-in, not migrated |
| cs / Sonar way |  | Skipped | Built-in, not migrated |
| css / Sonar way |  | Skipped | Built-in, not migrated |
| dart / Sonar way |  | Skipped | Built-in, not migrated |
| docker / Sonar way |  | Skipped | Built-in, not migrated |
| flex / Sonar way |  | Skipped | Built-in, not migrated |
| githubactions / Sonar way |  | Skipped | Built-in, not migrated |
| go / Sonar way |  | Skipped | Built-in, not migrated |
| groovy / Sonar way |  | Skipped | Built-in, not migrated |
| ipynb / Sonar agentic AI |  | Skipped | Built-in, not migrated |
| ipynb / Sonar way |  | Skipped | Built-in, not migrated |
| java / Sonar agentic AI |  | Skipped | Built-in, not migrated |
| java / Sonar way |  | Skipped | Built-in, not migrated |
| jcl / Sonar way |  | Skipped | Built-in, not migrated |
| js / Sonar agentic AI |  | Skipped | Built-in, not migrated |
| js / Sonar way |  | Skipped | Built-in, not migrated |
| json / Sonar way |  | Skipped | Built-in, not migrated |
| jsp / Sonar way |  | Skipped | Built-in, not migrated |
| kotlin / Sonar way |  | Skipped | Built-in, not migrated |
| kubernetes / Sonar way |  | Skipped | Built-in, not migrated |
| objc / Sonar way |  | Skipped | Built-in, not migrated |
| php / Sonar way |  | Skipped | Built-in, not migrated |
| pli / Sonar way |  | Skipped | Built-in, not migrated |
| plsql / Sonar way |  | Skipped | Built-in, not migrated |
| powershell / Sonar way |  | Skipped | Built-in, not migrated |
| py / Sonar agentic AI |  | Skipped | Built-in, not migrated |
| py / Sonar way |  | Skipped | Built-in, not migrated |
| rpg / Sonar way |  | Skipped | Built-in, not migrated |
| ruby / Sonar way |  | Skipped | Built-in, not migrated |
| rust / Sonar way |  | Skipped | Built-in, not migrated |
| scala / Sonar way |  | Skipped | Built-in, not migrated |
| secrets / Sonar way |  | Skipped | Built-in, not migrated |
| shell / Sonar way |  | Skipped | Built-in, not migrated |
| swift / Sonar way |  | Skipped | Built-in, not migrated |
| terraform / Sonar way |  | Skipped | Built-in, not migrated |
| text / Sonar way |  | Skipped | Built-in, not migrated |
| ts / Sonar agentic AI |  | Skipped | Built-in, not migrated |
| ts / Sonar way |  | Skipped | Built-in, not migrated |
| tsql / Sonar way |  | Skipped | Built-in, not migrated |
| vb / Sonar way |  | Skipped | Built-in, not migrated |
| vbnet / Sonar way |  | Skipped | Built-in, not migrated |
| web / Sonar way |  | Skipped | Built-in, not migrated |
| xml / Sonar way |  | Skipped | Built-in, not migrated |
| yaml / Sonar way |  | Skipped | Built-in, not migrated |
| ansible / Test |  | Skipped | Not used by any migrated project |
| java / Sonar Way + Checkstyle |  | Skipped | Not used by any migrated project |
| java / Sonar Way + Creedengo |  | Skipped | Not used by any migrated project |
| jcl / All rules |  | Skipped | Not used by any migrated project |
| kotlin / No rules |  | Skipped | Not used by any migrated project |
| py / Prioritized |  | Skipped | Not used by any migrated project |
| secrets / Corp Way |  | Skipped | Not used by any migrated project |

## Permission Templates
9 succeeded, 0 failed, 2 skipped (2 organization skipped)
| Name | Organization | Outcome | Details |
|:---|:---|:---|:---|
| 0. Default Template for portfolio | latest-gh | Full Migration |  |
| 0. Default Template for portfolio | latest-unbound | Full Migration |  |
| 1. Banking projects | latest-unbound | Full Migration | Permissions granted to 1 user have been dropped in the migration |
| 0. Default Template for portfolio | latest-others | Full Migration |  |
| 0. Default template | latest-unbound | Full Migration |  |
| 0. Default template | latest-others | Full Migration |  |
| 0. Default template | latest-gh | Full Migration |  |
| 0. Default template | latest-gl | Full Migration |  |
| 0. Default Template for portfolio | latest-gl | Full Migration |  |
| 0. Default Template for portfolio | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| 0. Default template | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |

## Groups
32 succeeded, 0 failed, 10 skipped (9 organization skipped, 1 built-in)
| Name | Organization | Outcome | Details |
|:---|:---|:---|:---|
| language-experts | latest-gh | Full Migration |  |
| tech-leads | latest-gl | Full Migration |  |
| ci-tools | latest-others | Full Migration |  |
| developers | latest-others | Full Migration |  |
| language-experts | latest-others | Full Migration |  |
| tech-leads | latest-gh | Full Migration |  |
| project-admins | latest-others | Full Migration |  |
| project-admins | latest-gh | Full Migration |  |
| quality-managers | latest-others | Full Migration |  |
| sonar-administrators | latest-gh | Full Migration |  |
| ci-tools | latest-gh | Full Migration |  |
| security-auditors | latest-others | Full Migration |  |
| ci-tools | latest-gl | Full Migration |  |
| developers | latest-gl | Full Migration |  |
| security-auditors | latest-gh | Full Migration |  |
| sonar-administrators | latest-others | Full Migration |  |
| quality-managers | latest-gh | Full Migration |  |
| tech-leads | latest-others | Full Migration |  |
| ci-tools | latest-unbound | Full Migration |  |
| developers | latest-unbound | Full Migration |  |
| project-admins | latest-unbound | Full Migration |  |
| developers | latest-gh | Full Migration |  |
| language-experts | latest-unbound | Full Migration |  |
| quality-managers | latest-unbound | Full Migration |  |
| security-auditors | latest-unbound | Full Migration |  |
| sonar-administrators | latest-unbound | Full Migration |  |
| tech-leads | latest-unbound | Full Migration |  |
| language-experts | latest-gl | Full Migration |  |
| project-admins | latest-gl | Full Migration |  |
| quality-managers | latest-gl | Full Migration |  |
| security-auditors | latest-gl | Full Migration |  |
| sonar-administrators | latest-gl | Full Migration |  |
| quality-managers | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| security-auditors | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| sonar-administrators | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| language-experts | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| sonar-users | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| project-admins | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| developers | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| ci-tools | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| tech-leads | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |
| sonar-users |  | Skipped | Built-in group on SonarQube Server; replaced by the implicit 'Members' group on SonarQube Cloud. |

## Portfolios
15 succeeded, 2 near full migration, 6 partial migration, 0 failed
| Name | Outcome | Details |
|:---|:---|:---|
| My favorite projects | Full Migration |  |
| Life Insurance | Full Migration |  |
| Olivier's projects | Full Migration |  |
| Portfolio Tag 1 | Full Migration |  |
| Portfolio regexp 1 | Full Migration |  |
| Portfolio Tag 2 | Full Migration |  |
| Portfolio regexp 2 | Full Migration |  |
| CEO Strategic Projects | Full Migration |  |
| Portfolios multiple branches | Full Migration |  |
| Private Banking | Full Migration |  |
| Python Projects | Full Migration |  |
| Retail Banking | Full Migration |  |
| Health Insurance | Full Migration |  |
| ESA-BU-XXX | Full Migration |  |
| Demo projects | Full Migration |  |
| Portfolio of Apps | Near Full Migration | The SQS portfolio contains applications that were replaced by their enclosed projects |
| Hybrid app proj portfolio | Near Full Migration | The SQS portfolio contains applications that were replaced by their enclosed projects |
| Other unclassified projects | Partial Migration | The SQS portfolio is defined with REST selection mode, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different |
| Portfolio super regexp | Partial Migration | The SQS portfolio has nested subportfolios with different selection modes, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different |
| Portofolio super tags | Partial Migration | The SQS portfolio has nested subportfolios with different selection modes, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different |
| Company global portfolio | Partial Migration | The SQS portfolio has nested subportfolios with different selection modes, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different |
| Insurance | Partial Migration | The SQS portfolio has nested subportfolios with different selection modes, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different |
| Banking | Partial Migration | The SQS portfolio has nested subportfolios depth higher than 2, it was converted to a flat list of projects in SQC. The portfolio perimeter may be slightly different |
| Investment Banking | Skipped | The SQS portfolio is empty, was not migrated |
| Corporate Mergers and Acquisitions | Skipped | The SQS portfolio is empty, was not migrated |
| Corporate loans | Skipped | The SQS portfolio is empty, was not migrated |
| fdfgf | Skipped | The SQS portfolio is empty, was not migrated |
| Other Insurance | Skipped | The SQS portfolio is empty, was not migrated |

## Projects
64 succeeded, 7 near full migration, 6 partial migration, 0 failed, 1 skipped (1 organization skipped)
| Name | Organization | Outcome | Details |
|:---|:---|:---|:---|
| Demo Gitlab-CI Gradle | latest-gl | Full Migration | New Project Key: **latest-gl_okorach_demo-gitlabci-gradle_5bd095d2-c47c-4b64-aea5-ea48f95446c0**<br>Source project was provisioned but never analyzed, project settings migrated anyway<br>Permissions granted to 1 user have been dropped in the migration |
| BANKING-TRADING-EURO | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-TRADING-EURO** |
| Retail Clerk | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-RETAIL-CLERK** |
| Demo Jenkins Maven | latest-gl | Full Migration | New Project Key: **latest-gl_okorach_demo-jenkins-maven_605e4e16-531b-4c2c-9a30-c98c254c6e15**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| pr-demo | latest-others | Full Migration | New Project Key: **latest-others_okorach-org_pr-demo_3a1857ec-cebc-49f2-96ac-9bbc99111469**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| Project 2 | latest-others | Full Migration | New Project Key: **latest-others_test:project2** |
| GitHub / Actions / CLI | latest-unbound | Full Migration | New Project Key: **latest-unbound_demo:github-actions-cli** |
| 12k-issues-flat | latest-unbound | Full Migration | New Project Key: **latest-unbound_12k-issues-flat** |
| Retail Web | latest-unbound | Full Migration | New Project Key: **latest-unbound_RETAIL-WEB**<br>100% of issues with manual changes synced (17/17) |
| exclusions-2 | latest-unbound | Full Migration | New Project Key: **latest-unbound_exclusions-2**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| GitHub / Actions / Maven | latest-gh | Full Migration | New Project Key: **latest-gh_demo:github-actions-maven** |
| INSURANCE-HOME | latest-unbound | Full Migration | New Project Key: **latest-unbound_INSURANCE-HOME** |
| BANKING-INVESTMENT-DILIGENCE | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-INVESTMENT-DILIGENCE** |
| okorach_docker-hello-world | latest-unbound | Full Migration | New Project Key: **latest-unbound_okorach_docker-hello-world**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| Web front-end | latest-unbound | Full Migration | New Project Key: **latest-unbound_web-frontend**<br>100% of issues with manual changes synced (21/21) |
| BANKING-ASIA-OPS | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-ASIA-OPS** |
| Web back-end | latest-unbound | Full Migration | New Project Key: **latest-unbound_web-backend** |
| code-variants | latest-unbound | Full Migration | New Project Key: **latest-unbound_code-variants**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| BANKING-INVESTMENT-EQUITY | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-INVESTMENT-EQUITY** |
| GitLab-CI / Maven | latest-gl | Full Migration | New Project Key: **latest-gl_demo:gitlab-ci-maven** |
| Project without analyses | latest-unbound | Full Migration | New Project Key: **latest-unbound_project-without-analyses**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| demo:gitlab:gradle | latest-unbound | Full Migration | New Project Key: **latest-unbound_demo:gitlab:gradle**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| BANKING-INVESTMENT-MERGER | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-INVESTMENT-MERGER** |
| BANKING-INVESTMENT-ACQUISITIONS | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-INVESTMENT-ACQUISITIONS** |
| Wrong Scanner | latest-unbound | Full Migration | New Project Key: **latest-unbound_wrong-scanner** |
| Coverage demo | latest-unbound | Full Migration | New Project Key: **latest-unbound_org.sonarqube:example-coverage** |
| Project 5 | latest-unbound | Full Migration | New Project Key: **latest-unbound_test:proyecto5** |
| INSURANCE-LIFE | latest-unbound | Full Migration | New Project Key: **latest-unbound_INSURANCE-LIFE** |
| AI CodeFix examples | latest-unbound | Full Migration | New Project Key: **latest-unbound_ai-code-fix** |
| BANKING-ACQUISITIONS | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-ACQUISITIONS** |
| BANKING-ACQUISITIONS-DILIGENCE | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-ACQUISITIONS-DILIGENCE** |
| Banking Africa operations | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-AFRICA-OPS** |
| BANKING-MERGERS | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-MERGERS** |
| BANKING-TRADING-JAPAN | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-TRADING-JAPAN** |
| 12k-issues-structured | latest-unbound | Full Migration | New Project Key: **latest-unbound_12k-issues-structured** |
| Juice Shop | latest-unbound | Full Migration | New Project Key: **latest-unbound_test:juice-shop** |
| demo:github-actions-mono-maven | latest-gh | Full Migration | New Project Key: **latest-gh_demo:github-actions-mono-maven**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| Creedengo | latest-unbound | Full Migration | New Project Key: **latest-unbound_creedengo** |
| okorach_demo-gitlabci-cli_e81d5112-e681-44b2-aee4-62b56c8ac5cb | latest-unbound | Full Migration | New Project Key: **latest-unbound_okorach_demo-gitlabci-cli_e81d5112-e681-44b2-aee4-62b56c8ac5cb**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| BANKING-TRADING-NASDAQ | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-TRADING-NASDAQ** |
| BANKING-RETAIL-WEB | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-RETAIL-WEB** |
| Retail - ATM | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-RETAIL-ATM** |
| Project with no perms | latest-unbound | Full Migration | New Project Key: **latest-unbound_Project-with-no-perms**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| laravel | latest-unbound | Full Migration | New Project Key: **latest-unbound_laravel**<br>100% of issues with manual changes synced (1/1) |
| demo:gitlab:scanner-cli | latest-gl | Full Migration | New Project Key: **latest-gl_demo:gitlab:scanner-cli**<br>Source code of branches **main**, **master** is missing (likely purged in SQS). Migration is executed without the sources. |
| INSURANCE-HEALTH | latest-unbound | Full Migration | New Project Key: **latest-unbound_INSURANCE-HEALTH** |
| demo:coverage | latest-unbound | Full Migration | New Project Key: **latest-unbound_demo:coverage**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| iceoryx | latest-unbound | Full Migration | New Project Key: **latest-unbound_iceoryx**<br>100% of issues with manual changes synced (6/6) |
| Secrets detection | latest-unbound | Full Migration | New Project Key: **latest-unbound_demo:secrets** |
| okorach_demo-gitlabci-maven | latest-unbound | Full Migration | New Project Key: **latest-unbound_okorach_demo-gitlabci-maven**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| BANKING-PRIVATE-ASSETS | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-PRIVATE-ASSETS** |
| Wealth Management | latest-unbound | Full Migration | New Project Key: **latest-unbound_BANKING-PRIVATE-WEALTH** |
| SCA demo - Log4shell detect - Maven | latest-unbound | Full Migration | New Project Key: **latest-unbound_demo:sca-log4shell-detect-maven** |
| dotnet-with-cli | latest-unbound | Full Migration | New Project Key: **latest-unbound_dotnet-with-cli**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| GitHub / Actions / monorepo .Net Core | latest-gh | Full Migration | New Project Key: **latest-gh_demo:github-actions-mono-dotnet** |
| gradle-with-cli | latest-unbound | Full Migration | New Project Key: **latest-unbound_gradle-with-cli**<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| name | latest-unbound | Full Migration | New Project Key: **latest-unbound_TEST**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| Green-IT | latest-unbound | Full Migration | New Project Key: **latest-unbound_creedengo-issues** |
| Third party issues | latest-unbound | Full Migration | New Project Key: **latest-unbound_third-party-issues** |
| demo:ado-cli | latest-unbound | Full Migration | New Project Key: **latest-unbound_demo:ado-cli**<br>Source project was provisioned but never analyzed, project settings migrated anyway |
| INSURANCE-PET | latest-unbound | Full Migration | New Project Key: **latest-unbound_INSURANCE-PET** |
| Training: Cyclomatic vs Cognitive complexity | latest-unbound | Full Migration | New Project Key: **latest-unbound_training:complexity** |
| Mute issue in IDE | latest-unbound | Full Migration | New Project Key: **latest-unbound_mute-in-ide**<br>100% of issues with manual changes synced (1/1) |
| GitHub / Actions / monorepo CLI | latest-gh | Full Migration | New Project Key: **latest-gh_demo:github-actions-mono-cli** |
| maven-with-cli | latest-unbound | Near Full Migration | New Project Key: **latest-unbound_maven-with-cli**<br>66% of issues with manual changes synced (4/6)<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| Demo Security | latest-gh | Near Full Migration | New Project Key: **latest-gh_demo:java-security**<br>0% of issues with manual changes synced (0/4) |
| Training: External issues import | latest-unbound | Near Full Migration | New Project Key: **latest-unbound_training:external-issues**<br>0% of issues with manual changes synced (0/2) |
| demo-rules | latest-unbound | Near Full Migration | New Project Key: **latest-unbound_demo-rules**<br>0% of issues with manual changes synced (0/3)<br>Source code of branch **main** is missing (likely purged in SQS). Migration is executed without the sources. |
| file-issue | latest-unbound | Near Full Migration | New Project Key: **latest-unbound_file-issue**<br>0% of issues with manual changes synced (0/1) |
| Project with checkstyle issues | latest-unbound | Near Full Migration | New Project Key: **latest-unbound_checkstyle-issues**<br>44% of issues with manual changes synced (36/81) |
| GitHub / Actions / monorepo Gradle | latest-gh | Near Full Migration | New Project Key: **latest-gh_demo:github-actions-mono-gradle**<br>0% of issues with manual changes synced (0/1) |
| Project 4 | latest-unbound | Partial Migration | New Project Key: **latest-unbound_test:project4**<br>The new code period "reference branch" does not exist on SonarQube Cloud and has been replaced by the org default. |
| Project 1 | latest-others | Partial Migration | New Project Key: **latest-others_test:project1**<br>0% of issues with manual changes synced (0/18)<br>Per-branch new code period overrides do not exist on SonarQube Cloud; branches will inherit the project-level new code period. |
| Sonar Tools | latest-unbound | Partial Migration | New Project Key: **latest-unbound_okorach-oss_sonar-tools**<br>Source project was provisioned but never analyzed, project settings migrated anyway<br>97% of issues with manual changes synced (451/461); 98% of hotspots with manual changes synced (59/60); 1 ACKNOWLEDGED hotspot left as TO_REVIEW (status not supported on SonarQube Cloud)<br>Per-branch new code period overrides do not exist on SonarQube Cloud; branches will inherit the project-level new code period. |
| BANKING-PORTAL | latest-unbound | Partial Migration | New Project Key: **latest-unbound_BANKING-PORTAL**<br>Project data migration skipped: API error when migrating project data: CE task failed: CE task AZ8P7uBbnJDP_RSpCj5i failed: Fail to process issues of component 'latest-unbound_BANKING-PORTAL:src/main/java/demo/security/util/DBUtils.java:BRANCH:release-3.2' (Visit of Component {key=latest-unbound_BANKING-PORTAL:src/main/java/demo/security/util/DBUtils.java:BRANCH:release-3.2,type=FILE} failed) |
| BANKING-ASIA-OPS | latest-unbound | Partial Migration | New Project Key: **latest-unbound_bad:stale-project**<br>Project data migration skipped: API error when migrating project data: CE task failed: CE task AZ8P7zaMj1SK7a-5nF2C failed: Fail to process issues of component 'latest-unbound_bad:stale-project:src/main/java/demo/security/util/DBUtils.java' (Visit of Component {key=latest-unbound_bad:stale-project:src/main/java/demo/security/util/DBUtils.java,type=FILE} failed) |
| dvpa | latest-unbound | Partial Migration | New Project Key: **latest-unbound_dvpa**<br>Project data migration skipped: API error when migrating project data: CE task failed: CE task AZ8P75HxnJDP_RSpCj55 failed: Fail to process issues of component 'latest-unbound_dvpa:RCE-Labs/RCE-2/rce_2.php' (Visit of Component {key=latest-unbound_dvpa:RCE-Labs/RCE-2/rce_2.php,type=FILE} failed) |
| Project 3 | bitbucket-server.your-company.com/bitbucket-server.your-company.com | Skipped | Organization skipped |

## Global Settings
110 succeeded, 4 near full migration, 2 partial migration, 0 failed, 252 skipped (11 not applicable on SonarQube cloud, 237 left at default value on SonarQube server)
| Setting Key | Organization | Outcome | Details |
|:---|:---|:---|:---|
| sonar.cpd.exclusions | latest-gh | Full Migration | Applied value=**duplex*,triplex*** |
| sonar.cpd.exclusions | latest-gl | Full Migration | Applied value=**duplex*,triplex*** |
| sonar.cpd.exclusions | latest-others | Full Migration | Applied value=**duplex*,triplex*** |
| sonar.cpd.exclusions | latest-unbound | Full Migration | Applied value=**duplex*,triplex*** |
| sonar.issue.ignore.multicriteria | latest-gh | Full Migration | Applied value=**[{"resourceKey":"**/javafiles*","ruleKey":"java:S11*"},{"resourceKey":"**/gen/c/**","ruleKey":"c:S2*"}]** |
| sonar.issue.ignore.multicriteria | latest-gl | Full Migration | Applied value=**[{"resourceKey":"**/javafiles*","ruleKey":"java:S11*"},{"resourceKey":"**/gen/c/**","ruleKey":"c:S2*"}]** |
| sonar.issue.ignore.multicriteria | latest-others | Full Migration | Applied value=**[{"resourceKey":"**/javafiles*","ruleKey":"java:S11*"},{"resourceKey":"**/gen/c/**","ruleKey":"c:S2*"}]** |
| sonar.issue.ignore.multicriteria | latest-unbound | Full Migration | Applied value=**[{"resourceKey":"**/javafiles*","ruleKey":"java:S11*"},{"resourceKey":"**/gen/c/**","ruleKey":"c:S2*"}]** |
| sonar.issue.ignore.block | latest-gh | Full Migration | Applied value=**[{"beginBlockRegexp":"EXCLUDE_BEGIN","endBlockRegexp":"EXCLUDE_END"}]** |
| sonar.issue.ignore.block | latest-gl | Full Migration | Applied value=**[{"beginBlockRegexp":"EXCLUDE_BEGIN","endBlockRegexp":"EXCLUDE_END"}]** |
| sonar.issue.ignore.block | latest-others | Full Migration | Applied value=**[{"beginBlockRegexp":"EXCLUDE_BEGIN","endBlockRegexp":"EXCLUDE_END"}]** |
| sonar.issue.ignore.block | latest-unbound | Full Migration | Applied value=**[{"beginBlockRegexp":"EXCLUDE_BEGIN","endBlockRegexp":"EXCLUDE_END"}]** |
| sonar.issue.enforce.multicriteria | latest-gh | Full Migration | Applied value=**[{"resourceKey":"**/new*","ruleKey":"java:*Naming*"}]** |
| sonar.issue.enforce.multicriteria | latest-gl | Full Migration | Applied value=**[{"resourceKey":"**/new*","ruleKey":"java:*Naming*"}]** |
| sonar.issue.enforce.multicriteria | latest-others | Full Migration | Applied value=**[{"resourceKey":"**/new*","ruleKey":"java:*Naming*"}]** |
| sonar.issue.enforce.multicriteria | latest-unbound | Full Migration | Applied value=**[{"resourceKey":"**/new*","ruleKey":"java:*Naming*"}]** |
| sonar.test.inclusions | latest-gh | Full Migration | Applied value=**testinclude.*** |
| sonar.test.inclusions | latest-gl | Full Migration | Applied value=**testinclude.*** |
| sonar.test.inclusions | latest-others | Full Migration | Applied value=**testinclude.*** |
| sonar.test.inclusions | latest-unbound | Full Migration | Applied value=**testinclude.*** |
| sonar.coverage.exclusions | latest-gh | Full Migration | Applied value=**covexclude.*** |
| sonar.coverage.exclusions | latest-gl | Full Migration | Applied value=**covexclude.*** |
| sonar.coverage.exclusions | latest-others | Full Migration | Applied value=**covexclude.*** |
| sonar.coverage.exclusions | latest-unbound | Full Migration | Applied value=**covexclude.*** |
| sonar.html.file.suffixes | latest-gh | Full Migration | Applied value=**.ascx,.aspx,.cmp,.cshtml,.erb,.html,.rhtml,.shtm,.shtml,.twig,.vbhtml,.xhtml** to all projects |
| sonar.html.file.suffixes | latest-gl | Full Migration | Applied value=**.ascx,.aspx,.cmp,.cshtml,.erb,.html,.rhtml,.shtm,.shtml,.twig,.vbhtml,.xhtml** to all projects |
| sonar.html.file.suffixes | latest-others | Full Migration | Applied value=**.ascx,.aspx,.cmp,.cshtml,.erb,.html,.rhtml,.shtm,.shtml,.twig,.vbhtml,.xhtml** to all projects |
| sonar.html.file.suffixes | latest-unbound | Full Migration | Applied value=**.ascx,.aspx,.cmp,.cshtml,.erb,.html,.rhtml,.shtm,.shtml,.twig,.vbhtml,.xhtml** to all projects |
| sonar.issue.ignore.allfile | latest-gh | Full Migration | Applied value=**[{"fileRegexp":"Generated with"},{"fileRegexp":"DONT_(SCAN\|ANALYZE)_THIS_FILE"}]** |
| sonar.issue.ignore.allfile | latest-gl | Full Migration | Applied value=**[{"fileRegexp":"Generated with"},{"fileRegexp":"DONT_(SCAN\|ANALYZE)_THIS_FILE"}]** |
| sonar.issue.ignore.allfile | latest-others | Full Migration | Applied value=**[{"fileRegexp":"Generated with"},{"fileRegexp":"DONT_(SCAN\|ANALYZE)_THIS_FILE"}]** |
| sonar.issue.ignore.allfile | latest-unbound | Full Migration | Applied value=**[{"fileRegexp":"Generated with"},{"fileRegexp":"DONT_(SCAN\|ANALYZE)_THIS_FILE"}]** |
| sonar.exclusions | latest-gh | Full Migration | Applied value=****/*.bin,**/*.exe** (merged from sonar.global.exclusions + sonar.exclusions) |
| sonar.exclusions | latest-gl | Full Migration | Applied value=****/*.bin,**/*.exe** (merged from sonar.global.exclusions + sonar.exclusions) |
| sonar.exclusions | latest-others | Full Migration | Applied value=****/*.bin,**/*.exe** (merged from sonar.global.exclusions + sonar.exclusions) |
| sonar.exclusions | latest-unbound | Full Migration | Applied value=****/*.bin,**/*.exe** (merged from sonar.global.exclusions + sonar.exclusions) |
| sonar.coverage.jacoco.xmlReportPaths | latest-gh | Full Migration | Applied value=****/jacoco*.xml** to all projects |
| sonar.coverage.jacoco.xmlReportPaths | latest-gl | Full Migration | Applied value=****/jacoco*.xml** to all projects |
| sonar.coverage.jacoco.xmlReportPaths | latest-others | Full Migration | Applied value=****/jacoco*.xml** to all projects (skipped: latest-others_test:project1 (override)) |
| sonar.coverage.jacoco.xmlReportPaths | latest-unbound | Full Migration | Applied value=****/jacoco*.xml** to all projects |
| sonar.kotlin.file.suffixes | latest-gh | Full Migration | Applied value=**.kt** to all projects |
| sonar.kotlin.file.suffixes | latest-gl | Full Migration | Applied value=**.kt** to all projects |
| sonar.kotlin.file.suffixes | latest-others | Full Migration | Applied value=**.kt** to all projects |
| sonar.kotlin.file.suffixes | latest-unbound | Full Migration | Applied value=**.kt** to all projects |
| sonar.java.checkstyle.reportPaths | latest-gh | Full Migration | Applied value=**target/checkstyle-result.xml,target/sonar/checkstyle-result.xml** to all projects |
| sonar.java.checkstyle.reportPaths | latest-gl | Full Migration | Applied value=**target/checkstyle-result.xml,target/sonar/checkstyle-result.xml** to all projects |
| sonar.java.checkstyle.reportPaths | latest-others | Full Migration | Applied value=**target/checkstyle-result.xml,target/sonar/checkstyle-result.xml** to all projects |
| sonar.java.checkstyle.reportPaths | latest-unbound | Full Migration | Applied value=**target/checkstyle-result.xml,target/sonar/checkstyle-result.xml** to all projects |
| sonar.javascript.globals | latest-gh | Full Migration | Applied value=**Backbone,OenLayers,_,angular,casper,d3,dijit,dojo,dojox,goog,google,moment,sap** to all projects |
| sonar.javascript.globals | latest-gl | Full Migration | Applied value=**Backbone,OenLayers,_,angular,casper,d3,dijit,dojo,dojox,goog,google,moment,sap** to all projects |
| sonar.javascript.globals | latest-others | Full Migration | Applied value=**Backbone,OenLayers,_,angular,casper,d3,dijit,dojo,dojox,goog,google,moment,sap** to all projects |
| sonar.javascript.globals | latest-unbound | Full Migration | Applied value=**Backbone,OenLayers,_,angular,casper,d3,dijit,dojo,dojox,goog,google,moment,sap** to all projects |
| sonar.text.inclusions | latest-gh | Full Migration | Applied value=****/*.bash,**/*.conf,**/*.config,**/*.ksh,**/*.pem,**/*.properties,**/*.ps1,**/*.sh,**/*.zsh,.aws/config,.env** to all projects (skipped: latest-gh_demo:github-actions-mono-dotnet (override)) |
| sonar.text.inclusions | latest-gl | Full Migration | Applied value=****/*.bash,**/*.conf,**/*.config,**/*.ksh,**/*.pem,**/*.properties,**/*.ps1,**/*.sh,**/*.zsh,.aws/config,.env** to all projects |
| sonar.text.inclusions | latest-others | Full Migration | Applied value=****/*.bash,**/*.conf,**/*.config,**/*.ksh,**/*.pem,**/*.properties,**/*.ps1,**/*.sh,**/*.zsh,.aws/config,.env** to all projects |
| sonar.text.inclusions | latest-unbound | Full Migration | Applied value=****/*.bash,**/*.conf,**/*.config,**/*.ksh,**/*.pem,**/*.properties,**/*.ps1,**/*.sh,**/*.zsh,.aws/config,.env** to all projects (skipped: latest-unbound_demo:secrets (override)) |
| sonar.androidLint.reportPaths | latest-gh | Full Migration | Applied value=**alint/**** to all projects |
| sonar.androidLint.reportPaths | latest-gl | Full Migration | Applied value=**alint/**** to all projects |
| sonar.androidLint.reportPaths | latest-others | Full Migration | Applied value=**alint/**** to all projects |
| sonar.androidLint.reportPaths | latest-unbound | Full Migration | Applied value=**alint/**** to all projects |
| sonar.python.xunit.reportPath | latest-gh | Full Migration | Applied value=**build//xunit-results*.xml** to all projects |
| sonar.python.xunit.reportPath | latest-gl | Full Migration | Applied value=**build//xunit-results*.xml** to all projects |
| sonar.python.xunit.reportPath | latest-others | Full Migration | Applied value=**build//xunit-results*.xml** to all projects |
| sonar.python.xunit.reportPath | latest-unbound | Full Migration | Applied value=**build//xunit-results*.xml** to all projects |
| sonar.coverage.jacoco.aggregateXmlReportPaths | latest-gh | Full Migration | Applied value=**someplace,some-other-place** to all projects |
| sonar.coverage.jacoco.aggregateXmlReportPaths | latest-gl | Full Migration | Applied value=**someplace,some-other-place** to all projects |
| sonar.coverage.jacoco.aggregateXmlReportPaths | latest-others | Full Migration | Applied value=**someplace,some-other-place** to all projects |
| sonar.coverage.jacoco.aggregateXmlReportPaths | latest-unbound | Full Migration | Applied value=**someplace,some-other-place** to all projects |
| sonar.azureresourcemanager.file.identifier | latest-gh | Full Migration | Applied value=**https://schema.management.azure.com/schemas/** to all projects |
| sonar.azureresourcemanager.file.identifier | latest-gl | Full Migration | Applied value=**https://schema.management.azure.com/schemas/** to all projects |
| sonar.azureresourcemanager.file.identifier | latest-others | Full Migration | Applied value=**https://schema.management.azure.com/schemas/** to all projects |
| sonar.java.file.suffixes | latest-gh | Full Migration | Applied value=**.jav,.java,.javax** to all projects |
| sonar.java.file.suffixes | latest-gl | Full Migration | Applied value=**.jav,.java,.javax** to all projects |
| sonar.java.file.suffixes | latest-others | Full Migration | Applied value=**.jav,.java,.javax** to all projects |
| sonar.dbcleaner.branchesToKeepWhenInactive | latest-gh | Full Migration | Combined 6 SonarQube Server branch patterns into a single SonarQube Cloud regex "(comma,branch\|develop\|main\|master\|release-.*\|trunk)" (target setting: sonar.branch.longLivedBranches.regex). |
| sonar.dbcleaner.branchesToKeepWhenInactive | latest-gl | Full Migration | Combined 6 SonarQube Server branch patterns into a single SonarQube Cloud regex "(comma,branch\|develop\|main\|master\|release-.*\|trunk)" (target setting: sonar.branch.longLivedBranches.regex). |
| sonar.dbcleaner.branchesToKeepWhenInactive | latest-others | Full Migration | Combined 6 SonarQube Server branch patterns into a single SonarQube Cloud regex "(comma,branch\|develop\|main\|master\|release-.*\|trunk)" (target setting: sonar.branch.longLivedBranches.regex). |
| sonar.dbcleaner.branchesToKeepWhenInactive | latest-unbound | Full Migration | Combined 6 SonarQube Server branch patterns into a single SonarQube Cloud regex "(comma,branch\|develop\|main\|master\|release-.*\|trunk)" (target setting: sonar.branch.longLivedBranches.regex). |
| sonar.java.ignoreUnnamedModuleForSplitPackage | latest-gh | Full Migration | Applied value=**false** to all projects |
| sonar.java.ignoreUnnamedModuleForSplitPackage | latest-gl | Full Migration | Applied value=**false** to all projects |
| sonar.java.ignoreUnnamedModuleForSplitPackage | latest-others | Full Migration | Applied value=**false** to all projects |
| sonar.java.ignoreUnnamedModuleForSplitPackage | latest-unbound | Full Migration | Applied value=**false** to all projects |
| sonar.java.enablePreview | latest-gh | Full Migration | Applied value=**false** to all projects |
| sonar.java.enablePreview | latest-gl | Full Migration | Applied value=**false** to all projects |
| sonar.java.enablePreview | latest-others | Full Migration | Applied value=**false** to all projects |
| sonar.java.enablePreview | latest-unbound | Full Migration | Applied value=**false** to all projects |
| sonar.javascript.environments | latest-gh | Full Migration | Applied value=**amd,applescript,atomtest,browser,commonjs,couch,embertest,flow,greasemonkey,jasmine,jest,jquery,meteor,mocha,mongo,nashorn,node,phantomjs,prototypejs,protractor,qunit,rhino,serviceworker,shared-node-browser,shelljs,webextensions,worker,wsh,yui** to all projects |
| sonar.javascript.environments | latest-gl | Full Migration | Applied value=**amd,applescript,atomtest,browser,commonjs,couch,embertest,flow,greasemonkey,jasmine,jest,jquery,meteor,mocha,mongo,nashorn,node,phantomjs,prototypejs,protractor,qunit,rhino,serviceworker,shared-node-browser,shelljs,webextensions,worker,wsh,yui** to all projects |
| sonar.javascript.environments | latest-others | Full Migration | Applied value=**amd,applescript,atomtest,browser,commonjs,couch,embertest,flow,greasemonkey,jasmine,jest,jquery,meteor,mocha,mongo,nashorn,node,phantomjs,prototypejs,protractor,qunit,rhino,serviceworker,shared-node-browser,shelljs,webextensions,worker,wsh,yui** to all projects |
| sonar.javascript.environments | latest-unbound | Full Migration | Applied value=**amd,applescript,atomtest,browser,commonjs,couch,embertest,flow,greasemonkey,jasmine,jest,jquery,meteor,mocha,mongo,nashorn,node,phantomjs,prototypejs,protractor,qunit,rhino,serviceworker,shared-node-browser,shelljs,webextensions,worker,wsh,yui** to all projects |
| sonar.abap.file.suffixes | latest-gh | Full Migration | Applied value=**.ab4,.abap,.asprog,.flow,.abapx** to all projects |
| sonar.abap.file.suffixes | latest-gl | Full Migration | Applied value=**.ab4,.abap,.asprog,.flow,.abapx** to all projects |
| sonar.abap.file.suffixes | latest-others | Full Migration | Applied value=**.ab4,.abap,.asprog,.flow,.abapx** to all projects |
| sonar.abap.file.suffixes | latest-unbound | Full Migration | Applied value=**.ab4,.abap,.asprog,.flow,.abapx** to all projects |
| sonar.vb.file.suffixes | latest-gh | Full Migration | Applied value=**.BAS,.CLS,.CTL,.FRM,.bas,.cls,.ctl,.frm** to all projects |
| sonar.vb.file.suffixes | latest-gl | Full Migration | Applied value=**.BAS,.CLS,.CTL,.FRM,.bas,.cls,.ctl,.frm** to all projects |
| sonar.vb.file.suffixes | latest-others | Full Migration | Applied value=**.BAS,.CLS,.CTL,.FRM,.bas,.cls,.ctl,.frm** to all projects |
| sonar.vb.file.suffixes | latest-unbound | Full Migration | Applied value=**.BAS,.CLS,.CTL,.FRM,.bas,.cls,.ctl,.frm** to all projects |
| sonar.css.file.suffixes | latest-gh | Full Migration | Applied value=**.css,.less,.scss** to all projects |
| sonar.css.file.suffixes | latest-gl | Full Migration | Applied value=**.css,.less,.scss** to all projects |
| sonar.css.file.suffixes | latest-others | Full Migration | Applied value=**.css,.less,.scss** to all projects |
| sonar.css.file.suffixes | latest-unbound | Full Migration | Applied value=**.css,.less,.scss** to all projects |
| sonar.test.exclusions | latest-gh | Full Migration | Applied value=**globaltestexclude*,superglobaltestexclude*,blah*.*** (merged from sonar.global.test.exclusions + sonar.test.exclusions) |
| sonar.test.exclusions | latest-gl | Full Migration | Applied value=**globaltestexclude*,superglobaltestexclude*,blah*.*** (merged from sonar.global.test.exclusions + sonar.test.exclusions) |
| sonar.test.exclusions | latest-others | Full Migration | Applied value=**globaltestexclude*,superglobaltestexclude*,blah*.*** (merged from sonar.global.test.exclusions + sonar.test.exclusions) |
| sonar.test.exclusions | latest-unbound | Full Migration | Applied value=**globaltestexclude*,superglobaltestexclude*,blah*.*** (merged from sonar.global.test.exclusions + sonar.test.exclusions) |
| newCodePeriod | latest-unbound | Full Migration | Applied (defaultLeakPeriodType=previous_version, defaultLeakPeriod=previous_version) |
| newCodePeriod | latest-gh | Full Migration | Applied (defaultLeakPeriodType=previous_version, defaultLeakPeriod=previous_version) |
| newCodePeriod | latest-gl | Full Migration | Applied (defaultLeakPeriodType=previous_version, defaultLeakPeriod=previous_version) |
| newCodePeriod | latest-others | Full Migration | Applied (defaultLeakPeriodType=previous_version, defaultLeakPeriod=previous_version) |
| sonar.ai.suggestions.enabled | latest-gh | Near Full Migration | OpenAI GPT-4o is not available on SonarQube Cloud; the LLM was changed to GPT-5.1. |
| sonar.ai.suggestions.enabled | latest-gl | Near Full Migration | OpenAI GPT-4o is not available on SonarQube Cloud; the LLM was changed to GPT-5.1. |
| sonar.ai.suggestions.enabled | latest-others | Near Full Migration | OpenAI GPT-4o is not available on SonarQube Cloud; the LLM was changed to GPT-5.1. |
| sonar.ai.suggestions.enabled | latest-unbound | Near Full Migration | OpenAI GPT-4o is not available on SonarQube Cloud; the LLM was changed to GPT-5.1. |
| sonar.azureresourcemanager.file.identifier | latest-unbound | Partial Migration | Applied value=**https://schema.management.azure.com/schemas/** to 63 of 64 projects (failed: latest-unbound_BANKING-PRIVATE-ASSETS) |
| sonar.java.file.suffixes | latest-unbound | Partial Migration | Applied value=**.jav,.java,.javax** to 62 of 63 projects (failed: latest-unbound_BANKING-RETAIL-WEB; skipped: latest-unbound_okorach-oss_sonar-tools (override)) |
| sonar.mcp.enabled |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.sca.featureEnabled |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.issues.sandbox.enabled |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.issues.sandbox.override.enabled |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.technicalDebt.ratingGrid |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.issues.sandbox.default |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.misracompliance.enabled |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.architecture.visualization.enabled |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.qualityProfiles.allowDisableInheritedRules |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.issues.sandbox.software-qualities |  | Skipped | Setting does not exist (feature does not exist or setting irrelevant) on SQC, no migration possible |
| sonar.auth.* |  | Skipped | Settings not migrated. Authentication must be redefined from scratch on SonarQube Cloud |
| sonar.vbnet.roslyn.vulnerabilityCategories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vbnet.roslyn.codeSmellCategories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.roslyn.codeSmellCategories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.githubactions.actionlint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ansible.ansible-lint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cloudformation.cfn-lint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.docker.hadolint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.yaml.spectral.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.terraform.tflint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.bandit.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.kotlin.detekt.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.eslint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.flake8.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.kotlin.ktlint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.mypy.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.phpstan.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.apex.pmd.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.java.pmd.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.psalm.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.pylint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ruby.rubocop.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.ruff.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.scala.scalastyle.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.scala.scapegoat.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.java.spotbugs.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.css.stylelint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.swift.swiftLint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.typescript.tslint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.flex.cobertura.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.flex.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.plugins.downloadOnlyRequired |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.report.partMaxSizeMBytes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cpd.cross_project |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.issues.defaultAssigneeLogin |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.projectCreation.mainBranchName |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.developerAggregatedInfo.disabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.jreAutoProvisioning.disabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.announcement.displayMessage |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.issues.issueResolution.global.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.issues.issueResolution.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ce.parallelProjectTasks |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.lf.enableGravatar |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.lf.gravatarServerUrl |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.qualitygate.ignoreSmallChanges |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.lf.logoUrl |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.lf.logoWidthPx |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.githubactions.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.tests.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.coverage.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.govet.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.golint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.gometalinter.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.go.golangci-lint.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.pdf.confidential.header.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.governance.report.project.branch.frequency |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.portfolios.recompute.hours |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.governance.report.view.frequency |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.governance.report.view.recipients |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dre.groovy.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.groovy.cobertura.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.groovy.file.patterns |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.groovy.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.hoursBeforeKeepingOnlyOneSnapshotByDay |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.daysBeforeDeletingInactiveBranchesAndPRs |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.branchesToKeepWhenInactive |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.weeksBeforeKeepingOnlyOneSnapshotByWeek |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.weeksBeforeKeepingOnlyOneSnapshotByMonth |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.weeksBeforeKeepingOnlyAnalysesWithVersion |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.weeksBeforeDeletingAllSnapshots |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.daysBeforeDeletingClosedIssues |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.daysBeforeDeletingAnticipatedTransitions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.daysBeforeDeletingScannerCache |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dbcleaner.auditHousekeeping |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.jsp.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.java.jvmframeworkconfig.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.java.jvmframeworkconfig.file.patterns |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.junit.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.createTSProgramForOrphanFiles |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.css.additional.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.disableTypeChecking |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.ecmaScriptVersion |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.html.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.ignoreHeaderComments |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.lcov.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.maxFileSize |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.scanner.skipNodeProvisioning |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.typescript.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.typescript.tsconfigPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.javascript.yaml.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.jcl.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.json.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.json.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.json.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.kubernetes.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.kubernetes.helm.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.maturity.allowSuperstrictQualityGates |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.maturity.maxCustomQualityGates |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.maturity.maxQualityGateConditions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.tasks.volume.threshold.value |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.tasks.age.threshold.value |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.tasks.volume.threshold.minutes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.tasks.age.threshold.minutes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.tasks.failure_rate.threshold.value |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.alerts.tasks.failure_rate.threshold.minutes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.coverage.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.frameworkDetection |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.php.tests.reportPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.pli.extralingualCharacters |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.pli.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.pli.ignoreHeaderComments |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.pli.marginLeft |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.pli.marginRight |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.plsql.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.plsql.ignoreHeaderComments |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dre.powershell.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.powershell.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.misra.hello |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.example.hello |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ipynb.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.version |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.coverage.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.coverage.reportPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.python.xunit.skipDetails |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rpg.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rpg.leftMarginWidth |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dre.ruby.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ruby.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ruby.coverage.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ruby.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.cargo.manifestPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.clippyReport.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.cobertura.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.clippy.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.clippy.offline |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.lcov.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.rust.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.security.config.roslyn.sonaranalyzer.security.cs |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.security.config.gosecurity |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.security.config.javasecurity |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.security.config.kotlinsecurity |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.security.config.phpsecurity |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.security.config.pythonsecurity |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.scala.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.scala.coverage.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.scm.disabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.text.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.text.inclusions.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.text.excluded.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.secrets.disableEntropyFilter |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.validateWebhooks |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.enforceAzureOpenAiDomainValidation |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.forceAuthentication |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.shell.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.shell.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.swift.coverage.reportPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.swift.coverage.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.swift.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.tsql.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.technicalDebt.developmentCost |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.terraform.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.terraform.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.terraform.provider.aws.version |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.terraform.provider.azure.version |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vbnet.analyzeGeneratedCode |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vbnet.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vbnet.ignoreHeaderComments |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vb.ignoreHeaderComments |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.xml.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.yaml.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.yaml.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.sca.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.sca.rescan.frequency |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.sca.rescan.branch_type |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.autodetect.ai.code |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.ansible.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dre.apex.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.apex.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.apex.coverage.reportPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| provisioning.gitlab.token.secured |  | Skipped | Setting is left to default on SQS, no migration needed |
| provisioning.github.project.visibility.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| provisioning.gitlab.enabled |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.azurepipelines.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.azureresourcemanager.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.azureresourcemanager.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.c.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cpp.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.objc.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.bullseye.reportPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.cobertura.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.cppunit.reportsPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.gcov.reportsPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.llvm-cov.reportPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.valgrind.reportsPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cfamily.vscoveragexml.reportsPath |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.analyzeGeneratedCode |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.analyzeRazorCode |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.ignoreHeaderComments |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cloudformation.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cloudformation.file.identifier |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.aucobol.preprocessor.directives.default |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.adaprep.activation |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.copy.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.copy.directories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.copy.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.byteBasedColumnCount |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.preprocessor.skipping.first.matching.characters |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.db2include.directories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.dialect |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.exec.recoveryMode |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cpd.cobol.ignoreLiteral |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.compilationConstants |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.sql.catalog.defaultSchema |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.sql.catalog.csv.path |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.sourceFormat |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cobol.tab.width |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dart.lcov.reportPaths |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.dart.file.suffixes |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.docker.activate |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.docker.file.patterns |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.global.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.global.test.exclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.inclusions |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vbnet.roslyn.ignoreIssues |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.roslyn.ignoreIssues |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.vbnet.roslyn.bugCategories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.roslyn.bugCategories |  | Skipped | Setting is left to default on SQS, no migration needed |
| sonar.cs.roslyn.vulnerabilityCategories |  | Skipped | Setting is left to default on SQS, no migration needed |

## Bottlenecks

### Phase Timings
| Phase | Tasks | Duration |
|:---|:---|:---|
| Phase 6 | 2 | 8m27.842s |
| Phase 5 | 5 | 4m42.743s |
| Phase 4 | 12 | 2m14.934s |
| Phase 3 | 14 | 52.401s |
| Phase 2 | 13 | 16.465s |
| Phase 1 | 7 | 10ms |

### Slowest Tasks
| Task | Phase | Duration | OK |
|:---|:---|:---|:---|
| syncIssueMetadata | 6 | 8m27.842s | Yes |
| importProjectData | 5 | 4m42.743s | Yes |
| setProjectGroupPermissions | 4 | 2m14.934s | Yes |
| setGlobalSettings | 3 | 52.4s | Yes |
| syncHotspotMetadata | 6 | 40.954s | Yes |
| configurePortfolios | 5 | 17.458s | Yes |
| createProjects | 2 | 16.464s | Yes |
| grantMigrationUserProjectPermissions | 3 | 15.899s | Yes |
| createProfiles | 2 | 11.522s | Yes |
| setProfileParent | 3 | 10.168s | Yes |
| setGlobalNewCodePeriod | 2 | 6.794s | Yes |
| restoreProfiles | 4 | 5.579s | Yes |
| setProjectSettings | 4 | 4.66s | Yes |
| addGateConditions | 4 | 3.821s | Yes |
| createGroups | 2 | 3.728s | Yes |
| setNewCodePeriods | 4 | 3.587s | Yes |
| setProjectLinks | 4 | 2.812s | Yes |
| createMigrationGroups | 2 | 2.411s | Yes |
| setDefaultProfiles | 5 | 2.241s | Yes |
| setTemplateGroupPermissions | 3 | 2.229s | Yes |
| addMigrationGroupToTemplates | 3 | 1.687s | Yes |
| setProjectGates | 4 | 1.516s | Yes |
| getMigrationUser | 2 | 1.485s | Yes |
| getProjectIds | 3 | 1.463s | Yes |
| updateRuleTags | 2 | 1.415s | Yes |
| updateRuleDescriptions | 2 | 1.405s | Yes |
| setProjectWebhooks | 4 | 1.269s | Yes |
| addMigrationUserToMigrationGroups | 3 | 1.202s | Yes |
| setProjectTags | 4 | 1.142s | Yes |
| createPortfolios | 3 | 1.085s | Yes |
| analyzeProfileRules | 3 | 1.003s | Yes |
| setProjectProfiles | 4 | 892ms | Yes |
| setDefaultTemplates | 3 | 877ms | Yes |
| getEnterprises | 2 | 867ms | Yes |
| setProfileGroupPermissions | 3 | 808ms | Yes |
| setGlobalWebhooks | 2 | 724ms | Yes |
| createGates | 2 | 697ms | Yes |
| createPermissionTemplates | 2 | 595ms | Yes |
| getOrgRepos | 2 | 414ms | Yes |
| getProfileBackups | 3 | 358ms | Yes |
| matchProjectRepos | 4 | 34ms | Yes |
| getGateConditions | 3 | 18ms | Yes |
| setOrgGroupPermissions | 3 | 11ms | Yes |
| generateProjectMappings | 1 | 8ms | Yes |
| generateGroupMappings | 1 | 7ms | Yes |
| generateProfileMappings | 1 | 7ms | Yes |
| setDefaultGates | 5 | 6ms | Yes |
| generateTemplateMappings | 1 | 6ms | Yes |
| generatePortfolioMappings | 1 | 5ms | Yes |
| generateOrganizationMappings | 1 | 5ms | Yes |
| generateGateMappings | 1 | 3ms | Yes |
| setProjectBinding | 5 | 3ms | Yes |
| setPortfolioProjects | 4 | 0s | Yes |

### Per-Branch CE
| Branch | Type | Status | Task Id |
|:---|:---|:---|:---|
| comma,branch | long | packaged | AZ8P7zo7nJDP_RSpCj5o |
| develop | long | packaged | AZ8P8XCDnJDP_RSpCj6I |
| feature/new-feature | long | packaged | AZ8P7kKnj1SK7a-5nF1v |
| fix-log4shell | long | packaged | AZ8P78G-j1SK7a-5nF2M |
| fix/ruff-import-lint | long | packaged | AZ8P8Hh4nJDP_RSpCj6C |
| main |  | packaged |  |
| master | long | packaged | AZ8P8TYUj1SK7a-5nF2Z |
| release-2.x | long | packaged | AZ8P74KHnJDP_RSpCj5x |
| release-3.2 | long | packaged | AZ8P7uBbnJDP_RSpCj5i |
| release-3.x | long | packaged | AZ8P8Y-YnJDP_RSpCj6L |
| some-branch | long | packaged | AZ8P7rGBnJDP_RSpCj5b |

## Warnings, Retries & Skips

### Gate Condition Skips
| Gate | Metric | Action | Note |
|:---|:---|:---|:---|
| ß test QG | new_software_quality_maintainability_rating | remapped | addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143) |
| ß test QG | new_software_quality_reliability_issues | remapped | addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143) |
| 0 - Corp Platinum | software_quality_blocker_issues | remapped | addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143) |
| ß test QG | new_software_quality_security_issues | remapped | addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143) |
| 0 - Corp Platinum | software_quality_reliability_rating | remapped | addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143) |
| 0 - Corp Platinum | software_quality_security_rating | remapped | addGateConditions: source metric remapped to SonarQube Cloud equivalent(s) (#143) |
| 0 - Corp Platinum | new_software_quality_reliability_remediation_effort | skipped | addGateConditions: source metric has no SonarQube Cloud equivalent — condition skipped (#143) |

### Metric Remaps
| Gate | Source Metric | Target Metric |
|:---|:---|:---|
| ß test QG | new_software_quality_maintainability_rating | new_maintainability_rating |
| ß test QG | new_software_quality_reliability_issues | new_reliability_rating |
| 0 - Corp Platinum | software_quality_blocker_issues | security_rating, reliability_rating |
| ß test QG | new_software_quality_security_issues | new_security_rating |
| 0 - Corp Platinum | software_quality_reliability_rating | reliability_rating |
| 0 - Corp Platinum | software_quality_security_rating | security_rating |

## Branch Project Data
| Branch | Type | Status | Issues | External Issues | Components | Active Rules | Zip Bytes | Task Id | Skip Reason |
|:---|:---|:---|:---|:---|:---|:---|:---|:---|:---|
| comma,branch | long | packaged | 6 | 0 | 1 | 435 | 4,977 | AZ8P7zo7nJDP_RSpCj5o |  |
| develop | long | packaged | 125 | 210 | 20 | 622 | 51,299 | AZ8P8XCDnJDP_RSpCj6I |  |
| feature/new-feature | long | packaged | 6 | 0 | 3 | 459 | 7,361 | AZ8P7kKnj1SK7a-5nF1v |  |
| fix-log4shell | long | packaged | 1 | 0 | 2 | 622 | 8,483 | AZ8P78G-j1SK7a-5nF2M |  |
| fix/ruff-import-lint | long | packaged | 105 | 580 | 128 | 514 | 848,331 | AZ8P8Hh4nJDP_RSpCj6C |  |
| main |  | packaged | 73 | 1 | 249 | 486 | 1,136,156 |  |  |
| master | long | packaged | 105 | 573 | 128 | 514 | 849,264 | AZ8P8TYUj1SK7a-5nF2Z |  |
| release-2.x | long | packaged | 6 | 0 | 1 | 435 | 4,978 | AZ8P74KHnJDP_RSpCj5x |  |
| release-3.2 | long | packaged | 74 | 0 | 14 | 622 | 17,366 | AZ8P7uBbnJDP_RSpCj5i |  |
| release-3.x | long | packaged | 79 | 1,462 | 96 | 486 | 718,940 | AZ8P8Y-YnJDP_RSpCj6L |  |
| some-branch | long | packaged | 6 | 0 | 3 | 459 | 7,361 | AZ8P7rGBnJDP_RSpCj5b |  |

## Migration Limitations

- Applications do not exist on SonarQube Cloud, 5 SQS applications were not migrated.
- SonarQube Cloud does not support the reference_branch or specific_analysis new-code-definition types; 1 project(s) were migrated with the SonarQube Cloud organization default instead.
- SonarQube Cloud does not support user permissions via API. The following 23 user(s) had global SonarQube Server permissions and were not migrated: Global-analyser-2026, TEMP-user-42746, TEMP-user-42846, TEMP-user-49113, TEMP-user-49644, TEMP-user-51113, TEMP-user-52404, TEMP-user-52445, TEMP-user-54234, TEMP-user-55689, TEMP-user-57961, TEMP-user-59264, TEMP-user-61203, TEMP-user-64044, admin, ado, bbTEMPaa, james, michal, olivier, olivier-k31581, olivier-korach22656, syncer.
- SonarQube Cloud does not support user permissions via API. The following 4 user(s) had permissions on SonarQube Server permission templates and were not migrated: james, michal, olivier, olivier-k31581.
- SonarQube Cloud has no per-branch new-code-definition concept; 3 branch-level new code definition(s) on SonarQube Server were not migrated.
- sonar.qualitygate.ignoreSmallChanges is set on SonarQube Server but has no /api/settings/set equivalent on SonarQube Cloud. Configure "Ignore duplication and coverage on small changes (org-level)" manually after migration.

