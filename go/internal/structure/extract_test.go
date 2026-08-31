// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"path/filepath"
	"testing"
)

// TestGetUniqueExtracts_PicksNumericallyLatestPast99 reproduces #542: once
// a source URL has both a two-digit and a three-digit extract run, the
// latest run must be picked by numeric run order, not lexicographic
// string order (which would wrongly treat "...-99" as newer than
// "...-101").
func TestGetUniqueExtracts_PicksNumericallyLatestPast99(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "2026-08-20-99", "extract.json"),
		map[string]any{"url": testSQURL})
	writeTestJSON(t, filepath.Join(dir, "2026-08-20-101", "extract.json"),
		map[string]any{"url": testSQURL})

	mapping, err := GetUniqueExtracts(dir)
	if err != nil {
		t.Fatalf("GetUniqueExtracts: %v", err)
	}
	if got, want := mapping[testSQURL], "2026-08-20-101"; got != want {
		t.Errorf("GetUniqueExtracts picked %q, want %q (numeric, not lexicographic, ordering)", got, want)
	}
}

// TestGetUniqueExtracts_RejectsNonConformingDirectoryName closes #550: a
// rogue directory whose name doesn't match the "YYYY-MM-DD-N" run-ID
// shape (e.g. one an attacker could plant in the export directory) must
// never be picked as "latest", even though a plain string compare would
// rank it above every legitimately dated run (any letter sorts above
// any digit in ASCII).
func TestGetUniqueExtracts_RejectsNonConformingDirectoryName(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "2026-08-20-0001", "extract.json"),
		map[string]any{"url": testSQURL})
	writeTestJSON(t, filepath.Join(dir, "zzz-evil", "extract.json"),
		map[string]any{"url": testSQURL})

	mapping, err := GetUniqueExtracts(dir)
	if err != nil {
		t.Fatalf("GetUniqueExtracts: %v", err)
	}
	if got, want := mapping[testSQURL], "2026-08-20-0001"; got != want {
		t.Errorf("GetUniqueExtracts picked %q, want %q (rogue non-conforming directory name must be rejected)", got, want)
	}
}
