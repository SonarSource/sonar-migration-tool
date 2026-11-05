// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"encoding/json"
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
// The runs covered are the migrate runs (a run_meta.json) older than runDir
// whose entities a completed reset (see readResetRuns) has not wiped since:
// a run older than a reset of every organization is left out, and a run
// older than a reset of some organizations keeps only its entities in the
// other ones. Each run is collected on its own, exactly as a single-run
// report, and the entity sections are then merged with the newest run
// winning per entity, see mergeSections. The runtime sections (timings,
// failure ledger, warnings, rate limiting) stay those of runDir; Runs
// lists every run covered.
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
	earlier := earlierRuns(runDir, exportDir)
	if len(earlier) == 0 {
		return latest, nil
	}
	summaries := []*MigrationSummary{latest}
	for _, run := range earlier {
		s, err := CollectSummary(run.dir, exportDir)
		if err != nil {
			slog.Warn("leaving a migrate run out of the cumulative report: it could not be read",
				"run_id", filepath.Base(run.dir), "error", err)
			continue
		}
		if len(run.resetOrgs) > 0 && !dropOrganizations(s, run.resetOrgs) {
			continue
		}
		summaries = append(summaries, s)
	}
	if len(summaries) == 1 {
		return latest, nil
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

// earlierRun is a migrate run folded into a cumulative report, with the
// organizations a later reset wiped.
type earlierRun struct {
	dir       string
	resetOrgs map[string]bool
}

// resetRun is a completed reset run: the organizations it wiped, or nil
// for every organization.
type resetRun struct {
	id   string
	orgs []string
}

// earlierRuns returns the migrate runs of exportDir older than runDir that
// a completed reset older than runDir has not wiped entirely, newest first.
func earlierRuns(runDir, exportDir string) []earlierRun {
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return nil
	}
	current := filepath.Base(runDir)
	var resets []resetRun
	var runs []string
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || id == current || !common.RunIDAfter(current, id) {
			continue
		}
		dir := filepath.Join(exportDir, id)
		if meta := filepath.Join(dir, "clear.json"); fileExists(meta) {
			if orgs, ok := readCompletedReset(meta); ok {
				resets = append(resets, resetRun{id: id, orgs: orgs})
			}
			continue
		}
		if fileExists(filepath.Join(dir, "run_meta.json")) {
			runs = append(runs, id)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return common.RunIDAfter(runs[i], runs[j]) })
	var out []earlierRun
	for _, id := range runs {
		run := earlierRun{dir: filepath.Join(exportDir, id)}
		wiped := false
		for _, r := range resets {
			if !common.RunIDAfter(r.id, id) {
				continue
			}
			if len(r.orgs) == 0 {
				wiped = true
				break
			}
			if run.resetOrgs == nil {
				run.resetOrgs = map[string]bool{}
			}
			for _, org := range r.orgs {
				run.resetOrgs[org] = true
			}
		}
		if !wiped {
			out = append(out, run)
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readCompletedReset reads a reset run's clear.json. It reports ok only for
// a reset that ran every phase: a dry run and a failed or cancelled reset
// say nothing reliable about what was deleted, so the runs before them stay
// in the report. A clear.json written before resets recorded their
// completion (#667) has no "completed" field and counts as a completed
// reset of every organization. orgs are the organizations the reset was
// confirmed for.
func readCompletedReset(metaPath string) (orgs []string, ok bool) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, false
	}
	var meta struct {
		DryRun        bool     `json:"dry_run"`
		Completed     *bool    `json:"completed"`
		Organizations []string `json:"organizations"`
	}
	if json.Unmarshal(data, &meta) != nil || meta.DryRun || (meta.Completed != nil && !*meta.Completed) {
		return nil, false
	}
	return meta.Organizations, true
}

// dropOrganizations removes from s every entity of one of orgs, which a
// reset newer than s wiped. It reports whether s still has an entity that
// was migrated, failed or partial: a run left with nothing but skip rows
// has nothing to add to the report.
func dropOrganizations(s *MigrationSummary, orgs map[string]bool) bool {
	kept := false
	for i := range s.Sections {
		for b, items := range sectionBuckets(&s.Sections[i]) {
			out := (*items)[:0]
			for _, item := range *items {
				if !orgs[item.Organization] {
					out = append(out, item)
					kept = kept || b != bucketSkipped
				}
			}
			*items = out
		}
	}
	return kept
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

// mergeSections merges the sections of summaries, newest first. An entity
// reported by a newer run replaces every record an older run has of it, so
// a project re-migrated by a later slice shows only its latest outcome.
// Within one run, items are kept as that run reported them.
//
// The exception is a weak record, one saying nothing about what is on the
// target (see isWeak): every slice re-creates the global objects, finding
// those an earlier slice created already there, and reports the objects no
// project of its own slice uses. A weak record is dropped whenever any run
// has a record of the entity that is not weak, so an earlier success is not
// hidden by, nor listed next to, a later slice's "already exists" failure
// or "Not used by any migrated project" row.
func mergeSections(summaries []*MigrationSummary) []Section {
	merged := make([]Section, len(summaries[0].Sections))
	for i, s := range summaries[0].Sections {
		merged[i] = Section{Name: s.Name}
		secs := make([]*Section, len(summaries))
		for run, sum := range summaries {
			secs[run] = sectionByName(sum.Sections, s.Name)
		}
		mergeSection(&merged[i], secs)
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

type weakItem struct {
	run    int
	bucket int
	item   EntityItem
}

// mergeSection merges into dst the runs' records of one section, secs
// newest first; a run without the section has a nil entry.
func mergeSection(dst *Section, secs []*Section) {
	keys := newEntityKeys(dst.Name, secs)
	dstBuckets := sectionBuckets(dst)
	claims := map[string]int{}
	covered := map[string]bool{}
	var weak []weakItem
	for run, sec := range secs {
		if sec == nil {
			continue
		}
		for b, items := range sectionBuckets(sec) {
			for _, item := range *items {
				if isWeak(b, item) {
					weak = append(weak, weakItem{run: run, bucket: b, item: item})
					continue
				}
				key := keys.of(item)
				if claimed, ok := claims[key]; ok && claimed != run {
					continue
				}
				claims[key] = run
				for _, k := range keys.covering(item) {
					covered[k] = true
				}
				*dstBuckets[b] = append(*dstBuckets[b], item)
			}
		}
	}
	weakClaims := map[string]int{}
	for _, w := range weak {
		if covered[keys.coveredBy(w.item)] {
			continue
		}
		key := keys.of(w.item)
		if claimed, ok := weakClaims[key]; ok && claimed != w.run {
			continue
		}
		weakClaims[key] = w.run
		*dstBuckets[w.bucket] = append(*dstBuckets[w.bucket], w.item)
	}
}

// isWeak reports whether item, in the given bucket, says nothing about what
// is on the target: an expected failure (the entity already exists) or an
// extracted entity no project of the run uses.
func isWeak(bucket int, item EntityItem) bool {
	switch bucket {
	case bucketFailed:
		return !migrate.FailureClass(item.Cause).Actionable()
	case bucketSkipped:
		return item.SkipReason == SkipReasonUnused
	}
	return false
}

// entityKeys identifies the entities of one section across runs. A project
// is its source key, whatever organization each run mapped it to; any other
// entity is its name, language (quality profiles) and organization.
//
// Not every record carries all three: a failure read from requests.log has
// no language, and an unused entity row no organization. A quality profile
// failure takes the language of the one profile of that name and
// organization the runs report, when there is only one.
type entityKeys struct {
	section string
	// languages maps a profile's name and organization to its language,
	// or to "" when the runs report it in more than one language.
	languages map[string]string
}

const anyField = "\x01"

func newEntityKeys(section string, secs []*Section) entityKeys {
	keys := entityKeys{section: section}
	if section != sectionQualityProfiles {
		return keys
	}
	keys.languages = map[string]string{}
	for _, sec := range secs {
		if sec == nil {
			continue
		}
		for _, items := range sectionBuckets(sec) {
			for _, item := range *items {
				if item.Language == "" {
					continue
				}
				id := item.Name + "\x00" + item.Organization
				if lang, seen := keys.languages[id]; !seen {
					keys.languages[id] = item.Language
				} else if lang != item.Language {
					keys.languages[id] = ""
				}
			}
		}
	}
	return keys
}

func (k entityKeys) language(item EntityItem) string {
	if item.Language != "" {
		return item.Language
	}
	return k.languages[item.Name+"\x00"+item.Organization]
}

func (k entityKeys) isProject(item EntityItem) bool {
	return k.section == "Projects" && item.SourceKey != ""
}

// of returns the key identifying item's entity.
func (k entityKeys) of(item EntityItem) string {
	if k.isProject(item) {
		return "src\x00" + item.SourceKey
	}
	return item.Name + "\x00" + k.language(item) + "\x00" + item.Organization
}

// covering returns the keys under which a weak record finds item, the
// record of the same entity that is not weak: its own, and the ones with
// the language, the organization or both left out.
func (k entityKeys) covering(item EntityItem) []string {
	if k.isProject(item) {
		return []string{k.of(item)}
	}
	lang := k.language(item)
	return []string{
		item.Name + "\x00" + lang + "\x00" + item.Organization,
		item.Name + "\x00" + anyField + "\x00" + item.Organization,
		item.Name + "\x00" + lang + "\x00" + anyField,
		item.Name + "\x00" + anyField + "\x00" + anyField,
	}
}

// coveredBy returns the key under which a weak record looks up the record
// of its entity that is not weak, leaving out what it does not carry.
func (k entityKeys) coveredBy(item EntityItem) string {
	if k.isProject(item) {
		return k.of(item)
	}
	lang, org := k.language(item), item.Organization
	if lang == "" {
		lang = anyField
	}
	if org == "" {
		org = anyField
	}
	return item.Name + "\x00" + lang + "\x00" + org
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
