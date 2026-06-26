# Syntax Highlighting Migration (issue #420)
<!-- updated: 2026-06-26_18:49:04 -->

## Problem
<!-- updated: 2026-06-26_18:49:04 -->

Migrated projects rendered source code as **raw, uncolored text** in the
SonarQube Cloud Code view, while a native scanner analysis of the same code is
syntax-highlighted (GitHub issue #420).

Root cause: the scanner-report ZIP that the tool submits to the CE *Submit*
endpoint never contained the `syntax-highlightings-<ref>.pb` protobuf streams.
The packager wrote `metadata`, `component-*`, `issues-*`, `external-issues-*`,
`measures-*`, `changesets-*`, `activerules`, `adhocrules`, and `source-*.txt`,
but had no notion of syntax highlighting at all. The source text was fetched
from `/api/sources/raw` (plain text), and the highlighting available from
`/api/sources/lines` was discarded (only used, stripped of markup, as a
source-text fallback). CloudVoyager never implemented this either — its
`syntax-highlighting.js` extractor was an empty stub.

## Where highlighting survives on the source
<!-- updated: 2026-06-26_18:49:04 -->

SonarQube does **not** expose the raw scanner highlighting protobuf over the web
API. The only place the highlighting survives is the per-line `code` field of
`/api/sources/lines`, where each highlighted token is wrapped in a
`<span class="<cssClass>">`. The CSS class is SonarQube's `TypeOfText`
abbreviation:

| CSS class      | HighlightingType (protobuf)        |
|----------------|------------------------------------|
| `a`            | `ANNOTATION` (1)                   |
| `c`            | `CONSTANT` (2)                     |
| `cd`, `cppd`   | `COMMENT` (3)                      |
| `j`            | `STRUCTURED_COMMENT` (5)           |
| `k`            | `KEYWORD` (6)                      |
| `s`            | `HIGHLIGHTING_STRING` (7)          |
| `h`            | `KEYWORD_LIGHT` (8)                |
| `p`            | `PREPROCESS_DIRECTIVE` (9)         |
| `sym`, `sym-N` | symbol references — *not* a syntax type (ignored) |

Symbol-reference spans (`sym`, `sym-N`) are a different feature (click-to-
highlight-occurrences) and **nest** with syntax spans, so the HTML must be
parsed with a stack rather than a flat regex.

## Solution
<!-- updated: 2026-06-26_18:49:04 -->

1. **Extract** (`internal/extract/tasks_projectdata.go`) — `fetchSourceCode`
   now always calls `/api/sources/lines` (via `fetchHighlightedLines`) and
   stores the per-line `code` HTML under a new `highlightedLines` field in the
   `getProjectSourceCode` record. The same response still backs the
   plain-text fallback (`plainTextFromHighlighted`) for purged `raw`.
2. **Parse + build** (`internal/scanreport/syntax.go`) — `BuildSyntaxHighlighting`
   parses each line's HTML into `SyntaxHighlightingRule{Range, Type}` messages.
   `parseHighlightedLine` walks the HTML with an open-span stack; the active
   type for any text run is the innermost span class that maps to a real type
   (so nested `sym` spans are seen through). Offsets are **0-based UTF-16 column
   offsets of the HTML-unescaped text** (`utf16Len`), and every rule is a
   single-line range (`StartLine == EndLine`) — matching exactly what a native
   `sonar-scanner` writes (verified against a real `.scannerwork` report).
3. **Package** (`internal/scanreport/packager.go`) — `addSyntaxHighlighting`
   writes one `syntax-highlightings-<ref>.pb` per component, each a
   length-delimited stream of `SyntaxHighlightingRule` (same framing as
   `issues-*`/`measures-*`). Refs with no rules are skipped.
4. **Wire-in** (`internal/migrate/tasks_projectdata.go`) —
   `loadExtractedSyntaxHighlighting` reads the `highlightedLines` back per
   branch, and `buildBranchReport` builds the rules with the **same**
   `ComponentRef`, so `syntax-highlightings-<ref>.pb` keys off the same ref as
   `component-<ref>.pb` / `source-<ref>.txt`.

### Offset-vs-source clamping (robustness)
<!-- updated: 2026-06-26_18:49:04 -->

The highlighting offsets come from `/api/sources/lines` while the source text
written to `source-<ref>.txt` comes from `/api/sources/raw`. If a line's length
diverges between the two (CRLF vs LF, BOM, trailing-whitespace handling), the CE
`RangeOffsetConverter` throws and **silently drops ALL highlighting for that
file** (logged only at DEBUG; the analysis still succeeds). To prevent a whole
file losing its colors over a one-character mismatch, `BuildSyntaxHighlighting`
clamps each range to the UTF-16 length of the matching `source-<ref>.txt` line
(trailing `\r` excluded), dropping rules that start past end-of-line and
truncating rules that end past it. At worst a single token is trimmed.

## Verification
<!-- updated: 2026-06-26_18:49:04 -->

Live clean-slate transfer of `okorach-oss_sonar-tools`
(`localhost:9000` → `sc-staging.io`, org `open-digital-society-1`):

- **Before fix** — target `sonar/cli/__init__.py`: 0 / 22 lines highlighted.
- **After fix** — 20 / 22 lines highlighted (2 blank lines correctly skipped).
- **Parity** — `sonar/projects.py`: source and target both emit **1835**
  syntax spans, with **0 of 1571 lines differing** (offsets, text, and type
  all match). Target class histogram: `k`=978, `s`=471, `j`=267, `c`=86,
  `cd`=33 — all classes migrate, not just comments.
- **All branches** — highlighting present on `master`, `my-test`, `release-3.x`,
  `reduce-tech-debt` (non-main branches too).
- **No regression** — issues 1292, hotspots 31/31, 4 LONG branches, `develop`
  correctly skipped (source purged), `ncloc`=17,379, SCM blame 1572/1572 lines.

Unit tests: `internal/scanreport/syntax_test.go` covers class mapping, UTF-16
offsets, HTML entities, nested symbol spans, malformed tags, the build/ref
mapping, and source-line clamping (incl. CRLF).

## Out of scope
<!-- updated: 2026-06-26_18:49:04 -->

Symbol references (`symbols-<ref>.pb`, the click-to-highlight-occurrences
feature) are **not** migrated. `/api/sources/lines` exposes only `sym-N`
grouping classes, not the declaration/reference metadata the `symbols` protobuf
requires. This is a separate feature from issue #420.
