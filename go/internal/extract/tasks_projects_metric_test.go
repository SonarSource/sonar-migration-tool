package extract

import (
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// SonarQube Server 9.9 exposes the "accepted" issue metric as
// wont_fix_issues; 10.2+ renamed it to accepted_issues. Issue #278.
func TestMeasureMetricKeysFor_VersionRename(t *testing.T) {
	cases := []struct {
		version string
		want    string
		notWant string
		name    string
	}{
		{version: "9.9", want: "wont_fix_issues", notWant: "accepted_issues", name: "SQS 9.9"},
		{version: "9.9.3.12345", want: "wont_fix_issues", notWant: "accepted_issues", name: "SQS 9.9.3 with build"},
		{version: "10.1", want: "wont_fix_issues", notWant: "accepted_issues", name: "SQS 10.1 (still old name)"},
		{version: "10.2", want: "accepted_issues", notWant: "wont_fix_issues", name: "SQS 10.2 (rename landed)"},
		{version: "10.2.0", want: "accepted_issues", notWant: "wont_fix_issues", name: "SQS 10.2.0 (trailing zero)"},
		{version: "10.7", want: "accepted_issues", notWant: "wont_fix_issues", name: "SQS 10.7"},
		{version: "10.10", want: "accepted_issues", notWant: "wont_fix_issues", name: "SQS 10.10 (float would collapse to 10.1)"},
		{version: "2025.1", want: "accepted_issues", notWant: "wont_fix_issues", name: "Year-versioned 2025.1"},
		{version: "2026.4.0.123541", want: "accepted_issues", notWant: "wont_fix_issues", name: "Year-versioned 2026.4 with build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := measureMetricKeysFor(common.ParseVersion(tc.version))
			if !strings.Contains(got, tc.want) {
				t.Errorf("expected %q in metrics list, got %q", tc.want, got)
			}
			if strings.Contains(got, tc.notWant) {
				t.Errorf("did not expect %q in metrics list, got %q", tc.notWant, got)
			}
		})
	}
}
