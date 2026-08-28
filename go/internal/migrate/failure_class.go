// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	sqapi "github.com/sonar-solutions/sq-api-go"
)

// FailureClass answers the only question an operator really has when a
// migration reports a failure: is this the tool being wrong, or SonarQube
// Cloud legitimately refusing something it does not support?
//
// Before this existed every failure looked alike. One customer run logged
// 42,181 warnings and zero errors; 42,048 of them were a single expected
// platform limitation and the rest were buried. Classifying each failure
// lets the log, the task summary and the report separate "nothing you can
// do" from "please report this".
type FailureClass string

const (
	// FailureByDesign means SonarQube Cloud cannot do what was asked and
	// never will: the setting has no project scope, the metric has no Cloud
	// equivalent, the new-code type is unsupported. The migration is
	// working correctly; the value is expected to be dropped.
	FailureByDesign FailureClass = "by-design"

	// FailureAlreadyDone means the desired end state already holds — the
	// entity exists, or the thing being deleted is already gone. Not a
	// failure in any meaningful sense.
	FailureAlreadyDone FailureClass = "already-done"

	// FailureEnvironment means something outside the tool blocked the
	// operation: token permissions, org subscription or quota, rate
	// limiting, connectivity, a Cloud-side 5xx. Re-running or fixing the
	// environment may succeed.
	FailureEnvironment FailureClass = "environment"

	// FailureBug means the request was rejected for a reason the tool does
	// not recognise. The payload it sent is most likely wrong. These are
	// the ones worth a bug report.
	FailureBug FailureClass = "bug"
)

// FailureVerdict is the full explanation attached to a failed operation.
type FailureVerdict struct {
	Class FailureClass
	// Why explains the cause in operator language, not API language.
	Why string
	// Remediation is what to do about it, or why nothing need be done.
	Remediation string
	// Reportable marks failures that indicate a defect in this tool and
	// should be raised with the maintainers.
	Reportable bool
}

// httpFailure normalizes the two error shapes the codebase produces —
// sqapi.APIError from the typed clients and common.HTTPError from the raw
// reader — so classification does not have to care which one it got.
type httpFailure struct {
	status  int
	message string
	ok      bool
}

func asHTTPFailure(err error) httpFailure {
	var apiErr *sqapi.APIError
	if errors.As(err, &apiErr) {
		return httpFailure{status: apiErr.StatusCode, message: apiErr.Message(), ok: true}
	}
	var httpErr *common.HTTPError
	if errors.As(err, &httpErr) {
		return httpFailure{status: httpErr.StatusCode, message: httpErr.Message(), ok: true}
	}
	return httpFailure{}
}

// environmentMessageHints maps substrings of SonarQube Cloud 400 messages
// that describe an account or configuration state rather than a bad
// request. Without these they would fall through to FailureBug, which
// would send operators chasing a defect that is really a subscription or
// permission problem.
var environmentMessageHints = []struct {
	Substring   string
	Why         string
	Remediation string
}{
	{
		Substring:   "not allowed to use private projects",
		Why:         "the target organization cannot create private projects, and the migration always creates them private",
		Remediation: "enable private projects on the organization (a paid plan or an available private-project allowance), then re-run",
	},
	{
		Substring:   "not bound to an alm application",
		Why:         "the target organization is not bound to a DevOps platform",
		Remediation: "bind the organization to its DevOps platform, then re-run with --target_task matchProjectRepos",
	},
	{
		Substring:   "maximum number of",
		Why:         "an organization or plan limit was reached",
		Remediation: "raise the limit on the target organization or reduce the migration scope, then re-run",
	},
	{
		Substring:   "no organization for key",
		Why:         "the target organization key does not exist or is not visible to this token",
		Remediation: "check organizations.csv and that the token can administer the organization",
	},
}

// ClassifyFailure explains a failed operation.
//
// Ordering matters: the specific, recognised platform rejections are
// matched first, then account/permission state, then transport, and only
// what is left over is called a bug. An unrecognised 400 is deliberately
// classified as a bug rather than shrugged off — the tool built a payload
// SonarQube Cloud would not accept, and nothing else will notice.
func ClassifyFailure(err error) FailureVerdict {
	if err == nil {
		return FailureVerdict{}
	}

	switch {
	case sqapi.IsAlreadyExists(err), sqapi.IsMangledAlreadyExists(err):
		return FailureVerdict{
			Class:       FailureAlreadyDone,
			Why:         "the entity already exists on the target, so the state the migration wanted is already in place",
			Remediation: "none needed; expected when re-running a migration or migrating into a populated organization",
		}

	case sqapi.IsProjectLevelRejection(err):
		return FailureVerdict{
			Class:       FailureByDesign,
			Why:         "SonarQube Cloud's definition for this setting does not include the project qualifier, so it cannot be set on a project",
			Remediation: "none available; the setting is instance-scope-only on SonarQube Server and has no project-scope counterpart on Cloud. It is expected to be dropped",
		}

	case sqapi.IsOrgLevelRejection(err):
		return FailureVerdict{
			Class:       FailureByDesign,
			Why:         "SonarQube Cloud lists this setting at organization scope but refuses to write it there; it only takes effect per project",
			Remediation: "none needed; the migration falls back to setting the value on each project in the organization",
		}
	}

	f := asHTTPFailure(err)
	if !f.ok {
		// Not an HTTP failure at all: a context error, a JSON decode
		// failure, a nil dereference surfaced as an error. None of these
		// are the platform's fault.
		return FailureVerdict{
			Class:       FailureBug,
			Why:         "the operation failed before or outside an HTTP response, so this is a fault in the migration tool rather than a SonarQube Cloud limitation",
			Remediation: "re-run with --debug and report the error with the surrounding log lines",
			Reportable:  true,
		}
	}

	lower := strings.ToLower(f.message)
	for _, hint := range environmentMessageHints {
		if strings.Contains(lower, hint.Substring) {
			return FailureVerdict{Class: FailureEnvironment, Why: hint.Why, Remediation: hint.Remediation}
		}
	}

	switch {
	case f.status == 0:
		return FailureVerdict{
			Class:       FailureEnvironment,
			Why:         "no HTTP response was received — the connection failed at the transport level",
			Remediation: "check network reachability, proxy settings and TLS interception between the runner and SonarQube Cloud; retries already covered transient blips",
		}

	case f.status == http.StatusUnauthorized:
		return FailureVerdict{
			Class:       FailureEnvironment,
			Why:         "SonarQube Cloud rejected the credentials",
			Remediation: "check the target token is valid and not expired",
		}

	case f.status == http.StatusForbidden:
		return FailureVerdict{
			Class:       FailureEnvironment,
			Why:         "the token is valid but lacks permission for this operation",
			Remediation: "grant the migration user Administer on the target organization (and Execute Analysis where project data is imported)",
		}

	case f.status == http.StatusNotFound:
		return FailureVerdict{
			Class:       FailureEnvironment,
			Why:         "the target entity does not exist",
			Remediation: "expected for delete-style cleanup and shortly after project creation while Cloud indexes; otherwise check the entity was created earlier in the run",
		}

	case f.status == http.StatusTooManyRequests:
		return FailureVerdict{
			Class:       FailureEnvironment,
			Why:         "SonarQube Cloud rate-limited the request and the retry budget was exhausted",
			Remediation: "lower --concurrency and re-run; the run is resumable with --run_id",
		}

	case f.status >= 500:
		return FailureVerdict{
			Class:       FailureEnvironment,
			Why:         "SonarQube Cloud returned a server-side error",
			Remediation: "re-run with --run_id to resume; if it persists for the same operation, raise it with SonarQube Cloud support",
		}

	case f.status == http.StatusBadRequest:
		// Everything the tool understands about 400s has been matched
		// above. A 400 it cannot explain means the payload was wrong.
		return FailureVerdict{
			Class:       FailureBug,
			Why:         "SonarQube Cloud rejected the request and the migration tool does not recognise the reason, which means the payload it built is probably invalid",
			Remediation: "report this with the endpoint, the message above and the run id; the request body is recorded in requests.log",
			Reportable:  true,
		}
	}

	return FailureVerdict{
		Class:       FailureBug,
		Why:         "the operation failed with a status the migration tool does not classify",
		Remediation: "report this with the endpoint, the message above and the run id",
		Reportable:  true,
	}
}
