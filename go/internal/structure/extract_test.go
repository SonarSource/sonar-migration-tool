// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package structure

import (
	"path/filepath"
	"slices"
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
	if got, want := mapping[testSQURL], []string{"2026-08-20-101"}; !slices.Equal(got, want) {
		t.Errorf("GetUniqueExtracts picked %v, want %v (numeric, not lexicographic, ordering)", got, want)
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
	if got, want := mapping[testSQURL], []string{"2026-08-20-0001"}; !slices.Equal(got, want) {
		t.Errorf("GetUniqueExtracts picked %v, want %v (rogue non-conforming directory name must be rejected)", got, want)
	}
}

// TestGetUniqueExtracts_AcceptsLegacyDateFormat guards against a
// regression in the #550 IsValidRunID hardening: the legacy pre-#108
// "MM-DD-YYYY-N" directory naming (still present in export directories
// on users' disks — see internal/gui/history.go and
// internal/regtest/suite.go, which accept the same two shapes) must
// still be recognised as a legitimate extract, not silently dropped.
func TestGetUniqueExtracts_AcceptsLegacyDateFormat(t *testing.T) {
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "08-20-2026-0001", "extract.json"),
		map[string]any{"url": testSQURL})

	mapping, err := GetUniqueExtracts(dir)
	if err != nil {
		t.Fatalf("GetUniqueExtracts: %v", err)
	}
	if got, want := mapping[testSQURL], []string{"08-20-2026-0001"}; !slices.Equal(got, want) {
		t.Errorf("GetUniqueExtracts picked %v, want %v (legacy MM-DD-YYYY-N directory must not be dropped)", got, want)
	}
}

func writeExtractMeta(t *testing.T, dir, id string, meta map[string]any) {
	t.Helper()
	meta["url"] = testSQURL
	writeTestJSON(t, filepath.Join(dir, id, "extract.json"), meta)
}

// #664: the merged set is the latest full extract plus every newer partial one.
func TestGetUniqueExtracts_FullPlusNewerPartials(t *testing.T) {
	dir := t.TempDir()
	writeExtractMeta(t, dir, "2026-08-20-0001", map[string]any{})                 // legacy full
	writeExtractMeta(t, dir, "2026-08-20-0002", map[string]any{"partial": false}) // latest full
	writeExtractMeta(t, dir, "2026-08-20-0003", map[string]any{"partial": true, "project_keys": []string{"a"}})
	writeExtractMeta(t, dir, "2026-08-20-0004", map[string]any{"partial": true, "project_keys": []string{"b"}})
	// A partial older than the latest full is already covered by it.
	writeExtractMeta(t, dir, "2026-08-19-0009", map[string]any{"partial": true})

	mapping, err := GetUniqueExtracts(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-08-20-0002", "2026-08-20-0003", "2026-08-20-0004"}
	if got := mapping.IDs(testSQURL); !slices.Equal(got, want) {
		t.Errorf("set = %v, want %v", got, want)
	}
}

func TestGetUniqueExtracts_OnlyPartials(t *testing.T) {
	dir := t.TempDir()
	writeExtractMeta(t, dir, "2026-08-20-0010", map[string]any{"partial": true})
	writeExtractMeta(t, dir, "2026-08-20-0002", map[string]any{"partial": true})

	mapping, err := GetUniqueExtracts(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-08-20-0002", "2026-08-20-0010"}
	if got := mapping.IDs(testSQURL); !slices.Equal(got, want) {
		t.Errorf("set = %v, want %v", got, want)
	}
}

func TestGetUniqueExtracts_AllFullKeepsLatestOnly(t *testing.T) {
	dir := t.TempDir()
	writeExtractMeta(t, dir, "2026-08-20-0001", map[string]any{})
	writeExtractMeta(t, dir, "2026-08-20-0002", map[string]any{})

	mapping, _ := GetUniqueExtracts(dir)
	if got, want := mapping.IDs(testSQURL), []string{"2026-08-20-0002"}; !slices.Equal(got, want) {
		t.Errorf("set = %v, want %v", got, want)
	}
}
