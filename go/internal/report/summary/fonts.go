package summary

import _ "embed"

// Noto Sans is embedded for Unicode-capable PDF rendering. fpdf's built-in
// core fonts (Helvetica, Times, Courier) are limited to the Windows-1252
// single-byte encoding, which mangles any character outside that range —
// accented Latin, Greek/Cyrillic letters, dashes, quotes, emoji, etc.
//
// Coverage: Latin (incl. extended), Greek, Cyrillic, plus common symbols.
// Glyphs outside this subset (e.g. emoji, CJK) will fall back to the PDF
// missing-glyph box rather than producing garbled text.
//
// Licensed under the SIL Open Font License v1.1 (see fonts/OFL.txt).

//go:embed fonts/NotoSans-Regular.ttf
var notoSansRegular []byte

//go:embed fonts/NotoSans-Bold.ttf
var notoSansBold []byte

// pdfFontFamily is the family name registered with fpdf for the embedded font.
const pdfFontFamily = "NotoSans"
