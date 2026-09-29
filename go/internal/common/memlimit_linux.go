// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

//go:build linux

package common

import (
	"log/slog"
	"os"
)

// ApplyMemoryLimit sets a soft Go heap ceiling derived from the cgroup
// memory limit (v2, then v1), falling back to total system memory. It is a
// no-op when GOMEMLIMIT is already set, so an operator can always override
// the detection (GOMEMLIMIT=off disables the limit entirely).
//
// Returns the applied limit in bytes and the source it was derived from, or
// (0, "") when no limit was applied.
//
// Linux only: cgroups are the mechanism that actually constrains the
// migration VMs this guards against. On other platforms it is a no-op and
// operators set GOMEMLIMIT by hand.
func ApplyMemoryLimit(logger *slog.Logger) (int64, string) {
	return applyMemoryLimit("/", os.Getenv, setMemoryLimit, logger)
}

// MemoryBudget returns the memory available to this process — the same
// cgroup v2/v1/total-system-memory detection ApplyMemoryLimit uses for
// GOMEMLIMIT — without applying anything. Callers that need to size their
// own concurrency to available memory (e.g. migrate's adaptive
// --project_data_build_concurrency, #573) use this instead of duplicating
// the detection. Returns (0, "") when it cannot be determined.
//
// Linux only, mirroring ApplyMemoryLimit: see its doc for why.
func MemoryBudget() (int64, string) {
	return detectMemoryBudget("/")
}
