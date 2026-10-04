// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

const (
	typesParam      = "types"
	severitiesParam = "severities"
	rulesParam      = "rules"
	facetsParam     = "facets"

	// impactSoftwareQualitiesParam / impactSeveritiesParam are the MQR
	// (Multi-Quality Rule) mode equivalents of types/severities (#630).
	// rules needs no MQR equivalent: a rule key means the same thing in
	// both models.
	impactSoftwareQualitiesParam = "impactSoftwareQualities"
	impactSeveritiesParam        = "impactSeverities"

	// directoriesParam / filesParam are the cascade's third and fourth
	// fallback facets (#630's own "ruleId in particular but others may
	// be used (directory, file, ...)"), tried when a single rule key is
	// itself still over the ceiling — the shape of a systematic rule
	// (e.g. a duplication or convention check) firing on every file of
	// a first analysis. "files" rather than "fileUuids": the latter is
	// sonar-tools' SonarQube-Cloud-only facet name, and this package
	// only ever talks to the source SonarQube Server.
	directoriesParam = "directories"
	filesParam       = "files"

	// maxFacetValues bounds how many distinct facet values the cascade
	// will fan out into follow-up requests for at any single level.
	// Mirrors the reference implementation's own safeguard (sonar-tools'
	// _MAX_FACETS = 100, see #630): a facet with more than this many
	// distinct values is evidence the dimension will not usefully
	// partition one second's issues (or would cost more requests than
	// it recovers), so the cascade gives up on that cell instead of
	// fanning out 100+ follow-up requests. Exactly maxFacetValues
	// distinct values is still tried — the cutoff is "more than", not
	// "at least" — so a project with precisely that many files, say, is
	// not punished for landing exactly on the line.
	maxFacetValues = 100

	settingsAPI       = "api/settings/values"
	mqrEnabledSetting = "sonar.multi-quality-mode.enabled"
)

// mqrIntroVersion / mqr5SeveritiesVersion mirror sonar-tools'
// MQR_INTRO_VERSION / MQR_5_SEVERITIES_VERSION (the reference
// implementation #630 cited, sonar/util/constants.py): MQR mode existed
// from 10.2.0 with no way to turn it off; the sonar.multi-quality-mode.enabled
// setting that lets an instance choose its mode was not added until
// 10.8.0.
var (
	mqrIntroVersion       = common.MustParseVersion("10.2.0")
	mqr5SeveritiesVersion = common.MustParseVersion("10.8.0")
)

// issueTaxonomy names one of SonarQube's two issue classification
// models' type/severity filter and facet parameters (#630).
type issueTaxonomy struct {
	typeParam     string
	severityParam string
}

var (
	// standardTaxonomy is "Standard Experience" mode: one type and one
	// severity per issue — the model fetchAtomicWindow's own doc comment
	// was measured against (SonarQube 2026.4.1, types x severities,
	// zero-delta 15-cell partition).
	standardTaxonomy = issueTaxonomy{typeParam: typesParam, severityParam: severitiesParam}

	// mqrTaxonomy is MQR (Multi-Quality Rule) mode. Unlike Standard
	// Experience, one issue can carry impacts on more than one software
	// quality at different severities simultaneously, so these facet
	// counts are not the clean, non-overlapping partition types x
	// severities is — a cell fetched under this taxonomy can return an
	// issue a sibling cell also returns. That is not a correctness bug:
	// absorb's existing dedup (s.seen) is exactly the verifier for this
	// kind of overlap, and a duplicate here is evidence of a real
	// multi-quality issue, not of a broken seam.
	mqrTaxonomy = issueTaxonomy{typeParam: impactSoftwareQualitiesParam, severityParam: impactSeveritiesParam}
)

// facetCascadeOrder is the fallback order tried once a (type, severity)
// cell is itself still over the ceiling: rule key, then directory, then
// file path (#630) — mirroring sonar-tools' own facet search order for
// the dimensions that apply uniformly regardless of taxonomy (rules,
// directories and files mean the same thing under Standard Experience
// and MQR mode alike, unlike type/severity).
var facetCascadeOrder = []string{rulesParam, directoriesParam, filesParam}

// IsMQRMode reports whether the source SonarQube Server is configured
// in MQR mode rather than Standard Experience mode, mirroring
// sonar-tools' Platform.is_mqr_mode (#630) — SonarQube Server only:
// SonarQube Cloud is unconditionally MQR (sonar-tools short-circuits
// is_sonarcloud() to true), but nothing in this package ever talks to
// Cloud (extract's issue-search slicing is source-only), so that branch
// is intentionally not implemented here. Probed at most once per
// Executor and cached: the setting is instance-wide, and this is only
// ever asked from inside the facet-slicing cascade, itself a rare path.
func (e *Executor) IsMQRMode(ctx context.Context) bool {
	e.mqrModeMu.Lock()
	defer e.mqrModeMu.Unlock()
	if e.mqrMode != nil {
		return *e.mqrMode
	}
	mqr := e.probeMQRMode(ctx)
	e.mqrMode = &mqr
	return mqr
}

func (e *Executor) probeMQRMode(ctx context.Context) bool {
	if e.Version.Less(mqrIntroVersion) {
		// MQR did not exist yet; every such version only understands
		// Standard Experience's types/severities.
		return false
	}
	if e.Version.Less(mqr5SeveritiesVersion) {
		// MQR existed with no toggle to turn it off yet.
		return true
	}
	items, err := e.Raw.GetArray(ctx, settingsAPI, "settings", url.Values{"keys": {mqrEnabledSetting}})
	if err != nil || len(items) == 0 {
		// A server new enough to have the toggle but that did not
		// answer it defaults to Standard taxonomy — the one every
		// version understands — rather than guessing MQR.
		e.Logger.Debug("could not read "+mqrEnabledSetting+", assuming Standard Experience mode",
			"err", err)
		return false
	}
	var setting struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(items[0], &setting) != nil {
		return false
	}
	return strings.EqualFold(setting.Value, "true")
}

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
// to roughly 24,500 issues with zero loss), falling back through rule
// key, then directory, then file path (facetCascadeOrder) within any
// (type, severity) cell that is itself still over the ceiling — the
// shape of a systematic rule firing on every file of a first analysis.
// Only a cell that survives every level of this cascade still
// over the ceiling falls through to the same capped-fetch give-up
// fetchAtomicWindow always performed on its own.
func (s *issueSlicer) fetchByFacets(ctx context.Context, w issueWindow, total int) error {
	tax := standardTaxonomy
	if s.e.IsMQRMode(ctx) {
		tax = mqrTaxonomy
	}
	types, err := s.probeFacet(ctx, w, tax.typeParam)
	if err != nil || len(types) == 0 {
		// No usable type facet — a probe failure, or (unreachable in
		// practice, since total > 0) a response carrying none. Give up
		// exactly as fetchAtomicWindow always did, before this change.
		return s.giveUpOnWindow(ctx, w, total, "")
	}

	for _, t := range types {
		if err := s.fetchTypeCell(ctx, w, tax, t); err != nil {
			return err
		}
	}
	return nil
}

// fetchTypeCell handles one type facet value: probe severities scoped
// to this type (the response's facet counts are the exact (type,
// severity) cross-product cells under Standard Experience, since the
// type filter conditions the facet — see mqrTaxonomy's doc comment for
// why that guarantee weakens, not breaks, under MQR mode), then fetch
// or further split each one.
func (s *issueSlicer) fetchTypeCell(ctx context.Context, w issueWindow, tax issueTaxonomy, t facetValue) error {
	typeFilter := [2]string{tax.typeParam, t.Val}
	severities, err := s.probeFacet(ctx, w, tax.severityParam, typeFilter)
	if err != nil || len(severities) == 0 {
		return s.giveUpOnWindow(ctx, w, t.Count, cellLabel(typeFilter), typeFilter)
	}
	for _, sev := range severities {
		severityFilter := [2]string{tax.severityParam, sev.Val}
		if sev.Count <= s.ceiling {
			if err := s.fetchCellOrError(ctx, w, typeFilter, severityFilter); err != nil {
				return err
			}
			continue
		}
		if err := s.fetchByRemainingFacets(ctx, w, sev.Count, facetCascadeOrder, typeFilter, severityFilter); err != nil {
			return err
		}
	}
	return nil
}

// fetchByRemainingFacets is the cascade's fallback engine for every
// level after (type, severity): pop the next facet off order, probe it
// scoped by every filter accumulated so far, and for each value either
// fetch it (it fits) or recurse into whatever's left of order (#630).
// Exhausting order with a cell still over the ceiling — the facet
// cascade's true terminal case — gives up exactly as fetchAtomicWindow
// always did on its own, now naming the exact cell via Scope.Detail.
func (s *issueSlicer) fetchByRemainingFacets(ctx context.Context, w issueWindow, cellTotal int, order []string, filters ...[2]string) error {
	if len(order) == 0 {
		return s.giveUpOnWindow(ctx, w, cellTotal, cellLabel(filters...), filters...)
	}
	facet, rest := order[0], order[1:]
	values, err := s.probeFacet(ctx, w, facet, filters...)
	if err != nil || len(values) == 0 || len(values) > maxFacetValues {
		return s.giveUpOnWindow(ctx, w, cellTotal, cellLabel(filters...), filters...)
	}
	for _, v := range values {
		cellFilters := append(append([][2]string(nil), filters...), [2]string{facet, v.Val})
		if v.Count <= s.ceiling {
			if err := s.fetchCellOrError(ctx, w, cellFilters...); err != nil {
				return err
			}
			continue
		}
		if err := s.fetchByRemainingFacets(ctx, w, v.Count, rest, cellFilters...); err != nil {
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
