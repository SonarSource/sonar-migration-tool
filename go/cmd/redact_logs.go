// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/redact"
	"github.com/spf13/cobra"
)

const (
	flagRedactLogs = "redact_logs"

	redactLogsUsage = "Mask personal data (users, e-mails, logins, authors, assignees) and source code in log output, including --debug payload dumps (default true). " +
		"Takes an explicit value: --redact_logs false or --redact_logs=false disables it. Also settable as \"redact_logs\" in the config file; the flag wins. " +
		"Only logs are redacted, never extracted or migrated data (#713)."
)

// parseRedactLogsValue validates the --redact_logs / "redact_logs" value.
func parseRedactLogsValue(raw string) (bool, error) {
	v, ok := migrate.ParseFlexibleBool(raw)
	if !ok {
		return false, fmt.Errorf("invalid --redact_logs value %q: expected true or false", raw)
	}
	return v, nil
}

// redactLogsFromConfigFile reads the optional top-level "redact_logs" field
// (JSON bool or string) of the config file at path. It returns set=false when
// the file is unreadable, not JSON, or lacks the field: the command's own
// config loader reports those problems, so this stays silent about them.
func redactLogsFromConfigFile(path string) (value, set bool, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return false, false, nil
	}
	var shape struct {
		RedactLogs *migrate.FlexibleBool `json:"redact_logs"`
	}
	if jsonErr := json.Unmarshal(data, &shape); jsonErr != nil {
		if shape.RedactLogs == nil {
			return false, false, nil
		}
		return false, false, fmt.Errorf("invalid redact_logs value in config file %s: %w", path, jsonErr)
	}
	if shape.RedactLogs == nil || !shape.RedactLogs.Set {
		return false, false, nil
	}
	return shape.RedactLogs.Value, true, nil
}

// applyRedactLogs resolves the process-global log redaction switch with the
// precedence explicit CLI flag > config file > default (on). It runs from the
// root PersistentPreRunE, after the logger is installed; the redacting
// handler reads redact.Enabled() at log time, so the late switch is honoured.
func applyRedactLogs(cmd *cobra.Command) error {
	if f := cmd.Flags().Lookup(flagRedactLogs); f != nil && f.Changed {
		v, err := parseRedactLogsValue(f.Value.String())
		if err != nil {
			return err
		}
		redact.SetEnabled(v)
		return nil
	}
	redact.SetEnabled(true)
	configFile, _ := cmd.Flags().GetString("config")
	if configFile == "" {
		return nil
	}
	v, set, err := redactLogsFromConfigFile(configFile)
	if err != nil {
		return err
	}
	if set {
		redact.SetEnabled(v)
	}
	return nil
}
