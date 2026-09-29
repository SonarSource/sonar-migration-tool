// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package summary

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// portfolioFailure describes a failed PATCH/DELETE against
// /enterprises/portfolios/{id}. The SonarQube Cloud enterprise API encodes
// the portfolio id in the URL rather than the body, so failures land in
// requests.log as "Unknown" entries under the analysis report classifier.
// This helper re-parses them and binds the failure back to the portfolio
// name via createPortfolios JSONL.
type portfolioFailure struct {
	CloudPortfolioID string
	Error            string

	// HTTPStatus is set only for a failure read back from requests.log,
	// where the status and message are SonarQube Cloud's own. A sidecar
	// failure never reached the wire and leaves this zero, which is what
	// keeps its tool-authored reason from being classified as if the
	// platform had said it.
	HTTPStatus int
}

// collectPortfolioFailures combines two sources of per-portfolio failures:
//
//  1. requests.log entries for failed PATCH/DELETE on
//     /enterprises/portfolios/{id}.
//  2. The configurePortfolios.failures JSONL sidecar, which records
//     task-level failures (e.g. "no resolved projects on source") that
//     never produced an HTTP request.
//
// Entries are keyed by the cloud portfolio id. When both sources have an
// entry for the same portfolio, the HTTP failure wins (more specific).
func collectPortfolioFailures(runDir string) map[string]portfolioFailure {
	out := make(map[string]portfolioFailure)
	mergePortfolioSidecar(runDir, out)
	mergePortfolioRequestLog(runDir, out)
	return out
}

// mergePortfolioRequestLog reads requests.log and records HTTP-level
// portfolio failures.
func mergePortfolioRequestLog(runDir string, out map[string]portfolioFailure) {
	logPath := filepath.Join(runDir, "requests.log")
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		entry, ok := parseConfigLogLine(scanner.Text())
		if !ok {
			continue
		}
		if id, fail, ok := matchPortfolioFailure(entry); ok {
			out[id] = fail // Most recent / HTTP wins.
		}
	}
}

// mergePortfolioSidecar reads the configurePortfolios.failures JSONL written
// by the migrate task and records task-level failures (these don't produce
// HTTP requests, so requests.log can't see them).
func mergePortfolioSidecar(runDir string, out map[string]portfolioFailure) {
	dir := filepath.Join(runDir, "configurePortfolios.failures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var rec struct {
				CloudPortfolioID string `json:"cloud_portfolio_id"`
				Reason           string `json:"reason"`
			}
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			if rec.CloudPortfolioID == "" {
				continue
			}
			if _, alreadyHTTPFailure := out[rec.CloudPortfolioID]; alreadyHTTPFailure {
				continue
			}
			out[rec.CloudPortfolioID] = portfolioFailure{
				CloudPortfolioID: rec.CloudPortfolioID,
				Error:            rec.Reason,
			}
		}
		f.Close()
	}
}

func matchPortfolioFailure(entry map[string]any) (string, portfolioFailure, bool) {
	if asString(entry["process_type"]) != "request_completed" {
		return "", portfolioFailure{}, false
	}
	payload, ok := entry["payload"].(map[string]any)
	if !ok {
		return "", portfolioFailure{}, false
	}
	method := asString(payload["method"])
	if method != "PATCH" && method != "DELETE" {
		return "", portfolioFailure{}, false
	}
	url := asString(payload["url"])
	id, ok := extractPortfolioIDFromURL(url)
	if !ok {
		return "", portfolioFailure{}, false
	}
	if !isFailure(payload["status"], asString(entry["status"])) {
		return "", portfolioFailure{}, false
	}
	return id, portfolioFailure{
		CloudPortfolioID: id,
		Error:            extractFailureError(payload),
		HTTPStatus:       httpStatusOf(payload["status"]),
	}, true
}

// extractPortfolioIDFromURL returns the portfolio id from a URL ending in
// /enterprises/portfolios/{id}.
func extractPortfolioIDFromURL(url string) (string, bool) {
	const prefix = "/enterprises/portfolios/"
	idx := strings.Index(url, prefix)
	if idx < 0 {
		return "", false
	}
	id := url[idx+len(prefix):]
	if id == "" {
		return "", false
	}
	// Trim any query string.
	if q := strings.Index(id, "?"); q >= 0 {
		id = id[:q]
	}
	return id, id != ""
}

// applyPortfolioFailures moves any Succeeded portfolio whose cloud id appears
// in the failures map into the Failed bucket. The portfolio is removed from
// the Partial list as well to avoid double-counting. Returns the new
// (succeeded, failed, partial) lists.
func applyPortfolioFailures(store *common.DataStore,
	succeeded, failed, partial []EntityItem,
	failures map[string]portfolioFailure) ([]EntityItem, []EntityItem, []EntityItem) {

	if len(failures) == 0 || len(succeeded) == 0 {
		return succeeded, failed, partial
	}

	// Build name → cloud id from createPortfolios JSONL — same lookup we use
	// for the hierarchy detector.
	idByName := portfolioNameToID(store)
	if len(idByName) == 0 {
		return succeeded, failed, partial
	}

	keep := succeeded[:0:0]
	for _, item := range succeeded {
		id, ok := idByName[item.Name]
		if !ok {
			keep = append(keep, item)
			continue
		}
		fail, broken := failures[id]
		if !broken {
			keep = append(keep, item)
			continue
		}
		failedItem := EntityItem{
			Name:         item.Name,
			Organization: item.Organization,
			Detail:       item.Detail,
			ErrorMessage: failureMessage(fail),
			Cause:        classifyPortfolioCause(fail),
		}
		failed = append(failed, failedItem)
	}

	if len(partial) > 0 {
		partialKeep := partial[:0:0]
		for _, item := range partial {
			if id, ok := idByName[item.Name]; ok {
				if _, broken := failures[id]; broken {
					continue
				}
			}
			partialKeep = append(partialKeep, item)
		}
		partial = partialKeep
	}

	return keep, failed, partial
}

func portfolioNameToID(store *common.DataStore) map[string]string {
	items, err := store.ReadAll("createPortfolios")
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		name := jsonStr(item, "name")
		id := jsonStr(item, "cloud_portfolio_id")
		if name == "" || id == "" {
			continue
		}
		out[name] = id
	}
	return out
}

// classifyPortfolioCause labels a portfolio failure with the run's own
// classification — the invariant every builder of a Failed item holds,
// and which this one did not, leaving the side of the split its rows
// land on to the zero value.
//
// Only a failure read back from requests.log is classified: its status
// and message are SonarQube Cloud's own, which is what ClassifyHTTPFailure
// reads everywhere else. A sidecar failure never reached the wire and its
// reason is prose this tool wrote, so it stays unclassified — and so
// actionable, which is the right way round for a default.
func classifyPortfolioCause(f portfolioFailure) string {
	if f.HTTPStatus == 0 {
		return ""
	}
	return classifyFailureCause(strconv.Itoa(f.HTTPStatus), failureMessage(f))
}

// httpStatusOf reads requests.log's numeric status, which is a JSON
// number and so arrives as a float64.
func httpStatusOf(v any) int {
	n, ok := numericStatus(v)
	if !ok {
		return 0
	}
	return int(n)
}

func failureMessage(f portfolioFailure) string {
	if f.Error == "" {
		return "Portfolio configuration failed"
	}
	return f.Error
}

// jsonRawHasField is unused but documented here for readers: avoid trying to
// parse the whole raw item — the project already has jsonStr to pull strings
// out of json.RawMessage values.
var _ = json.RawMessage(nil)
