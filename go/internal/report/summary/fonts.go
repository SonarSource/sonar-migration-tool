package summary

import _ "embed"

// Noto Sans is kept as the fallback Unicode font: it has the broadest
// glyph coverage among the three families embedded here (Latin extended,
// Greek, Cyrillic, symbols), so any string that the Sonar-branded fonts
// (Poppins, Inter) can't render still has a non-mangled rendering.
// fpdf's built-in core fonts (Helvetica, Times, Courier) are limited to
// Windows-1252 single-byte encoding, which mangles characters outside
// that range.
//
// Licensed under the SIL Open Font License v1.1 (see fonts/OFL.txt).

//go:embed fonts/NotoSans-Regular.ttf
var notoSansRegular []byte

//go:embed fonts/NotoSans-Bold.ttf
var notoSansBold []byte

// Poppins drives the Sonar-branded title banner, section headers, and
// table headers (#167). Licensed under SIL OFL — see fonts/OFL-Poppins.txt.

//go:embed fonts/Poppins-Regular.ttf
var poppinsRegular []byte

//go:embed fonts/Poppins-Bold.ttf
var poppinsBold []byte

// Inter drives the table body cells and the page footer (#167). Licensed
// under SIL OFL — see fonts/LICENSE-Inter.txt.

//go:embed fonts/Inter-Regular.ttf
var interRegular []byte

//go:embed fonts/Inter-Bold.ttf
var interBold []byte

// Sonar logos used in the page header (wide horizontal logo, 1010x320
// rendered from upstream SVG) and the page footer (small square Sonar
// glyph, 77x78). The header logo is rasterised at 10× the upstream
// SVG nominal size so PDF readers stay sharp at any zoom.

//go:embed images/sonar-logo-header.png
var sonarLogoHeader []byte

//go:embed images/sonar-logo-footer.png
var sonarLogoFooter []byte

// Font family names registered with fpdf.
const (
	pdfFontFamily        = "NotoSans" // Unicode fallback (legacy, kept for completeness).
	pdfFontFamilyHeading = "Poppins"  // Title banner, section + table headers (#167).
	pdfFontFamilyBody    = "Inter"    // Table body cells, footer text (#167).
)
