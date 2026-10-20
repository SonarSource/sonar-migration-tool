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
	streak := e.recordProjectFailure(projectKey, taskName, err)
	if streak >= maxConsecutiveProjectFailures {
		return fmt.Errorf("%s: %d projects in a row failed, so the server looks unavailable; stopping: %w",
			taskName, streak, err)
	}
	e.Logger.Warn(taskName+": request failed after every retry; skipping the project and leaving it out of the migration",
		"project", projectKey, "err", err)
	return nil
}

// isProjectFailure reports whether err is a server or network failure the
// SDK's retries and the page-size fallback could not get past, as opposed
// to an error that would fail for every project or a cancelled run.
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
	return errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
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
