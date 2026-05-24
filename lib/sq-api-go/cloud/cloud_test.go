package cloud_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sq-api-go/types"
)

// Test constants for duplicated string literals (go:S1192).
const (
	pathProjectsCreate  = "/api/projects/create"
	pathGroupsCreate    = "/api/user_groups/create"
	pathPermsCreateTmpl = "/api/permissions/create_template"
	pathRulesUpdate     = "/api/rules/update"
	pathSettingsSet     = "/api/settings/set"
	pathEntPortfolios   = "/enterprises/portfolios"
	pathEntPortfolioP1  = "/enterprises/portfolios/p1"
	nameProjectOne      = "Project One"
	nameCustomJava      = "Custom Java"
	nameMyProfile       = "My Profile"
	nameMyGate          = "My Gate"
	nameMyTemplate      = "My Template"
	nameMyPortfolio     = "My Portfolio"
	testRuleKey         = "java:S1234"
)

// newTestCloud creates a cloud.Client backed by an httptest server using mux.
func newTestCloud(t *testing.T, mux *http.ServeMux) *cloud.Client {
	t.Helper()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	base := sqapi.NewCloudClient(ts.URL, "test-token")
	return cloud.New(base)
}

// writeJSON encodes v as JSON to w.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// assertFormValue checks a POST form parameter.
func assertFormValue(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	assert.Equal(t, want, r.FormValue(key), "form param %q", key)
}

// --- Projects ---

func TestProjectsCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathProjectsCreate, func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "project", "org_proj1")
		assertFormValue(t, r, "name", nameProjectOne)
		assertFormValue(t, r, "organization", "myorg")
		assertFormValue(t, r, "visibility", "public")
		writeJSON(w, types.ProjectCreateResponse{
			Project: types.Project{Key: "org_proj1", Name: nameProjectOne},
		})
	})
	cc := newTestCloud(t, mux)

	proj, err := cc.Projects.Create(context.Background(), cloud.CreateProjectParams{
		ProjectKey:   "org_proj1",
		Name:         nameProjectOne,
		Organization: "myorg",
		Visibility:   "public",
	})
	require.NoError(t, err)
	assert.Equal(t, "org_proj1", proj.Key)
	assert.Equal(t, nameProjectOne, proj.Name)
}

func TestProjectsCreateWithNewCode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathProjectsCreate, func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "newCodeDefinitionType", "NUMBER_OF_DAYS")
		assertFormValue(t, r, "newCodeDefinitionValue", "30")
		writeJSON(w, types.ProjectCreateResponse{
			Project: types.Project{Key: "p1"},
		})
	})
	cc := newTestCloud(t, mux)

	proj, err := cc.Projects.Create(context.Background(), cloud.CreateProjectParams{
		ProjectKey:             "p1",
		Name:                   "P",
		Organization:           "o",
		NewCodeDefinitionType:  "NUMBER_OF_DAYS",
		NewCodeDefinitionValue: "30",
	})
	require.NoError(t, err)
	assert.Equal(t, "p1", proj.Key)
}

// SQC rejects /api/projects/create requests that include
// newCodeDefinitionType without newCodeDefinitionValue (HTTP 400 "Both
// newCodeDefinitionType and newCodeDefinitionValue must be provided"). For
// "previous_version" — the SQC default, which has no value — every project
// in the migration plan would otherwise cascade-fail and break downstream
// tasks (setProjectSettings, setProjectGates, etc.). The SDK now omits both
// fields whenever either side is empty.
func TestProjectsCreateOmitsNewCodeWhenValueMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathProjectsCreate, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assert.Empty(t, r.Form["newCodeDefinitionType"],
			"type must be omitted when value is empty (SQC rejects the half-set pair)")
		assert.Empty(t, r.Form["newCodeDefinitionValue"])
		writeJSON(w, types.ProjectCreateResponse{Project: types.Project{Key: "p1"}})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Projects.Create(context.Background(), cloud.CreateProjectParams{
		ProjectKey:            "p1",
		Name:                  "P",
		Organization:          "o",
		NewCodeDefinitionType: "previous_version",
	})
	require.NoError(t, err)
}

func TestProjectsCreateError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathProjectsCreate, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Projects.Create(context.Background(), cloud.CreateProjectParams{
		ProjectKey: "p", Name: "P", Organization: "o",
	})
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestProjectsDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/delete", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "project", "proj1")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Projects.Delete(context.Background(), "proj1")
	require.NoError(t, err)
}

func TestProjectsDeleteError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/delete", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	cc := newTestCloud(t, mux)

	err := cc.Projects.Delete(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

func TestProjectsSetTags(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_tags/set", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "project", "proj1")
		assertFormValue(t, r, "tags", "java,backend")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Projects.SetTags(context.Background(), "proj1", "java,backend")
	require.NoError(t, err)
}

// --- Groups ---

func TestGroupsCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathGroupsCreate, func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "name", "devs")
		assertFormValue(t, r, "organization", "myorg")
		assertFormValue(t, r, "description", "Developers")
		writeJSON(w, types.GroupCreateResponse{
			Group: types.Group{ID: 42, Name: "devs"},
		})
	})
	cc := newTestCloud(t, mux)

	group, err := cc.Groups.Create(context.Background(), cloud.CreateGroupParams{
		Name: "devs", Description: "Developers", Organization: "myorg",
	})
	require.NoError(t, err)
	assert.Equal(t, "devs", group.Name)
	assert.Equal(t, 42, group.ID)
}

func TestGroupsCreateNoDescription(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathGroupsCreate, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Empty(t, r.FormValue("description"))
		writeJSON(w, types.GroupCreateResponse{
			Group: types.Group{ID: 1, Name: "g"},
		})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Groups.Create(context.Background(), cloud.CreateGroupParams{
		Name: "g", Organization: "o",
	})
	require.NoError(t, err)
}

func TestGroupsCreateError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathGroupsCreate, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Groups.Create(context.Background(), cloud.CreateGroupParams{
		Name: "g", Organization: "o",
	})
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestGroupsDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user_groups/delete", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "id", "42")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Groups.Delete(context.Background(), 42, "myorg")
	require.NoError(t, err)
}

func TestGroupsAddUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user_groups/add_user", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "name", "devs")
		assertFormValue(t, r, "login", "alice")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Groups.AddUser(context.Background(), "devs", "alice", "myorg")
	require.NoError(t, err)
}

// --- QualityProfiles ---

func TestQualityProfilesCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/create", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "name", nameCustomJava)
		assertFormValue(t, r, "language", "java")
		assertFormValue(t, r, "organization", "myorg")
		writeJSON(w, types.QualityProfileCreateResponse{
			Profile: types.QualityProfile{Key: "qp1", Name: nameCustomJava, Language: "java"},
		})
	})
	cc := newTestCloud(t, mux)

	profile, err := cc.QualityProfiles.Create(context.Background(), cloud.CreateProfileParams{
		Name: nameCustomJava, Language: "java", Organization: "myorg",
	})
	require.NoError(t, err)
	assert.Equal(t, "qp1", profile.Key)
}

func TestQualityProfilesCreateError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.QualityProfiles.Create(context.Background(), cloud.CreateProfileParams{
		Name: "P", Language: "java", Organization: "o",
	})
	require.Error(t, err)
}

func TestQualityProfilesRestore(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/restore", func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("Content-Type"), "multipart/form-data")
		writeJSON(w, types.QualityProfileRestoreResponse{
			Profile: types.QualityProfile{Key: "restored1", Name: "Restored"},
		})
	})
	cc := newTestCloud(t, mux)

	profile, err := cc.QualityProfiles.Restore(context.Background(), "myorg", []byte("<profile/>"))
	require.NoError(t, err)
	assert.Equal(t, "restored1", profile.Key)
}

func TestQualityProfilesRestoreError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/restore", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Bad Request"}]}`, http.StatusBadRequest)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.QualityProfiles.Restore(context.Background(), "o", []byte("<bad/>"))
	require.Error(t, err)
}

func TestQualityProfilesDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/delete", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "language", "java")
		assertFormValue(t, r, "qualityProfile", "Old Profile")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityProfiles.Delete(context.Background(), "java", "Old Profile", "myorg")
	require.NoError(t, err)
}

func TestQualityProfilesSetDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/set_default", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "language", "java")
		assertFormValue(t, r, "qualityProfile", nameMyProfile)
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityProfiles.SetDefault(context.Background(), "java", nameMyProfile, "myorg")
	require.NoError(t, err)
}

func TestQualityProfilesChangeParent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/change_parent", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "language", "java")
		assertFormValue(t, r, "qualityProfile", "Child")
		assertFormValue(t, r, "parentQualityProfile", "Parent")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityProfiles.ChangeParent(context.Background(), "java", "Child", "Parent", "myorg")
	require.NoError(t, err)
}

func TestQualityProfilesAddProject(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/add_project", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "language", "java")
		assertFormValue(t, r, "qualityProfile", nameMyProfile)
		assertFormValue(t, r, "project", "proj1")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityProfiles.AddProject(context.Background(), "java", nameMyProfile, "proj1", "myorg")
	require.NoError(t, err)
}

func TestQualityProfilesAddGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualityprofiles/add_group", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "language", "java")
		assertFormValue(t, r, "qualityProfile", nameMyProfile)
		assertFormValue(t, r, "group", "devs")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityProfiles.AddGroup(context.Background(), "java", nameMyProfile, "devs", "myorg")
	require.NoError(t, err)
}

// --- QualityGates ---

func TestQualityGatesCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/create", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "name", nameMyGate)
		assertFormValue(t, r, "organization", "myorg")
		writeJSON(w, types.QualityGate{ID: 10, Name: nameMyGate})
	})
	cc := newTestCloud(t, mux)

	gate, err := cc.QualityGates.Create(context.Background(), nameMyGate, "myorg")
	require.NoError(t, err)
	assert.Equal(t, 10, gate.ID)
	assert.Equal(t, nameMyGate, gate.Name)
}

func TestQualityGatesCreateError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.QualityGates.Create(context.Background(), "G", "o")
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestQualityGatesCreateCondition(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "gateId", "10")
		assertFormValue(t, r, "metric", "coverage")
		assertFormValue(t, r, "op", "LT")
		assertFormValue(t, r, "error", "80")
		writeJSON(w, types.QualityGateCondition{ID: 1, Metric: "coverage", Op: "LT", Error: "80"})
	})
	cc := newTestCloud(t, mux)

	cond, err := cc.QualityGates.CreateCondition(context.Background(), cloud.CreateConditionParams{
		GateID: 10, Organization: "myorg", Metric: "coverage", Op: "LT", Error: "80",
	})
	require.NoError(t, err)
	assert.Equal(t, "coverage", cond.Metric)
}

func TestQualityGatesCreateConditionError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/create_condition", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.QualityGates.CreateCondition(context.Background(), cloud.CreateConditionParams{
		GateID: 10, Organization: "o", Metric: "coverage", Op: "LT", Error: "80",
	})
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestQualityGatesShow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/show", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "MyGate", r.URL.Query().Get("name"))
		assert.Equal(t, "myorg", r.URL.Query().Get("organization"))
		writeJSON(w, types.QualityGate{
			ID: 10, Name: "MyGate",
			Conditions: []types.QualityGateCondition{
				{ID: 100, Metric: "coverage", Op: "LT", Error: "80"},
				{ID: 101, Metric: "new_bugs", Op: "GT", Error: "0"},
			},
		})
	})
	cc := newTestCloud(t, mux)

	gate, err := cc.QualityGates.Show(context.Background(), "MyGate", "myorg")
	require.NoError(t, err)
	assert.Equal(t, 10, gate.ID)
	assert.Len(t, gate.Conditions, 2)
	assert.Equal(t, 100, gate.Conditions[0].ID)
}

func TestQualityGatesDeleteCondition(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/delete_condition", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "id", "100")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	require.NoError(t, cc.QualityGates.DeleteCondition(context.Background(), 100, "myorg"))
}

func TestQualityGatesDestroy(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/destroy", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "id", "10")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityGates.Destroy(context.Background(), 10, "myorg")
	require.NoError(t, err)
}

func TestQualityGatesSelect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/select", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "gateId", "10")
		assertFormValue(t, r, "projectKey", "proj1")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityGates.Select(context.Background(), 10, "proj1", "myorg")
	require.NoError(t, err)
}

func TestQualityGatesSetDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qualitygates/set_as_default", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "id", "10")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.QualityGates.SetDefault(context.Background(), 10, "myorg")
	require.NoError(t, err)
}

// --- Permissions ---

func TestPermissionsCreateTemplate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathPermsCreateTmpl, func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "name", nameMyTemplate)
		assertFormValue(t, r, "organization", "myorg")
		assertFormValue(t, r, "description", "A template")
		assertFormValue(t, r, "projectKeyPattern", "proj_*")
		writeJSON(w, types.PermissionTemplateCreateResponse{
			PermissionTemplate: types.PermissionTemplate{ID: "tmpl1", Name: nameMyTemplate},
		})
	})
	cc := newTestCloud(t, mux)

	tmpl, err := cc.Permissions.CreateTemplate(context.Background(), cloud.CreateTemplateParams{
		Name: nameMyTemplate, Description: "A template", Organization: "myorg", ProjectKeyPattern: "proj_*",
	})
	require.NoError(t, err)
	assert.Equal(t, "tmpl1", tmpl.ID)
}

func TestPermissionsCreateTemplateMinimal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathPermsCreateTmpl, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Empty(t, r.FormValue("description"))
		assert.Empty(t, r.FormValue("projectKeyPattern"))
		writeJSON(w, types.PermissionTemplateCreateResponse{
			PermissionTemplate: types.PermissionTemplate{ID: "t1", Name: "T"},
		})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Permissions.CreateTemplate(context.Background(), cloud.CreateTemplateParams{
		Name: "T", Organization: "o",
	})
	require.NoError(t, err)
}

func TestPermissionsCreateTemplateError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathPermsCreateTmpl, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Permissions.CreateTemplate(context.Background(), cloud.CreateTemplateParams{
		Name: "T", Organization: "o",
	})
	require.Error(t, err)
}

func TestPermissionsDeleteTemplate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/delete_template", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "templateId", "tmpl1")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Permissions.DeleteTemplate(context.Background(), "tmpl1", "myorg")
	require.NoError(t, err)
}

func TestPermissionsSetDefaultTemplate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/set_default_template", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "templateId", "tmpl1")
		assertFormValue(t, r, "qualifier", "TRK")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Permissions.SetDefaultTemplate(context.Background(), "tmpl1", "TRK", "myorg")
	require.NoError(t, err)
}

func TestPermissionsAddGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/add_group", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "groupName", "devs")
		assertFormValue(t, r, "permission", "admin")
		assertFormValue(t, r, "organization", "myorg")
		assertFormValue(t, r, "projectKey", "proj1")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Permissions.AddGroup(context.Background(), "devs", "admin", "myorg", "proj1")
	require.NoError(t, err)
}

func TestPermissionsAddGroupOrgLevel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/add_group", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Empty(t, r.FormValue("projectKey"))
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Permissions.AddGroup(context.Background(), "devs", "admin", "myorg", "")
	require.NoError(t, err)
}

func TestPermissionsAddGroupToTemplate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/permissions/add_group_to_template", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "templateId", "tmpl1")
		assertFormValue(t, r, "groupName", "devs")
		assertFormValue(t, r, "permission", "codeviewer")
		assertFormValue(t, r, "organization", "myorg")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Permissions.AddGroupToTemplate(context.Background(), "tmpl1", "devs", "codeviewer", "myorg")
	require.NoError(t, err)
}

// --- Branches ---

func TestBranchesRename(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_branches/rename", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "project", "proj1")
		assertFormValue(t, r, "name", "main")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Branches.Rename(context.Background(), "proj1", "main")
	require.NoError(t, err)
}

func TestBranchesRenameError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_branches/rename", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	cc := newTestCloud(t, mux)

	err := cc.Branches.Rename(context.Background(), "nonexistent", "main")
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

func TestBranchesListAndMainBranchID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "my-project", r.URL.Query().Get("project"))
		writeJSON(w, types.BranchesResponse{
			Branches: []types.Branch{
				{Name: "feature/foo", IsMain: false, BranchID: "feature-uuid"},
				{Name: "master", IsMain: true, BranchID: "main-uuid"},
			},
		})
	})
	cc := newTestCloud(t, mux)

	branches, err := cc.Branches.List(context.Background(), "my-project")
	require.NoError(t, err)
	assert.Len(t, branches, 2)
	assert.Equal(t, "main-uuid", branches[1].BranchID)

	id, err := cc.Branches.MainBranchID(context.Background(), "my-project")
	require.NoError(t, err)
	assert.Equal(t, "main-uuid", id)
}

func TestBranchesMainBranchIDMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/project_branches/list", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, types.BranchesResponse{
			Branches: []types.Branch{{Name: "feature/foo", IsMain: false, BranchID: "u1"}},
		})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Branches.MainBranchID(context.Background(), "my-project")
	require.Error(t, err)
}

func TestNewCodePeriodsSetDays(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/new_code_periods/set", func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "project", "p1")
		assertFormValue(t, r, "branch", "main")
		assertFormValue(t, r, "type", "days")
		assertFormValue(t, r, "value", "30")
		assertFormValue(t, r, "organization", "org1")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.NewCodePeriods.Set(context.Background(), cloud.SetNewCodePeriodParams{
		Project: "p1", Branch: "main", Type: "days", Value: "30", Organization: "org1",
	})
	require.NoError(t, err)
}

func TestNewCodePeriodsSetPreviousVersionOmitsValue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/new_code_periods/set", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Equal(t, "previous_version", r.FormValue("type"))
		assert.Equal(t, "", r.FormValue("value"), "value must be omitted for previous_version mode")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.NewCodePeriods.Set(context.Background(), cloud.SetNewCodePeriodParams{
		Project: "p1", Type: "previous_version", Organization: "org1",
	})
	require.NoError(t, err)
}

// --- Rules ---

func TestRulesUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathRulesUpdate, func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "key", testRuleKey)
		assertFormValue(t, r, "tags", "security,java")
		assertFormValue(t, r, "markdown_note", "Important rule")
		writeJSON(w, types.RuleShowResponse{
			Rule: types.Rule{Key: testRuleKey, Name: "Do something"},
		})
	})
	cc := newTestCloud(t, mux)

	rule, err := cc.Rules.Update(context.Background(), cloud.UpdateRuleParams{
		Key: testRuleKey, Organization: "myorg", Tags: "security,java", MarkdownNote: "Important rule",
	})
	require.NoError(t, err)
	assert.Equal(t, testRuleKey, rule.Key)
}

func TestRulesUpdateMinimal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathRulesUpdate, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Equal(t, testRuleKey, r.FormValue("key"))
		assert.Empty(t, r.FormValue("tags"))
		assert.Empty(t, r.FormValue("markdown_note"))
		writeJSON(w, types.RuleShowResponse{
			Rule: types.Rule{Key: testRuleKey},
		})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Rules.Update(context.Background(), cloud.UpdateRuleParams{Key: testRuleKey, Organization: "myorg"})
	require.NoError(t, err)
}

func TestRulesUpdateError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathRulesUpdate, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Rules.Update(context.Background(), cloud.UpdateRuleParams{Key: "nonexistent", Organization: "myorg"})
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

// --- Settings ---

func TestSettingsSet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathSettingsSet, func(w http.ResponseWriter, r *http.Request) {
		assertFormValue(t, r, "component", "proj1")
		assertFormValue(t, r, "key", "sonar.coverage.exclusions")
		assertFormValue(t, r, "value", "**/*.java")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Settings.Set(context.Background(), "proj1", "sonar.coverage.exclusions", "**/*.java", "myorg")
	require.NoError(t, err)
}

func TestSettingsSetError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathSettingsSet, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	err := cc.Settings.Set(context.Background(), "p", "k", "v", "")
	require.Error(t, err)
	assert.True(t, sqapi.IsForbidden(err))
}

func TestSettingsSetValues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathSettingsSet, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Equal(t, "proj1", r.FormValue("component"))
		assert.Equal(t, "sonar.exclusions", r.FormValue("key"))
		// Multi-value settings must be sent as repeated "values" form
		// parameters (not as a single comma-joined "value") so SonarQube
		// Cloud parses the list correctly.
		assert.Equal(t, []string{"a.java", "b.java", "c.java"}, r.Form["values"])
		assert.Empty(t, r.Form["value"], "single value param must not be present for multi-value settings")
		// SonarQube Cloud rejects requests that send both component AND
		// organization with HTTP 400 "Only component or organization can be
		// set, not both". Project-level scope is conveyed by component only.
		assert.Empty(t, r.Form["organization"], "organization must be omitted when component is set")
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Settings.SetValues(context.Background(), "proj1", "sonar.exclusions", []string{"a.java", "b.java", "c.java"}, "myorg")
	require.NoError(t, err)
}

func TestSettingsSetUsesOrgWhenComponentEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathSettingsSet, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Empty(t, r.Form["component"], "component must be absent for org-level settings")
		assert.Equal(t, "myorg", r.FormValue("organization"))
		assert.Equal(t, "sonar.example", r.FormValue("key"))
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Settings.Set(context.Background(), "", "sonar.example", "v", "myorg")
	require.NoError(t, err)
}

func TestSettingsSetFieldValues(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathSettingsSet, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		assert.Equal(t, "proj1", r.FormValue("component"))
		assert.Equal(t, "sonar.issue.ignore.allfile", r.FormValue("key"))
		// Each property-set entry is JSON-encoded and sent as a separate
		// "fieldValues" form parameter.
		fv := r.Form["fieldValues"]
		require.Len(t, fv, 2)
		var entries []map[string]any
		for _, raw := range fv {
			var m map[string]any
			require.NoError(t, json.Unmarshal([]byte(raw), &m))
			entries = append(entries, m)
		}
		assert.Equal(t, "Generated test", entries[0]["fileRegexp"])
		assert.Equal(t, "Mock data", entries[1]["fileRegexp"])
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Settings.SetFieldValues(context.Background(), "proj1", "sonar.issue.ignore.allfile",
		[]map[string]any{
			{"fileRegexp": "Generated test"},
			{"fileRegexp": "Mock data"},
		}, "myorg")
	require.NoError(t, err)
}

// TestSettingsListDefinitions exercises the SDK's read of
// /api/settings/list_definitions, which the migrate task uses to decide
// whether each setting key on the target SQC org expects a single value,
// repeated values, or a property-set fieldValues payload. The migration
// regression that motivated this endpoint was sonar.java.file.suffixes:
// SQS returns it as values=[...] but SQC defines it as a single STRING
// (multiValues=false), so the migrate tool needs SQC's view of the schema
// — not SQS's — to pick the right shape.
func TestSettingsListDefinitions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/settings/list_definitions", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "myorg", r.URL.Query().Get("organization"))
		writeJSON(w, types.SettingsListDefinitionsResponse{
			Definitions: []types.SettingDefinition{
				{Key: "sonar.exclusions", Type: "STRING", MultiValues: true},
				{Key: "sonar.java.file.suffixes", Type: "STRING", MultiValues: false},
				{Key: "sonar.issue.ignore.allfile", Type: "PROPERTY_SET", MultiValues: false},
			},
		})
	})
	cc := newTestCloud(t, mux)

	defs, err := cc.Settings.ListDefinitions(context.Background(), "myorg", "")
	require.NoError(t, err)
	require.Len(t, defs, 3)

	byKey := make(map[string]types.SettingDefinition, len(defs))
	for _, d := range defs {
		byKey[d.Key] = d
	}
	assert.True(t, byKey["sonar.exclusions"].MultiValues,
		"sonar.exclusions must round-trip multiValues=true")
	assert.False(t, byKey["sonar.java.file.suffixes"].MultiValues,
		"sonar.java.file.suffixes must round-trip multiValues=false — this is the bit that determines whether migrate sends value= or values=")
	assert.Equal(t, "PROPERTY_SET", byKey["sonar.issue.ignore.allfile"].Type)
}

// TestSettingsListDefinitionsWithComponent exercises the project-scope
// variant of /api/settings/list_definitions. SQC returns a SUPERSET of
// the org-scope response when a component (project key) is supplied —
// including language settings (sonar.java.file.suffixes, etc.) and
// external-analyzer settings that have no org-level counterpart.
// Issue #189/#191 migration uses the difference between the two
// responses to decide which SQS global settings need to be propagated
// to every SQC project.
func TestSettingsListDefinitionsWithComponent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/settings/list_definitions", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "myorg", r.URL.Query().Get("organization"))
		assert.Equal(t, "myorg_someproject", r.URL.Query().Get("component"),
			"the SDK must forward the project key as component= so SQC returns project-scope defs")
		writeJSON(w, types.SettingsListDefinitionsResponse{
			Definitions: []types.SettingDefinition{
				{Key: "sonar.java.file.suffixes", Type: "STRING", MultiValues: false},
			},
		})
	})
	cc := newTestCloud(t, mux)

	defs, err := cc.Settings.ListDefinitions(context.Background(), "myorg", "myorg_someproject")
	require.NoError(t, err)
	require.Len(t, defs, 1)
	assert.Equal(t, "sonar.java.file.suffixes", defs[0].Key)
}

// --- Enterprises ---

func TestEnterprisesList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/enterprises/enterprises", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(w, types.EnterprisesListResponse{
			Enterprises: []types.Enterprise{{ID: "ent1", Name: "My Enterprise"}},
		})
	})
	cc := newTestCloud(t, mux)

	enterprises, err := cc.Enterprises.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, enterprises, 1)
	assert.Equal(t, "ent1", enterprises[0].ID)
}

func TestEnterprisesListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/enterprises/enterprises", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Unauthorized"}]}`, http.StatusUnauthorized)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Enterprises.List(context.Background())
	require.Error(t, err)
	assert.True(t, sqapi.IsUnauthorized(err))
}

func TestEnterprisesCreatePortfolio(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolios, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		assert.Equal(t, "ent1", body["enterpriseId"])
		assert.Equal(t, nameMyPortfolio, body["name"])
		assert.Equal(t, "projects", body["selection"])
		writeJSON(w, types.Portfolio{ID: "p1", Name: nameMyPortfolio})
	})
	cc := newTestCloud(t, mux)

	portfolio, err := cc.Enterprises.CreatePortfolio(context.Background(), cloud.CreatePortfolioParams{
		EnterpriseID: "ent1", Name: nameMyPortfolio, Description: "A portfolio",
	})
	require.NoError(t, err)
	assert.Equal(t, "p1", portfolio.ID)
}

func TestEnterprisesCreatePortfolioCustomSelection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolios, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "tags", body["selection"])
		writeJSON(w, types.Portfolio{ID: "p1"})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Enterprises.CreatePortfolio(context.Background(), cloud.CreatePortfolioParams{
		EnterpriseID: "ent1", Name: "P", Selection: "tags",
	})
	require.NoError(t, err)
}

func TestEnterprisesCreatePortfolioError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolios, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Forbidden"}]}`, http.StatusForbidden)
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Enterprises.CreatePortfolio(context.Background(), cloud.CreatePortfolioParams{
		EnterpriseID: "e", Name: "P",
	})
	require.Error(t, err)
}

func TestEnterprisesUpdatePortfolioProjects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolioP1, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		var body map[string]any
		err := json.NewDecoder(r.Body).Decode(&body)
		require.NoError(t, err)
		projects := body["projects"].([]any)
		assert.Len(t, projects, 2)
		first := projects[0].(map[string]any)
		assert.Equal(t, "br-1", first["branchId"])
		// Selection / RegularExpression / Tags / OrganizationIDs were not set,
		// so they must NOT appear in the body.
		_, hasSelection := body["selection"]
		assert.False(t, hasSelection)
		_, hasRegex := body["regularExpression"]
		assert.False(t, hasRegex)
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Enterprises.UpdatePortfolio(context.Background(), cloud.UpdatePortfolioParams{
		PortfolioID: "p1",
		Projects: []cloud.PortfolioProjectRef{
			{BranchID: "br-1"}, {BranchID: "br-2"},
		},
	})
	require.NoError(t, err)
}

func TestEnterprisesUpdatePortfolioRegex(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolioP1, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "regex", body["selection"])
		assert.Equal(t, "^org1_backend-.*$", body["regularExpression"])
		// branchKey must be present even when empty — SQC rejects the PATCH
		// otherwise when selection is set.
		branchKey, hasBranchKey := body["branchKey"]
		assert.True(t, hasBranchKey, "branchKey must always travel with selection")
		assert.Equal(t, "", branchKey)
		orgs := body["organizationIds"].([]any)
		assert.Len(t, orgs, 1)
		assert.Equal(t, "org-uuid", orgs[0])
		// Empty Projects/Tags must be absent from the body.
		_, hasProjects := body["projects"]
		assert.False(t, hasProjects)
		_, hasTags := body["tags"]
		assert.False(t, hasTags)
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Enterprises.UpdatePortfolio(context.Background(), cloud.UpdatePortfolioParams{
		PortfolioID:       "p1",
		Selection:         "regex",
		RegularExpression: "^org1_backend-.*$",
		OrganizationIDs:   []string{"org-uuid"},
	})
	require.NoError(t, err)
}

func TestEnterprisesUpdatePortfolioError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolioP1, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	cc := newTestCloud(t, mux)

	err := cc.Enterprises.UpdatePortfolio(context.Background(), cloud.UpdatePortfolioParams{
		PortfolioID: "p1",
	})
	require.Error(t, err)
}

func TestEnterprisesDeletePortfolio(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolioP1, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.Enterprises.DeletePortfolio(context.Background(), "p1")
	require.NoError(t, err)
}

func TestEnterprisesDeletePortfolioError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolioP1, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Not found"}]}`, http.StatusNotFound)
	})
	cc := newTestCloud(t, mux)

	err := cc.Enterprises.DeletePortfolio(context.Background(), "p1")
	require.Error(t, err)
	assert.True(t, sqapi.IsNotFound(err))
}

func TestEnterprisesListPortfoliosPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathEntPortfolios, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		q := r.URL.Query()
		assert.Equal(t, "ent-1", q.Get("enterpriseId"))
		assert.Equal(t, "search-term", q.Get("q"))
		assert.Equal(t, "2", q.Get("pageIndex"))
		assert.Equal(t, "10", q.Get("pageSize"))
		writeJSON(w, types.PortfoliosListResponse{
			Portfolios: []types.Portfolio{
				{ID: "p1", Name: "Portfolio One"},
				{ID: "p2", Name: "Portfolio Two"},
			},
			Page: types.PortfoliosPage{PageIndex: 2, PageSize: 10, Total: 12},
		})
	})
	cc := newTestCloud(t, mux)

	resp, err := cc.Enterprises.ListPortfoliosPage(context.Background(), cloud.ListPortfoliosParams{
		EnterpriseID: "ent-1",
		Query:        "search-term",
		PageIndex:    2,
		PageSize:     10,
	})
	require.NoError(t, err)
	assert.Len(t, resp.Portfolios, 2)
	assert.Equal(t, "p1", resp.Portfolios[0].ID)
	assert.Equal(t, 12, resp.Page.Total)
}

func TestEnterprisesListPortfoliosPaginates(t *testing.T) {
	mux := http.NewServeMux()
	// Build a mock that returns 50 portfolios on page 1 and 5 on page 2.
	mux.HandleFunc(pathEntPortfolios, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "ent-1", r.URL.Query().Get("enterpriseId"))
		page := r.URL.Query().Get("pageIndex")
		switch page {
		case "1":
			items := make([]types.Portfolio, 50)
			for i := range items {
				items[i] = types.Portfolio{ID: "p1-" + strconv.Itoa(i)}
			}
			writeJSON(w, types.PortfoliosListResponse{
				Portfolios: items,
				Page:       types.PortfoliosPage{PageIndex: 1, PageSize: 50, Total: 55},
			})
		case "2":
			items := make([]types.Portfolio, 5)
			for i := range items {
				items[i] = types.Portfolio{ID: "p2-" + strconv.Itoa(i)}
			}
			writeJSON(w, types.PortfoliosListResponse{
				Portfolios: items,
				Page:       types.PortfoliosPage{PageIndex: 2, PageSize: 50, Total: 55},
			})
		default:
			t.Errorf("unexpected pageIndex %q", page)
		}
	})
	cc := newTestCloud(t, mux)

	all, err := cc.Enterprises.ListPortfolios(context.Background(), cloud.ListPortfoliosParams{
		EnterpriseID: "ent-1",
	})
	require.NoError(t, err)
	assert.Len(t, all, 55)
}

func TestOrganizationsLookupID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/organizations/search", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "my-org", r.URL.Query().Get("organizations"))
		writeJSON(w, types.OrganizationsSearchResponse{
			Organizations: []types.Organization{
				{ID: "uuid-1", Key: "my-org", Name: "My Org"},
			},
		})
	})
	cc := newTestCloud(t, mux)

	id, err := cc.Organizations.LookupID(context.Background(), "my-org")
	require.NoError(t, err)
	assert.Equal(t, "uuid-1", id)
}

func TestOrganizationsLookupIDNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/organizations/search", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, types.OrganizationsSearchResponse{})
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Organizations.LookupID(context.Background(), "missing-org")
	require.Error(t, err)
}

// TestOrganizationsUpdateLeakPeriod pins the contract used by issue #136:
// PATCH /organizations/{id} on api.sonarcloud.io with a JSON body
// carrying defaultLeakPeriodType and defaultLeakPeriod. The body must
// include ONLY the fields the caller set — sending name, description,
// etc. as their zero values would silently overwrite them.
func TestOrganizationsUpdateLeakPeriod(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotBody   map[string]any
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/organizations/organizations/", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	cc := newTestCloud(t, mux)

	days := "30"
	dtype := "days"
	err := cc.Organizations.UpdateOrganization(context.Background(), "uuid-1",
		cloud.UpdateOrganizationParams{
			DefaultLeakPeriod:     &days,
			DefaultLeakPeriodType: &dtype,
		})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPatch, gotMethod)
	assert.Equal(t, "/organizations/organizations/uuid-1", gotPath)
	assert.Equal(t, "30", gotBody["defaultLeakPeriod"])
	assert.Equal(t, "days", gotBody["defaultLeakPeriodType"])
	// Untouched fields must be absent — the PATCH is supposed to be
	// minimal so it doesn't clobber name/description/etc.
	for _, untouched := range []string{"name", "description", "url", "avatar",
		"newProjectPrivate", "onlyPrivateProjects"} {
		if _, present := gotBody[untouched]; present {
			t.Errorf("body must not include unset field %q, got %v", untouched, gotBody[untouched])
		}
	}
}

func TestOrganizationsUpdateRequiresID(t *testing.T) {
	mux := http.NewServeMux()
	cc := newTestCloud(t, mux)
	err := cc.Organizations.UpdateOrganization(context.Background(), "",
		cloud.UpdateOrganizationParams{})
	require.Error(t, err)
}

// --- DOP ---

func TestDOPCreateProjectBinding(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dop-translation/project-bindings", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		body, _ := io.ReadAll(r.Body)
		var data map[string]string
		_ = json.Unmarshal(body, &data)
		assert.Equal(t, "123", data["projectId"])
		assert.Equal(t, "repo456", data["repositoryId"])
		w.WriteHeader(http.StatusNoContent)
	})
	cc := newTestCloud(t, mux)

	err := cc.DOP.CreateProjectBinding(context.Background(), cloud.ProjectBindingParams{
		ProjectID: "123", RepositoryID: "repo456",
	})
	require.NoError(t, err)
}

func TestDOPCreateProjectBindingError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dop-translation/project-bindings", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":[{"msg":"Bad Request"}]}`, http.StatusBadRequest)
	})
	cc := newTestCloud(t, mux)

	err := cc.DOP.CreateProjectBinding(context.Background(), cloud.ProjectBindingParams{
		ProjectID: "1", RepositoryID: "2",
	})
	require.Error(t, err)
}

// --- Base client error paths ---

func TestBaseClientHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathProjectsCreate, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errors":[{"msg":"Internal Server Error"}]}`))
	})
	cc := newTestCloud(t, mux)

	_, err := cc.Projects.Create(context.Background(), cloud.CreateProjectParams{
		ProjectKey: "p", Name: "P", Organization: "o",
	})
	require.Error(t, err)
	var apiErr *sqapi.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.StatusCode)
}
