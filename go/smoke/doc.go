// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

// Package smoke holds the live end-to-end smoke suite.
//
// Every test file in this package is guarded by the "smoke" build tag, so
// `go test ./...` compiles none of them and CI is unaffected. This file
// carries no build tag on purpose: without it, the package would have zero
// buildable files under default tags and `go test ./...` would fail with
// "build constraints exclude all Go files in .../go/smoke".
//
// Run the suite with `make smoke`, `make smoke-fast`, or `make smoke-full`
// from the repository root — never with a bare `go test`, which will not
// have built the binary the suite drives.
package smoke
