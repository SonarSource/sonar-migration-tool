package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/server"
	"github.com/sonar-solutions/sq-api-go/types"
)

const (
	testAPISettingsValues = "/api/settings/values"
	testSettingCovExcl    = "sonar.coverage.exclusions"
	testAPIViewsSearch    = "/api/views/search"
)

// newTestServer creates a server.Client backed by an httptest server using mux.
func newTestServer(t *testing.T, mux *http.ServeMux) *server.Client {
	t.Helper()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	base := sqapi.NewServerClient(ts.URL, "test-token", 10.7)
	return server.New(base)
}

// writeJSON encodes v as JSON to w.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- Permissions ---

func TestPermissionsSearchTemplates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, types.PermissionTemplatesResponse{
			PermissionTemplates: []types.PermissionTemplate{
				{ID: "tmpl1", Name: "Default"},
			},
		})
	})
	sc := newTestServer(t, mux)

	tmpls, err := sc.Permissions.SearchTemplates(context.Background())
	require.NoError(t, err)
	assert.Len(t, tmpls, 1)
	assert.Equal(t, "Default", tmpls[0].Name)
}

func TestPermissionsSearchTemplatesError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/search_templates", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Permissions.SearchTemplates(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestPermissionsTemplateGroups(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/template_groups", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Default", r.URL.Query().Get("templateName"))
		writeJSON(w, types.TemplateGroupsResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 1, PageIndex: 1, PageSize: 500}},
			Groups:        []types.TemplateGroup{{ID: "g1", Name: "sonar-users"}},
		})
	})
	sc := newTestServer(t, mux)

	groups, err := sc.Permissions.TemplateGroups(context.Background(), "Default").All(context.Background())
	require.NoError(t, err)
	assert.Len(t, groups, 1)
	assert.Equal(t, "sonar-users", groups[0].Name)
}

func TestPermissionsTemplateGroupsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/template_groups", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Permissions.TemplateGroups(context.Background(), "Default").All(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestPermissionsTemplateUsers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/template_users", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Default", r.URL.Query().Get("templateName"))
		writeJSON(w, types.TemplateUsersResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 1, PageIndex: 1, PageSize: 500}},
			Users:         []types.TemplateUser{{Login: "admin", Name: "Admin"}},
		})
	})
	sc := newTestServer(t, mux)

	users, err := sc.Permissions.TemplateUsers(context.Background(), "Default").All(context.Background())
	require.NoError(t, err)
	assert.Len(t, users, 1)
	assert.Equal(t, "admin", users[0].Login)
}

func TestPermissionsTemplateUsersError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/template_users", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Permissions.TemplateUsers(context.Background(), "Default").All(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Branches ---

func TestBranchesList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("project"))
		writeJSON(w, types.BranchesResponse{
			Branches: []types.Branch{{Name: "main", IsMain: true}},
		})
	})
	sc := newTestServer(t, mux)

	branches, err := sc.Branches.List(context.Background(), "myproject")
	require.NoError(t, err)
	assert.Len(t, branches, 1)
	assert.Equal(t, "main", branches[0].Name)
	assert.True(t, branches[0].IsMain)
}

func TestBranchesListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Branches.List(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

// --- Pull Requests ---

func TestPullRequestsList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_pull_requests/list", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("project"))
		writeJSON(w, types.PullRequestsResponse{
			PullRequests: []types.PullRequest{{Key: "42", Title: "Fix bug", Branch: "feature/fix"}},
		})
	})
	sc := newTestServer(t, mux)

	prs, err := sc.PullRequests.List(context.Background(), "myproject")
	require.NoError(t, err)
	assert.Len(t, prs, 1)
	assert.Equal(t, "42", prs[0].Key)
	assert.Equal(t, "Fix bug", prs[0].Title)
}

func TestPullRequestsListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_pull_requests/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.PullRequests.List(context.Background(), "myproject")
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Analyses ---

func TestAnalysesSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_analyses/search", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("project"))
		writeJSON(w, types.AnalysesSearchResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 1, PageIndex: 1, PageSize: 500}},
			Analyses:      []types.Analysis{{Key: "analysis1", Date: "2024-01-01T00:00:00+0000"}},
		})
	})
	sc := newTestServer(t, mux)

	analyses, err := sc.Analyses.Search(context.Background(), "myproject").All(context.Background())
	require.NoError(t, err)
	assert.Len(t, analyses, 1)
	assert.Equal(t, "analysis1", analyses[0].Key)
}

func TestAnalysesSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_analyses/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Analyses.Search(context.Background(), "myproject").All(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Issues ---

func TestIssuesSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("components"))
		writeJSON(w, types.IssuesSearchResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 2, PageIndex: 1, PageSize: 500}},
			Issues:        []types.Issue{{Key: "issue1", Rule: "java:S1234"}, {Key: "issue2", Rule: "java:S5678"}},
		})
	})
	sc := newTestServer(t, mux)

	issues, err := sc.Issues.Search(context.Background(), "myproject").All(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, "issue1", issues[0].Key)
}

func TestIssuesSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/issues/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Unauthorized"}]}`, http.StatusUnauthorized)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Issues.Search(context.Background(), "myproject").All(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsUnauthorized(err))
}

// --- Hotspots ---

func TestHotspotsSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("projectKey"))
		writeJSON(w, types.HotspotsSearchResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 1, PageIndex: 1, PageSize: 500}},
			Hotspots:      []types.Hotspot{{Key: "hotspot1", Status: "TO_REVIEW"}},
		})
	})
	sc := newTestServer(t, mux)

	hotspots, err := sc.Hotspots.Search(context.Background(), "myproject").All(context.Background())
	require.NoError(t, err)
	assert.Len(t, hotspots, 1)
	assert.Equal(t, "hotspot1", hotspots[0].Key)
}

func TestHotspotsSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Hotspots.Search(context.Background(), "myproject").All(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Measures ---

func TestMeasuresSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/measures/search", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "proj1,proj2", r.URL.Query().Get("projectKeys"))
		assert.Equal(t, "coverage,violations", r.URL.Query().Get("metricKeys"))
		writeJSON(w, types.MeasuresSearchResponse{
			Measures: []types.Measure{
				{Component: "proj1", Metric: "coverage", Value: "85.0"},
				{Component: "proj1", Metric: "violations", Value: "3"},
			},
		})
	})
	sc := newTestServer(t, mux)

	measures, err := sc.Measures.Search(context.Background(), []string{"proj1", "proj2"}, []string{"coverage", "violations"})
	require.NoError(t, err)
	assert.Len(t, measures, 2)
	assert.Equal(t, "coverage", measures[0].Metric)
	assert.Equal(t, "85.0", measures[0].Value)
}

func TestMeasuresSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/measures/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Measures.Search(context.Background(), []string{"proj1"}, []string{"coverage"})
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Settings ---

func TestSettingsValuesGlobal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(testAPISettingsValues, func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.Query().Get("component"))
		assert.Empty(t, r.URL.Query().Get("keys"))
		writeJSON(w, types.SettingsValuesResponse{
			Settings: []types.Setting{{Key: "sonar.core.serverBaseURL", Value: "http://sonar.example.com"}},
		})
	})
	sc := newTestServer(t, mux)

	settings, err := sc.Settings.Values(context.Background(), "", "")
	require.NoError(t, err)
	assert.Len(t, settings, 1)
	assert.Equal(t, "sonar.core.serverBaseURL", settings[0].Key)
}

func TestSettingsValuesWithComponentAndKeys(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(testAPISettingsValues, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("component"))
		assert.Equal(t, testSettingCovExcl, r.URL.Query().Get("keys"))
		writeJSON(w, types.SettingsValuesResponse{
			Settings: []types.Setting{{Key: testSettingCovExcl, Value: "**/*.java"}},
		})
	})
	sc := newTestServer(t, mux)

	settings, err := sc.Settings.Values(context.Background(), "myproject", testSettingCovExcl)
	require.NoError(t, err)
	assert.Len(t, settings, 1)
	assert.Equal(t, "**/*.java", settings[0].Value)
}

func TestSettingsValuesError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(testAPISettingsValues, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Settings.Values(context.Background(), "", "")
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// Global-settings migration (issue #186) needs each definition's
// defaultValue to decide whether the SQS-side value is customized. This
// test pins the JSON unmarshalling: the SDK must decode defaultValue,
// multiValues, and type into the typed struct.
func TestSettingsListDefinitions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/settings/list_definitions", func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.Query().Get("component"), "global call must not send a component")
		writeJSON(w, types.SettingsListDefinitionsResponse{
			Definitions: []types.SettingDefinition{
				{Key: "sonar.exclusions", Type: "STRING", MultiValues: true, DefaultValue: ""},
				{Key: "sonar.java.file.suffixes", Type: "STRING", MultiValues: false, DefaultValue: ".java,.jav"},
				{Key: "sonar.cleanasyoucode.enabled", Type: "BOOLEAN", MultiValues: false, DefaultValue: "true"},
			},
		})
	})
	sc := newTestServer(t, mux)

	defs, err := sc.Settings.ListDefinitions(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, defs, 3)

	byKey := make(map[string]types.SettingDefinition, len(defs))
	for _, d := range defs {
		byKey[d.Key] = d
	}
	assert.Equal(t, ".java,.jav", byKey["sonar.java.file.suffixes"].DefaultValue,
		"defaultValue must round-trip — this is how migrate detects customization")
	assert.True(t, byKey["sonar.exclusions"].MultiValues)
	assert.Equal(t, "BOOLEAN", byKey["sonar.cleanasyoucode.enabled"].Type)
}

// --- Plugins ---

func TestPluginsInstalled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/plugins/installed", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, types.PluginsInstalledResponse{
			Plugins: []types.Plugin{{Key: "java", Name: "Java", Version: "7.35.0.35076"}},
		})
	})
	sc := newTestServer(t, mux)

	plugins, err := sc.Plugins.Installed(context.Background())
	require.NoError(t, err)
	assert.Len(t, plugins, 1)
	assert.Equal(t, "java", plugins[0].Key)
}

func TestPluginsInstalledError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/plugins/installed", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Plugins.Installed(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Views ---

func TestViewsSearchWithQualifier(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(testAPIViewsSearch, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "VW", r.URL.Query().Get("qualifiers"))
		writeJSON(w, types.ViewsSearchResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 1, PageIndex: 1, PageSize: 500}},
			Views:         []types.View{{Key: "portfolio1", Name: "My Portfolio", Qualifier: "VW"}},
		})
	})
	sc := newTestServer(t, mux)

	views, err := sc.Views.Search(context.Background(), "VW").All(context.Background())
	require.NoError(t, err)
	assert.Len(t, views, 1)
	assert.Equal(t, "portfolio1", views[0].Key)
}

func TestViewsSearchNoQualifier(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(testAPIViewsSearch, func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.Query().Get("qualifiers"))
		writeJSON(w, types.ViewsSearchResponse{
			PagedResponse: types.PagedResponse{Paging: types.Paging{Total: 0}},
		})
	})
	sc := newTestServer(t, mux)

	views, err := sc.Views.Search(context.Background(), "").All(context.Background())
	require.NoError(t, err)
	assert.Empty(t, views)
}

func TestViewsSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(testAPIViewsSearch, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Views.Search(context.Background(), "VW").All(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

func TestViewsShow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/views/show", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "portfolio1", r.URL.Query().Get("key"))
		writeJSON(w, types.ViewDetails{Key: "portfolio1", Name: "My Portfolio", Qualifier: "VW"})
	})
	sc := newTestServer(t, mux)

	details, err := sc.Views.Show(context.Background(), "portfolio1")
	require.NoError(t, err)
	assert.Equal(t, "portfolio1", details.Key)
	assert.Equal(t, "VW", details.Qualifier)
}

func TestViewsShowError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/views/show", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Views.Show(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

// --- Webhooks ---

func TestWebhooksList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/webhooks/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, types.WebhooksListResponse{
			Webhooks: []types.Webhook{{Key: "wh1", Name: "CI webhook", URL: "https://ci.example.com"}},
		})
	})
	sc := newTestServer(t, mux)

	webhooks, err := sc.Webhooks.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, webhooks, 1)
	assert.Equal(t, "CI webhook", webhooks[0].Name)
}

func TestWebhooksListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/webhooks/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Webhooks.List(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- Tokens ---

func TestTokensSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user_tokens/search", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "admin", r.URL.Query().Get("login"))
		writeJSON(w, types.UserTokensResponse{
			Login:      "admin",
			UserTokens: []types.UserToken{{Name: "my-ci-token", Type: "PROJECT_ANALYSIS_TOKEN"}},
		})
	})
	sc := newTestServer(t, mux)

	tokens, err := sc.Tokens.Search(context.Background(), "admin")
	require.NoError(t, err)
	assert.Len(t, tokens, 1)
	assert.Equal(t, "my-ci-token", tokens[0].Name)
}

func TestTokensSearchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user_tokens/search", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.Tokens.Search(context.Background(), "admin")
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- NewCode ---

func TestNewCodeList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/new_code_periods/list", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("project"))
		writeJSON(w, types.NewCodePeriodsResponse{
			NewCodePeriods: []types.NewCodePeriod{{BranchKey: "main", Type: "PREVIOUS_VERSION"}},
		})
	})
	sc := newTestServer(t, mux)

	periods, err := sc.NewCode.List(context.Background(), "myproject")
	require.NoError(t, err)
	assert.Len(t, periods, 1)
	assert.Equal(t, "PREVIOUS_VERSION", periods[0].Type)
	assert.Equal(t, "main", periods[0].BranchKey)
}

func TestNewCodeListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/new_code_periods/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.NewCode.List(context.Background(), "myproject")
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

// --- ALM ---

func TestALMListSettings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/alm_settings/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, types.AlmSettingsResponse{
			AlmSettings: []types.AlmSetting{{Key: "github1", ALM: "github", URL: "https://github.com"}},
		})
	})
	sc := newTestServer(t, mux)

	settings, err := sc.ALM.ListSettings(context.Background())
	require.NoError(t, err)
	assert.Len(t, settings, 1)
	assert.Equal(t, "github", settings[0].ALM)
}

func TestALMListSettingsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/alm_settings/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	sc := newTestServer(t, mux)

	_, err := sc.ALM.ListSettings(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestALMGetBinding(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/alm_settings/get_binding", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myproject", r.URL.Query().Get("project"))
		writeJSON(w, types.AlmBinding{ALM: "github", Repository: "myorg/myrepo"})
	})
	sc := newTestServer(t, mux)

	binding, err := sc.ALM.GetBinding(context.Background(), "myproject")
	require.NoError(t, err)
	assert.Equal(t, "github", binding.ALM)
	assert.Equal(t, "myorg/myrepo", binding.Repository)
}

func TestALMGetBindingError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/alm_settings/get_binding", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	sc := newTestServer(t, mux)

	_, err := sc.ALM.GetBinding(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}
