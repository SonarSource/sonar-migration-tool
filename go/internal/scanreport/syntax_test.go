// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"testing"

	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

type rng struct {
	start, end int32
	typ        pb.SyntaxHighlightingRule_HighlightingType
}

func gotRanges(rules []*pb.SyntaxHighlightingRule) []rng {
	out := make([]rng, 0, len(rules))
	for _, r := range rules {
		out = append(out, rng{r.GetRange().GetStartOffset(), r.GetRange().GetEndOffset(), r.GetType()})
	}
	return out
}

func TestParseHighlightedLine(t *testing.T) {
	cases := []struct {
		name string
		code string
		want []rng
	}{
		{
			name: "plain text, no spans",
			code: "    plain = value",
			want: nil,
		},
		{
			name: "empty line",
			code: "",
			want: nil,
		},
		{
			name: "single comment span",
			code: `<span class="cd"># sonar-tools</span>`,
			want: []rng{{0, 13, pb.SyntaxHighlightingRule_COMMENT}},
		},
		{
			name: "two keywords separated by plain text (from X import)",
			code: `<span class="k">from</span> __future__ <span class="k">import</span> <span class="sym-1 sym">annotations</span>`,
			want: []rng{
				{0, 4, pb.SyntaxHighlightingRule_KEYWORD},
				{16, 22, pb.SyntaxHighlightingRule_KEYWORD},
			},
		},
		{
			name: "symbol span alone yields no rule",
			code: `<span class="sym-10 sym">os</span>.path`,
			want: nil,
		},
		{
			name: "keyword nested inside symbol span (depth 2)",
			code: `<span class="sym-3 sym"><span class="k">class</span></span> Foo`,
			want: []rng{{0, 5, pb.SyntaxHighlightingRule_KEYWORD}},
		},
		{
			name: "string then constant",
			code: `<span class="s">"hi"</span> = <span class="c">42</span>`,
			want: []rng{
				{0, 4, pb.SyntaxHighlightingRule_HIGHLIGHTING_STRING},
				{7, 9, pb.SyntaxHighlightingRule_CONSTANT},
			},
		},
		{
			name: "html entities count as one char each",
			// decoded prefix "a < b " is 6 cols (&lt; -> 1 char); spanned "&&" is 2 cols.
			code: `a &lt; b <span class="k">&amp;&amp;</span> c`,
			want: []rng{{6, 8, pb.SyntaxHighlightingRule_KEYWORD}},
		},
		{
			name: "structured comment (javadoc/docstring)",
			code: `<span class="j">"""docstring"""</span>`,
			want: []rng{{0, 15, pb.SyntaxHighlightingRule_STRUCTURED_COMMENT}},
		},
		{
			name: "preprocess directive and annotation and keyword_light",
			code: `<span class="p">#include</span> <span class="a">@Override</span> <span class="h">var</span>`,
			want: []rng{
				{0, 8, pb.SyntaxHighlightingRule_PREPROCESS_DIRECTIVE},
				{9, 18, pb.SyntaxHighlightingRule_ANNOTATION},
				{19, 22, pb.SyntaxHighlightingRule_KEYWORD_LIGHT},
			},
		},
		{
			name: "unknown class produces no rule",
			code: `<span class="zzz">weird</span>`,
			want: nil,
		},
		{
			name: "malformed unterminated tag stops cleanly",
			code: `<span class="k">def</span> foo <span class="k"`,
			want: []rng{{0, 3, pb.SyntaxHighlightingRule_KEYWORD}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gotRanges(parseHighlightedLine(7, tc.code))
			if len(got) != len(tc.want) {
				t.Fatalf("range count: got %d %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("range[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
			// Every emitted rule must be a single-line range anchored to the line.
			for _, r := range parseHighlightedLine(7, tc.code) {
				if r.GetRange().GetStartLine() != 7 || r.GetRange().GetEndLine() != 7 {
					t.Errorf("expected single-line range on line 7, got %+v", r.GetRange())
				}
			}
		})
	}
}

func TestUTF16Len(t *testing.T) {
	cases := []struct {
		s    string
		want int32
	}{
		{"", 0},
		{"abc", 3},
		{"café", 4},         // é is BMP -> 1 unit
		{"aéb", 3},          // precomposed é
		{"x\U0001F600y", 4}, // emoji is astral -> 2 units, plus x and y
	}
	for _, c := range cases {
		if got := utf16Len(c.s); got != c.want {
			t.Errorf("utf16Len(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestBuildSyntaxHighlighting(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("proj")                 // ref 1 (root)
	fileRef := cr.Get("proj:a.py") // ref 2

	inputs := []HighlightInput{
		{
			Component: "proj:a.py",
			Lines: []string{
				`<span class="k">def</span> foo():`, // line 1
				`    <span class="cd"># hi</span>`,  // line 2
				"    pass",                          // line 3, no highlighting
			},
		},
		{
			Component: "proj:missing.py", // not in ref map -> skipped
			Lines:     []string{`<span class="k">if</span>`},
		},
	}
	sources := map[int32]string{
		fileRef: "def foo():\n    # hi\n    pass\n",
	}

	got := BuildSyntaxHighlighting(inputs, sources, cr)

	if len(got) != 1 {
		t.Fatalf("expected highlighting for exactly 1 known component, got %d", len(got))
	}
	rules, ok := got[fileRef]
	if !ok {
		t.Fatalf("expected rules for ref %d", fileRef)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules (def, comment), got %d", len(rules))
	}
	// Line 1: keyword "def" at [0,3)
	if r := rules[0].GetRange(); r.GetStartLine() != 1 || r.GetStartOffset() != 0 || r.GetEndOffset() != 3 {
		t.Errorf("rule0 range = %+v, want line1 [0,3)", r)
	}
	if rules[0].GetType() != pb.SyntaxHighlightingRule_KEYWORD {
		t.Errorf("rule0 type = %v, want KEYWORD", rules[0].GetType())
	}
	// Line 2: comment "# hi" at [4,8)
	if r := rules[1].GetRange(); r.GetStartLine() != 2 || r.GetStartOffset() != 4 || r.GetEndOffset() != 8 {
		t.Errorf("rule1 range = %+v, want line2 [4,8)", r)
	}
	if rules[1].GetType() != pb.SyntaxHighlightingRule_COMMENT {
		t.Errorf("rule1 type = %v, want COMMENT", rules[1].GetType())
	}
}

// TestBuildSyntaxHighlightingClampsToSource guards the robustness fix: when the
// highlighted HTML (from /api/sources/lines) implies an offset longer than the
// source line actually shipped (from /api/sources/raw), the rule must be clamped
// or dropped so the CE never silently discards the whole file's highlighting.
func TestBuildSyntaxHighlightingClampsToSource(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("proj")
	ref := cr.Get("proj:a.py")

	inputs := []HighlightInput{{
		Component: "proj:a.py",
		Lines: []string{
			`<span class="s">"abcdef"</span>`, // highlight implies cols [0,8)
			`<span class="k">keyword</span>`,  // highlight implies cols [0,7) but no such source line
		},
	}}
	// Source line 1 is only 4 chars ("abc\r" -> CR stripped -> "abc"=3? use "abcd").
	// There is no source line 2 (file is a single line).
	sources := map[int32]string{ref: "abcd"}

	rules := BuildSyntaxHighlighting(inputs, sources, cr)[ref]
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (line1 clamped, line2 dropped), got %d: %+v", len(rules), rules)
	}
	r := rules[0].GetRange()
	if r.GetStartLine() != 1 || r.GetStartOffset() != 0 || r.GetEndOffset() != 4 {
		t.Errorf("expected line1 clamped to [0,4), got %+v", r)
	}
}

func TestClampStripsTrailingCR(t *testing.T) {
	cr := NewComponentRef()
	cr.Get("proj")
	ref := cr.Get("proj:a.py")
	inputs := []HighlightInput{{
		Component: "proj:a.py",
		Lines:     []string{`<span class="k">abc</span>`}, // [0,3)
	}}
	// CRLF source: split on "\n" leaves "abc\r"; CE measures length without CR = 3,
	// so [0,3) must survive unclamped.
	sources := map[int32]string{ref: "abc\r\nnext"}
	rules := BuildSyntaxHighlighting(inputs, sources, cr)[ref]
	if len(rules) != 1 || rules[0].GetRange().GetEndOffset() != 3 {
		t.Fatalf("CRLF line should keep [0,3), got %+v", rules)
	}
}
