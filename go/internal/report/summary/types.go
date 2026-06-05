// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// Package summary generates a PDF migration summary report from task outputs.
package summary

import "time"

// MigrationSummary holds the collected data for the PDF report.
//
// Limitations is a list of free-text messages rendered as a
// "Migration limitations" section at the very end of the report
// (issue #154). It documents SonarQube Server features that have no
// SonarQube Cloud counterpart, so the operator knows which parts of
// the source platform did NOT make it across — e.g. applications.
//
// OmitSections lets a caller hide named sections from both the
// executive summary table and the per-section detail tables. The
// predictive-report command (#235) sets this for "Global Settings"
// because settings prediction needs runtime SQC support detection.
//
// Predictive toggles a small set of presentation tweaks that only
// apply to the predict pipeline (#240): the title is "SonarQube
// Migration Prediction" (with "Prediction" underlined), the
// Organization column is hidden from per-object tables, and synthetic
// cloud-side IDs are stripped from the Details column.
type MigrationSummary struct {
	RunID        string
	GeneratedAt  time.Time
	Sections     []Section
	Limitations  []string
	OmitSections map[string]bool
	Predictive   bool

	// Runtime execution fields (issue #240+ migrate engine telemetry).
	// These are populated only for real migration runs from the run
	// directory's run_meta.json / run_events.jsonl. Under predictive
	// reports they are absent and carry zero values, so the sections
	// that render them omit themselves entirely.
	StartedAt     time.Time
	CompletedAt   time.Time
	TotalElapsed  time.Duration
	OverallStatus string
	Phases        []PhaseTiming
	Tasks         []TaskTiming
	Failures      []FailureRow
	Warnings      WarningLedger
	Branches      []BranchStat
	Throughput    ThroughputStats
}

// PhaseTiming captures the wall-clock duration of one migration phase.
type PhaseTiming struct {
	Phase    string
	Tasks    int
	Duration time.Duration
}

// TaskTiming captures the outcome and duration of a single task within a phase.
type TaskTiming struct {
	Phase    int
	Task     string
	Duration time.Duration
	OK       bool
	Err      string
}

// FailureRow describes a single entity-level failure for the failures table.
type FailureRow struct {
	EntityType   string
	EntityName   string
	Organization string
	URL          string
	HTTPStatus   string
	ErrorMessage string
}

// RetryStat aggregates retried requests by method+endpoint.
type RetryStat struct {
	Method     string
	Endpoint   string
	Count      int
	MaxAttempt int
	LastStatus string
}

// BranchSkip records a branch whose source code could not be retrieved.
type BranchSkip struct {
	Branch   string
	Findings int
	Reason   string
}

// GateConditionSkip records a quality-gate condition that was skipped or
// remapped. Action is "skipped" or "remapped".
type GateConditionSkip struct {
	Gate   string
	Metric string
	Action string
	Note   string
}

// MetricRemap records a source metric remapped to a SonarQube Cloud equivalent.
type MetricRemap struct {
	Gate         string
	SourceMetric string
	TargetMetric string
}

// WarningLedger collects the non-fatal advisories surfaced during a run.
type WarningLedger struct {
	Retries        []RetryStat
	BranchSkips    []BranchSkip
	GateConditions []GateConditionSkip
	MetricRemaps   []MetricRemap
}

// BranchStat captures per-branch packaging/submission stats.
// Status is one of packaged|submitted|skipped.
type BranchStat struct {
	Branch         string
	Type           string
	Issues         int
	ExternalIssues int
	Components     int
	ActiveRules    int
	ZipBytes       int64
	TaskID         string
	Status         string
	SkipReason     string
}

// ThroughputStats aggregates totals across all branches for the run.
type ThroughputStats struct {
	TotalIssues         int
	TotalExternalIssues int
	TotalComponents     int
	TotalZipBytes       int64
	BranchesPackaged    int
	BranchesSkipped     int
	TasksSubmitted      int
	TotalRetries        int
}

// Section represents a category of migrated entities (e.g., Projects, Quality Gates).
//
// The four success/non-success buckets correspond to the five-status taxonomy
// from issues #224 and #227:
//   - Succeeded   → green  (perfect-fidelity migration)
//   - NearPerfect → yellow (migrated with a known close-equivalent substitution,
//                   e.g. a metric mapping from #143)
//   - Partial     → orange (created on SQC but a follow-up configuration step
//                   was incomplete, or a feature was dropped)
//   - Failed      → red    (create call itself failed)
//   - Skipped     → grey   (deliberately skipped by configuration)
type Section struct {
	Name        string
	Succeeded   []EntityItem
	NearPerfect []EntityItem
	Partial     []EntityItem
	Failed      []EntityItem
	Skipped     []EntityItem
}

// EntityItem represents a single entity in the report.
type EntityItem struct {
	Name         string
	Language     string   // populated for Quality Profiles only; empty otherwise
	Organization string
	Detail       string   // cloud key for successes, scan history status, skip reason
	ErrorMessage string   // failures only
	SkipReason   string   // for skipped items: SkipReason* constants below
	Issues       []string // for partial migrations: human-readable list of issues
}

// Skip reason constants used when classifying skipped entities.
//
// SkipReasonSQSOnly marks rows that describe a SonarQube-Server-only
// setting which has no SonarQube Cloud counterpart (issue #200). The
// row carries a single section-level note (Organization is left
// blank) and is rendered last in the Skipped bucket so it appears at
// the bottom of the section.
const (
	SkipReasonOrgSkipped   = "org-skipped"
	SkipReasonBuiltIn      = "built-in"
	SkipReasonUnused       = "unused"
	SkipReasonSQSOnly      = "sqs-only"
	SkipReasonDefaultValue = "default-value"
	// SkipReasonEmpty marks portfolios that resolve to zero projects
	// on the source — empty SQS portfolios are not migrated.
	SkipReasonEmpty = "empty"
)

// sectionDef maps a report section to its corresponding task names and analysis entity type.
type sectionDef struct {
	Name           string // display name
	InputTask      string // generate*Mappings task (for computing skips)
	OutputTask     string // create* task (successes)
	AnalysisEntity string // entity type in analysis report (for failures)
	NameField      string // JSONL field to extract entity name
	DetailField    string // JSONL field for detail column (e.g., cloud key)
	ExtractTask    string // extract task for source data (empty if not applicable)
}

// sectionDefs defines the sections in report order.
var sectionDefs = []sectionDef{
	{
		Name:           "Quality Gates",
		InputTask:      "generateGateMappings",
		OutputTask:     "createGates",
		AnalysisEntity: "Quality Gate",
		NameField:      "name",
		DetailField:    "cloud_gate_id",
		ExtractTask:    "getGates",
	},
	{
		Name:           "Quality Profiles",
		InputTask:      "generateProfileMappings",
		OutputTask:     "createProfiles",
		AnalysisEntity: "Quality Profile",
		NameField:      "name",
		DetailField:    "cloud_profile_key",
		ExtractTask:    "getProfiles",
	},
	{
		Name:           "Permission Templates",
		InputTask:      "generateTemplateMappings",
		OutputTask:     "createPermissionTemplates",
		AnalysisEntity: "Permission Template",
		NameField:      "name",
		DetailField:    "cloud_template_id",
	},
	{
		Name:           "Groups",
		InputTask:      "generateGroupMappings",
		OutputTask:     "createGroups",
		AnalysisEntity: "Group",
		NameField:      "name",
		DetailField:    "cloud_group_id",
	},
	{
		Name:           "Portfolios",
		InputTask:      "generatePortfolioMappings",
		OutputTask:     "createPortfolios",
		AnalysisEntity: "Portfolio",
		NameField:      "name",
		DetailField:    "cloud_portfolio_id",
	},
	{
		Name:           "Projects",
		InputTask:      "generateProjectMappings",
		OutputTask:     "createProjects",
		AnalysisEntity: "Project",
		NameField:      "name",
		DetailField:    "cloud_project_key",
	},
	{
		// Global settings (issue #186) — one row per non-default SQS
		// setting, fanned out across orgs by collectGlobalSettings.
		// InputTask is left empty: the "skip when value=default"
		// filter is applied inside the migrate task, so the report
		// just reflects what was written. NameField is the setting
		// key; DetailField is the pre-rendered value + per-org
		// summary string set by renderGlobalSettingDetail.
		Name:        "Global Settings",
		OutputTask:  "setGlobalSettings",
		NameField:   "key",
		DetailField: "detail",
	},
}

// skippedOrgSentinel matches the value used by the wizard when an org is skipped.
const skippedOrgSentinel = "SKIPPED"
