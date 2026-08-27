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
// container or a systemd slice, total system memory otherwise.
//
// "Under a systemd slice" requires resolving the process's own cgroup from
// /proc/self/cgroup rather than reading the hierarchy root: a unit with
// MemoryMax= runs in /system.slice/<unit>, and the root it sits under is
// unlimited. Reading only the root found nothing there and silently fell
// through to total system memory, which is no protection at all when the
// unit's cap is the thing about to kill the process.
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

// cgroupSelfPaths returns the cgroup-relative paths to probe, from the
// hierarchy root down to the process's own cgroup.
//
// The empty path is always first. Inside a cgroup namespace — the usual
// container case — /proc/self/cgroup reports "/" and the mount root IS the
// process's cgroup, so the empty path is the whole answer. Outside one, a
// process placed in a nested cgroup (a systemd unit with MemoryMax= lands
// in /system.slice/<unit>) sees an unlimited hierarchy root, and its real
// ceiling lives further down. Probing only the root missed those entirely.
//
// controller selects the v1 hierarchy to read; pass "" for unified v2.
// A missing or unparseable /proc/self/cgroup degrades to the empty path,
// i.e. exactly the old root-only behaviour.
func cgroupSelfPaths(root, controller string) []string {
	paths := []string{""}

	rel := cgroupSelfPath(root, controller)
	segments := strings.Split(strings.Trim(rel, "/"), "/")
	cur := ""
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		cur += "/" + seg
		paths = append(paths, cur)
	}
	return paths
}

// cgroupSelfPath parses /proc/self/cgroup and returns the path this process
// belongs to in the requested hierarchy, or "" when it cannot be determined.
//
// Lines are "hierarchy-ID:controller-list:cgroup-path". The unified v2
// hierarchy is the entry with ID 0 and an empty controller list; a v1
// hierarchy is identified by its controller appearing in the list.
func cgroupSelfPath(root, controller string) string {
	b, err := os.ReadFile(filepath.Join(root, "proc/self/cgroup"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if cgroupLineMatches(parts[0], parts[1], controller) {
			return parts[2]
		}
	}
	return ""
}

// cgroupLineMatches reports whether a /proc/self/cgroup line belongs to the
// requested hierarchy.
func cgroupLineMatches(id, controllers, want string) bool {
	if want == "" {
		return id == "0" && controllers == ""
	}
	for _, c := range strings.Split(controllers, ",") {
		if c == want {
			return true
		}
	}
	return false
}

// cgroupV2Limit reads the cgroup v2 memory ceiling for this process.
//
// memory.max is the hard limit and memory.high the throttling threshold,
// and the kernel enforces both hierarchically: a process is bound by the
// tightest limit anywhere in its ancestor chain. So the effective ceiling
// is the minimum across every level, not the first one that happens to be
// set — an ancestor slice capped looser than the unit's own MemoryMax must
// not mask it. The literal "max" means unlimited and is skipped.
func cgroupV2Limit(root string) int64 {
	limit := int64(0)
	for _, rel := range cgroupSelfPaths(root, "") {
		for _, name := range []string{"memory.max", "memory.high"} {
			v := readCgroupValue(filepath.Join(root, "sys/fs/cgroup", rel, name))
			if v > 0 && (limit == 0 || v < limit) {
				limit = v
			}
		}
	}
	return limit
}

// cgroupV1Limit reads the cgroup v1 memory ceiling for this process, taking
// the minimum across the ancestor chain for the same reason as v2.
func cgroupV1Limit(root string) int64 {
	limit := int64(0)
	for _, rel := range cgroupSelfPaths(root, "memory") {
		v := readCgroupValue(filepath.Join(root, "sys/fs/cgroup/memory", rel, "memory.limit_in_bytes"))
		if v > 0 && (limit == 0 || v < limit) {
			limit = v
		}
	}
	return limit
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
