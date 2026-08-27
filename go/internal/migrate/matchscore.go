// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

// This file implements the exact-then-approximate matching algorithm
// requested by issue #412, adapted from sonar-tools' findings.py
// (strictly_identical_to / almost_identical_to). The SonarQube API exposes
// no line-content hash for issues or hotspots, so the exact-match test and
// the approximate score are built entirely from fields the search APIs
// already return: rule, file, line, message, type/offset, severity, and
// author.

// levenshteinWithin reports whether the edit distance between a and b is at
// most maxDist. Plain O(len(a)*len(b)) DP — inputs here are short issue and
// hotspot messages, and each call only runs across a small, already
// file+rule-scoped candidate set, so no bounded/banded optimisation is
// warranted.
func levenshteinWithin(a, b string, maxDist int) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if abs(len(ra)-len(rb)) > maxDist {
		return false
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)] <= maxDist
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// messageApproxDistance is the maximum edit distance for two messages to be
// considered "similar" by the approximate scorer, mirroring sonar-tools'
// Levenshtein.distance(...) <= 5 cutoff.
const messageApproxDistance = 5

// issueApproxMatchThreshold is the minimum score (out of a 7-point maximum,
// see issueMatchScore) an approximate candidate must reach to qualify.
// Mirrors sonar-tools' "need at least 7/8" ratio (~87.5%), adapted to the
// smaller point scale used here since no hash-based tie-break is available.
const issueApproxMatchThreshold = 6

// issueMatchScore scores how well candidate matches source, mirroring
// sonar-tools' almost_identical_to. Returns -1 when the rule differs — a
// rule mismatch is a hard rejection, never a candidate. Otherwise the score
// (0-7) accumulates: message exact (+2) or edit-distance-similar (+1); file
// equal (+1); line equal (+1); type equal (+1); severity equal (+1); author
// equal (+1).
func issueMatchScore(source, candidate matchableIssue) int {
	if source.Rule != candidate.Rule {
		return -1
	}
	score := 0
	switch {
	case source.Message == candidate.Message:
		score += 2
	case levenshteinWithin(source.Message, candidate.Message, messageApproxDistance):
		score++
	}
	if stripProjectKeyPrefix(source.Component) == stripProjectKeyPrefix(candidate.Component) {
		score++
	}
	if source.Line == candidate.Line {
		score++
	}
	if source.Type == candidate.Type {
		score++
	}
	if source.Severity == candidate.Severity {
		score++
	}
	if source.Author == candidate.Author {
		score++
	}
	return score
}

// classifyIssueCandidatesByScore resolves a cloud counterpart for one
// source issue from the per-file+rule candidate set returned by
// /api/issues/search (issue #412).
//
// Phase 1 — exact match: file, line and message all equal (rule is already
// guaranteed equal by the scoped Cloud search; a line-content hash, which
// sonar-tools' strictly_identical_to also checks, is not exposed by the
// SonarQube API and is therefore omitted). Exactly one exact match syncs;
// several is reported as ambiguous.
//
// Phase 2 — approximate match: every candidate is scored via
// issueMatchScore; those scoring >= issueApproxMatchThreshold qualify.
// Zero qualifying candidates → not_found; exactly one → synced; several →
// ambiguous (line_mismatch) — the literal decision tree from issue #412,
// rather than sonar-tools' own smallest-line-gap tiebreak.
func classifyIssueCandidatesByScore(candidates []matchableIssue, source matchableIssue) (matchableIssue, syncOutcome) {
	var exact []matchableIssue
	for _, c := range candidates {
		if stripProjectKeyPrefix(c.Component) == stripProjectKeyPrefix(source.Component) &&
			c.Line == source.Line && c.Message == source.Message {
			exact = append(exact, c)
		}
	}
	switch len(exact) {
	case 0:
		// Fall through to approximate scoring.
	case 1:
		return exact[0], syncOutcomeSynced
	default:
		return matchableIssue{}, syncOutcomeLineMismatch
	}

	var approx []matchableIssue
	for _, c := range candidates {
		if issueMatchScore(source, c) >= issueApproxMatchThreshold {
			approx = append(approx, c)
		}
	}
	switch len(approx) {
	case 0:
		return matchableIssue{}, syncOutcomeNotFound
	case 1:
		return approx[0], syncOutcomeSynced
	default:
		return matchableIssue{}, syncOutcomeLineMismatch
	}
}
