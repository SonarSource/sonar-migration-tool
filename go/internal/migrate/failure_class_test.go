// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"errors"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	sqapi "github.com/sonar-solutions/sq-api-go"
)

func apiErr(status int, msg string) error {
	return &sqapi.APIError{StatusCode: status, Method: "POST", URL: "/api/x",
		Body: `{"errors":[{"msg":"` + msg + `"}]}`}
}

// The classification is the difference between "nothing you can do" and
// "please report this", so each class must be reachable and an
// unrecognised rejection must never be quietly excused.
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		wantClass      FailureClass
		wantReportable bool
	}{
		{
			name:      "setting has no project scope on Cloud",
			err:       apiErr(400, "Setting 'sonar.dbcleaner.x' cannot be set on a Project"),
			wantClass: FailureByDesign,
		},
		{
			name:      "setting cannot be written at org scope",
			err:       apiErr(400, "Provided property can't be set at organization level: x"),
			wantClass: FailureByDesign,
		},
		{
			name:      "entity already exists",
			err:       apiErr(400, "Quality gate with name 'x' already exists"),
			wantClass: FailureAlreadyDone,
		},
		{
			name:      "mangled already-exists is still already-done",
			err:       apiErr(400, "Conversion = ')'"),
			wantClass: FailureAlreadyDone,
		},
		{
			name:      "private projects not permitted is an account state",
			err:       apiErr(400, "The organization 'x' is not allowed to use private projects."),
			wantClass: FailureEnvironment,
		},
		{
			name:      "org not bound to a DevOps platform",
			err:       apiErr(400, "This organization is not bound to an ALM application"),
			wantClass: FailureEnvironment,
		},
		{name: "unauthorized", err: apiErr(401, "no"), wantClass: FailureEnvironment},
		{name: "forbidden", err: apiErr(403, "no"), wantClass: FailureEnvironment},
		{name: "not found", err: apiErr(404, "gone"), wantClass: FailureEnvironment},
		{name: "rate limited", err: apiErr(429, "slow down"), wantClass: FailureEnvironment},
		{name: "server error", err: apiErr(503, "unavailable"), wantClass: FailureEnvironment},
		{
			name:      "transport failure has no status",
			err:       apiErr(0, ""),
			wantClass: FailureEnvironment,
		},
		{
			// The important one: a 400 the tool cannot explain means it
			// built a payload Cloud would not accept.
			name:           "unrecognised 400 is a bug",
			err:            apiErr(400, "Value of parameter 'foo' must be one of: [a, b]"),
			wantClass:      FailureBug,
			wantReportable: true,
		},
		{
			name:           "non-HTTP error is a bug",
			err:            errors.New("json: cannot unmarshal string into int"),
			wantClass:      FailureBug,
			wantReportable: true,
		},
		{name: "nil is not a failure", err: nil, wantClass: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ClassifyFailure(c.err)
			if v.Class != c.wantClass {
				t.Errorf("class = %q, want %q", v.Class, c.wantClass)
			}
			if v.Reportable != c.wantReportable {
				t.Errorf("reportable = %v, want %v", v.Reportable, c.wantReportable)
			}
			if c.wantClass != "" {
				if v.Why == "" {
					t.Error("every classified failure must explain why")
				}
				if v.Remediation == "" {
					t.Error("every classified failure must say what to do (or that nothing is needed)")
				}
			}
		})
	}
}

// The raw reader produces common.HTTPError rather than sqapi.APIError;
// both must classify identically or half the tool loses the explanation.
func TestClassifyFailureHandlesRawClientErrors(t *testing.T) {
	err := &common.HTTPError{StatusCode: 403, Method: "GET", URL: "/api/x",
		Body: `{"errors":[{"msg":"Insufficient privileges"}]}`}
	if got := ClassifyFailure(err).Class; got != FailureEnvironment {
		t.Errorf("common.HTTPError 403 classified as %q, want %q", got, FailureEnvironment)
	}
}

// A non-zero failure count means nothing on its own. The summary must say
// whether those failures were expected, and escalate only when they were
// not.
func TestTaskCounterSummaryBreakdownAndSeverity(t *testing.T) {
	t.Run("all by design stays a warning", func(t *testing.T) {
		c := NewTaskCounter("setProjectSettings")
		c.Success()
		for i := 0; i < 5; i++ {
			c.FailAPI(apiErr(400, "Setting 'x' cannot be set on a Project"))
		}
		logs := captureSummary(t, c)
		assertContains(t, logs, `level=WARN`, `failed=5`, `failed_by_design=5`, `failed_bugs=0`)
		assertNotContains(t, logs, `level=ERROR`)
	})

	t.Run("a single bug escalates to error", func(t *testing.T) {
		c := NewTaskCounter("createProjects")
		for i := 0; i < 20; i++ {
			c.Success()
		}
		c.FailAPI(apiErr(400, "Value of parameter 'foo' must be one of: [a, b]"))
		logs := captureSummary(t, c)
		assertContains(t, logs, `level=ERROR`, `failed_bugs=1`)
	})

	t.Run("nothing succeeded is an error whatever the cause", func(t *testing.T) {
		c := NewTaskCounter("createProjects")
		c.FailAPI(apiErr(400, "The organization 'x' is not allowed to use private projects."))
		logs := captureSummary(t, c)
		assertContains(t, logs, `level=ERROR`, `failed_environment=1`)
	})

	t.Run("a clean task stays informational and carries no breakdown", func(t *testing.T) {
		c := NewTaskCounter("createGates")
		c.Success()
		logs := captureSummary(t, c)
		assertContains(t, logs, `level=INFO`, `failed=0`)
		assertNotContains(t, logs, `failed_by_design`)
	})
}

func captureSummary(t *testing.T, c *TaskCounter) string {
	t.Helper()
	logger, buf, _ := newEventLogger(t)
	c.LogSummary(logger, 0)
	return buf.String()
}

func assertContains(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("expected %q in summary, got:\n%s", n, haystack)
		}
	}
}

func assertNotContains(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("did not expect %q in summary, got:\n%s", n, haystack)
		}
	}
}
