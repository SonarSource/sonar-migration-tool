package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

	rows := buildUnifiedRows(section)
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
	if rows[1].details != "43 — Add condition: foo" {
		t.Errorf("expected partial details to combine cloud key + issues, got %q", rows[1].details)
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
	got := successDetails(EntityItem{Detail: "proj1|scan:failed"})
	if !strings.Contains(got, "proj1") || !strings.Contains(got, "Failed") {
		t.Errorf("expected scan history in details, got %q", got)
	}
}
