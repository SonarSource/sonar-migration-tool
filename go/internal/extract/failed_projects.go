// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// skipFailedProject decides what a failed per-project request does to
// the run. When err is a server or network failure for one project (see
// isProjectFailure), the project is skipped for the rest of the run and
// left out of the migration, and the run carries on: on a large instance
// one slow or failing project used to stop an extract that had been
// running for hours. Anything else is returned and stops the run as
// before: a request that is wrong for every project, an item that is not
// a project's (a rule, a user, a quality profile), cancellation. So does
// a run of maxConsecutiveProjectFailures failures with no success between
// them, which means the server is down rather than one project failing.
func skipFailedProject(ctx context.Context, e *Executor, taskName, projectKey string, err error) error {
	if projectKey == "" || !isProjectFailure(ctx, err) {
		return err
	}
	streak, isNew := e.recordProjectFailure(projectKey, taskName, err)
	if !isNew {
		return nil // already skipped, by a request in another task
	}
	if streak >= maxConsecutiveProjectFailures {
		if cerr := e.endOutage(); cerr != nil {
			e.Logger.Error("taking back the tasks that finished during the outage", "err", cerr)
		}
		return fmt.Errorf("%s: %d projects in a row failed, so the server looks unavailable; stopping. "+
			"Those projects are not recorded as failed: resume with --extract_id once the server answers: %w",
			taskName, streak, err)
	}
	e.Logger.Warn(taskName+": request failed after every retry; skipping the project and leaving it out of the migration",
		"project", projectKey, "err", err)
	return nil
}

// isProjectFailure reports whether err is a server or network failure the
// SDK's retries and the page-size fallback could not get past for one
// project: an HTTP 500, 502, 503 or 504, a timeout, or a connection the
// server or something in front of it cut (reset, or closed mid-response,
// which Go reports as EOF). Any other network error, such as a refused
// connection, a DNS failure or a bad certificate, would fail for every
// project, and stops the run; so does a cancelled run.
func isProjectFailure(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if common.IsHTTPError(err,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// PrintFailedProjectsBlock writes the end-of-run notice for the projects
// extract skipped after a failed request. It prints nothing when there
// are none.
func PrintFailedProjectsBlock(w io.Writer, list []common.FailedProject) {
	if len(list) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%d project(s) were skipped because a request for them failed after every retry.\n", len(list))
	fmt.Fprintf(w, "They are left out of the migration (listed in %s):\n", common.FailedProjectsFileName)
	for _, fp := range list {
		fmt.Fprintf(w, "  - %s (%s): %s\n", fp.Key, fp.Task, fp.Error)
	}
	fmt.Fprintln(w, "Migrate them in a separate run once the server answers: extract and migrate again with --project_key_regexp matching them.")
}
