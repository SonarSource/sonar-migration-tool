// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package regtest

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// FormatReport writes the report in the specified format.
func FormatReport(w io.Writer, r *Report, format string) error {
	switch format {
	case "json":
		return formatJSON(w, r)
	case "markdown":
		return formatMarkdown(w, r)
	default:
		return formatTable(w, r)
	}
}

func formatJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func formatTable(w io.Writer, r *Report) error {
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "╔══════════════════════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(w, "║                    REGRESSION TEST SUITE RESULTS                            ║\n")
	fmt.Fprintf(w, "╠══════════════════════════════════════════════════════════════════════════════╣\n")
	fmt.Fprintf(w, "║  SQS: %-69s ║\n", r.SQSURL)
	fmt.Fprintf(w, "║  SC:  %-69s ║\n", r.SCURL)
	fmt.Fprintf(w, "║  Org: %-69s ║\n", r.SCOrg)
	fmt.Fprintf(w, "║  Duration: %-64s ║\n", r.Duration.Round(100*1e6))
	fmt.Fprintf(w, "╠══════════════════════════════════════════════════════════════════════════════╣\n")

	verdictLine := fmt.Sprintf("  VERDICT: %s  |  Total: %d  Passed: %d  Failed: %d  Errors: %d  Skipped: %d  SQS_AND_SQC_FEATURE_DIVERGENCE: %d",
		r.Verdict, r.TotalChecks, r.Passed, r.Failed, r.Errors, r.Skipped, r.SqsAndSqcFeatureDivergence)
	fmt.Fprintf(w, "║%-77s║\n", verdictLine)
	fmt.Fprintf(w, "╚══════════════════════════════════════════════════════════════════════════════╝\n\n")

	// Group by category
	categories := groupByCategory(r.Results)
	for _, cat := range categories {
		fmt.Fprintf(w, "── %s ─────────────────────────────────────────────────────────\n", cat.Name)
		fmt.Fprintf(w, "  %-4s %-40s %-12s %-12s %-30s %s\n", "#", "Check", "SQS", "SC", "Match", "Notes")
		fmt.Fprintf(w, "  %-4s %-40s %-12s %-12s %-30s %s\n",
			"----", "----------------------------------------", "------------", "------------", "------------------------------", "-----")
		for _, r := range cat.Results {
			status := "PASS"
			if r.Error != "" {
				status = "ERR"
			} else if r.Notes == "SKIPPED" {
				status = "SKIP"
			} else if r.SqsAndSqcFeatureDivergence {
				status = "SQS_AND_SQC_FEATURE_DIVERGENCE"
			} else if !r.Match {
				status = "FAIL"
			}

			name := truncateStr(r.Name, 40)
			sqsVal := truncateStr(r.SQSValue, 12)
			scVal := truncateStr(r.SCValue, 12)
			notes := r.Notes
			if r.Error != "" {
				notes = "ERROR: " + truncateStr(r.Error, 40)
			}
			fmt.Fprintf(w, "  %-4d %-40s %-12s %-12s %-30s %s\n",
				r.ID, name, sqsVal, scVal, status, notes)
		}
		fmt.Fprintf(w, "\n")
	}

	// Summary of failures (real mismatches only — SQS_AND_SQC_FEATURE_DIVERGENCE
	// results are reported separately below, not as failures).
	failures := filterResults(r.Results, func(r CheckResult) bool {
		return !r.Match && !r.SqsAndSqcFeatureDivergence && r.Error == "" && r.Notes != "SKIPPED"
	})
	if len(failures) > 0 {
		fmt.Fprintf(w, "═══ FAILURES (%d) ═══════════════════════════════════════════════════════════\n", len(failures))
		for _, f := range failures {
			fmt.Fprintf(w, "  [%s] %s: SQS=%s SC=%s %s\n", f.Category, f.Name, f.SQSValue, f.SCValue, f.Notes)
		}
		fmt.Fprintf(w, "\n")
	}

	// SQS_AND_SQC_FEATURE_DIVERGENCE: known SonarQube Server vs. SonarQube
	// Cloud differences that are expected, permanent, and not something a
	// rerun or a code fix will change.
	sqsAndSqcFeatureDivergences := filterResults(r.Results, func(r CheckResult) bool { return r.SqsAndSqcFeatureDivergence })
	if len(sqsAndSqcFeatureDivergences) > 0 {
		fmt.Fprintf(w, "═══ SQS_AND_SQC_FEATURE_DIVERGENCE (%d) — SQS vs SQC by design, not bugs ═══════════\n", len(sqsAndSqcFeatureDivergences))
		for _, d := range sqsAndSqcFeatureDivergences {
			fmt.Fprintf(w, "  [%s] %s: SQS=%s SC=%s\n      %s\n", d.Category, d.Name, d.SQSValue, d.SCValue, d.Notes)
		}
		fmt.Fprintf(w, "\n")
	}

	errs := filterResults(r.Results, func(r CheckResult) bool { return r.Error != "" })
	if len(errs) > 0 {
		fmt.Fprintf(w, "═══ ERRORS (%d) ════════════════════════════════════════════════════════════\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(w, "  [%s] %s: %s\n", e.Category, e.Name, e.Error)
		}
		fmt.Fprintf(w, "\n")
	}

	return nil
}

func formatMarkdown(w io.Writer, r *Report) error {
	fmt.Fprintf(w, "# Regression Test Suite Results\n\n")
	fmt.Fprintf(w, "| Property | Value |\n|---|---|\n")
	fmt.Fprintf(w, "| SQS URL | %s |\n", r.SQSURL)
	fmt.Fprintf(w, "| SC URL | %s |\n", r.SCURL)
	fmt.Fprintf(w, "| SC Org | %s |\n", r.SCOrg)
	fmt.Fprintf(w, "| Duration | %s |\n", r.Duration.Round(100*1e6))
	fmt.Fprintf(w, "| **Verdict** | **%s** |\n", r.Verdict)
	fmt.Fprintf(w, "| Total | %d |\n", r.TotalChecks)
	fmt.Fprintf(w, "| Passed | %d |\n", r.Passed)
	fmt.Fprintf(w, "| Failed | %d |\n", r.Failed)
	fmt.Fprintf(w, "| Errors | %d |\n", r.Errors)
	fmt.Fprintf(w, "| Skipped | %d |\n", r.Skipped)
	fmt.Fprintf(w, "| SQS_AND_SQC_FEATURE_DIVERGENCE (known SQS vs SQC differences) | %d |\n\n", r.SqsAndSqcFeatureDivergence)

	fmt.Fprintf(w, "## Results\n\n")
	fmt.Fprintf(w, "| # | Category | Check | SQS | SC | Match | Notes |\n")
	fmt.Fprintf(w, "|---|---|---|---|---|---|---|\n")
	for _, res := range r.Results {
		status := "PASS"
		if res.Error != "" {
			status = "ERR"
		} else if res.Notes == "SKIPPED" {
			status = "SKIP"
		} else if res.SqsAndSqcFeatureDivergence {
			status = "SQS_AND_SQC_FEATURE_DIVERGENCE"
		} else if !res.Match {
			status = "FAIL"
		}
		notes := res.Notes
		if res.Error != "" {
			notes = "ERROR: " + res.Error
		}
		fmt.Fprintf(w, "| %d | %s | %s | %s | %s | %s | %s |\n",
			res.ID, res.Category, res.Name, res.SQSValue, res.SCValue, status, notes)
	}

	return nil
}

type categoryGroup struct {
	Name    string
	Results []CheckResult
}

func groupByCategory(results []CheckResult) []categoryGroup {
	order := make([]string, 0)
	groups := make(map[string][]CheckResult)
	for _, r := range results {
		if _, exists := groups[r.Category]; !exists {
			order = append(order, r.Category)
		}
		groups[r.Category] = append(groups[r.Category], r)
	}
	out := make([]categoryGroup, len(order))
	for i, name := range order {
		out[i] = categoryGroup{Name: name, Results: groups[name]}
	}
	return out
}

func filterResults(results []CheckResult, pred func(CheckResult) bool) []CheckResult {
	var out []CheckResult
	for _, r := range results {
		if pred(r) {
			out = append(out, r)
		}
	}
	return out
}

func truncateStr(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
