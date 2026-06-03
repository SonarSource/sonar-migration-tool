package common

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
	}{
		{"", nil},
		{"   ", nil},
		{"9.9", Version{9, 9}},
		{"9.9.3", Version{9, 9, 3}},
		{"10.2", Version{10, 2}},
		{"2025.1", Version{2025, 1}},
		{"2026.4.0.123541", Version{2026, 4, 0, 123541}},
		{"9.9.3-rc1", Version{9, 9}}, // stops at first non-numeric segment
		{"abc", nil},
	}
	for _, tc := range cases {
		got := ParseVersion(tc.in)
		if !sliceEq(got, tc.want) {
			t.Errorf("ParseVersion(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func sliceEq(a, b Version) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Verify the orderings from the issue: 10.2 > 9.9.3, 2025.1.2 > 2025.1.1
// > 2025.1, and trailing-zero equivalence (2025.1 == 2025.1.0).
func TestVersionOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int // -1 a<b, 0 equal, 1 a>b
	}{
		{"10.2", "9.9.3", 1},
		{"9.9.3", "10.2", -1},
		{"2025.1.2", "2025.1.1", 1},
		{"2025.1.1", "2025.1", 1},
		{"2025.1", "2025.1.0", 0},
		{"2025.1.0", "2025.1", 0},
		{"10.10", "10.2", 1}, // float would have collapsed both to 10.1
		{"10.2", "10.10", -1},
		{"9.9", "9.9.0", 0},
		{"9.9.0.1", "9.9", 1},
		{"10.0", "9.9.99999", 1},
	}
	for _, tc := range cases {
		a := ParseVersion(tc.a)
		b := ParseVersion(tc.b)
		got := compare(a, b)
		if got != tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func compare(a, b Version) int {
	switch {
	case a.Less(b):
		return -1
	case b.Less(a):
		return 1
	default:
		return 0
	}
}

func TestVersionAtLeast(t *testing.T) {
	if !ParseVersion("10.2").AtLeast(ParseVersion("10.2")) {
		t.Error("10.2 >= 10.2")
	}
	if !ParseVersion("10.2.1").AtLeast(ParseVersion("10.2")) {
		t.Error("10.2.1 >= 10.2")
	}
	if ParseVersion("9.9.3").AtLeast(ParseVersion("10.2")) {
		t.Error("9.9.3 < 10.2")
	}
}

func TestVersionString(t *testing.T) {
	if got := ParseVersion("9.9.3.12345").String(); got != "9.9.3.12345" {
		t.Errorf("String() = %q, want 9.9.3.12345", got)
	}
	if got := Version(nil).String(); got != "" {
		t.Errorf("String() of nil = %q, want empty", got)
	}
}

func TestVersionLegacyFloat(t *testing.T) {
	cases := []struct {
		v    string
		want float64
	}{
		{"9.9", 9.9},
		{"9.9.3", 9.9},
		{"10.2", 10.2},
		{"2026.4.0.123541", 2026.4},
	}
	for _, tc := range cases {
		got := ParseVersion(tc.v).LegacyFloat()
		if got != tc.want {
			t.Errorf("ParseVersion(%q).LegacyFloat() = %v, want %v", tc.v, got, tc.want)
		}
	}
}
