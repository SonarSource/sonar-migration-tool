package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sq-api-go/types"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// associateTasks returns tasks that associate projects with profiles, gates, etc.
func associateTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "setProjectProfiles",
			Dependencies: []string{"createProfiles", "createProjects"},
			Run:          runSetProjectProfiles,
		},
		{
			Name:         "setProjectGates",
			Dependencies: []string{"createGates", "createProjects"},
			Run:          runSetProjectGates,
		},
		{
			Name:         "setProjectGroupPermissions",
			Dependencies: []string{"createGroups", "createProjects"},
			Run:          runSetProjectGroupPermissions,
		},
		{
			Name:         "setProjectSettings",
			Dependencies: []string{"createProjects"},
			Run:          runSetProjectSettings,
		},
		{
			Name:         "setProjectTags",
			Dependencies: []string{"createProjects"},
			Run:          runSetProjectTags,
		},
		{
			Name:         "setNewCodePeriods",
			Dependencies: []string{"createProjects"},
			Run:          runSetNewCodePeriods,
		},
		{
			// Migrates non-default SQS global settings to every SQC org
			// in scope. Depends on the org mapping plus both halves of
			// the SQS settings extract (values + definitions, the latter
			// supplies the defaultValue used to detect customization).
			// Issue #186.
			Name:         "setGlobalSettings",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runSetGlobalSettings,
		},
	}
}

func runSetProjectProfiles(ctx context.Context, e *Executor) error {
	// Build profile lookup: orgKey+language+name -> true.
	profiles, _ := e.Store.ReadAll("createProfiles")
	profileLookup := make(map[string]bool)
	for _, p := range profiles {
		orgKey := extractField(p, "sonarcloud_org_key")
		lang := extractField(p, "language")
		name := extractField(p, "name")
		profileLookup[orgKey+lang+name] = true
	}

	counter := NewTaskCounter("setProjectProfiles")
	err := forEachMigrateItem(ctx, e, "setProjectProfiles", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			projectKey := extractField(item, "cloud_project_key")
			profilesArr := extractProfilesList(item)

			for _, p := range profilesArr {
				lang := getString(p, "language")
				name := getString(p, "name")
				if unsupportedLanguages[lang] || getBool(p, "deleted") {
					continue
				}
				if !profileLookup[orgKey+lang+name] {
					continue
				}
				e.Logger.Debug("project api call: POST /api/qualityprofiles/add_project",
					"project", projectKey, "language", lang, "profile", name, "org", orgKey)
				if err := e.Cloud.QualityProfiles.AddProject(ctx, lang, name, projectKey, orgKey); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "setProjectProfiles failed", err, "project", projectKey)
				} else {
					counter.Success()
				}
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runSetProjectGates(ctx context.Context, e *Executor) error {
	// Build gate lookup: orgKey+name -> gateID.
	gates, _ := e.Store.ReadAll("createGates")
	gateLookup := make(map[string]int)
	for _, g := range gates {
		orgKey := extractField(g, "sonarcloud_org_key")
		name := extractField(g, "name")
		id, _ := strconv.Atoi(extractField(g, "cloud_gate_id"))
		gateLookup[orgKey+name] = id
	}

	counter := NewTaskCounter("setProjectGates")
	err := forEachMigrateItem(ctx, e, "setProjectGates", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			projectKey := extractField(item, "cloud_project_key")
			gateName := extractField(item, "gate_name")
			if gateName == "" {
				return nil
			}
			gateID, ok := gateLookup[orgKey+gateName]
			if !ok {
				return nil
			}
			e.Logger.Debug("project api call: POST /api/qualitygates/select",
				"project", projectKey, "gate_id", gateID, "gate_name", gateName, "org", orgKey)
			if err := e.Cloud.QualityGates.Select(ctx, gateID, projectKey, orgKey); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setProjectGates failed", err, "project", projectKey)
			} else {
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runSetProjectGroupPermissions(ctx context.Context, e *Executor) error {
	// Build project key lookup from created projects.
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping) // serverURL+key -> mapping
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		projectKeyMap[serverURL+key] = projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
			OrgKey:   extractField(p, "sonarcloud_org_key"),
		}
	}

	counter := NewTaskCounter("setProjectGroupPermissions")
	err := forEachExtractItem(ctx, e, "setProjectGroupPermissions", "getProjectGroupsPermissions",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			project := extractField(item.Data, "project")
			pm, ok := projectKeyMap[item.ServerURL+project]
			if !ok {
				return nil
			}
			applyGroupPermissions(ctx, e, item.Data, pm, w, counter)
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func applyGroupPermissions(ctx context.Context, e *Executor, data json.RawMessage, pm projectMapping, w *common.ChunkWriter, counter *TaskCounter) {
	name := extractField(data, "name")
	permsRaw := extractPermissions(data)
	for _, perm := range permsRaw {
		if !validPermissions[perm] {
			continue
		}
		if err := e.Cloud.Permissions.AddGroup(ctx, name, perm, pm.OrgKey, pm.CloudKey); err != nil {
			counter.Fail()
			logAPIWarn(e.Logger, "setProjectGroupPermissions failed", err,
				"project", pm.CloudKey, "group", name, "perm", perm)
		} else {
			counter.Success()
		}
	}
	_ = w.WriteOne(common.EnrichRaw(data, map[string]any{
		"cloud_project_key": pm.CloudKey,
	}))
}

// runSetProjectSettings migrates every non-inherited project-level setting
// extracted from the source server, including the analysis-scope keys
// listed in issue #120 (sonar.exclusions, sonar.inclusions,
// sonar.coverage.exclusions, sonar.cpd.exclusions, sonar.<language>.*,
// sonar.scm.*, sonar.coverage.*, external-analyzer settings).
//
// SonarQube's /api/settings/values can return a setting in three shapes
// depending on its definition:
//
//   - "value":       single scalar value (e.g. sonar.cfamily.ignoreHeaderComments=false)
//   - "values":      multi-value array (e.g. sonar.exclusions=[a,b,c])
//   - "fieldValues": property-set array of objects (e.g.
//                    sonar.issue.ignore.allfile=[{fileRegexp:...}])
//
// Until this change only "value" was forwarded; multi-value and
// property-set settings were silently dropped. Each shape now routes to
// the matching SDK helper so the setting actually lands on SQC.
func runSetProjectSettings(ctx context.Context, e *Executor) error {
	// Build project lookup.
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping)
	orgs := make(map[string]struct{})
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		pm := projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
			OrgKey:   extractField(p, "sonarcloud_org_key"),
		}
		projectKeyMap[serverURL+key] = pm
		if pm.OrgKey != "" {
			orgs[pm.OrgKey] = struct{}{}
		}
	}

	// Pre-fetch SQC's setting definitions per org we will write to. SQS and
	// SQC don't always agree on whether a setting is multi-value or stored
	// as a single CSV string (sonar.java.file.suffixes is the canonical
	// example: SQS exposes it as values=[.java,.jav] but SQC defines it as
	// STRING + multiValues=false, so POSTing values= just no-ops with 204).
	// Reading list_definitions lets us pick the right form per setting key.
	defsByOrg := loadSettingDefinitionsForOrgs(ctx, e, orgs, "setProjectSettings")

	counter := NewTaskCounter("setProjectSettings")
	err := forEachExtractItem(ctx, e, "setProjectSettings", "getProjectSettings",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			// projectSettingsTask enriches each setting with "project": <key>
			// (see internal/extract/tasks_projects.go); legacy fixtures used
			// "projectKey", so accept either to stay robust.
			projectKey := extractField(item.Data, "project")
			if projectKey == "" {
				projectKey = extractField(item.Data, "projectKey")
			}
			settingKey := extractField(item.Data, "key")
			pm, ok := projectKeyMap[item.ServerURL+projectKey]
			if !ok {
				// Most common cause: the source project failed createProjects
				// (or wasn't in this run's scope). Surface it as a Warn so
				// users see *why* their settings aren't landing on SQC instead
				// of silently losing them.
				e.Logger.Warn("setProjectSettings: skipping setting, project not found in migration scope",
					"project", projectKey, "setting", settingKey, "server", item.ServerURL)
				return nil
			}
			if settingKey == "" {
				return nil
			}

			def, hasDef := defsByOrg[pm.OrgKey][settingKey]
			err := applyProjectSetting(ctx, e, pm, item.Data, settingKey, def, hasDef)
			switch {
			case errors.Is(err, errSettingEmpty):
				// Empty payload — skip silently, do not count as success
				// or failure.
				return nil
			case err != nil:
				counter.Fail()
				logAPIWarn(e.Logger, "setProjectSettings failed", err,
					"project", pm.CloudKey, "setting", settingKey)
			default:
				counter.Success()
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

// applyProjectSetting dispatches a single getProjectSettings record via
// the shared definition-aware dispatcher. See applySettingByDef.
func applyProjectSetting(ctx context.Context, e *Executor, pm projectMapping, raw json.RawMessage, settingKey string, def types.SettingDefinition, hasDef bool) error {
	return applySettingByDef(ctx, e, pm.CloudKey, pm.OrgKey, raw, settingKey, def, hasDef)
}

// runSetNewCodePeriods migrates per-branch new-code policy. It iterates the
// extract getNewCodePeriods records (one per source project+branch), maps
// each to its target SonarQube Cloud project, translates the SQS
// type/value to SQC equivalents, and calls
// POST /api/new_code_periods/set on SQC. This handles both newly-created
// projects and pre-existing ones (createProjects only sets the main-branch
// NCD at creation time and skips that work entirely when the project
// already exists).
func runSetNewCodePeriods(ctx context.Context, e *Executor) error {
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping)
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		projectKeyMap[serverURL+key] = projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
			OrgKey:   extractField(p, "sonarcloud_org_key"),
		}
	}

	counter := NewTaskCounter("setNewCodePeriods")
	err := forEachExtractItem(ctx, e, "setNewCodePeriods", "getNewCodePeriods",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			projectKey := extractField(item.Data, "projectKey")
			pm, ok := projectKeyMap[item.ServerURL+projectKey]
			if !ok || pm.CloudKey == "" {
				return nil
			}
			sqsType := extractField(item.Data, "type")
			sqcType, mapped := sqcNewCodeType[sqsType]
			if !mapped {
				e.Logger.Warn("setNewCodePeriods: unmapped source NCD type, skipping",
					"project", pm.CloudKey, "source_type", sqsType)
				return nil
			}
			branch := extractField(item.Data, "branchKey")
			value := extractAnyStr(item.Data, "value")
			// previous_version must travel with an empty value; SQC rejects
			// the call otherwise.
			if sqcType == "previous_version" {
				value = ""
			}

			e.Logger.Debug("project api call: POST /api/new_code_periods/set",
				"project", pm.CloudKey, "branch", branch, "type", sqcType,
				"value", value, "org", pm.OrgKey, "source_type", sqsType)
			err := e.Cloud.NewCodePeriods.Set(ctx, cloud.SetNewCodePeriodParams{
				Project:      pm.CloudKey,
				Branch:       branch,
				Type:         sqcType,
				Value:        value,
				Organization: pm.OrgKey,
			})
			if err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setNewCodePeriods failed", err,
					"project", pm.CloudKey, "branch", branch, "type", sqcType)
			} else {
				counter.Success()
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

// sqcNewCodeType maps the SonarQube Server NCD type enum to the equivalent
// SonarQube Cloud "type" value accepted by /api/new_code_periods/set.
var sqcNewCodeType = map[string]string{
	"NUMBER_OF_DAYS":    "days",
	"PREVIOUS_VERSION":  "previous_version",
	"REFERENCE_BRANCH":  "reference_branch",
	"SPECIFIC_ANALYSIS": "specific_analysis",
}

func runSetProjectTags(ctx context.Context, e *Executor) error {
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping)
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		projectKeyMap[serverURL+key] = projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
		}
	}

	counter := NewTaskCounter("setProjectTags")
	err := forEachExtractItem(ctx, e, "setProjectTags", "getProjectTags",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			projectKey := extractField(item.Data, "projectKey")
			pm, ok := projectKeyMap[item.ServerURL+projectKey]
			if !ok || pm.CloudKey == "" {
				return nil
			}
			tags := extractStringArray(item.Data, "tags")
			if len(tags) == 0 {
				return nil
			}
			tagStr := strings.Join(tags, ",")
			e.Logger.Debug("project api call: POST /api/project_tags/set",
				"project", pm.CloudKey, "tags", tagStr, "tag_count", len(tags))
			if err := e.Cloud.Projects.SetTags(ctx, pm.CloudKey, tagStr); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setProjectTags failed", err, "project", pm.CloudKey)
			} else {
				counter.Success()
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

type projectMapping struct {
	CloudKey string
	OrgKey   string
}

// extractProfilesList extracts the profiles array from a project mapping item.
func extractProfilesList(raw json.RawMessage) []map[string]any {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	profilesRaw, ok := obj["profiles"]
	if !ok {
		return nil
	}
	var profiles []map[string]any
	json.Unmarshal(profilesRaw, &profiles)
	return profiles
}

func getString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func getBool(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// extractPermissions extracts the permissions array as []string.
func extractPermissions(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	permsRaw, ok := obj["permissions"]
	if !ok {
		return nil
	}
	var perms []string
	json.Unmarshal(permsRaw, &perms)
	return perms
}

// extractStringArray extracts a string array from JSON.
func extractStringArray(raw json.RawMessage, key string) []string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	arrRaw, ok := obj[key]
	if !ok {
		return nil
	}
	var arr []string
	json.Unmarshal(arrRaw, &arr)
	return arr
}
