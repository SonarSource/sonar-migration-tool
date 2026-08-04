// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
)

// SyncIssuesConfig holds the parameters for a standalone sync-issues run
// (issue #412). Unlike MigrateConfig, there is no project-creation or
// configuration-provisioning here — every target project is assumed to
// already exist in SonarQube Cloud (created by a prior migrate/transfer, or
// scanned directly).
type SyncIssuesConfig struct {
	URL           string // SonarQube Cloud URL (default: https://sonarcloud.io/)
	Token         string
	EnterpriseKey string

	ExportDirectory string
	Concurrency     int
	Timeout         int

	// ProjectKeyPattern must match the pattern used when the target
	// projects were created, so the rendered keys resolve to the same
	// SonarQube Cloud projects. Defaults to DefaultProjectKeyPattern.
	ProjectKeyPattern string

	// DefaultOrganization is used as the SonarCloud org for every row in
	// organizations.csv if none have a sonarcloud_org_key. See
	// applyOrgMapping.
	DefaultOrganization string

	// ProjectKeys, when non-empty, restricts the sync to these source
	// project keys. Empty means every project found in projects.csv.
	ProjectKeys []string

	Debug bool
}

func (cfg *SyncIssuesConfig) applyDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 25
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
	}
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = "./migration-files/"
	}
	if cfg.URL == "" {
		cfg.URL = "https://sonarcloud.io/"
	}
	if strings.TrimSpace(cfg.ProjectKeyPattern) == "" {
		cfg.ProjectKeyPattern = DefaultProjectKeyPattern
	}
	if cfg.URL != "" && cfg.URL[len(cfg.URL)-1] != '/' {
		cfg.URL += "/"
	}
}

// SyncIssuesSummary aggregates the per-project stats from every synced
// project so the caller can print a run-wide total.
type SyncIssuesSummary struct {
	ProjectsSynced int

	IssuesActionable     int64
	IssuesSynced         int64
	IssuesLineMismatch   int64
	IssuesNotFound       int64
	HotspotsActionable   int64
	HotspotsSynced       int64
	HotspotsLineMismatch int64
	HotspotsNotFound     int64
	HotspotsAckDemoted   int64
}

// syncTarget is the {source project, resolved Cloud project} tuple driving
// one project's sync — the same shape the createProjects task output
// carries (cloud_project_key, sonarcloud_org_key, server_url, key), built
// here directly from projects.csv + organizations.csv instead of from a
// migrate run's task store.
type syncTarget struct {
	Key             string // source (SonarQube Server) project key
	ServerURL       string
	CloudProjectKey string
	OrgKey          string
}

// RunSyncIssues is the entry point for the standalone sync-issues command
// (issue #412). It resolves every source project to its already-migrated
// SonarQube Cloud counterpart via the same project_key_pattern +
// organizations.csv convention `createProjects` uses, then runs the
// existing issue/hotspot metadata sync (transitions, comments, tags,
// back-links — unchanged) against each one directly, without going through
// the migrate task planner/DAG.
func RunSyncIssues(ctx context.Context, cfg SyncIssuesConfig) (SyncIssuesSummary, error) {
	cfg.applyDefaults()

	var summary SyncIssuesSummary

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := ValidateProjectKeyPattern(cfg.ProjectKeyPattern); err != nil {
		return summary, common.NewExitError(2, fmt.Errorf("invalid project_key_pattern: %w", err))
	}

	appliedDefault, err := applyOrgMapping(cfg.ExportDirectory, cfg.DefaultOrganization, logger)
	if err != nil {
		return summary, err
	}

	cloudURL := cfg.URL
	clientOpts := []sqapi.Option{sqapi.WithTimeout(cfg.Timeout)}
	if cfg.Debug {
		clientOpts = append(clientOpts, sqapi.WithDebugLogger(common.NewHTTPDebugLogger(logger)))
	}
	cloudClient := sqapi.NewCloudClient(cloudURL, cfg.Token, clientOpts...)
	cc := cloud.New(cloudClient)
	raw := common.NewRawClient(cloudClient.HTTPClient(), cloudClient.BaseURL())

	if err := validateOrgsExist(ctx, cc.Organizations, cfg.ExportDirectory, cfg.EnterpriseKey, cfg.DefaultOrganization, appliedDefault); err != nil {
		return summary, err
	}
	if err := validatePatternOrgCollision(ctx, cc.Organizations, cfg.ProjectKeyPattern); err != nil {
		return summary, err
	}

	mapping, err := structure.GetUniqueExtracts(cfg.ExportDirectory)
	if err != nil {
		return summary, fmt.Errorf("scanning extracts: %w", err)
	}

	targets, err := resolveSyncTargets(cfg.ExportDirectory, cfg.ProjectKeyPattern, cfg.ProjectKeys)
	if err != nil {
		return summary, err
	}
	if len(targets) == 0 {
		logger.Warn("sync-issues: no matching projects found in projects.csv")
		return summary, nil
	}

	e := &Executor{
		Cloud:             cc,
		Raw:               raw,
		ExportDir:         cfg.ExportDirectory,
		Mapping:           mapping,
		Sem:               make(chan struct{}, cfg.Concurrency),
		ProjectKeyPattern: cfg.ProjectKeyPattern,
		Logger:            logger,
	}

	ruleDefaults := loadRuleTagDefaults(e)
	counter := NewTaskCounter("syncIssues")

	var issuesActionable, issuesSynced, issuesMismatch, issuesNotFound atomic.Int64
	var hotspotsActionable, hotspotsSynced, hotspotsMismatch, hotspotsNotFound, hotspotsAck atomic.Int64

	runProjectSyncLoop(ctx, e, targets, "sync-issues: projects", 5,
		func(gctx context.Context, t syncTarget) {
			logger.Info("sync-issues: syncing project", "source_key", t.Key, "cloud_project_key", t.CloudProjectKey, "org", t.OrgKey)

			iStats := syncProjectIssues(gctx, e, t.CloudProjectKey, t.OrgKey, t.ServerURL, t.Key, counter, ruleDefaults)
			issuesActionable.Add(iStats.Actionable)
			issuesSynced.Add(iStats.A)
			issuesMismatch.Add(iStats.B)
			issuesNotFound.Add(iStats.C)

			hResult := syncProjectHotspots(gctx, e, syncHotspotInput{
				CloudKey:  t.CloudProjectKey,
				OrgKey:    t.OrgKey,
				ServerURL: t.ServerURL,
				ServerKey: t.Key,
			})
			hotspotsActionable.Add(hResult.Stats.Actionable)
			hotspotsSynced.Add(hResult.Stats.A)
			hotspotsMismatch.Add(hResult.Stats.B)
			hotspotsNotFound.Add(hResult.Stats.C)
			hotspotsAck.Add(hResult.Stats.AckDemoted)
		})

	summary = SyncIssuesSummary{
		ProjectsSynced:       len(targets),
		IssuesActionable:     issuesActionable.Load(),
		IssuesSynced:         issuesSynced.Load(),
		IssuesLineMismatch:   issuesMismatch.Load(),
		IssuesNotFound:       issuesNotFound.Load(),
		HotspotsActionable:   hotspotsActionable.Load(),
		HotspotsSynced:       hotspotsSynced.Load(),
		HotspotsLineMismatch: hotspotsMismatch.Load(),
		HotspotsNotFound:     hotspotsNotFound.Load(),
		HotspotsAckDemoted:   hotspotsAck.Load(),
	}
	logger.Info("sync-issues: run complete",
		"projects", summary.ProjectsSynced,
		"issues_synced", summary.IssuesSynced, "issues_skipped", summary.IssuesLineMismatch+summary.IssuesNotFound,
		"hotspots_synced", summary.HotspotsSynced, "hotspots_skipped", summary.HotspotsLineMismatch+summary.HotspotsNotFound,
	)
	return summary, nil
}

// resolveSyncTargets reads projects.csv + organizations.csv from exportDir
// and computes each project's target SonarQube Cloud project key, exactly
// mirroring what the createProjects task does (tasks_create.go:68) so
// sync-issues resolves to the SAME already-migrated Cloud project.
// Projects whose org is unmapped (shouldSkipOrg) or that aren't in
// projectKeys (when non-empty) are excluded.
func resolveSyncTargets(exportDir, pattern string, projectKeys []string) ([]syncTarget, error) {
	rows, err := structure.LoadCSV(exportDir, "projects.csv")
	if err != nil {
		return nil, fmt.Errorf("loading projects.csv: %w", err)
	}
	orgLookup, err := buildOrgKeyLookup(exportDir)
	if err != nil {
		return nil, fmt.Errorf("loading organizations.csv: %w", err)
	}

	var wanted map[string]bool
	if len(projectKeys) > 0 {
		wanted = make(map[string]bool, len(projectKeys))
		for _, k := range projectKeys {
			wanted[k] = true
		}
	}

	var targets []syncTarget
	for _, row := range rows {
		key, _ := row["key"].(string)
		if key == "" {
			continue
		}
		if wanted != nil && !wanted[key] {
			continue
		}
		serverURL, _ := row["server_url"].(string)
		sourceOrgKey, _ := row["sonarqube_org_key"].(string)
		orgKey := orgLookup[sourceOrgKey]
		if shouldSkipOrg(orgKey) {
			continue
		}
		targets = append(targets, syncTarget{
			Key:             key,
			ServerURL:       serverURL,
			CloudProjectKey: RenderProjectKey(pattern, key, orgKey),
			OrgKey:          orgKey,
		})
	}
	return targets, nil
}
