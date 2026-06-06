// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
)

// SonarCloud indexes a freshly-created project asynchronously, and the
// settings and search endpoints become consistent on independent
// timelines (#334). A POST /api/projects/create returns 200 well
// before GET /api/settings/list_definitions / POST /api/settings/set
// acknowledge the project. Empirically the gap routinely exceeds 30
// seconds for a subset of projects, so a pre-task wait at the create
// site cannot reliably predict downstream readiness — different
// endpoints have different indexing pipelines.
//
// The pragmatic fix is to retry the actual write/read call when it
// fails with a 404 "Project doesn't exist". Each call pays the wait
// only when it is actually too early, in parallel with concurrent
// goroutines.
//
// Vars (not consts) so test code can set them to near-zero values
// without coupling production timing to test speed.
var (
	projectIndexingRetryInitialDelay = 500 * time.Millisecond
	projectIndexingRetryMaxDelay     = 5 * time.Second
	projectIndexingRetryMaxAttempts  = 8
)

// retryOnProjectNotFound runs op and, when it fails with a SonarCloud
// 404 "Project doesn't exist" — the known post-create indexing-lag
// signature (#334) — retries with exponential backoff up to
// projectIndexingRetryMaxAttempts attempts. Non-404 errors and
// successes return immediately. Returns the last error if all retries
// are exhausted.
//
// Each time a retry is scheduled, logger.Debug emits a line with the
// attempt count, the delay before the next try, and the caller-supplied
// `attrs` (typically the project key, org, and setting key). A nil
// logger silences the log; the retry behavior is unchanged. The Debug
// level keeps the line silent at normal verbosity yet visible under
// `--debug` for the operator who wants to see the indexing-lag
// recovery in flight.
func retryOnProjectNotFound(ctx context.Context, logger *slog.Logger, op func() error, attrs ...any) error {
	delay := projectIndexingRetryInitialDelay
	var err error
	for attempt := 0; attempt < projectIndexingRetryMaxAttempts; attempt++ {
		err = op()
		if err == nil || !isProjectNotFound(err) {
			return err
		}
		if logger != nil {
			fields := []any{
				"attempt", attempt + 1,
				"max_attempts", projectIndexingRetryMaxAttempts,
				"retry_in", delay.String(),
			}
			fields = append(fields, attrs...)
			logger.Debug("SQC project not yet indexed, waiting before retry", fields...)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		if delay > projectIndexingRetryMaxDelay {
			delay = projectIndexingRetryMaxDelay
		}
	}
	return err
}

// isProjectNotFound returns true for the specific SonarCloud signature
// that indicates "the project exists in the database but the index for
// this endpoint hasn't caught up yet" — HTTP 404 with a message
// mentioning "project" and "doesn't exist". This is narrower than
// sqapi.IsNotFound (which is any 404) so we don't accidentally retry
// genuine missing-component errors that are not indexing-related.
func isProjectNotFound(err error) bool {
	var apiErr *sqapi.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(apiErr.Message())
	return strings.Contains(msg, "project") && strings.Contains(msg, "doesn't exist")
}
