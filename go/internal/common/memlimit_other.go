// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

//go:build !linux

package common

import "log/slog"

// ApplyMemoryLimit is a no-op on non-Linux platforms, where there is no
// cgroup to read and the migration workloads this guards against do not
// run. Operators on these platforms can still set GOMEMLIMIT by hand.
//
// See the Linux implementation for the behaviour this stands in for.
func ApplyMemoryLimit(_ *slog.Logger) (int64, string) {
	return 0, ""
}
