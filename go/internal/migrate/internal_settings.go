package migrate

import "strings"

// internalSettingPrefixes lists SonarQube Server setting key prefixes
// that are considered "internal" — server-emitted metadata, plugin
// manifest fields, license / consent flags, etc. — and should never
// appear in the migration report. Ported from sonar-tools'
// _SQ_INTERNAL_SETTINGS (see okorach/sonar-tools/sonar/settings.py).
//
// The filter is intentionally narrower than the curated sqsOnlySettings
// + sqsOnlyPrefixes lists which classify "SQS-only" settings for the
// report (those still surface as Skipped with explanatory notes). An
// internal setting is one we want to drop entirely from extract /
// report consideration.
var internalSettingPrefixes = []string{
	"sonaranalyzer",                  // covers sonaranalyzer-cs.*, sonaranalyzer-vbnet.*, sonaranalyzer.security.cs.*, ...
	"sonar.updatecenter",             // server update-center plumbing
	"sonar.plugins.risk.consent",     // license / risk-acceptance flag
	"sonar.core.id",                  // server identity
	"sonar.core.startTime",           // read-only server timestamp
	"sonar.plsql.jdbc.driver.class",  // server JDBC plumbing
	"sonar.documentation.baseUrl",    // server-only doc URL
}

// IsInternalSqsSetting reports whether the named SonarQube Server
// global / project setting is internal and should be excluded from the
// migration pipeline (extract, real migrate, and predictive report).
// Matching is prefix-based via the curated list above.
func IsInternalSqsSetting(key string) bool {
	for _, prefix := range internalSettingPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
