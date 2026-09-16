---
spec_id: SPEC-004
title: Source Code & SCM Data Migration
status: draft
priority: P0
epic: "Core Data Migration"
depends_on: [SPEC-001]
estimated_effort: XL
cloudvoyager_ref: "src/pipelines/source-code/..."
---

# SPEC-004: Source Code & SCM Data Migration
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

The Source Code & SCM Data Migration pipeline extracts source code and SCM (Software Configuration Management) blame data from SonarQube Server and encodes them into the scanner report format for injection into SonarQube Cloud. Source code is needed so that SonarQube Cloud can display code snippets alongside issues, while SCM blame data is critical for attributing issues to the correct authors and — most importantly — for backdating issue creation dates.

The crown jewel algorithm in this pipeline is `backdateChangesets()`, which rewrites SCM blame dates per-line to trick SonarQube Cloud's Compute Engine into assigning the correct historical creation dates to migrated issues. Without this algorithm, all migrated issues would appear to have been created on the date of the synthetic analysis, destroying years of historical trend data. CloudVoyager's implementation of this algorithm is the key innovation that makes historical data migration viable.

This pipeline also handles the extraction of supplementary source metadata including symbols and syntax highlighting data, though these are lower priority than the core source and blame data.

## Problem Statement

When SonarQube Cloud's Compute Engine processes a scanner report, it uses SCM blame data to determine the creation date of each issue: the issue's creation date is set to the blame date of the line where the issue was detected. If no SCM data is provided, or if the blame dates are current, all issues appear as "new" — created on the day of the synthetic analysis. This completely destroys historical trend data (new issues over time, issue aging, leak period metrics) which is critical for engineering management and compliance reporting.

The source code itself is also needed for the SonarQube Cloud UI to display code context around issues. Without source code in the report, users see issues with no code context, making triage significantly harder.

## User Stories

- **As a** migration operator, **I want to** extract source code from SonarQube Server, **so that** issues in SonarQube Cloud display with code context.
- **As a** migration operator, **I want to** preserve SCM blame data, **so that** issue authorship is correctly attributed in SonarQube Cloud.
- **As a** migration operator, **I want to** backdate SCM changesets, **so that** migrated issues retain their original creation dates in SonarQube Cloud.
- **As a** engineering manager, **I want to** see accurate historical issue trend data in SonarQube Cloud, **so that** migration does not create artificial spikes in the "new issues" metric.
- **As a** migration operator, **I want to** extract source code concurrently, **so that** large projects with thousands of files complete in reasonable time.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Extract source code per file via `/api/sources/raw` | Must |
| FR-2 | Extract SCM blame data per file via `/api/sources/scm` | Must |
| FR-3 | Encode source code as `source-{ref}.txt` plain text files in the report ZIP | Must |
| FR-4 | Encode SCM blame data as `changesets-{ref}.pb` single-message protobuf files | Must |
| FR-5 | Implement `backdateChangesets()` algorithm to rewrite blame dates based on issue creation dates | Must |
| FR-6 | Handle safety-splitting: calendar days with >5K issues get 1-day-spaced synthetic dates | Must |
| FR-7 | Support concurrent source file extraction (configurable, default 10 workers) | Must |
| FR-8 | Handle files that return 404 (deleted from server but still referenced by issues) | Must |
| FR-9 | Preserve line count metadata per component for the protobuf Component message | Must |
| FR-10 | Extract file language metadata for component encoding | Must |
| FR-11 | Write extracted source and SCM data to intermediate files for resume capability | Should |
| FR-12 | Extract symbols data via `/api/sources/symbols` for reference tables | Nice |
| FR-13 | Extract syntax highlighting via `/api/sources/syntax_highlighting` | Nice |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Source extraction throughput | >= 50 files/second with 10 concurrent workers |
| NFR-2 | Maximum file size | Handle files up to 10 MB |
| NFR-3 | Memory usage | Stream source files to disk, do not buffer all in memory |
| NFR-4 | Backdate algorithm performance | Process 100K issues across 10K files in < 30 seconds |
| NFR-5 | Progress reporting | Log progress every 100 files extracted |
| NFR-6 | Error resilience | Continue on individual file fetch failure, log and create empty placeholder |

## Technical Design

### Architecture

The `go/internal/scanreport/` package already contains a working skeleton (builder.go, packager.go, submit.go, backdate.go) that should be extended, not replaced.

**Extract phase** (`go/internal/extract/tasks_sources.go`):
- `ExtractSourceCode` task: concurrent `/api/sources/raw` calls per file
- `ExtractSCMData` task: concurrent `/api/sources/scm` calls per file
- Write raw source to `files/<extract_id>/sources/{component_key}.txt`
- Write SCM data to `files/<extract_id>/scm/{component_key}.jsonl`

**Scanner phase** (`go/internal/scanreport/sources.go`):
- Transform source code into `source-{ref}.txt` files
- Transform SCM data into `changesets-{ref}.pb` protobuf messages

**Backdate engine** (`go/internal/scanreport/backdate.go`):
- Standalone module implementing the `backdateChangesets()` algorithm
- Input: SCM blame data + issue creation dates + text ranges
- Output: Modified changeset with rewritten dates

### Key Algorithms

#### backdateChangesets() — The Core Innovation

This algorithm rewrites SCM blame dates so that SonarQube Cloud's CE assigns the correct historical creation date to each issue. The CE determines an issue's creation date from the blame date of the line where the issue is located. By rewriting blame dates to match the desired issue creation dates, we control when issues appear to have been created.

```
func backdateChangesets(
    scmData SCMBlameData,
    issues []IssueWithDates,
    componentRef int32,
) *pb.Changesets:

    // Step 1: Build a map of line -> desired date
    // For each issue, map its textRange lines to its creationDate
    lineDateMap = map[int]time.Time{}
    for _, issue in issues:
        if issue.TextRange == nil:
            continue
        for line = issue.TextRange.StartLine; line <= issue.TextRange.EndLine; line++:
            existingDate, exists = lineDateMap[line]
            issueDate = parseDate(issue.CreationDate)
            // Take the MINIMUM (oldest) date across overlapping lines
            // This ensures the oldest issue on each line sets the changeset date,
            // which is the correct behavior for preserving original creation dates.
            // See backdate.go: if existing, ok := lineDateMap[ln]; !ok || dateMs < existing
            if !exists || issueDate.Before(existingDate):
                lineDateMap[line] = issueDate

    // Step 2: Safety-split dense dates
    // Count issues per calendar day
    dateHistogram = map[string]int{}
    for _, date in lineDateMap:
        dayKey = date.Format("2006-01-02")
        dateHistogram[dayKey]++

    // For days with >5000 issues, spread across synthetic dates
    for dayKey, count in dateHistogram:
        if count > 5000:
            spreadDates(lineDateMap, dayKey, count)

    // Step 3: Build protobuf Changesets message
    changesets = &pb.Changesets{
        ComponentRef: componentRef,
    }

    // Create unique changeset entries (deduplicated by date+author)
    changesetMap = map[string]int{}  // key -> index
    for lineNum = 1; lineNum <= len(scmData.Lines); lineNum++:
        originalBlame = scmData.Lines[lineNum]

        // Use backdated date if this line has an issue, otherwise use original
        date = lineDateMap[lineNum]
        if date.IsZero():
            date = parseDate(originalBlame.Date)

        author = originalBlame.Author
        revision = originalBlame.Revision

        // Deduplicate changesets
        csKey = fmt.Sprintf("%s|%s|%d", author, revision, date.Unix())
        if idx, ok = changesetMap[csKey]; ok:
            changesets.ChangesetIndexByLine = append(changesets.ChangesetIndexByLine, int32(idx))
        else:
            idx = len(changesets.Changeset)
            changesetMap[csKey] = idx
            changesets.Changeset = append(changesets.Changeset, &pb.Changesets_Changeset{
                Revision: revision,
                Author:   author,
                Date:     date.UnixMilli(),
            })
            changesets.ChangesetIndexByLine = append(changesets.ChangesetIndexByLine, int32(idx))

    return changesets
```

#### Safety-Splitting Dense Dates

When more than 5,000 issues share the same calendar day, SonarQube Cloud may have performance issues processing them. Spread the dates across adjacent days:

```
func spreadDates(lineDateMap map[int]time.Time, dayKey string, count int):
    baseDate = parseDate(dayKey)
    linesForDay = []int{}

    for line, date in lineDateMap:
        if date.Format("2006-01-02") == dayKey:
            linesForDay = append(linesForDay, line)

    sort.Ints(linesForDay)

    // Spread across synthetic dates, 1 day apart
    // First 5000 keep the original date
    // Remaining get synthetic dates, each 1 day earlier
    for i, line in enumerate(linesForDay):
        if i >= 5000:
            dayOffset = (i - 5000) / 5000 + 1
            lineDateMap[line] = baseDate.AddDate(0, 0, -dayOffset)
```

#### Concurrent Source Extraction

```
func extractSourcesConcurrently(
    ctx context.Context,
    client *server.Client,
    components []Component,
    concurrency int,
) (map[string]string, map[string]SCMBlameData, error):

    sources = sync.Map{}
    scmData = sync.Map{}
    sem = semaphore.NewWeighted(int64(concurrency))

    var g errgroup.Group
    for _, comp in components:
        comp := comp
        g.Go(func() error:
            sem.Acquire(ctx, 1)
            defer sem.Release(1)

            // Fetch source code
            src, err = client.Sources().Raw(ctx, comp.Key)
            if err != nil:
                if isNotFound(err):
                    log.Warn("source file not found, using empty placeholder", "component", comp.Key)
                    sources.Store(comp.Key, "")
                    return nil
                return err
            sources.Store(comp.Key, src)

            // Fetch SCM blame data
            blame, err = client.Sources().SCM(ctx, comp.Key)
            if err != nil:
                if isNotFound(err):
                    log.Warn("SCM data not found", "component", comp.Key)
                    return nil
                return err
            scmData.Store(comp.Key, blame)

            return nil
        )

    if err = g.Wait(); err != nil:
        return nil, nil, err

    return toMap(sources), toSCMMap(scmData), nil
```

#### Source-to-Report Encoding

```
func encodeSourceFile(sourceCode string, ref int32) (filename string, data []byte):
    return fmt.Sprintf("source-%d.txt", ref), []byte(sourceCode)
```

### Data Flow

1. **List Components** — Get all file components from the project (extracted during SPEC-001 component enumeration).
2. **Extract Source** — Concurrently fetch source code via `/api/sources/raw` for each component.
3. **Extract SCM** — Concurrently fetch SCM blame data via `/api/sources/scm` for each component.
4. **Write Intermediates** — Write source and SCM data to disk for resume capability.
5. **Collect Issue Dates** — Load extracted issues (SPEC-002) to get creation dates and text ranges per component.
6. **Backdate Changesets** — Run `backdateChangesets()` per component using issue creation dates and SCM blame data.
7. **Encode Source** — Write each source file as `source-{ref}.txt` into the report archive.
8. **Encode Changesets** — Encode each backdated changeset as `changesets-{ref}.pb` (single-message protobuf).
9. **Add to Report** — Pass all source and changeset files to the Report Builder (SPEC-001).

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/sources/raw` | GET | Fetch raw source code for a component. Params: `key` (component key). Returns plain text. |
| `/api/sources/scm` | GET | Fetch SCM blame data per line. Params: `key` (component key). Returns JSON array of `[lineNum, author, date, revision]` tuples. |
| `/api/sources/symbols` | GET | Fetch symbol reference data. Params: `key`. Lower priority. |
| `/api/sources/syntax_highlighting` | GET | Fetch syntax highlighting metadata. Params: `key`. Lower priority. |
| `/api/components/tree` | GET | List all file components in a project for enumeration. |

### Protobuf Schema

#### ScannerReport.Changesets (single-message per component)
```protobuf
message Changesets {
    int32 component_ref = 1;
    bool copy_from_previous = 2;
    repeated Changeset changeset = 3;
    repeated int32 changeset_index_by_line = 4;  // maps line to changeset index

    message Changeset {
        string revision = 1;   // SCM revision hash
        string author = 2;     // Blame author
        int64 date = 3;        // Timestamp in milliseconds (BACKDATED)
    }
}
```

The `changeset_index_by_line` array is 0-indexed: array index 0 corresponds to source line 1, index 1 to line 2, etc. Each entry is an index into the `changeset` array. This compact representation avoids repeating changeset data for lines that share the same blame entry. The `copy_from_previous` field (field 2) indicates whether to copy changesets from the previous analysis rather than providing new data.

### SCM Data Types

```go
type SCMBlameData struct {
    Lines map[int]SCMBlameLine  // lineNum -> blame data
}

type SCMBlameLine struct {
    Author   string `json:"author"`
    Date     string `json:"date"`      // ISO 8601 date
    Revision string `json:"revision"`  // SCM revision hash
}

type IssueWithDates struct {
    CreationDate string     `json:"creationDate"`
    TextRange    *TextRange `json:"textRange"`
    Component    string     `json:"component"`
}
```

### Backdate Algorithm Visual Example

Consider a file with 5 lines and 2 issues:

```
Original SCM blame:
  Line 1: author=alice, date=2024-01-15, rev=abc123
  Line 2: author=alice, date=2024-01-15, rev=abc123
  Line 3: author=bob,   date=2024-02-01, rev=def456
  Line 4: author=bob,   date=2024-02-01, rev=def456
  Line 5: author=carol, date=2024-03-10, rev=ghi789

Issues:
  Issue A: creationDate=2023-06-15, textRange={startLine=2, endLine=3}
  Issue B: creationDate=2023-09-20, textRange={startLine=4, endLine=4}

After backdateChangesets():
  Line 1: date=2024-01-15 (unchanged, no issue)
  Line 2: date=2023-06-15 (backdated to Issue A creation)
  Line 3: date=2023-06-15 (backdated to Issue A creation)
  Line 4: date=2023-09-20 (backdated to Issue B creation)
  Line 5: date=2024-03-10 (unchanged, no issue)

Result: When CE processes this report:
  - Issue A will appear with creationDate=2023-06-15 (MIN/oldest of lines 2-3)
  - Issue B will appear with creationDate=2023-09-20 (blame date of line 4)
```

## Acceptance Criteria

- [ ] AC-1: Source code is extracted for all file components and encoded as `source-{ref}.txt` in the report.
- [ ] AC-2: SCM blame data is extracted and encoded as `changesets-{ref}.pb` in single-message protobuf format.
- [ ] AC-3: `backdateChangesets()` correctly rewrites blame dates to match issue creation dates.
- [ ] AC-4: Overlapping issues on the same lines use the MIN (oldest) date to preserve original creation dates.
- [ ] AC-5: Safety-splitting activates for calendar days with >5K issues and spreads them across synthetic dates.
- [ ] AC-6: Files that return 404 are handled gracefully with empty placeholders.
- [ ] AC-7: Concurrent extraction achieves at least 50 files/second with 10 workers.
- [ ] AC-8: The `changeset_index_by_line` array correctly maps each line to its changeset entry.
- [ ] AC-9: Changeset deduplication correctly merges identical author+revision+date entries.
- [ ] AC-10: Issue creation dates in SonarQube Cloud match the original dates from SonarQube Server (verified end-to-end).
- [ ] AC-11: Unit tests cover the backdateChangesets algorithm with edge cases (overlapping ranges, dense dates, missing SCM data).
- [ ] AC-12: Large project test: 10K files, 100K issues process within 60 seconds.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Source code extraction | `src/pipelines/source-code/extractor.js` |
| SCM blame extraction | `src/pipelines/source-code/scm-extractor.js` |
| Backdate algorithm | `src/pipelines/source-code/backdate-changesets.js` |
| Safety-splitting | `src/pipelines/source-code/date-spreader.js` |
| Changeset protobuf encoding | `src/pipelines/scanner-report/encoders/changesets.js` |
| Source file encoding | `src/pipelines/scanner-report/encoders/source.js` |
| Concurrent worker pool | `src/pipelines/source-code/pool.js` |

For official SonarQube API documentation, see https://docs.sonarsource.com/llms.txt

## Known Limitations

- The `/api/sources/raw` endpoint returns the source code as of the most recent analysis, not as of a specific historical point. If the file has changed since the issues were detected, line numbers may not align perfectly with the issue text ranges.
- SCM blame data availability depends on the SonarQube Server having SCM data. Projects analyzed without SCM integration will have no blame data, making backdating impossible for those projects.
- The safety-splitting algorithm uses a fixed threshold of 5,000 issues per day. This threshold was determined empirically by CloudVoyager and may need adjustment for SonarQube Cloud's current CE implementation.
- Very large source files (>10 MB) may cause memory pressure during extraction. Consider streaming for such files.
- The `/api/sources/raw` endpoint requires the `codeviewer` permission. Some SonarQube Server configurations may restrict this API.
- Symbol and syntax highlighting extraction are lower priority and may be omitted in the initial implementation without significant user impact.

## Open Questions

- Should we attempt to reconstruct historical source code from SCM (git) if the SonarQube Server API returns current-version source? This would improve line number accuracy for backdated issues.
- What should the behavior be when a component has issues but no SCM data? Use the issue creation date as a fallback for all lines?
- Should the safety-split threshold (5K) be configurable?
- How should we handle components where source extraction fails (network timeout, permission denied) but SCM data succeeds, or vice versa?
- Should we parallelize source and SCM extraction for the same component, or fetch them sequentially to reduce server load?
