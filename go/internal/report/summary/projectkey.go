// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

const projectKeyHeading = "Project key conflicts detected"

// projectKeyMessageMaxItems bounds how many individual collisions /
// over-length keys are spelled out in the callout before it summarises the
// remainder, so a pathological run can't produce a page-long box.
const projectKeyMessageMaxItems = 8

// projectKeyMessage builds the body text for both the PDF callout and the
// markdown section. Pure so it can be unit-tested without fpdf.
func projectKeyMessage(r *ProjectKeyReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Target project keys were derived with the pattern %q. ", r.Pattern)
	if len(r.Collisions) > 0 {
		fmt.Fprintf(&b, "%d target key(s) are claimed by more than one source project — "+
			"SonarQube Cloud keys are globally unique, so colliding projects cannot all be created. ",
			len(r.Collisions))
	}
	if len(r.TooLong) > 0 {
		fmt.Fprintf(&b, "%d key(s) exceed the %d-character limit and will be rejected. ",
			len(r.TooLong), migrate.MaxProjectKeyLength)
	}
	b.WriteString("Adjust project_key_pattern (e.g. include <ORGANIZATION_KEY>) to disambiguate.")

	for i, c := range r.Collisions {
		if i >= projectKeyMessageMaxItems {
			fmt.Fprintf(&b, "\n  … and %d more collision(s)", len(r.Collisions)-projectKeyMessageMaxItems)
			break
		}
		parts := make([]string, 0, len(c.Sources))
		for _, s := range c.Sources {
			parts = append(parts, fmt.Sprintf("%q in %q", s.SourceKey, s.OrgKey))
		}
		fmt.Fprintf(&b, "\n  collision %q ← %s", c.TargetKey, strings.Join(parts, ", "))
	}
	for i, tl := range r.TooLong {
		if i >= projectKeyMessageMaxItems {
			fmt.Fprintf(&b, "\n  … and %d more over-length key(s)", len(r.TooLong)-projectKeyMessageMaxItems)
			break
		}
		fmt.Fprintf(&b, "\n  too long (%d chars): %q ← %q in %q", tl.Length, tl.TargetKey, tl.SourceKey, tl.OrgKey)
	}
	return b.String()
}

// renderProjectKeyWarning draws the amber callout for project-key problems,
// mirroring renderRateLimitWarning. Renders only when CollectSummary
// produced a non-nil report; nil is a programmer error.
func renderProjectKeyWarning(pdf *fpdf.Fpdf, r *ProjectKeyReport) {
	pdf.Ln(4)
	pageW, _ := pdf.GetPageSize()
	left, _, right, _ := pdf.GetMargins()
	boxW := pageW - left - right
	innerW := boxW - 2*rateLimitBoxPadding

	body := sanitizeForPDF(projectKeyMessage(r))

	pdf.SetFont(pdfFontFamilyBody, "", rateLimitBodyFontPt)
	bodyLines := pdf.SplitLines([]byte(body), innerW)
	bodyLineCount := len(bodyLines)
	if bodyLineCount < 1 {
		bodyLineCount = 1
	}
	boxHeight := rateLimitHeadingLineH + rateLimitBodyLineH*float64(bodyLineCount) + rateLimitBoxPadding*2
	checkPageBreak(pdf, boxHeight)

	startY := pdf.GetY()
	innerX := left + rateLimitBoxPadding

	pdf.SetXY(innerX, startY+rateLimitBoxPadding)
	setColor(pdf, colorAmber)
	pdf.SetFont(pdfFontFamilyHeading, "B", rateLimitHeadingFontPt)
	pdf.CellFormat(innerW, rateLimitHeadingLineH, projectKeyHeading, "", 1, "L", false, 0, "")

	pdf.SetX(innerX)
	setColor(pdf, colorBlack)
	pdf.SetFont(pdfFontFamilyBody, "", rateLimitBodyFontPt)
	pdf.MultiCell(innerW, rateLimitBodyLineH, body, "", "L", false)

	endY := pdf.GetY() + rateLimitBoxPadding

	prevLineWidth := pdf.GetLineWidth()
	pdf.SetLineWidth(rateLimitBoxBorderWidth)
	prevDraw := setDrawColorAmber(pdf)
	pdf.Rect(left, startY, boxW, endY-startY, "D")
	restoreDrawColor(pdf, prevDraw)
	pdf.SetLineWidth(prevLineWidth)

	pdf.SetY(endY + 2)
}

// renderMarkdownProjectKeyConflicts writes the project-key conflict section
// to the markdown report. No-op when there is nothing to report.
func renderMarkdownProjectKeyConflicts(sb *strings.Builder, summary *MigrationSummary) {
	r := summary.ProjectKeys
	if r == nil {
		return
	}
	sb.WriteString("## ⚠️ Project key conflicts\n\n")
	fmt.Fprintf(sb, "Target project keys were derived with the pattern `%s`.\n\n", r.Pattern)

	if len(r.Collisions) > 0 {
		fmt.Fprintf(sb, "**%d colliding target key(s)** — claimed by more than one source project. "+
			"SonarQube Cloud project keys are globally unique, so these cannot all be created; "+
			"adjust `project_key_pattern` (for example include `<ORGANIZATION_KEY>`) to disambiguate.\n\n",
			len(r.Collisions))
		sb.WriteString("| Target key | Source projects (key in organization) |\n")
		sb.WriteString("|---|---|\n")
		for _, c := range r.Collisions {
			parts := make([]string, 0, len(c.Sources))
			for _, s := range c.Sources {
				parts = append(parts, fmt.Sprintf("`%s` in `%s`", s.SourceKey, s.OrgKey))
			}
			fmt.Fprintf(sb, "| `%s` | %s |\n", mdCell(c.TargetKey), mdCell(strings.Join(parts, "; ")))
		}
		sb.WriteString("\n")
	}

	if len(r.TooLong) > 0 {
		fmt.Fprintf(sb, "**%d over-length key(s)** — longer than the %d-character SonarQube limit and will be rejected.\n\n",
			len(r.TooLong), migrate.MaxProjectKeyLength)
		sb.WriteString("| Target key | Length | Source key | Organization |\n")
		sb.WriteString("|---|---|---|---|\n")
		for _, tl := range r.TooLong {
			fmt.Fprintf(sb, "| `%s` | %d | `%s` | `%s` |\n",
				mdCell(tl.TargetKey), tl.Length, mdCell(tl.SourceKey), mdCell(tl.OrgKey))
		}
		sb.WriteString("\n")
	}
}
