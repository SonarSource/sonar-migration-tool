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
	req.Header.Set("Content-Type", contentType)

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
		case <-time.After(5 * time.Second):
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
	h.Set("Content-Type", "application/zip")
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
