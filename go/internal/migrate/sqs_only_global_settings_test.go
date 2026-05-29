package migrate

import (
	"encoding/json"
	"testing"
)

func mkRaw(t *testing.T, payload map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestEvaluateSQSOnlyGlobalSetting covers the curated #240 list:
//   - settings on the list emit the standard explanation when set to a
//     non-default value; silent otherwise.
//   - settings off the list always come back isSQSOnly=false.
func TestEvaluateSQSOnlyGlobalSetting(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		raw         map[string]any
		wantSQSOnly bool
	}{
		{
			name:        "sandbox.enabled=true surfaces",
			key:         "sonar.issues.sandbox.enabled",
			raw:         map[string]any{"key": "sonar.issues.sandbox.enabled", "value": "true"},
			wantSQSOnly: true,
		},
		{
			name:        "sandbox.enabled=false silent (default)",
			key:         "sonar.issues.sandbox.enabled",
			raw:         map[string]any{"key": "sonar.issues.sandbox.enabled", "value": "false"},
			wantSQSOnly: false,
		},
		{
			name:        "sandbox.override.enabled=true surfaces",
			key:         "sonar.issues.sandbox.override.enabled",
			raw:         map[string]any{"key": "sonar.issues.sandbox.override.enabled", "value": "true"},
			wantSQSOnly: true,
		},
		{
			name:        "sandbox.software-qualities non-empty surfaces",
			key:         "sonar.issues.sandbox.software-qualities",
			raw:         map[string]any{"key": "sonar.issues.sandbox.software-qualities", "value": "RELIABILITY"},
			wantSQSOnly: true,
		},
		{
			name:        "sandbox.software-qualities empty silent",
			key:         "sonar.issues.sandbox.software-qualities",
			raw:         map[string]any{"key": "sonar.issues.sandbox.software-qualities", "value": ""},
			wantSQSOnly: false,
		},
		{
			name:        "allowPermissionManagement=true surfaces",
			key:         "sonar.allowPermissionManagementForProjectAdmins",
			raw:         map[string]any{"key": "sonar.allowPermissionManagementForProjectAdmins", "value": "true"},
			wantSQSOnly: true,
		},
		{
			name:        "allowPermissionManagement=false silent",
			key:         "sonar.allowPermissionManagementForProjectAdmins",
			raw:         map[string]any{"key": "sonar.allowPermissionManagementForProjectAdmins", "value": "false"},
			wantSQSOnly: false,
		},
		{
			name:        "allowDisableInheritedRules=true (existing #200 entry) still surfaces",
			key:         "sonar.qualityProfiles.allowDisableInheritedRules",
			raw:         map[string]any{"key": "sonar.qualityProfiles.allowDisableInheritedRules", "value": "true"},
			wantSQSOnly: true,
		},
		{
			name:        "technicalDebt.ratingGrid customised value surfaces",
			key:         "sonar.technicalDebt.ratingGrid",
			raw:         map[string]any{"key": "sonar.technicalDebt.ratingGrid", "value": "0.1,0.2,0.5,1.0"},
			wantSQSOnly: true,
		},
		{
			name:        "technicalDebt.ratingGrid default value silent",
			key:         "sonar.technicalDebt.ratingGrid",
			raw:         map[string]any{"key": "sonar.technicalDebt.ratingGrid", "value": "0.05,0.1,0.2,0.5"},
			wantSQSOnly: false,
		},
		{
			name:        "off-list key (e.g. sonar.exclusions) is not SQS-only",
			key:         "sonar.exclusions",
			raw:         map[string]any{"key": "sonar.exclusions", "value": "**/vendor/**"},
			wantSQSOnly: false,
		},
		{
			name:        "off-list key sonar.core.id no longer treated as SQS-only here (handled by dynamic discovery)",
			key:         "sonar.core.id",
			raw:         map[string]any{"key": "sonar.core.id", "value": "server-uuid"},
			wantSQSOnly: false,
		},
		{
			name:        "sonar.core.startTime is silent (read-only) — not surfaced as SQS-only either",
			key:         "sonar.core.startTime",
			raw:         map[string]any{"key": "sonar.core.startTime", "value": "1717000000000"},
			wantSQSOnly: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note, isSQSOnly := EvaluateSQSOnlyGlobalSetting(tc.key, mkRaw(t, tc.raw))
			if isSQSOnly != tc.wantSQSOnly {
				t.Errorf("isSQSOnly: got %v, want %v (note=%q)", isSQSOnly, tc.wantSQSOnly, note)
			}
			if tc.wantSQSOnly && note == "" {
				t.Errorf("expected a non-empty explanatory note, got empty string")
			}
		})
	}
}

// TestIsSilentlySkippedGlobalSetting covers the "drop from the report
// entirely" verdict — used by predict to keep its output consistent
// with real-migrate's partitionSQSOnlySettings.
func TestIsSilentlySkippedGlobalSetting(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		raw      map[string]any
		wantSkip bool
	}{
		{
			name:     "sonar.core.startTime is silently skipped (read-only)",
			key:      "sonar.core.startTime",
			raw:      map[string]any{"key": "sonar.core.startTime", "value": "1717000000000"},
			wantSkip: true,
		},
		{
			name:     "sonar.core.serverBaseURL is silently skipped",
			key:      "sonar.core.serverBaseURL",
			raw:      map[string]any{"key": "sonar.core.serverBaseURL", "value": "https://sqs.example.com"},
			wantSkip: true,
		},
		{
			name:     "sandbox.enabled=true is NOT silently skipped (surfaces in report)",
			key:      "sonar.issues.sandbox.enabled",
			raw:      map[string]any{"key": "sonar.issues.sandbox.enabled", "value": "true"},
			wantSkip: false,
		},
		{
			name:     "sandbox.enabled=false IS silently skipped (default value)",
			key:      "sonar.issues.sandbox.enabled",
			raw:      map[string]any{"key": "sonar.issues.sandbox.enabled", "value": "false"},
			wantSkip: true,
		},
		{
			name:     "off-list keys are never silently skipped here",
			key:      "sonar.exclusions",
			raw:      map[string]any{"key": "sonar.exclusions", "value": "**/vendor/**"},
			wantSkip: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSilentlySkippedGlobalSetting(tc.key, mkRaw(t, tc.raw)); got != tc.wantSkip {
				t.Errorf("got %v, want %v", got, tc.wantSkip)
			}
		})
	}
}
