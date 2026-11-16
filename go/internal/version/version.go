// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package version

// Version is a var, not a const, so the release workflow can override it at
// build time via `-ldflags "-X .../internal/version.Version=vX.Y.Z"` — a
// released binary then reports the real release version instead of the
// "-SNAPSHOT" suffix this source tree carries between releases (#615).
// A plain `go build` with no ldflags (local dev builds, go/smoke, the
// non-release CI build) keeps reporting this literal, SNAPSHOT included.
var Version = "v1.3.0-SNAPSHOT"

const ToolName = "sonar-migration-tool"
