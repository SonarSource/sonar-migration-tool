package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sq-api-go/types"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	"golang.org/x/sync/errgroup"
)

// associateTasks returns tasks that associate projects with profiles, gates, etc.
func associateTasks() []TaskDef {
	return []TaskDef{
		// Every per-project mutation below also depends on
		// grantMigrationUserProjectPermissions so the migration
		// user has Browse/Administer/Administer-Issues/Administer-
		// Hotspots before any settings/profile/gate/tag/NCD/group
		// write reaches a project (issue #190). The dep is in
		// addition to createProjects — the DAG resolver picks the
		// right order regardless.
		{
			Name:         "setProjectProfiles",
			Dependencies: []string{"createProfiles", "createProjects", "grantMigrationUserProjectPermissions"},
			Run:          runSetProjectProfiles,
		},
		{
			Name:         "setProjectGates",
			Dependencies: []string{"createGates", "createProjects", "grantMigrationUserProjectPermissions"},
			Run:          runSetProjectGates,
		},
		{
			Name:         "setProjectGroupPermissions",
			Dependencies: []string{"createGroups", "createProjects", "grantMigrationUserProjectPermissions"},
			Run:          runSetProjectGroupPermissions,
		},
		{
			Name:         "setProjectSettings",
			Dependencies: []string{"createProjects", "grantMigrationUserProjectPermissions"},
			Run:          runSetProjectSettings,
		},
		{
			Name:         "setProjectTags",
			Dependencies: []string{"createProjects", "grantMigrationUserProjectPermissions"},
			Run:          runSetProjectTags,
		},
		{
			Name:         "setNewCodePeriods",
			Dependencies: []string{"createProjects", "grantMigrationUserProjectPermissions"},
			Run:          runSetNewCodePeriods,
		},
		{
			// Propagates SonarQube Server's platform-wide new-code-period
			// default to every SonarQube Cloud org's org-level default
			// (issue #136). Depends on the org mapping for the fan-out
			// target list.
			Name:         "setGlobalNewCodePeriod",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runSetGlobalNewCodePeriod,
		},
		{
			// Migrates non-default SQS global settings to every SQC org
			// in scope. Depends on the org mapping plus both halves of
			// the SQS settings extract (values + definitions, the latter
			// supplies the defaultValue used to detect customization).
			// Issue #186.
			// createProjects supplies a per-org "probe" project key so
			// setGlobalSettings can fetch project-scope list_definitions
			// and distinguish "truly not on SQC" from "exists at
			// project scope only" (issues #189 / #191).
			Name:         "setGlobalSettings",
			Dependencies: []string{"generateOrganizationMappings", "createProjects"},
			Run:          runSetGlobalSettings,
		},
	}
}

// runSetProjectProfiles drives quality-profile assignments on SonarCloud
// from the getProfileProjects extract — the definitive list of
// explicit project↔profile bindings on SonarQube Server.
//
// Earlier versions of this task iterated createProjects' embedded
// profiles array, which was populated from api/navigation/component.
// That endpoint reports the profile used at the LAST ANALYSIS, not
// the current explicit binding, so a project that once used a custom
// profile and was later unassigned still appeared bound to it. The
// migrate task then re-applied the stale binding on SQC and the
// custom profile ended up listed for more projects on SQC than on
// SQS (issue #160).
//
// The new flow drives off getProfileProjects, which queries
// /api/qualityprofiles/projects?selected=selected per non-built-in
// profile — exactly the projects with an active explicit assignment.
// Each record carries the SQS project key + profile name/language;
// we resolve the cloud project key via createProjects output and
// confirm the profile was migrated via createProfiles output before
// dispatching POST /api/qualityprofiles/add_project.
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

	// Build project lookup: serverURL+sqsProjectKey ->
	// (cloudProjectKey, sonarcloudOrgKey). createProjects rows carry
	// both, so a single pass is enough.
	type projTarget struct {
		cloudKey string
		orgKey   string
	}
	projects, _ := e.Store.ReadAll("createProjects")
	projectLookup := make(map[string]projTarget, len(projects))
	for _, p := range projects {
		server := extractField(p, "server_url")
		srcKey := extractField(p, "key")
		cloudKey := extractField(p, "cloud_project_key")
		orgKey := extractField(p, "sonarcloud_org_key")
		if srcKey == "" || cloudKey == "" {
			continue
		}
		projectLookup[server+srcKey] = projTarget{cloudKey: cloudKey, orgKey: orgKey}
	}

	counter := NewTaskCounter("setProjectProfiles")
	err := forEachExtractItem(ctx, e, "setProjectProfiles", "getProfileProjects",
		func(ctx context.Context, ext structure.ExtractItem, w *common.ChunkWriter) error {
			server := extractField(ext.Data, "serverUrl")
			lang := extractField(ext.Data, "language")
			name := extractField(ext.Data, "profileName")
			srcProjectKey := extractField(ext.Data, "key")
			if unsupportedLanguages[lang] || srcProjectKey == "" {
				return nil
			}
			target, ok := projectLookup[server+srcProjectKey]
			if !ok {
				// Project wasn't migrated (e.g., skipped org). Nothing
				// to assign on SQC.
				return nil
			}
			if !profileLookup[target.orgKey+lang+name] {
				// Profile wasn't migrated (built-in, or org skipped).
				// SQC's own default applies; no AddProject needed.
				return nil
			}
			e.Logger.Debug("project api call: POST /api/qualityprofiles/add_project",
				"project", target.cloudKey, "language", lang, "profile", name, "org", target.orgKey)
			if err := e.Cloud.QualityProfiles.AddProject(ctx, lang, name, target.cloudKey, target.orgKey); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setProjectProfiles failed", err,
					"project", target.cloudKey, "language", lang, "profile", name)
			} else {
				counter.Success()
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

	// Project-scope defs are a SUPERSET of org-scope: they include
	// language settings (sonar.<lang>.*) and external-analyzer settings
	// that SQC doesn't expose at org level. The diff is the set of keys
	// where SQS globals need to be propagated to every project — see
	// the post-pass below (issues #189, #191).
	projectDefsByOrg := loadProjectScopedSettingDefinitionsForOrgs(ctx, e, projectKeyMap, "setProjectSettings")

	// Track which (project × setting) pairs were already covered by a
	// per-project SQS extract record so the post-pass doesn't overwrite
	// an explicit project override with the global value.
	var coveredMu sync.Mutex
	covered := make(map[string]map[string]bool, len(projectKeyMap))

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

			// Record the (project, settingKey) pair so the post-pass
			// for global propagation knows to skip it (the per-project
			// SQS override wins). Done BEFORE the API call: even if the
			// SDK call fails we don't want the post-pass to overwrite a
			// value the user explicitly set on SQS.
			coveredMu.Lock()
			cmap := covered[item.ServerURL+projectKey]
			if cmap == nil {
				cmap = make(map[string]bool)
				covered[item.ServerURL+projectKey] = cmap
			}
			cmap[settingKey] = true
			coveredMu.Unlock()

			// Prefer project-scope defs for per-record dispatch:
			// they're a superset that includes language and external-
			// analyzer keys (single STRING with multiValues=false on
			// SQC even though SQS exposes them as values=[...]). Using
			// org-scope only would silently misdispatch those — the
			// same regression issue #120 fixed for the project loop.
			def, hasDef := projectDefsByOrg[pm.OrgKey][settingKey]
			if !hasDef {
				def, hasDef = defsByOrg[pm.OrgKey][settingKey]
			}
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
	if err != nil {
		counter.LogSummary(e.Logger)
		return err
	}

	// Post-pass: propagate customized SQS globals to every SQC project
	// when the key is project-scope-only on SQC. Issues #189 (language
	// settings) and #191 (external analyzer settings) — and any future
	// global setting that has no SQC org-level counterpart.
	if err := propagateGlobalsToProjects(ctx, e, projectKeyMap, defsByOrg, projectDefsByOrg, covered, counter); err != nil {
		counter.LogSummary(e.Logger)
		return err
	}

	counter.LogSummary(e.Logger)
	return nil
}

// propagateGlobalsToProjects applies each customized SQS global setting
// to every target SQC project — but only for keys that exist on SQC at
// project scope and NOT at org scope (the org-scope ones are handled by
// setGlobalSettings), and only when the project doesn't already have a
// per-project override on SQS (the override wins, recorded in
// `covered`). Issues #189 / #191.
func propagateGlobalsToProjects(ctx context.Context, e *Executor,
	projectKeyMap map[string]projectMapping,
	orgDefsByOrg, projectDefsByOrg map[string]map[string]types.SettingDefinition,
	covered map[string]map[string]bool,
	counter *TaskCounter,
) error {
	customizedGlobals, err := readCustomizedSQSGlobals(e)
	if err != nil {
		return fmt.Errorf("setProjectSettings: reading customized SQS globals: %w", err)
	}
	if len(customizedGlobals) == 0 {
		return nil
	}

	// Pre-bucket customized globals by org: a key is propagated to an
	// org's projects only if it's in projectDefsByOrg[org] but NOT in
	// orgDefsByOrg[org]. Computed once per org rather than per project.
	type globalEntry struct {
		raw  json.RawMessage
		def  types.SettingDefinition
		key  string
	}
	bucketByOrg := make(map[string][]globalEntry)
	for org := range projectDefsByOrg {
		projectDefs := projectDefsByOrg[org]
		orgDefs := orgDefsByOrg[org]
		for _, raw := range customizedGlobals {
			key := extractField(raw, "key")
			if key == "" {
				continue
			}
			def, atProject := projectDefs[key]
			if !atProject {
				continue
			}
			if _, atOrg := orgDefs[key]; atOrg {
				continue // setGlobalSettings handles this one
			}
			bucketByOrg[org] = append(bucketByOrg[org], globalEntry{raw: raw, def: def, key: key})
		}
	}
	if len(bucketByOrg) == 0 {
		return nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for projLookupKey, pm := range projectKeyMap {
		bucket := bucketByOrg[pm.OrgKey]
		if len(bucket) == 0 {
			continue
		}
		coverSet := covered[projLookupKey]
		for _, entry := range bucket {
			if coverSet[entry.key] {
				e.Logger.Debug("setProjectSettings: per-project override wins, skipping global propagation",
					"project", pm.CloudKey, "key", entry.key, "org", pm.OrgKey)
				continue
			}
			g.Go(func() error {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				e.Logger.Debug("setProjectSettings: propagating SQS global to project",
					"project", pm.CloudKey, "key", entry.key, "org", pm.OrgKey)
				err := applySettingByDef(gctx, e, pm.CloudKey, pm.OrgKey, entry.raw, entry.key, entry.def, true)
				switch {
				case errors.Is(err, errSettingEmpty):
					return nil
				case err != nil:
					counter.Fail()
					logAPIWarn(e.Logger, "setProjectSettings: global propagation failed", err,
						"project", pm.CloudKey, "setting", entry.key)
				default:
					counter.Success()
				}
				return nil
			})
		}
	}
	return g.Wait()
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
// runSetNewCodePeriods applies project new-code-definition overrides
// extracted from SonarQube Server to SonarQube Cloud.
//
// SonarQube Server's /api/new_code_periods/list returns ONE record
// per (project, branch). The `inherited` flag describes BRANCH-level
// inheritance from the project — NOT project-level inheritance from
// the org or instance. So:
//
//   - branchKey == mainBranch (with or without inherited:true)
//     carries the PROJECT-level NCD — this is the value we must
//     migrate.
//   - branchKey != mainBranch && inherited == false is an explicit
//     per-branch override. SonarQube Cloud has no per-branch NCD
//     concept (issue #134); skipped and surfaced as a limitation.
//   - branchKey != mainBranch && inherited == true means the branch
//     is simply reflecting the project-level NCD (already covered by
//     the main-branch record); silently skipped, no limitation.
//   - Records whose type is not in sqcProjectNewCodeType
//     (REFERENCE_BRANCH, SPECIFIC_ANALYSIS as of May 2026) cannot be
//     applied at project scope on SQC (issue #135); skipped and
//     surfaced as a limitation. The project is left with the org
//     default.
func runSetNewCodePeriods(ctx context.Context, e *Executor) error {
	type projectInfo struct {
		mapping    projectMapping
		mainBranch string
	}
	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectInfo, len(projects))
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		main := extractField(p, "main_branch")
		if main == "" {
			main = "master"
		}
		projectKeyMap[serverURL+key] = projectInfo{
			mapping: projectMapping{
				CloudKey: extractField(p, "cloud_project_key"),
				OrgKey:   extractField(p, "sonarcloud_org_key"),
			},
			mainBranch: main,
		}
	}

	// Org-default NCD that projects with unsupported types fall back
	// to. Derived from the SQS global NCD extract via the project-
	// scope type map so the value is guaranteed to be settable on
	// SQC at project scope. If SQS had no global NCD set, or its
	// global type isn't project-scope-supported on SQC, we use the
	// SonarCloud built-in default (previous_version).
	orgDefaultType, orgDefaultValue := projectScopeOrgDefaultNCD(e)

	counter := NewTaskCounter("setNewCodePeriods")
	err := forEachExtractItem(ctx, e, "setNewCodePeriods", "getNewCodePeriods",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			projectKey := extractField(item.Data, "projectKey")
			info, ok := projectKeyMap[item.ServerURL+projectKey]
			if !ok || info.mapping.CloudKey == "" {
				return nil
			}
			pm := info.mapping
			branch := extractField(item.Data, "branchKey")
			// Skip records that don't describe the project-level NCD.
			// Non-main-branch records are either explicit per-branch
			// overrides (which SQC can't represent — issue #134) or
			// inherited reflections of the project setting (covered
			// by the main-branch record). Either way, nothing to apply.
			if branch != "" && branch != info.mainBranch {
				if !extractBool(item.Data, "inherited") {
					e.Logger.Info("setNewCodePeriods: per-branch NCD override not migratable to SonarQube Cloud, skipping",
						"project", pm.CloudKey, "branch", branch, "type", extractField(item.Data, "type"))
				}
				return nil
			}
			sqsType := extractField(item.Data, "type")
			sqcType, mapped := sqcProjectNewCodeType[sqsType]
			if !mapped {
				// Type not supported at project scope on SQC (issue
				// #135). Explicitly set the project's NCD to the org
				// default (derived from the SQS global NCD) — a
				// settings/reset alone leaves the project unset
				// because SonarCloud's org-level NCD lives in the
				// organization metadata, not in inherited settings.
				e.Logger.Info("setNewCodePeriods: NCD type not supported on SonarQube Cloud, setting project to org default",
					"project", pm.CloudKey, "source_type", sqsType,
					"org_default_type", orgDefaultType, "org_default_value", orgDefaultValue)
				if err := e.Cloud.Settings.Set(ctx, pm.CloudKey, "sonar.leak.period", orgDefaultValue, ""); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "setNewCodePeriods org-default value failed", err,
						"project", pm.CloudKey, "source_type", sqsType)
					return nil
				}
				if err := e.Cloud.Settings.Set(ctx, pm.CloudKey, "sonar.leak.period.type", orgDefaultType, ""); err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "setNewCodePeriods org-default type failed", err,
						"project", pm.CloudKey, "source_type", sqsType)
					return nil
				}
				counter.Success()
				// Sidecar JSONL marker so the PDF report's Projects
				// table can flag this project as having had an
				// unsupported NCD type that fell back to the org
				// default. The cloud_project_key + source_ncd_type +
				// ncd_fallback flag are what collectNCDFallback
				// reads in internal/report/summary.
				_ = w.WriteOne(common.EnrichRaw(item.Data, map[string]any{
					"cloud_project_key": pm.CloudKey,
					"source_ncd_type":   sqsType,
					"ncd_fallback":      true,
				}))
				return nil
			}
			// SonarCloud sets the project-level new code definition via
			// TWO settings on POST /api/settings/set (the legacy
			// /api/new_code_periods/set endpoint that SonarQube Server
			// uses does NOT exist on SonarCloud — calls 404).
			//
			// Order matters — sonar.leak.period must be set BEFORE
			// sonar.leak.period.type, otherwise the type write rejects
			// against a value SonarCloud considers inconsistent:
			//
			//   1. key=sonar.leak.period,
			//      value=<n> for days | "previous_version" for previous_version
			//   2. key=sonar.leak.period.type, value=days | previous_version
			leakValue := extractAnyStr(item.Data, "value")
			if sqcType == "previous_version" {
				leakValue = "previous_version"
			}
			e.Logger.Debug("project api call: POST /api/settings/set (sonar.leak.period)",
				"project", pm.CloudKey, "value", leakValue, "source_type", sqsType)
			err := e.Cloud.Settings.Set(ctx, pm.CloudKey, "sonar.leak.period", leakValue, "")
			if err == nil {
				e.Logger.Debug("project api call: POST /api/settings/set (sonar.leak.period.type)",
					"project", pm.CloudKey, "type", sqcType)
				err = e.Cloud.Settings.Set(ctx, pm.CloudKey, "sonar.leak.period.type", sqcType, "")
			}
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

// sqcNewCodeType maps the SonarQube Server NCD type enum to the
// equivalent SonarQube Cloud "type" value accepted at ORG scope via
// PATCH /organizations/organizations/{key} (issue #136). The
// SonarCloud organization-default NCD does accept reference_branch
// and specific_analysis. The PROJECT-scope endpoint
// /api/new_code_periods/set does NOT — see sqcProjectNewCodeType.
var sqcNewCodeType = map[string]string{
	"NUMBER_OF_DAYS":    "days",
	"PREVIOUS_VERSION":  "previous_version",
	"REFERENCE_BRANCH":  "reference_branch",
	"SPECIFIC_ANALYSIS": "specific_analysis",
}

// projectScopeOrgDefaultNCD computes the (sonar.leak.period.type,
// sonar.leak.period) value pair that runSetNewCodePeriods uses as
// the fallback for projects whose SQS NCD type is not supported at
// SonarCloud project scope (issue #135). The pair is derived from
// the SQS global NCD extract so it matches the org-level default
// that runSetGlobalNewCodePeriod migrates. If SQS had no global NCD,
// or its global type isn't supported at SQC project scope, we fall
// back to SonarCloud's built-in default (previous_version), which
// is what a fresh SQC org uses.
func projectScopeOrgDefaultNCD(e *Executor) (typeValue string, value string) {
	const sqcBuiltinType, sqcBuiltinValue = "previous_version", "previous_version"
	items, err := readExtractItems(e, "getGlobalNewCodePeriod")
	if err != nil || len(items) == 0 {
		return sqcBuiltinType, sqcBuiltinValue
	}
	src := items[0].Data
	sqsType := extractField(src, "type")
	if sqsType == "DAYS" {
		sqsType = "NUMBER_OF_DAYS"
	}
	sqcType, mapped := sqcProjectNewCodeType[sqsType]
	if !mapped {
		return sqcBuiltinType, sqcBuiltinValue
	}
	v := extractAnyStr(src, "value")
	if sqcType == "previous_version" {
		v = "previous_version"
	}
	if v == "" {
		return sqcBuiltinType, sqcBuiltinValue
	}
	return sqcType, v
}

// sqcProjectNewCodeType maps the NCD types accepted by SonarCloud's
// PROJECT-scope /api/new_code_periods/set endpoint. As of May 2026
// only NUMBER_OF_DAYS and PREVIOUS_VERSION are accepted at project
// scope; REFERENCE_BRANCH and SPECIFIC_ANALYSIS are deliberately
// omitted so setNewCodePeriods skips them and the limitation
// surfaces in the PDF report (issue #135). collectNCDLimitations in
// the summary package maintains a mirror of this set.
var sqcProjectNewCodeType = map[string]string{
	"NUMBER_OF_DAYS":   "days",
	"PREVIOUS_VERSION": "previous_version",
}

// runSetGlobalNewCodePeriod propagates SonarQube Server's platform-wide
// new-code-period default to every SonarQube Cloud organization
// (issue #136).
//
// SonarCloud exposes the org-level NCD default through the
// Enterprise API: PATCH /organizations/{organizationId} on
// api.sonarcloud.io, with body { "defaultLeakPeriodType": "days",
// "defaultLeakPeriod": "30" }. The other endpoints we tried —
// /api/settings/set with sonar.leak.period.type and
// /api/new_code_periods/set?organization=<key> — both 400 with
// "can't be set at organization level" on current SonarCloud.
//
// The task therefore:
//
//  1. Looks up the org UUID for each migration-scope SQC org key
//     (regular sonarcloud.io base — /api/organizations/search).
//  2. PATCHes /organizations/{uuid} on the api.sonarcloud.io base
//     with defaultLeakPeriodType + defaultLeakPeriod set.
//
// PREVIOUS_VERSION is SQC's own default, so the task no-ops in that
// case to avoid useless API calls (issue #196 principle).
func runSetGlobalNewCodePeriod(ctx context.Context, e *Executor) error {
	ncdItems, err := readExtractItems(e, "getGlobalNewCodePeriod")
	if err != nil {
		return fmt.Errorf("setGlobalNewCodePeriod: reading getGlobalNewCodePeriod: %w", err)
	}
	if len(ncdItems) == 0 {
		e.Logger.Info("setGlobalNewCodePeriod: no global NCD extracted, nothing to migrate")
		return nil
	}
	src := ncdItems[0].Data
	sqsType := extractField(src, "type")
	if sqsType == "" {
		e.Logger.Info("setGlobalNewCodePeriod: SQS global NCD has no type, skipping")
		return nil
	}
	if sqsType == "DAYS" {
		sqsType = "NUMBER_OF_DAYS"
	}
	sqcType, mapped := sqcNewCodeType[sqsType]
	if !mapped {
		e.Logger.Warn("setGlobalNewCodePeriod: unmapped SQS NCD type, skipping",
			"source_type", sqsType)
		return nil
	}
	// Note: we do NOT short-circuit PREVIOUS_VERSION. The target SQC
	// org might already be at a non-default value (e.g. an operator
	// manually set "32 days" earlier); skipping would leave that
	// stale value in place. We always PATCH.
	value := extractAnyStr(src, "value")
	// SonarCloud's PATCH /organizations/organizations/{key} validates
	// the (defaultLeakPeriodType, defaultLeakPeriod) pair and rejects
	// previous_version with an empty defaultLeakPeriod
	// ("Invalid default leak period for type PREVIOUS_VERSION"). The
	// UI sends "previous_version" as the value too — mirror that.
	if sqcType == "previous_version" {
		value = "previous_version"
	}

	orgItems, _ := e.Store.ReadAll("generateOrganizationMappings")
	seen := make(map[string]struct{})
	counter := NewTaskCounter("setGlobalNewCodePeriod")

	// Build per-org outcomes so the report's Global Settings section
	// can render one row per (NCD, org) just like every other global
	// setting (issue #136 follow-up). The schema matches
	// setGlobalSettings's globalSettingResult; collectGlobalSettings
	// reads both tasks' outputs into the same Section.
	var outcomes []orgOutcome
	ncdSummary := "defaultLeakPeriodType=" + sqcType
	if value != "" {
		ncdSummary += ", defaultLeakPeriod=" + value
	}

	for _, o := range orgItems {
		orgKey := extractField(o, "sonarcloud_org_key")
		if shouldSkipOrg(orgKey) {
			continue
		}
		if _, dup := seen[orgKey]; dup {
			continue
		}
		seen[orgKey] = struct{}{}

		// Resolve the org's current name. SonarCloud's PATCH
		// endpoint requires the name field in the body even when
		// it's not changing — sending only defaultLeakPeriod fields
		// without name has been observed to be rejected. The regular
		// sonarcloud.io /api/organizations/search returns name (it
		// does NOT return the UUID, which is why we don't bother
		// with LookupID).
		orgName := orgKey // fallback if the lookup misses
		if orgs, err := e.Cloud.Organizations.Search(ctx, orgKey); err != nil {
			e.Logger.Warn("setGlobalNewCodePeriod: org name lookup failed, using key as name",
				"org", orgKey, "err", err)
		} else {
			for _, o := range orgs {
				if o.Key == orgKey && o.Name != "" {
					orgName = o.Name
					break
				}
			}
		}

		// PATCH /organizations/organizations/{key} on api.sonarcloud.io.
		// Pointer-based params: only the three fields we set end up
		// in the body, so SonarCloud keeps everything else (url,
		// avatar, description, ...) unchanged.
		e.Logger.Debug("setGlobalNewCodePeriod: PATCH /organizations/organizations/{key}",
			"org", orgKey, "name", orgName,
			"defaultLeakPeriodType", sqcType, "defaultLeakPeriod", value,
			"source_type", sqsType)
		// Always include defaultLeakPeriod (even when empty) so SQC
		// explicitly clears any previous value when the type is
		// previous_version. Pointer-to-empty-string sends
		// `"defaultLeakPeriod":""` in the JSON; nil omits the field
		// entirely. We want the former.
		params := cloud.UpdateOrganizationParams{
			Name:                  &orgName,
			DefaultLeakPeriodType: &sqcType,
			DefaultLeakPeriod:     &value,
		}
		if err := e.CloudAPI.Organizations.UpdateOrganization(ctx, orgKey, params); err != nil {
			counter.Fail()
			logAPIWarn(e.Logger, "setGlobalNewCodePeriod failed", err,
				"org", orgKey, "type", sqcType)
			outcomes = append(outcomes, orgOutcome{
				Org: orgKey, Status: outcomeFailed, Reason: apiErrMessage(err),
				Detail: "Failed: " + apiErrMessage(err),
			})
			continue
		}
		counter.Success()
		outcomes = append(outcomes, orgOutcome{
			Org: orgKey, Status: outcomeApplied,
			Detail: "Applied (" + ncdSummary + ")",
		})
	}
	counter.LogSummary(e.Logger)

	// Write a single record describing the migration so the report
	// section picks it up. Using key="newCodePeriod" — a synthetic
	// pseudo-setting-key — makes the row distinct from real settings
	// like sonar.exclusions in the Section's Name column.
	if len(outcomes) > 0 {
		w, err := e.Store.Writer("setGlobalNewCodePeriod")
		if err != nil {
			return err
		}
		rec := globalSettingResult{
			Key:      "newCodePeriod",
			Value:    value,
			Outcomes: outcomes,
		}
		b, _ := json.Marshal(rec)
		_ = w.WriteOne(b)
	}
	return nil
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
