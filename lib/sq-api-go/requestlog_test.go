// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"net/url"
	"testing"
)

// requests.log is written to the run directory in plaintext and routinely
// attached to support tickets, so it must not become a credential leak.
func TestRedactFormValue(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value string
		all   url.Values
		want  string
	}{
		{
			name:  "secured setting value is redacted",
			field: "value", value: "s3cr3t",
			all:  url.Values{"key": {"sonar.auth.github.clientSecret.secured"}, "value": {"s3cr3t"}},
			want: "<redacted>",
		},
		{
			name:  "secured setting multi-value is redacted",
			field: "values", value: "s3cr3t",
			all:  url.Values{"key": {"sonar.something.secured"}, "values": {"s3cr3t"}},
			want: "<redacted>",
		},
		{
			name:  "inherently secret field is redacted regardless of key",
			field: "password", value: "hunter2",
			all:  url.Values{"password": {"hunter2"}},
			want: "<redacted>",
		},
		{
			name:  "token field is redacted",
			field: "token", value: "squ_abc",
			all:  url.Values{"token": {"squ_abc"}},
			want: "<redacted>",
		},
		{
			name:  "ordinary setting value is preserved",
			field: "value", value: "**/gen/**",
			all:  url.Values{"key": {"sonar.exclusions"}, "value": {"**/gen/**"}},
			want: "**/gen/**",
		},
		{
			name:  "setting key itself is preserved so failures stay diagnosable",
			field: "key", value: "sonar.auth.github.clientSecret.secured",
			all:  url.Values{"key": {"sonar.auth.github.clientSecret.secured"}},
			want: "sonar.auth.github.clientSecret.secured",
		},
		{
			name:  "empty stays empty",
			field: "value", value: "",
			all:  url.Values{"key": {"sonar.x.secured"}, "value": {""}},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := redactFormValue(c.field, c.value, c.all); got != c.want {
				t.Errorf("redactFormValue(%q, %q) = %q, want %q", c.field, c.value, got, c.want)
			}
		})
	}
}
