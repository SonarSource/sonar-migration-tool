// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package analysis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	testProjectCreateURL  = "/api/projects/create"
	testGatesCreateURL    = "/api/qualitygates/create"
	testProfilesCreateURL = "/api/qualityprofiles/create"
	testGroupsCreateURL   = "/api/user_groups/create"
	testPortfoliosURL     = "/enterprises/portfolios"
	testOrgName           = "my-org"
	testProjectName       = "My Project"
	testCSVFilename       = "final_analysis_report.csv"
	errGenReport          = "GenerateReport: %v"
	errExpect1Row         = "expected 1 row, got %d"
	errEntityType         = "EntityType: got %q"
	errOutcome            = "Outcome: got %q"
	errGotWant            = "%s: got %q, want %q"
	errExpectComplete     = "expected COMPLETE, got %s"
)

func makeLogEntry(method, url string, statusCode any, logStatus string, data, jsonBody, response any) map[string]any {
	payload := map[string]any{
		"method":     method,
		"url":        url,
		"status":     statusCode,
		"created_ts": 1234567890.0,
	}
	if data != nil {
		payload["data"] = data
	}
	if jsonBody != nil {
		payload["json"] = jsonBody
	}
	if response != nil {
		payload["response"] = response
	}
	return map[string]any{
		"process_type": "request_completed",
		"created_ts":   1234567890.0,
		"status":       logStatus,
		"payload":      payload,
	}
}

func writeLog(t *testing.T, dir string, entries []map[string]any) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "requests.log"))
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()
	for _, e := range entries {
		data, _ := json.Marshal(e)
		f.Write(data)
		f.Write([]byte("\n"))
	}
}

// --- parseLogLine ---

func TestParseLogLineValid(t *testing.T) {
	result, ok := parseLogLine(`{"key": "value"}`)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if result["key"] != "value" {
		t.Errorf("expected value, got %v", result["key"])
	}
}

func TestParseLogLineEmpty(t *testing.T) {
	for _, line := range []string{"", "   "} {
		_, ok := parseLogLine(line)
		if ok {
			t.Errorf("expected ok=false for %q", line)
		}
	}
}

func TestParseLogLineInvalid(t *testing.T) {
	_, ok := parseLogLine("not json")
	if ok {
		t.Error("expected ok=false for invalid JSON")
	}
}

// --- classifyEntityType ---

func TestClassifyEntityTypeKnown(t *testing.T) {
	tests := []struct {
		url, want string
	}{
		{testProjectCreateURL, "Project"},
		{testGatesCreateURL, "Quality Gate"},
		{testProfilesCreateURL, "Quality Profile"},
		{testGroupsCreateURL, "Group"},
		{"/api/permissions/create_template", "Permission Template"},
		{testPortfoliosURL, "Portfolio"},
		{"/dop-translation/project-bindings", "Project Binding"},
		{"/api/rules/update", "Rule"},
	}
	for _, tt := range tests {
		got := classifyEntityType(tt.url)
		if got != tt.want {
			t.Errorf("classifyEntityType(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestClassifyEntityTypeUnknown(t *testing.T) {
	if classifyEntityType("/api/unknown/endpoint") != "Unknown" {
		t.Error("expected Unknown")
	}
}

func TestClassifyEntityTypeEmpty(t *testing.T) {
	if classifyEntityType("") != "Unknown" {
		t.Error("expected Unknown for empty")
	}
}

// --- extractEntityName ---

func TestExtractEntityNameFields(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"name field", map[string]any{"data": map[string]any{"name": testProjectName, "key": "proj-1"}}, testProjectName},
		{"project field", map[string]any{"data": map[string]any{"project": "proj-key"}}, "proj-key"},
		{"projectKey field", map[string]any{"json": map[string]any{"projectKey": "proj-key-2"}}, "proj-key-2"},
		{"gateName field", map[string]any{"data": map[string]any{"gateName": "My Gate"}}, "My Gate"},
		{"groupName field", map[string]any{"data": map[string]any{"groupName": "developers"}}, "developers"},
		{"key field", map[string]any{"data": map[string]any{"key": "some-key"}}, "some-key"},
		{"language field", map[string]any{"data": map[string]any{"language": "java"}}, "java"},
		{"no match", map[string]any{"data": map[string]any{"foo": "bar"}}, ""},
		{"empty payload", map[string]any{}, ""},
		{"params fallback", map[string]any{"params": map[string]any{"name": "from-params"}}, "from-params"},
	}
	for _, tt := range tests {
		got := extractEntityName(tt.payload)
		if got != tt.want {
			t.Errorf(errGotWant, tt.name, got, tt.want)
		}
	}
}

func TestExtractEntityNamePriority(t *testing.T) {
	payload := map[string]any{"data": map[string]any{"key": "key-val", "name": "name-val"}}
	if got := extractEntityName(payload); got != "name-val" {
		t.Errorf("priority: got %q, want name-val", got)
	}
}

// --- extractOrganization ---

func TestExtractOrganization(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"from data", map[string]any{"data": map[string]any{"organization": testOrgName}}, testOrgName},
		{"from json", map[string]any{"json": map[string]any{"organization": "json-org"}}, "json-org"},
		{"missing", map[string]any{"data": map[string]any{"name": "test"}}, ""},
		{"empty payload", map[string]any{}, ""},
	}
	for _, tt := range tests {
		got := extractOrganization(tt.payload)
		if got != tt.want {
			t.Errorf(errGotWant, tt.name, got, tt.want)
		}
	}
}

// --- extractErrorMessage ---

func TestExtractErrorMessage(t *testing.T) {
	errJSON := `{"errors": [{"msg": "Project already exists"}]}`
	multiErrJSON := `{"errors": [{"msg": "Error 1"}, {"msg": "Error 2"}]}`

	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"SQ error string", map[string]any{"response": errJSON}, "Project already exists"},
		{"multiple errors", map[string]any{"response": multiErrJSON}, "Error 1; Error 2"},
		{"no response", map[string]any{}, ""},
		// A response that is not SonarQube's {"errors":[...]} envelope is
		// summarized rather than dropped. Returning "" here left the
		// report's Error column blank for every failure that did not come
		// from SonarQube itself, so a gateway 403 served as an HTML page
		// showed a bare status and no reason at all.
		{"non-JSON response", map[string]any{"response": "Internal Server Error"}, "Internal Server Error"},
		{"HTML error page", map[string]any{"response": "<html><head><TITLE>ERROR: The request could not be satisfied</TITLE></head>" +
			"<body><H1>403 ERROR</H1><H2>The request could not be satisfied.</H2>Request blocked.</body></html>"},
			"ERROR: The request could not be satisfied: 403 ERROR: The request could not be satisfied."},
		{"content field", map[string]any{"content": `{"errors": [{"msg": "From content"}]}`}, "From content"},
		{"response as dict", map[string]any{"response": map[string]any{"errors": []any{map[string]any{"msg": "Already a dict"}}}}, "Already a dict"},
	}
	for _, tt := range tests {
		got := extractErrorMessage(tt.payload)
		if got != tt.want {
			t.Errorf(errGotWant, tt.name, got, tt.want)
		}
	}
}

// --- determineOutcome ---

func TestDetermineOutcome(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus any
		logStatus  string
		want       string
	}{
		{"success 200", float64(200), "success", "success"},
		{"failure 400", float64(400), "failure", "failure"},
		{"failure 500", float64(500), "", "failure"},
		{"nil status log failure", nil, "failure", "failure"},
		{"nil status log success", nil, "success", "success"},
		{"nil status empty log", nil, "", "success"},
	}
	for _, tt := range tests {
		got := determineOutcome(tt.httpStatus, tt.logStatus)
		if got != tt.want {
			t.Errorf(errGotWant, tt.name, got, tt.want)
		}
	}
}

// --- GenerateReport integration tests ---

func TestGenerateReportBasicSuccess(t *testing.T) {
	dir := t.TempDir()
	entry := makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
		map[string]any{"name": testProjectName, "organization": testOrgName}, nil, nil)
	writeLog(t, dir, []map[string]any{entry})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 1 {
		t.Fatalf(errExpect1Row, len(rows))
	}
	r := rows[0]
	if r.EntityType != "Project" {
		t.Errorf(errEntityType,r.EntityType)
	}
	if r.EntityName != testProjectName {
		t.Errorf("EntityName: got %q", r.EntityName)
	}
	if r.Organization != testOrgName {
		t.Errorf("Organization: got %q", r.Organization)
	}
	if r.URL != testProjectCreateURL {
		t.Errorf("URL: got %q", r.URL)
	}
	if r.HTTPStatus != "200" {
		t.Errorf("HTTPStatus: got %q", r.HTTPStatus)
	}
	if r.Outcome != "success" {
		t.Errorf(errOutcome,r.Outcome)
	}
	if r.ErrorMessage != "" {
		t.Errorf("ErrorMessage: got %q", r.ErrorMessage)
	}
}

func TestGenerateReportFailure(t *testing.T) {
	dir := t.TempDir()
	errResp := `{"errors": [{"msg": "Project already exists"}]}`
	entry := makeLogEntry("POST", testProjectCreateURL, float64(400), "failure",
		map[string]any{"name": "Dup Project", "organization": testOrgName}, nil, errResp)
	writeLog(t, dir, []map[string]any{entry})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 1 {
		t.Fatalf(errExpect1Row, len(rows))
	}
	if rows[0].Outcome != "failure" {
		t.Errorf(errOutcome,rows[0].Outcome)
	}
	if rows[0].HTTPStatus != "400" {
		t.Errorf("HTTPStatus: got %q", rows[0].HTTPStatus)
	}
	if rows[0].ErrorMessage != "Project already exists" {
		t.Errorf("ErrorMessage: got %q", rows[0].ErrorMessage)
	}
}

func TestGenerateReportFiltersGET(t *testing.T) {
	dir := t.TempDir()
	entries := []map[string]any{
		makeLogEntry("GET", "/api/server/version", float64(200), "success", nil, nil, nil),
		makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
			map[string]any{"name": "Test"}, nil, nil),
	}
	writeLog(t, dir, entries)

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (POST only), got %d", len(rows))
	}
	if rows[0].EntityType != "Project" {
		t.Errorf(errEntityType,rows[0].EntityType)
	}
}

func TestGenerateReportFiltersRequestStarted(t *testing.T) {
	dir := t.TempDir()
	started := makeLogEntry("POST", testProjectCreateURL, float64(200), "success", nil, nil, nil)
	started["process_type"] = "request_started"
	completed := makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
		map[string]any{"name": "Test"}, nil, nil)
	writeLog(t, dir, []map[string]any{started, completed})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 1 {
		t.Fatalf(errExpect1Row, len(rows))
	}
}

func TestGenerateReportCSVCreated(t *testing.T) {
	dir := t.TempDir()
	entry := makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
		map[string]any{"name": "Test"}, nil, nil)
	writeLog(t, dir, []map[string]any{entry})

	GenerateReport(dir)

	csvPath := filepath.Join(dir, testCSVFilename)
	if _, err := os.Stat(csvPath); os.IsNotExist(err) {
		t.Error("expected CSV file to be created")
	}
}

func TestGenerateReportToCustomDir(t *testing.T) {
	runDir := t.TempDir()
	outDir := t.TempDir()

	entry := makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
		map[string]any{"name": "Test"}, nil, nil)
	writeLog(t, runDir, []map[string]any{entry})

	GenerateReportTo(runDir, outDir)

	if _, err := os.Stat(filepath.Join(outDir, testCSVFilename)); os.IsNotExist(err) {
		t.Error("expected CSV in output dir")
	}
	if _, err := os.Stat(filepath.Join(runDir, testCSVFilename)); !os.IsNotExist(err) {
		t.Error("did not expect CSV in run dir")
	}
}

func TestGenerateReportEmptyLog(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, []map[string]any{})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
	if _, err := os.Stat(filepath.Join(dir, testCSVFilename)); !os.IsNotExist(err) {
		t.Error("expected no CSV for empty log")
	}
}

func TestGenerateReportNoLogFile(t *testing.T) {
	dir := t.TempDir()
	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if rows != nil {
		t.Errorf("expected nil rows, got %d", len(rows))
	}
}

func TestGenerateReportMultipleEntityTypes(t *testing.T) {
	dir := t.TempDir()
	entries := []map[string]any{
		makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
			map[string]any{"name": "Proj1", "organization": "org1"}, nil, nil),
		makeLogEntry("POST", testGatesCreateURL, float64(200), "success",
			map[string]any{"name": "Gate1", "organization": "org1"}, nil, nil),
		makeLogEntry("POST", testGroupsCreateURL, float64(200), "success",
			map[string]any{"name": "Group1", "organization": "org1"}, nil, nil),
		makeLogEntry("POST", testProfilesCreateURL, float64(200), "success",
			map[string]any{"name": "Profile1", "organization": "org1"}, nil, nil),
	}
	writeLog(t, dir, entries)

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}
	types := map[string]bool{}
	for _, r := range rows {
		types[r.EntityType] = true
	}
	for _, expected := range []string{"Project", "Quality Gate", "Group", "Quality Profile"} {
		if !types[expected] {
			t.Errorf("missing entity type: %s", expected)
		}
	}
}

func TestGenerateReportFailureWithLogStatus(t *testing.T) {
	dir := t.TempDir()
	entry := makeLogEntry("POST", testProjectCreateURL, nil, "failure",
		map[string]any{"name": "Failed Project"}, nil, nil)
	entry["payload"].(map[string]any)["status"] = nil
	writeLog(t, dir, []map[string]any{entry})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 1 {
		t.Fatalf(errExpect1Row, len(rows))
	}
	if rows[0].Outcome != "failure" {
		t.Errorf(errOutcome,rows[0].Outcome)
	}
	if rows[0].HTTPStatus != "" {
		t.Errorf("HTTPStatus: got %q, want empty", rows[0].HTTPStatus)
	}
}

func TestGenerateReportCSVColumns(t *testing.T) {
	dir := t.TempDir()
	entry := makeLogEntry("POST", testProjectCreateURL, float64(200), "success",
		map[string]any{"name": "Test", "organization": "org1"}, nil, nil)
	writeLog(t, dir, []map[string]any{entry})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	r := rows[0]
	// Verify all fields are populated (no zero struct).
	if r.EntityType == "" || r.URL == "" || r.Outcome == "" {
		t.Error("expected non-empty core fields")
	}
}

func TestGenerateReportJSONBody(t *testing.T) {
	dir := t.TempDir()
	entry := makeLogEntry("POST", testPortfoliosURL, float64(200), "success",
		nil, map[string]any{"name": "My Portfolio", "organization": "org1"}, nil)
	writeLog(t, dir, []map[string]any{entry})

	rows, err := GenerateReport(dir)
	if err != nil {
		t.Fatalf(errGenReport, err)
	}
	if len(rows) != 1 {
		t.Fatalf(errExpect1Row, len(rows))
	}
	if rows[0].EntityName != "My Portfolio" {
		t.Errorf("EntityName: got %q", rows[0].EntityName)
	}
	if rows[0].Organization != "org1" {
		t.Errorf("Organization: got %q", rows[0].Organization)
	}
	if rows[0].EntityType != "Portfolio" {
		t.Errorf(errEntityType,rows[0].EntityType)
	}
}

// --- URL Entity Map Coverage ---

func TestURLEntityMapCoverage(t *testing.T) {
	expectedURLs := []string{
		testProjectCreateURL,
		testGatesCreateURL,
		"/api/qualitygates/create_condition",
		"/api/qualitygates/select",
		"/api/qualitygates/set_as_default",
		testProfilesCreateURL,
		"/api/qualityprofiles/restore",
		"/api/qualityprofiles/set_default",
		"/api/qualityprofiles/add_project",
		"/api/qualityprofiles/add_group",
		"/api/qualityprofiles/change_parent",
		testGroupsCreateURL,
		"/api/user_groups/add_user",
		"/api/permissions/create_template",
		"/api/permissions/set_default_template",
		"/api/permissions/add_group_to_template",
		"/api/permissions/add_group",
		"/api/settings/set",
		"/api/rules/update",
		"/api/project_branches/rename",
		"/api/project_tags/set",
		"/dop-translation/project-bindings",
		testPortfoliosURL,
	}
	for _, url := range expectedURLs {
		if _, ok := urlEntityMap[url]; !ok {
			t.Errorf("missing URL mapping: %s", url)
		}
		if classifyEntityType(url) == "Unknown" {
			t.Errorf("URL classified as Unknown: %s", url)
		}
	}
}

// summarizeNonJSONError's whole reason to exist is the body that is not
// JSON — a gateway or proxy error page, which is exactly the input that
// carries non-ASCII text. Capping it with a byte slice cuts inside a
// multi-byte rune whenever the boundary falls there, and the broken rune
// flows on into final_analysis_report.csv and the report's Error column.
func TestSummarizeNonJSONErrorCutsOnARuneBoundary(t *testing.T) {
	// Walk the cut point through every offset within a 2- and a 3-byte
	// rune, so the test does not pass by landing on a clean boundary.
	for _, r := range []string{"é", "€"} {
		for _, prefix := range []string{"", "x", "xx", "xxx"} {
			body := prefix + strings.Repeat(r, 300)
			got := summarizeNonJSONError(body)
			if !utf8.ValidString(got) {
				t.Errorf("rune %q prefix %q: summary is not valid UTF-8: %q", r, prefix, got)
			}
		}
	}
}
