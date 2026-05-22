package summary

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
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
}

// collectPortfolioFailures re-parses requests.log and returns one entry per
// failed PATCH/DELETE on /enterprises/portfolios/{id}. Entries are keyed by
// the cloud portfolio id pulled from the URL.
func collectPortfolioFailures(runDir string) map[string]portfolioFailure {
	logPath := filepath.Join(runDir, "requests.log")
	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	out := make(map[string]portfolioFailure)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		entry, ok := parseConfigLogLine(scanner.Text())
		if !ok {
			continue
		}
		if id, fail, ok := matchPortfolioFailure(entry); ok {
			// Keep the most recent failure for an id.
			out[id] = fail
		}
	}
	return out
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
