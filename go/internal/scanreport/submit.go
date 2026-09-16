// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package scanreport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const headerContentType = "Content-Type"

// CEPollInterval is the delay between api/ce/task polls in PollCETask.
// Doubled from the original 5s (#571) to reduce the polling pressure a
// migration run puts on SonarQube Cloud. A package-level var rather than a
// const so tests can drive it down to near-zero instead of paying the real
// wall-clock cost of every CE poll in the suite.
var CEPollInterval = 10 * time.Second

// SubmitConfig holds the parameters for submitting a scanner report.
type SubmitConfig struct {
	CloudURL       string // e.g. "https://sonarcloud.io/"
	ProjectKey     string
	OrgKey         string
	BranchName     string
	ProjectVersion string
	// IsMain marks the project's main/default branch. For the main branch we
	// must NOT send branch characteristics — the first analysis establishes
	// the main branch. Sending branch=<name>&branchType=LONG for a brand-new
	// project (which has no main analysis to anchor to) makes the CE reject
	// the report. Mirrors CloudVoyager, which omits the characteristic for
	// the main branch.
	IsMain bool
}

// SubmitResult holds the response from a CE submission.
type SubmitResult struct {
	TaskID string
}

// AnalysisConfig holds parameters for the SonarCloud "Create analysis" handshake.
type AnalysisConfig struct {
	APIURL         string // v2 API host, e.g. "https://api.sonarcloud.io/"
	OrgKey         string
	ProjectKey     string
	ProjectVersion string
	BranchName     string
	TargetBranch   string // reference/main branch on the target
	// BranchType requests an explicit branch type ("long"/"short"). For
	// migration we send "long" so every migrated branch is a long-lived branch
	// that keeps its full issue history (SonarQube Server branches are all
	// long-lived). Empty lets the server classify by the long-lived-branch regex.
	BranchType string
}

// AnalysisResult holds the response of the create-analysis handshake.
type AnalysisResult struct {
	AnalysisUUID        string `json:"id"`
	BranchID            string `json:"branchId"`
	BranchType          string `json:"branchType"`
	ReferenceBranchName string `json:"referenceBranchName"`
}

// PreCreateAnalysis performs the SonarCloud "Create analysis" handshake that a
// real scanner runs before uploading the report: POST {APIURL}/analysis/analyses.
// It creates/anchors the branch row server-side and returns an analysis id that
// must be stamped into the report metadata (analysis_uuid, field 19). Without
// this handshake a non-main branch report is accepted by the CE (task SUCCESS)
// but no branch is ever created. Captured from the real scanner's traffic; the
// client must inject the Bearer token (use the SonarCloud API client's HTTPClient).
func PreCreateAnalysis(ctx context.Context, client *http.Client, cfg AnalysisConfig) (*AnalysisResult, error) {
	version := cfg.ProjectVersion
	if version == "" {
		version = "not provided"
	}
	bodyMap := map[string]string{
		"organizationKey": cfg.OrgKey,
		"projectKey":      cfg.ProjectKey,
		"projectVersion":  version,
	}
	if cfg.BranchName != "" {
		bodyMap["branchName"] = cfg.BranchName
	}
	if cfg.TargetBranch != "" {
		bodyMap["targetBranchName"] = cfg.TargetBranch
	}
	if cfg.BranchType != "" {
		bodyMap["branchType"] = cfg.BranchType
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}

	// {APIURL}/analysis/analyses — NOTE: no "/api/v2" prefix on the api host.
	submitURL := strings.TrimRight(cfg.APIURL, "/") + "/analysis/analyses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(headerContentType, "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST analysis/analyses: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading create-analysis response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("create-analysis HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 300))
	}

	var result AnalysisResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing create-analysis response: %w", err)
	}
	if result.AnalysisUUID == "" {
		return nil, fmt.Errorf("create-analysis: no analysis id in response: %s", truncateStr(string(respBody), 200))
	}
	return &result, nil
}

// SubmitReport uploads a scanner report ZIP to the SonarCloud Compute Engine.
func SubmitReport(ctx context.Context, client *http.Client, cfg SubmitConfig, reportZIP []byte) (*SubmitResult, error) {
	body, contentType, err := buildMultipartForm(cfg, reportZIP)
	if err != nil {
		return nil, fmt.Errorf("building form: %w", err)
	}

	submitURL := strings.TrimRight(cfg.CloudURL, "/") + "/api/ce/submit"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set(headerContentType, contentType)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST ce/submit: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("CE submit HTTP %d: %s", resp.StatusCode, truncateStr(string(respBody), 500))
	}

	taskID := parseTaskID(respBody)
	if taskID == "" {
		return nil, fmt.Errorf("CE submit: no taskId in response: %s", truncateStr(string(respBody), 200))
	}

	return &SubmitResult{TaskID: taskID}, nil
}

// PollCETask polls the CE activity endpoint until the task reaches a terminal
// state (SUCCESS, FAILED, CANCELED) or the context is canceled.
func PollCETask(ctx context.Context, client *http.Client, cloudURL, taskID string, logger *slog.Logger) error {
	activityURL := strings.TrimRight(cloudURL, "/") + "/api/ce/task"
	// The default response already includes errorType/errorMessage on FAILED.
	// Do NOT request additionalFields=stacktrace — SonarCloud rejects it (only
	// scannerContext|warnings are valid) and the 400 would break polling.
	params := url.Values{"id": {taskID}}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(CEPollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, activityURL+"?"+params.Encode(), nil)
		if err != nil {
			return err
		}

		resp, err := client.Do(req)
		if err != nil {
			logger.Warn("CE poll error, retrying", "err", err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			logger.Warn("CE poll HTTP error", "status", resp.StatusCode, "body", truncateStr(string(body), 200))
			continue
		}

		status := extractJSONField(body, "task", "status")
		switch status {
		case "SUCCESS":
			return nil
		case "FAILED":
			errMsg := extractJSONField(body, "task", "errorMessage")
			errType := extractJSONField(body, "task", "errorType")
			errStack := extractJSONField(body, "task", "errorStacktrace")
			logger.Warn("CE task failed",
				"taskId", taskID,
				"errorType", errType,
				"errorMessage", errMsg,
				"stacktrace", truncateStr(errStack, 2000),
			)
			if errType != "" {
				return fmt.Errorf("CE task %s failed [%s]: %s", taskID, errType, errMsg)
			}
			return fmt.Errorf("CE task %s failed: %s", taskID, errMsg)
		case "CANCELED":
			return fmt.Errorf("CE task %s was canceled", taskID)
		default:
			logger.Debug("CE task polling", "taskId", taskID, "status", status)
		}
	}
}

func buildMultipartForm(cfg SubmitConfig, reportZIP []byte) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// report file
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="report"; filename="scanner-report.zip"`)
	h.Set(headerContentType, "application/zip")
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(reportZIP); err != nil {
		return nil, "", err
	}

	if err := w.WriteField("projectKey", cfg.ProjectKey); err != nil {
		return nil, "", err
	}
	if cfg.OrgKey != "" {
		if err := w.WriteField("organization", cfg.OrgKey); err != nil {
			return nil, "", err
		}
	}

	// Omit branch characteristics for the main branch (mirrors CloudVoyager).
	// The first analysis of a new project must register as its main branch;
	// declaring it as a named LONG branch makes the CE reject the report.
	if !cfg.IsMain && cfg.BranchName != "" {
		if err := w.WriteField("characteristic", "branch="+cfg.BranchName); err != nil {
			return nil, "", err
		}
		if err := w.WriteField("characteristic", "branchType=LONG"); err != nil {
			return nil, "", err
		}
	}

	version := cfg.ProjectVersion
	if version == "" {
		version = "1.0.0"
	}
	props := strings.Join([]string{
		"sonar.projectKey=" + cfg.ProjectKey,
		"sonar.organization=" + cfg.OrgKey,
		"sonar.projectVersion=" + version,
		"sonar.sourceEncoding=UTF-8",
	}, "\n")
	if err := w.WriteField("properties", props); err != nil {
		return nil, "", err
	}

	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

func parseTaskID(body []byte) string {
	// Try { "taskId": "..." }
	var simple struct {
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(body, &simple) == nil && simple.TaskID != "" {
		return simple.TaskID
	}

	// Try { "task": { "id": "..." } }
	var nested struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if json.Unmarshal(body, &nested) == nil && nested.Task.ID != "" {
		return nested.Task.ID
	}

	return ""
}

// extractJSONField extracts a nested field like extractJSONField(body, "task", "status").
func extractJSONField(body []byte, keys ...string) string {
	var current json.RawMessage = body
	for _, key := range keys {
		var obj map[string]json.RawMessage
		if json.Unmarshal(current, &obj) != nil {
			return ""
		}
		val, ok := obj[key]
		if !ok {
			return ""
		}
		current = val
	}
	var s string
	if json.Unmarshal(current, &s) == nil {
		return s
	}
	return strings.Trim(string(current), `"`)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
