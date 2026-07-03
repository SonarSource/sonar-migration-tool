// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"html"
	"strings"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

// HighlightInput holds a single file's per-line highlighted HTML, as returned
// by the source server's /api/sources/lines endpoint ("code" field). Index i
// holds the HTML for source line i+1. This is the raw input from which the
// syntax-highlightings-<ref>.pb scanner-report stream is reconstructed.
type HighlightInput struct {
	Component string // component key, for ref lookup
	Lines     []string
}

// BuildSyntaxHighlighting reconstructs the per-component syntax-highlighting
// rules from the highlighted HTML lines extracted via /api/sources/lines.
//
// SonarQube does not expose the raw scanner highlighting protobuf over the web
// API; the only place the highlighting survives is the per-line "code" HTML in
// /api/sources/lines, where each highlighted token is wrapped in a
// <span class="<cssClass>"> whose class maps to a HighlightingType (see
// highlightingType). We parse that HTML back into TextRange + type rules so the
// migrated report renders with the same colors as a native scanner analysis
// (issue #420). Symbol-reference spans (class "sym"/"sym-N") carry no syntax
// type and are treated as transparent.
//
// Returned ranges are per line (StartLine == EndLine) with 0-based UTF-16
// column offsets, matching exactly what a native sonar-scanner emits — verified
// against a real scanner-report syntax-highlightings-*.pb.
//
// sources maps a component ref to the exact source text that will be written to
// source-<ref>.txt. Offsets are validated against it: the highlighting comes
// from /api/sources/lines while the source text comes from /api/sources/raw, so
// per-line lengths can diverge (CRLF, BOM, trailing whitespace). If any offset
// exceeds its line length the SonarCloud CE throws RangeOffsetConverterException
// and SILENTLY drops ALL highlighting for that file, so we clamp each range to
// the source line — at worst trimming one token instead of losing the file's
// colors. A file with no source text is skipped (highlighting would be dropped
// anyway). Passing a nil sources map disables clamping (used in unit tests).
func BuildSyntaxHighlighting(inputs []HighlightInput, sources map[int32]string, cr *ComponentRef) map[int32][]*pb.SyntaxHighlightingRule {
	result := make(map[int32][]*pb.SyntaxHighlightingRule)
	for _, in := range inputs {
		ref, ok := cr.refs[in.Component]
		if !ok {
			continue
		}
		var srcLines []string
		if sources != nil {
			src, ok := sources[ref]
			if !ok {
				continue // no source-<ref>.txt -> highlighting would be meaningless
			}
			srcLines = strings.Split(src, "\n")
		}
		var rules []*pb.SyntaxHighlightingRule
		for i, code := range in.Lines {
			line := i + 1
			lineRules := parseHighlightedLine(int32(line), code)
			if sources != nil {
				lineRules = clampRulesToSourceLine(lineRules, line, srcLines)
			}
			rules = append(rules, lineRules...)
		}
		if len(rules) > 0 {
			result[ref] = rules
		}
	}
	return result
}

// clampRulesToSourceLine drops or truncates rules so no offset exceeds the
// UTF-16 length of the matching source line (line is 1-based). The trailing CR
// of a CRLF line is excluded because the CE measures line length without the
// terminator. Rules on a line beyond the source's extent are dropped.
func clampRulesToSourceLine(rules []*pb.SyntaxHighlightingRule, line int, srcLines []string) []*pb.SyntaxHighlightingRule {
	if line-1 >= len(srcLines) {
		return nil
	}
	maxOff := utf16Len(strings.TrimRight(srcLines[line-1], "\r"))
	out := make([]*pb.SyntaxHighlightingRule, 0, len(rules))
	for _, r := range rules {
		tr := r.GetRange()
		if tr.GetStartOffset() >= maxOff {
			continue // starts at or past end of line
		}
		if tr.GetEndOffset() > maxOff {
			tr.EndOffset = maxOff
		}
		if tr.GetEndOffset() <= tr.GetStartOffset() {
			continue
		}
		out = append(out, r)
	}
	return out
}

// parseHighlightedLine converts one line of /api/sources/lines "code" HTML into
// syntax-highlighting rules. It walks the HTML left to right, maintaining a
// stack of open <span> classes; the active highlighting type for any stretch of
// text is the innermost span class that maps to a real type (so nested
// symbol-reference spans, which carry no type, are seen through). Adjacent text
// of the same type is coalesced into a single range. Column offsets are counted
// in UTF-16 code units of the HTML-unescaped text, matching the Java scanner.
func parseHighlightedLine(line int32, code string) []*pb.SyntaxHighlightingRule {
	if !strings.Contains(code, "<span") {
		return nil
	}
	lp := lineParser{line: line, curType: pb.SyntaxHighlightingRule_UNSET}
	var stack []string // raw class attribute of each currently-open span

	for i := 0; i < len(code); {
		if code[i] == '<' {
			gt := strings.IndexByte(code[i:], '>')
			if gt < 0 {
				break // malformed; stop rather than misattribute offsets
			}
			stack = applyTag(code[i:i+gt+1], stack)
			i += gt + 1
			continue
		}
		end := strings.IndexByte(code[i:], '<')
		if end < 0 {
			end = len(code) - i
		}
		lp.advance(html.UnescapeString(code[i:i+end]), activeHighlightType(stack))
		i += end
	}
	return lp.finish()
}

// lineParser accumulates highlighting rules for a single line, coalescing
// adjacent same-type text runs into one range.
type lineParser struct {
	line     int32
	col      int32
	curStart int32
	curType  pb.SyntaxHighlightingRule_HighlightingType
	rules    []*pb.SyntaxHighlightingRule
}

// advance consumes one text run of the given active type, emitting the previous
// range when the type changes.
func (p *lineParser) advance(text string, t pb.SyntaxHighlightingRule_HighlightingType) {
	if t != p.curType {
		p.flush()
		p.curType = t
		p.curStart = p.col
	}
	p.col += utf16Len(text)
}

func (p *lineParser) flush() {
	if p.curType != pb.SyntaxHighlightingRule_UNSET && p.col > p.curStart {
		p.rules = append(p.rules, &pb.SyntaxHighlightingRule{
			Range: &pb.TextRange{
				StartLine:   p.line,
				EndLine:     p.line,
				StartOffset: p.curStart,
				EndOffset:   p.col,
			},
			Type: p.curType,
		})
	}
	p.curType = pb.SyntaxHighlightingRule_UNSET
}

func (p *lineParser) finish() []*pb.SyntaxHighlightingRule {
	p.flush()
	return p.rules
}

// applyTag updates the open-span stack for a single <span>/</span> tag. Other
// tags leave the stack unchanged.
func applyTag(tag string, stack []string) []string {
	switch {
	case strings.HasPrefix(tag, "</span"):
		if len(stack) > 0 {
			return stack[:len(stack)-1]
		}
	case strings.HasPrefix(tag, "<span"):
		return append(stack, classAttr(tag))
	}
	return stack
}

// classAttr extracts the value of the class attribute from a <span ...> tag.
// Returns "" if the tag has no class attribute.
func classAttr(tag string) string {
	idx := strings.Index(tag, "class=\"")
	if idx < 0 {
		return ""
	}
	rest := tag[idx+len("class=\""):]
	if end := strings.IndexByte(rest, '"'); end >= 0 {
		return rest[:end]
	}
	return ""
}

// activeHighlightType returns the highlighting type for the current text,
// scanning the open-span stack from innermost outward and returning the first
// class that maps to a real type. Spans with no recognized type (e.g. symbol
// references) are skipped so the syntax type underneath them is still applied.
func activeHighlightType(stack []string) pb.SyntaxHighlightingRule_HighlightingType {
	for i := len(stack) - 1; i >= 0; i-- {
		if t := highlightingType(stack[i]); t != pb.SyntaxHighlightingRule_UNSET {
			return t
		}
	}
	return pb.SyntaxHighlightingRule_UNSET
}

// highlightingType maps a span class attribute (which may contain several
// space-separated tokens) to its scanner HighlightingType. The CSS class names
// are SonarQube's TypeOfText abbreviations. Returns UNSET when no token is a
// recognized syntax-highlighting class (e.g. "sym"/"sym-12" symbol references).
func highlightingType(class string) pb.SyntaxHighlightingRule_HighlightingType {
	for _, tok := range strings.Fields(class) {
		switch tok {
		case "a":
			return pb.SyntaxHighlightingRule_ANNOTATION
		case "c":
			return pb.SyntaxHighlightingRule_CONSTANT
		case "cd", "cppd":
			return pb.SyntaxHighlightingRule_COMMENT
		case "j":
			return pb.SyntaxHighlightingRule_STRUCTURED_COMMENT
		case "k":
			return pb.SyntaxHighlightingRule_KEYWORD
		case "s":
			return pb.SyntaxHighlightingRule_HIGHLIGHTING_STRING
		case "h":
			return pb.SyntaxHighlightingRule_KEYWORD_LIGHT
		case "p":
			return pb.SyntaxHighlightingRule_PREPROCESS_DIRECTIVE
		}
	}
	return pb.SyntaxHighlightingRule_UNSET
}

// utf16Len returns the number of UTF-16 code units in s. SonarQube highlighting
// offsets are Java String indices (UTF-16 code units), so a character outside
// the Basic Multilingual Plane (e.g. an emoji) counts as 2.
func utf16Len(s string) int32 {
	n := int32(0)
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
