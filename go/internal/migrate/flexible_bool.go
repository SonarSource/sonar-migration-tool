// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FlexibleBool is a boolean that accepts the common natural-language
// aliases when parsed from JSON config files: true/false, on/off,
// yes/no, 1/0 — all case-insensitive. Issue #299.
//
// The zero value is (false, false): "absent". Distinguishing absent
// from explicit-false lets the merge logic preserve config file
// defaults that the user didn't set.
//
// Wire it up in a config struct via a *FlexibleBool pointer — nil
// means "the field was not present in the JSON".
type FlexibleBool struct {
	Set   bool // true when the JSON carried a value
	Value bool // the parsed boolean
}

// UnmarshalJSON accepts JSON booleans, JSON numbers (0/1), and JSON
// strings carrying one of the supported aliases. Anything else is
// rejected with a descriptive error so a typo in the config file
// surfaces at parse time rather than silently defaulting.
func (f *FlexibleBool) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		// JSON null: same as absent.
		return nil
	}
	// Bare JSON boolean.
	if raw == "true" {
		*f = FlexibleBool{Set: true, Value: true}
		return nil
	}
	if raw == "false" {
		*f = FlexibleBool{Set: true, Value: false}
		return nil
	}
	// Numeric form: 0 / 1.
	if raw == "1" {
		*f = FlexibleBool{Set: true, Value: true}
		return nil
	}
	if raw == "0" {
		*f = FlexibleBool{Set: true, Value: false}
		return nil
	}
	// String form. Unquote first to drop the surrounding "..." and
	// handle any JSON-escaped characters inside.
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("flexible bool: cannot parse %s: %w", raw, err)
	}
	v, ok := parseFlexibleBoolString(s)
	if !ok {
		return fmt.Errorf("flexible bool: %q is not a recognised boolean (true/false, on/off, yes/no, 1/0)", s)
	}
	*f = FlexibleBool{Set: true, Value: v}
	return nil
}

// parseFlexibleBoolString matches a string against the supported
// boolean aliases (case-insensitive, surrounding whitespace trimmed).
func parseFlexibleBoolString(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "on", "yes", "1":
		return true, true
	case "false", "off", "no", "0":
		return false, true
	default:
		return false, false
	}
}

// ParseFlexibleBool reports the boolean carried by s using the same aliases
// as FlexibleBool (true/false, on/off, yes/no, 1/0, case-insensitive). The
// second result is false when s is not a recognised boolean.
func ParseFlexibleBool(s string) (value, ok bool) {
	return parseFlexibleBoolString(s)
}
