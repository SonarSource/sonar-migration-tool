package migrate

import (
	"context"
	"encoding/json"

	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	migrationScanners = "migration-scanners"
	migrationViewers  = "migration-viewers"
)

var migrationGroups = []string{migrationScanners, migrationViewers}

// permissionTasks returns tasks for setting up migration groups and permissions.
func permissionTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "createMigrationGroups",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runCreateMigrationGroups,
		},
		{
			Name:         "addMigrationUserToMigrationGroups",
			Dependencies: []string{"createMigrationGroups", "getMigrationUser"},
			Run:          runAddMigrationUserToMigrationGroups,
		},
		{
			Name:         "addMigrationGroupToTemplates",
			Dependencies: []string{"createPermissionTemplates", "createMigrationGroups"},
			Run:          runAddMigrationGroupToTemplates,
		},
		{
			Name:         "setOrgGroupPermissions",
			Dependencies: []string{"createGroups", "generateOrganizationMappings"},
			Run:          runSetOrgGroupPermissions,
		},
		{
			Name:         "setProfileGroupPermissions",
			Dependencies: []string{"createProfiles", "createGroups"},
			Run:          runSetProfileGroupPermissions,
		},
		{
			// Issue #190: depending on the SQS permission template
			// the user provisioned the SQC org with, the migration
			// user can end up without Browse/Administer on the
			// projects it just created — which then makes every
			// downstream per-project mutation (settings, profiles,
			// gates, tags, NCD, group perms, binding) 403. Grant the
			// migration user user/admin/issueadmin/securityhotspotadmin
			// on every newly-created project as the FIRST step after
			// createProjects; the other per-project tasks list this
			// task as an additional dependency so the DAG enforces
			// the order.
			Name:         "grantMigrationUserProjectPermissions",
			Dependencies: []string{"createProjects", "getMigrationUser"},
			Run:          runGrantMigrationUserProjectPermissions,
		},
	}
}

// migrationUserProjectPermissions are the four permissions the
// migration user grants itself on every project it just created.
// user=Browse, admin=Administer, issueadmin=Administer Issues,
// securityhotspotadmin=Administer Security Hotspots. The latter two
// anticipate the project-data migration feature (issues + hotspots
// status changes).
var migrationUserProjectPermissions = []string{
	"user",
	"admin",
	"issueadmin",
	"securityhotspotadmin",
}

func runGrantMigrationUserProjectPermissions(ctx context.Context, e *Executor) error {
	userItems, _ := e.Store.ReadAll("getMigrationUser")
	if len(userItems) == 0 {
		e.Logger.Info("grantMigrationUserProjectPermissions: no migration user record, nothing to grant")
		return nil
	}
	login := extractField(userItems[0], "login")
	if login == "" {
		e.Logger.Info("grantMigrationUserProjectPermissions: migration user has no login, nothing to grant")
		return nil
	}

	counter := NewTaskCounter("grantMigrationUserProjectPermissions")
	err := forEachMigrateItem(ctx, e, "grantMigrationUserProjectPermissions", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			cloudKey := extractField(item, "cloud_project_key")
			if cloudKey == "" {
				return nil
			}
			for _, perm := range migrationUserProjectPermissions {
				e.Logger.Debug("grantMigrationUserProjectPermissions: POST /api/permissions/add_user",
					"login", login, "perm", perm, "project", cloudKey, "org", orgKey)
				err := e.Cloud.Permissions.AddUser(ctx, login, perm, orgKey, cloudKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "grantMigrationUserProjectPermissions failed", err,
						"login", login, "project", cloudKey, "perm", perm)
					continue
				}
				counter.Success()
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runCreateMigrationGroups(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("createMigrationGroups")
	err := forEachMigrateItem(ctx, e, "createMigrationGroups", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			for _, groupName := range migrationGroups {
				_, err := e.Cloud.Groups.Create(ctx, cloud.CreateGroupParams{
					Name:         groupName,
					Description:  "Migration group for " + groupName,
					Organization: orgKey,
				})
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "createMigrationGroups failed", err, "group", groupName)
				} else {
					counter.Success()
				}
			}
			result, _ := json.Marshal(map[string]any{
				"sonarcloud_org_key": orgKey,
				"groups":             migrationGroups,
			})
			return w.WriteOne(result)
		})
	counter.LogSummary(e.Logger)
	return err
}

func runAddMigrationUserToMigrationGroups(ctx context.Context, e *Executor) error {
	// Get migration user login.
	userItems, _ := e.Store.ReadAll("getMigrationUser")
	if len(userItems) == 0 {
		return nil
	}
	login := extractField(userItems[0], "login")
	if login == "" {
		return nil
	}

	counter := NewTaskCounter("addMigrationUserToMigrationGroups")
	err := forEachMigrateItem(ctx, e, "addMigrationUserToMigrationGroups", "createMigrationGroups",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			for _, groupName := range migrationGroups {
				err := e.Cloud.Groups.AddUser(ctx, groupName, login, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "addMigrationUser failed", err, "group", groupName)
				} else {
					counter.Success()
				}
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runAddMigrationGroupToTemplates(ctx context.Context, e *Executor) error {
	counter := NewTaskCounter("addMigrationGroupToTemplates")
	err := forEachMigrateItem(ctx, e, "addMigrationGroupToTemplates", "createPermissionTemplates",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			templateID := extractField(item, "cloud_template_id")
			if templateID == "" {
				return nil
			}
			orgKey := extractField(item, "sonarcloud_org_key")
			for _, perm := range []string{"scan", "user"} {
				err := e.Cloud.Permissions.AddGroupToTemplate(ctx, templateID, migrationScanners, perm, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "addMigrationGroupToTemplates failed", err, "template", templateID, "perm", perm)
				} else {
					counter.Success()
				}
			}
			for _, perm := range []string{"user", "codeviewer"} {
				err := e.Cloud.Permissions.AddGroupToTemplate(ctx, templateID, migrationViewers, perm, orgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "addMigrationGroupToTemplates failed", err, "template", templateID, "perm", perm)
				} else {
					counter.Success()
				}
			}
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func runSetOrgGroupPermissions(ctx context.Context, e *Executor) error {
	// Build org lookup.
	orgKeys := buildServerOrgLookup(e)

	counter := NewTaskCounter("setOrgGroupPermissions")
	err := forEachExtractItem(ctx, e, "setOrgGroupPermissions", "getGroups",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			name := extractField(item.Data, "name")
			if name == "Anyone" {
				return nil
			}
			orgKey := orgKeys[item.ServerURL]
			if shouldSkipOrg(orgKey) {
				return nil
			}
			applyOrgPermissions(ctx, e, item.Data, name, orgKey, counter)
			_ = w.WriteOne(item.Data)
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

func applyOrgPermissions(ctx context.Context, e *Executor, data json.RawMessage, name, orgKey string, counter *TaskCounter) {
	perms := extractPermissions(data)
	for _, perm := range perms {
		if !validPermissions[perm] {
			continue
		}
		err := e.Cloud.Permissions.AddGroup(ctx, name, perm, orgKey, "")
		if err != nil {
			counter.Fail()
			logAPIWarn(e.Logger, "setOrgGroupPermissions failed", err, "group", name, "perm", perm)
		} else {
			counter.Success()
		}
	}
}

func runSetProfileGroupPermissions(ctx context.Context, e *Executor) error {
	// Build profile lookup: sourceKey -> []orgKey+profileName+language.
	profiles, _ := e.Store.ReadAll("createProfiles")
	profileInfo := make(map[string][]profileRef) // source_profile_key -> refs
	for _, p := range profiles {
		srcKey := extractField(p, "source_profile_key")
		profileInfo[srcKey] = append(profileInfo[srcKey], profileRef{
			OrgKey:   extractField(p, "sonarcloud_org_key"),
			Name:     extractField(p, "name"),
			Language: extractField(p, "language"),
		})
	}

	counter := NewTaskCounter("setProfileGroupPermissions")
	err := forEachExtractItem(ctx, e, "setProfileGroupPermissions", "getProfileGroups",
		func(ctx context.Context, item structure.ExtractItem, w *common.ChunkWriter) error {
			profileKey := extractField(item.Data, "profileKey")
			groupName := extractField(item.Data, "name")
			refs := profileInfo[profileKey]
			for _, ref := range refs {
				err := e.Cloud.QualityProfiles.AddGroup(ctx, ref.Language, ref.Name, groupName, ref.OrgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "setProfileGroupPermissions failed", err,
						"profile", ref.Name, "group", groupName)
				} else {
					counter.Success()
				}
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	counter.LogSummary(e.Logger)
	return err
}

type profileRef struct {
	OrgKey   string
	Name     string
	Language string
}
