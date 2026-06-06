// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// sectionsSortedByName lists section names whose unified table should be
// sorted alphabetically by the Name column rather than grouped by outcome.
var sectionsSortedByName = map[string]bool{
	"Quality Gates":   true,
	"Groups":          true,
	"Portfolios":      true,
	"Projects":        true,
	"Global Settings": true,
}

// Color constants for the PDF report.
//
// Issue #167 introduced the Sonar-branded palette below. The legacy
// "Med/Dark Blue" names are aliased to the new Sonar colours rather
// than removed so the (many) existing callers keep compiling without
// per-call rewrites. Status colours (green/yellow/amber/red/grey) are
// unchanged — they convey outcome semantics, not branding.
var (
	// Sonar brand palette (issue #167).
	colorSonarBlue      = [3]int{0x12, 0x6e, 0xd3} // #126ed3 — primary brand, table & summary headers
	colorSonarLightBlue = [3]int{0xb7, 0xd3, 0xf2} // #b7d3f2 — title banner + footer band
	colorSonarGrey      = [3]int{0x9e, 0x9e, 0x9e} // #9e9e9e — table cell borders

	// Aliases retained for compatibility with existing rendering code.
	// Anything currently using colorDarkBlue / colorMedBlue picks up
	// the Sonar primary; legacy "off-white" alternation stays.
	colorDarkBlue  = colorSonarBlue
	colorMedBlue   = colorSonarBlue
	colorLightGray = [3]int{245, 245, 245}
	colorWhite     = [3]int{255, 255, 255}
	colorBlack     = [3]int{0, 0, 0}

	// Outcome colours.
	colorGreen    = [3]int{34, 139, 34}
	colorYellow   = [3]int{180, 150, 0} // near-perfect (issue #224 yellow status)
	colorRed      = [3]int{200, 0, 0}
	colorAmber    = [3]int{200, 110, 0} // partial migration (issue #224 orange status)
	colorDarkGray = [3]int{90, 90, 90}
)

// Skip reason display order and labels.
var skipReasonOrder = []struct {
	Reason string
	Label  string
}{
	{SkipReasonOrgSkipped, "Organization skipped"},
	{SkipReasonBuiltIn, "Built-in"},
	{SkipReasonUnused, "Unused"},
	{SkipReasonEmpty, "Empty (no projects)"},
	{"", "Other"},
	// SQS-only settings appear last so the section ends with the
	// "not applicable on SQC" notes — one row per such setting,
	// no Organization (issue #200).
	{SkipReasonSQSOnly, "Not applicable on SonarQube cloud"},
	// Default-value settings (#244) — one row per setting in the
	// SQS list_definitions catalog that was left untouched on the
	// source. Rendered after SQS-only so the catalog inventory
	// trails the "needs attention" rows.
	{SkipReasonDefaultValue, "Left at default value on SonarQube Server"},
}

// Outcome labels used in the unified per-section table.
const (
	outcomeSuccess     = "Perfect"
	outcomeNearPerfect = "Near Perfect"
	outcomePartial     = "Partial"
	outcomeFailed      = "Failed"
	outcomeSkipped     = "Skipped"
)

// RenderPDF builds a PDF document from the migration summary and returns the bytes.
func RenderPDF(summary *MigrationSummary) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "Letter", "")
	pdf.SetAutoPageBreak(true, 20)

	registerUnicodeFont(pdf)
	registerLogoImages(pdf)

	// Sonar grey cell borders for every CellFormat / Rect call in the
	// document (#167). SetDrawColor is sticky in fpdf, so setting it
	// once at document creation suffices — individual renderers only
	// need to override it if they want a non-Sonar-grey border.
	pdf.SetDrawColor(colorSonarGrey[0], colorSonarGrey[1], colorSonarGrey[2])

	// Body content on every page starts below the header band so the
	// table top borders don't collide with the Sonar logo. headerBandH
	// gives ~4mm of breathing room past the rasterised logo height
	// (9.5mm scaled) plus the band's top inset.
	pdf.SetTopMargin(headerBandH)

	pdf.SetHeaderFuncMode(func() {
		addPageHeader(pdf, summary.RunID, summary.GeneratedAt)
	}, true)
	pdf.SetFooterFunc(func() {
		addPageFooter(pdf)
	})

	pdf.AddPage()
	renderTitlePage(pdf, summary)

	if summary.RateLimit != nil {
		renderRateLimitWarning(pdf, summary.RateLimit)
	}

	for _, section := range summary.Sections {
		if summary.OmitSections[section.Name] {
			continue
		}
		renderSection(pdf, section, summary.Predictive)
	}

	renderLimitations(pdf, summary.Limitations, summary.Predictive)

	// Runtime telemetry sections (#240+). Each renderer no-ops when its
	// input is empty / zero, so predictive reports — which never populate
	// these fields — are completely unaffected. Rendered in this fixed
	// order after the limitations section.
	renderRunMetadata(pdf, summary)
	renderBottlenecks(pdf, summary)
	renderFailureLedger(pdf, summary)
	renderWarningsLedger(pdf, summary)
	renderBranchProjectData(pdf, summary)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("generating PDF: %w", err)
	}
	return buf.Bytes(), nil
}

// renderLimitations writes the "Migration limitations" section at the
// very end of the report (issue #154). Each entry is rendered as a
// single bulleted line. No-op when the list is empty so reports for
// instances without any limitation stay clean.
func renderLimitations(pdf *fpdf.Fpdf, limitations []string, predictive bool) {
	if len(limitations) == 0 {
		return
	}
	pdf.Ln(8)
	checkPageBreak(pdf, 30)

	setColor(pdf, colorSonarBlue)
	pdf.SetFont(pdfFontFamilyHeading, "B", 14)
	pdf.CellFormat(0, 10, "Migration limitations", "", 1, "L", false, 0, "")

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamilyBody, "", 9)
	for _, line := range limitations {
		// MultiCell so long messages wrap instead of running off
		// the edge. Bullet prefix mirrors the visual treatment used
		// by other free-text sections.
		pdf.MultiCell(0, 5, "• "+sanitizeForPDF(toPredictiveTense(line, predictive)), "", "L", false)
	}
}

// registerUnicodeFont registers every embedded TTF (NotoSans, Poppins,
// Inter) so SetFont(<family>, ...) can switch families per usage:
// Poppins for headings (#167), Inter for body cells (#167), NotoSans as
// the Unicode-coverage fallback for legacy callers and any string the
// Sonar-branded fonts can't render. Embedded once at PDF creation so
// the per-cell SetFont calls don't repay registration cost.
func registerUnicodeFont(pdf *fpdf.Fpdf) {
	pdf.AddUTF8FontFromBytes(pdfFontFamily, "", notoSansRegular)
	pdf.AddUTF8FontFromBytes(pdfFontFamily, "B", notoSansBold)
	pdf.AddUTF8FontFromBytes(pdfFontFamilyHeading, "", poppinsRegular)
	pdf.AddUTF8FontFromBytes(pdfFontFamilyHeading, "B", poppinsBold)
	pdf.AddUTF8FontFromBytes(pdfFontFamilyBody, "", interRegular)
	pdf.AddUTF8FontFromBytes(pdfFontFamilyBody, "B", interBold)
}

// registerLogoImages registers the embedded Sonar logo PNGs with fpdf
// under fixed names so the header/footer functions can stamp them on
// every page via pdf.ImageOptions(name, ...). Registration is
// idempotent within an fpdf document, but we still call it once at
// document creation to keep first-page rendering free of an extra
// allocation.
func registerLogoImages(pdf *fpdf.Fpdf) {
	pdf.RegisterImageOptionsReader("sonar-logo-header",
		fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false},
		bytes.NewReader(sonarLogoHeader))
	pdf.RegisterImageOptionsReader("sonar-logo-footer",
		fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false},
		bytes.NewReader(sonarLogoFooter))
}

// pageMarginLeft / pageMarginRight mirror fpdf's default Letter margins
// so the header / footer logos and bands line up with the table edges.
const (
	pageMarginLeft  = 10.0
	pageMarginRight = 10.0
	// Header logo footprint. The upstream Sonar wordmark is 1010×320,
	// which we scale to a width of 30mm here — height auto-scales via
	// pdf.ImageOptions's h=0 convention. Used on page 1 only.
	headerLogoWidth = 30.0
	// From page 2 onward the header carries the small standalone Sonar
	// glyph (77×78) instead of the full wordmark — keeps the band low
	// and leaves room on the right for the run id + timestamp.
	headerGlyphWidth = 8.0
	// Footer logo footprint. The standalone Sonar glyph is 77×78 so
	// we keep it small; the footer height accommodates the glyph plus
	// the page-number text on the same band.
	footerLogoWidth = 7.0
	footerBandH     = 12.0
	// headerBandH is the vertical space reserved above the body on
	// every page so tables / banners never collide with the page
	// header. Generous on page 1 (where the wordmark dominates) and
	// already-comfortable for subsequent pages where only the small
	// glyph + run id sit in the band.
	headerBandH = 22.0
)

// addPageHeader stamps a Sonar-branded band at the top of every page
// (#167). Page 1 carries the full Sonar wordmark, mirroring the title
// page mockup; pages 2+ switch to the standalone Sonar glyph on the
// left with the Run ID and generation timestamp right-aligned, both in
// small Inter so the band stays a single visual line.
func addPageHeader(pdf *fpdf.Fpdf, runID string, generatedAt time.Time) {
	pageW, _ := pdf.GetPageSize()

	if pdf.PageNo() == 1 {
		pdf.ImageOptions("sonar-logo-header", pageMarginLeft, 6,
			headerLogoWidth, 0, false,
			fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
		pdf.SetY(headerBandH)
		return
	}

	// Page 2+ header: small Sonar glyph left, Run ID + timestamp right.
	glyphY := 6.0
	pdf.ImageOptions("sonar-logo-footer", pageMarginLeft, glyphY,
		headerGlyphWidth, 0, false,
		fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	pdf.SetFont(pdfFontFamilyBody, "", 8)
	setColor(pdf, colorDarkGray)
	rightText := fmt.Sprintf("Run ID: %s  |  Generated: %s",
		runID, generatedAt.Format("2006-01-02 15:04:05"))
	textY := glyphY + (headerGlyphWidth-4)/2
	rightW := pageW - 2*pageMarginRight
	pdf.SetXY(pageMarginLeft, textY)
	pdf.CellFormat(rightW, 5, rightText, "", 0, "R", false, 0, "")
	setColor(pdf, colorBlack)

	pdf.SetY(headerBandH)
}

// addPageFooter renders the Sonar-branded footer band (#167):
//   - light-blue full-width background (#b7d3f2)
//   - Sonar glyph + "© <year>, SonarSource Sàrl" on the left, Inter 8pt
//   - "Page <n>" on the right, Inter 8pt
//
// The band sits flush against the bottom margin so it visually frames
// the page like the mockup in #167.
func addPageFooter(pdf *fpdf.Fpdf) {
	pageW, pageH := pdf.GetPageSize()
	bandY := pageH - footerBandH - 2

	// Light-blue band.
	setFillColor(pdf, colorSonarLightBlue)
	pdf.Rect(0, bandY, pageW, footerBandH, "F")

	// Sonar glyph, centred vertically in the band, with a small left
	// inset. h=0 means "preserve aspect ratio".
	glyphY := bandY + (footerBandH-footerLogoWidth)/2
	pdf.ImageOptions("sonar-logo-footer", pageMarginLeft, glyphY,
		footerLogoWidth, 0, false,
		fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

	// Copyright text — Inter 8pt, vertically centred.
	pdf.SetFont(pdfFontFamilyBody, "", 8)
	setColor(pdf, colorBlack)
	copyText := fmt.Sprintf("© %d, SonarSource Sàrl", time.Now().Year())
	textW := pageW - 2*pageMarginLeft - footerLogoWidth - 4
	pdf.SetXY(pageMarginLeft+footerLogoWidth+4, bandY+(footerBandH-5)/2)
	pdf.CellFormat(textW/2, 5, copyText, "", 0, "L", false, 0, "")

	// Page number on the right.
	pdf.SetXY(pageW/2, bandY+(footerBandH-5)/2)
	pdf.CellFormat(pageW/2-pageMarginRight, 5,
		fmt.Sprintf("Page %d", pdf.PageNo()), "", 0, "R", false, 0, "")
}

// renderTitleBanner draws the Sonar-branded title banner (#167): full-
// width light-blue (#b7d3f2) background with the report title centred
// inside it. The predictive variant renders "SonarQube Migration
// Predictive Report" with "Predictive" underlined; the actual variant
// renders "SonarQube Migration Report" with no underline. Title font
// is Poppins Bold.
func renderTitleBanner(pdf *fpdf.Fpdf, predictive bool) {
	const fontSize = 22.0
	const bannerH = 18.0
	const lineH = 12.0

	pageW, _ := pdf.GetPageSize()
	y := pdf.GetY()

	// Light-blue background, full page width.
	setFillColor(pdf, colorSonarLightBlue)
	pdf.Rect(0, y, pageW, bannerH, "F")

	pdf.SetFont(pdfFontFamilyHeading, "B", fontSize)
	setColor(pdf, colorSonarBlue)

	prefix := "SonarQube Migration "
	var middle, suffix string
	if predictive {
		middle, suffix = "Predictive", " Report"
	} else {
		// Whole title is plain bold; no underlined segment.
		prefix = "SonarQube Migration Report"
	}

	pdf.SetFont(pdfFontFamilyHeading, "B", fontSize)
	prefixW := pdf.GetStringWidth(prefix)
	var middleW, suffixW float64
	if predictive {
		pdf.SetFont(pdfFontFamilyHeading, "BU", fontSize)
		middleW = pdf.GetStringWidth(middle)
		pdf.SetFont(pdfFontFamilyHeading, "B", fontSize)
		suffixW = pdf.GetStringWidth(suffix)
	}
	totalW := prefixW + middleW + suffixW
	startX := (pageW - totalW) / 2
	textY := y + (bannerH-lineH)/2

	pdf.SetXY(startX, textY)
	pdf.SetFont(pdfFontFamilyHeading, "B", fontSize)
	pdf.Write(lineH, prefix)
	if predictive {
		pdf.SetFont(pdfFontFamilyHeading, "BU", fontSize)
		pdf.Write(lineH, middle)
		pdf.SetFont(pdfFontFamilyHeading, "B", fontSize)
		pdf.Write(lineH, suffix)
	}
	pdf.SetY(y + bannerH)
}

func renderTitlePage(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	pdf.SetY(28)
	renderTitleBanner(pdf, summary.Predictive)
	pdf.Ln(6)

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamilyBody, "", 11)
	pdf.CellFormat(0, 7, "Run ID: "+summary.RunID, "", 1, "C", false, 0, "")
	pdf.CellFormat(0, 7, "Generated: "+summary.GeneratedAt.Format("2006-01-02 15:04:05"), "", 1, "C", false, 0, "")
	pdf.Ln(8)

	renderExecutiveSummary(pdf, summary.Sections, summary.OmitSections)
}

// renderExecutiveSummary renders the six-column summary table at the top of
// the report: Objects | Perfect | Near Perfect | Partial | Failed | Skipped.
// The five status buckets follow the green/yellow/orange/red/grey taxonomy
// defined in issues #224 and #227; colours are conveyed by the count cells.
// Sections present in `omit` are skipped entirely (predictive reports
// omit "Global Settings", #235).
func renderExecutiveSummary(pdf *fpdf.Fpdf, sections []Section, omit map[string]bool) {
	headers := []string{"Objects", "Perfect", "Near Perfect", "Partial", "Failed", "Skipped"}
	const (
		objectsWidth = 45.0
		countWidth   = 30.0
	)
	widths := []float64{objectsWidth, countWidth, countWidth, countWidth, countWidth, countWidth}

	// Header row — Poppins Bold on the Sonar primary background (#167).
	pdf.SetDrawColor(colorSonarGrey[0], colorSonarGrey[1], colorSonarGrey[2])
	setFillColor(pdf, colorSonarBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamilyHeading, "B", 10)
	for i, h := range headers {
		align := "C"
		if i == 0 {
			align = "L"
		}
		pdf.CellFormat(widths[i], 8, h, "1", 0, align, true, 0, "")
	}
	pdf.Ln(-1)

	// Body rows — Inter for readable counts.
	pdf.SetFont(pdfFontFamilyBody, "", 10)
	var totalPerfect, totalYellow, totalOrange, totalRed, totalGrey int
	rowIdx := 0
	for _, sec := range sections {
		if omit[sec.Name] {
			continue
		}
		perfect := len(sec.Succeeded)
		yellow := len(sec.NearPerfect)
		orange := len(sec.Partial)
		red := len(sec.Failed)
		grey := len(sec.Skipped)
		totalPerfect += perfect
		totalYellow += yellow
		totalOrange += orange
		totalRed += red
		totalGrey += grey

		if rowIdx%2 == 0 {
			setFillColor(pdf, colorLightGray)
		} else {
			setFillColor(pdf, colorWhite)
		}
		rowIdx++
		setColor(pdf, colorBlack)
		pdf.CellFormat(widths[0], 7, sec.Name, "1", 0, "L", true, 0, "")
		renderCountCell(pdf, widths[1], perfect, colorGreen)
		renderCountCell(pdf, widths[2], yellow, colorYellow)
		renderCountCell(pdf, widths[3], orange, colorAmber)
		renderCountCell(pdf, widths[4], red, colorRed)
		renderCountCell(pdf, widths[5], grey, colorDarkGray)
		pdf.Ln(-1)
	}

	// Totals row — matches the header styling (Poppins Bold on #126ed3).
	setFillColor(pdf, colorSonarBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamilyHeading, "B", 10)
	pdf.CellFormat(widths[0], 8, "Total", "1", 0, "L", true, 0, "")
	pdf.CellFormat(widths[1], 8, itoa(totalPerfect), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[2], 8, itoa(totalYellow), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[3], 8, itoa(totalOrange), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[4], 8, itoa(totalRed), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[5], 8, itoa(totalGrey), "1", 0, "C", true, 0, "")
	pdf.Ln(-1)
}

func renderCountCell(pdf *fpdf.Fpdf, w float64, count int, color [3]int) {
	if count > 0 {
		pdf.SetTextColor(color[0], color[1], color[2])
	} else {
		setColor(pdf, colorBlack)
	}
	pdf.CellFormat(w, 7, itoa(count), "1", 0, "C", true, 0, "")
}

func renderSection(pdf *fpdf.Fpdf, section Section, predictive bool) {
	total := len(section.Succeeded) + len(section.NearPerfect) + len(section.Partial) + len(section.Failed) + len(section.Skipped)
	if total == 0 {
		return
	}

	pdf.Ln(8)
	checkPageBreak(pdf, 30)

	// Section header — Poppins Bold, Sonar primary (#167).
	setColor(pdf, colorSonarBlue)
	pdf.SetFont(pdfFontFamilyHeading, "B", 14)
	pdf.CellFormat(0, 10, section.Name, "", 1, "L", false, 0, "")

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamilyBody, "", 9)
	// MultiCell so long count summaries — particularly Global Settings
	// where the skipped breakdown can reach 150+ chars — wrap onto a
	// second line instead of running off the right page edge.
	pdf.MultiCell(0, 5, sectionCountSummary(section), "", "L", false)
	pdf.Ln(2)

	renderUnifiedTable(pdf, section, predictive)
}

// unifiedRow is one row in the per-section unified table.
type unifiedRow struct {
	name     string
	language string // populated for Quality Profiles
	org      string
	outcome  string
	color    [3]int
	details  string
}

// displayName returns the row's name as displayed in the Name column.
// For Quality Profiles rows (language != ""), it prefixes the language.
func (r unifiedRow) displayName() string {
	if r.language != "" {
		return r.language + " / " + r.name
	}
	return r.name
}

// sectionsWithoutOrganization lists sections rendered without an Organization
// column. Portfolios are created at the enterprise level, so an organization
// dimension is not meaningful.
var sectionsWithoutOrganization = map[string]bool{
	"Portfolios": true,
}

// renderUnifiedTable renders the per-section table:
//
//	Name | Organization | Outcome (colored) | Details
//
// For sections in sectionsWithoutOrganization (and for the predictive
// report, #240, where org mapping isn't meaningful), the Organization
// column is dropped — producing a 3-column table: Name | Outcome |
// Details.
func renderUnifiedTable(pdf *fpdf.Fpdf, section Section, predictive bool) {
	hideOrg := predictive || sectionsWithoutOrganization[section.Name]

	nameHeader := "Name"
	isGlobalSettings := section.Name == "Global Settings"
	if isGlobalSettings {
		nameHeader = "Setting Key"
	}
	// Predictive-only column re-proportioning: when the Organization
	// column is dropped (predict mode) the freed width was going to
	// the Name column, leaving huge whitespace next to short names
	// and crowding the Details column. Narrow Name to ~30mm and
	// spend the rest on Details so long Issues blocks wrap less
	// aggressively. Global Settings keeps its wider Name column for
	// long setting keys, and the actual-migrate layout is unchanged.
	headers := []string{nameHeader, "Organization", "Outcome", "Details"}
	var widths []float64
	switch {
	case isGlobalSettings && hideOrg:
		// Predictive Global Settings: narrower Setting Key, wider Details.
		widths = []float64{72, 25, 99}
	case isGlobalSettings:
		widths = []float64{55, 35, 25, 81}
	case predictive:
		// Predict always drops the Organization column.
		widths = []float64{60, 25, 111}
	case hideOrg:
		// Real-migrate Portfolios — narrower Name, wider Details.
		widths = []float64{63, 25, 108}
	default:
		// Real-migrate standard 4-column layout.
		widths = []float64{55, 35, 25, 81}
	}
	if hideOrg {
		headers = []string{nameHeader, "Outcome", "Details"}
	}

	renderTableHeader(pdf, headers, widths)

	rows := buildUnifiedRows(section, predictive)
	if section.Name == "Quality Profiles" {
		sort.SliceStable(rows, func(i, j int) bool {
			li, lj := strings.ToLower(rows[i].language), strings.ToLower(rows[j].language)
			if li != lj {
				return li < lj
			}
			return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
		})
	} else if sectionsSortedByName[section.Name] {
		sort.SliceStable(rows, func(i, j int) bool {
			return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
		})
	}

	// Single-line rows render at the original 6mm height; when the Details
	// text wraps, each extra line is set to a tighter 3.0mm so the row does
	// not balloon vertically. Multi-line details (typically metric mapping
	// notes) also drop to a smaller 6pt font so the per-line cost stays low.
	const (
		singleLineH = 6.0
		// wrappedLineH at 3.0mm was too tight for the 8pt body font
		// used in the Name column: descenders on g/p/y/j were clipped
		// for long wrapping setting keys (issue #207, example
		// sonar.java.ignoreUnnamedModuleForSplitPackage). 4.0mm gives
		// 8pt enough leading while staying readable for the 6pt
		// multi-line details column.
		wrappedLineH      = 4.0
		bodyFontSize      = 8.0
		multiLineFontSize = 6.0
	)
	// Body cells render in Inter per #167; the Outcome bold and
	// per-row Details inherit the family unless overridden.
	pdf.SetFont(pdfFontFamilyBody, "", bodyFontSize)
	// All sections word-wrap the Name column now (#226 follow-up).
	// Long object names / QP names / setting keys overflow the
	// narrowed Name column otherwise; wrapping keeps the value fully
	// visible at the cost of taller rows.
	wrapName := true
	for i, row := range rows {
		// Compute wrapped line count for the Details column so the whole row
		// (Name, Organization, Outcome) can match that height. SplitLines
		// already accounts for the cell's internal margin.
		detailsText := sanitizeForPDF(row.details)
		detailsCol := len(widths) - 1
		multiLine := strings.Contains(detailsText, "\n")
		_ = multiLine // retained for clarity; details now always use the smaller font
		// Always render the Details column at the smaller (6pt) font —
		// it carries info-dense notes that benefit from more density
		// alongside the 8pt Name and Outcome columns. Applies to both
		// the real-migrate and predictive reports.
		detailsFontSize := multiLineFontSize
		pdf.SetFont(pdfFontFamilyBody, "", detailsFontSize)
		// When the details carry inline bold markers (#167) the
		// drawDetailsCell renderer does its own width-aware wrap that
		// accounts for the regular-vs-bold glyph width difference.
		// Use the same wrap here for the line count so the row height
		// matches what gets drawn — pdf.SplitLines treats the markers
		// as opaque glyphs and miscounts on long bold values (#302).
		const detailsCellPad = 1.0
		var detailsLineCount int
		if strings.Contains(detailsText, inlineBoldStart) {
			detailsLineCount = len(wrapInlineBoldLines(pdf, detailsText, widths[detailsCol]-detailsCellPad, detailsFontSize))
		} else {
			detailsLineCount = len(pdf.SplitLines([]byte(detailsText), widths[detailsCol]))
		}
		pdf.SetFont(pdfFontFamilyBody, "", bodyFontSize)
		if detailsLineCount < 1 {
			detailsLineCount = 1
		}
		// If the Name column word-wraps, compute its line count too
		// and take the max so the row is tall enough for either side.
		// Sanitize astral-plane runes (emoji, etc.) BEFORE measuring
		// or rendering — the wrap path uses pdf.SplitLines / MultiCell
		// directly without going through truncate(), which was the
		// previous sanitizer entry point for the Name column.
		nameText := sanitizeForPDF(row.displayName())
		nameLineCount := 1
		if wrapName {
			nameLineCount = len(pdf.SplitLines([]byte(nameText), widths[0]))
			if nameLineCount < 1 {
				nameLineCount = 1
			}
		}
		lineCount := detailsLineCount
		if nameLineCount > lineCount {
			lineCount = nameLineCount
		}
		var lineH, rowHeight float64
		if lineCount == 1 {
			lineH = singleLineH
			rowHeight = singleLineH
		} else {
			lineH = wrappedLineH
			rowHeight = float64(lineCount) * wrappedLineH
		}

		checkPageBreak(pdf, rowHeight)
		// Capture the row's start position so we can explicitly
		// advance to the next row after drawing all columns. The
		// original code relied on the trailing MultiCell auto-
		// advancing the cursor; drawWrappedCell deliberately does
		// not (it stays flush right of its own cell so the caller
		// can place the next column), so we restore the X+Y here.
		rowStartX := pdf.GetX()
		rowStartY := pdf.GetY()
		if i%2 == 0 {
			setFillColor(pdf, colorLightGray)
		} else {
			setFillColor(pdf, colorWhite)
		}
		// Name (+ Organization, when present) in black on alternating background.
		setColor(pdf, colorBlack)
		col := 0
		nameLimit := 36
		if hideOrg {
			nameLimit = 60
		}
		if wrapName {
			// Render the Name column as one CellFormat per row-line
			// so the cell ALWAYS fills the full row height — issue
			// #207. MultiCell with trailing-newline padding does not
			// produce extra empty rows on this fpdf fork, which left
			// the Name cell short when detailsLineCount exceeded
			// nameLineCount (cells looked uneven) and additionally
			// clipped descenders when nameLineCount matched (the per-
			// line height of 3mm was too tight for 8pt body). Drawing
			// one cell per line at the row's lineH gives consistent
			// height AND room for descenders.
			drawWrappedCell(pdf, widths[col], lineH, lineCount, pdf.SplitLines([]byte(nameText), widths[col]))
		} else {
			pdf.CellFormat(widths[col], rowHeight, truncate(nameText, nameLimit), "1", 0, "L", true, 0, "")
		}
		col++
		if !hideOrg {
			pdf.CellFormat(widths[col], rowHeight, truncate(row.org, 24), "1", 0, "L", true, 0, "")
			col++
		}
		// Outcome cell in its color, bold (Inter Bold per #167).
		setColor(pdf, row.color)
		pdf.SetFont(pdfFontFamilyBody, "B", 8)
		pdf.CellFormat(widths[col], rowHeight, row.outcome, "1", 0, "C", true, 0, "")
		col++
		// Details in black, regular — MultiCell wraps long text across lines
		// rather than truncating. lineH is per-line so the cell ends up at
		// rowHeight, matching the row. Multi-line details drop to a smaller
		// font so a long metric-mapping block doesn't visually dominate the
		// table.
		setColor(pdf, colorBlack)
		pdf.SetFont(pdfFontFamilyBody, "", detailsFontSize)
		// Render Details with the same explicit per-line approach as
		// Name so the cell always reaches rowHeight. drawDetailsCell
		// falls through to drawWrappedCell when no inline bold markers
		// are present and otherwise renders the row by hand so it can
		// switch fonts mid-line. Issue #207 / Olivier's "New Project
		// Key: <key>" follow-up.
		drawDetailsCell(pdf, widths[col], lineH, lineCount, detailsText, detailsFontSize)
		pdf.SetFont(pdfFontFamilyBody, "", bodyFontSize)
		// Advance to the next row. drawWrappedCell leaves the cursor
		// flush right of the cell at the row's start Y; explicitly
		// move to (rowStartX, rowStartY + rowHeight) so the next
		// iteration's GetXY() reports the correct position.
		pdf.SetXY(rowStartX, rowStartY+rowHeight)
	}
}

// drawDetailsCell renders the Details column for a unified-table row.
// It supports inline bold markers (inlineBoldStart / inlineBoldEnd) by
// drawing the cell border + fill manually via pdf.Rect and rendering
// the text with width-aware wrapping (#302). When the text contains
// no markers the function delegates to the regular drawWrappedCell,
// preserving the existing wrap semantics for every section that
// doesn't need inline bold.
//
// Width-aware wrap matters because pdf.Write — the only fpdf helper
// that lets us flip font style mid-line — wraps at the *page* right
// margin, not the cell's right edge. A long bold value would
// otherwise overflow and "wrap" by continuing at the page's LEFT
// margin, splashing into the next row's leftmost column (#302).
func drawDetailsCell(pdf *fpdf.Fpdf, w, lineH float64, lineCount int, text string, fontSize float64) {
	if !strings.Contains(text, inlineBoldStart) {
		drawWrappedCell(pdf, w, lineH, lineCount, pdf.SplitLines([]byte(text), w))
		return
	}
	x, y := pdf.GetXY()
	rowH := lineH * float64(lineCount)
	// Fill background + draw outer border in one Rect call. SetFillColor
	// and SetDrawColor are sticky and already match the row's expected
	// styling at this point.
	pdf.Rect(x, y, w, rowH, "FD")

	const leftPad = 1.0
	cellWidth := w - leftPad
	phys := wrapInlineBoldLines(pdf, text, cellWidth, fontSize)
	for i, segs := range phys {
		if i >= lineCount {
			break
		}
		pdf.SetXY(x+leftPad, y+float64(i)*lineH)
		writeInlineBoldLine(pdf, segs, lineH, fontSize)
	}
	pdf.SetXY(x+w, y)
}

// styledSeg is a contiguous run of characters that all share the
// same font style (regular or bold). A single physical line of a
// details cell is a slice of styledSegs in left-to-right order.
type styledSeg struct {
	text string
	bold bool
}

// splitStyledLine turns a logical line (no embedded newlines) into
// styled segments by tokenising on inlineBoldStart / inlineBoldEnd.
// Unclosed markers cause the rest of the line to render as bold so
// the operator's intent isn't lost on malformed input.
func splitStyledLine(line string) []styledSeg {
	var out []styledSeg
	parts := strings.Split(line, inlineBoldStart)
	if parts[0] != "" {
		out = append(out, styledSeg{text: parts[0], bold: false})
	}
	for _, p := range parts[1:] {
		idx := strings.Index(p, inlineBoldEnd)
		if idx < 0 {
			out = append(out, styledSeg{text: p, bold: true})
			continue
		}
		if idx > 0 {
			out = append(out, styledSeg{text: p[:idx], bold: true})
		}
		if rest := p[idx+len(inlineBoldEnd):]; rest != "" {
			out = append(out, styledSeg{text: rest, bold: false})
		}
	}
	return out
}

// tokenizeForWrap splits a segment of text into atomic break units.
// Words are kept together; whitespace and commas act as break points
// but stay attached to the preceding token so commas don't end up
// orphaned at the start of a wrapped line (most settings values are
// long CSVs with no whitespace — commas are the only natural break).
func tokenizeForWrap(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	for _, r := range s {
		cur.WriteRune(r)
		if r == ' ' || r == '\t' || r == ',' {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// wrapInlineBoldLines wraps each newline-delimited logical line in
// `text` to fit within `cellWidth` mm, accounting for the different
// glyph widths of the regular and bold body fonts. Returns one slice
// of styledSegs per physical line, ready for writeInlineBoldLine to
// render. Pure measurement; no drawing.
func wrapInlineBoldLines(pdf *fpdf.Fpdf, text string, cellWidth, fontSize float64) [][]styledSeg {
	var out [][]styledSeg
	for _, logical := range strings.Split(text, "\n") {
		segs := splitStyledLine(logical)
		out = append(out, wrapOneLogicalLine(pdf, segs, cellWidth, fontSize)...)
	}
	return out
}

// wrapOneLogicalLine performs the greedy word-wrap for a single
// logical line's worth of styledSegs. Tokens that are wider than
// cellWidth on their own (rare, but possible for path-like values
// with no separators) are hard-broken at character boundaries.
func wrapOneLogicalLine(pdf *fpdf.Fpdf, segs []styledSeg, cellWidth, fontSize float64) [][]styledSeg {
	setStyle := func(bold bool) {
		if bold {
			pdf.SetFont(pdfFontFamilyBody, "B", fontSize)
		} else {
			pdf.SetFont(pdfFontFamilyBody, "", fontSize)
		}
	}

	var out [][]styledSeg
	var line []styledSeg
	var lineW float64

	appendToken := func(tok string, bold, isHardBreak bool) {
		if !isHardBreak && len(line) > 0 && line[len(line)-1].bold == bold {
			line[len(line)-1].text += tok
		} else {
			line = append(line, styledSeg{text: tok, bold: bold})
		}
	}
	flush := func() {
		if len(line) > 0 {
			out = append(out, line)
			line = nil
			lineW = 0
		}
	}

	for _, seg := range segs {
		setStyle(seg.bold)
		for _, tok := range tokenizeForWrap(seg.text) {
			tw := pdf.GetStringWidth(tok)
			// Token fits on the current line.
			if lineW+tw <= cellWidth {
				appendToken(tok, seg.bold, false)
				lineW += tw
				continue
			}
			// Token doesn't fit. Try wrapping to a new line first.
			if lineW > 0 {
				flush()
				if tw <= cellWidth {
					appendToken(tok, seg.bold, false)
					lineW += tw
					continue
				}
			}
			// Token alone is wider than the cell — hard-break at runes.
			for _, r := range tok {
				rs := string(r)
				rw := pdf.GetStringWidth(rs)
				if lineW+rw > cellWidth && lineW > 0 {
					flush()
				}
				appendToken(rs, seg.bold, false)
				lineW += rw
			}
		}
	}
	flush()
	// Empty logical line still occupies one physical line so a stray
	// "\n\n" in the source doesn't collapse to zero rows.
	if len(out) == 0 {
		out = append(out, nil)
	}
	return out
}

// writeInlineBoldLine renders one physical line's styled segments at
// the cursor's current (x, y). Caller is responsible for positioning
// the cursor (drawDetailsCell does this per line). The line is
// guaranteed by wrapInlineBoldLines to fit within the cell, so
// pdf.Write won't trigger its own (page-margin-based) wrap.
func writeInlineBoldLine(pdf *fpdf.Fpdf, segs []styledSeg, lineH, fontSize float64) {
	for _, s := range segs {
		if s.bold {
			pdf.SetFont(pdfFontFamilyBody, "B", fontSize)
		} else {
			pdf.SetFont(pdfFontFamilyBody, "", fontSize)
		}
		if s.text != "" {
			pdf.Write(lineH, s.text)
		}
	}
}

// drawWrappedCell renders a wrapping table cell as `lineCount` stacked
// CellFormat rows so the cell's outer border ALWAYS reaches a height
// of lineCount*lineH, regardless of how many of those lines actually
// carry text. Trailing empty cells (when len(lines) < lineCount) are
// drawn with the same background fill as the text rows so the column
// reads as a single tall cell. Side borders are drawn on every row
// (L+R); the top is drawn on the first row, the bottom on the last,
// so the result is visually indistinguishable from a single bordered
// rectangle around all rows.
//
// After drawing, the cursor is left at (x+w, startY) — i.e. flush to
// the right of the cell at the row's top — so the caller can continue
// with CellFormat for the next column without an explicit SetXY.
func drawWrappedCell(pdf fpdfCell, w, lineH float64, lineCount int, lines [][]byte) {
	x, y := pdf.GetXY()
	for j := 0; j < lineCount; j++ {
		var text string
		if j < len(lines) {
			text = string(lines[j])
		}
		border := ""
		switch {
		case lineCount == 1:
			border = "1"
		case j == 0:
			border = "LRT"
		case j == lineCount-1:
			border = "LRB"
		default:
			border = "LR"
		}
		pdf.SetXY(x, y+float64(j)*lineH)
		pdf.CellFormat(w, lineH, text, border, 0, "L", true, 0, "")
	}
	pdf.SetXY(x+w, y)
}

// fpdfCell is the subset of fpdf.Fpdf used by drawWrappedCell. Defined
// as an interface so the helper can be unit-tested without spinning
// up a full PDF document.
type fpdfCell interface {
	GetXY() (float64, float64)
	SetXY(x, y float64)
	CellFormat(w, h float64, txtStr, borderStr string, ln int, alignStr string, fill bool, link int, linkStr string)
}

// buildUnifiedRows flattens the section's buckets into ordered rows:
// Succeeded → NearPerfect → Partial → Failed → Skipped (skipped grouped by
// reason order). Order mirrors the green → yellow → orange → red → grey
// taxonomy from issues #224 and #227.
//
// When predictive is true (#240), the Details column drops the
// synthetic predict:<task>:<org>:<name> cloud-id placeholder (those
// carry no useful information for prediction) and rewrites past-tense
// detail strings to the future tense (#167) so the predictive report
// reads as "will be …" while the actual report keeps "was …".
func buildUnifiedRows(section Section, predictive bool) []unifiedRow {
	var rows []unifiedRow

	successLabel := outcomeSuccess

	// Cloud-side internal ids (quality profile cloud_profile_key,
	// portfolio cloud id, group id, permission template id, etc.) are
	// opaque SQC identifiers — not user-facing and not actionable.
	// Strip them from the Details column for the sections where they
	// would otherwise dominate the cell. Permission Templates and
	// Groups added per Olivier's #167 follow-up (internal ids removed).
	hideCloudKey := section.Name == "Quality Profiles" ||
		section.Name == "Portfolios" ||
		section.Name == "Quality Gates" ||
		section.Name == "Permission Templates" ||
		section.Name == "Groups"
	// Projects gets a "New Project Key: <key>" label with the key in
	// inline bold. The bold markers survive into the PDF renderer where
	// drawDetailsCell switches font style mid-line.
	labelProjectKey := section.Name == "Projects"

	for _, item := range section.Succeeded {
		rows = append(rows, unifiedRow{
			name:     item.Name,
			language: item.Language,
			org:      item.Organization,
			outcome:  successLabel,
			color:    colorGreen,
			details:  toPredictiveTense(successDetails(item, predictive, hideCloudKey, labelProjectKey), predictive),
		})
	}
	for _, item := range section.NearPerfect {
		name := item.Name
		if name == "" {
			name = "(unknown)"
		}
		rows = append(rows, unifiedRow{
			name:     name,
			language: item.Language,
			org:      item.Organization,
			outcome:  outcomeNearPerfect,
			color:    colorYellow,
			details:  toPredictiveTense(partialDetails(item, predictive, hideCloudKey, labelProjectKey), predictive),
		})
	}
	for _, item := range section.Partial {
		name := item.Name
		if name == "" {
			name = "(unknown)"
		}
		rows = append(rows, unifiedRow{
			name:     name,
			language: item.Language,
			org:      item.Organization,
			outcome:  outcomePartial,
			color:    colorAmber,
			details:  toPredictiveTense(partialDetails(item, predictive, hideCloudKey, labelProjectKey), predictive),
		})
	}
	for _, item := range section.Failed {
		rows = append(rows, unifiedRow{
			name:     item.Name,
			language: item.Language,
			org:      item.Organization,
			outcome:  outcomeFailed,
			color:    colorRed,
			details:  toPredictiveTense(item.ErrorMessage, predictive),
		})
	}
	// Skipped — preserve group ordering by SkipReason.
	skippedGroups := make(map[string][]EntityItem)
	for _, item := range section.Skipped {
		skippedGroups[item.SkipReason] = append(skippedGroups[item.SkipReason], item)
	}
	for _, entry := range skipReasonOrder {
		for _, item := range skippedGroups[entry.Reason] {
			rows = append(rows, unifiedRow{
				name:     item.Name,
				language: item.Language,
				org:      item.Organization,
				outcome:  outcomeSkipped,
				color:    colorDarkGray,
				details:  toPredictiveTense(skippedDetails(item), predictive),
			})
		}
	}
	return rows
}

// toPredictiveTense rewrites past-tense / "Applied" / "Migrated" phrasing
// in a Detail string to the future tense when the report is being rendered
// in predictive mode (#167). Source strings throughout the migrate and
// predict packages stay in the natural "what happened" voice; this helper
// is the single point that switches them to "what will happen" so the
// predictive PDF reads consistently as a forward-looking forecast.
//
// Returns input unchanged when predictive=false. Replacements are ordered
// longest-first so multi-word phrases ("has been applied") win over
// single-word matches ("applied").
func toPredictiveTense(s string, predictive bool) string {
	if !predictive || s == "" {
		return s
	}
	type rule struct{ from, to string }
	rules := []rule{
		// Negative phrasing first so "will not be X" wins over the
		// generic "will be X" mapping. The order also catches "Were
		// not migrated" / "was not changed" idioms cleanly.
		{"was not ", "will not be "},
		{"were not ", "will not be "},
		// Multi-word phrases next.
		{"Has been applied", "Will be applied"},
		{"has been applied", "will be applied"},
		{"Have been applied", "Will be applied"},
		{"have been applied", "will be applied"},
		{"has been ", "will be "},
		{"have been ", "will be "},
		{"was turned off", "will be turned off"},
		{"was turned on", "will be turned on"},
		{"was disabled", "will be disabled"},
		{"was enabled", "will be enabled"},
		{"was changed", "will be changed"},
		{"was applied", "will be applied"},
		{"was set", "will be set"},
		{"was migrated", "will be migrated"},
		{"were applied", "will be applied"},
		{"were migrated", "will be migrated"},
		{"were disabled", "will be disabled"},
		{"were enabled", "will be enabled"},
		// Sentence-leading verb forms emitted by the migrate task.
		// Order matters: "Applied to all projects" is for the legacy
		// form (no longer emitted but kept as a safety net); the
		// "Applied value=" rule covers the new format that always
		// carries the value summary.
		{"Applied to all projects", "Will be applied to all projects"},
		{"Applied value=", "Will be applied value="},
		{"Applied (", "Will be applied ("},
		{"Migrated to ", "Will be migrated to "},
		{"Combined ", "Will combine "},
		{"Mirrored ", "Will mirror "},
		{"mirroring ", "will mirror "},
		// Standalone "was" / "were" as a last-resort fallback.
		{" was ", " will be "},
		{" were ", " will be "},
	}
	out := s
	for _, r := range rules {
		out = strings.ReplaceAll(out, r.from, r.to)
	}
	return out
}

// Inline bold markers used by drawDetailsCell. Private-use Unicode so
// they never collide with real data, and they survive sanitizeForPDF
// (which only strips astral-plane runes). The markers wrap the bold
// portion: "regular text " + inlineBoldStart + "bold text" +
// inlineBoldEnd + " more regular text".
const (
	inlineBoldStart = migrate.InlineBoldStart
	inlineBoldEnd   = migrate.InlineBoldEnd
)

// successDetails formats the Details column for a Succeeded item.
// Projects may carry up to two trailing markers added by the
// collectors:
//   - |ncdFallback:<sqs_type> — issue #135, the SQS NCD type wasn't
//     supported at SonarCloud project scope; the project fell back
//     to the org default.
//   - |scan:<status> — issue #208 era, project-data import outcome.
//
// Both markers are split off and rendered on separate lines after
// the cloud key. When predictive is true (#240) the SYNTHETIC
// "predict:<task>:<org>:<name>" placeholder is suppressed — but any
// other Detail string (e.g. the #249 dbcleaner-branches transformation
// note) is kept verbatim so genuinely informative content reaches the
// report.
//
// When labelProjectKey is true (Projects section), the cloud key is
// rendered as "New Project Key: <key>" with <key> wrapped in inline
// bold markers so the PDF renderer can stress it. Other sections keep
// the bare cloud key as-is.
func successDetails(item EntityItem, predictive, hideCloudKey, labelProjectKey bool) string {
	cloudKey, scan, ncdFallback := parseProjectDetailMarkers(item.Detail)
	if hideCloudKey || (predictive && strings.HasPrefix(cloudKey, "predict:")) {
		cloudKey = ""
	}
	parts := []string{}
	if cloudKey != "" {
		if labelProjectKey {
			parts = append(parts, "New Project Key: "+inlineBoldStart+cloudKey+inlineBoldEnd)
		} else {
			parts = append(parts, cloudKey)
		}
	}
	if ncdFallback != "" {
		parts = append(parts, fmt.Sprintf(
			"new code definition: %s on SonarQube Server is not supported at project scope on SonarQube Cloud — falling back to the org default",
			ncdFallback))
	}
	if scan != "" {
		parts = append(parts, fmt.Sprintf("project data: %s", scanStatusLabel(scan)))
	}
	return strings.Join(parts, "\n")
}

// partialDetails formats the Details column for a Partial / NearPerfect
// item — each issue rendered on its own line, prefixed by the cloud
// key if known. The predictive renderer passes predictive=true so the
// synthetic predict:<task>:<org>:<name> placeholder is suppressed
// (issue #240). Non-synthetic Detail strings are kept so genuinely
// informative content (e.g. transformation notes) still renders.
//
// labelProjectKey mirrors successDetails: when true, a non-empty
// project cloud key is rendered as "New Project Key: <key>" with the
// key bold.
func partialDetails(item EntityItem, predictive, hideCloudKey, labelProjectKey bool) string {
	// Reuse successDetails for the cloud-key / NCD-fallback / project-data
	// header so the |scan: and |ncdFallback: markers are split off and
	// rendered on their own lines (exactly like Succeeded rows), instead
	// of leaving the raw marker embedded inside the bolded project key.
	// Embedding the raw marker previously left the inline-bold span
	// unterminated when a downstream renderer re-split on "|scan:" (the
	// Markdown report truncated the closing bold marker and the issue
	// lines). Then append the per-item issue lines that make the row
	// Partial / NearPerfect.
	head := successDetails(item, predictive, hideCloudKey, labelProjectKey)
	issues := strings.Join(item.Issues, "\n")
	switch {
	case head != "" && issues != "":
		return head + "\n" + issues
	case issues != "":
		return issues
	default:
		return head
	}
}

// skippedDetails formats the Details column for a Skipped item — prefer the
// explicit detail message; otherwise fall back to the skip reason label.
func skippedDetails(item EntityItem) string {
	if item.Detail != "" {
		return item.Detail
	}
	for _, entry := range skipReasonOrder {
		if entry.Reason == item.SkipReason {
			return entry.Label
		}
	}
	return ""
}

// sectionsWithoutSkipped lists sections for which the per-section counts
// summary suppresses any "skipped" reference. Portfolios are created at the
// enterprise level, so a per-organization skip is not a meaningful concept.
var sectionsWithoutSkipped = map[string]bool{
	"Portfolios": true,
}

// sectionCountSummary returns a one-line counts summary for a section,
// breaking down skipped items by reason.
func sectionCountSummary(section Section) string {
	parts := []string{
		fmt.Sprintf("%d succeeded", len(section.Succeeded)),
	}
	if len(section.NearPerfect) > 0 {
		parts = append(parts, fmt.Sprintf("%d near perfect", len(section.NearPerfect)))
	}
	if len(section.Partial) > 0 {
		parts = append(parts, fmt.Sprintf("%d partial", len(section.Partial)))
	}
	parts = append(parts, fmt.Sprintf("%d failed", len(section.Failed)))

	if sectionsWithoutSkipped[section.Name] {
		return strings.Join(parts, ", ")
	}

	skipTotal := len(section.Skipped)
	if skipTotal == 0 {
		parts = append(parts, "0 skipped")
		return strings.Join(parts, ", ")
	}
	breakdown := skipBreakdown(section.Skipped)
	if len(breakdown) == 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipTotal))
	} else {
		parts = append(parts, fmt.Sprintf("%d skipped (%s)", skipTotal, strings.Join(breakdown, ", ")))
	}
	return strings.Join(parts, ", ")
}

func skipBreakdown(skipped []EntityItem) []string {
	counts := make(map[string]int)
	for _, item := range skipped {
		counts[item.SkipReason]++
	}
	var parts []string
	for _, entry := range skipReasonOrder {
		if c := counts[entry.Reason]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, lowerLabelPreservingProductName(entry.Label)))
		}
	}
	return parts
}

func renderTableHeader(pdf *fpdf.Fpdf, headers []string, widths []float64) {
	// Sonar primary (#126ed3) bg + white Poppins Bold text (#167).
	// Cell borders use the Sonar grey (#9e9e9e) set via SetDrawColor.
	pdf.SetDrawColor(colorSonarGrey[0], colorSonarGrey[1], colorSonarGrey[2])
	setFillColor(pdf, colorSonarBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamilyHeading, "B", 8)
	for i, h := range headers {
		pdf.CellFormat(widths[i], 6, h, "1", 0, "L", true, 0, "")
	}
	pdf.Ln(-1)
}

func parseProjectData(detail string) (string, string) {
	idx := strings.Index(detail, "|scan:")
	if idx < 0 {
		return detail, ""
	}
	return detail[:idx], detail[idx+6:]
}

// parseProjectDetailMarkers splits a project's Detail string into its
// three parts: the cloud key, the project-data status (or empty),
// and the NCD fallback source type (or empty). Marker ordering set
// by attachNCDFallback puts the NCD marker BEFORE the scan marker.
func parseProjectDetailMarkers(detail string) (cloudKey, scan, ncdFallback string) {
	cloudKey = detail
	// scan marker (always last when present).
	if idx := strings.Index(cloudKey, "|scan:"); idx >= 0 {
		scan = cloudKey[idx+len("|scan:"):]
		cloudKey = cloudKey[:idx]
	}
	// NCD fallback marker.
	if idx := strings.Index(cloudKey, "|ncdFallback:"); idx >= 0 {
		ncdFallback = cloudKey[idx+len("|ncdFallback:"):]
		cloudKey = cloudKey[:idx]
	}
	return cloudKey, scan, ncdFallback
}

func scanStatusLabel(status string) string {
	switch status {
	case "success":
		return "Yes"
	case "failed":
		return "Failed"
	case "skipped":
		return "No"
	default:
		return ""
	}
}

func checkPageBreak(pdf *fpdf.Fpdf, h float64) {
	_, pageH := pdf.GetPageSize()
	_, _, _, bottom := pdf.GetMargins()
	if pdf.GetY()+h > pageH-bottom-10 {
		pdf.AddPage()
	}
}

func setColor(pdf *fpdf.Fpdf, c [3]int) {
	pdf.SetTextColor(c[0], c[1], c[2])
}

func setFillColor(pdf *fpdf.Fpdf, c [3]int) {
	pdf.SetFillColor(c[0], c[1], c[2])
}

func truncate(s string, maxLen int) string {
	s = sanitizeForPDF(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// sanitizeForPDF replaces runes outside the Unicode Basic Multilingual Plane
// (codepoints ≥ U+10000, i.e. astral-plane characters such as most emoji)
// with "?". fpdf v0.9.0's CID font map is sized for the BMP only and panics
// with an index-out-of-range error on any astral-plane rune. The embedded
// Noto Sans LGC font would not have glyphs for those characters anyway —
// replacing them keeps the rest of the string (accented Latin, Greek,
// Cyrillic, BMP symbols) rendering correctly.
func sanitizeForPDF(s string) string {
	hasAstral := false
	for _, r := range s {
		if r >= 0x10000 {
			hasAstral = true
			break
		}
	}
	if !hasAstral {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0x10000 {
			out = append(out, '?')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// lowerLabelPreservingProductName lowercases a skip-reason label for
// the per-section counts summary line ("3 not applicable on SonarQube
// cloud") while keeping the "SonarQube" product name properly cased.
// A naïve strings.ToLower would render "sonarqube" — readable, but
// inconsistent with the way the product is named everywhere else in
// the report.
func lowerLabelPreservingProductName(s string) string {
	out := strings.ToLower(s)
	return strings.ReplaceAll(out, "sonarqube", "SonarQube")
}

// ---------------------------------------------------------------------------
// Runtime telemetry renderers (#240+).
//
// Everything below renders the migrate engine's per-run telemetry that is
// collected from the run directory (run_meta.json / run_events.jsonl) into
// the in-memory MigrationSummary. All of these renderers are NO-OPs when
// their backing slice is empty / the start time is zero, so predictive
// reports — which never populate the runtime fields — produce no extra
// pages. They reuse the existing colour / font constants and table header
// helper; no new fonts, images, or colours are introduced.
// ---------------------------------------------------------------------------

// renderSectionHeading draws a section heading using the same heading
// idiom as renderLimitations / renderSection (Poppins Bold, Sonar primary)
// with the surrounding spacing + page-break guard, so every runtime
// section is visually consistent with the rest of the report.
func renderSectionHeading(pdf *fpdf.Fpdf, title string) {
	pdf.Ln(8)
	checkPageBreak(pdf, 30)
	setColor(pdf, colorSonarBlue)
	pdf.SetFont(pdfFontFamilyHeading, "B", 14)
	pdf.CellFormat(0, 10, sanitizeForPDF(title), "", 1, "L", false, 0, "")
	setColor(pdf, colorBlack)
}

// renderSubHeading draws a smaller heading used to title an individual
// table inside a multi-table runtime section (e.g. the per-table titles
// in renderBottlenecks / renderWarningsLedger).
func renderSubHeading(pdf *fpdf.Fpdf, title string) {
	pdf.Ln(3)
	checkPageBreak(pdf, 20)
	setColor(pdf, colorSonarBlue)
	pdf.SetFont(pdfFontFamilyHeading, "B", 11)
	pdf.CellFormat(0, 8, sanitizeForPDF(title), "", 1, "L", false, 0, "")
	setColor(pdf, colorBlack)
}

// formatDuration renders a time.Duration human-readably (e.g. "1m2s",
// "350ms"). Sub-second durations keep their native String() form;
// second-or-longer durations are truncated to whole seconds so the
// table column stays compact ("1m2.34s" → "1m2s").
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return d.String()
	}
	return d.Truncate(time.Second).String()
}

// kvCellPad is the internal left padding subtracted from a wrapping
// column's width before SplitLines / drawWrappedCell measure + draw it,
// mirroring the detailsCellPad used by renderUnifiedTable.
const kvCellPad = 1.0

const (
	// kvLineH / kvFontSize are the per-line height and font size used by
	// every runtime KV table row.
	kvLineH    = 5.0
	kvFontSize = 8.0
	// kvWrapWidthThreshold: columns wider than this word-wrap via
	// drawWrappedCell; narrower columns render with a single CellFormat
	// at the full row height. This keeps long values (endpoints, error
	// notes, task ids, skip reasons) inside their cell.
	kvWrapWidthThreshold = 34.0
)

// kvCell returns the sanitized text for column c of a row, or "" when the
// row is shorter than the header set.
func kvCell(row []string, c int) string {
	if c < len(row) {
		return sanitizeForPDF(row[c])
	}
	return ""
}

// kvMeasureRow word-wraps every wide column of a row and returns the
// per-column wrapped lines plus the tallest line count (≥1). The caller
// must have already selected the kv body font.
func kvMeasureRow(pdf *fpdf.Fpdf, row []string, widths []float64, headers []string) (wrapped [][][]byte, lineCount int) {
	lineCount = 1
	wrapped = make([][][]byte, len(headers))
	for c := range headers {
		if c >= len(widths) || widths[c] <= kvWrapWidthThreshold {
			continue
		}
		lines := pdf.SplitLines([]byte(kvCell(row, c)), widths[c]-kvCellPad)
		if len(lines) < 1 {
			lines = [][]byte{[]byte(kvCell(row, c))}
		}
		wrapped[c] = lines
		if len(lines) > lineCount {
			lineCount = len(lines)
		}
	}
	return wrapped, lineCount
}

// kvDrawRow draws a single measured row at the cursor's current position
// and leaves the cursor at the start of the next row.
func kvDrawRow(pdf *fpdf.Fpdf, row []string, widths []float64, headers []string, wrapped [][][]byte, lineCount int) {
	rowHeight := float64(lineCount) * kvLineH
	rowStartX := pdf.GetX()
	rowStartY := pdf.GetY()
	x := rowStartX
	for c := range headers {
		w := 0.0
		if c < len(widths) {
			w = widths[c]
		}
		pdf.SetXY(x, rowStartY)
		if c < len(widths) && widths[c] > kvWrapWidthThreshold {
			// Wide column — wrap the text across the row's height.
			drawWrappedCell(pdf, w, kvLineH, lineCount, wrapped[c])
		} else {
			pdf.CellFormat(w, rowHeight, kvCell(row, c), "1", 0, "L", true, 0, "")
		}
		x += w
	}
	// Advance to the next row regardless of which column drew last
	// (drawWrappedCell leaves the cursor flush-right at the row top).
	pdf.SetXY(rowStartX, rowStartY+rowHeight)
}

// renderKVTable draws a heading, the shared column header (via the
// existing renderTableHeader), and one row per entry in `rows`. Columns
// whose width is "wide" wrap their text with drawWrappedCell; short
// columns render with a single CellFormat. The row height is the tallest
// wrapped column.
//
// Unlike renderUnifiedTable, this helper repeats the column header on
// every continuation page: before drawing each row, if the row would
// cross the page's bottom margin, it calls pdf.AddPage() and re-draws the
// header via renderTableHeader. This keeps the long runtime tables
// readable across page breaks.
//
// No-op when there are no rows so an empty sub-table renders nothing.
func renderKVTable(pdf *fpdf.Fpdf, title string, headers []string, widths []float64, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	if title != "" {
		renderSubHeading(pdf, title)
	}
	renderTableHeader(pdf, headers, widths)

	_, pageH := pdf.GetPageSize()
	_, _, _, bottom := pdf.GetMargins()
	pageBottom := pageH - bottom

	for i, row := range rows {
		pdf.SetFont(pdfFontFamilyBody, "", kvFontSize)
		wrapped, lineCount := kvMeasureRow(pdf, row, widths, headers)
		rowHeight := float64(lineCount) * kvLineH

		// Repeat the header on continuation pages. The existing
		// renderUnifiedTable relies on checkPageBreak only; these long
		// tables additionally re-draw their header so every page is
		// self-describing.
		if pdf.GetY()+rowHeight > pageBottom {
			pdf.AddPage()
			renderTableHeader(pdf, headers, widths)
		}

		if i%2 == 0 {
			setFillColor(pdf, colorLightGray)
		} else {
			setFillColor(pdf, colorWhite)
		}
		setColor(pdf, colorBlack)
		pdf.SetFont(pdfFontFamilyBody, "", kvFontSize)
		kvDrawRow(pdf, row, widths, headers, wrapped, lineCount)
	}
}

// renderRunMetadata renders the "Run metadata" section: a small key/value
// block with the run's Started / Completed timestamps, total elapsed wall
// time, and overall status. The Run ID already appears on the title page,
// so it is intentionally omitted here. No-op for predictive reports,
// where StartedAt is the zero time.
func renderRunMetadata(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	if summary.StartedAt.IsZero() {
		return
	}
	renderSectionHeading(pdf, "Run metadata")

	rows := [][]string{
		{"Started", summary.StartedAt.Format("2006-01-02 15:04:05")},
		{"Completed", summary.CompletedAt.Format("2006-01-02 15:04:05")},
		{"Total elapsed", formatDuration(summary.TotalElapsed)},
		{"Overall status", summary.OverallStatus},
	}
	// No per-table sub-heading (the section heading already names it);
	// pass an empty title so renderKVTable only draws the column header.
	renderKVTable(pdf, "", []string{"Field", "Value"}, []float64{50, 130}, rows)
}

// renderBottlenecks renders the "Performance bottlenecks" section as three
// key/value tables: phase timings, the slowest tasks, and per-branch CE
// activity. No-op when Phases, Tasks and Branches are all empty.
func renderBottlenecks(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	if len(summary.Phases) == 0 && len(summary.Tasks) == 0 && len(summary.Branches) == 0 {
		return
	}
	renderSectionHeading(pdf, "Performance bottlenecks")

	// Phase timings.
	phaseRows := make([][]string, 0, len(summary.Phases))
	for _, p := range summary.Phases {
		phaseRows = append(phaseRows, []string{
			p.Phase,
			itoa(p.Tasks),
			formatDuration(p.Duration),
		})
	}
	renderKVTable(pdf, "Phase timings",
		[]string{"Phase", "Tasks", "Duration"},
		[]float64{110, 30, 40},
		phaseRows)

	// Slowest tasks — sort a copy descending by duration so the heaviest
	// tasks lead the table.
	slowest := make([]TaskTiming, len(summary.Tasks))
	copy(slowest, summary.Tasks)
	sort.SliceStable(slowest, func(i, j int) bool {
		return slowest[i].Duration > slowest[j].Duration
	})
	taskRows := make([][]string, 0, len(slowest))
	for _, t := range slowest {
		ok := "Yes"
		if !t.OK {
			ok = "No"
		}
		taskRows = append(taskRows, []string{
			t.Task,
			itoa(t.Phase),
			formatDuration(t.Duration),
			ok,
		})
	}
	renderKVTable(pdf, "Slowest tasks",
		[]string{"Task", "Phase", "Duration", "OK"},
		[]float64{105, 25, 30, 20},
		taskRows)

	// Per-branch CE activity.
	branchRows := make([][]string, 0, len(summary.Branches))
	for _, b := range summary.Branches {
		branchRows = append(branchRows, []string{
			b.Branch,
			b.Type,
			b.Status,
			b.TaskID,
		})
	}
	renderKVTable(pdf, "Per-branch CE activity",
		[]string{"Branch", "Type", "Status", "Task Id"},
		[]float64{55, 25, 30, 70},
		branchRows)
}

// renderFailureLedger renders the "Failure ledger" section: one table of
// entity-level failures. The Error column is the only wide column and
// wraps. No-op when there are no failures.
func renderFailureLedger(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	if len(summary.Failures) == 0 {
		return
	}
	renderSectionHeading(pdf, "Failure ledger")

	rows := make([][]string, 0, len(summary.Failures))
	for _, f := range summary.Failures {
		rows = append(rows, []string{
			f.EntityType,
			f.EntityName,
			f.Organization,
			f.HTTPStatus,
			f.ErrorMessage,
		})
	}
	renderKVTable(pdf, "",
		[]string{"Entity Type", "Name", "Organization", "HTTP", "Error"},
		[]float64{30, 40, 30, 18, 62},
		rows)
}

// renderWarningsLedger renders the "Warnings ledger" section as up to
// four key/value sub-tables: HTTP retries, branch skips, gate condition
// skips/remaps, and metric remaps. Each sub-table no-ops when its slice
// is empty (renderKVTable returns early on zero rows), and the whole
// section no-ops when every slice is empty.
func renderWarningsLedger(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	w := summary.Warnings
	if len(w.Retries) == 0 && len(w.BranchSkips) == 0 &&
		len(w.GateConditions) == 0 && len(w.MetricRemaps) == 0 {
		return
	}
	renderSectionHeading(pdf, "Warnings ledger")

	// Retries.
	retryRows := make([][]string, 0, len(w.Retries))
	for _, r := range w.Retries {
		retryRows = append(retryRows, []string{
			r.Method,
			r.Endpoint,
			itoa(r.Count),
			itoa(r.MaxAttempt),
			r.LastStatus,
		})
	}
	renderKVTable(pdf, "Retries",
		[]string{"Method", "Endpoint", "Retries", "Max Attempt", "Last Status"},
		[]float64{20, 70, 20, 28, 24},
		retryRows)

	// Branch skips.
	skipRows := make([][]string, 0, len(w.BranchSkips))
	for _, b := range w.BranchSkips {
		skipRows = append(skipRows, []string{
			b.Branch,
			itoa(b.Findings),
			b.Reason,
		})
	}
	renderKVTable(pdf, "Branch Skips",
		[]string{"Branch", "Findings", "Reason"},
		[]float64{50, 25, 95},
		skipRows)

	// Gate condition skips / remaps.
	gateRows := make([][]string, 0, len(w.GateConditions))
	for _, g := range w.GateConditions {
		gateRows = append(gateRows, []string{
			g.Gate,
			g.Metric,
			g.Action,
			g.Note,
		})
	}
	renderKVTable(pdf, "Gate Condition Skips",
		[]string{"Gate", "Metric", "Action", "Note"},
		[]float64{40, 40, 25, 65},
		gateRows)

	// Metric remaps.
	remapRows := make([][]string, 0, len(w.MetricRemaps))
	for _, m := range w.MetricRemaps {
		remapRows = append(remapRows, []string{
			m.Gate,
			m.SourceMetric,
			m.TargetMetric,
		})
	}
	renderKVTable(pdf, "Metric Remaps",
		[]string{"Gate", "Source Metric", "Target Metric"},
		[]float64{50, 60, 60},
		remapRows)
}

// renderBranchProjectData renders the "Branch project data" section: one
// table summarising every branch's packaging / submission stats. The Skip
// Reason column is the only wide column and wraps. No-op when there are no
// branches.
func renderBranchProjectData(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	if len(summary.Branches) == 0 {
		return
	}
	renderSectionHeading(pdf, "Branch project data")

	rows := make([][]string, 0, len(summary.Branches))
	for _, b := range summary.Branches {
		zipBytes := ""
		if b.ZipBytes > 0 {
			zipBytes = fmt.Sprintf("%d", b.ZipBytes)
		}
		rows = append(rows, []string{
			b.Branch,
			b.Type,
			b.Status,
			itoa(b.Issues),
			itoa(b.ExternalIssues),
			itoa(b.Components),
			itoa(b.ActiveRules),
			zipBytes,
			b.SkipReason,
		})
	}
	renderKVTable(pdf, "",
		[]string{"Branch", "Type", "Status", "Issues", "External", "Components", "Active Rules", "Zip Bytes", "Skip Reason"},
		[]float64{28, 16, 18, 14, 16, 22, 20, 18, 28},
		rows)
}
