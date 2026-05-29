package migrate

import "testing"

func TestIsInternalSqsSetting(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// Direct matches from sonar-tools _SQ_INTERNAL_SETTINGS.
		{"sonar.core.id", true},
		{"sonar.core.startTime", true},
		{"sonar.plugins.risk.consent", true},
		{"sonar.plsql.jdbc.driver.class", true},
		{"sonar.documentation.baseUrl", true},
		// Prefix matches.
		{"sonar.updatecenter.url", true},
		{"sonar.updatecenter.cache.ttl", true},
		{"sonaranalyzer-cs.pluginVersion", true},
		{"sonaranalyzer-cs.analyzerId", true},
		{"sonaranalyzer-vbnet.staticResourceName", true},
		{"sonaranalyzer.security.cs.ruleNamespace", true},
		// Off-list keys: NOT internal — they may still be SQS-only via
		// the curated handlers, but they should appear in the report.
		{"sonar.exclusions", false},
		{"sonar.coverage.exclusions", false},
		{"sonar.issues.sandbox.enabled", false},
		{"sonar.cs.analyzer.dotnet.pluginVersion", false}, // separate prefix; handled by sqsOnlyPrefixes
		{"sonar.qualityProfiles.allowDisableInheritedRules", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if got := IsInternalSqsSetting(c.key); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
