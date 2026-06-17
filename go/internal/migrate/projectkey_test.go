// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import "testing"

func TestValidateProjectKeyPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"default", DefaultProjectKeyPattern, false},
		{"org and original", "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>", false},
		{"static prefix with org", "ACME_CORP_<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>", false},
		{"org with postfix", "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>_migrated", false},
		{"static-only prefix 5 chars", "acme_<ORIGINAL_PROJECT_KEY>", false},
		{"static-only prefix long", "MYCORP_<ORIGINAL_PROJECT_KEY>", false},
		{"prefix and postfix combined", "sqs_<ORIGINAL_PROJECT_KEY>_migrated", false},
		{"postfix only ≥5 chars", "<ORIGINAL_PROJECT_KEY>_migrated", false},
		{"bare original (keep unchanged)", "<ORIGINAL_PROJECT_KEY>", false},
		{"bare original via alias", "<PROJECT_KEY>", false},
		{"alias with org", "<ORGANIZATION_KEY>_<PROJECT_KEY>", false},
		{"lowercase accepted (case-insensitive)", "<organization_key>_<original_project_key>", false},

		{"static prefix too short", "AAA_<ORIGINAL_PROJECT_KEY>", true},
		{"static prefix 4 chars", "abc_<ORIGINAL_PROJECT_KEY>", true},
		{"short postfix only", "<ORIGINAL_PROJECT_KEY>_x", true},
		{"no original placeholder", "<ORGANIZATION_KEY>_static", true},
		{"zero placeholders", "ACME_CORP", true},
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"unknown placeholder", "<TEAM_KEY>_<ORIGINAL_PROJECT_KEY>", true},
		{"old org_key name now rejected", "<ORG_KEY>_<ORIGINAL_PROJECT_KEY>", true},
		{"enterprise placeholder now rejected", "<ENTERPRISE_KEY>_<ORIGINAL_PROJECT_KEY>", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProjectKeyPattern(tc.pattern)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.pattern)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for %q, got %v", tc.pattern, err)
			}
		})
	}
}

func TestRenderProjectKey(t *testing.T) {
	cases := []struct {
		name     string
		pattern  string
		original string
		org      string
		want     string
	}{
		{"default reproduces legacy", DefaultProjectKeyPattern, "my-proj", "myorg", "myorg_my-proj"},
		{"org then original", "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>", "p", "o", "o_p"},
		{"static prefix", "ACME_CORP_<ORIGINAL_PROJECT_KEY>", "p", "o", "ACME_CORP_p"},
		{"prefix and postfix", "sqs_<ORIGINAL_PROJECT_KEY>_migrated", "p", "o", "sqs_p_migrated"},
		{"org with postfix", "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>_migrated", "p", "o", "o_p_migrated"},
		{"keep unchanged", "<ORIGINAL_PROJECT_KEY>", "p", "o", "p"},
		{"alias keep unchanged", "<PROJECT_KEY>", "p", "o", "p"},
		{"lowercase canonicalised", "<organization_key>_<original_project_key>", "p", "o", "o_p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderProjectKey(tc.pattern, tc.original, tc.org)
			if got != tc.want {
				t.Fatalf("RenderProjectKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProjectKeyAffixes(t *testing.T) {
	cases := []struct {
		name       string
		pattern    string
		org        string
		wantPrefix string
		wantSuffix string
	}{
		{"default", DefaultProjectKeyPattern, "o", "o_", ""},
		{"static prefix no org", "ACME_CORP_<ORIGINAL_PROJECT_KEY>", "o", "ACME_CORP_", ""},
		{"keep unchanged", "<ORIGINAL_PROJECT_KEY>", "o", "", ""},
		{"suffix", "<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>_x", "o", "o_", "_x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix, suffix := ProjectKeyAffixes(tc.pattern, tc.org)
			if prefix != tc.wantPrefix || suffix != tc.wantSuffix {
				t.Fatalf("ProjectKeyAffixes = (%q, %q), want (%q, %q)", prefix, suffix, tc.wantPrefix, tc.wantSuffix)
			}
		})
	}
}

func TestPatternUsesOrg(t *testing.T) {
	if !PatternUsesOrg("<ORGANIZATION_KEY>_<ORIGINAL_PROJECT_KEY>") {
		t.Fatal("expected PatternUsesOrg true for org pattern")
	}
	if !PatternUsesOrg("<organization_key>_<original_project_key>") {
		t.Fatal("expected PatternUsesOrg true for lowercase org pattern")
	}
	if PatternUsesOrg("ACME_CORP_<ORIGINAL_PROJECT_KEY>") {
		t.Fatal("expected PatternUsesOrg false for static-prefix pattern")
	}
}
