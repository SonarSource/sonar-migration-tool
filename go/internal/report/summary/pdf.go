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
	"Quality Gates": true,
	"Groups":        true,
	"Portfolios":    true,
	"Projects":      true,
}

// Color constants for the PDF report.
var (
	colorDarkBlue  = [3]int{26, 60, 110}
	colorMedBlue   = [3]int{45, 95, 154}
	colorLightGray = [3]int{245, 245, 245}
	colorWhite     = [3]int{255, 255, 255}
	colorBlack     = [3]int{0, 0, 0}
	colorGreen     = [3]int{34, 139, 34}
	colorRed       = [3]int{200, 0, 0}
	colorAmber     = [3]int{200, 110, 0}
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
}

// Outcome labels used in the unified per-section table.
const (
	outcomeSuccess = "Success"
	outcomePartial = "Partial"
	outcomeFailed  = "Failed"
	outcomeSkipped = "Skipped"
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
		renderSection(pdf, section)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("generating PDF: %w", err)
	}
	return buf.Bytes(), nil
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

func renderTitlePage(pdf *fpdf.Fpdf, summary *MigrationSummary) {
	pdf.SetY(30)

	setColor(pdf, colorDarkBlue)
	pdf.SetFont(pdfFontFamily, "B", 22)
	pdf.CellFormat(0, 12, "SonarQube Migration Summary", "", 1, "C", false, 0, "")
	pdf.Ln(4)

	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamily, "", 11)
	pdf.CellFormat(0, 7, "Run ID: "+summary.RunID, "", 1, "C", false, 0, "")
	pdf.CellFormat(0, 7, "Generated: "+summary.GeneratedAt.Format("2006-01-02 15:04:05"), "", 1, "C", false, 0, "")
	pdf.Ln(10)

	renderExecutiveSummary(pdf, summary.Sections)
}

func renderExecutiveSummary(pdf *fpdf.Fpdf, sections []Section) {
	headers := []string{"Section", "Succeeded", "Partial", "Failed", "Skipped", "Total"}
	widths := []float64{50, 25, 25, 25, 25, 25}

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
	var totalS, totalP, totalF, totalSk int
	for i, sec := range sections {
		s, p, f, sk := len(sec.Succeeded), len(sec.Partial), len(sec.Failed), len(sec.Skipped)
		totalS += s
		totalP += p
		totalF += f
		totalSk += sk

		if i%2 == 0 {
			setFillColor(pdf, colorLightGray)
		} else {
			setFillColor(pdf, colorWhite)
		}
		setColor(pdf, colorBlack)
		pdf.CellFormat(widths[0], 7, sec.Name, "1", 0, "L", true, 0, "")
		renderCountCell(pdf, widths[1], s, colorGreen)
		renderCountCell(pdf, widths[2], p, colorAmber)
		renderCountCell(pdf, widths[3], f, colorRed)
		renderCountCell(pdf, widths[4], sk, colorDarkGray)
		setColor(pdf, colorBlack)
		pdf.CellFormat(widths[5], 7, itoa(s+p+f+sk), "1", 0, "C", true, 0, "")
		pdf.Ln(-1)
	}

	// Totals row
	setFillColor(pdf, colorDarkBlue)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont(pdfFontFamily, "B", 10)
	pdf.CellFormat(widths[0], 8, "Total", "1", 0, "L", true, 0, "")
	pdf.CellFormat(widths[1], 8, itoa(totalS), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[2], 8, itoa(totalP), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[3], 8, itoa(totalF), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[4], 8, itoa(totalSk), "1", 0, "C", true, 0, "")
	pdf.CellFormat(widths[5], 8, itoa(totalS+totalP+totalF+totalSk), "1", 0, "C", true, 0, "")
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

func renderSection(pdf *fpdf.Fpdf, section Section) {
	total := len(section.Succeeded) + len(section.Partial) + len(section.Failed) + len(section.Skipped)
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

	renderUnifiedTable(pdf, section)
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

// renderUnifiedTable renders the 4-column per-section table:
//   Name | Organization | Outcome (colored) | Details
func renderUnifiedTable(pdf *fpdf.Fpdf, section Section) {
	headers := []string{"Name", "Organization", "Outcome", "Details"}
	widths := []float64{55, 35, 25, 81}

	renderTableHeader(pdf, headers, widths)

	rows := buildUnifiedRows(section)
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

	pdf.SetFont(pdfFontFamily, "", 8)
	for i, row := range rows {
		checkPageBreak(pdf, 7)
		if i%2 == 0 {
			setFillColor(pdf, colorLightGray)
		} else {
			setFillColor(pdf, colorWhite)
		}
		// Name + Organization in black on alternating background.
		setColor(pdf, colorBlack)
		pdf.CellFormat(widths[0], 6, truncate(row.displayName(), 36), "1", 0, "L", true, 0, "")
		pdf.CellFormat(widths[1], 6, truncate(row.org, 24), "1", 0, "L", true, 0, "")
		// Outcome cell in its color, bold.
		setColor(pdf, row.color)
		pdf.SetFont(pdfFontFamily, "B", 8)
		pdf.CellFormat(widths[2], 6, row.outcome, "1", 0, "C", true, 0, "")
		// Details in black, regular.
		setColor(pdf, colorBlack)
		pdf.SetFont(pdfFontFamily, "", 8)
		pdf.CellFormat(widths[3], 6, truncate(row.details, 56), "1", 0, "L", true, 0, "")
		pdf.Ln(-1)
	}
}

// buildUnifiedRows flattens the section's four buckets into ordered rows:
// Succeeded → Partial → Failed → Skipped (skipped grouped by reason order).
func buildUnifiedRows(section Section) []unifiedRow {
	var rows []unifiedRow

	for _, item := range section.Succeeded {
		rows = append(rows, unifiedRow{
			name:     item.Name,
			language: item.Language,
			org:      item.Organization,
			outcome:  outcomeSuccess,
			color:    colorGreen,
			details:  successDetails(item),
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
			details:  partialDetails(item),
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

// successDetails formats the Details column for a Succeeded item, including
// scan-history status (projects only) when present.
func successDetails(item EntityItem) string {
	cloudKey, scan := parseScanHistory(item.Detail)
	if scan == "" {
		return cloudKey
	}
	return fmt.Sprintf("%s — scan history: %s", cloudKey, scanStatusLabel(scan))
}

// partialDetails formats the Details column for a Partial item — joined issues,
// and the cloud key if known.
func partialDetails(item EntityItem) string {
	issues := strings.Join(item.Issues, "; ")
	if item.Detail != "" && issues != "" {
		return item.Detail + " — " + issues
	}
	if issues != "" {
		return issues
	}
	return item.Detail
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

// sectionCountSummary returns a one-line counts summary for a section,
// breaking down skipped items by reason.
func sectionCountSummary(section Section) string {
	parts := []string{
		fmt.Sprintf("%d succeeded", len(section.Succeeded)),
	}
	if len(section.Partial) > 0 {
		parts = append(parts, fmt.Sprintf("%d partial", len(section.Partial)))
	}
	parts = append(parts, fmt.Sprintf("%d failed", len(section.Failed)))

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
			parts = append(parts, fmt.Sprintf("%d %s", c, strings.ToLower(entry.Label)))
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
