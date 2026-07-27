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
)

// BindingSkipDetail maps a skip reason to the operator-facing sentence
// rendered in the migration report's Details column.
var BindingSkipDetail = map[string]string{
	BindingSkipOrgNotBound:  "project binding was not possible because the org itself is not bound",
	BindingSkipRepoNotFound: "project binding was not possible because the repository was not found in the bound DevOps organization",
	BindingSkipNoProjectID:  "project binding was not possible because the target project id could not be resolved",
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
}

// runGetOrgBinding resolves, for every target organization, the DevOps
// platform organization it is bound to (issue #122). An org that is not
// bound yields a record with bound=false rather than being omitted, so
// matchProjectRepos can tell "not bound" apart from "not looked up".
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
			rec := map[string]any{"sonarcloud_org_key": orgKey, "bound": false}
			if err != nil {
				// An unbound org answers 404 (and 400/403 when the token
				// cannot administer it). Either way the binding cannot
				// be replicated — record it as unbound rather than
				// failing the whole migration.
				if !common.IsHTTPError(err, 400, 403, 404) {
					return err
				}
				e.Logger.Info("getOrgBinding: organization is not bound to a DevOps platform",
					"org", orgKey, "err", err)
				out, _ := json.Marshal(rec)
				return w.WriteOne(out)
			}
			almURL, dopOrg := parseBoundOrganization(raw)
			alm := almFromURL(almURL)
			if dopOrg != "" || alm != "" {
				rec["bound"] = true
			}
			rec["alm"] = alm
			rec["dop_organization"] = dopOrg
			rec["alm_url"] = almURL
			e.Logger.Info("getOrgBinding: organization DevOps binding resolved",
				"org", orgKey, "alm", alm, "dop_organization", dopOrg)
			out, _ := json.Marshal(rec)
			return w.WriteOne(out)
		})
}

func runMatchProjectRepos(ctx context.Context, e *Executor) error {
	// Load project IDs.
	projectItems, _ := e.Store.ReadAll("getProjectIds")
	// Load org repos.
	repoItems, _ := e.Store.ReadAll("getOrgRepos")

	// Build repo lookup: orgKey -> []repo.
	reposByOrg := make(map[string][]json.RawMessage)
	for _, r := range repoItems {
		orgKey := extractField(r, "sonarcloud_org_key")
		reposByOrg[orgKey] = append(reposByOrg[orgKey], r)
	}

	// Build the org -> DevOps-platform-binding lookup (issue #122).
	orgBindings := make(map[string]orgBinding)
	bindingItems, _ := e.Store.ReadAll("getOrgBinding")
	for _, b := range bindingItems {
		orgBindings[extractField(b, "sonarcloud_org_key")] = orgBinding{
			Bound:     extractBool(b, "bound"),
			ALM:       extractField(b, "alm"),
			DevOpsOrg: extractField(b, "dop_organization"),
			URL:       extractField(b, "alm_url"),
		}
	}

	// Load project mappings to get ALM info.
	projMappings, _ := e.Store.ReadAll("generateProjectMappings")
	projALMInfo := make(map[string]projectALMInfo) // cloud_project_key -> ALM info
	for _, pm := range projMappings {
		orgKey := extractField(pm, "sonarcloud_org_key")
		key := extractField(pm, "key")
		cloudKey := RenderProjectKey(e.ProjectKeyPattern, key, orgKey)
		projALMInfo[cloudKey] = projectALMInfo{
			ALM:        extractField(pm, "alm"),
			Repository: extractField(pm, "repository"),
			Slug:       extractField(pm, "slug"),
			IsCloud:    extractBool(pm, "is_cloud_binding"),
			Monorepo:   extractBool(pm, "monorepo"),
			OrgKey:     orgKey,
			SourceKey:  key,
		}
	}

	w, err := e.Store.Writer("matchProjectRepos")
	if err != nil {
		return err
	}

	writeSkip := func(cloudKey, orgKey, reason string, extra map[string]any) {
		rec := map[string]any{
			"cloud_project_key":  cloudKey,
			"sonarcloud_org_key": orgKey,
			"binding_skipped":    true,
			"skip_reason":        reason,
			"skip_detail":        BindingSkipDetail[reason],
		}
		for k, v := range extra {
			rec[k] = v
		}
		out, _ := json.Marshal(rec)
		_ = w.WriteOne(out)
	}

	for _, proj := range projectItems {
		projKey := extractField(proj, "key")
		orgKey := extractField(proj, "sonarcloud_org_key")

		info, ok := projALMInfo[projKey]
		// Issue #122: only attempt a binding when the source project is
		// itself bound to a cloud DevOps platform.
		if !ok || !info.IsCloud || info.ALM == "" {
			continue
		}

		// Issue #122: only attempt a binding when the target org is bound
		// to the same DevOps platform. Otherwise record a skip so the
		// report can mark the project as partially migrated.
		ob := orgBindings[orgKey]
		if !ob.Bound || !strings.EqualFold(ob.ALM, info.ALM) {
			e.Logger.Warn("matchProjectRepos: target organization is not bound to the project's DevOps platform",
				"project", projKey, "org", orgKey,
				"project_alm", info.ALM, "org_alm", ob.ALM, "org_bound", ob.Bound)
			writeSkip(projKey, orgKey, BindingSkipOrgNotBound, map[string]any{
				"alm":     info.ALM,
				"org_alm": ob.ALM,
			})
			continue
		}

		repos := reposByOrg[orgKey]
		repoID := MatchDevOpsPlatform(info.ALM, info.Repository, info.Slug, repos)
		if repoID == "" {
			e.Logger.Warn("matchProjectRepos: no matching repository in the bound DevOps organization",
				"project", projKey, "org", orgKey, "alm", info.ALM,
				"repository", info.Repository, "slug", info.Slug,
				"dop_organization", ob.DevOpsOrg, "candidates", len(repos))
			writeSkip(projKey, orgKey, BindingSkipRepoNotFound, map[string]any{
				"alm":        info.ALM,
				"repository": info.Repository,
			})
			continue
		}

		// SonarQube Cloud's /api/projects/search does not return an
		// internal project id, so resolve it explicitly (issue #122).
		projID := extractField(proj, "id")
		if projID == "" {
			projID = e.resolveCloudProjectID(ctx, projKey)
		}
		if projID == "" {
			e.Logger.Warn("matchProjectRepos: could not resolve the cloud project id",
				"project", projKey, "org", orgKey)
			writeSkip(projKey, orgKey, BindingSkipNoProjectID, nil)
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

// bindingID returns the identifier to send as repositoryId when creating
// the DOP binding. SonarQube Cloud resolves the repository from its
// fully qualified slug; the numeric id is only a fallback for shapes
// that carry no slug.
func (r repoIdentity) bindingID() string {
	if r.Slug != "" {
		return r.Slug
	}
	if r.ID != "" {
		return r.ID
	}
	return r.Label
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

	exact := func(r repoIdentity) bool {
		switch platform {
		case "github":
			return eqIdent(repository, r.Slug) || eqIdent(repository, r.ID)
		case "gitlab":
			// SonarQube Server stores the numeric GitLab project id.
			return eqIdent(repository, r.ID) || eqIdent(repository, r.Slug)
		case "bitbucketcloud":
			return eqIdent(repository, r.Slug) || eqIdent(repository, r.Label) ||
				eqIdent(repository, r.ID)
		case "azure":
			// SonarQube Cloud labels Azure repositories
			// "<project name> / <repository name>".
			return eqIdent(slug+" / "+repository, r.Label) ||
				eqIdent(slug+"/"+repository, r.Slug) ||
				eqIdent(slug+" / "+repository, r.Slug)
		}
		return false
	}

	fallback := func(r repoIdentity) bool {
		switch platform {
		case "github", "bitbucketcloud":
			name := lastSegment(repository)
			return eqIdent(name, r.Label) || eqIdent(name, lastSegment(r.Slug))
		case "azure":
			return eqIdent(repository, r.Label) || eqIdent(repository, lastSegment(r.Slug))
		}
		// GitLab bindings only carry an opaque numeric id — guessing by
		// name would be wrong, so there is no fallback.
		return false
	}

	for _, pass := range []func(repoIdentity) bool{exact, fallback} {
		for _, repo := range repos {
			ri := repoIdentityOf(repo)
			if pass(ri) {
				return ri.bindingID()
			}
		}
	}
	return ""
}

// eqIdent compares two DevOps identifiers case-insensitively, ignoring
// surrounding whitespace. Empty values never match.
func eqIdent(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
