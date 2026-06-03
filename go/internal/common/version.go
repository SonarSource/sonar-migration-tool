package common

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed SonarQube version expressed as an ordered tuple of
// integer components: 9.9.3 → {9,9,3}, 2026.4.0.123541 → {2026,4,0,123541}.
//
// Float-based handling truncated 9.9.3 to 9.9, conflated 10.10 with 10.1,
// and could not order 9.9.3 against 9.9.12. Compare against another
// Version with Less / AtLeast / Equal — those treat missing trailing
// components as zero so 2025.1 == 2025.1.0.
type Version []int

// ParseVersion parses a dotted version string. Leading numeric components
// are consumed until the first non-integer segment, so "9.9.3.12345-rc"
// parses as {9,9,3,12345}. An empty string returns a nil Version, which
// compares less than every populated one.
func ParseVersion(s string) Version {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make(Version, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			break
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MustParseVersion is ParseVersion for constants — panics on a string
// that produces no numeric components.
func MustParseVersion(s string) Version {
	v := ParseVersion(s)
	if v == nil {
		panic(fmt.Sprintf("MustParseVersion: %q has no numeric components", s))
	}
	return v
}

// Less reports whether v sorts before other. Missing trailing components
// are treated as zero, so 2025.1 == 2025.1.0 and 9.9 < 9.9.1.
func (v Version) Less(other Version) bool {
	n := len(v)
	if len(other) > n {
		n = len(other)
	}
	for i := 0; i < n; i++ {
		a, b := 0, 0
		if i < len(v) {
			a = v[i]
		}
		if i < len(other) {
			b = other[i]
		}
		if a != b {
			return a < b
		}
	}
	return false
}

// AtLeast reports whether v >= other.
func (v Version) AtLeast(other Version) bool {
	return !v.Less(other)
}

// Equal reports whether v == other under trailing-zero semantics.
func (v Version) Equal(other Version) bool {
	return !v.Less(other) && !other.Less(v)
}

// String renders the version back to dotted form for logging.
func (v Version) String() string {
	if len(v) == 0 {
		return ""
	}
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// LegacyFloat returns major + minor/10 for SDK callers that still want a
// float. Use only at SDK boundaries; comparisons inside this codebase
// should use Less/AtLeast.
func (v Version) LegacyFloat() float64 {
	if len(v) == 0 {
		return 0
	}
	out := float64(v[0])
	if len(v) >= 2 {
		out += float64(v[1]) / 10
	}
	return out
}
