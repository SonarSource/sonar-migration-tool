package summary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// configFailure describes a failed request against a configuration endpoint
// (e.g. adding a gate condition, restoring a profile backup, setting a parent).
// These represent partial migrations: the parent entity exists on SQC, but a
// follow-up configuration step did not complete successfully.
type configFailure struct {
	Section      string // "Quality Gates" or "Quality Profiles"
	Operation    string // human-readable label for the failed operation
	EntityName   string // gate/profile name (best-effort, may be empty)
	Organization string
	Language     string // profile language, when applicable
	Detail       string // extra context (e.g., metric for conditions)
	Error        string
}

// configFailureMatcher maps an endpoint suffix to section + operation label.
type configFailureMatcher struct {
	URLSuffix string
	Section   string
	Operation string
}

var configFailureMatchers = []configFailureMatcher{
	{URLSuffix: "/api/qualitygates/create_condition", Section: "Quality Gates", Operation: "Add condition"},
	{URLSuffix: "/api/qualitygates/set_as_default", Section: "Quality Gates", Operation: "Set as default"},
	{URLSuffix: "/api/qualitygates/select", Section: "Quality Gates", Operation: "Assign to project"},
	{URLSuffix: "/api/qualityprofiles/restore", Section: "Quality Profiles", Operation: "Restore rules from backup"},
	{URLSuffix: "/api/qualityprofiles/change_parent", Section: "Quality Profiles", Operation: "Set parent profile"},
	{URLSuffix: "/api/qualityprofiles/set_default", Section: "Quality Profiles", Operation: "Set as default"},
	{URLSuffix: "/api/qualityprofiles/add_project", Section: "Quality Profiles", Operation: "Assign to project"},
	{URLSuffix: "/api/qualityprofiles/add_group", Section: "Quality Profiles", Operation: "Grant group permission"},
}

// collectConfigFailures re-parses requests.log and returns failures from
// configuration endpoints (i.e. follow-up steps after the initial create).
// These describe partial migrations of Quality Gates / Quality Profiles.
func collectConfigFailures(runDir string) ([]configFailure, error) {
	logPath := filepath.Join(runDir, "requests.log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var failures []configFailure
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		entry, ok := parseConfigLogLine(scanner.Text())
		if !ok {
			continue
		}
		if cf, ok := classifyConfigFailure(entry); ok {
			failures = append(failures, cf)
		}
	}
	return failures, scanner.Err()
}

func parseConfigLogLine(line string) (map[string]any, bool) {
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

// classifyConfigFailure returns a configFailure if the log entry represents
// a failed POST to a known configuration endpoint.
func classifyConfigFailure(entry map[string]any) (configFailure, bool) {
	if asString(entry["process_type"]) != "request_completed" {
		return configFailure{}, false
	}
	payload, ok := entry["payload"].(map[string]any)
	if !ok {
		return configFailure{}, false
	}
	if asString(payload["method"]) != "POST" {
		return configFailure{}, false
	}

	url := asString(payload["url"])
	matcher, ok := matchConfigEndpoint(url)
	if !ok {
		return configFailure{}, false
	}

	status := payload["status"]
	logStatus := asString(entry["status"])
	if !isFailure(status, logStatus) {
		return configFailure{}, false
	}

	body := configRequestBody(payload)
	cf := configFailure{
		Section:      matcher.Section,
		Operation:    matcher.Operation,
		Organization: asString(body["organization"]),
		Language:     asString(body["language"]),
		Error:        extractFailureError(payload),
	}
	cf.EntityName, cf.Detail = entityNameAndDetail(matcher, body)
	return cf, true
}

func matchConfigEndpoint(url string) (configFailureMatcher, bool) {
	for _, m := range configFailureMatchers {
		if strings.HasSuffix(url, m.URLSuffix) || url == m.URLSuffix {
			return m, true
		}
	}
	return configFailureMatcher{}, false
}

// entityNameAndDetail returns (entity name, extra detail) extracted from the
// request body based on the endpoint family.
func entityNameAndDetail(m configFailureMatcher, body map[string]any) (string, string) {
	switch {
	case strings.HasSuffix(m.URLSuffix, "/create_condition"):
		// Body contains gateId and metric; we cannot resolve gateId -> name
		// without a join, so prefer leaving the entity name empty and put
		// the metric into the detail column.
		metric := asString(body["metric"])
		op := asString(body["op"])
		errVal := asString(body["error"])
		detail := metric
		if op != "" {
			detail = fmt.Sprintf("%s %s %s", metric, op, errVal)
		}
		return "", strings.TrimSpace(detail)
	case strings.HasSuffix(m.URLSuffix, "/qualitygates/set_as_default"),
		strings.HasSuffix(m.URLSuffix, "/qualitygates/select"):
		return asString(body["gateName"]), ""
	case strings.HasSuffix(m.URLSuffix, "/qualityprofiles/restore"):
		// Body has backup XML only; name is inside the XML.
		return "", ""
	case strings.HasSuffix(m.URLSuffix, "/qualityprofiles/change_parent"),
		strings.HasSuffix(m.URLSuffix, "/qualityprofiles/set_default"),
		strings.HasSuffix(m.URLSuffix, "/qualityprofiles/add_project"),
		strings.HasSuffix(m.URLSuffix, "/qualityprofiles/add_group"):
		return asString(body["qualityProfile"]), ""
	}
	return "", ""
}

// configRequestBody extracts the request body map from a payload, supporting
// the same shapes as analysis.getRequestBody (data/json/params).
func configRequestBody(payload map[string]any) map[string]any {
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
	return map[string]any{}
}

func isFailure(status any, logStatus string) bool {
	if n, ok := numericStatus(status); ok {
		return n >= 400
	}
	return logStatus == "failure"
}

func numericStatus(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func extractFailureError(payload map[string]any) string {
	val := payload["response"]
	if val == nil {
		val = payload["content"]
	}
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case map[string]any:
		return joinErrorMessages(v)
	case string:
		if v == "" {
			return ""
		}
		var parsed map[string]any
		if json.Unmarshal([]byte(v), &parsed) == nil {
			if msg := joinErrorMessages(parsed); msg != "" {
				return msg
			}
		}
		return v
	}
	return ""
}

func joinErrorMessages(obj map[string]any) string {
	errs, ok := obj["errors"].([]any)
	if !ok {
		return ""
	}
	var msgs []string
	for _, e := range errs {
		if m, ok := e.(map[string]any); ok {
			if s := asString(m["msg"]); s != "" {
				msgs = append(msgs, s)
			}
		}
	}
	return strings.Join(msgs, "; ")
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// collectPartial returns partial migration entries for a section. A partial
// entry surfaces a follow-up configuration failure that prevented the entity
// from being migrated 100% identically to the source.
//
// When a config failure cannot be attributed to a specific successful entity,
// it is still reported (with an empty name) so that the issue is visible.
func collectPartial(def sectionDef, failures []configFailure, succeeded []EntityItem) []EntityItem {
	if len(failures) == 0 {
		return nil
	}
	succeededByName := make(map[string]int)
	for i, item := range succeeded {
		if item.Name != "" {
			succeededByName[item.Name] = i
		}
	}

	var partial []EntityItem
	merged := make(map[string]int) // partial index by entity name
	for _, cf := range failures {
		if cf.Section != def.Name {
			continue
		}
		issue := cf.Operation
		if cf.Detail != "" {
			issue = cf.Operation + ": " + cf.Detail
		}
		if cf.Error != "" {
			issue = issue + " — " + cf.Error
		}

		if cf.EntityName != "" {
			if idx, ok := merged[cf.EntityName]; ok {
				partial[idx].Issues = append(partial[idx].Issues, issue)
				continue
			}
			item := EntityItem{
				Name:         cf.EntityName,
				Language:     cf.Language,
				Organization: cf.Organization,
				Issues:       []string{issue},
			}
			// Carry over detail (cloud_id) from the matching success entry, if any.
			if sIdx, ok := succeededByName[cf.EntityName]; ok {
				item.Detail = succeeded[sIdx].Detail
			}
			merged[cf.EntityName] = len(partial)
			partial = append(partial, item)
			continue
		}
		// Unattributed failure — append as its own row.
		partial = append(partial, EntityItem{
			Language:     cf.Language,
			Organization: cf.Organization,
			ErrorMessage: cf.Error,
			Issues:       []string{issue},
		})
	}
	return partial
}
