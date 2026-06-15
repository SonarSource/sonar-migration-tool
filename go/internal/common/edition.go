// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// Edition represents a SonarQube edition.
type Edition string

const (
	EditionCommunity  Edition = "community"
	EditionDeveloper  Edition = "developer"
	EditionEnterprise Edition = "enterprise"
	EditionDatacenter Edition = "datacenter"
)

// AllEditions is the full set of Server editions (not including Cloud teams/free).
var AllEditions = []Edition{EditionCommunity, EditionDeveloper, EditionEnterprise, EditionDatacenter}

// ParseEdition normalises the string from /api/system/info into an Edition.
// It checks both the top-level "edition" key and the nested "System.Edition" path.
func ParseEdition(raw json.RawMessage) Edition {
	var info map[string]json.RawMessage
	if err := json.Unmarshal(raw, &info); err != nil {
		return EditionCommunity
	}

	// Try top-level "edition" first.
	if ed, ok := parseEditionString(info["edition"]); ok {
		return ed
	}

	// Try nested "System.Edition" (newer API format).
	if sysRaw, ok := info["System"]; ok {
		var sys map[string]json.RawMessage
		if json.Unmarshal(sysRaw, &sys) == nil {
			if ed, ok := parseEditionString(sys["Edition"]); ok {
				return ed
			}
		}
	}

	return EditionCommunity
}

// parseEditionString extracts and normalises an edition from a raw
// JSON string into one of the four enum values.
//
// Normalisation contract (#395): the SonarQube Server api/system/info
// payload carries the edition in display form — e.g. "Data Center"
// with a space, or a future "Data Center Edition" — which used to
// fall through the exact-match switch and silently downgrade real
// Data Center servers to Community. We now:
//
//  1. lowercase + strip every space, so "Data Center" → "datacenter"
//     and "  DATA  CENTER  " → "datacenter";
//  2. accept any prefix match against the known enums, so a future
//     "Data Center Edition" → "datacenter" too. None of the four
//     enum values is a prefix of another, so prefix ordering is
//     unambiguous.
//
// Truly-unknown non-empty values are still treated as "no match"
// (caller falls back to EditionCommunity) but we log one slog.Warn
// so the silent-downgrade no longer happens invisibly.
func parseEditionString(raw json.RawMessage) (Edition, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	if s == "" {
		return "", false
	}
	normalised := strings.ReplaceAll(strings.ToLower(s), " ", "")
	for _, ed := range AllEditions {
		if strings.HasPrefix(normalised, string(ed)) {
			return ed, true
		}
	}
	slog.Warn("unrecognised SonarQube edition value — falling back to community",
		"raw_value", s)
	return "", false
}
