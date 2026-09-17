// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package common

import "time"

// SQDateLayout is the only date-time layout SonarQube's search endpoints
// accept for createdAfter / createdBefore. Verified live against
// SonarQube 2026.4.1 (#574): the server rejects with HTTP 400 every one
// of time.RFC3339's outputs that differ from this layout — a trailing
// "Z", a fractional-seconds component, a "+00:00" style offset, a
// missing offset, and a space instead of the "T" separator.
//
// The layout's offset element is "-0700" rather than a literal "+0000"
// on purpose. A literal would render a non-UTC time with a UTC-looking
// suffix, which is a wrong timestamp the server would happily accept, so
// FormatSQDate converts to UTC and lets the layout emit the real offset.
//
// The other accepted format, plain "yyyy-MM-dd", is deliberately NOT
// offered: a date-only createdBefore means "< D+1day" while a date-only
// createdAfter means ">= D 00:00", so the two halves of a split overlap
// by a day and double-count (measured: 14,904 issues across a split of a
// 14,903-issue project).
const SQDateLayout = "2006-01-02T15:04:05-0700"

// FormatSQDate renders t as a SonarQube search bound. The time is
// converted to UTC first so the emitted offset is always "+0000" and two
// bounds constructed in different locations are byte-comparable — the
// half-open [start, mid) / [mid, end) split relies on both sides
// formatting the same instant to the same string.
func FormatSQDate(t time.Time) string {
	return t.UTC().Format(SQDateLayout)
}

// ParseSQDate parses a SonarQube search bound, or an issue's
// creationDate, which is emitted on the wire in exactly this layout.
func ParseSQDate(s string) (time.Time, error) {
	return time.Parse(SQDateLayout, s)
}
