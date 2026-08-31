// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
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

// sonarUsersGroupName is the SonarQube Server built-in "everyone on this
// server" group. Kept as a named constant so the three call sites that
// guard against the admin-escalation alias (issue #550, see
// skipAdminForBuiltInAlias) compare against the same literal.
const sonarUsersGroupName = "sonar-users"

// skipAdminForBuiltInAlias reports whether granting perm to a group would
// be an accidental privilege escalation introduced by the sonar-users →
// Members built-in alias (MapGroupNameToCloud, issue #269). That alias
// remaps SQS's "everyone on this server" group to SQC's "everyone in
// this org" group with zero awareness of which permission is being
// carried over: a global admin permission granted to sonar-users on the
// source would otherwise be faithfully re-granted to Members at org (or
// resource) scope on the target, making every single org member an
// administrator (issue #550).
//
// originalName MUST be the SOURCE group name, captured before
// MapGroupNameToCloud runs — comparing the mapped name instead would
// also catch a legitimately named custom "Members" group, which is not
// what this guard is for.
func skipAdminForBuiltInAlias(originalName, perm string) bool {
	return originalName == sonarUsersGroupName && perm == "admin"
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

	counter := TaskCounterFromContext(ctx)
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
			var attempted, succeeded int
			for _, perm := range migrationUserProjectPermissions {
				attempted++
				e.Logger.Debug("grantMigrationUserProjectPermissions: POST /api/permissions/add_user",
					"login", login, "perm", perm, "project", cloudKey, "org", orgKey)
				err := e.Cloud.Permissions.AddUser(ctx, login, perm, orgKey, cloudKey)
				if err != nil {
					failAPI(counter, e.Logger, "grantMigrationUserProjectPermissions failed", err,
						"login", login, "project", cloudKey, "perm", perm)
					continue
				}
				succeeded++
				counter.Success()
			}
			// Issue #550: partial failure is expected and swallowed above
			// so the other permissions still get applied — but if EVERY
			// grant on this project failed, the migration user very
			// likely can't administer it at all, and every downstream
			// per-project task will 403. Surface that as a real task
			// error so the DAG halts instead of silently proceeding as
			// if the permissions were granted.
			if attempted > 0 && succeeded == 0 {
				return fmt.Errorf("all %d permission grant(s) failed for project %s (org %s)",
					attempted, cloudKey, orgKey)
			}
			return nil
		})
	return err
}

func runCreateMigrationGroups(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
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
						failAPI(counter, e.Logger, "createMigrationGroups failed", err, "group", groupName)
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

	counter := TaskCounterFromContext(ctx)
	err := forEachMigrateItem(ctx, e, "addMigrationUserToMigrationGroups", "createMigrationGroups",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			for _, groupName := range migrationGroups {
				err := e.Cloud.Groups.AddUser(ctx, groupName, login, orgKey)
				if err != nil {
					failAPI(counter, e.Logger, "addMigrationUser failed", err, "group", groupName)
				} else {
					counter.Success()
				}
			}
			return nil
		})
	return err
}

func runAddMigrationGroupToTemplates(ctx context.Context, e *Executor) error {
	counter := TaskCounterFromContext(ctx)
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
					failAPI(counter, e.Logger, "addMigrationGroupToTemplates failed", err, "template", templateID, "perm", perm)
				} else {
					counter.Success()
				}
			}
			for _, perm := range []string{"user", "codeviewer"} {
				err := e.Cloud.Permissions.AddGroupToTemplate(ctx, templateID, migrationViewers, perm, orgKey)
				if err != nil {
					failAPI(counter, e.Logger, "addMigrationGroupToTemplates failed", err, "template", templateID, "perm", perm)
				} else {
					counter.Success()
				}
			}
			return nil
		})
	return err
}

func runSetOrgGroupPermissions(ctx context.Context, e *Executor) error {
	// Build org lookup.
	orgKeys := buildServerOrgLookup(e)

	counter := TaskCounterFromContext(ctx)
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
			err := applyOrgPermissions(ctx, e, item.Data, name, orgKey, counter)
			_ = w.WriteOne(item.Data)
			return err
		})
	return err
}

func applyOrgPermissions(ctx context.Context, e *Executor, data json.RawMessage, name, orgKey string, counter *TaskCounter) error {
	// Issue #269: remap SQS built-in groups to their SQC equivalents
	// (today: sonar-users → Members). Skip the grant if no equivalent
	// exists.
	cloudName, ok := MapGroupNameToCloud(name)
	if !ok {
		return nil
	}
	perms := extractPermissions(data)
	var attempted, succeeded int
	for _, perm := range perms {
		if !validPermissions[perm] {
			continue
		}
		// Issue #550: never auto-escalate the sonar-users → Members alias
		// into an org-wide admin grant. The other permissions in this
		// same group still get applied normally.
		if skipAdminForBuiltInAlias(name, perm) {
			e.Logger.Warn("setOrgGroupPermissions: admin NOT auto-granted to Members — source aliased it from sonar-users; grant manually if genuinely intended",
				"group", cloudName, "org", orgKey)
			counter.Fail()
			continue
		}
		attempted++
		err := e.Cloud.Permissions.AddGroup(ctx, cloudName, perm, orgKey, "")
		if err != nil {
			failAPI(counter, e.Logger, "setOrgGroupPermissions failed", err, "group", cloudName, "perm", perm)
			continue
		}
		succeeded++
		counter.Success()
	}
	if attempted > 0 && succeeded == 0 {
		return fmt.Errorf("all %d permission grant(s) failed for group %s in org %s", attempted, cloudName, orgKey)
	}
	return nil
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

	counter := TaskCounterFromContext(ctx)
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
			// api/qualityprofiles/add_group has no separate permission
			// argument — the grant itself is "this group may edit the
			// profile", i.e. it IS the admin-equivalent action for this
			// resource. Issue #550: don't let the sonar-users → Members
			// alias silently hand every org member edit rights on the
			// profile; skip the grant entirely and require manual review.
			if skipAdminForBuiltInAlias(groupName, "admin") {
				for _, ref := range refs {
					e.Logger.Warn("setProfileGroupPermissions: edit rights NOT auto-granted to Members — source aliased it from sonar-users; grant manually if genuinely intended",
						"profile", ref.Name, "group", cloudGroup)
					counter.Fail()
				}
				_ = w.WriteOne(item.Data)
				return nil
			}
			var attempted, succeeded int
			for _, ref := range refs {
				attempted++
				err := e.Cloud.QualityProfiles.AddGroup(ctx, ref.Language, ref.Name, cloudGroup, ref.OrgKey)
				if err != nil {
					failAPI(counter, e.Logger, "setProfileGroupPermissions failed", err,
						"profile", ref.Name, "group", cloudGroup)
					continue
				}
				succeeded++
				counter.Success()
			}
			_ = w.WriteOne(item.Data)
			if attempted > 0 && succeeded == 0 {
				return fmt.Errorf("all %d permission grant(s) failed for group %s on profile group %s",
					attempted, cloudGroup, groupName)
			}
			return nil
		})
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

	counter := TaskCounterFromContext(ctx)

	apply := func(ctx context.Context, srvURL string, data json.RawMessage) error {
		srcTemplateID := extractField(data, "templateId")
		groupName := extractField(data, "name")
		if srcTemplateID == "" || groupName == "" || skipGroups[groupName] {
			return nil
		}
		tmpl, ok := templateMap[srvURL+"\x00"+srcTemplateID]
		if !ok || tmpl.cloudID == "" || tmpl.org == "" {
			return nil
		}
		// Issue #269: remap SQS built-in groups (sonar-users → Members).
		// Aliased built-ins exist on SQC by default and won't appear in
		// the createGroups output, so the groupExists check is skipped
		// for them.
		cloudGroup, mapOK := MapGroupNameToCloud(groupName)
		if !mapOK {
			return nil
		}
		aliased := cloudGroup != groupName
		if !aliased && !groupExists[tmpl.org+"\x00"+groupName] {
			return nil
		}
		perms := extractStringArray(data, "permissions")
		var attempted, succeeded int
		for _, perm := range perms {
			if perm == "" {
				continue
			}
			// Issue #550: never auto-escalate the sonar-users → Members
			// alias into an admin grant on the permission template. The
			// other permissions for this group/template still apply.
			if skipAdminForBuiltInAlias(groupName, perm) {
				e.Logger.Warn("setTemplateGroupPermissions: admin NOT auto-granted to Members — source aliased it from sonar-users; grant manually if genuinely intended",
					"template", tmpl.cloudID, "group", cloudGroup)
				counter.Fail()
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
			attempted++
			if err := e.Cloud.Permissions.AddGroupToTemplate(ctx, tmpl.cloudID, cloudGroup, perm, tmpl.org); err != nil {
				failAPI(counter, e.Logger, "setTemplateGroupPermissions failed", err,
					"template", tmpl.cloudID, "group", cloudGroup, "perm", perm)
				continue
			}
			succeeded++
			counter.Success()
		}
		if attempted > 0 && succeeded == 0 {
			return fmt.Errorf("all %d permission grant(s) failed for group %s on template %s",
				attempted, cloudGroup, tmpl.cloudID)
		}
		return nil
	}

	for _, feed := range []string{"getTemplateGroupsScanners", "getTemplateGroupsViewers"} {
		if err := forEachExtractItem(ctx, e, feed+":apply", feed,
			func(ctx context.Context, item structure.ExtractItem, _ *common.ChunkWriter) error {
				return apply(ctx, item.ServerURL, item.Data)
			}); err != nil {
			return err
		}
	}
	return nil
}
