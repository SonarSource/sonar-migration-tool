// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package analysis

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// ReportRow is a single row in the final analysis report CSV.
type ReportRow struct {
	EntityType   string `csv:"entity_type"`
	EntityName   string `csv:"entity_name"`
	Organization string `csv:"organization"`
	URL          string `csv:"url"`
	HTTPStatus   string `csv:"http_status"`
	Outcome      string `csv:"outcome"`
	ErrorMessage string `csv:"error_message"`

	// Project is the project the request acted on, when its body named
	// one. Many entities are only meaningful alongside their project: two
	// projects failing to have their source link set produced two rows
	// reading "SQS migrated project" — the link's name, identical for
	// every project — with no way to tell which projects were affected.
	Project string `csv:"project"`
}

// urlEntityMap maps API URL paths to human-readable entity type names.
var urlEntityMap = map[string]string{
	"/api/projects/create":                  "Project",
	"/api/projects/search":                  "Project",
	"/api/projects/delete":                  "Project",
	"/api/navigation/component":             "Project",
	"/api/project_branches/list":            "Branch",
	"/api/project_branches/rename":          "Branch",
	"/api/project_tags/set":                 "Project Tag",
	"/api/qualitygates/create":              "Quality Gate",
	"/api/qualitygates/create_condition":     "Quality Gate Condition",
	"/api/qualitygates/select":              "Quality Gate Assignment",
	"/api/qualitygates/set_as_default":       "Quality Gate Default",
	"/api/qualityprofiles/create":           "Quality Profile",
	"/api/qualityprofiles/restore":          "Quality Profile",
	"/api/qualityprofiles/set_default":       "Quality Profile Default",
	"/api/qualityprofiles/add_project":       "Quality Profile Assignment",
	"/api/qualityprofiles/add_group":         "Quality Profile Permission",
	"/api/qualityprofiles/change_parent":     "Quality Profile Inheritance",
	"/api/user_groups/create":               "Group",
	"/api/user_groups/add_user":              "Group Membership",
	"/api/permissions/create_template":       "Permission Template",
	"/api/permissions/set_default_template":  "Permission Template Default",
	"/api/permissions/add_group_to_template": "Template Permission",
	"/api/permissions/add_group":             "Group Permission",
	"/api/settings/set":                     "Setting",
	"/api/settings/values":                  "Setting",
	"/api/project_links/create":              "Project Link",
	"/api/project_links/search":              "Project Link",
	"/api/project_links/delete":              "Project Link",
	"/api/webhooks/create":                   "Webhook",
	"/api/new_code_periods/set":              "New Code Period",
	"/api/rules/update":                     "Rule",
	"/api/alm_integration/list_repositories": "ALM Repository",
	"/dop-translation/project-bindings":      "Project Binding",
	"/enterprises/portfolios":               "Portfolio",
	"api/users/current":                     "User",
}

// entityNameFields is the priority order for extracting entity names from request bodies.
var entityNameFields = []string{"name", "project", "projectKey", "gateName", "groupName", "key", "language"}

// ParseRequestsLog parses requests.log and returns report rows without writing CSV.
func ParseRequestsLog(runDir string) ([]ReportRow, error) {
	logPath := filepath.Join(runDir, "requests.log")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return nil, nil
	}
	return parseLogFile(logPath)
}

// GenerateReport parses requests.log in runDirectory and writes final_analysis_report.csv
// to the same directory. Returns the parsed rows.
func GenerateReport(runDirectory string) ([]ReportRow, error) {
	return GenerateReportTo(runDirectory, "")
}

// GenerateReportTo parses requests.log and writes CSV to outputDirectory.
// If outputDirectory is empty, writes to runDirectory.
func GenerateReportTo(runDirectory, outputDirectory string) ([]ReportRow, error) {
	if outputDirectory == "" {
		outputDirectory = runDirectory
	}

	logPath := filepath.Join(runDirectory, "requests.log")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return nil, nil
	}

	rows, err := parseLogFile(logPath)
	if err != nil {
		return nil, fmt.Errorf("parsing requests.log: %w", err)
	}

	if len(rows) > 0 {
		if err := structure.ExportCSV(outputDirectory, "final_analysis_report", rows); err != nil {
			return nil, fmt.Errorf("writing CSV: %w", err)
		}
	}

	return rows, nil
}

func parseLogFile(logPath string) ([]ReportRow, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []ReportRow
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		entry, ok := parseLogLine(scanner.Text())
		if !ok {
			continue
		}
		if row := processEntry(entry); row != nil {
			rows = append(rows, *row)
		}
	}
	return rows, scanner.Err()
}

func parseLogLine(line string) (map[string]any, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return nil, false
	}
	return entry, true
}

func classifyEntityType(url string) string {
	if url == "" {
		return "Unknown"
	}
	for path, entityType := range urlEntityMap {
		if strings.HasSuffix(url, path) || url == path {
			return entityType
		}
	}
	return "Unknown"
}

func getRequestBody(payload map[string]any) map[string]any {
	for _, key := range []string{"data", "json", "params"} {
		if v, ok := payload[key]; ok && v != nil {
			if m, ok := v.(map[string]any); ok {
				return m
			}
			if s, ok := v.(string); ok {
				var m map[string]any
				if json.Unmarshal([]byte(s), &m) == nil {
					return m
				}
			}
		}
	}
	return nil
}

func extractEntityName(payload map[string]any) string {
	body := getRequestBody(payload)
	if body == nil {
		return ""
	}
	for _, field := range entityNameFields {
		if v, ok := body[field]; ok && v != nil {
			if s := fmt.Sprintf("%v", v); s != "" {
				return s
			}
		}
	}
	return ""
}

func extractOrganization(payload map[string]any) string {
	body := getRequestBody(payload)
	if body == nil {
		return ""
	}
	if v, ok := body["organization"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// projectFields is the priority order for identifying the project a
// request acted on. "projectKey" first: where a body carries both, it is
// the target-side key, while "project" is sometimes the source one.
var projectFields = []string{"projectKey", "project", "component"}

// extractProject returns the project named in the request body, or "".
func extractProject(payload map[string]any) string {
	body := getRequestBody(payload)
	if body == nil {
		return ""
	}
	for _, field := range projectFields {
		if v, ok := body[field]; ok && v != nil {
			if s := fmt.Sprintf("%v", v); s != "" {
				return s
			}
		}
	}
	return ""
}

func extractErrorMessage(payload map[string]any) string {
	responseVal := payload["response"]
	if responseVal == nil {
		responseVal = payload["content"]
	}
	if responseVal == nil {
		return ""
	}
	return extractErrorsFromValue(responseVal)
}

func extractErrorsFromValue(val any) string {
	switch v := val.(type) {
	case map[string]any:
		return joinErrorMsgs(v)
	case string:
		if v == "" {
			return ""
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(v), &parsed) == nil {
			if msgs := joinErrorMsgs(parsed); msgs != "" {
				return msgs
			}
			return ""
		}
		// Not SonarQube's {"errors":[{"msg":...}]} shape. Returning ""
		// here left the ledger's Error column blank for every failure
		// that did not come from SonarQube itself — a CloudFront 403
		// served as an HTML page recorded its status and nothing else,
		// so the report showed a bare "403" with no reason.
		return summarizeNonJSONError(v)
	}
	return ""
}

// htmlTagRe strips tags so an HTML error page can be reduced to its text.
var htmlTagRe = regexp.MustCompile(`(?s)<[^>]*>`)

// htmlTitleRe pulls the most descriptive line out of an HTML error page:
// its <title>, or the first heading.
var htmlTitleRe = regexp.MustCompile(`(?is)<(?:title|h1|h2)[^>]*>(.*?)</(?:title|h1|h2)>`)

// maxNonJSONErrorLen caps the summary so one gateway error page cannot
// push a whole table off the side of the report.
const maxNonJSONErrorLen = 200

// summarizeNonJSONError reduces a non-JSON error body to a single
// readable line: the headings of an HTML page, else its collapsed text.
func summarizeNonJSONError(body string) string {
	var out string
	if matches := htmlTitleRe.FindAllStringSubmatch(body, -1); len(matches) > 0 {
		parts := make([]string, 0, len(matches))
		seen := map[string]bool{}
		for _, m := range matches {
			text := collapseWhitespace(htmlTagRe.ReplaceAllString(m[1], " "))
			if text != "" && !seen[text] {
				seen[text] = true
				parts = append(parts, text)
			}
		}
		out = strings.Join(parts, ": ")
	}
	if out == "" {
		out = collapseWhitespace(htmlTagRe.ReplaceAllString(body, " "))
	}
	if len(out) > maxNonJSONErrorLen {
		out = strings.TrimSpace(out[:maxNonJSONErrorLen]) + "..."
	}
	return out
}

// collapseWhitespace squeezes every run of whitespace into one space and
// trims the result, so a multi-line page becomes one table cell.
func collapseWhitespace(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

func joinErrorMsgs(obj map[string]any) string {
	errList, ok := obj["errors"]
	if !ok {
		return ""
	}
	arr, ok := errList.([]any)
	if !ok {
		return ""
	}
	var msgs []string
	for _, e := range arr {
		if em, ok := e.(map[string]any); ok {
			if msg, ok := em["msg"].(string); ok && msg != "" {
				msgs = append(msgs, msg)
			}
		}
	}
	return strings.Join(msgs, "; ")
}

func determineOutcome(httpStatus any, logStatus string) string {
	if statusNum, ok := toFloat64(httpStatus); ok {
		if statusNum < 400 {
			return "success"
		}
		return "failure"
	}
	if logStatus == "failure" {
		return "failure"
	}
	return "success"
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case nil:
		return 0, false
	}
	return 0, false
}

func processEntry(entry map[string]any) *ReportRow {
	if str(entry, "process_type") != "request_completed" {
		return nil
	}
	payload, ok := entry["payload"].(map[string]any)
	if !ok {
		return nil
	}
	if str(payload, "method") != "POST" {
		return nil
	}

	url := str(payload, "url")
	httpStatus := payload["status"]
	logStatus := str(entry, "status")
	outcome := determineOutcome(httpStatus, logStatus)

	var errorMessage string
	if outcome == "failure" {
		errorMessage = extractErrorMessage(payload)
	}

	statusStr := ""
	if n, ok := toFloat64(httpStatus); ok {
		statusStr = fmt.Sprintf("%d", int(n))
	}

	return &ReportRow{
		EntityType:   classifyEntityType(url),
		EntityName:   extractEntityName(payload),
		Organization: extractOrganization(payload),
		URL:          url,
		HTTPStatus:   statusStr,
		Outcome:      outcome,
		ErrorMessage: errorMessage,
		Project:      extractProject(payload),
	}
}

func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
