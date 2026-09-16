---
spec_id: SPEC-012
title: Clean Code Attribute Mapping
status: draft
priority: P1
epic: "Version Compatibility"
depends_on: [SPEC-002, SPEC-011]
estimated_effort: M
cloudvoyager_ref: "src/pipelines/sq-9.9/extract/extract-rules/, src/shared/utils/clean-code/"
---

# SPEC-012: Clean Code Attribute Mapping
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Clean Code taxonomy is a classification system introduced in SonarQube 10.0 that assigns every code issue a `cleanCodeAttribute` (one of 14 values describing the violated coding principle) and one or more `impacts` (each pairing a `softwareQuality` with an `impactSeverity`). SonarQube Cloud requires these attributes on every issue in submitted scanner reports. This creates a version-specific challenge: servers running SQ 10.0+ include Clean Code attributes natively in their API responses, but SQ 9.9 LTS predates the taxonomy entirely and returns neither `cleanCodeAttribute` nor `impacts`.

CloudVoyager solved this by enriching SQ 9.9 issues with Clean Code data fetched from SonarQube Cloud itself. For each rule encountered during extraction, CloudVoyager queries the SonarQube Cloud rules API to retrieve the canonical Clean Code classification, then applies it to every matching issue. This enrichment is critical because SonarQube Cloud's Compute Engine (CE) rejects scanner reports where issues lack Clean Code attributes, and it rejects them where those attributes are encoded as strings instead of protobuf enum integers.

This spec defines the Go implementation of the Clean Code enrichment pipeline for SQ 9.9, the direct extraction path for SQ 10.0+, the enum-to-integer mapping tables required for protobuf encoding, and the fallback logic for rules that cannot be resolved through any lookup.

## Problem Statement

SonarQube Cloud's CE expects every issue in a scanner report to carry a `cleanCodeAttribute` (encoded as a protobuf enum integer) and an `impacts` array (each with `softwareQuality` and `severity` as enum integers). SQ 9.9 LTS does not provide these fields. Without enrichment, scanner reports generated from SQ 9.9 data will be rejected by CE, making historical data migration impossible for the most widely deployed LTS version.

Additionally, even for SQ 10.0+ where Clean Code data is available natively, the data must be converted from string representations (as returned by the JSON API) to protobuf enum integers (as required by the scanner report format). Incorrect encoding — using the string "MAINTAINABILITY" instead of the integer 1 — causes silent CE rejection with no descriptive error message, making debugging extremely difficult.

## User Stories

- **As a** migration operator with a SQ 9.9 server, **I want to** have Clean Code attributes automatically enriched from SonarQube Cloud, **so that** my historical issues appear correctly in SonarQube Cloud with full Clean Code classification.
- **As a** migration operator with a SQ 10.4 server, **I want to** have native Clean Code attributes correctly encoded in scanner reports, **so that** CE processes them without rejection.
- **As a** migration operator, **I want to** receive clear warnings when rules cannot be enriched with Clean Code data, **so that** I can review the fallback defaults applied to those issues.
- **As a** developer maintaining the tool, **I want to** have a single source of truth for Clean Code enum mappings, **so that** adding new attributes or qualities does not require changes in multiple places.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Define Go constants for all 14 CleanCodeAttribute enum values with their protobuf integer mappings | Must |
| FR-2 | Define Go constants for all 3 SoftwareQuality enum values with their protobuf integer mappings | Must |
| FR-3 | Define Go constants for all ImpactSeverity enum values (UNKNOWN_IMPACT_SEVERITY, LOW, MEDIUM, HIGH, INFO, BLOCKER) with their protobuf integer mappings. Note: INFO and BLOCKER were introduced in later SQ versions; earlier versions (10.0-10.3) only support LOW, MEDIUM, HIGH. | Must |
| FR-4 | For SQ 9.9: build enrichment map by querying SC `/api/rules/search` with language filter | Must |
| FR-5 | For SQ 9.9: apply enrichment map to each extracted issue, keyed by `ruleKey` | Must |
| FR-6 | For SQ 9.9: implement fallback defaults when a rule is not found in SC (based on issue type and severity) | Must |
| FR-7 | For SQ 10.0+: extract `cleanCodeAttribute` and `impacts` directly from `/api/issues/search` response | Must |
| FR-8 | Encode `cleanCodeAttribute` as protobuf enum integer (not string) in scanner report | Must |
| FR-9 | Encode `softwareQuality` and `impactSeverity` as protobuf enum integers in scanner report | Must |
| FR-10 | Cache the enrichment map per migration run to avoid redundant SC API calls | Should |
| FR-11 | Log a warning for each rule that falls back to defaults, including the rule key and applied defaults | Should |
| FR-12 | Support incremental enrichment map building (fetch only languages present in the project, not all) | Should |
| FR-13 | Validate that all issues have non-zero Clean Code attributes before protobuf encoding | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Enrichment map build time | < 30 seconds for projects with up to 10 languages |
| NFR-2 | Enrichment application throughput | >= 10,000 issues/second (in-memory map lookup) |
| NFR-3 | Memory for enrichment map | < 50 MB for 20,000 rules |
| NFR-4 | Enum mapping correctness | 100% verified against SonarScanner source protobuf definitions |
| NFR-5 | Test coverage for enum encoding | Every enum value must have a round-trip test (encode then decode) |

## Technical Design

### Architecture

The Clean Code system spans two packages:

```
go/internal/cleancode/
├── attributes.go       # Enum definitions and string-to-int mapping tables
├── attributes_test.go  # Round-trip encoding tests for every enum value
├── enrichment.go       # SQ 9.9 enrichment: build map from SC, apply to issues
├── enrichment_test.go  # Enrichment tests with recorded SC API responses
├── fallback.go         # Fallback defaults for unresolved rules
└── fallback_test.go    # Fallback logic tests

go/internal/scanreport/
├── encoder.go          # Modified to use cleancode.AttributeToProto() for encoding
└── ...
```

### Key Algorithms

#### Enum Mapping Tables

```go
package cleancode

// CleanCodeAttribute maps API string values to protobuf enum integers.
// Integer assignments MUST be copied from `go/internal/scanreport/proto/scanner-report.proto`.
// The actual values from the codebase are shown below.
type CleanCodeAttribute int32

const (
    CLEAN_CODE_ATTRIBUTE_UNSPECIFIED CleanCodeAttribute = 0
    CONVENTIONAL  CleanCodeAttribute = 1
    FORMATTED     CleanCodeAttribute = 2
    IDENTIFIABLE  CleanCodeAttribute = 3
    CLEAR         CleanCodeAttribute = 4
    COMPLETE      CleanCodeAttribute = 5
    EFFICIENT     CleanCodeAttribute = 6
    LOGICAL       CleanCodeAttribute = 7
    DISTINCT      CleanCodeAttribute = 8
    FOCUSED       CleanCodeAttribute = 9
    MODULAR       CleanCodeAttribute = 10
    TESTED        CleanCodeAttribute = 11
    LAWFUL        CleanCodeAttribute = 12
    RESPECTFUL    CleanCodeAttribute = 13
    TRUSTWORTHY   CleanCodeAttribute = 14
)

// attributeFromString parses the API string representation to enum.
var attributeFromString = map[string]CleanCodeAttribute{
    "CONVENTIONAL": CONVENTIONAL,
    "FORMATTED":    FORMATTED,
    "IDENTIFIABLE": IDENTIFIABLE,
    "CLEAR":        CLEAR,
    "COMPLETE":     COMPLETE,
    "EFFICIENT":    EFFICIENT,
    "LOGICAL":      LOGICAL,
    "DISTINCT":     DISTINCT,
    "FOCUSED":      FOCUSED,
    "MODULAR":      MODULAR,
    "TESTED":       TESTED,
    "LAWFUL":       LAWFUL,
    "RESPECTFUL":   RESPECTFUL,
    "TRUSTWORTHY":  TRUSTWORTHY,
}

type SoftwareQuality int32

const (
    MAINTAINABILITY SoftwareQuality = 1
    RELIABILITY     SoftwareQuality = 2
    SECURITY        SoftwareQuality = 3
)

// ImpactSeverity enum values. The 5-level impact severity (LOW, MEDIUM, HIGH,
// INFO, BLOCKER) was introduced in later SQ versions. Earlier versions (10.0-10.3)
// only support LOW, MEDIUM, HIGH. Actual proto values from scanner-report.proto:
type ImpactSeverity int32

const (
    UNKNOWN_IMPACT_SEVERITY ImpactSeverity = 0
    LOW                     ImpactSeverity = 1
    MEDIUM                  ImpactSeverity = 2
    HIGH                    ImpactSeverity = 3
    INFO                    ImpactSeverity = 4
    BLOCKER                 ImpactSeverity = 5
)
```

#### SQ 9.9 Enrichment Map Building

```
FUNCTION BuildEnrichmentMap(scClient, languages):
    enrichmentMap = map[string]CleanCodeInfo{}
    
    FOR EACH lang IN languages:
        page = 1
        LOOP:
            response = scClient.GET /api/rules/search?languages={lang}&ps=500&p={page}
            FOR EACH rule IN response.rules:
                key = rule.key   // e.g., "java:S1234"
                info = CleanCodeInfo{
                    Attribute: parseAttribute(rule.cleanCodeAttribute),
                    Impacts:   parseImpacts(rule.impacts),
                }
                enrichmentMap[key] = info
            
            IF page * 500 >= response.total:
                BREAK
            page++
    
    RETURN enrichmentMap
```

#### Enrichment Application

```
FUNCTION EnrichIssues(issues, enrichmentMap):
    enriched = 0
    fallbacks = 0
    
    FOR EACH issue IN issues:
        IF info, ok = enrichmentMap[issue.RuleKey]; ok:
            issue.CleanCodeAttribute = info.Attribute
            issue.Impacts = info.Impacts
            enriched++
        ELSE:
            // Fallback based on legacy type and severity
            issue.CleanCodeAttribute = fallbackAttribute(issue.Type)
            issue.Impacts = fallbackImpacts(issue.Type, issue.Severity)
            fallbacks++
            log.Warn("Rule %s not found in SC, applied fallback defaults", issue.RuleKey)
    
    log.Info("Enriched %d issues, %d used fallback defaults", enriched, fallbacks)
    RETURN issues
```

#### Fallback Defaults

CloudVoyager uses the following fallback mapping when a rule cannot be found in SonarQube Cloud:

| Legacy Type | Default CleanCodeAttribute | Default SoftwareQuality | Severity Mapping |
|-------------|---------------------------|------------------------|-----------------|
| BUG | LOGICAL | RELIABILITY | Maps from legacy severity |
| VULNERABILITY | TRUSTWORTHY | SECURITY | Maps from legacy severity |
| CODE_SMELL | CLEAR | MAINTAINABILITY | Maps from legacy severity |
| ~~SECURITY_HOTSPOT~~ | ~~COMPLETE~~ | ~~SECURITY~~ | ~~MEDIUM (always)~~ |

> **Note:** SECURITY_HOTSPOT has been removed from this fallback table. Security hotspots are extracted via `/api/hotspots/search` (SPEC-003), not `/api/issues/search`. They should not appear in the issue Clean Code enrichment pipeline.

Legacy severity to impact severity mapping:
| Legacy Severity | Impact Severity |
|----------------|----------------|
| BLOCKER | HIGH |
| CRITICAL | HIGH |
| MAJOR | MEDIUM |
| MINOR | LOW |
| INFO | INFO |

### Data Flow

1. **SQ 9.9 Path**: Extract issues from SQ Server (no Clean Code data) -> Identify unique languages from extracted issues -> Build enrichment map from SC rules API -> Apply enrichment map to issues -> Encode with protobuf enum integers
2. **SQ 10.0+ Path**: Extract issues from SQ Server (includes Clean Code data as strings) -> Convert string representations to enum integers -> Encode in protobuf
3. **Protobuf Encoding**: Both paths converge at the scanner report encoder, which uses `CleanCodeAttribute.ToProto()` to produce the integer value that CE expects

### API Dependencies

| Endpoint | Method | Purpose | Used By |
|----------|--------|---------|---------|
| `/api/rules/search` | GET | Fetch rule Clean Code attributes from SC | SQ 9.9 enrichment |
| `/api/issues/search` | GET | Extract issues (includes Clean Code on 10.0+) | SQ 10.0+ direct extraction |

## Acceptance Criteria

- [ ] AC-1: All 14 CleanCodeAttribute enum values are defined with correct protobuf integer mappings matching scanner-report.proto
- [ ] AC-2: All 3 SoftwareQuality enum values are defined with correct protobuf integer mappings
- [ ] AC-3: All 6 ImpactSeverity enum values (UNKNOWN_IMPACT_SEVERITY=0, LOW=1, MEDIUM=2, HIGH=3, INFO=4, BLOCKER=5) are defined with correct protobuf integer mappings
- [ ] AC-4: SQ 9.9 enrichment map is built by querying SC `/api/rules/search` for each language present in the project
- [ ] AC-5: SQ 9.9 enrichment correctly applies Clean Code attributes to issues, verified by decoding the resulting protobuf
- [ ] AC-6: Fallback defaults are applied for rules not found in SC, with a warning log per rule
- [ ] AC-7: SQ 10.0+ issues have their string-valued Clean Code attributes correctly converted to protobuf enum integers
- [ ] AC-8: A scanner report with enriched Clean Code attributes from SQ 9.9 is accepted by SonarQube Cloud CE (integration test)
- [ ] AC-9: A scanner report with native Clean Code attributes from SQ 10.4 is accepted by SonarQube Cloud CE (integration test)
- [ ] AC-10: Encoding a CleanCodeAttribute as a string (instead of integer) causes a validation error before the report is submitted
- [ ] AC-11: Enrichment map building respects API pagination (projects with 500+ rules per language are fully fetched)
- [ ] AC-12: Enrichment map is cached per migration run (building it twice for the same project returns the cached version)

## CloudVoyager Reference

| Area | Path |
|------|------|
| Clean Code enrichment for SQ 9.9 | `src/pipelines/sq-9.9/extract/extract-rules/` |
| Clean Code utility functions | `src/shared/utils/clean-code/` |
| Attribute constants | `src/shared/constants/clean-code-attributes.js` |
| Impact severity constants | `src/shared/constants/impact-severities.js` |
| Fallback mapping | `src/shared/utils/clean-code/fallback-defaults.js` |
| Protobuf encoding | `src/pipelines/shared/build/encode-issues.js` |

## Known Limitations

- The enrichment map approach requires a functioning SonarQube Cloud connection during SQ 9.9 migration. If the SC API is unreachable, all issues will use fallback defaults, which are less accurate than the actual rule classifications.
- The 14 CleanCodeAttribute values are hardcoded. If SonarSource adds new attributes in a future release, the enum mapping table must be updated. The protobuf schema must also be updated to match.
- Fallback defaults are an approximation. A CODE_SMELL defaulting to CLEAR/MAINTAINABILITY is reasonable for most cases but incorrect for rules that actually target RELIABILITY or SECURITY qualities. The operator should review the fallback warning log.
- The enrichment map can be large for organizations with many custom rules. Memory usage scales linearly with total rule count across all languages.
- Language detection depends on the `languages` field in the issue search response. If SQ 9.9 returns issues without a language (e.g., project-level issues), those rules cannot be enriched by language filter and will fall back to defaults.

## Sonar Documentation Reference

For full Sonar product documentation, see: https://docs.sonarsource.com/llms.txt

## Open Questions

- Q1: Should the tool allow operators to provide a custom fallback mapping file (e.g., JSON) to override the built-in defaults for specific rules?
- Q2: Should the enrichment map be persisted to disk between migration runs, so that re-running a migration for additional projects does not re-fetch all rules from SC?
- Q3: Are there CleanCodeAttribute values beyond the current 14 planned for near-term SonarQube releases? If so, the enum table should be designed for easy extension.
- Q4: For SQ 10.0-10.3, are there edge cases where `cleanCodeAttribute` is present on the issue but `impacts` is empty? CloudVoyager does not document this scenario.
