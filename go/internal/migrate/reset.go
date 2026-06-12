// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqapi "github.com/sonar-solutions/sq-api-go"
	"github.com/sonar-solutions/sq-api-go/cloud"
	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
	"github.com/sonar-solutions/sonar-migration-tool/internal/version"
	"golang.org/x/sync/errgroup"
)

// ResetConfig holds parameters for a reset run.
type ResetConfig struct {
	Token           string
	EnterpriseKey   string
	Edition         string
	URL             string
	Concurrency     int
	ExportDirectory string
	Debug           bool

	// ConfirmedOrgs is the operator-confirmed subset of mapped
	// SonarCloud organizations to actually wipe (#381). Populated by
	// the reset command's interactive confirmation prompt or by the
	// --yes flag for non-interactive runs. When empty / nil, no
	// filter is applied (all mapped orgs get reset — the legacy
	// behaviour for callers that don't go through cmd/reset.go).
	ConfirmedOrgs []string
}

// RunReset deletes all migrated entities from SonarQube Cloud.
func RunReset(ctx context.Context, cfg ResetConfig) error {
	cfg.applyDefaults()

	cmdStart := time.Now()

	cloudURL := cfg.URL
	apiURL := strings.Replace(cloudURL, "https://", "https://api.", 1)

	// Eager-construct the logger so we can install an HTTP debug logger when
	// --debug is set.
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	// End-of-command timing line (#311). Defer so it fires on every
	// exit path — success or any of the validate/plan/execute errors.
	defer common.LogCommandDuration(logger, "reset", cmdStart)

	var clientOpts []sqapi.Option
	if cfg.Debug {
		clientOpts = append(clientOpts, sqapi.WithDebugLogger(common.NewHTTPDebugLogger(logger)))
	}
	cloudClient := sqapi.NewCloudClient(cloudURL, cfg.Token, clientOpts...)
	apiClient := sqapi.NewCloudClient(apiURL, cfg.Token, clientOpts...)
	cc := cloud.New(cloudClient)
	apiCC := cloud.New(apiClient)
	raw := common.NewRawClient(cloudClient.HTTPClient(), cloudClient.BaseURL())
	rawAPI := common.NewRawClient(apiClient.HTTPClient(), apiClient.BaseURL())

	edition := common.Edition(cfg.Edition)

	runID := generateRunID(cfg.ExportDirectory)
	runDir := filepath.Join(cfg.ExportDirectory, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("creating run dir: %w", err)
	}

	allDefs := RegisterAll()
	registry := BuildMigrateRegistry(allDefs)
	registry = FilterByEdition(registry, edition)

	// Target the delete* tasks plus the curated set of reset* tasks
	// whose dependency chains do not pull migrate-only create*/set*
	// work back into the plan. The other reset* tasks
	// (resetDefaultProfiles, resetDefaultGates, resetPermissionTemplates)
	// are pulled into the plan as dependencies of the corresponding
	// delete* tasks (so they run first to clear the current default
	// before the destroy call) and are intentionally NOT named here.
	resetPrefixTargets := map[string]bool{
		"resetGlobalSettings": true,
	}
	var targets []string
	for name := range registry {
		if strings.HasPrefix(name, "delete") || resetPrefixTargets[name] {
			targets = append(targets, name)
		}
	}

	taskSet := ResolveDependencies(targets, registry)
	if taskSet == nil {
		return fmt.Errorf("cannot resolve dependencies for delete tasks")
	}

	plan, err := PlanPhases(taskSet, registry)
	if err != nil {
		return err
	}

	// Write metadata.
	meta := map[string]any{
		"plan":           plan,
		"version":        "cloud",
		"edition":        string(edition),
		"enterprise_key": cfg.EnterpriseKey,
		"run_id":         runID,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(runDir, "clear.json"), data, 0o644)

	store := common.NewDataStore(runDir)

	executor := &Executor{
		Cloud:     cc,
		CloudAPI:  apiCC,
		Raw:       raw,
		RawAPI:    rawAPI,
		Store:     store,
		CloudURL:  cloudClient.BaseURL(),
		APIURL:    apiClient.BaseURL(),
		EntKey:    cfg.EnterpriseKey,
		Edition:   edition,
		ExportDir: cfg.ExportDirectory,
		Sem:       make(chan struct{}, cfg.Concurrency),
		Logger:    logger,
	}
	if len(cfg.ConfirmedOrgs) > 0 {
		executor.ResetConfirmedOrgs = make(map[string]bool, len(cfg.ConfirmedOrgs))
		for _, o := range cfg.ConfirmedOrgs {
			executor.ResetConfirmedOrgs[o] = true
		}
	}

	for i, phase := range plan {
		logger.Info("starting phase", "phase", i+1, "tasks", len(phase))
		if err := runResetPhase(ctx, executor, phase, registry); err != nil {
			return fmt.Errorf("phase %d: %w", i+1, err)
		}
		for _, taskName := range phase {
			store.MarkComplete(taskName)
		}
	}

	fmt.Printf("%s v%s - Reset Complete: %s\n", version.ToolName, version.Version, runID)
	return nil
}

func runResetPhase(ctx context.Context, e *Executor, taskNames []string, registry map[string]*TaskDef) error {
	g, ctx := errgroup.WithContext(ctx)
	for _, name := range taskNames {
		def := registry[name]
		e.Logger.Info("running task", "task", name)
		g.Go(func() error {
			taskStart := time.Now()
			counter := NewTaskCounter(name)
			taskCtx := WithTaskCounter(ctx, counter)
			err := def.Run(taskCtx, e)
			// Single end-of-task INFO log carrying counts + duration
			// (#311 + #333). Empty counter → plain duration line.
			counter.LogSummary(e.Logger, time.Since(taskStart))
			if err != nil {
				return fmt.Errorf("task %s: %w", name, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func (cfg *ResetConfig) applyDefaults() {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 25
	}
	if cfg.ExportDirectory == "" {
		cfg.ExportDirectory = "/app/files/"
	}
	if cfg.URL == "" {
		cfg.URL = "https://sonarcloud.io/"
	}
	if cfg.Edition == "" {
		cfg.Edition = "enterprise"
	}
	if cfg.URL != "" && cfg.URL[len(cfg.URL)-1] != '/' {
		cfg.URL += "/"
	}
}
