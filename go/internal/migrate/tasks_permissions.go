package migrate

import (
	"context"
	"encoding/json"
	"sync"

	sqapi "github.com/sonar-solutions/sq-api-go"
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
			// Issue #230 O3: migrate every SQS template group permission
			// (not just the migration-tool's own groups). Reads the
			// extracted getTemplateGroups* JSONL and replays the rows
			// against SQC via /api/permissions/add_group_to_template.
			Name:         "setTemplateGroupPermissions",
			Dependencies: []string{"createPermissionTemplates", "createGroups"},
			Run:          runSetTemplateGroupPermissions,
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
					// "already exists" is the steady-state outcome for
					// re-runs (the migration groups are deterministic
					// per org) — surface it at Info so it doesn't
					// pollute the warn channel, and count it as a
					// success since the group is in the desired state.
					if sqapi.IsAlreadyExists(err) {
						e.Logger.Info("createMigrationGroups: already exists", "group", groupName, "org", orgKey)
						counter.Success()
					} else {
						counter.Fail()
						logAPIWarn(e.Logger, "createMigrationGroups failed", err, "group", groupName)
					}
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
	// Issue #269: remap SQS built-in groups to their SQC equivalents
	// (today: sonar-users → Members). Skip the grant if no equivalent
	// exists.
	cloudName, ok := MapGroupNameToCloud(name)
	if !ok {
		return
	}
	perms := extractPermissions(data)
	for _, perm := range perms {
		if !validPermissions[perm] {
			continue
		}
		err := e.Cloud.Permissions.AddGroup(ctx, cloudName, perm, orgKey, "")
		if err != nil {
			counter.Fail()
			logAPIWarn(e.Logger, "setOrgGroupPermissions failed", err, "group", cloudName, "perm", perm)
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
			// Issue #269: remap SQS built-in groups (sonar-users → Members).
			cloudGroup, ok := MapGroupNameToCloud(groupName)
			if !ok {
				return nil
			}
			refs := profileInfo[profileKey]
			for _, ref := range refs {
				err := e.Cloud.QualityProfiles.AddGroup(ctx, ref.Language, ref.Name, cloudGroup, ref.OrgKey)
				if err != nil {
					counter.Fail()
					logAPIWarn(e.Logger, "setProfileGroupPermissions failed", err,
						"profile", ref.Name, "group", cloudGroup)
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

// runSetTemplateGroupPermissions migrates every SQS permission-template
// group permission to its SonarQube Cloud counterpart. Reads the two
// extract feeds — getTemplateGroupsScanners (groups with scan permission)
// and getTemplateGroupsViewers (groups with user/browse permission) —
// and deduplicates by (templateId, group) since each feed returns the
// full permissions[] array for every matching row. Issue #230 O3.
//
// Built-in / migration-tool groups are skipped:
//   - sonar-users / sonar-administrators have no SQC equivalent
//     accessible via API.
//   - migration-scanners / migration-viewers are wired up by
//     addMigrationGroupToTemplates and don't need a second pass.
//
// Groups that didn't make it into createGroups (e.g. failed creation,
// org-skipped) are silently passed over — the missing group is
// already surfaced in the Groups section.
func runSetTemplateGroupPermissions(ctx context.Context, e *Executor) error {
	// SQS templateId → (SQC templateId, sonarcloud_org_key) lookup.
	// createPermissionTemplates writes the SQS-side id under
	// "source_template_key" (Template.SourceTemplateKey).
	templates, _ := e.Store.ReadAll("createPermissionTemplates")
	templateMap := make(map[string]struct{ cloudID, org string }, len(templates))
	for _, t := range templates {
		srvURL := extractField(t, "server_url")
		srcID := extractField(t, "source_template_key")
		if srvURL == "" || srcID == "" {
			continue
		}
		templateMap[srvURL+"\x00"+srcID] = struct{ cloudID, org string }{
			cloudID: extractField(t, "cloud_template_id"),
			org:     extractField(t, "sonarcloud_org_key"),
		}
	}
	if len(templateMap) == 0 {
		e.Logger.Info("setTemplateGroupPermissions: no permission templates in scope, nothing to migrate")
		return nil
	}

	// Set of migrated SQS group names per cloud org so we know which
	// (org, group) pairs are safe to reference.
	createdGroups, _ := e.Store.ReadAll("createGroups")
	groupExists := make(map[string]bool, len(createdGroups))
	for _, g := range createdGroups {
		name := extractField(g, "name")
		org := extractField(g, "sonarcloud_org_key")
		if name != "" && org != "" {
			groupExists[org+"\x00"+name] = true
		}
	}

	// sonar-users is intentionally NOT in skipGroups (issue #269): the
	// apply closure remaps it to SQC's built-in `Members` group via
	// MapGroupNameToCloud and grants the permission there.
	skipGroups := map[string]bool{
		"sonar-administrators": true,
		migrationScanners:      true,
		migrationViewers:       true,
	}

	// Dedup applied (templateId, group, permission) triples — each
	// extract feed surfaces the same row in both "scanners" and
	// "viewers" responses when the group has both permissions.
	type triple struct{ cloudTemplate, group, perm string }
	applied := make(map[triple]bool)
	var appliedMu sync.Mutex

	counter := NewTaskCounter("setTemplateGroupPermissions")

	apply := func(ctx context.Context, srvURL string, data json.RawMessage) {
		srcTemplateID := extractField(data, "templateId")
		groupName := extractField(data, "name")
		if srcTemplateID == "" || groupName == "" || skipGroups[groupName] {
			return
		}
		tmpl, ok := templateMap[srvURL+"\x00"+srcTemplateID]
		if !ok || tmpl.cloudID == "" || tmpl.org == "" {
			return
		}
		// Issue #269: remap SQS built-in groups (sonar-users → Members).
		// Aliased built-ins exist on SQC by default and won't appear in
		// the createGroups output, so the groupExists check is skipped
		// for them.
		cloudGroup, mapOK := MapGroupNameToCloud(groupName)
		if !mapOK {
			return
		}
		aliased := cloudGroup != groupName
		if !aliased && !groupExists[tmpl.org+"\x00"+groupName] {
			return
		}
		perms := extractStringArray(data, "permissions")
		for _, perm := range perms {
			if perm == "" {
				continue
			}
			k := triple{tmpl.cloudID, cloudGroup, perm}
			appliedMu.Lock()
			if applied[k] {
				appliedMu.Unlock()
				continue
			}
			applied[k] = true
			appliedMu.Unlock()
			if err := e.Cloud.Permissions.AddGroupToTemplate(ctx, tmpl.cloudID, cloudGroup, perm, tmpl.org); err != nil {
				counter.Fail()
				logAPIWarn(e.Logger, "setTemplateGroupPermissions failed", err,
					"template", tmpl.cloudID, "group", cloudGroup, "perm", perm)
			} else {
				counter.Success()
			}
		}
	}

	for _, feed := range []string{"getTemplateGroupsScanners", "getTemplateGroupsViewers"} {
		if err := forEachExtractItem(ctx, e, feed+":apply", feed,
			func(ctx context.Context, item structure.ExtractItem, _ *common.ChunkWriter) error {
				apply(ctx, item.ServerURL, item.Data)
				return nil
			}); err != nil {
			counter.LogSummary(e.Logger)
			return err
		}
	}
	counter.LogSummary(e.Logger)
	return nil
}
