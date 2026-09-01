// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitObjectsCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single", "settings", []string{"settings"}},
		{"multiple no spaces", "quality_profiles,quality_gates", []string{"quality_profiles", "quality_gates"}},
		{
			// #536: spaces around commas must be stripped.
			name: "spaces stripped",
			in:   " quality_profiles, quality_gates , groups ",
			want: []string{"quality_profiles", "quality_gates", "groups"},
		},
		{"empty tokens dropped", "settings,,groups", []string{"settings", "groups"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SplitObjectsCSV(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("SplitObjectsCSV(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseObjects(t *testing.T) {
	t.Run("empty means everything", func(t *testing.T) {
		got, err := ParseObjects(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil (no filter), got %v", got)
		}
	})

	t.Run("valid canonical names", func(t *testing.T) {
		got, err := ParseObjects([]string{"quality_profiles", "quality_gates"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{"quality_profiles": true, "quality_gates": true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("aliases resolve to canonical names", func(t *testing.T) {
		got, err := ParseObjects([]string{"qp", "qg", "pt", "lp"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{
			ObjectQualityProfiles:     true,
			ObjectQualityGates:        true,
			ObjectPermissionTemplates: true,
			ObjectLicenseProfiles:     true,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("case insensitive and trimmed", func(t *testing.T) {
		got, err := ParseObjects([]string{" Settings ", "GROUPS"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{ObjectSettings: true, ObjectGroups: true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("duplicate names deduplicate", func(t *testing.T) {
		got, err := ParseObjects([]string{"settings", "settings", "qp", "quality_profiles"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{ObjectSettings: true, ObjectQualityProfiles: true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("unknown value errors with the bad token named", func(t *testing.T) {
		_, err := ParseObjects([]string{"quality_profiles", "bogus_object"})
		if err == nil {
			t.Fatal("expected an error for an unrecognized object name")
		}
		if !strings.Contains(err.Error(), "bogus_object") {
			t.Errorf("error %q does not mention the invalid value", err.Error())
		}
	})

	t.Run("license_profiles is a valid, accepted value", func(t *testing.T) {
		got, err := ParseObjects([]string{"license_profiles"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got[ObjectLicenseProfiles] {
			t.Errorf("expected license_profiles accepted, got %v", got)
		}
	})
}
