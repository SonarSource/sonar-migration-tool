// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// applyPortfolioFailures is a fourth builder of Failed items and set no
// Cause at all, so which side of the split its rows landed on was an
// accident of the zero value rather than anything the run observed.
//
// The two sources it merges do not deserve the same treatment: a
// requests.log failure carries SonarQube Cloud's own status and message
// and is classified like every other one, while the sidecar's reason is
// prose this tool wrote and is no evidence that the migration is content.
func TestPortfolioFailuresCarryACause(t *testing.T) {
	dir := t.TempDir()
	writeTaskJSONL(t, dir, "createPortfolios", []map[string]any{
		{"name": "Already There", "cloud_portfolio_id": "pf-exists"},
		{"name": "Unexplained", "cloud_portfolio_id": "pf-unknown"},
	})

	succeeded := []EntityItem{{Name: "Already There"}, {Name: "Unexplained"}}
	failures := map[string]portfolioFailure{
		"pf-exists": {
			CloudPortfolioID: "pf-exists", HTTPStatus: 400,
			Error: "Portfolio with key 'pf' already exists",
		},
		// No status: this one came from the configurePortfolios sidecar.
		"pf-unknown": {
			CloudPortfolioID: "pf-unknown",
			Error:            "no resolved projects on source",
		},
	}

	_, failed, _ := applyPortfolioFailures(common.NewDataStore(dir), succeeded, nil, nil, failures)
	if len(failed) != 2 {
		t.Fatalf("got %d failed rows, want 2: %+v", len(failed), failed)
	}

	byName := make(map[string]EntityItem, len(failed))
	for _, f := range failed {
		byName[f.Name] = f
	}
	if got := byName["Already There"].Cause; got != string(migrate.FailureAlreadyDone) {
		t.Errorf("HTTP rejection Cause = %q, want %q", got, migrate.FailureAlreadyDone)
	}
	if got := byName["Unexplained"].Cause; got != "" {
		t.Errorf("sidecar reason Cause = %q, want it left unclassified (and so actionable)", got)
	}

	actionable, expected := Section{Failed: failed}.SplitFailed()
	if len(actionable) != 1 || actionable[0].Name != "Unexplained" {
		t.Errorf("actionable = %+v, want just the unexplained portfolio", actionable)
	}
	if len(expected) != 1 || expected[0].Name != "Already There" {
		t.Errorf("no-action-needed = %+v, want just the already-existing portfolio", expected)
	}
}
