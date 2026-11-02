// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
)

// CoveredRun is one migrate run folded into a cumulative report (#667).
type CoveredRun struct {
	RunID         string
	StartedAt     string
	CompletedAt   string
	OverallStatus string
}

// CollectCumulativeSummary builds the final report of runDir cumulated over
// every earlier migrate run of the same export directory (#667): a large
// instance migrated in slices (--project_key_regexp, portfolio or template
// regexps) gets one report covering every slice so far, not only the last.
//
// The runs covered are the migrate runs (a run_meta.json) newer than the
// latest reset run (a clear.json), up to runDir itself: a reset wiped the
// target, so what was migrated before it no longer exists. Each run is
// collected on its own, exactly as a single-run report, and the entity
// sections are then merged with the newest run winning per entity, see
// mergeSections. The runtime sections (timings, failure ledger, warnings,
// rate limiting) stay those of runDir; Runs lists every run covered.
//
// With no earlier run to fold in, the result is CollectSummary's.
func CollectCumulativeSummary(runDir, exportDir string) (*MigrationSummary, error) {
	if exportDir == "" {
		exportDir = filepath.Dir(runDir)
	}
	latest, err := CollectSummary(runDir, exportDir)
	if err != nil {
		return nil, err
	}
	earlier := earlierRunDirs(runDir, exportDir)
	if len(earlier) == 0 {
		return latest, nil
	}
	summaries := []*MigrationSummary{latest}
	for _, dir := range earlier {
		s, err := CollectSummary(dir, exportDir)
		if err != nil {
			slog.Warn("leaving a migrate run out of the cumulative report: it could not be read",
				"run_id", filepath.Base(dir), "error", err)
			continue
		}
		summaries = append(summaries, s)
	}
	latest.Sections = mergeSections(summaries)
	latest.Limitations = mergeLimitations(summaries)
	for i := len(summaries) - 1; i >= 0; i-- {
		latest.Runs = append(latest.Runs, coveredRun(summaries[i]))
	}
	return latest, nil
}

func coveredRun(s *MigrationSummary) CoveredRun {
	run := CoveredRun{RunID: s.RunID, OverallStatus: s.OverallStatus}
	if !s.StartedAt.IsZero() {
		run.StartedAt = s.StartedAt.Format(dateTimeLayout)
	}
	if !s.CompletedAt.IsZero() {
		run.CompletedAt = s.CompletedAt.Format(dateTimeLayout)
	}
	return run
}

// earlierRunDirs returns the migrate runs of exportDir older than runDir and
// newer than the latest reset run older than runDir, newest first.
func earlierRunDirs(runDir, exportDir string) []string {
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return nil
	}
	current := filepath.Base(runDir)
	lastReset := ""
	var runs []string
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || id == current || !common.RunIDAfter(current, id) {
			continue
		}
		dir := filepath.Join(exportDir, id)
		if fileExists(filepath.Join(dir, "clear.json")) {
			if lastReset == "" || common.RunIDAfter(id, lastReset) {
				lastReset = id
			}
			continue
		}
		if fileExists(filepath.Join(dir, "run_meta.json")) {
			runs = append(runs, id)
		}
	}
	var out []string
	for _, id := range runs {
		if lastReset == "" || common.RunIDAfter(id, lastReset) {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return common.RunIDAfter(out[i], out[j]) })
	for i, id := range out {
		out[i] = filepath.Join(exportDir, id)
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Bucket indexes of a Section, in the order sectionBuckets returns them.
const (
	bucketSucceeded = iota
	bucketNearPerfect
	bucketPartial
	bucketFailed
	bucketSkipped
	bucketCount
)

func sectionBuckets(s *Section) [bucketCount]*[]EntityItem {
	return [bucketCount]*[]EntityItem{&s.Succeeded, &s.NearPerfect, &s.Partial, &s.Failed, &s.Skipped}
}

// mergedEntity is the claim the newest run reporting an entity holds on it.
type mergedEntity struct {
	run int
	// weak is true while every item of the claiming run is an expected
	// failure (an entity that already exists): a slice re-creating the
	// global objects an earlier slice created must not hide that success.
	weak bool
}

// mergeSections merges the sections of summaries, newest first. An entity
// reported by a newer run replaces every record an older run has of it, so
// a project re-migrated by a later slice shows only its latest outcome. The
// one exception is an expected failure ("already exists"): an older run's
// record of the entity in any other bucket replaces it instead. Within one
// run, items are kept as that run reported them.
func mergeSections(summaries []*MigrationSummary) []Section {
	merged := make([]Section, len(summaries[0].Sections))
	for i, s := range summaries[0].Sections {
		merged[i] = Section{Name: s.Name}
	}
	for i := range merged {
		claims := map[string]*mergedEntity{}
		for run, s := range summaries {
			if sec := sectionByName(s.Sections, merged[i].Name); sec != nil {
				mergeSectionRun(&merged[i], sec, run, claims)
			}
		}
	}
	return merged
}

func sectionByName(sections []Section, name string) *Section {
	for i := range sections {
		if sections[i].Name == name {
			return &sections[i]
		}
	}
	return nil
}

func mergeSectionRun(dst, src *Section, run int, claims map[string]*mergedEntity) {
	dstBuckets := sectionBuckets(dst)
	srcBuckets := sectionBuckets(src)
	for b, items := range srcBuckets {
		for _, item := range *items {
			key := entityKey(dst.Name, item)
			weak := isExpectedFailure(b, item)
			claim, ok := claims[key]
			switch {
			case !ok:
				claims[key] = &mergedEntity{run: run, weak: weak}
			case claim.run == run:
				claim.weak = claim.weak && weak
			case claim.weak && !weak:
				dropEntity(dst, key)
				claims[key] = &mergedEntity{run: run}
			default:
				continue
			}
			*dstBuckets[b] = append(*dstBuckets[b], item)
		}
	}
}

func isExpectedFailure(bucket int, item EntityItem) bool {
	return bucket == bucketFailed && !migrate.FailureClass(item.Cause).Actionable()
}

func dropEntity(s *Section, key string) {
	for _, items := range sectionBuckets(s) {
		kept := (*items)[:0]
		for _, item := range *items {
			if entityKey(s.Name, item) != key {
				kept = append(kept, item)
			}
		}
		*items = kept
	}
}

// entityKey identifies an entity across runs. A project is its source key,
// whatever organization each run mapped it to; any other entity is its name,
// language (quality profiles) and organization.
func entityKey(section string, item EntityItem) string {
	if section == "Projects" && item.SourceKey != "" {
		return "src\x00" + item.SourceKey
	}
	return item.Name + "\x00" + item.Language + "\x00" + item.Organization
}

// mergeLimitations returns every run's limitations once, newest run first.
func mergeLimitations(summaries []*MigrationSummary) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range summaries {
		for _, l := range s.Limitations {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}
