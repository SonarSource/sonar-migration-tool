package summary

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/go-pdf/fpdf"
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
var (
	colorDarkBlue  = [3]int{26, 60, 110}
	colorMedBlue   = [3]int{45, 95, 154}
	colorLightGray = [3]int{245, 245, 245}
	colorWhite     = [3]int{255, 255, 255}
	colorBlack     = [3]int{0, 0, 0}
	colorGreen     = [3]int{34, 139, 34}
	colorYellow    = [3]int{180, 150, 0} // near-perfect (issue #224 yellow status)
	colorRed       = [3]int{200, 0, 0}
	colorAmber     = [3]int{200, 110, 0} // partial migration (issue #224 orange status)
	colorDarkGray  = [3]int{90, 90, 90}
)

// Skip reason display order and labels.
var skipReasonOrder = []struct {
	Reason string
	Label  string
}{
	{SkipReasonOrgSkipped, "Organization skipped"},
	{SkipReasonBuiltIn, "Built-in"},
	{SkipReasonUnused, "Unused"},
	{"", "Other"},
	// SQS-only settings appear last so the section ends with the
	// "not applicable on SQC" notes — one row per such setting,
	// no Organization (issue #200).
	{SkipReasonSQSOnly, "Not applicable on SonarQube cloud"},
}

// Outcome labels used in the unified per-section table.
const (
	outcomeSuccess     = "Success"
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

	pdf.SetHeaderFuncMode(func() {
		addPageHeader(pdf, summary.RunID)
	}, true)
	pdf.SetFooterFunc(func() {
		addPageFooter(pdf)
	})

	pdf.AddPage()
	renderTitlePage(pdf, summary)

	for _, section := range summary.Sections {
		if summary.OmitSections[section.Name] {
			continue
		}
		renderSection(pdf, section, summary.Predictive)
	}

	renderLimitations(pdf, summary.Limitations)

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
func renderLimitations(pdf *fpdf.Fpdf, limitations []string) {
	if len(limitations) == 0 {
		return
	}
	pdf.Ln(8)
	checkPageBreak(pdf, 30)

	setColor(pdf, colorMedBlue)
	pdf.SetFont(pdfFontFamily, "B", 14)
	pdf.CellFormat(0, 10, "Migration limitations", "", 1, "L", false, 0, "")

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamily, "", 9)
	for _, line := range limitations {
		// MultiCell so long messages wrap instead of running off
		// the edge. Bullet prefix mirrors the visual treatment used
		// by other free-text sections.
		pdf.MultiCell(0, 5, "• "+sanitizeForPDF(line), "", "L", false)
	}
}

// registerUnicodeFont registers the embedded Noto Sans regular + bold variants
// so SetFont(pdfFontFamily, ...) renders strings as UTF-8 with broad coverage
// instead of the single-byte Helvetica fallback that mangles non-ASCII text.
func registerUnicodeFont(pdf *fpdf.Fpdf) {
	pdf.AddUTF8FontFromBytes(pdfFontFamily, "", notoSansRegular)
	pdf.AddUTF8FontFromBytes(pdfFontFamily, "B", notoSansBold)
}

func addPageHeader(pdf *fpdf.Fpdf, runID string) {
	pdf.SetY(5)
	setColor(pdf, colorDarkBlue)
	pdf.SetFont(pdfFontFamily, "B", 8)
	pdf.CellFormat(0, 6, "SonarQube Migration Summary - "+runID, "", 0, "R", false, 0, "")
	pdf.Ln(8)
}

func addPageFooter(pdf *fpdf.Fpdf) {
	pdf.SetY(-15)
	pdf.SetFont(pdfFontFamily, "", 8)
	setColor(pdf, colorBlack)
	pdf.CellFormat(0, 10, fmt.Sprintf("Page %d", pdf.PageNo()), "", 0, "C", false, 0, "")
}

// renderPredictiveTitle renders the title for the predictive report
// (#240): "SonarQube Migration Prediction" centered, with "Prediction"
// underlined while the rest stays plain bold. fpdf draws the underline
// programmatically when "U" is part of the style string, so no extra
// font registration is needed.
func renderPredictiveTitle(pdf *fpdf.Fpdf) {
	const fontSize = 22.0
	const lineH = 12.0
	prefix := "SonarQube Migration "
	suffix := "Prediction"

	pdf.SetFont(pdfFontFamily, "B", fontSize)
	prefixW := pdf.GetStringWidth(prefix)
	pdf.SetFont(pdfFontFamily, "BU", fontSize)
	suffixW := pdf.GetStringWidth(suffix)

	pageW, _ := pdf.GetPageSize()
	leftMargin, _, rightMargin, _ := pdf.GetMargins()
	usableW := pageW - leftMargin - rightMargin
	startX := leftMargin + (usableW-(prefixW+suffixW))/2

	y := pdf.GetY()
	pdf.SetXY(startX, y)
	pdf.SetFont(pdfFontFamily, "B", fontSize)
	pdf.Write(lineH, prefix)
	pdf.SetFont(pdfFontFamily, "BU", fontSize)
	pdf.Write(lineH, suffix)
	pdf.Ln(lineH)
}

func renderTitlePage(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	pdf.SetY(30)

	setColor(pdf, colorDarkBlue)
	if summary.Predictive {
		renderPredictiveTitle(pdf)
	} else {
		pdf.SetFont(pdfFontFamily, "B", 22)
		pdf.CellFormat(0, 12, "SonarQube Migration Summary", "", 1, "C", false, 0, "")
	}
	pdf.Ln(4)

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamily, "", 11)
	pdf.CellFormat(0, 7, "Run ID: "+summary.RunID, "", 1, "C", false, 0, "")
	pdf.CellFormat(0, 7, "Generated: "+summary.GeneratedAt.Format("2006-01-02 15:04:05"), "", 1, "C", false, 0, "")
	pdf.Ln(10)

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

	setFillColor(pdf, colorDarkBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamily, "B", 10)
	for i, h := range headers {
		align := "C"
		if i == 0 {
			align = "L"
		}
		pdf.CellFormat(widths[i], 8, h, "1", 0, align, true, 0, "")
	}
	pdf.Ln(-1)

	pdf.SetFont(pdfFontFamily, "", 10)
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

	// Totals row.
	setFillColor(pdf, colorDarkBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamily, "B", 10)
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

	setColor(pdf, colorMedBlue)
	pdf.SetFont(pdfFontFamily, "B", 14)
	pdf.CellFormat(0, 10, section.Name, "", 1, "L", false, 0, "")

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamily, "", 9)
	pdf.CellFormat(0, 6, sectionCountSummary(section), "", 1, "L", false, 0, "")
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
//   Name | Organization | Outcome (colored) | Details
// For sections in sectionsWithoutOrganization (and for the predictive
// report, #240, where org mapping isn't meaningful), the Organization
// column is dropped — producing a 3-column table: Name | Outcome |
// Details.
func renderUnifiedTable(pdf *fpdf.Fpdf, section Section, predictive bool) {
	hideOrg := predictive || sectionsWithoutOrganization[section.Name]

	nameHeader := "Name"
	if section.Name == "Global Settings" {
		nameHeader = "Setting Key"
	}
	headers := []string{nameHeader, "Organization", "Outcome", "Details"}
	widths := []float64{55, 35, 25, 81}
	if hideOrg {
		headers = []string{nameHeader, "Outcome", "Details"}
		widths = []float64{90, 25, 81}
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
		singleLineH       = 6.0
		// wrappedLineH at 3.0mm was too tight for the 8pt body font
		// used in the Name column: descenders on g/p/y/j were clipped
		// for long wrapping setting keys (issue #207, example
		// sonar.java.ignoreUnnamedModuleForSplitPackage). 4.0mm gives
		// 8pt enough leading while staying readable for the 6pt
		// multi-line details column.
		wrappedLineH = 4.0
		bodyFontSize      = 8.0
		multiLineFontSize = 6.0
	)
	pdf.SetFont(pdfFontFamily, "", bodyFontSize)
	// Sections where the Name column may carry long setting keys
	// (e.g. sonar.qualityProfiles.allowDisableInheritedRules) and
	// must word-wrap instead of truncating. Other sections keep the
	// truncate behaviour to avoid disturbing layouts that depend on
	// single-line names.
	wrapName := section.Name == "Global Settings"
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
		pdf.SetFont(pdfFontFamily, "", detailsFontSize)
		detailsLineCount := len(pdf.SplitLines([]byte(detailsText), widths[detailsCol]))
		pdf.SetFont(pdfFontFamily, "", bodyFontSize)
		if detailsLineCount < 1 {
			detailsLineCount = 1
		}
		// If the Name column word-wraps, compute its line count too
		// and take the max so the row is tall enough for either side.
		nameText := row.displayName()
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
		// Outcome cell in its color, bold.
		setColor(pdf, row.color)
		pdf.SetFont(pdfFontFamily, "B", 8)
		pdf.CellFormat(widths[col], rowHeight, row.outcome, "1", 0, "C", true, 0, "")
		col++
		// Details in black, regular — MultiCell wraps long text across lines
		// rather than truncating. lineH is per-line so the cell ends up at
		// rowHeight, matching the row. Multi-line details drop to a smaller
		// font so a long metric-mapping block doesn't visually dominate the
		// table.
		setColor(pdf, colorBlack)
		pdf.SetFont(pdfFontFamily, "", detailsFontSize)
		// Render Details with the same explicit per-line approach as
		// Name so the cell always reaches rowHeight. Issue #207.
		drawWrappedCell(pdf, widths[col], lineH, lineCount, pdf.SplitLines([]byte(detailsText), widths[col]))
		pdf.SetFont(pdfFontFamily, "", bodyFontSize)
		// Advance to the next row. drawWrappedCell leaves the cursor
		// flush right of the cell at the row's start Y; explicitly
		// move to (rowStartX, rowStartY + rowHeight) so the next
		// iteration's GetXY() reports the correct position.
		pdf.SetXY(rowStartX, rowStartY+rowHeight)
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
// When predictive is true (#240), the success-bucket outcome label is
// rendered as "Perfect" instead of "Success" and the Details column
// drops the synthetic predict:<task>:<org>:<name> cloud-id placeholder
// (those carry no useful information for prediction).
func buildUnifiedRows(section Section, predictive bool) []unifiedRow {
	var rows []unifiedRow

	successLabel := outcomeSuccess
	if predictive {
		successLabel = "Perfect"
	}

	for _, item := range section.Succeeded {
		rows = append(rows, unifiedRow{
			name:     item.Name,
			language: item.Language,
			org:      item.Organization,
			outcome:  successLabel,
			color:    colorGreen,
			details:  successDetails(item, predictive),
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
			details:  partialDetails(item, predictive),
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
			details:  partialDetails(item, predictive),
		})
	}
	for _, item := range section.Failed {
		rows = append(rows, unifiedRow{
			name:     item.Name,
			language: item.Language,
			org:      item.Organization,
			outcome:  outcomeFailed,
			color:    colorRed,
			details:  item.ErrorMessage,
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
				details:  skippedDetails(item),
			})
		}
	}
	return rows
}

// successDetails formats the Details column for a Succeeded item.
// Projects may carry up to two trailing markers added by the
// collectors:
//   - |ncdFallback:<sqs_type> — issue #135, the SQS NCD type wasn't
//     supported at SonarCloud project scope; the project fell back
//     to the org default.
//   - |scan:<status> — issue #208 era, scan-history import outcome.
//
// Both markers are split off and rendered on separate lines after
// the cloud key. When predictive is true (#240) the cloud key itself
// is suppressed — the predict pipeline emits a synthetic placeholder
// ("predict:<task>:<org>:<name>") that carries no useful information.
func successDetails(item EntityItem, predictive bool) string {
	cloudKey, scan, ncdFallback := parseProjectDetailMarkers(item.Detail)
	if predictive {
		cloudKey = ""
	}
	parts := []string{}
	if cloudKey != "" {
		parts = append(parts, cloudKey)
	}
	if ncdFallback != "" {
		parts = append(parts, fmt.Sprintf(
			"new code definition: %s on SonarQube Server is not supported at project scope on SonarQube Cloud — falling back to the org default",
			ncdFallback))
	}
	if scan != "" {
		parts = append(parts, fmt.Sprintf("scan history: %s", scanStatusLabel(scan)))
	}
	return strings.Join(parts, "\n")
}

// partialDetails formats the Details column for a Partial / NearPerfect
// item — each issue rendered on its own line, prefixed by the cloud
// key if known. The predictive renderer passes predictive=true so the
// synthetic predict:<task>:<org>:<name> placeholder is suppressed
// (issue #240; matches the equivalent stripping in successDetails).
func partialDetails(item EntityItem, predictive bool) string {
	issues := strings.Join(item.Issues, "\n")
	detail := item.Detail
	if predictive {
		detail = ""
	}
	if detail != "" && issues != "" {
		return detail + "\n" + issues
	}
	if issues != "" {
		return issues
	}
	return detail
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
	setFillColor(pdf, colorMedBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamily, "B", 8)
	for i, h := range headers {
		pdf.CellFormat(widths[i], 6, h, "1", 0, "L", true, 0, "")
	}
	pdf.Ln(-1)
}

func parseScanHistory(detail string) (string, string) {
	idx := strings.Index(detail, "|scan:")
	if idx < 0 {
		return detail, ""
	}
	return detail[:idx], detail[idx+6:]
}

// parseProjectDetailMarkers splits a project's Detail string into its
// three parts: the cloud key, the scan-history status (or empty),
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
