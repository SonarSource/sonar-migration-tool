// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import "github.com/sonar-solutions/sonar-migration-tool/internal/common"

// RawClient is an alias for common.RawClient.
type RawClient = common.RawClient

// NewRawClient wraps an sqapi.Client's HTTP infrastructure.
var NewRawClient = common.NewRawClient

// PaginatedOpts is an alias for common.PaginatedOpts.
type PaginatedOpts = common.PaginatedOpts

// TruncationScope is an alias for common.TruncationScope.
type TruncationScope = common.TruncationScope

// TruncationTracker is an alias for common.TruncationTracker.
type TruncationTracker = common.TruncationTracker

// NewTruncationTracker returns an empty truncation tracker for one
// extract run.
var NewTruncationTracker = common.NewTruncationTracker

// TruncationEventsFile is the name of the artefact written under the
// extract directory when a run truncated something (#574).
const TruncationEventsFile = common.TruncationEventsFile
