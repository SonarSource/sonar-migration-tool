// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeRoot builds a filesystem fixture standing in for "/". files maps a
// path relative to the root (e.g. "sys/fs/cgroup/memory.max") to contents.
func fakeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return root
}

// noEnv is a getenv that reports every variable as unset.
func noEnv(string) string { return "" }

// quietLogger discards output so tests don't spam stderr.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// spy records what SetMemoryLimit would have been called with.
func spy(applied *int64) func(int64) int64 {
	return func(n int64) int64 {
		*applied = n
		return n
	}
}

const gib = int64(1) << 30

// Fixture paths, relative to the fake root.
const (
	pathV2Max   = "sys/fs/cgroup/memory.max"
	pathV2High  = "sys/fs/cgroup/memory.high"
	pathV1Limit = "sys/fs/cgroup/memory/memory.limit_in_bytes"
	pathMemInfo = "proc/meminfo"
)

// want80 mirrors the production truncation. It must be a function, not a
// constant expression: 80% of a power-of-two byte count is not an integer,
// so folding it at compile time fails to convert to int64.
func want80(n int64) int64 { return int64(float64(n) * memLimitFraction) }

func TestApplyMemoryLimitCgroupV2(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV2Max: "34359738368\n", // 32 GiB
	})

	var applied int64
	limit, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if source != sourceCgroupV2 {
		t.Errorf("source = %q, want %q", source, sourceCgroupV2)
	}
	want := want80(32 * gib)
	if limit != want {
		t.Errorf("limit = %d, want %d", limit, want)
	}
	if applied != want {
		t.Errorf("SetMemoryLimit called with %d, want %d", applied, want)
	}
}

// "max" is the cgroup v2 spelling of unlimited, so detection must fall
// through to the next source rather than treating it as a real number.
func TestApplyMemoryLimitCgroupV2MaxFallsThrough(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV2Max:   "max\n",
		pathMemInfo: "MemTotal:       16777216 kB\n",
	})

	var applied int64
	_, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if source != sourceMemInfo {
		t.Errorf("source = %q, want %q", source, sourceMemInfo)
	}
}

// When both v2 knobs are set, the lower one is what the process actually
// feels, so that is the one we must derive the limit from.
func TestApplyMemoryLimitCgroupV2PrefersLowerOfMaxAndHigh(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV2Max:  "34359738368\n", // 32 GiB
		pathV2High: "17179869184\n", // 16 GiB
	})

	var applied int64
	limit, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if source != sourceCgroupV2 {
		t.Errorf("source = %q, want %q", source, sourceCgroupV2)
	}
	want := want80(16 * gib)
	if limit != want {
		t.Errorf("limit = %d, want %d (should follow memory.high, the lower knob)", limit, want)
	}
}

func TestApplyMemoryLimitCgroupV1(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV1Limit: "8589934592\n", // 8 GiB
	})

	var applied int64
	limit, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if source != sourceCgroupV1 {
		t.Errorf("source = %q, want %q", source, sourceCgroupV1)
	}
	want := want80(8 * gib)
	if limit != want {
		t.Errorf("limit = %d, want %d", limit, want)
	}
}

// cgroup v1 signals "unlimited" with a near-INT64_MAX sentinel whose exact
// value depends on page size, so we test the threshold rather than a
// specific constant.
func TestApplyMemoryLimitCgroupV1UnlimitedSentinelFallsThrough(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV1Limit: "9223372036854771712\n",
		pathMemInfo: "MemTotal:       16777216 kB\n",
	})

	var applied int64
	_, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if source != sourceMemInfo {
		t.Errorf("source = %q, want %q", source, sourceMemInfo)
	}
}

func TestApplyMemoryLimitMemInfoFallback(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathMemInfo: "MemFree:         1024 kB\nMemTotal:       33554432 kB\nSwapTotal: 0 kB\n",
	})

	var applied int64
	limit, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if source != sourceMemInfo {
		t.Errorf("source = %q, want %q", source, sourceMemInfo)
	}
	want := want80(32 * gib)
	if limit != want {
		t.Errorf("limit = %d, want %d", limit, want)
	}
}

// An operator who set GOMEMLIMIT deliberately must win — including
// GOMEMLIMIT=off, which is the documented way to disable the limit.
func TestApplyMemoryLimitRespectsExistingGOMEMLIMIT(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV2Max: "34359738368\n",
	})

	for _, val := range []string{"16GiB", "off"} {
		var applied int64
		getenv := func(k string) string {
			if k == "GOMEMLIMIT" {
				return val
			}
			return ""
		}
		limit, source := applyMemoryLimit(root, getenv, spy(&applied), quietLogger())

		if limit != 0 || source != "" {
			t.Errorf("GOMEMLIMIT=%s: got (%d, %q), want (0, \"\")", val, limit, source)
		}
		if applied != 0 {
			t.Errorf("GOMEMLIMIT=%s: SetMemoryLimit must not be called, got %d", val, applied)
		}
	}
}

func TestApplyMemoryLimitNoSourcesAvailable(t *testing.T) {
	root := fakeRoot(t, nil)

	var applied int64
	limit, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if limit != 0 || source != "" {
		t.Errorf("got (%d, %q), want (0, \"\")", limit, source)
	}
	if applied != 0 {
		t.Errorf("SetMemoryLimit must not be called, got %d", applied)
	}
}

// Below the floor, throttling the heap does more harm than good, so we
// back off rather than strangle a small container.
func TestApplyMemoryLimitBelowFloorBacksOff(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV2Max: "268435456\n", // 256 MiB -> 204 MiB, under the floor
	})

	var applied int64
	limit, source := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if limit != 0 || source != "" {
		t.Errorf("got (%d, %q), want (0, \"\")", limit, source)
	}
	if applied != 0 {
		t.Errorf("SetMemoryLimit must not be called below the floor, got %d", applied)
	}
}

// Exactly at the floor the limit still applies — the guard is "below",
// not "at or below".
func TestApplyMemoryLimitAtFloorApplies(t *testing.T) {
	// Pick a budget whose 80% lands exactly on the floor.
	budget := int64(float64(memLimitFloor) / memLimitFraction)
	root := fakeRoot(t, map[string]string{
		pathV2Max: strconv.FormatInt(budget, 10),
	})

	var applied int64
	limit, _ := applyMemoryLimit(root, noEnv, spy(&applied), quietLogger())

	if limit < memLimitFloor {
		t.Errorf("limit = %d, want >= floor %d", limit, memLimitFloor)
	}
	if applied == 0 {
		t.Error("SetMemoryLimit should have been called at the floor")
	}
}

func TestReadCgroupValueRejectsGarbage(t *testing.T) {
	tests := []struct {
		name, content string
	}{
		{"empty", ""},
		{"whitespace only", "   \n"},
		{"non-numeric", "not-a-number\n"},
		{"negative", "-1\n"},
		{"zero", "0\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot(t, map[string]string{pathV2Max: tc.content})
			if got := cgroupV2Limit(root); got != 0 {
				t.Errorf("cgroupV2Limit = %d, want 0", got)
			}
		})
	}
}

func TestReadCgroupValueMissingFile(t *testing.T) {
	if got := readCgroupValue(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Errorf("readCgroupValue on missing file = %d, want 0", got)
	}
}

func TestMemTotalMalformed(t *testing.T) {
	tests := []struct {
		name, content string
	}{
		{"missing MemTotal", "MemFree: 1024 kB\n"},
		{"no value", "MemTotal:\n"},
		{"non-numeric", "MemTotal:       abc kB\n"},
		{"zero", "MemTotal:       0 kB\n"},
		{"empty file", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot(t, map[string]string{pathMemInfo: tc.content})
			if got := memTotal(root); got != 0 {
				t.Errorf("memTotal = %d, want 0", got)
			}
		})
	}
}

func TestMemTotalMissingFile(t *testing.T) {
	if got := memTotal(t.TempDir()); got != 0 {
		t.Errorf("memTotal on missing file = %d, want 0", got)
	}
}

// A nil logger must not panic — callers outside cmd may not have one.
func TestApplyMemoryLimitNilLogger(t *testing.T) {
	root := fakeRoot(t, map[string]string{
		pathV2Max: "34359738368\n",
	})
	var applied int64
	if _, source := applyMemoryLimit(root, noEnv, spy(&applied), nil); source != sourceCgroupV2 {
		t.Errorf("source = %q, want %q", source, sourceCgroupV2)
	}
}

// ApplyMemoryLimit is the exported wrapper; on Linux it probes the real
// root, elsewhere it is a no-op. Either way it must not panic and must not
// return a limit below the floor.
func TestApplyMemoryLimitExportedIsSafe(t *testing.T) {
	limit, source := ApplyMemoryLimit(quietLogger())
	if limit != 0 && limit < memLimitFloor {
		t.Errorf("ApplyMemoryLimit returned %d, below floor %d", limit, memLimitFloor)
	}
	if limit == 0 && source != "" {
		t.Errorf("no limit applied but source = %q", source)
	}
}
