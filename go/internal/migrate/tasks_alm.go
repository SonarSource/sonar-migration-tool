// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sq-api-go/cloud"
)

// Reasons recorded on a matchProjectRepos skip record. They are consumed
// by the report (internal/report/summary) to route the project into the
// "Partial Migration" bucket with an operator-facing explanation.
const (
	// BindingSkipOrgNotBound means the source project carried a DevOps
	// platform binding but the target SonarQube Cloud organization is
	// not itself bound to a DevOps platform (or is bound to a different
	// platform than the source project uses). Per issue #122 the project
	// must then be reported as partially migrated.
	BindingSkipOrgNotBound = "org_not_bound"
	// BindingSkipRepoNotFound means the target org IS bound to the right
	// platform but the source repository does not exist in the bound
	// DevOps organization, so there is nothing to bind to.
	BindingSkipRepoNotFound = "repo_not_found"
	// BindingSkipNoProjectID means the cloud project's internal id could
	// not be resolved, so the DOP binding call cannot be made.
	BindingSkipNoProjectID = "no_project_id"
	// BindingSkipOrgBindingUnknown means the target org's own DevOps
	// binding could not be READ (issue #505): show_bound_organization
	// failed with something other than the "not bound" answer, so the
	// tool never found out whether the org is bound. Reported apart from
	// BindingSkipOrgNotBound because telling an operator "your org is not
	// bound" when we never managed to look is misleading.
	BindingSkipOrgBindingUnknown = "org_binding_unknown"
	// BindingSkipReposUnknown means listing the bound DevOps
	// organization's repositories failed, so no repository could be
	// matched — as opposed to BindingSkipRepoNotFound, where the listing
	// succeeded and simply did not contain the source repository (#505).
	BindingSkipReposUnknown = "repos_unknown"
	// BindingSkipOnPremPlatform means the source project is bound to an
	// on-premise DevOps platform (GitHub Enterprise Server, self-managed
	// GitLab, Bitbucket Server). SonarQube Cloud integrates only with
	// GitHub.com, GitLab.com, Azure DevOps Services and Bitbucket Cloud,
	// so such a binding has no target equivalent. Before #505 these were
	// dropped silently and the project was still reported as fully
	// migrated.
	BindingSkipOnPremPlatform = "on_prem_platform"
)

// BindingSkipDetail maps a skip reason to the operator-facing sentence
// rendered in the migration report's Details column.
var BindingSkipDetail = map[string]string{
	BindingSkipOrgNotBound:       "project binding was not possible because the org itself is not bound",
	BindingSkipRepoNotFound:      "project binding was not possible because the repository was not found in the bound DevOps organization",
	BindingSkipNoProjectID:       "project binding was not possible because the target project id could not be resolved",
	BindingSkipOrgBindingUnknown: "project binding was not possible because the target organization's DevOps platform binding could not be read",
	BindingSkipReposUnknown:      "project binding was not possible because the repositories of the bound DevOps organization could not be listed",
	BindingSkipOnPremPlatform:    "project binding was not possible because the source project is bound to an on-premise DevOps platform, which SonarQube Cloud cannot integrate with",
}

// bindingSkip explains why a project's DevOps binding could not be
// replicated. Err carries the underlying API error when the skip was
// caused by a failed lookup rather than by an observed fact, so the
// report can quote it (issues #122, #505).
type bindingSkip struct {
	Reason string
	Err    string
}

// almTasks returns tasks for ALM (DevOps platform) binding.
func almTasks() []TaskDef {
	return []TaskDef{
		{
			// Reads the DevOps platform organization each target org is
			// bound to. Issue #122: a project binding may only be
			// attempted when the target org is itself bound.
			Name:         "getOrgBinding",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetOrgBinding,
		},
		{
			Name:         "matchProjectRepos",
			Dependencies: []string{"getProjectIds", "getOrgRepos", "getOrgBinding"},
			Run:          runMatchProjectRepos,
		},
		{
			// Project DevOps binding writes need the migration user
			// to be a project admin (issue #190).
			Name:         "setProjectBinding",
			Dependencies: []string{"matchProjectRepos", "grantMigrationUserProjectPermissions"},
			Run:          runSetProjectBinding,
		},
	}
}

// almHostPatterns maps a DevOps platform host fragment to the `alm`
// identifier SonarQube Server reports in /api/alm_settings/list. Used to
// infer the platform of a bound SonarQube Cloud organization, whose
// show_bound_organization response only carries the ALM URL.
var almHostPatterns = []struct {
	host string
	alm  string
}{
	{"github.com", "github"},
	{"gitlab.com", "gitlab"},
	{"dev.azure.com", "azure"},
	{"visualstudio.com", "azure"},
	{"bitbucket.org", "bitbucketcloud"},
}

// almFromURL infers the DevOps platform identifier from an ALM URL.
// Returns "" when the host matches no known cloud platform.
func almFromURL(almURL string) string {
	lower := strings.ToLower(almURL)
	for _, p := range almHostPatterns {
		if strings.Contains(lower, p.host) {
			return p.alm
		}
	}
	return ""
}

// parseBoundOrganization pulls the ALM URL and the DevOps organization
// key out of an /api/alm_integration/show_bound_organization response:
//
//	{"almOrganization":{"key":"Acme","almUrl":"https://github.com/Acme",...}}
//
// Returns ("", "") when the payload carries no almOrganization object,
// which is how SonarQube Cloud answers for an unbound organization.
func parseBoundOrganization(raw json.RawMessage) (almURL, dopOrg string) {
	var wrapper struct {
		AlmOrganization struct {
			Key    string `json:"key"`
			AlmURL string `json:"almUrl"`
			URL    string `json:"url"`
		} `json:"almOrganization"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", ""
	}
	almURL = wrapper.AlmOrganization.AlmURL
	if almURL == "" {
		almURL = wrapper.AlmOrganization.URL
	}
	return almURL, wrapper.AlmOrganization.Key
}

// orgBinding is the resolved DevOps platform binding of a target org.
type orgBinding struct {
	Bound bool
	// ALM is the inferred platform identifier (github, gitlab, azure,
	// bitbucketcloud), matching the `alm` value SonarQube Server reports.
	ALM string
	// DevOpsOrg is the organization/workspace key on the DevOps platform.
	DevOpsOrg string
	URL       string
	// LookupError is non-empty when the binding could not be read at all
	// (issue #505). Bound is then false because nothing was observed,
	// NOT because the org was seen to be unbound.
	LookupError string
}

// runGetOrgBinding resolves, for every target organization, the DevOps
// platform organization it is bound to (issue #122). An org that is not
// bound yields a record with bound=false rather than being omitted, so
// matchProjectRepos can tell "not bound" apart from "not looked up".
//
// The lookup is best-effort: it only enables the optional project ALM
// binding, so no failure of it may abort the migration. Issue #505: an
// unbound org answers HTTP 500 here (see orgBindingNotBoundCodes), which
// the old code treated as fatal, so every migration into an unbound org
// died with "phase 2: task getOrgBinding: http: read on closed response
// body".
func runGetOrgBinding(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getOrgBinding", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			e.Logger.Debug("org api call: GET /api/alm_integration/show_bound_organization",
				"org", orgKey)
			raw, err := e.Raw.Get(ctx, "api/alm_integration/show_bound_organization",
				url.Values{"organization": {orgKey}})
			if err != nil {
				// A cancelled/expired run is being torn down — that must
				// still propagate, unlike a failure of this optional
				// lookup.
				if ctx.Err() != nil {
					return err
				}
				return w.WriteOne(e.unboundOrgRecord(orgKey, err))
			}
			return w.WriteOne(e.boundOrgRecord(orgKey, raw))
		})
}

// orgBindingNotBoundCodes are the statuses that ARE an answer: none of
// them can yield a binding, so each records a plain bound=false. Probed
// live against SonarQube Cloud for issue #505:
//
//	500  the organization exists but is NOT bound to a DevOps platform.
//	     This is SonarQube Cloud's normal answer for an unbound org, not
//	     a transient server fault — it comes back with
//	     {"errors":[{"msg":"An unexpected error occurred. Please try
//	     again later."}]} and is reproducible per org. Two of three
//	     probed orgs answered it, so it is the common case.
//	404  no organization with that key (NOT "unbound", as an earlier
//	     comment here claimed).
//	400/403  the token may not administer that organization.
var orgBindingNotBoundCodes = []int{400, 403, 404, 500}

// unboundOrgRecord builds the getOrgBinding record for an organization
// no binding could be read for, and logs it honestly.
//
// Anything outside orgBindingNotBoundCodes (a transport error, 502/503,
// an edition not serving the endpoint, a rate limit that outlived its
// retries) means the tool never found out. It is logged at WARN and
// recorded as lookup_error so the report can say "could not be read"
// instead of asserting an unbound org it never observed (issue #505).
func (e *Executor) unboundOrgRecord(orgKey string, err error) []byte {
	rec := map[string]any{"sonarcloud_org_key": orgKey, "bound": false}
	switch {
	case common.IsHTTPError(err, 404):
		e.Logger.Info("getOrgBinding: organization not found, no DevOps binding to replicate",
			"org", orgKey, "err", err)
	case common.IsHTTPError(err, orgBindingNotBoundCodes...):
		e.Logger.Info("getOrgBinding: organization is not bound to a DevOps platform",
			"org", orgKey, "err", err)
	default:
		rec["lookup_error"] = err.Error()
		e.Logger.Warn("getOrgBinding: could not read the organization's DevOps binding; "+
			"continuing without project DevOps bindings for this organization",
			"org", orgKey, "err", err)
	}
	out, _ := json.Marshal(rec)
	return out
}

// boundOrgRecord builds the getOrgBinding record from a successful
// show_bound_organization response. An empty almOrganization is how
// SonarQube Cloud answers for an org that is genuinely not bound.
func (e *Executor) boundOrgRecord(orgKey string, raw json.RawMessage) []byte {
	almURL, dopOrg := parseBoundOrganization(raw)
	alm := almFromURL(almURL)
	rec := map[string]any{
		"sonarcloud_org_key": orgKey,
		"bound":              dopOrg != "" || alm != "",
		"alm":                alm,
		"dop_organization":   dopOrg,
		"alm_url":            almURL,
	}
	e.Logger.Info("getOrgBinding: organization DevOps binding resolved",
		"org", orgKey, "alm", alm, "dop_organization", dopOrg)
	out, _ := json.Marshal(rec)
	return out
}

// loadOrgRepos groups the repositories read by getOrgRepos by target
// organization key. Marker records written when the listing itself
// failed (issue #505) are returned separately, keyed by org, rather than
// being mistaken for repositories.
func (e *Executor) loadOrgRepos() (byOrg map[string][]json.RawMessage, failed map[string]string) {
	items, _ := e.Store.ReadAll("getOrgRepos")
	byOrg = make(map[string][]json.RawMessage)
	failed = make(map[string]string)
	for _, r := range items {
		orgKey := extractField(r, "sonarcloud_org_key")
		if msg := extractField(r, "repos_lookup_error"); msg != "" {
			failed[orgKey] = msg
			continue
		}
		byOrg[orgKey] = append(byOrg[orgKey], r)
	}
	return byOrg, failed
}

// loadOrgBindings indexes the getOrgBinding records by target
// organization key (issue #122).
func (e *Executor) loadOrgBindings() map[string]orgBinding {
	items, _ := e.Store.ReadAll("getOrgBinding")
	out := make(map[string]orgBinding, len(items))
	for _, b := range items {
		out[extractField(b, "sonarcloud_org_key")] = orgBinding{
			Bound:       extractBool(b, "bound"),
			ALM:         extractField(b, "alm"),
			DevOpsOrg:   extractField(b, "dop_organization"),
			URL:         extractField(b, "alm_url"),
			LookupError: extractField(b, "lookup_error"),
		}
	}
	return out
}

// orgContext is everything matchProjectRepos knows about one target
// organization: its own DevOps binding, the repositories of the bound
// DevOps organization, and why either lookup failed (issue #505).
type orgContext struct {
	Binding    orgBinding
	Repos      []json.RawMessage
	ReposError string
}

// loadOrgContexts joins the two best-effort org lookups per target org.
func (e *Executor) loadOrgContexts() map[string]orgContext {
	out := make(map[string]orgContext)
	set := func(org string, apply func(*orgContext)) {
		oc := out[org]
		apply(&oc)
		out[org] = oc
	}
	for org, b := range e.loadOrgBindings() {
		set(org, func(oc *orgContext) { oc.Binding = b })
	}
	repos, reposFailed := e.loadOrgRepos()
	for org, list := range repos {
		set(org, func(oc *orgContext) { oc.Repos = list })
	}
	for org, msg := range reposFailed {
		set(org, func(oc *orgContext) { oc.ReposError = msg })
	}
	return out
}

// loadProjectALMInfo indexes each mapped project's source DevOps binding
// by the cloud project key it will be created under.
func (e *Executor) loadProjectALMInfo() map[string]projectALMInfo {
	items, _ := e.Store.ReadAll("generateProjectMappings")
	out := make(map[string]projectALMInfo, len(items))
	for _, pm := range items {
		orgKey := extractField(pm, "sonarcloud_org_key")
		key := extractField(pm, "key")
		cloudKey := RenderProjectKey(e.ProjectKeyPattern, key, orgKey)
		out[cloudKey] = projectALMInfo{
			ALM:        extractField(pm, "alm"),
			Repository: extractField(pm, "repository"),
			Slug:       extractField(pm, "slug"),
			IsCloud:    extractBool(pm, "is_cloud_binding"),
			Monorepo:   extractBool(pm, "monorepo"),
			OrgKey:     orgKey,
			SourceKey:  key,
		}
	}
	return out
}

func runMatchProjectRepos(ctx context.Context, e *Executor) error {
	projectItems, _ := e.Store.ReadAll("getProjectIds")
	orgs := e.loadOrgContexts()
	projALMInfo := e.loadProjectALMInfo()

	w, err := e.Store.Writer("matchProjectRepos")
	if err != nil {
		return err
	}

	for _, proj := range projectItems {
		projKey := extractField(proj, "key")
		orgKey := extractField(proj, "sonarcloud_org_key")

		info, ok := projALMInfo[projKey]
		// Issue #122: only attempt a binding when the source project is
		// itself bound to a cloud DevOps platform.
		if !ok || info.ALM == "" {
			// Not bound on the source: nothing to replicate and nothing
			// to report (#122).
			continue
		}
		if !info.IsCloud {
			// Bound on the source, but to an on-premise platform that
			// SonarQube Cloud cannot integrate with at all. Silently
			// dropping this left the project reported as fully migrated
			// even though a real binding did not come across (#505).
			e.Logger.Warn("matchProjectRepos: source binding is to an on-premise DevOps platform, which SonarQube Cloud cannot bind to",
				"project", projKey, "org", orgKey,
				"alm", info.ALM, "repository", info.Repository)
			writeBindingSkip(w, projKey, orgKey, info,
				bindingSkip{Reason: BindingSkipOnPremPlatform})
			continue
		}

		repoID, projID, skip := e.resolveBindingTargets(
			ctx, proj, projKey, orgKey, info, orgs[orgKey])
		if skip.Reason != "" {
			writeBindingSkip(w, projKey, orgKey, info, skip)
			continue
		}

		result, _ := json.Marshal(map[string]any{
			"project_id":         projID,
			"repository_id":      repoID,
			"cloud_project_key":  projKey,
			"sonarcloud_org_key": orgKey,
			"alm":                info.ALM,
			"repository":         info.Repository,
			"monorepo":           info.Monorepo,
		})
		_ = w.WriteOne(result)
	}
	return nil
}

// writeBindingSkip records that a project's DevOps binding could not be
// replicated. internal/report/summary reads skip_detail — plus
// skip_error when a lookup failure caused the skip (#505) — into the
// project's Details column and marks it partially migrated.
func writeBindingSkip(w *common.ChunkWriter, cloudKey, orgKey string,
	info projectALMInfo, skip bindingSkip) {

	rec := map[string]any{
		"cloud_project_key":  cloudKey,
		"sonarcloud_org_key": orgKey,
		"binding_skipped":    true,
		"skip_reason":        skip.Reason,
		"skip_detail":        BindingSkipDetail[skip.Reason],
		"alm":                info.ALM,
		"repository":         info.Repository,
	}
	if skip.Err != "" {
		rec["skip_error"] = skip.Err
	}
	out, _ := json.Marshal(rec)
	_ = w.WriteOne(out)
}

// resolveBindingTargets decides what a bound source project can be bound to
// on SonarQube Cloud. It returns the repository identifier and the cloud
// project id, or a bindingSkip explaining why no binding is possible
// (issue #122).
func (e *Executor) resolveBindingTargets(ctx context.Context, proj json.RawMessage,
	projKey, orgKey string, info projectALMInfo,
	oc orgContext) (repoID, projID string, skip bindingSkip) {

	// Only attempt a binding when the target org is bound to the same
	// DevOps platform: SonarQube Cloud can only bind a project to a
	// repository of the DevOps organization its own org is bound to.
	if !oc.Binding.Bound || !strings.EqualFold(oc.Binding.ALM, info.ALM) {
		return "", "", e.orgBindingSkip(projKey, orgKey, info, oc.Binding)
	}

	repoID = MatchDevOpsPlatform(info.ALM, info.Repository, info.Slug, oc.Repos)
	if repoID == "" {
		return "", "", e.repoMatchSkip(projKey, orgKey, info, oc)
	}

	// SonarQube Cloud's /api/projects/search does not return an internal
	// project id, so resolve it explicitly.
	projID = extractField(proj, "id")
	if projID == "" {
		projID = e.resolveCloudProjectID(ctx, projKey)
	}
	if projID == "" {
		e.Logger.Warn("matchProjectRepos: could not resolve the cloud project id",
			"project", projKey, "org", orgKey)
		return "", "", bindingSkip{Reason: BindingSkipNoProjectID}
	}
	return repoID, projID, bindingSkip{}
}

// orgBindingSkip separates "the target org is genuinely not bound to the
// project's DevOps platform" from "the org's binding could never be
// read" (issue #505): reporting the second as the first states something
// the tool never observed.
func (e *Executor) orgBindingSkip(projKey, orgKey string,
	info projectALMInfo, ob orgBinding) bindingSkip {

	if ob.LookupError != "" {
		e.Logger.Warn("matchProjectRepos: the target organization's DevOps binding could not be read, skipping the project binding",
			"project", projKey, "org", orgKey,
			"project_alm", info.ALM, "err", ob.LookupError)
		return bindingSkip{Reason: BindingSkipOrgBindingUnknown, Err: ob.LookupError}
	}
	e.Logger.Warn("matchProjectRepos: target organization is not bound to the project's DevOps platform",
		"project", projKey, "org", orgKey,
		"project_alm", info.ALM, "org_alm", ob.ALM, "org_bound", ob.Bound)
	return bindingSkip{Reason: BindingSkipOrgNotBound}
}

// repoMatchSkip separates "the repository is not in the bound DevOps
// organization" from "the repositories could not be listed at all"
// (issue #505) — an empty candidate list means nothing when the listing
// itself failed.
func (e *Executor) repoMatchSkip(projKey, orgKey string,
	info projectALMInfo, oc orgContext) bindingSkip {

	if oc.ReposError != "" {
		e.Logger.Warn("matchProjectRepos: the bound DevOps organization's repositories could not be listed, skipping the project binding",
			"project", projKey, "org", orgKey, "alm", info.ALM,
			"repository", info.Repository, "err", oc.ReposError)
		return bindingSkip{Reason: BindingSkipReposUnknown, Err: oc.ReposError}
	}
	e.Logger.Warn("matchProjectRepos: no matching repository in the bound DevOps organization",
		"project", projKey, "org", orgKey, "alm", info.ALM,
		"repository", info.Repository, "slug", info.Slug,
		"dop_organization", oc.Binding.DevOpsOrg, "candidates", len(oc.Repos))
	return bindingSkip{Reason: BindingSkipRepoNotFound}
}

// resolveCloudProjectID looks up a SonarQube Cloud project's internal id.
// /api/projects/search omits it on Cloud, so fall back to
// /api/navigation/component, which returns it as `id`.
func (e *Executor) resolveCloudProjectID(ctx context.Context, cloudProjectKey string) string {
	raw, err := e.Raw.Get(ctx, "api/navigation/component",
		url.Values{"component": {cloudProjectKey}})
	if err != nil {
		e.Logger.Warn("resolveCloudProjectID failed", "project", cloudProjectKey, "err", err)
		return ""
	}
	return common.ExtractField(raw, "id")
}

func runSetProjectBinding(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "setProjectBinding", "matchProjectRepos",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			// Skip records emitted by matchProjectRepos carry no ids;
			// forward them verbatim so the report can pick them up.
			if extractBool(item, "binding_skipped") {
				return w.WriteOne(item)
			}
			projID := extractField(item, "project_id")
			repoID := extractField(item, "repository_id")
			cloudKey := extractField(item, "cloud_project_key")
			if projID == "" || repoID == "" {
				return nil
			}

			e.Logger.Debug("project api call: POST /dop-translation/project-bindings",
				"project", cloudKey, "project_id", projID, "repository_id", repoID)
			// The DOP Translation API is only served by the Cloud
			// enterprise host (api.sonarcloud.io); the standard host
			// answers the SPA index for this path (issue #122).
			err := e.CloudAPI.DOP.CreateProjectBinding(ctx, cloud.ProjectBindingParams{
				ProjectID:    projID,
				RepositoryID: repoID,
			})
			rec := map[string]any{
				"cloud_project_key":  cloudKey,
				"sonarcloud_org_key": extractField(item, "sonarcloud_org_key"),
				"project_id":         projID,
				"repository_id":      repoID,
				"alm":                extractField(item, "alm"),
			}
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setProjectBinding failed", err,
					"project", cloudKey, "project_id", projID, "repo", repoID)
				rec["status"] = "failed"
				rec["error"] = err.Error()
			} else {
				counter.Success()
				e.Logger.Info("setProjectBinding: DevOps platform binding created",
					"project", cloudKey, "repository", repoID)
				rec["status"] = "success"
			}
			out, _ := json.Marshal(rec)
			return w.WriteOne(out)
		})
	return err
}

type projectALMInfo struct {
	ALM        string
	Repository string
	Slug       string
	IsCloud    bool
	Monorepo   bool
	OrgKey     string
	SourceKey  string
}

// repoIdentity is the set of identifiers a SonarQube Cloud
// /api/alm_integration/list_repositories entry can be matched on.
type repoIdentity struct {
	// Slug is the fully qualified repository path (e.g. "org/repo" on
	// GitHub). This is the value the DOP Translation API expects as
	// repositoryId.
	Slug string
	// Label is the short display name of the repository.
	Label string
	// ID is the platform's numeric/opaque repository id, taken from the
	// `installationKey` suffix ("<slug>|<id>") or a bare `id` field.
	ID string
}

// repoIdentityOf normalises a list_repositories entry. SonarQube Cloud
// returns {label, slug, installationKey, linkedProjects, private}; older
// shapes (and the unit-test fixtures) may carry a bare `id`.
func repoIdentityOf(repo json.RawMessage) repoIdentity {
	ri := repoIdentity{
		Slug:  extractField(repo, "slug"),
		Label: extractField(repo, "label"),
		ID:    extractField(repo, "id"),
	}
	if key := extractField(repo, "installationKey"); key != "" {
		// installationKey is "<slug>|<platform repository id>".
		if idx := strings.LastIndex(key, "|"); idx >= 0 {
			if ri.Slug == "" {
				ri.Slug = key[:idx]
			}
			if ri.ID == "" {
				ri.ID = key[idx+1:]
			}
		} else if ri.Slug == "" {
			ri.Slug = key
		}
	}
	return ri
}

// bindingIDFor returns the identifier to send as repositoryId when
// creating the DOP binding for the given platform.
//
// SonarQube Cloud resolves the repository by calling the DevOps platform
// with this value, so it must be the identifier that platform's API
// expects:
//
//	GitHub / Azure DevOps / Bitbucket Cloud  fully qualified slug
//	                                        ("owner/repo"). Verified
//	                                        live against GitHub: posting
//	                                        the numeric id yields
//	                                        "Call to GitHub on endpoint
//	                                        .../repos/<id> failed with
//	                                        status code 404".
//	GitLab                                  numeric project id, which is
//	                                        also what a SonarQube Server
//	                                        GitLab binding stores.
func (r repoIdentity) bindingIDFor(platform string) string {
	ordered := []string{r.Slug, r.ID, r.Label}
	if platform == "gitlab" {
		ordered = []string{r.ID, r.Slug, r.Label}
	}
	for _, v := range ordered {
		if v != "" {
			return v
		}
	}
	return ""
}

// lastSegment returns the text after the final "/" — the bare repository
// name of a fully qualified slug.
func lastSegment(s string) string {
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// MatchDevOpsPlatform matches a source project's DevOps binding to a
// repository of the target organization's bound DevOps platform, and
// returns the identifier to bind to (empty string when nothing matches).
//
// The identifiers carried by a SonarQube Server binding differ per
// platform (issue #122):
//
//	GitHub          repository = "owner/repo"
//	GitLab          repository = numeric GitLab project id
//	Azure DevOps    slug = ADO project name, repository = repository name
//	Bitbucket Cloud repository = repository slug
//
// Matching is deliberately two-pass: an exact fully-qualified match is
// preferred, and only when that finds nothing do we fall back to the
// bare repository name. The fallback is what makes a migration from
// DevOps organization A into a Cloud org bound to DevOps organization B
// bind successfully, and it is safe because list_repositories only ever
// returns repositories of the single bound organization.
func MatchDevOpsPlatform(alm, repository, slug string, repos []json.RawMessage) string {
	if repository == "" && slug == "" {
		return ""
	}
	platform := strings.ToLower(alm)

	passes := []func(string, string, repoIdentity) bool{
		exactRepoMatch(platform),
		fallbackRepoMatch(platform),
	}
	for _, matches := range passes {
		for _, repo := range repos {
			ri := repoIdentityOf(repo)
			if matches(repository, slug, ri) {
				return ri.bindingIDFor(platform)
			}
		}
	}
	return ""
}

// exactRepoMatch returns the fully-qualified matcher for a platform: the
// source binding's identifier must equal the target repository's own
// identifier exactly.
func exactRepoMatch(platform string) func(repository, slug string, r repoIdentity) bool {
	switch platform {
	case "github":
		return func(repository, _ string, r repoIdentity) bool {
			return eqIdent(repository, r.Slug) || eqIdent(repository, r.ID)
		}
	case "gitlab":
		// SonarQube Server stores the numeric GitLab project id.
		return func(repository, _ string, r repoIdentity) bool {
			return eqIdent(repository, r.ID) || eqIdent(repository, r.Slug)
		}
	case "bitbucketcloud":
		return func(repository, _ string, r repoIdentity) bool {
			return eqIdent(repository, r.Slug) || eqIdent(repository, r.Label) ||
				eqIdent(repository, r.ID)
		}
	case "azure":
		// SonarQube Cloud labels Azure repositories
		// "<project name> / <repository name>".
		return func(repository, slug string, r repoIdentity) bool {
			return eqIdent(slug+" / "+repository, r.Label) ||
				eqIdent(slug+"/"+repository, r.Slug) ||
				eqIdent(slug+" / "+repository, r.Slug)
		}
	}
	return neverMatches
}

// fallbackRepoMatch returns the bare-repository-name matcher for a
// platform, used only when no exact match was found. GitLab has no
// fallback: its bindings carry an opaque numeric id, so guessing by name
// would risk binding to the wrong repository.
func fallbackRepoMatch(platform string) func(repository, slug string, r repoIdentity) bool {
	switch platform {
	case "github", "bitbucketcloud":
		return func(repository, _ string, r repoIdentity) bool {
			name := lastSegment(repository)
			return eqIdent(name, r.Label) || eqIdent(name, lastSegment(r.Slug))
		}
	case "azure":
		return func(repository, _ string, r repoIdentity) bool {
			return eqIdent(repository, r.Label) ||
				eqIdent(repository, lastSegment(r.Slug))
		}
	}
	return neverMatches
}

func neverMatches(_, _ string, _ repoIdentity) bool { return false }

// eqIdent compares two DevOps identifiers case-insensitively, ignoring
// surrounding whitespace. Empty values never match.
func eqIdent(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
