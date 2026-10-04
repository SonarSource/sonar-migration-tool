// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

const (
	typesParam      = "types"
	severitiesParam = "severities"
	rulesParam      = "rules"
	facetsParam     = "facets"

	// maxFacetValues bounds how many distinct facet values the cascade
	// will fan out into follow-up requests for. Mirrors the reference
	// implementation's own safeguard (sonar-tools' _MAX_FACETS = 100,
	// see #630): a facet with this many distinct values is evidence the
	// dimension will not usefully partition one second's issues, so the
	// cascade gives up on that cell rather than firing 100+ follow-up
	// requests for diminishing returns.
	maxFacetValues = 100
)

// facetValue is one (value, count) pair read from a SonarQube facets
// response. Values with a zero count are dropped before this type is
// ever populated — they carry no issues to fetch.
type facetValue struct {
	Val   string
	Count int
}

// facetParams clones the window's own date-bounded params (the same
// clone-and-set discipline windowParams already guarantees for
// createdAfter/createdBefore) and additionally scopes by zero or more
// key/value filters, skipping any filter whose value is empty.
func facetParams(base url.Values, w issueWindow, filters ...[2]string) url.Values {
	params := windowParams(base, w)
	for _, kv := range filters {
		if kv[1] != "" {
			params.Set(kv[0], kv[1])
		}
	}
	return params
}

// cellLabel renders a set of facet filters for a log line or a
// TruncationRecord's Scope.Detail, e.g. "types=BUG severities=MAJOR".
// Scope.Detail is part of TruncationRecord's merge key (identity), so
// two different degenerate cells inside the same one-second window
// never collapse into one record.
func cellLabel(filters ...[2]string) string {
	label := ""
	for _, kv := range filters {
		if kv[1] == "" {
			continue
		}
		if label != "" {
			label += " "
		}
		label += kv[0] + "=" + kv[1]
	}
	return label
}

// probeFacet asks SonarQube for the distinct values (and counts) of one
// facet within w, scoped by filters, with a single ps=1 request — facet
// counts are computed over the whole filtered selection regardless of
// page size, exactly like probeWindowTotal's paging.total is.
func (s *issueSlicer) probeFacet(ctx context.Context, w issueWindow, facet string, filters ...[2]string) ([]facetValue, error) {
	if !w.safeToRequest() {
		// Unreachable in practice: fetchByFacets only ever runs against
		// a window fetchAtomicWindow has already closed. Loud rather
		// than silent for the same reason fetchWindow's own gate is.
		return nil, fmt.Errorf("refusing a zero-width issue window %s", w.label())
	}
	params := facetParams(s.base, w, filters...)
	params.Set(facetsParam, facet)
	params.Set("p", "1")
	params.Set("ps", "1")

	s.requests++
	body, err := s.e.Raw.Get(ctx, issuesSearchAPI, params)
	if err != nil {
		return nil, asDateBoundRejection(w, err)
	}

	var resp struct {
		Facets []struct {
			Property string `json:"property"`
			Values   []struct {
				Val   string `json:"val"`
				Count int    `json:"count"`
			} `json:"values"`
		} `json:"facets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("issues window %s: parsing %q facet response: %w", w.label(), facet, err)
	}
	for _, f := range resp.Facets {
		if f.Property != facet {
			continue
		}
		out := make([]facetValue, 0, len(f.Values))
		for _, v := range f.Values {
			if v.Count > 0 {
				out = append(out, facetValue{Val: v.Val, Count: v.Count})
			}
		}
		return out, nil
	}
	// The facet was requested but is absent from the response — treat
	// the same as "no values" rather than guessing; the caller's own
	// give-up path covers this exactly like a facet probe that errored.
	return nil, nil
}

// fetchAndAbsorbCell issues one capped fetch for w scoped by filters and
// absorbs whatever it returns, handing back the raw PageResult so the
// caller can reconcile against a known cell total (the give-up path) or
// simply trust the cell fit (every other path).
func (s *issueSlicer) fetchAndAbsorbCell(ctx context.Context, w issueWindow, filters ...[2]string) (common.PageResult, error) {
	if !w.safeToRequest() {
		return common.PageResult{}, fmt.Errorf("refusing a zero-width issue window %s", w.label())
	}
	res, err := s.e.Raw.GetPaginatedResult(ctx, PaginatedOpts{
		Path:                     issuesSearchAPI,
		Params:                   facetParams(s.base, w, filters...),
		ResultKey:                issueResultKey,
		MaxPageSize:              issuePageSize,
		PageLimit:                issuePageLimit,
		SuppressTruncationRecord: true,
		Scope:                    s.scope,
	})
	s.requests += max(1, res.PagesRead)
	if err != nil {
		return res, asDateBoundRejection(w, err)
	}
	s.windows++
	if err := s.absorb(res.Items); err != nil {
		return res, err
	}
	return res, nil
}

// fetchByFacets is fetchAtomicWindow's replacement give-up body (#630):
// w is already closed (fetchAtomicWindow closes any open edge before
// calling this) and one second wide, and still holds more than
// s.ceiling issues — date bisection has nothing left to subdivide. This
// tries a second partition axis instead of giving up immediately: types
// x severities first (validated on real production data — see
// fetchAtomicWindow's doc comment — to recover a one-second window up
// to roughly 24,500 issues with zero loss), falling back to rules
// within any (type, severity) cell that is itself still over the
// ceiling. Only a cell that survives every level of this cascade still
// over the ceiling falls through to the same capped-fetch give-up
// fetchAtomicWindow always performed on its own.
func (s *issueSlicer) fetchByFacets(ctx context.Context, w issueWindow, total int) error {
	types, err := s.probeFacet(ctx, w, typesParam)
	if err != nil || len(types) == 0 {
		// No usable types facet — a probe failure, or (unreachable in
		// practice, since total > 0) a response carrying none. Give up
		// exactly as fetchAtomicWindow always did, before this change.
		return s.giveUpOnWindow(ctx, w, total, "")
	}

	for _, t := range types {
		if err := s.fetchTypeCell(ctx, w, t); err != nil {
			return err
		}
	}
	return nil
}

// fetchTypeCell handles one types facet value: probe severities scoped
// to this type (the response's facet counts are the exact (type,
// severity) cross-product cells, since the type filter conditions the
// facet), then fetch or further split each one.
func (s *issueSlicer) fetchTypeCell(ctx context.Context, w issueWindow, t facetValue) error {
	typeFilter := [2]string{typesParam, t.Val}
	severities, err := s.probeFacet(ctx, w, severitiesParam, typeFilter)
	if err != nil || len(severities) == 0 {
		return s.giveUpOnWindow(ctx, w, t.Count, cellLabel(typeFilter), typeFilter)
	}
	for _, sev := range severities {
		severityFilter := [2]string{severitiesParam, sev.Val}
		if sev.Count <= s.ceiling {
			if err := s.fetchCellOrError(ctx, w, typeFilter, severityFilter); err != nil {
				return err
			}
			continue
		}
		if err := s.fetchByRules(ctx, w, sev.Count, typeFilter, severityFilter); err != nil {
			return err
		}
	}
	return nil
}

// fetchByRules is the cascade's second and final fallback level: a
// (type, severity) cell that is itself still over the ceiling, split
// further by rule key — the facet #630 explicitly asked for.
func (s *issueSlicer) fetchByRules(ctx context.Context, w issueWindow, cellTotal int, parentFilters ...[2]string) error {
	rules, err := s.probeFacet(ctx, w, rulesParam, parentFilters...)
	if err != nil || len(rules) == 0 || len(rules) >= maxFacetValues {
		return s.giveUpOnWindow(ctx, w, cellTotal, cellLabel(parentFilters...), parentFilters...)
	}
	for _, r := range rules {
		filters := append(append([][2]string(nil), parentFilters...), [2]string{rulesParam, r.Val})
		if r.Count <= s.ceiling {
			if err := s.fetchCellOrError(ctx, w, filters...); err != nil {
				return err
			}
			continue
		}
		// A single rule, within one type and severity, still over the
		// ceiling in one second — the cascade's true terminal case.
		if err := s.giveUpOnWindow(ctx, w, r.Count, cellLabel(filters...), filters...); err != nil {
			return err
		}
	}
	return nil
}

// fetchCellOrError is the non-give-up leaf: the caller has already
// confirmed this cell fits, so only the fetch itself can still fail.
func (s *issueSlicer) fetchCellOrError(ctx context.Context, w issueWindow, filters ...[2]string) error {
	_, err := s.fetchAndAbsorbCell(ctx, w, filters...)
	return err
}

// giveUpOnWindow is the cascade's terminal case at any level: fetch
// whatever the capped result returns for this cell, absorb it, and
// record exactly what it will not — the same contract fetchAtomicWindow
// always kept, now scoped to the facet cell the cascade reached (detail
// is "" only when no facet could even be tried) rather than always the
// whole second. Scope.Detail is part of TruncationRecord's identity, so
// a second with two independently-pathological cells (vanishingly
// unlikely, but the reason to get this right) records both instead of
// one merging into the other.
func (s *issueSlicer) giveUpOnWindow(ctx context.Context, w issueWindow, total int, detail string, filters ...[2]string) error {
	res, err := s.fetchAndAbsorbCell(ctx, w, filters...)
	if err != nil {
		return err
	}
	lost := total - res.Fetched
	if lost < 0 {
		lost = 0
	}
	s.e.Logger.Warn("more issues share one creation second than the API will return - date and facet slicing cannot subdivide further",
		"project", s.scope.ProjectKey, "branch", s.scope.Branch, "window", w.label(),
		"facets", detail, "total", total, "fetched", res.Fetched, "lost", lost)
	start, end := w.bounds()
	scope := s.scope
	scope.Detail = detail
	s.record(common.TruncationRecord{
		Endpoint:    issuesSearchAPI,
		Reason:      common.ReasonAtomicWindow,
		Scope:       scope,
		Total:       total,
		TotalKnown:  true,
		Fetched:     res.Fetched,
		Lost:        lost,
		PageSize:    res.PageSize,
		PageLimit:   res.PageLimit,
		WindowStart: start,
		WindowEnd:   end,
	})
	return nil
}
