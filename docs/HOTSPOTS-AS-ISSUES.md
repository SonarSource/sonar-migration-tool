# Hotspots as Issues (SonarQube Cloud, July 2026 onward)

<!-- updated: 2026-07-27_00:00:00 -->

Design + findings record for GitHub issue
[#423](https://github.com/SonarSource/sonar-migration-tool/issues/423) —
*"Adjust migration tool to the fact that SQC drops hotspots starting July 1st, 2026"*.

This document is written **before** the implementation so the research survives
process restarts. Sections marked `TODO:` are open at the time of writing.

## 1. What changed on SonarQube Cloud

<!-- updated: 2026-07-27_00:00:00 -->

As of **July 1st, 2026**, SonarQube Cloud no longer has Security Hotspots as a
distinct finding kind. The former hotspot **rules were converted in place** into
ordinary issue rules. SonarQube **Server** (source, 2026.3.1) still has hotspots.

Live-probed against the staging target `https://sc-staging.io`, org
`open-digital-society-1`, on 2026-07-27 via `/api/rules/show`:

| rule | `type` | `cleanCodeAttribute` | `impacts` | `sysTags` | `status` |
|---|---|---|---|---|---|
| `python:S4502` | `VULNERABILITY` | `CONVENTIONAL` | SECURITY / HIGH | cwe, django, flask, **former-hotspot** | READY |
| `python:S2245` | `VULNERABILITY` | `TRUSTWORTHY` | SECURITY / MEDIUM | cwe, **former-hotspot** | READY |
| `java:S2245` | `VULNERABILITY` | `TRUSTWORTHY` | SECURITY / MEDIUM | cwe, **former-hotspot** | READY |
| `javascript:S2245` | `VULNERABILITY` | `TRUSTWORTHY` | SECURITY / MEDIUM | cwe, **former-hotspot** | READY |
| `python:S1313` | `CODE_SMELL` | `TRUSTWORTHY` | SECURITY / LOW | bad-practice, **former-hotspot** | READY |
| `python:S4823` | `SECURITY_HOTSPOT` | (none) | (empty) | (none) | **REMOVED** |

Conclusions:

1. Converted rules carry the SonarSource **system** tag `former-hotspot`. That is
   SonarSource's marker on the *rule*, not ours, and it is not per-issue.
   Issue #423 asks for `sqs-hotspot` on the migrated **issues** — a
   migration-tool marker meaning "this was a Security Hotspot on SonarQube
   Server". The two are complementary, not the same thing.
2. The converted type is **not uniform**: mostly `VULNERABILITY`, sometimes
   `CODE_SMELL`. The tool must therefore *not* hardcode a type — and luckily it
   cannot (see §3).
3. Some old hotspot rules are `status: REMOVED` on Cloud rather than converted.
   Findings on those rules are **unmigratable** — the rule does not exist on the
   target. This is a real, unavoidable data-loss case that must be reported, not
   silently dropped.
4. `/api/hotspots/search` still *responds* on Cloud (HTTP 400 demanding
   `projectKey` rather than 404), but it returns no hotspots for migrated
   projects, because nothing is a hotspot any more. Any migration logic that
   verifies or syncs through `/api/hotspots/*` is now dead weight.
5. `/api/issues/tags` for the org already lists `former-hotspot` and
   `metadata-synchronized`; `sqs-hotspot` returns 0 issues today (baseline).

## 2. Why hotspots vanish on the target today

<!-- updated: 2026-07-27_00:00:00 -->

Hotspots were *already* being emitted into the scanner report as plain issues —
this is forced by the protobuf schema (§3). The regression is in the
**active-rule contract**, and it is the same class of bug the sibling issues
#456 and #474 hit:

- `go/internal/migrate/tasks_projectdata.go` drops native issues whose rule is
  not in the surviving active-rule set (`dropIssuesWithInactiveRules`), because
  the Compute Engine aborts the whole report on an issue referencing a rule that
  is not activated in the analysis.
- Hotspot-derived issues are appended to the issue list **after** that filter,
  deliberately bypassing it. The in-code comment justifies this with *"they are
  validated against hotspot rules, not the active-rule set"* — which was true
  while Cloud had hotspots and is **false as of 2026-07-01**.

So a hotspot-derived issue now either gets dropped by the CE or poisons the whole
report, depending on whether its rule happens to be activated. Either way the
finding does not land. Fixing this means hotspot rules must participate in
`activerules.pb` like any other issue rule, and hotspot-derived issues must be
subject to the same orphan-rule filter.

`TODO:` confirm empirically which of "silently dropped" vs "report rejected"
happens, from the before-run CE task log.

## 3. Protobuf constraints (why we cannot set a type)

<!-- updated: 2026-07-27_00:00:00 -->

From `go/internal/scanreport/proto/scanner-report.proto`:

- The native **`Issue`** message has **no `type` field at all**. Its fields are
  `rule_repository`, `rule_key`, `msg`, `overridden_severity`, `gap`,
  `text_range`, `flow`, `quick_fix_available`,
  `rule_description_context_key`, `msg_formatting`, `code_variants`,
  `overridden_impacts`, `internal_tags`.
- The `IssueType` enum (which does contain `SECURITY_HOTSPOT = 4`) is reachable
  only from `ExternalIssue.type` and `AdHocRule.type` — neither of which is the
  path hotspots travel.

This is exactly how the real scanner behaves: the scanner reports a *rule key*
and the server decides the finding's type, clean-code attribute and impacts from
its own rule metadata. **Mimicking the scanner therefore means emitting a native
`Issue` that names the rule and nothing type-related** — and letting Cloud
classify it as VULNERABILITY or CODE_SMELL per its converted rule definition.
This is the correct answer to issue #423's second checkbox, and it means we must
resist the temptation to stamp a type or an impact.

Corollary: `overridden_severity` should be left **unset** for converted
hotspots. The current code squashes `vulnerabilityProbability`
(HIGH/MEDIUM/LOW) into a legacy severity (CRITICAL/MAJOR/MINOR) and stamps it as
an override. The real scanner does not override severity; Cloud's converted rule
already carries the right `impacts` severity (e.g. SECURITY/HIGH for S4502).
Overriding it produces a severity the rule would never have raised.
`TODO:` verify on the live run that dropping the override yields the rule's
natural severity rather than a default.

## 4. The `sqs-hotspot` tag — where to apply it

<!-- updated: 2026-07-27_00:00:00 -->

Two candidate mechanisms:

**(a) In the scanner report**, via `Issue.internal_tags` (proto field 13).
Rejected. The field is referenced nowhere in this codebase, "internal" tags are
not the user-visible issue-tag surface, and there is no evidence the CE promotes
them to issue tags. Using it would be a guess, and the issue explicitly asks the
tag to be *assigned* to the migrated issues.

**(b) Post-import via `POST /api/issues/set_tags`.** Chosen. This is already how
the tool migrates ordinary issue tags — `syncIssueTags` in
`go/internal/migrate/tasks_issuesync.go` sets the source tags plus the
`metadata-synchronized` idempotency marker. Tags are a triage attribute on
Cloud, not an analysis attribute, so the sync phase is where they belong; it
matches how every other triage field (status, comments, tags) is already
handled, and it is idempotent.

Consequence: the hotspot sync task changes target-side identity. It must stop
looking for hotspots on Cloud and start matching source hotspots against target
**issues**, then apply status + comments + the `sqs-hotspot` tag.

## 5. Review-status mapping

<!-- updated: 2026-07-27_00:00:00 -->

Source hotspot review state → target issue transition:

| source status | source resolution | target transition | resulting issue state |
|---|---|---|---|
| `TO_REVIEW` | — | *(none)* | OPEN — matches "still needs review" |
| `REVIEWED` | `SAFE` | `falsepositive` | FALSE_POSITIVE — reviewer judged there is no risk |
| `REVIEWED` | `ACKNOWLEDGED` | `accept` | ACCEPTED — risk is real, accepted without fixing |
| `REVIEWED` | `FIXED` | `accept` | ACCEPTED — risk was real and addressed |

Rationale: SonarQube's own hotspot UI defines SAFE as "there is no risk", which
is semantically false-positive; ACKNOWLEDGED and FIXED both assert the risk was
real, which is semantically "accepted". This is a **strict improvement** on the
status quo, where `ACKNOWLEDGED` was silently downgraded to `SAFE` because
Cloud's hotspot API had no ACKNOWLEDGED resolution — a distinction the issue API
*can* represent.

`TODO:` no official SonarSource mapping was located; this is the tool's own
documented choice. Note it in the PR body as such.

`FIXED` keeps its original creation date and gets a migrated comment recording
the original resolution so the nuance is not lost.

## 6. Candidate source projects for live verification

<!-- updated: 2026-07-27_00:00:00 -->

Rule distribution of hotspots on the source (`http://localhost:9000`,
SonarQube Server 2026.3.1), probed 2026-07-27:

| project | hotspots | rules | notes |
|---|---|---|---|
| `okorach-oss_sonar-tools` | 31 (all REVIEWED) | S4823×11, S5852×9, S4784×9, S2245×1, S5332×1 | **11/31 on `python:S4823`, REMOVED on Cloud → unmigratable** |
| `test:juice-shop` | 103 (all TO_REVIEW) | 20 distinct across typescript/githubactions/docker/Web/shell/javascript | biggest, but multi-language → collides with the #474 qprofile-per-language trap |
| `demo:java-security` | 13 (all TO_REVIEW) | java only: S2077×4, S4507×2, S5443×2, S2245, S2092, S4544, S1313, S3330 | **primary candidate** — single language, all-Java, no REMOVED rules |
| `demo:secrets` | 1 | `python:S2068` | too small to prove anything |

Plan: **`demo:java-security`** as the primary end-to-end subject (single
language keeps the #474 failure mode out of the picture, 13 findings is enough
to prove exact parity). `okorach-oss_sonar-tools` as a secondary run to
demonstrate correct, *reported* handling of the REMOVED-rule case (expect
20/31 migrated, 11 reported unmigratable).

Target keys must end in `-m423` (three other agents share this org).

## 7. Implementation plan

<!-- updated: 2026-07-27_00:00:00 -->

1. **New `go/internal/scanreport/hotspots.go`** — a real, typed conversion:
   `HotspotInput` → `IssueInput`, mimicking the scanner (rule key only; no type,
   no severity override). Pure function, fully unit-testable.
2. **Active-rule participation** — include former-hotspot rules in the active
   rule set so the CE accepts the converted issues, and route hotspot-derived
   issues through the same orphan-rule filter as native issues so one bad rule
   can never abort the report. Rules that are REMOVED on Cloud drop out here and
   must be counted + logged.
3. **Retire hotspot-API sync** — rewrite the target side of
   `syncHotspotMetadata` to match against `/api/issues/search` instead of
   `/api/hotspots/search`, and apply: status transition (§5), migrated comments,
   then `set_tags` with `sqs-hotspot` (+ `metadata-synchronized`).
4. **Creation dates** — unchanged: converted hotspots keep `Key` and
   `CreationDate` on `IssueInput`, so the existing
   `scanreport.BackdateChangesets` mechanism preserves original dates. Must
   verify the `Key` is populated (the external-issue path has a known bug where
   an empty `Key` breaks the safety-split override).
5. **Extract-side bug** — `go/internal/extract/tasks_projectdata.go:162` has a
   bare `return nil` inside the `for status := range {TO_REVIEW, REVIEWED}` loop
   that, on a non-fatal 403/404 for the *second* status, discards the
   already-accumulated hotspots from the *first* and never writes the chunk.
   Should be `continue`. In scope because it silently loses the very findings
   this issue is about.
6. **Unit tests** — table-driven over the conversion, the status mapping, and
   the tag set.
7. **Docs** — update `MIGRATION-FACETS.md`, `MIGRATION-CAPABILITIES.md`,
   `ARCHITECTURE.md`, `TRANSFER.md`, `TRANSFER-INTERNALS.md`.
