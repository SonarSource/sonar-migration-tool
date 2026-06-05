// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// update gates the optional golden-file refresh. The structural assertions
// below are the default contract and pass without any pre-existing golden;
// run `go test -run TestRenderMarkdown -update` to (re)write the golden file.
var update = flag.Bool("update", false, "update the markdown golden file")

const markdownGoldenPath = "testdata/migration_summary.golden.md"

// fullySeededSummary builds a *MigrationSummary that exercises every renderer
// branch in RenderMarkdown: all five Section status buckets, every runtime
// collection (Phases / Tasks / Failures / Warnings.* / Branches / Throughput),
// and a fixed GeneratedAt + run timestamps so the output is reproducible.
//
// The failure row's ErrorMessage deliberately embeds a pipe so the
// cell-escaping path (mdCell) is covered.
func fullySeededSummary() *MigrationSummary {
	fixed := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)

	return &MigrationSummary{
		RunID:       "06-05-2026-01",
		GeneratedAt: fixed,
		StartedAt:   fixed,
		CompletedAt: fixed.Add(90 * time.Second),
		// Total elapsed is derived from the timestamps in the collector,
		// but for an in-code summary we set it explicitly.
		TotalElapsed:  90 * time.Second,
		OverallStatus: "partial",
		Sections: []Section{
			{
				Name: "Projects",
				Succeeded: []EntityItem{
					{Name: "Proj Perfect", Organization: "org1", Detail: "org1_perfect"},
				},
				NearPerfect: []EntityItem{
					{Name: "Proj Near", Organization: "org1", Detail: "org1_near",
						Issues: []string{"new_security_rating_with_aica <= A --> new_security_rating <= A"}},
				},
				Partial: []EntityItem{
					{Name: "Proj Partial", Organization: "org1", Detail: "org1_partial",
						Issues: []string{"The new-code definition (reference branch) was replaced by the org default"}},
				},
				Failed: []EntityItem{
					{Name: "Proj Failed", Organization: "org1",
						ErrorMessage: "create failed: boom | already exists"},
				},
				Skipped: []EntityItem{
					{Name: "Proj Skipped", Organization: "org1",
						SkipReason: SkipReasonOrgSkipped, Detail: "org was skipped by the wizard"},
				},
			},
		},
		Phases: []PhaseTiming{
			{Phase: "Phase 0", Tasks: 3, Duration: 60 * time.Second},
			{Phase: "Phase 1", Tasks: 2, Duration: 30 * time.Second},
		},
		Tasks: []TaskTiming{
			{Phase: 0, Task: "createProjects", Duration: 45 * time.Second, OK: true},
			{Phase: 0, Task: "importScanHistory", Duration: 15 * time.Second, OK: false,
				Err: "CE task failed"},
		},
		Failures: []FailureRow{
			{
				EntityType:   "Project",
				EntityName:   "Proj Failed",
				Organization: "org1",
				URL:          "/api/projects/create",
				HTTPStatus:   "400",
				// Pipe must be escaped so it does not split the cell.
				ErrorMessage: "already exists | duplicate key",
			},
		},
		Warnings: WarningLedger{
			Retries: []RetryStat{
				{Method: "POST", Endpoint: "/api/ce/submit", Count: 3, MaxAttempt: 3, LastStatus: "503"},
			},
			BranchSkips: []BranchSkip{
				{Branch: "feature-x", Findings: 12,
					Reason: "skipping branch: source code not retrievable"},
			},
			GateConditions: []GateConditionSkip{
				{Gate: "Backend QG", Metric: "contains_ai_code", Action: "skipped",
					Note: "addGateConditions: source metric has no SonarQube Cloud equivalent"},
				{Gate: "Backend QG", Metric: "new_security_rating_with_aica", Action: "remapped",
					Note: "addGateConditions: source metric remapped"},
			},
			MetricRemaps: []MetricRemap{
				{Gate: "Backend QG", SourceMetric: "new_security_rating_with_aica",
					TargetMetric: "new_security_rating"},
			},
		},
		Branches: []BranchStat{
			{
				Branch: "feature-x", Type: "LONG", Issues: 0, ExternalIssues: 0,
				Components: 0, ActiveRules: 0, ZipBytes: 0, Status: "skipped",
				SkipReason: "skipping branch: source code not retrievable",
			},
			{
				Branch: "main", Type: "LONG", Issues: 120, ExternalIssues: 5,
				Components: 40, ActiveRules: 300, ZipBytes: 1048576, TaskID: "AY-task-1",
				Status: "submitted",
			},
		},
		Throughput: ThroughputStats{
			TotalIssues:         120,
			TotalExternalIssues: 5,
			TotalComponents:     40,
			TotalZipBytes:       1048576,
			BranchesPackaged:    1,
			BranchesSkipped:     1,
			TasksSubmitted:      1,
			TotalRetries:        3,
		},
	}
}

// assertLimitationsSection renders a summary that carries one limitation and
// asserts both the "## Migration Limitations" header and the bullet text are
// emitted. Kept separate to keep the main structural test under the cognitive-
// complexity threshold.
func assertLimitationsSection(t *testing.T) {
	t.Helper()
	withLimits := fullySeededSummary()
	withLimits.Limitations = []string{
		"Applications do not exist on SonarQube Cloud (3 SQS applications were not migrated).",
	}
	limOut, err := RenderMarkdown(withLimits)
	if err != nil {
		t.Fatalf("RenderMarkdown(withLimits): %v", err)
	}
	if !strings.Contains(string(limOut), "## Migration Limitations") {
		t.Errorf("expected Migration Limitations H2 header once a limitation is present")
	}
	if !strings.Contains(string(limOut), "Applications do not exist on SonarQube Cloud") {
		t.Errorf("expected the limitation bullet text in the Migration Limitations section")
	}
}

func TestRenderMarkdownStructuralContract(t *testing.T) {
	out, err := RenderMarkdown(fullySeededSummary())
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	got := string(out)

	// Expected H2 headers — every major runtime/section header must be
	// present given the fully-seeded summary above. ("## Warnings, Retries &
	// Skips" is the Warnings H2; "## Branch Scan History" and "## Migration
	// Limitations" round out the runtime sections.)
	wantHeaders := []string{
		"## Executive Summary",
		"## Bottlenecks",
		"## Failure Ledger",
		"## Warnings, Retries & Skips",
		"## Branch Scan History",
	}
	for _, h := range wantHeaders {
		if !strings.Contains(got, h) {
			t.Errorf("expected H2 header %q in markdown output", h)
		}
	}

	// Limitations are emitted only when summary.Limitations is non-empty. The
	// fully-seeded summary intentionally leaves them empty, so the section is
	// absent here; the header presence is covered separately.
	if strings.Contains(got, "## Migration Limitations") {
		t.Errorf("did not expect Migration Limitations section with empty Limitations")
	}
	assertLimitationsSection(t)

	// Failure-ledger row: entity name + escaped error message must be present.
	if !strings.Contains(got, "Proj Failed") {
		t.Errorf("expected failure-ledger row for 'Proj Failed'")
	}
	if !strings.Contains(got, "/api/ce/submit") && !strings.Contains(got, "already exists") {
		t.Errorf("expected failure-ledger error content in output")
	}

	// Branch-skip reason must surface (in the Warnings branch-skip table and
	// the Branch Scan History table).
	if !strings.Contains(got, "source code not retrievable") {
		t.Errorf("expected branch-skip reason in markdown output")
	}
	if !strings.Contains(got, "feature-x") {
		t.Errorf("expected skipped branch 'feature-x' in markdown output")
	}

	// Pipe escaping: the injected error message contains a literal pipe; the
	// rendered cell must carry the escaped form "\\|" and must NOT contain
	// the raw " | duplicate" that would split the table cell.
	if !strings.Contains(got, "already exists \\| duplicate key") {
		t.Errorf("expected pipe in error message to be escaped as \\| , got output:\n%s", got)
	}

	// Determinism: rendering twice must produce byte-identical output.
	out2, err := RenderMarkdown(fullySeededSummary())
	if err != nil {
		t.Fatalf("RenderMarkdown (2nd call): %v", err)
	}
	if !bytes.Equal(out, out2) {
		t.Errorf("RenderMarkdown is not deterministic: two renders differ")
	}
}

// TestRenderMarkdownGolden is gated behind -update. By default it compares the
// rendered bytes to the golden file IF it exists; when -update is set it
// (re)writes the golden. With no golden present and no -update flag, the test
// skips so the suite passes without a pre-existing golden.
func TestRenderMarkdownGolden(t *testing.T) {
	out, err := RenderMarkdown(fullySeededSummary())
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}

	if *update {
		if err := os.MkdirAll(filepath.Dir(markdownGoldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(markdownGoldenPath, out, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote golden file %s (%d bytes)", markdownGoldenPath, len(out))
		return
	}

	want, err := os.ReadFile(markdownGoldenPath)
	if err != nil {
		t.Skipf("golden file %s not present; run with -update to create it", markdownGoldenPath)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("rendered markdown does not match golden %s; run with -update to refresh", markdownGoldenPath)
	}
}
