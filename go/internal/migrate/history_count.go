// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"time"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// historyExtractTask is the extract task whose records hold the bounded
// historical analysis points (see internal/extract's getProjectAnalysisHistory).
const historyExtractTask = "getProjectAnalysisHistory"

// historyPointDate parses the "date" of one extracted history point and
// reports whether the point is replayable at all. A record with a missing or
// unparseable date has no place on the target's analysis timeline, so it is
// dropped.
//
// It is the single definition of that rule, shared by
// loadExtractedAnalysisHistory (which replays the points) and
// countReplayableHistoryPoints (which pre-counts them for the ETA), so the
// count can never include a point the replay would silently drop.
func historyPointDate(raw string) (time.Time, bool) {
	date := parseISODate(raw)
	return date, !date.IsZero()
}

// countReplayableHistoryPoints streams getProjectAnalysisHistory ONCE and
// returns how many replayable points each (server, project, branch) has: for
// every scope, counts[scope] is len(loadExtractedAnalysisHistory(e,
// scope.ServerURL, scope.ProjectKey, scope.Branch)), reached by the same two
// decodes per record — the recordHeader routing scopedExtractItems does, then
// extractField for the date — without that loader's full pass over the corpus
// on every call. That is the point of it: a caller that needs the count for
// many branches (projectHistoryPointTotal, since #625) pays for one pass
// instead of one per branch. Records that fail to decode are skipped, as
// scopedExtractItems skips them.
//
// The one difference is the scope: it is an exact key here, so an empty Branch
// is not the "every branch" wildcard it is for the loader.
// projectHistoryPointTotal never asks for one, because collectBranchInfo drops
// nameless branches.
//
// The date is read with extractField, which decodes the whole record into a
// map and so copies the measures array, rather than from a header struct
// declared next to recordHeader's fields. The struct would be cheaper per
// record, but it is not the same read: encoding/json matches struct keys
// case-insensitively, lets a null leave an earlier value alone and fails the
// whole record on a non-string value, where extractField takes the one
// exact-case key, last occurrence winning, whatever it holds. On a hand-edited
// extract the struct would count different records than the replay reads. The
// copy costs little where it matters: extract writes one file per point, so a
// pass is dominated by opening them, not by decoding what is in them.
func countReplayableHistoryPoints(e *Executor) map[extractScope]int {
	counts := make(map[extractScope]int)
	for item := range structure.ExtractItems(e.ExportDir, e.Mapping, historyExtractTask) {
		var hdr recordHeader
		if err := json.Unmarshal(item.Data, &hdr); err != nil {
			continue
		}
		if _, ok := historyPointDate(extractField(item.Data, "date")); !ok {
			continue
		}
		counts[extractScope{ServerURL: item.ServerURL, ProjectKey: hdr.ProjectKey, Branch: hdr.Branch}]++
	}
	return counts
}
