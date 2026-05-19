package migrate

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"golang.org/x/sync/errgroup"
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

	return forEachMigrateItem(ctx, e, "setProjectProfiles", "createProjects",
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
				err := e.Cloud.QualityProfiles.AddProject(ctx, lang, name, projectKey, orgKey)
				if err != nil {
					e.Logger.Warn("setProjectProfiles failed", "project", projectKey, "err", err)
				}
			}
			return nil
		})
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

	return forEachMigrateItem(ctx, e, "setProjectGates", "createProjects",
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
			err := e.Cloud.QualityGates.Select(ctx, gateID, projectKey, orgKey)
			if err != nil {
				e.Logger.Warn("setProjectGates failed", "project", projectKey, "err", err)
			}
			return nil
		})
}

func runSetProjectGroupPermissions(ctx context.Context, e *Executor) error {
	// Read project group permissions from extract data.
	items, _ := readExtractItems(e, "getProjectGroupsPermissions")

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

	w, err := e.Store.Writer("setProjectGroupPermissions")
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
			project := extractField(item.Data, "project")
			pm, ok := projectKeyMap[item.ServerURL+project]
			if !ok {
				return nil
			}
			applyGroupPermissions(ctx, e, item.Data, pm, w)
			return nil
		})
	}
	return g.Wait()
}

func applyGroupPermissions(ctx context.Context, e *Executor, data json.RawMessage, pm projectMapping, w *common.ChunkWriter) {
	name := extractField(data, "name")
	permsRaw := extractPermissions(data)
	for _, perm := range permsRaw {
		if !validPermissions[perm] {
			continue
		}
		err := e.Cloud.Permissions.AddGroup(ctx, name, perm, pm.OrgKey, pm.CloudKey)
		if err != nil {
			e.Logger.Warn("setProjectGroupPermissions failed",
				"project", pm.CloudKey, "group", name, "perm", perm, "err", err)
		}
	}
	_ = w.WriteOne(common.EnrichRaw(data, map[string]any{
		"cloud_project_key": pm.CloudKey,
	}))
}

func runSetProjectSettings(ctx context.Context, e *Executor) error {
	// Read project settings from extract data.
	items, _ := readExtractItems(e, "getProjectSettings")

	// Build project lookup.
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

	w, err := e.Store.Writer("setProjectSettings")
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
			projectKey := extractField(item.Data, "projectKey")
			pm, ok := projectKeyMap[item.ServerURL+projectKey]
			if !ok {
				return nil
			}
			settingKey := extractField(item.Data, "key")
			value := extractField(item.Data, "value")
			if settingKey == "" || value == "" {
				return nil
			}
			err := e.Cloud.Settings.Set(ctx, pm.CloudKey, settingKey, value)
			if err != nil {
				e.Logger.Warn("setProjectSettings failed",
					"project", pm.CloudKey, "setting", settingKey, "err", err)
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	}
	return g.Wait()
}

func runSetProjectTags(ctx context.Context, e *Executor) error {
	// Read project tags from extract data.
	items, _ := readExtractItems(e, "getProjectTags")

	projects, _ := e.Store.ReadAll("createProjects")
	projectKeyMap := make(map[string]projectMapping)
	for _, p := range projects {
		serverURL := extractField(p, "server_url")
		key := extractField(p, "key")
		projectKeyMap[serverURL+key] = projectMapping{
			CloudKey: extractField(p, "cloud_project_key"),
		}
	}

	w, err := e.Store.Writer("setProjectTags")
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(cap(e.Sem))
	for _, item := range items {
		g.Go(func() error {
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
			err := e.Cloud.Projects.SetTags(ctx, pm.CloudKey, tagStr)
			if err != nil {
				e.Logger.Warn("setProjectTags failed", "project", pm.CloudKey, "err", err)
			}
			_ = w.WriteOne(item.Data)
			return nil
		})
	}
	return g.Wait()
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
