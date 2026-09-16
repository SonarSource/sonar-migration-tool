---
spec_id: SPEC-013
title: External Issues & Ad-Hoc Rules
status: draft
priority: P1
epic: "Version Compatibility"
depends_on: [SPEC-001, SPEC-002]
estimated_effort: L
cloudvoyager_ref: "src/pipelines/*/build/helpers/external-issues/, src/shared/utils/rule-repositories/"
---

# SPEC-013: External Issues & Ad-Hoc Rules
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

SonarQube Server supports a rich ecosystem of third-party analysis plugins (FindBugs, SpotBugs, PMD, Checkstyle, ESLint plugin imports, and many others) that produce issues under their own rule repositories. These issues are fully native in SonarQube Server but have no corresponding rules in SonarQube Cloud, which only recognizes its own built-in rule engines. When migrating historical data, these third-party issues must be classified as "external issues" and encoded in a separate protobuf file format (`externalissues-{ref}.pb`) alongside companion "ad-hoc rule" definitions (`adhocrules.pb`) that tell SonarQube Cloud how to display them.

CloudVoyager solves this by maintaining a registry of SonarQube Cloud's known native rule repositories (43 at last count). Any issue whose `ruleRepository` is not in this registry is classified as external. External issues use the `ExternalIssue` protobuf message (distinct from the regular `Issue` message) and reference ad-hoc rule definitions that carry the rule's display name, description, severity, type, and Clean Code attribute. The ad-hoc rules are serialized in `adhocrules.pb` using length-delimited encoding, and the Clean Code attribute must be encoded as a protobuf enum integer — not a string — matching the same requirement documented in SPEC-012.

This spec defines the Go implementation for detecting external issues, encoding them in the correct protobuf format, generating companion ad-hoc rule definitions, and maintaining the SonarQube Cloud native repository registry.

## Problem Statement

Organizations running SonarQube Server commonly use third-party plugins that add hundreds or thousands of rules not present in SonarQube Cloud. When migrating, these issues cannot be encoded as regular issues because CE would reject rules it does not recognize. Without external issue encoding and ad-hoc rule definitions, all third-party plugin issues would be silently lost during migration, potentially discarding years of findings from tools like FindBugs, PMD, Checkstyle, and others that represent significant investment in code quality analysis.

The current sonar-migration-tool's `types.Issue` struct (at `lib/sq-api-go/types/issues.go`) does not distinguish between native and external issues, nor does the report builder have support for the `ExternalIssue` protobuf message or `adhocrules.pb` generation.

## User Stories

- **As a** migration operator with FindBugs/SpotBugs issues on SQ Server, **I want to** have those issues appear as external issues in SonarQube Cloud, **so that** my team retains visibility into findings from third-party analyzers.
- **As a** migration operator, **I want to** see which issues were classified as external vs native during migration, **so that** I can verify the classification is correct.
- **As a** migration operator with custom SonarQube plugins, **I want to** have issues from those plugins migrated with their rule metadata, **so that** they display correctly in the SonarQube Cloud UI.
- **As a** developer extending the tool, **I want to** easily add new SonarQube Cloud native repositories as they are released, **so that** newly supported languages do not get misclassified as external.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Maintain a registry of SonarQube Cloud native rule repositories (currently 43+) | Must |
| FR-2 | Classify issues by comparing `ruleRepository` against the native registry | Must |
| FR-3 | Attempt dynamic registry refresh via SC `/api/rules/search` at migration start | Should |
| FR-4 | Fall back to the built-in hardcoded registry when SC API is unreachable | Must |
| FR-5 | Encode external issues in `externalissues-{ref}.pb` using the `ExternalIssue` protobuf message | Must |
| FR-6 | Populate `ExternalIssue` fields: `engineId`, `ruleId`, `message`, `severity`, `type`, `textRange` | Must |
| FR-7 | Generate ad-hoc rule definitions for each unique external rule encountered | Must |
| FR-8 | Encode ad-hoc rules in `adhocrules.pb` using length-delimited encoding | Must |
| FR-9 | Populate ad-hoc rule fields: `engineId`, `ruleId`, `name`, `description`, `severity`, `type`, `cleanCodeAttribute` | Must |
| FR-10 | Encode `cleanCodeAttribute` in ad-hoc rules as protobuf enum integer (SPEC-012 dependency) | Must |
| FR-11 | Deduplicate ad-hoc rules: one definition per unique (engineId, ruleId) pair regardless of how many issues reference it | Must |
| FR-12 | Log classification summary: N native issues, M external issues, K unique external rules | Should |
| FR-13 | Support `--skip-external-issues` flag (default: false) to allow skipping external issue migration. Uses idiomatic Go CLI convention (positive action flags rather than inverted booleans). | Should |
| FR-14 | Handle rules that exist in multiple repositories (e.g., `squid:S1234` vs `java:S1234`) without duplication | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Classification throughput | >= 50,000 issues/second (in-memory set lookup) |
| NFR-2 | Registry size | < 1 KB in compiled binary (slice of strings) |
| NFR-3 | Ad-hoc rule deduplication memory | O(unique_rules), not O(total_issues) |
| NFR-4 | Protobuf encoding correctness | 100% acceptance rate by CE for generated external issue + ad-hoc rule files |
| NFR-5 | Built-in registry update cadence | Reviewed and updated with each sonar-migration-tool release |

## Technical Design

### Architecture

```
go/internal/external/
├── registry.go          # Native repository registry (hardcoded + dynamic refresh)
├── registry_test.go     # Registry lookup and refresh tests
├── classifier.go        # Issue classifier: native vs external
├── classifier_test.go   # Classification tests
├── encoder.go           # ExternalIssue + AdHocRule protobuf encoding
├── encoder_test.go      # Encoding correctness tests
└── testdata/
    ├── mixed_issues.json       # Test fixture with native + external issues
    └── adhoc_rules_golden.pb   # Golden file for ad-hoc rule encoding
```

### Key Algorithms

#### Native Repository Registry

```go
package external

// nativeRepositories is the built-in list of SonarQube Cloud's known
// rule repositories. Issues from these repositories are encoded as
// regular issues. Issues from any other repository are external.
//
// This list is derived from CloudVoyager's hardcoded registry and
// should be updated with each release.
var nativeRepositories = map[string]bool{
    // Core language analyzers
    "java": true, "javascript": true, "typescript": true,
    "python": true, "go": true, "kotlin": true,
    "ruby": true, "php": true, "csharp": true,
    "vbnet": true, "cpp": true, "c": true,
    "objc": true, "swift": true, "scala": true,
    
    // Markup and web
    "xml": true, "web": true, "css": true,
    "html": true, "flex": true,
    
    // Enterprise languages
    "abap": true, "cobol": true, "plsql": true,
    "tsql": true, "rpg": true, "pli": true,
    "vb6": true, "apex": true,
    
    // IaC and configuration
    "terraform": true, "cloudformation": true,
    "docker": true, "kubernetes": true,
    "azureresourcemanager": true,
    // NOTE: chef, puppet, and ansible removed — SonarQube Cloud does NOT
    // have native analyzers for these. They should be treated as external.
    
    // Additional built-in engines
    "common-java": true, "common-js": true,
    "common-ts": true, "common-web": true,
    // NOTE: common-py and common-cs removed pending verification —
    // confirm these exist as native SC repositories before re-adding.
    "csharpsquid": true, "jssecurity": true,
    "phpsecurity": true, "pythonsecurity": true,
    "javasecurity": true, "roslyn.sonaranalyzer.security.cs": true,
}
```

#### Issue Classification

```
FUNCTION ClassifyIssues(issues, registry):
    native = []
    external = []
    externalRules = map[string]AdHocRule{}   // keyed by "engineId:ruleId"
    
    FOR EACH issue IN issues:
        repo = extractRepository(issue.Rule)   // "java:S1234" → "java"
        ruleId = extractRuleId(issue.Rule)     // "java:S1234" → "S1234"
        
        IF registry.IsNative(repo):
            native = append(native, issue)
        ELSE:
            extIssue = ExternalIssue{
                EngineId:  repo,
                RuleId:    ruleId,
                Message:   issue.Message,
                Severity:  mapSeverity(issue.Severity),
                Type:      mapType(issue.Type),
                TextRange: convertTextRange(issue.TextRange),
            }
            external = append(external, extIssue)
            
            // Collect unique ad-hoc rule definitions
            ruleKey = repo + ":" + ruleId
            IF _, exists = externalRules[ruleKey]; !exists:
                externalRules[ruleKey] = AdHocRule{
                    EngineId:           repo,
                    RuleId:             ruleId,
                    Name:               issue.Rule,
                    // Ad-hoc rule descriptions should use a generic description
                    // rather than the issue-specific message, which contains
                    // code-level details (variable names, etc.) that don't belong
                    // in a rule description.
                    Description:        fmt.Sprintf("Rule from %s plugin", repo),
                    Severity:           mapSeverity(issue.Severity),
                    Type:               mapType(issue.Type),
                    CleanCodeAttribute: resolveCleanCodeAttribute(issue),
                }
    
    log.Info("Classified %d native, %d external issues (%d unique external rules)",
        len(native), len(external), len(externalRules))
    
    RETURN native, external, externalRules
```

#### Ad-Hoc Rule Protobuf Encoding

```
FUNCTION EncodeAdHocRules(rules map[string]AdHocRule) []byte:
    buf = new bytes.Buffer
    
    FOR EACH ruleKey IN sortedKeys(rules):   // Deterministic order
        rule = rules[ruleKey]
        msg = &pb.AdHocRule{
            EngineId:           rule.EngineId,
            RuleId:             rule.RuleId,
            Name:               rule.Name,
            Description:        rule.Description,
            Severity:           pb.Severity(rule.Severity),
            Type:               pb.IssueType(rule.Type),
            CleanCodeAttribute: pb.CleanCodeAttribute(rule.CleanCodeAttribute),
            // CRITICAL: CleanCodeAttribute is an enum integer, NOT a string
        }
        
        // Length-delimited encoding: write size prefix then message bytes
        data, _ = proto.Marshal(msg)
        writeVarint(buf, len(data))
        buf.Write(data)
    
    RETURN buf.Bytes()
```

#### Dynamic Registry Refresh

```
FUNCTION RefreshRegistry(scClient, builtInRegistry):
    TRY:
        repos = set()
        page = 1
        LOOP:
            resp = scClient.GET /api/rules/search?ps=500&p={page}&fields=repo
            FOR EACH rule IN resp.rules:
                repos.add(rule.repo)
            IF page * 500 >= resp.total:
                BREAK
            page++
        
        log.Info("Refreshed native registry: %d repositories from SC", len(repos))
        RETURN repos
    CATCH:
        log.Warn("Failed to refresh native registry from SC, using built-in (%d repos)", len(builtInRegistry))
        RETURN builtInRegistry
```

### Data Flow

1. **Migration Start**: Attempt to refresh the native repository registry from SC API. Fall back to built-in if unreachable.
2. **Issue Extraction**: Extract all issues from SQ Server (all issues treated uniformly at this stage).
3. **Classification**: Run `ClassifyIssues()` to split into native and external issue sets. Collect unique ad-hoc rule definitions.
4. **Clean Code Enrichment**: Apply SPEC-012 enrichment to both native and external issues (ad-hoc rules need `cleanCodeAttribute` too).
5. **Protobuf Encoding**: 
   - Native issues → `issues-{ref}.pb` (regular Issue message, length-delimited)
   - External issues → `externalissues-{ref}.pb` (ExternalIssue message, length-delimited)
   - Ad-hoc rules → `adhocrules.pb` (AdHocRule message, length-delimited)
6. **Archive Assembly**: All three file types are included in the ZIP archive for CE submission.

### API Dependencies

| Endpoint | Method | Purpose | When |
|----------|--------|---------|------|
| `/api/rules/search` | GET | Dynamic registry refresh from SC | Migration start (optional) |
| `/api/issues/search` | GET | Extract issues (classification happens post-extraction) | Extraction phase |

## Acceptance Criteria

- [ ] AC-1: Built-in native repository registry contains at least 43 repositories matching CloudVoyager's list
- [ ] AC-2: Issues from built-in repositories (e.g., `java:S1234`) are classified as native
- [ ] AC-3: Issues from third-party repositories (e.g., `findbugs:DMI_BIGDECIMAL_CONSTRUCTED_FROM_DOUBLE`) are classified as external
- [ ] AC-4: External issues are encoded in `externalissues-{ref}.pb` with correct ExternalIssue protobuf message structure
- [ ] AC-5: Ad-hoc rules are encoded in `adhocrules.pb` with length-delimited encoding
- [ ] AC-6: Ad-hoc rules are deduplicated: 100 issues from the same rule produce exactly 1 ad-hoc rule definition
- [ ] AC-7: `cleanCodeAttribute` in ad-hoc rules is encoded as protobuf enum integer, verified by decoding
- [ ] AC-8: Dynamic registry refresh from SC correctly augments the built-in registry
- [ ] AC-9: Dynamic registry refresh failure falls back to built-in registry with a warning log
- [ ] AC-10: Scanner report ZIP containing external issues + ad-hoc rules is accepted by SonarQube Cloud CE (integration test)
- [ ] AC-11: Classification summary is logged: "Classified N native, M external issues (K unique external rules)"
- [ ] AC-12: `--skip-external-issues` flag causes external issues and ad-hoc rules to be omitted from the report
- [ ] AC-13: Rule key parsing correctly handles colon-separated format: `repository:rule` where repository may itself contain dots (e.g., `roslyn.sonaranalyzer.security.cs:S5344`)

## CloudVoyager Reference

| Area | Path |
|------|------|
| External issue detection | `src/pipelines/*/build/helpers/external-issues/` |
| Rule repository registry | `src/shared/utils/rule-repositories/` |
| Native repository list | `src/shared/utils/rule-repositories/known-repositories.js` |
| Ad-hoc rule encoding | `src/pipelines/shared/build/encode-adhoc-rules.js` |
| External issue encoding | `src/pipelines/shared/build/encode-external-issues.js` |
| Rule key parsing | `src/shared/utils/rule-key-parser.js` |

## Known Limitations

- The built-in native repository registry is a point-in-time snapshot. SonarSource may add new language analyzers to SonarQube Cloud (and thus new native repositories) between tool releases. The dynamic refresh mechanism mitigates this, but if SC is unreachable, newly added native repositories may be misclassified as external.
- Ad-hoc rule descriptions use a generic format ("Rule from {engineId} plugin") rather than the issue-specific message, which contains code-level details (variable names, etc.) that do not belong in a rule description. SQ Server's `/api/rules/show` endpoint could provide better descriptions but would require an additional API call per unique external rule. This is a potential enhancement for a future iteration.
- External issues in SonarQube Cloud have limited UI capabilities compared to native issues (no code highlighting, no quickfix suggestions, reduced filtering options). This is a SonarQube Cloud platform limitation, not a tool limitation.
- The rule key parsing assumes the `repository:rule` format with the first colon as the delimiter. Edge cases where rule IDs contain colons (rare but possible with some plugins) may require special handling.
- Rules that were deprecated or removed from SQ Server but still have historical issues will use fallback Clean Code defaults since neither SC nor the server can provide current metadata for them.

## Sonar Documentation Reference

For full Sonar product documentation, see: https://docs.sonarsource.com/llms.txt

## Open Questions

- Q1: Should the tool attempt to fetch rule descriptions from SQ Server's `/api/rules/show` for each unique external rule to provide better ad-hoc rule descriptions? This would add API calls but improve the quality of external issue display in SC.
- Q2: Should there be a user-configurable file for custom repository classifications (e.g., marking a custom plugin's repository as "native" to avoid external encoding)?
- Q3: How should the tool handle the case where a rule repository exists in both SQ Server and SC but with different rule keys (e.g., `squid:S1234` on old SQ Server vs `java:S1234` on SC)?
- Q4: Should the tool produce a separate report file listing all external rules encountered, to help operators understand what third-party analysis data was migrated?
