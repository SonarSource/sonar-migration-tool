// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"testing"

	"github.com/go-pdf/fpdf"
)

// Issue #345: when the Details column carries a long project key
// WITHOUT inline-bold markers (Failed / Skipped rows, or any error
// message that embeds the key as plain text), drawDetailsCell used
// to delegate to gofpdf's pdf.SplitLines which only wraps at
// whitespace — long unbroken keys overflowed past the cell. The
// fix routes the no-marker branch through the same width-aware
// wrap that already handles the marker branch, gaining its
// hard-break-at-runes fallback for tokens wider than the cell.
func TestWrapPlainText_LongProjectKeyStaysInCell(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()
	pdf.SetFont(pdfFontFamilyBody, "", 6.0)

	longKey := "migration-tool-test-gh_okorach-org_pr-demo:" +
		"feature/long-living-branch:" +
		"some-subproject-with-extended-id-3a1857ec-cebc-49f2"
	// No inline-bold markers — exercise the plain-text wrap branch.
	text := "create failed for " + longKey

	const cellWidth = 80.0
	const fontSize = 6.0

	phys := wrapInlineBoldLines(pdf, text, cellWidth, fontSize)
	if len(phys) < 2 {
		t.Fatalf("expected the long project key to wrap to multiple lines, got %d", len(phys))
	}
	for i, segs := range phys {
		var lineW float64
		for _, s := range segs {
			pdf.SetFont(pdfFontFamilyBody, "", fontSize)
			lineW += pdf.GetStringWidth(s.text)
		}
		if lineW > cellWidth {
			t.Errorf("physical line %d width %.2f exceeds cellWidth %.2f (#345 plain-text overflow)", i, lineW, cellWidth)
		}
	}
}

// Issue #345 root cause: pdf.Write wraps at the *page* right margin.
// When a wrapped line lands flush against the Details column right
// edge (which on Letter portrait coincides with the page right
// margin), sub-millimetre rounding between our wrap measurement
// and gofpdf's internal width tracking trips Write's auto-wrap,
// rendering the trailing bold key at the page LEFT margin and
// splashing it into the Name / Organization columns of the same
// row. The fix is to draw each segment via CellFormat with its
// measured width — CellFormat takes an explicit width and never
// wraps.
//
// This test renders the exact bold-key segment from the screenshot
// at a cursor position that places the cell's right edge flush
// with the page right margin (worst case). After rendering, the
// cursor's Y must not have advanced — any Y drift means pdf.Write
// or CellFormat triggered an unintended wrap.
func TestWriteInlineBoldLine_NoOverflowAtPageRightMargin(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()

	// Exact cloud key shape from the #345 screenshot.
	cloudKey := "migration-tool-test_okorach_demo-gitlabci-cli_e81d5112-e681-44b2-aee4-62b56c8ac5cb"

	// Place the cursor so the rendered line will land near the
	// page right margin — the regime where pdf.Write used to wrap.
	pageW, _ := pdf.GetPageSize()
	_, _, rightMargin, _ := pdf.GetMargins()
	const fontSize = 6.0
	const lineH = 2.5
	pdf.SetFont(pdfFontFamilyBody, "B", fontSize)
	keyWidth := pdf.GetStringWidth(cloudKey)
	startX := pageW - rightMargin - keyWidth // flush against right margin
	startY := 50.0
	pdf.SetXY(startX, startY)

	segs := []styledSeg{{text: cloudKey, bold: true}}
	writeInlineBoldLine(pdf, segs, lineH, fontSize)

	endX, endY := pdf.GetXY()
	if endY != startY {
		t.Errorf("cursor Y drifted: started %.2f, ended %.2f — render wrapped past the cell", startY, endY)
	}
	// Cursor X must have advanced by exactly the bold key's width
	// (CellFormat with ln=0 leaves the cursor flush right of the
	// drawn cell, NOT at the page-left margin like pdf.Write would).
	wantEndX := startX + keyWidth
	if absMM(endX-wantEndX) > 0.01 {
		t.Errorf("cursor X: want %.2f, got %.2f (drift %.2f)", wantEndX, endX, endX-wantEndX)
	}
}

func absMM(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Issue #345: a long project key inside a Failed row's Details
// column (which carries no inline-bold markers — the text is just
// the error message) now routes through wrapInlineBoldLines via
// drawDetailsCell. Before the fix, drawDetailsCell delegated the
// no-marker case to gofpdf's pdf.SplitLines, which only wraps at
// whitespace; the key overflowed the cell and continued at the
// page's left margin on the next line.
//
// This test renders a single details cell at the same widths the
// real PDF uses, then inspects pdf.GetXY() after the draw: the
// cursor must land flush right of the cell at the row's top — any
// vertical drift means the renderer wrapped to a fresh page row.
func TestDrawDetailsCell_LongProjectKeyNoOverflow(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()
	pdf.SetFont(pdfFontFamilyBody, "", 6.0)

	longKey := "migration-tool-test-gh_okorach-org_pr-demo:" +
		"feature/long-living-branch:" +
		"some-subproject-with-extended-id-3a1857ec-cebc-49f2"
	text := "create failed for " + longKey

	const cellWidth = 80.0
	const fontSize = 6.0
	const lineH = 2.5

	lineCount := len(wrapInlineBoldLines(pdf, text, cellWidth-1.0, fontSize))
	if lineCount < 2 {
		t.Fatalf("expected wrap to produce >=2 lines, got %d", lineCount)
	}

	// Compare wrapInlineBoldLines (the wrap drawDetailsCell now
	// uses for ALL text) against pdf.SplitLines (the wrap the
	// old no-marker shortcut used). gofpdf's SplitLines only
	// breaks at whitespace, so for a no-whitespace project key
	// it returns a single oversize line that overflows the cell.
	// wrapInlineBoldLines must hand back multiple in-cell lines.
	splitLines := pdf.SplitLines([]byte(text), cellWidth)
	if len(splitLines) >= lineCount {
		// Sanity: if gofpdf already wraps this case correctly,
		// the test is no longer guarding anything useful.
		t.Skipf("gofpdf.SplitLines already wrapped this input to %d lines (>= our %d) — pick a longer key", len(splitLines), lineCount)
	}
	// At least one of the gofpdf lines is wider than the cell —
	// proves the old shortcut path was the source of the overflow.
	pdf.SetFont(pdfFontFamilyBody, "", fontSize)
	overflowed := false
	for _, line := range splitLines {
		if pdf.GetStringWidth(string(line)) > cellWidth {
			overflowed = true
			break
		}
	}
	if !overflowed {
		t.Skip("gofpdf.SplitLines kept every line within cellWidth for this input — pick one with longer atomic tokens")
	}

	// Render via drawDetailsCell — must not panic, and must land
	// the cursor at the row's top-right so the next column draws
	// alongside the cell.
	startX, startY := 20.0, 50.0
	pdf.SetXY(startX, startY)
	drawDetailsCell(pdf, cellWidth, lineH, lineCount, text, fontSize)
	endX, endY := pdf.GetXY()
	if endY != startY {
		t.Errorf("cursor drifted vertically: started Y=%.2f, ended Y=%.2f", startY, endY)
	}
	if endX != startX+cellWidth {
		t.Errorf("cursor X: want %.2f, got %.2f", startX+cellWidth, endX)
	}
}

// Issue #345: long SonarCloud project keys in the Projects-section
// Details column overflowed the cell because tokenizeForWrap only
// broke on space, tab, and comma — none of which appear in a typical
// project key (org_some-long-thing:branch:sub). The hard-break-at-
// runes path eventually kicks in, but only after a token wider than
// the entire cell, which is too late: by the time the wrap noticed
// the overflow, the renderer had already advanced. Adding `:`, `_`,
// `-`, `/`, `.` as break points lets the wrap split at natural
// project-key boundaries before any overflow.
func TestWrapInlineBoldLines_LongProjectKeyStaysInCell(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()

	// Worst-case key shape — every segment is at the upper end of
	// what users put in their SonarQube project keys.
	longKey := "migration-tool-test-gh_okorach-org_pr-demo:" +
		"feature/long-living-branch:" +
		"some-subproject-with-extended-id-3a1857ec-cebc-49f2"
	text := "New Project Key: " + inlineBoldStart + longKey + inlineBoldEnd

	const cellWidth = 80.0
	const fontSize = 6.0

	phys := wrapInlineBoldLines(pdf, text, cellWidth, fontSize)
	if len(phys) < 2 {
		t.Fatalf("expected the long project key to wrap to multiple physical lines, got %d", len(phys))
	}
	// Every wrapped line must fit within the cell — otherwise the
	// downstream pdf.Write would trigger a page-margin wrap and the
	// overflow would smear into the next row's first column.
	for i, segs := range phys {
		var lineW float64
		for _, s := range segs {
			style := ""
			if s.bold {
				style = "B"
			}
			pdf.SetFont(pdfFontFamilyBody, style, fontSize)
			lineW += pdf.GetStringWidth(s.text)
		}
		if lineW > cellWidth {
			t.Errorf("physical line %d width %.2f exceeds cellWidth %.2f (#345 overflow)", i, lineW, cellWidth)
		}
	}
}
