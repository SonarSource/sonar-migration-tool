// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-pdf/fpdf"
)

// TestRenderPDFGlobalSettingsWrappingRow is a regression for issue
// #207. The Global Settings section is the only section that
// word-wraps its Name column; previously the wrapped row had two
// visual bugs:
//   - Name cell shorter than the row when the Details column wraps
//     to more lines than the Name.
//   - 8pt body font's descenders clipped because wrappedLineH was
//     too tight (3.0mm) for the 8pt font used in the Name column.
//
// We can't pixel-diff a PDF in unit tests, but we can exercise the
// exact mismatched-wrap shape that produced the bug and verify the
// render produces a non-trivial PDF. Combined with the explicit
// padToLineCount unit test, this guards both code paths.
func TestRenderPDFGlobalSettingsWrappingRow(t *testing.T) {
	longName := "sonar.azureresourcemanager.file.identifier"
	// Details with many newlines drives detailsLineCount > nameLineCount.
	longDetails := "value: true\norg-A: applied\norg-B: applied\norg-C: applied\norg-D: applied"
	// Short details + long name drives nameLineCount > detailsLineCount.
	shortDetails := "value: false"
	summary := &MigrationSummary{
		RunID:       "issue-207",
		GeneratedAt: time.Now(),
		Sections: []Section{
			{
				Name: "Global Settings",
				Succeeded: []EntityItem{
					{Name: longName, Organization: "fubar", Detail: longDetails},
					{Name: "sonar.java.ignoreUnnamedModuleForSplitPackage", Organization: "fubar", Detail: shortDetails},
				},
			},
		},
	}
	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if string(pdfBytes[:5]) != "%PDF-" {
		t.Errorf("expected PDF header, got %q", string(pdfBytes[:5]))
	}
	if len(pdfBytes) < 10_000 {
		t.Errorf("expected non-trivial PDF size, got %d bytes", len(pdfBytes))
	}
}

// TestDrawWrappedCell uses a recorder that conforms to the fpdfCell
// interface to verify that drawWrappedCell emits exactly lineCount
// CellFormat calls — one per line — with the right border code on
// each (top on first, bottom on last, sides on all). This is the
// guarantee that makes the cell's outer rectangle always reach
// height = lineCount*lineH, even when len(lines) < lineCount.
func TestDrawWrappedCell(t *testing.T) {
	cases := []struct {
		name        string
		lineCount   int
		lines       []string
		wantBorders []string
		wantTexts   []string
	}{
		{
			name:        "single line",
			lineCount:   1,
			lines:       []string{"hello"},
			wantBorders: []string{"1"},
			wantTexts:   []string{"hello"},
		},
		{
			name:        "wrap matches lineCount",
			lineCount:   2,
			lines:       []string{"line1", "line2"},
			wantBorders: []string{"LRT", "LRB"},
			wantTexts:   []string{"line1", "line2"},
		},
		{
			name:        "wrap fewer than lineCount - pad with empties",
			lineCount:   4,
			lines:       []string{"only"},
			wantBorders: []string{"LRT", "LR", "LR", "LRB"},
			wantTexts:   []string{"only", "", "", ""},
		},
		{
			name:        "three lines",
			lineCount:   3,
			lines:       []string{"a", "b", "c"},
			wantBorders: []string{"LRT", "LR", "LRB"},
			wantTexts:   []string{"a", "b", "c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fpdfCellRecorder{}
			rawLines := make([][]byte, len(tc.lines))
			for i, s := range tc.lines {
				rawLines[i] = []byte(s)
			}
			drawWrappedCell(rec, 50, 4, tc.lineCount, rawLines)

			if len(rec.calls) != tc.lineCount {
				t.Fatalf("expected %d CellFormat calls, got %d", tc.lineCount, len(rec.calls))
			}
			for i, call := range rec.calls {
				if call.border != tc.wantBorders[i] {
					t.Errorf("line %d border: got %q want %q", i, call.border, tc.wantBorders[i])
				}
				if call.text != tc.wantTexts[i] {
					t.Errorf("line %d text: got %q want %q", i, call.text, tc.wantTexts[i])
				}
				wantY := float64(i) * 4
				if call.y != wantY {
					t.Errorf("line %d y: got %f want %f", i, call.y, wantY)
				}
			}
		})
	}
}

type fpdfCellCall struct {
	x, y, w, h           float64
	text, border, align  string
	ln                   int
	fill                 bool
}

type fpdfCellRecorder struct {
	x, y  float64
	calls []fpdfCellCall
}

func (r *fpdfCellRecorder) GetXY() (float64, float64) { return r.x, r.y }
func (r *fpdfCellRecorder) SetXY(x, y float64)        { r.x, r.y = x, y }
func (r *fpdfCellRecorder) CellFormat(w, h float64, text, border string, ln int, align string, fill bool, link int, linkStr string) {
	r.calls = append(r.calls, fpdfCellCall{
		x: r.x, y: r.y, w: w, h: h,
		text: text, border: border, align: align,
		ln: ln, fill: fill,
	})
}

func TestRenderPDFLongDetailsWrap(t *testing.T) {
	// The portfolio Partial issue text is intentionally long. Verify it does
	// not cause a render failure and that the resulting PDF is well-formed.
	longIssue := "Source portfolio has subportfolios; SonarQube Cloud has no hierarchy — migrated as a flat project list and the perimeter may differ from the source"
	summary := &MigrationSummary{
		RunID:       "wrap-run",
		GeneratedAt: time.Now(),
		Sections: []Section{
			{
				Name: "Portfolios",
				Partial: []EntityItem{
					{Name: "Top", Detail: "42", Issues: []string{longIssue}},
				},
			},
		},
	}

	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		t.Fatalf("RenderPDF with long details: %v", err)
	}
	if string(pdfBytes[:5]) != "%PDF-" {
		t.Errorf("expected PDF header, got %q", string(pdfBytes[:5]))
	}
	// A valid embedded-font PDF for one section + one row is >>10KB.
	if len(pdfBytes) < 10_000 {
		t.Errorf("expected non-trivial PDF size, got %d bytes", len(pdfBytes))
	}
}

func TestRenderPDFUnicodeNames(t *testing.T) {
	summary := &MigrationSummary{
		RunID:       "unicode-run",
		GeneratedAt: time.Now(),
		Sections: []Section{
			{
				Name: "Quality Gates",
				Succeeded: []EntityItem{
					{Name: "🥇 1 - Corp Gold", Organization: "org1", Detail: "42"},
					{Name: "🥉 3 - Corp base", Organization: "org1", Detail: "44"},
					{Name: "Café — Production", Organization: "org1", Detail: "45"},
					{Name: "Ürümqi 北京 αβγ", Organization: "org1", Detail: "46"},
				},
			},
		},
	}

	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		t.Fatalf("RenderPDF with unicode names: %v", err)
	}
	if string(pdfBytes[:5]) != "%PDF-" {
		t.Errorf("expected PDF header, got %q", string(pdfBytes[:5]))
	}
	// fpdf compresses and subsets the embedded TTF, so the family name does
	// not appear in the byte stream. We instead verify that rendering with
	// astral-plane / accented / non-Latin characters does not panic and
	// produces a PDF larger than the Helvetica-only fallback (≈3KB).
	if len(pdfBytes) < 10_000 {
		t.Errorf("expected embedded subsetted font (>=10KB PDF), got %d bytes", len(pdfBytes))
	}
}

func TestRenderPDFMinimal(t *testing.T) {
	summary := &MigrationSummary{
		RunID:       "test-run-01",
		GeneratedAt: time.Now(),
		Sections: []Section{
			{Name: "Projects"},
			{Name: "Quality Profiles"},
		},
	}

	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if len(pdfBytes) == 0 {
		t.Fatal("expected non-empty PDF")
	}
	// Check PDF magic header
	if string(pdfBytes[:5]) != "%PDF-" {
		t.Errorf("expected PDF header, got %q", string(pdfBytes[:5]))
	}
}

func TestRenderPDFWithData(t *testing.T) {
	summary := &MigrationSummary{
		RunID:       "04-27-2026-02",
		GeneratedAt: time.Now(),
		Sections: []Section{
			{
				Name: "Projects",
				Succeeded: []EntityItem{
					{Name: "Project A", Organization: "org1", Detail: "org1_projA|scan:success"},
					{Name: "Project B", Organization: "org1", Detail: "org1_projB|scan:failed"},
				},
				Failed: []EntityItem{
					{Name: "Project C", Organization: "org1", ErrorMessage: "already exists"},
				},
				Skipped: []EntityItem{
					{Name: "Project D", Organization: "old-org", Detail: "Organization skipped"},
				},
			},
			{
				Name: "Quality Gates",
				Succeeded: []EntityItem{
					{Name: "Custom Gate", Organization: "org1", Detail: "gate-1"},
				},
			},
			{
				Name: "Groups",
				Succeeded: []EntityItem{
					{Name: "DevTeam", Organization: "org1"},
					{Name: "QATeam", Organization: "org1"},
				},
				Failed: []EntityItem{
					{Name: "AdminGroup", Organization: "org1", ErrorMessage: "unauthorized"},
				},
			},
		},
	}

	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if len(pdfBytes) < 100 {
		t.Errorf("PDF seems too small: %d bytes", len(pdfBytes))
	}
}

func TestRenderPDFEmptySections(t *testing.T) {
	summary := &MigrationSummary{
		RunID:       "empty-run",
		GeneratedAt: time.Now(),
		Sections:    []Section{{Name: "Projects"}, {Name: "Quality Gates"}},
	}

	pdfBytes, err := RenderPDF(summary)
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if string(pdfBytes[:5]) != "%PDF-" {
		t.Error("expected valid PDF")
	}
}

func TestGeneratePDFReport(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "test-run-01")
	os.MkdirAll(runDir, 0o755)

	// Write some task data
	writeTaskJSONL(t, runDir, "createProjects", []map[string]any{
		{"name": "Proj1", "sonarcloud_org_key": "org1", "cloud_project_key": "org1_proj1"},
	})

	pdfPath, err := GeneratePDFReport(runDir, dir, dir)
	if err != nil {
		t.Fatalf("GeneratePDFReport: %v", err)
	}

	if filepath.Base(pdfPath) != "migration_summary.pdf" {
		t.Errorf("expected migration_summary.pdf, got %s", filepath.Base(pdfPath))
	}

	data, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("reading PDF: %v", err)
	}
	if string(data[:5]) != "%PDF-" {
		t.Error("output file is not a valid PDF")
	}
}

func TestTruncate(t *testing.T) {
	if truncate("short", 10) != "short" {
		t.Error("should not truncate short string")
	}
	got := truncate("this is a long string", 10)
	if got != "this is..." {
		t.Errorf("expected 'this is...', got %q", got)
	}
}

func TestBuildUnifiedRowsOrdering(t *testing.T) {
	section := Section{
		Name: "Quality Gates",
		Succeeded: []EntityItem{
			{Name: "GateA", Organization: "org1", Detail: "42"},
		},
		Partial: []EntityItem{
			{Name: "GateB", Organization: "org1", Detail: "43", Issues: []string{"Add condition: foo"}},
		},
		Failed: []EntityItem{
			{Name: "GateC", Organization: "org1", ErrorMessage: "boom"},
		},
		Skipped: []EntityItem{
			{Name: "GateD", SkipReason: SkipReasonUnused, Detail: "Not used"},
			{Name: "GateE", SkipReason: SkipReasonBuiltIn, Detail: "Built-in"},
		},
	}

	rows := buildUnifiedRows(section, false)
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
	expectedOutcomes := []string{
		outcomeSuccess, outcomePartial, outcomeFailed, outcomeSkipped, outcomeSkipped,
	}
	for i, want := range expectedOutcomes {
		if rows[i].outcome != want {
			t.Errorf("row %d: expected outcome %q, got %q", i, want, rows[i].outcome)
		}
	}
	// Skipped ordering: built-in comes before unused per skipReasonOrder.
	if rows[3].name != "GateE" {
		t.Errorf("expected built-in skipped first, got name %q", rows[3].name)
	}
	if rows[4].name != "GateD" {
		t.Errorf("expected unused skipped second, got name %q", rows[4].name)
	}
	// Quality Gates hide the opaque cloud gate id from Details
	// (same treatment as Quality Profiles and Portfolios), so the
	// partial row should render only the Issues lines.
	if rows[1].details != "Add condition: foo" {
		t.Errorf("expected partial details to be issues only (cloud key suppressed for QG), got %q", rows[1].details)
	}
}

func TestSanitizeForPDF(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Plain ASCII", "Plain ASCII"},
		{"Café — déjà vu", "Café — déjà vu"},        // BMP non-ASCII preserved
		{"Ürümqi 北京 αβγ", "Ürümqi 北京 αβγ"},        // BMP non-ASCII preserved
		{"🥇 1 - Corp Gold", "? 1 - Corp Gold"},     // astral emoji replaced
		{"🥇🥈🥉", "???"},                            // multi-astral
		{"a🥇b", "a?b"},
	}
	for _, c := range cases {
		got := sanitizeForPDF(c.in)
		if got != c.want {
			t.Errorf("sanitizeForPDF(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnifiedRowDisplayNameWithLanguage(t *testing.T) {
	row := unifiedRow{name: "Custom", language: "java"}
	if got := row.displayName(); got != "java / Custom" {
		t.Errorf("expected 'java / Custom', got %q", got)
	}
	row2 := unifiedRow{name: "Gate"}
	if got := row2.displayName(); got != "Gate" {
		t.Errorf("expected 'Gate', got %q", got)
	}
}

func TestSuccessDetailsScanHistory(t *testing.T) {
	got := successDetails(EntityItem{Detail: "proj1|scan:failed"}, false, false, false)
	if !strings.Contains(got, "proj1") || !strings.Contains(got, "Failed") {
		t.Errorf("expected scan history in details, got %q", got)
	}
}

func TestSuccessDetailsLabelProjectKey(t *testing.T) {
	// labelProjectKey=true wraps the cloud key with the inline bold
	// markers and prepends the human-readable label so the Projects
	// section renders "New Project Key: <bold key>" in the PDF.
	got := successDetails(EntityItem{Detail: "acme_proj-a"}, false, false, true)
	want := "New Project Key: " + inlineBoldStart + "acme_proj-a" + inlineBoldEnd
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
	// labelProjectKey=false keeps the bare key (legacy behaviour for
	// non-Projects sections that still show a cloud key).
	plain := successDetails(EntityItem{Detail: "acme_proj-a"}, false, false, false)
	if plain != "acme_proj-a" {
		t.Errorf("expected bare key, got %q", plain)
	}
}

func TestPartialDetailsSplitsScanMarker(t *testing.T) {
	// Regression: a Partial / NearPerfect project carrying a |scan: marker
	// in its Detail must have the marker split off onto its own
	// "scan history:" line (like Succeeded rows) so the inline-bold span
	// around the cloud key stays balanced and the issue lines are kept.
	// Previously the raw marker was embedded inside the bold key, which a
	// downstream re-split (the Markdown renderer) truncated — dropping the
	// closing bold marker and the issue text.
	got := partialDetails(
		EntityItem{Detail: "proj1|scan:success", Issues: []string{"per-branch NCD dropped"}},
		false, false, true,
	)
	if strings.Contains(got, "|scan:") {
		t.Errorf("raw scan marker leaked into details: %q", got)
	}
	// Cloud key bold span must be balanced (one open, one close).
	if strings.Count(got, inlineBoldStart) != 1 || strings.Count(got, inlineBoldEnd) != 1 {
		t.Errorf("unbalanced inline-bold markers: %q", got)
	}
	if !strings.Contains(got, "New Project Key: "+inlineBoldStart+"proj1"+inlineBoldEnd) {
		t.Errorf("expected labeled+bold cloud key, got %q", got)
	}
	if !strings.Contains(got, "scan history:") {
		t.Errorf("expected scan-history line, got %q", got)
	}
	if !strings.Contains(got, "per-branch NCD dropped") {
		t.Errorf("expected issue line preserved, got %q", got)
	}
}

func TestToPredictiveTense(t *testing.T) {
	cases := []struct {
		in         string
		predictive bool
		want       string
	}{
		// Actual report: never rewrite.
		{"AI Code Fix was turned off instead.", false, "AI Code Fix was turned off instead."},
		{"Applied (value=ENABLED_FOR_ALL_PROJECTS)", false, "Applied (value=ENABLED_FOR_ALL_PROJECTS)"},

		// Predictive: past → future.
		{"AI Code Fix was turned off instead.", true, "AI Code Fix will be turned off instead."},
		{"The LLM was changed to GPT-5.1.", true, "The LLM will be changed to GPT-5.1."},
		{"Applied (value=ENABLED_FOR_ALL_PROJECTS)", true, "Will be applied (value=ENABLED_FOR_ALL_PROJECTS)"},
		{"Applied to all projects (value=foo)", true, "Will be applied to all projects (value=foo)"},
		{"Migrated to sonar.branch.longLivedBranches.regex on SonarQube Cloud", true, "Will be migrated to sonar.branch.longLivedBranches.regex on SonarQube Cloud"},
		{"Setting has been applied for each project instead.", true, "Setting will be applied for each project instead."},
		{"3 SQS applications were not migrated.", true, "3 SQS applications will not be migrated."},
		// Empty input passes through.
		{"", true, ""},
	}
	for _, c := range cases {
		got := toPredictiveTense(c.in, c.predictive)
		if got != c.want {
			t.Errorf("toPredictiveTense(%q, %v) = %q, want %q", c.in, c.predictive, got, c.want)
		}
	}
}

func TestToPredictiveTenseAppliedValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Applied value=duplex*,triplex*", "Will be applied value=duplex*,triplex*"},
		{"Applied value=**/jacoco*.xml to all projects", "Will be applied value=**/jacoco*.xml to all projects"},
		{"Applied value=.html to 1 of 2 projects (failed: acme_projA)", "Will be applied value=.html to 1 of 2 projects (failed: acme_projA)"},
	}
	for _, c := range cases {
		got := toPredictiveTense(c.in, true)
		if got != c.want {
			t.Errorf("toPredictiveTense(%q, true) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Issue #302: long inline-bold values must wrap inside the cell
// instead of bleeding past the page right margin and continuing at
// the page left margin (which used to clobber the Setting Key column
// in the next row).
func TestWrapInlineBoldLines_LongValueStaysInCell(t *testing.T) {
	// Set up a minimal pdf with the embedded fonts so GetStringWidth
	// returns realistic mm widths.
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()

	longVal := "**/lib/**,**/vendor/**,**/node_modules/**,**/build/**,**/dist/**,**/.gradle/**,**/target/**,**/.tox/**,**/__pycache__/**"
	text := "Applied value=" + inlineBoldStart + longVal + inlineBoldEnd

	const cellWidth = 80.0
	const fontSize = 6.0

	phys := wrapInlineBoldLines(pdf, text, cellWidth, fontSize)
	if len(phys) < 2 {
		t.Fatalf("expected the value to wrap to multiple physical lines, got %d", len(phys))
	}
	// Every line's rendered width must fit within the cell — otherwise
	// pdf.Write would still trigger its page-margin wrap at render time.
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
			t.Errorf("physical line %d width %.2f exceeds cellWidth %.2f", i, lineW, cellWidth)
		}
	}
}

// Style is preserved across the wrap boundary — the "Applied value="
// prefix stays regular, the bold value stays bold even when the wrap
// point falls inside it.
func TestWrapInlineBoldLines_PreservesStyleAcrossWrap(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()

	longBold := strings.Repeat("ABCDEF,", 30)
	text := "Applied value=" + inlineBoldStart + longBold + inlineBoldEnd

	phys := wrapInlineBoldLines(pdf, text, 60.0, 6.0)
	if len(phys) < 2 {
		t.Fatalf("expected multi-line wrap, got %d", len(phys))
	}
	// First line: should start with a regular "Applied value=" segment.
	if len(phys[0]) == 0 || phys[0][0].bold {
		t.Errorf("first physical line should start regular, got %+v", phys[0])
	}
	// Some later line should still be bold (the trailing value continues).
	sawBold := false
	for _, segs := range phys[1:] {
		for _, s := range segs {
			if s.bold {
				sawBold = true
			}
		}
	}
	if !sawBold {
		t.Error("bold attribute should carry across the wrap to the trailing lines")
	}
}

// A short value still renders on a single physical line — the new
// wrap path mustn't regress the common case.
func TestWrapInlineBoldLines_ShortValueOneLine(t *testing.T) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	registerUnicodeFont(pdf)
	pdf.AddPage()

	text := "Applied value=" + inlineBoldStart + "true" + inlineBoldEnd
	phys := wrapInlineBoldLines(pdf, text, 80.0, 6.0)
	if len(phys) != 1 {
		t.Errorf("short value should fit on one line, got %d", len(phys))
	}
}
