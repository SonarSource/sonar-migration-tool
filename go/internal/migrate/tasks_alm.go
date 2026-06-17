// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// almTasks returns tasks for ALM (DevOps platform) binding.
func almTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "matchProjectRepos",
			Dependencies: []string{"getProjectIds", "getOrgRepos"},
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
			OrgKey:     orgKey,
		}
	}

	w, err := e.Store.Writer("matchProjectRepos")
	if err != nil {
		return err
	}

	for _, proj := range projectItems {
		projKey := extractField(proj, "key")
		orgKey := extractField(proj, "sonarcloud_org_key")
		projID := extractField(proj, "id")

		info, ok := projALMInfo[projKey]
		if !ok || !info.IsCloud || info.ALM == "" {
			continue
		}

		repos := reposByOrg[orgKey]
		repoID := MatchDevOpsPlatform(info.ALM, info.Repository, info.Slug, repos)
		if repoID == "" {
			continue
		}

		result, _ := json.Marshal(map[string]any{
			"project_id":         projID,
			"repository_id":      repoID,
			"cloud_project_key":  projKey,
			"sonarcloud_org_key": orgKey,
		})
		_ = w.WriteOne(result)
	}
	return nil
}

func runSetProjectBinding(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "setProjectBinding", "matchProjectRepos",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			projID := extractField(item, "project_id")
			repoID := extractField(item, "repository_id")
			if projID == "" || repoID == "" {
				return nil
			}

			e.Logger.Debug("project api call: POST /dop-translation/project-bindings",
				"project_id", projID, "repository_id", repoID)
			err := e.Cloud.DOP.CreateProjectBinding(ctx, cloud.ProjectBindingParams{
				ProjectID:    projID,
				RepositoryID: repoID,
			})
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setProjectBinding failed", err,
					"project", projID, "repo", repoID)
			} else {
				counter.Success()
			}
			return nil
		})
	return err
}

type projectALMInfo struct {
	ALM        string
	Repository string
	Slug       string
	IsCloud    bool
	OrgKey     string
}

// MatchDevOpsPlatform matches a project to a repository in the DevOps platform.
// Returns the repository ID (integration_key) or empty string if no match.
func MatchDevOpsPlatform(alm, repository, slug string, repos []json.RawMessage) string {
	for _, repo := range repos {
		repoSlug := extractField(repo, "slug")
		repoLabel := extractField(repo, "label")
		integrationKey := extractField(repo, "id")

		var matched bool
		switch strings.ToLower(alm) {
		case "github":
			matched = repository == repoSlug
		case "gitlab":
			matched = integrationKey == repository
		case "bitbucketcloud":
			matched = repository == repoLabel
		case "azure":
			matched = repoLabel == slug+" / "+repository
		}

		if matched {
			return integrationKey
		}
	}
	return ""
}
