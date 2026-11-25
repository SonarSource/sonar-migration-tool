// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func TestProfileFindingKindMessage(t *testing.T) {
	cases := []struct {
		kind    string
		wantSub string
	}{
		{kindCustomSeverity, "reverted to their default severities: a, b"},
		{kindThirdParty, "3rd party rules were removed from the quality profile: a, b"},
		{"prioritized", "migrated in the profile as regular rules: a, b"},
		{kindTemplateInstance, "the following rules were not migrated: a, b"},
		{kindCustomParams, "custom parameters could not be migrated due to an unexpected error: a, b"},
		{kindDisabledInherited, "the following rules were enabled in the profile: a, b"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			got := profileFindingKindMessage(tc.kind, []string{"a", "b"})
			if !strings.HasSuffix(got, tc.wantSub) {
				t.Errorf("message for %q = %q, want suffix %q", tc.kind, got, tc.wantSub)
			}
		})
	}
	if got := profileFindingKindMessage("unknown-kind", []string{"a"}); got != "" {
		t.Errorf("default kind message = %q, want empty", got)
	}
}

func TestDedupRuleEntries(t *testing.T) {
	entries := []ruleEntry{
		{"b", "d1"},
		{"a", ""},
		{"b", "d1"},
		{"b", "d2"},
		{"a", "x"},
		{"c", ""},
	}
	order, byRule := dedupRuleEntries(entries)
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	if want := []string{"x"}; !reflect.DeepEqual(byRule["a"], want) {
		t.Errorf("a details = %v, want %v", byRule["a"], want)
	}
	if want := []string{"d1", "d2"}; !reflect.DeepEqual(byRule["b"], want) {
		t.Errorf("b details = %v, want %v", byRule["b"], want)
	}
	if len(byRule["c"]) != 0 {
		t.Errorf("c details = %v, want none", byRule["c"])
	}
	if _, ok := byRule["c"]; !ok {
		t.Error("rule with only empty details must still be present")
	}
}

func TestDedupRuleEntries_Empty(t *testing.T) {
	order, byRule := dedupRuleEntries(nil)
	if len(order) != 0 || len(byRule) != 0 {
		t.Errorf("got order=%v byRule=%v, want empty", order, byRule)
	}
}

func TestSuppressTemplateInstanceDuplicates(t *testing.T) {
	t.Run("filters overlapping keys", func(t *testing.T) {
		kinds := map[string][]ruleEntry{
			kindTemplateInstance: {{key: "r1"}},
			kindCustomSeverity:   {{key: "r1"}, {key: "r2"}},
		}
		suppressTemplateInstanceDuplicates(kinds)
		want := []ruleEntry{{key: "r2"}}
		if !reflect.DeepEqual(kinds[kindCustomSeverity], want) {
			t.Errorf("custom-severity = %v, want %v", kinds[kindCustomSeverity], want)
		}
	})
	t.Run("deletes kind when all filtered", func(t *testing.T) {
		kinds := map[string][]ruleEntry{
			kindTemplateInstance: {{key: "r1"}, {key: "r2"}},
			kindCustomSeverity:   {{key: "r1"}, {key: "r2"}},
		}
		suppressTemplateInstanceDuplicates(kinds)
		if _, ok := kinds[kindCustomSeverity]; ok {
			t.Error("custom-severity should be deleted when emptied")
		}
		if len(kinds[kindTemplateInstance]) != 2 {
			t.Error("template-instance must be untouched")
		}
	})
	t.Run("no template kind is a no-op", func(t *testing.T) {
		kinds := map[string][]ruleEntry{kindCustomSeverity: {{key: "r1"}}}
		suppressTemplateInstanceDuplicates(kinds)
		if len(kinds[kindCustomSeverity]) != 1 {
			t.Error("custom-severity should be untouched")
		}
	})
	t.Run("no custom-severity kind is a no-op", func(t *testing.T) {
		kinds := map[string][]ruleEntry{kindTemplateInstance: {{key: "r1"}}}
		suppressTemplateInstanceDuplicates(kinds)
		if len(kinds) != 1 {
			t.Errorf("kinds = %v, want unchanged", kinds)
		}
	})
	t.Run("other kinds ignored", func(t *testing.T) {
		kinds := map[string][]ruleEntry{
			kindTemplateInstance: {{key: "r1"}},
			kindThirdParty:       {{key: "r1"}},
		}
		suppressTemplateInstanceDuplicates(kinds)
		if len(kinds[kindThirdParty]) != 1 {
			t.Error("third-party must not be filtered")
		}
	})
}

func TestBuildProfileLines(t *testing.T) {
	kinds := map[string][]ruleEntry{
		kindDisabledInherited: {{key: "z"}},
		kindCustomSeverity:    {{key: "a"}},
		kindThirdParty:        {},
		"not-in-order":        {{key: "q"}},
	}
	lines := buildProfileLines(kinds)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "default severities: a") {
		t.Errorf("first line should be custom-severity, got %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "were enabled in the profile: z") {
		t.Errorf("second line should be disabled-inherited, got %q", lines[1])
	}
	if got := buildProfileLines(nil); len(got) != 0 {
		t.Errorf("nil kinds lines = %v, want none", got)
	}
}

func TestRenderProfileFindingLine(t *testing.T) {
	t.Run("fixed message dedups and sorts", func(t *testing.T) {
		got := renderProfileFindingLine(kindThirdParty, []ruleEntry{{"b", "repo"}, {"a", ""}, {"b", "repo"}})
		want := "Because SQC does not support 3rd party plugins, the following 3rd party rules were removed from the quality profile: a, b"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("per-rule fallthrough with and without details", func(t *testing.T) {
		got := renderProfileFindingLine("unknown-kind", []ruleEntry{{"b", "d1"}, {"a", ""}, {"b", "d2"}, {"b", "d1"}})
		want := ":\na\nb (d1, d2)"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestCollectProfileFindings_Helpers(t *testing.T) {
	dir := t.TempDir()
	taskDir := filepath.Join(dir, "analyzeProfileRules")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := []profileFinding{
		{CloudProfileKey: "p1", Kind: kindThirdParty, RuleKey: "x:r1"},
		{CloudProfileKey: "p2", Kind: kindCustomSeverity, RuleKey: "java:S1"},
		{CloudProfileKey: "p2", Kind: kindTemplateInstance, RuleKey: "java:S1"},
		{CloudProfileKey: "p3", Kind: kindCustomSeverity, RuleKey: "java:S2"},
		{CloudProfileKey: "", Kind: kindCustomSeverity, RuleKey: "dropped"},
		{CloudProfileKey: "p4", Kind: "", RuleKey: "dropped"},
		{CloudProfileKey: "p4", Kind: kindCustomSeverity, RuleKey: ""},
	}
	var sb strings.Builder
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	sb.WriteString("not json\n")
	if err := os.WriteFile(filepath.Join(taskDir, "results.1.jsonl"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got := collectProfileFindings(common.NewDataStore(dir))
	if len(got) != 3 {
		t.Fatalf("got %d profiles, want 3: %v", len(got), got)
	}
	if !got["p1"].HasOrangeCriterion {
		t.Error("p1 (third-party) should have orange criterion")
	}
	if got["p2"].HasOrangeCriterion || got["p3"].HasOrangeCriterion {
		t.Error("p2/p3 should not have orange criterion")
	}
	if len(got["p2"].Issues) != 1 || !strings.Contains(got["p2"].Issues[0], "were not migrated: java:S1") {
		t.Errorf("p2 issues = %v, want only template-instance line", got["p2"].Issues)
	}
	if _, ok := got["p4"]; ok {
		t.Error("p4 had only invalid rows and must be omitted")
	}
	if collectProfileFindings(common.NewDataStore(t.TempDir())) != nil {
		t.Error("missing task data should yield nil")
	}
}
