// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
)

// AcquireSem acquires a semaphore slot, respecting context cancellation.
func AcquireSem(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EnrichRaw merges additional key-value pairs into a raw JSON object.
func EnrichRaw(raw json.RawMessage, metadata map[string]any) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		obj = make(map[string]json.RawMessage)
	}
	for k, v := range metadata {
		b, _ := json.Marshal(v)
		obj[k] = b
	}
	result, _ := json.Marshal(obj)
	return result
}

// EnrichAll applies EnrichRaw to every item in a slice.
func EnrichAll(items []json.RawMessage, metadata map[string]any) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		out[i] = EnrichRaw(item, metadata)
	}
	return out
}

// ExtractField extracts a string value from a json.RawMessage by key.
func ExtractField(raw json.RawMessage, key string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	val, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(val, &s); err != nil {
		return strings.Trim(string(val), "\"")
	}
	return s
}

// ExtractBool extracts a boolean value from a json.RawMessage by key.
func ExtractBool(raw json.RawMessage, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	val, ok := obj[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(val, &b); err != nil {
		return false
	}
	return b
}

// FirstNonEmpty returns the first non-empty string in values, letting a
// more-specific setting (e.g. a config file's source/target block) win over
// a less-specific one (e.g. its top-level default) without needing a
// tri-state type — an empty string always means "not set" for these
// string-valued config fields. Returns "" when every value is empty.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ResolveDeprecatedProjectKey combines the deprecated "project_key" value
// with its replacement "project_key_regexp" into one effective pattern
// (#592): newVal wins when both are non-empty. Logs a deprecation WARN
// when legacyVal is used at all, and a separate WARN when both are set,
// naming which one takes precedence. Shared by every config-file loader
// (extract, migrate, transfer) and by the CLI flag resolution in
// go/cmd/project_key_flags.go, so the wording and precedence rule stay
// identical everywhere this parameter can be set. Mirrors
// warnIfConcurrencyDeprecated (go/cmd/transfer.go) in taking no logger
// param and using slog.Default() directly.
func ResolveDeprecatedProjectKey(legacyVal, newVal string) string {
	if legacyVal != "" {
		slog.Default().Warn("project_key is deprecated; use project_key_regexp instead", "value", legacyVal)
	}
	if legacyVal != "" && newVal != "" {
		slog.Default().Warn("both project_key and project_key_regexp are set; project_key_regexp takes precedence",
			"project_key", legacyVal, "project_key_regexp", newVal)
	}
	if newVal != "" {
		return newVal
	}
	return legacyVal
}

// Expansion defines a set of values for cross-product iteration.
type Expansion struct {
	Key    string
	Values []string
}

// ExpandCombinations returns all combinations from a list of expansions.
func ExpandCombinations(expansions []Expansion) []map[string]string {
	if len(expansions) == 0 {
		return []map[string]string{{}}
	}
	first := expansions[0]
	rest := ExpandCombinations(expansions[1:])
	var result []map[string]string
	for _, val := range first.Values {
		for _, combo := range rest {
			m := make(map[string]string, len(combo)+1)
			for k, v := range combo {
				m[k] = v
			}
			m[first.Key] = val
			result = append(result, m)
		}
	}
	return result
}
