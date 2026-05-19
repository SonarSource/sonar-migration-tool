package migrate

import (
	"context"
	"encoding/json"

	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"golang.org/x/sync/errgroup"
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
	}
}

func runCreateMigrationGroups(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "createMigrationGroups", "generateOrganizationMappings",
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
					e.Logger.Warn("createMigrationGroups failed", "group", groupName, "err", err)
				}
			}
			result, _ := json.Marshal(map[string]any{
				"sonarcloud_org_key": orgKey,
				"groups":             migrationGroups,
			})
			return w.WriteOne(result)
		})
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

	return forEachMigrateItem(ctx, e, "addMigrationUserToMigrationGroups", "createMigrationGroups",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			for _, groupName := range migrationGroups {
				err := e.Cloud.Groups.AddUser(ctx, groupName, login, orgKey)
				if err != nil {
					e.Logger.Warn("addMigrationUser failed", "group", groupName, "err", err)
				}
			}
			return nil
		})
}

func runAddMigrationGroupToTemplates(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "addMigrationGroupToTemplates", "createPermissionTemplates",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			templateID := extractField(item, "cloud_template_id")
			if templateID == "" {
				return nil
			}
			for _, perm := range []string{"scan", "user"} {
				_ = e.Cloud.Permissions.AddGroupToTemplate(ctx, templateID, migrationScanners, perm)
			}
			for _, perm := range []string{"user", "codeviewer"} {
				_ = e.Cloud.Permissions.AddGroupToTemplate(ctx, templateID, migrationViewers, perm)
			}
			return nil
		})
}

func runSetOrgGroupPermissions(ctx context.Context, e *Executor) error {
	// Read groups from extract data that have org-level permissions.
	items, _ := readExtractItems(e, "getGroups")

	// Build org lookup.
	orgItems, _ := e.Store.ReadAll("generateOrganizationMappings")
	orgKeys := make(map[string]string) // serverURL -> cloudOrgKey
	for _, o := range orgItems {
		serverURL := extractField(o, "server_url")
		cloudKey := extractField(o, "sonarcloud_org_key")
		orgKeys[serverURL] = cloudKey
	}

	w, err := e.Store.Writer("setOrgGroupPermissions")
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
			name := extractField(item.Data, "name")
			if name == "Anyone" {
				return nil
			}
			orgKey := orgKeys[item.ServerURL]
			if shouldSkipOrg(orgKey) {
				return nil
			}
			applyOrgPermissions(ctx, e, item.Data, name, orgKey)
			_ = w.WriteOne(item.Data)
			return nil
		})
	}
	return g.Wait()
}

func applyOrgPermissions(ctx context.Context, e *Executor, data json.RawMessage, name, orgKey string) {
	perms := extractPermissions(data)
	for _, perm := range perms {
		if !validPermissions[perm] {
			continue
		}
		err := e.Cloud.Permissions.AddGroup(ctx, name, perm, orgKey, "")
		if err != nil {
			e.Logger.Warn("setOrgGroupPermissions failed",
				"group", name, "perm", perm, "err", err)
		}
	}
}

func runSetProfileGroupPermissions(ctx context.Context, e *Executor) error {
	// Read profile groups from extract data.
	items, _ := readExtractItems(e, "getProfileGroups")

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

	w, err := e.Store.Writer("setProfileGroupPermissions")
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
			profileKey := extractField(item.Data, "profileKey")
			groupName := extractField(item.Data, "name")
			refs := profileInfo[profileKey]
			for _, ref := range refs {
				err := e.Cloud.QualityProfiles.AddGroup(ctx, ref.Language, ref.Name, groupName, ref.OrgKey)
				if err != nil {
					e.Logger.Warn("setProfileGroupPermissions failed",
						"profile", ref.Name, "group", groupName, "err", err)
				}
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	}
	return g.Wait()
}

type profileRef struct {
	OrgKey   string
	Name     string
	Language string
}
