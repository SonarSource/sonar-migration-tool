// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/scanreport"
	pb "github.com/sonar-solutions/sonar-migration-tool/internal/scanreport/proto"
)

// resolveMigrateHistory mirrors resolveFastSync for the migrate_history
// tri-state (#554): the target block's value wins when explicitly set,
// else the top-level value, else the default (false — no history
// migration, the pre-#554 behavior).
func resolveMigrateHistory(target, top *FlexibleBool) bool {
	if target != nil && target.Set {
		return target.Value
	}
	if top != nil && top.Set {
		return top.Value
	}
	return false
}

// historySnapshot is one extracted historical analysis point for a
// project+branch: its date, the project version recorded at that analysis,
// and the project-level measures as of that point (#554).
type historySnapshot struct {
	Date           time.Time
	ProjectVersion string
	Measures       []scanreport.MeasureInput
}

// loadExtractedAnalysisHistory reads the getProjectAnalysisHistory extract
// records for one project+branch (written only when extract ran with
// --migrate_history), sorted oldest to newest. Returns nil when no history
// was extracted for this project+branch — including every run where
// --migrate_history wasn't passed to extract, which is what keeps this
// feature a true no-op for everyone who doesn't opt in.
func loadExtractedAnalysisHistory(e *Executor, serverURL, serverKey, branch string) []historySnapshot {
	scope := extractScope{ServerURL: serverURL, ProjectKey: serverKey, Branch: branch}
	var out []historySnapshot
	for item := range scopedExtractItems(e, "getProjectAnalysisHistory", scope) {
		date := parseISODate(extractField(item.Data, "date"))
		if date.IsZero() {
			continue
		}
		out = append(out, historySnapshot{
			Date:           date,
			ProjectVersion: extractField(item.Data, "projectVersion"),
			Measures:       extractHistoryMeasures(item.Data, serverKey),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// extractHistoryMeasures parses the "measures":[{"metric":..,"value":..}]
// array a getProjectAnalysisHistory record carries into MeasureInputs
// attributed to the project's root component (there are no file components
// in a historical snapshot report — see migrateBranchHistory).
func extractHistoryMeasures(data json.RawMessage, cloudProjectKey string) []scanreport.MeasureInput {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	raw, ok := obj["measures"]
	if !ok {
		return nil
	}
	var arr []struct {
		Metric string `json:"metric"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]scanreport.MeasureInput, 0, len(arr))
	for _, m := range arr {
		if m.Metric == "" || m.Value == "" {
			continue
		}
		out = append(out, scanreport.MeasureInput{Component: cloudProjectKey, MetricKey: m.Metric, Value: m.Value})
	}
	return out
}

// migrateBranchHistory replays a project's extracted historical analysis
// snapshots as separate, backdated analyses on the target, oldest to
// newest (#554, PoC).
//
// Main branch only, by design: the create-analysis handshake (see
// preCreateBranchAnalysis) exists specifically to anchor a NON-main branch
// on the target before its first report is accepted, and generalizing that
// per historical point for non-main branches is real additional complexity
// this PoC deliberately doesn't take on (see the PR description's "known
// limitations"). The main branch needs no handshake — same reasoning the
// regular current-snapshot import already relies on — so replaying its
// history is just N extra plain submissions before the regular one.
//
// Must run BEFORE the regular current-snapshot import for this branch: the
// Compute Engine requires each new analysis to be dated after the branch's
// most recent one, and the regular import always stamps its own submission
// with "now" — later than any historical point. Best-effort: a failure here
// is logged and does NOT fail or block the regular import that follows it,
// so --migrate_history can never turn a transfer that used to succeed into
// one that fails.
func migrateBranchHistory(ctx context.Context, e *Executor, bctx branchImportContext, branch branchInfo, targetBranch string) {
	if !e.MigrateHistory || !branch.IsMain {
		return
	}
	snapshots := loadExtractedAnalysisHistory(e, bctx.ServerURL, bctx.ServerKey, branch.Name)
	if len(snapshots) == 0 {
		return
	}

	// The placeholder file the snapshot report carries has to name a language
	// whose quality profile actually exists in THIS target organization: the
	// CE validates every qprofile key in the metadata against the org and
	// rejects the whole report otherwise ("Quality profiles with following
	// keys don't exist in organization [...]"). Resolve it per run instead of
	// assuming any particular language or profile key is present.
	placeholder, ok := resolveHistoryPlaceholderProfile(ctx, e, bctx.OrgKey)
	if !ok {
		e.Logger.Warn("project history migration skipped: no usable quality profile in the target organization",
			"project", bctx.CloudKey, "branch", targetBranch, "org", bctx.OrgKey)
		return
	}

	e.Logger.Info("migrating project history (PoC, #554)",
		"project", bctx.CloudKey, "branch", targetBranch, "points", len(snapshots),
		"placeholder_language", placeholder.Language)

	for i, snap := range snapshots {
		if err := submitHistoricalSnapshot(ctx, e, bctx, targetBranch, snap, placeholder); err != nil {
			// Stop rather than skip-and-continue: submitting a later
			// historical date after a skipped earlier one would still be
			// chronologically valid, but a submission failure here is far
			// more likely a CE rejection than a transient blip (the earlier
			// points, if any, just succeeded against the same endpoint), so
			// every remaining point on this branch would likely fail the
			// same way. Give up early rather than hammering the CE.
			e.Logger.Warn("project history migration stopped early for this branch",
				"project", bctx.CloudKey, "branch", targetBranch,
				"point", i+1, "of", len(snapshots), "date", snap.Date.Format(time.RFC3339), "err", err)
			return
		}
	}
}

// historyPlaceholderLangs are the languages the placeholder file may be
// stamped with, most-preferred first, each paired with the file extension it
// must carry.
//
// The extension is not decoration and is not derivable from the language key:
// SonarQube Cloud assigns a file's language from its extension, and a
// mismatch between that and the declared quality profile is exactly the #474
// "file with language X but no matching quality profile" rejection. Several
// language keys are not their own extension (apex→.cls, cobol→.cbl,
// web→.html), so the pairing is explicit and a language may only be used as
// a placeholder if it appears here.
//
// All six are ordinary, universally shipped languages with a stock "Sonar
// way" profile in every organization, which keeps the placeholder as
// unremarkable as possible. The file is never meant to be read.
var historyPlaceholderLangs = []struct{ Lang, Ext string }{
	{"js", "js"}, {"py", "py"}, {"java", "java"},
	{"ts", "ts"}, {"go", "go"}, {"xml", "xml"},
}

// historyPlaceholder is the resolved, target-org-specific identity of the
// throwaway file component a historical snapshot report is built around.
type historyPlaceholder struct {
	Language string
	Ext      string
	QProfile scanreport.QProfileInfo
}

// resolveHistoryPlaceholderProfile picks a language for the placeholder file
// whose quality profile really exists in orgKey, and returns that profile.
//
// The key must be the TARGET organization's profile key, not the source
// server's: profile keys are instance-scoped, and submitting a source key
// makes the CE reject the report outright. buildSCProfileMap is the same
// lookup the regular current-snapshot import already uses for this.
func resolveHistoryPlaceholderProfile(ctx context.Context, e *Executor, orgKey string) (historyPlaceholder, bool) {
	return pickHistoryPlaceholder(buildSCProfileMap(ctx, e, orgKey))
}

// pickHistoryPlaceholder is the pure selection half of
// resolveHistoryPlaceholderProfile, split out so the choice can be tested
// without a live organization.
func pickHistoryPlaceholder(byLang map[string]scanreport.QProfileInfo) (historyPlaceholder, bool) {
	for _, c := range historyPlaceholderLangs {
		if p, ok := byLang[c.Lang]; ok {
			return historyPlaceholder{Language: c.Lang, Ext: c.Ext, QProfile: p}, true
		}
	}
	// The organization has profiles, but none for a language we can safely
	// name a placeholder file after. Refusing here is deliberate: guessing an
	// extension from an unknown language key is what produces the #474
	// whole-report rejection, and skipping history is far better than
	// submitting reports the Compute Engine will refuse.
	return historyPlaceholder{}, false
}

// retargetMeasures returns a copy of measures with every entry's Component
// rewritten to newComponent. loadExtractedAnalysisHistory attributes
// measures to the project's own cloud key (there is no file component at
// extraction time to attribute them to instead); submitHistoricalSnapshot
// retargets them onto its placeholder file component just before building
// the report.
func retargetMeasures(measures []scanreport.MeasureInput, newComponent string) []scanreport.MeasureInput {
	out := make([]scanreport.MeasureInput, len(measures))
	for i, m := range measures {
		m.Component = newComponent
		out[i] = m
	}
	return out
}

// submitHistoricalSnapshot builds and submits one minimal, backdated scanner
// report for a single historical point: just the project root component and
// its measures — no files, no issues, no quality profiles (there are no
// files to validate a language/profile against). Per the issue's own PoC
// design, files/issues aren't needed for history points; only the measures
// need to show up in the target's analysis history.
func submitHistoricalSnapshot(ctx context.Context, e *Executor, bctx branchImportContext, targetBranch string, snap historySnapshot, placeholder historyPlaceholder) error {
	// A lone PROJECT component with a raw measure attached directly to it is
	// not a shape the real scanner ever produces — measures normally live on
	// FILE components and the CE aggregates the project total from them — and
	// submitting one that way was rejected by the CE ("issue whilst
	// processing the report", live-verified). One placeholder FILE component
	// gives the measures somewhere valid to live; per the issue's own PoC
	// design ("fake ones can be created just to let SQC/SonarCloud accept
	// the report"), its content is never meant to be seen.
	placeholderName := "__history_snapshot__." + placeholder.Ext
	placeholderKey := bctx.CloudKey + ":" + placeholderName
	root, fileComps, cr := scanreport.BuildComponents(bctx.CloudKey, []scanreport.ComponentInput{
		{Key: placeholderKey, Name: placeholderName, Path: placeholderName, Language: placeholder.Language, Lines: 1},
	})
	if len(fileComps) == 0 {
		return fmt.Errorf("building historical report: placeholder component was not created")
	}
	fileRef := fileComps[0].Ref

	reportData := &scanreport.ReportData{
		Metadata: scanreport.BuildMetadata(scanreport.MetadataInput{
			AnalysisDate: snap.Date,
			OrgKey:       bctx.OrgKey,
			ProjectKey:   bctx.CloudKey,
			BranchName:   targetBranch,
			BranchType:   pb.Metadata_BRANCH,
			QProfiles:    []scanreport.QProfileInfo{placeholder.QProfile},
			// Keyed by LANGUAGE despite the name — countFilesByExt, which the
			// regular import feeds this field from, counts c.Language.
			FileCountByExt: map[string]int32{placeholder.Language: 1},
			ProjectVersion: snap.ProjectVersion,
		}, root.Ref),
		RootComponent:  root,
		FileComponents: fileComps,
		Measures:       scanreport.BuildMeasures(retargetMeasures(snap.Measures, placeholderKey), cr),
		Sources:        map[int32]string{fileRef: ""},
		Changesets: map[int32]*pb.Changesets{
			fileRef: scanreport.BuildDefaultChangesets(fileRef, 1, snap.Date),
		},
		// No active rules: the report carries no issues, so nothing needs a
		// rule activated. Naming one would only add another identifier that
		// has to exist in the target organization.
	}

	zipBytes, err := scanreport.PackageReport(reportData)
	if err != nil {
		return fmt.Errorf("packaging historical report: %w", err)
	}

	cfg := scanreport.SubmitConfig{
		CloudURL:       e.CloudURL,
		ProjectKey:     bctx.CloudKey,
		OrgKey:         bctx.OrgKey,
		BranchName:     targetBranch,
		ProjectVersion: snap.ProjectVersion,
		IsMain:         true,
	}
	result, err := scanreport.SubmitReport(ctx, e.Raw.HTTPClient(), cfg, zipBytes)
	if err != nil {
		return fmt.Errorf("submitting historical report: %w", err)
	}
	if err := scanreport.PollCETask(ctx, e.Raw.HTTPClient(), e.CloudURL, result.TaskID, e.Logger); err != nil {
		return fmt.Errorf("CE task failed: %w", err)
	}

	e.Logger.Info("historical analysis migrated",
		"project", bctx.CloudKey, "branch", targetBranch,
		"date", snap.Date.Format(time.RFC3339), "taskId", result.TaskID)
	return nil
}
