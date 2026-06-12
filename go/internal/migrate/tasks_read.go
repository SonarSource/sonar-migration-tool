// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// readTasks returns tasks that fetch existing state from SonarQube Cloud.
func readTasks() []TaskDef {
	return []TaskDef{
		{
			Name:         "getProjectIds",
			Dependencies: []string{"createProjects"},
			Run:          runGetProjectIds,
		},
		{
			Name:         "getOrgRepos",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetOrgRepos,
		},
		{
			Name:         "getGateConditions",
			Dependencies: []string{"createGates"},
			Run:          runGetGateConditions,
		},
		{
			Name:         "getProfileBackups",
			Dependencies: []string{"createProfiles"},
			Run:          runGetProfileBackups,
		},
		{
			Name:         "getMigrationUser",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetMigrationUser,
		},
		{
			Name:         "getCreatedProjects",
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetCreatedProjects,
		},
		{
			Name:         "getEnterprises",
			Editions:     []common.Edition{common.EditionEnterprise, common.EditionDatacenter},
			Dependencies: []string{"generateOrganizationMappings"},
			Run:          runGetEnterprises,
		},
	}
}

func runGetProjectIds(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getProjectIds", "createProjects",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			projectKey := extractField(item, "cloud_project_key")
			if shouldSkipOrg(orgKey) || projectKey == "" {
				return nil
			}
			e.Logger.Debug("project api call: GET /api/projects/search (lookup by key)",
				"project", projectKey, "org", orgKey)
			raw, err := e.Raw.GetPaginated(ctx, common.PaginatedOpts{
				Path: "api/projects/search", ResultKey: "components",
				Params: url.Values{
					"organization": {orgKey},
					"projects":     {projectKey},
				},
			})
			if err != nil {
				return err
			}
			enriched := common.EnrichAll(raw, map[string]any{
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteChunk(enriched)
		})
}

func runGetOrgRepos(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getOrgRepos", "generateOrganizationMappings",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				return nil
			}
			raw, err := e.Raw.GetArray(ctx,
				"api/alm_integration/list_repositories", "repositories",
				url.Values{"organization": {orgKey}})
			if err != nil {
				e.Logger.Warn("getOrgRepos skipped", "org", orgKey, "err", err)
				return nil
			}
			enriched := common.EnrichAll(raw, map[string]any{
				"sonarcloud_org_key": orgKey,
			})
			return w.WriteChunk(enriched)
		})
}

func runGetGateConditions(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getGateConditions", "createGates",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			gateName := extractField(item, "source_gate_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			serverURL := extractField(item, "server_url")
			cloudGateID := extractField(item, "cloud_gate_id")
			wasPreexisting := extractBool(item, "was_preexisting")
			if cloudGateID == "" {
				return nil
			}

			// The extract writes one record per source condition, each
			// enriched with the parent gateName and serverUrl. Group every
			// matching condition into a single per-gate record so the
			// downstream addGateConditions task can decide once per gate
			// whether to clear the target's pre-existing conditions first.
			extractItems, _ := readExtractItems(e, "getGateConditions")
			var conditions []map[string]any
			for _, ei := range extractItems {
				if extractField(ei.Data, "gateName") != gateName || ei.ServerURL != serverURL {
					continue
				}
				var cond map[string]any
				if err := json.Unmarshal(ei.Data, &cond); err != nil {
					continue
				}
				// Drop the bookkeeping fields the extract added — only the
				// condition payload itself is useful downstream.
				delete(cond, "gateName")
				delete(cond, "serverUrl")
				conditions = append(conditions, cond)
			}
			if len(conditions) == 0 && !wasPreexisting {
				return nil
			}

			out, err := json.Marshal(map[string]any{
				"gate_name":          gateName,
				"sonarcloud_org_key": orgKey,
				"cloud_gate_id":      cloudGateID,
				"was_preexisting":    wasPreexisting,
				"conditions":         conditions,
			})
			if err != nil {
				return err
			}
			return w.WriteOne(out)
		})
}

func runGetProfileBackups(ctx context.Context, e *Executor) error {
	return forEachMigrateItem(ctx, e, "getProfileBackups", "createProfiles",
		func(ctx context.Context, item json.RawMessage, w *common.ChunkWriter) error {
			profileKey := extractField(item, "source_profile_key")
			orgKey := extractField(item, "sonarcloud_org_key")
			// Read backup from extract data.
			items, _ := readExtractItems(e, "getProfileBackups")
			for _, ei := range items {
				eiKey := extractField(ei.Data, "profileKey")
				if eiKey == profileKey {
					enriched := common.EnrichRaw(ei.Data, map[string]any{
						"sonarcloud_org_key": orgKey,
					})
					if err := w.WriteOne(enriched); err != nil {
						return err
					}
				}
			}
			return nil
		})
}

func runGetMigrationUser(ctx context.Context, e *Executor) error {
	w, err := e.Store.Writer("getMigrationUser")
	if err != nil {
		return err
	}
	raw, err := e.Raw.Get(ctx, "api/users/current", nil)
	if err != nil {
		return err
	}
	return w.WriteOne(raw)
}

// runGetCreatedProjects unions every prior migrate run's createProjects
// JSONL output and writes the deduplicated, org-filtered list to the
// reset run's getCreatedProjects task — the input deleteProjects reads
// to know which SonarCloud project keys to delete.
//
// Previously this task queried /api/projects/search?organization=<org>,
// which lists EVERY project in the SonarCloud org — including projects
// the migrate tool never created (pre-existing, manually provisioned,
// or imported via another tool). Running reset would then wipe those
// too — a serious safety problem (#381 follow-up). Scoping deletion to
// projects the migrate tool actually created closes that gap.
//
// The migrate task createProjects writes one record per provisioned
// SonarCloud project, with `cloud_project_key` + `sonarcloud_org_key`
// populated. We scan every migrate run directory under exportDir
// (identified by run_meta.json — reset runs use clear.json instead),
// union their createProjects records, dedup by cloud_project_key, and
// emit a {key, sonarcloud_org_key} record that matches the shape
// deleteProjects expects (`key` carries the cloud project key).
//
// Reset's interactive confirmation (#381) populates
// Executor.ResetConfirmedOrgs; when set, records whose cloud org isn't
// confirmed are filtered out here so deleteProjects never sees them.
func runGetCreatedProjects(ctx context.Context, e *Executor) error {
	w, err := e.Store.Writer("getCreatedProjects")
	if err != nil {
		return err
	}

	migrateRuns, err := listMigrateRunDirs(e.ExportDir)
	if err != nil {
		return fmt.Errorf("scanning migrate run directories: %w", err)
	}
	if len(migrateRuns) == 0 {
		e.Logger.Warn("getCreatedProjects: no prior migrate runs found under export directory — nothing to delete",
			"export_dir", e.ExportDir)
		return nil
	}

	seen := make(map[string]bool)
	var out []json.RawMessage
	for _, runDir := range migrateRuns {
		items, err := readJSONLDir(filepath.Join(runDir, "createProjects"))
		if err != nil {
			e.Logger.Debug("getCreatedProjects: skipping migrate run with no createProjects output",
				"run_dir", runDir, "err", err)
			continue
		}
		for _, item := range items {
			cloudKey := extractField(item, "cloud_project_key")
			if cloudKey == "" || seen[cloudKey] {
				continue
			}
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				continue
			}
			// Honor the operator's interactive confirmation (#381).
			if e.ResetConfirmedOrgs != nil && !e.ResetConfirmedOrgs[orgKey] {
				continue
			}
			seen[cloudKey] = true

			// Emit a record shaped for deleteProjects: it extracts
			// `key` (cloud project key) and uses sonarcloud_org_key
			// for the org context. The original createProjects record
			// carries `key` as the SOURCE key, so we transform here.
			rec, _ := json.Marshal(map[string]any{
				"key":                cloudKey,
				"sonarcloud_org_key": orgKey,
				"source_key":         extractField(item, "key"),
				"server_url":         extractField(item, "server_url"),
			})
			out = append(out, rec)
		}
	}
	e.Logger.Info("getCreatedProjects: collected migrate-created projects",
		"count", len(out), "migrate_runs_scanned", len(migrateRuns))
	return w.WriteChunk(out)
}

// MigrateCreatedProjectCounts returns the count of distinct
// migrate-created projects per SonarCloud organization key across
// every prior migrate run found under exportDir. It mirrors exactly
// what runGetCreatedProjects feeds deleteProjects, so the reset
// command's interactive confirmation prompt (#381) can display the
// real number of projects each org will lose.
//
// Returns (nil, nil) when exportDir doesn't exist or contains no
// migrate runs — callers render "(0 projects)" for every org in that
// case.
func MigrateCreatedProjectCounts(exportDir string) (map[string]int, error) {
	runDirs, err := listMigrateRunDirs(exportDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	seen := make(map[string]bool)
	counts := make(map[string]int)
	for _, runDir := range runDirs {
		items, err := readJSONLDir(filepath.Join(runDir, "createProjects"))
		if err != nil {
			continue
		}
		for _, item := range items {
			cloudKey := extractField(item, "cloud_project_key")
			if cloudKey == "" || seen[cloudKey] {
				continue
			}
			orgKey := extractField(item, "sonarcloud_org_key")
			if shouldSkipOrg(orgKey) {
				continue
			}
			seen[cloudKey] = true
			counts[orgKey]++
		}
	}
	return counts, nil
}

// listMigrateRunDirs returns the absolute paths of every subdirectory
// under exportDir that holds a migrate run (signalled by a
// run_meta.json file). Reset run directories are skipped — they hold
// clear.json instead.
func listMigrateRunDirs(exportDir string) ([]string, error) {
	entries, err := os.ReadDir(exportDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(exportDir, e.Name())
		if _, err := os.Stat(filepath.Join(runDir, "run_meta.json")); err != nil {
			continue
		}
		out = append(out, runDir)
	}
	return out, nil
}

// readJSONLDir reads every results.*.jsonl file under dir and returns
// the concatenated records. Mirrors common.DataStore.ReadAll without
// requiring an Executor / DataStore (used by listMigrateRunDirs to
// look at directories that aren't the reset's own run dir).
func readJSONLDir(dir string) ([]json.RawMessage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var all []json.RawMessage
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		items, err := common.ReadJSONLFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		all = append(all, items...)
	}
	return all, nil
}

func runGetEnterprises(ctx context.Context, e *Executor) error {
	w, err := e.Store.Writer("getEnterprises")
	if err != nil {
		return err
	}
	raw, err := e.RawAPI.Get(ctx, "enterprises/enterprises", nil)
	if err != nil {
		return err
	}
	return w.WriteOne(raw)
}
