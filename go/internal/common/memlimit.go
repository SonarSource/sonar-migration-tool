// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
)

// Go's garbage collector doubles the heap before collecting (GOGC=100).
// The migrate phase allocates hard enough that on a large SonarQube
// instance the allocation rate outruns collection and the kernel OOM-kills
// the process. A soft memory limit makes the collector run sooner, which
// is exactly the right lever for a workload whose garbage is short-lived.
//
// Operators used to have to discover this themselves and set GOMEMLIMIT by
// hand. These helpers derive the same ceiling automatically from whatever
// the process is actually allowed to use — the cgroup limit under a
// container or systemd slice, total system memory otherwise.
const (
	// memLimitFraction leaves headroom for the parts of the process the
	// Go heap limit does not cover (stacks, the binary, OS page cache
	// pressure) and for the collector to do useful work before the box
	// runs out. SetMemoryLimit is soft: if the live heap exceeds it, Go
	// keeps collecting rather than failing, so a value that is slightly
	// too low degrades throughput instead of breaking the run.
	memLimitFraction = 0.8

	// memLimitFloor is the point below which throttling the heap does
	// more harm than good — we back off entirely rather than strangle a
	// small container.
	memLimitFloor = 512 << 20 // 512 MiB

	// cgroupUnlimited is the threshold above which a cgroup v1 limit means
	// "no limit". The kernel writes PAGE_COUNTER_MAX, whose exact value
	// depends on page size, so a threshold is more reliable than testing
	// for a specific sentinel.
	cgroupUnlimited = int64(1) << 62
)

// memorySource names where a detected limit came from, for logging.
const (
	sourceCgroupV2 = "cgroup-v2"
	sourceCgroupV1 = "cgroup-v1"
	sourceMemInfo  = "meminfo"
)

// applyMemoryLimit is the testable core of ApplyMemoryLimit. root is the
// filesystem root to probe (tests pass a fixture directory), getenv reads
// the environment, and setLimit applies the limit — all injected so the
// detection logic can be exercised without a container.
//
// Returns the applied limit in bytes and its source, or (0, "") when no
// limit was applied.
func applyMemoryLimit(root string, getenv func(string) string,
	setLimit func(int64) int64, logger *slog.Logger,
) (int64, string) {
	// An explicit GOMEMLIMIT always wins: the Go runtime has already
	// applied it, and an operator who set it deliberately (including
	// GOMEMLIMIT=off) should not be second-guessed.
	if getenv("GOMEMLIMIT") != "" {
		return 0, ""
	}

	detected, source := detectMemoryBudget(root)
	if detected <= 0 {
		return 0, ""
	}

	limit := int64(float64(detected) * memLimitFraction)
	if limit < memLimitFloor {
		if logger != nil {
			logger.Debug("memory limit not applied: detected budget too small",
				"detectedMiB", detected>>20, "source", source)
		}
		return 0, ""
	}

	setLimit(limit)
	if logger != nil {
		logger.Info("memory limit applied",
			"limitMiB", limit>>20,
			"detectedMiB", detected>>20,
			"source", source,
			"override", "set GOMEMLIMIT to override, GOMEMLIMIT=off to disable")
	}
	return limit, source
}

// detectMemoryBudget returns the memory the process may use, preferring the
// cgroup limit (which is what the kernel actually enforces) over total
// system memory (which, in a container, is the host's and therefore a lie).
func detectMemoryBudget(root string) (int64, string) {
	if v := cgroupV2Limit(root); v > 0 {
		return v, sourceCgroupV2
	}
	if v := cgroupV1Limit(root); v > 0 {
		return v, sourceCgroupV1
	}
	if v := memTotal(root); v > 0 {
		return v, sourceMemInfo
	}
	return 0, ""
}

// cgroupV2Limit reads the cgroup v2 memory ceiling. memory.max is the hard
// limit and memory.high the throttling threshold; when both are set the
// lower one is what the process will actually feel, so we take the min.
// The literal "max" means unlimited.
func cgroupV2Limit(root string) int64 {
	limit := int64(0)
	for _, name := range []string{"memory.max", "memory.high"} {
		v := readCgroupValue(filepath.Join(root, "sys/fs/cgroup", name))
		if v <= 0 {
			continue
		}
		if limit == 0 || v < limit {
			limit = v
		}
	}
	return limit
}

// cgroupV1Limit reads the cgroup v1 memory ceiling.
func cgroupV1Limit(root string) int64 {
	return readCgroupValue(filepath.Join(root, "sys/fs/cgroup/memory/memory.limit_in_bytes"))
}

// readCgroupValue parses a single-value cgroup file, returning 0 for
// missing, malformed, or effectively-unlimited values.
func readCgroupValue(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	if s == "" || s == "max" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || v >= cgroupUnlimited {
		return 0
	}
	return v
}

// memTotal reads MemTotal from /proc/meminfo, which is reported in kB.
func memTotal(root string) int64 {
	f, err := os.Open(filepath.Join(root, "proc/meminfo"))
	if err != nil {
		return 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("MemTotal:")) {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb <= 0 {
			return 0
		}
		return kb << 10
	}
	return 0
}

// setMemoryLimit is the real applier, indirected so tests can spy on it.
var setMemoryLimit = debug.SetMemoryLimit
