// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package sqapi

import (
	"errors"
	"net/http"
	"testing"
)

// The project-scope rejection is the mirror of the org-scope one. It must
// match on the invariant stem only: the trailing noun is an i18n label
// ("qualifier.TRK=Project"), so a portfolio or application produces a
// different word, and SonarCloud escapes apostrophes in the raw body.
func TestIsProjectLevelRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "project scope rejection",
			err:  &APIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"msg":"Setting 'sonar.dbcleaner.daysBeforeDeletingClosedIssues' cannot be set on a Project"}]}`},
			want: true,
		},
		{
			name: "other qualifier label still matches the stem",
			err:  &APIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"msg":"Setting 'x' cannot be set on a Portfolio"}]}`},
			want: true,
		},
		{
			name: "escaped apostrophe in raw body",
			err:  &APIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"msg":"Setting &#39;x&#39; cannot be set on a Project"}]}`},
			want: true,
		},
		{
			name: "org-level rejection is a different class",
			err:  &APIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"msg":"Provided property can't be set at organization level: x"}]}`},
			want: false,
		},
		{
			name: "not a 400",
			err:  &APIError{StatusCode: http.StatusNotFound, Body: `{"errors":[{"msg":"cannot be set on a Project"}]}`},
			want: false,
		},
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: errors.New("cannot be set on a Project"), want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsProjectLevelRejection(c.err); got != c.want {
				t.Errorf("IsProjectLevelRejection = %v, want %v", got, c.want)
			}
		})
	}
}

// SonarQube destroys its own "already exists" message for metrics whose
// short name contains a percent sign, by running the interpolated string
// through String.format a second time. The client sees only the
// UnknownFormatConversionException text.
func TestIsMangledAlreadyExists(t *testing.T) {
	mangled := &APIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"msg":"Conversion = ')'"}]}`}
	if !IsMangledAlreadyExists(mangled) {
		t.Error("expected the mangled already-exists 400 to be recognised")
	}
	// The readable form is handled by IsAlreadyExists, not this detector.
	readable := &APIError{StatusCode: http.StatusBadRequest, Body: `{"errors":[{"msg":"Condition on metric 'Coverage on New Code' already exists."}]}`}
	if IsMangledAlreadyExists(readable) {
		t.Error("readable already-exists must not match the mangled detector")
	}
	if !IsAlreadyExists(readable) {
		t.Error("readable already-exists must match IsAlreadyExists")
	}
	if IsMangledAlreadyExists(&APIError{StatusCode: http.StatusInternalServerError, Body: `Conversion = ')'`}) {
		t.Error("only 400 should match")
	}
}
