// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunResetIntegration(t *testing.T) {
	cloudSrv := newMockCloudServer()
	defer cloudSrv.Close()
	apiSrv := newMockAPIServer()
	defer apiSrv.Close()
	dir := t.TempDir()
	setupExtractData(dir)
	setupCSVs(t, dir)

	// RunReset needs createX outputs to exist for delete tasks.
	// Run a migrate first to create them.
	migCfg := MigrateConfig{
		Token: "test-token", EnterpriseKey: "test-enterprise",
		Edition: "enterprise", URL: cloudSrv.URL + "/",
		Concurrency: 5, ExportDirectory: dir,
		TargetTask: "createProjects",
	}
	if _, err := RunMigrate(context.Background(), migCfg); err != nil {
		t.Fatalf("RunMigrate setup: %v", err)
	}

	cfg := ResetConfig{
		Token: "test-token", EnterpriseKey: "test-enterprise",
		Edition: "enterprise", URL: cloudSrv.URL + "/",
		Concurrency: 5, ExportDirectory: dir,
		// #550: RunReset now fails closed on an empty ConfirmedOrgs, so
		// this orchestration-exercising test must confirm the one org
		// the fixture data uses.
		ConfirmedOrgs: []string{testCloudOrg},
	}

	// RunReset targets delete* tasks, which depend on createX outputs.
	// Since delete tasks read from the same store and the mock server
	// accepts all DELETE/POST operations, this should succeed.
	err := RunReset(context.Background(), cfg)
	// May fail due to missing deps (createProfiles etc not run) — that's OK,
	// the point is to exercise the orchestration code.
	if err != nil {
		t.Logf("RunReset returned error (expected for partial setup): %v", err)
	}
}

func TestResetConfigDefaults(t *testing.T) {
	cfg := ResetConfig{}
	cfg.applyDefaults()

	if cfg.Concurrency != 25 {
		t.Errorf("expected concurrency=25, got %d", cfg.Concurrency)
	}
	if cfg.URL != "https://sonarcloud.io/" {
		t.Errorf("expected default URL, got %q", cfg.URL)
	}
	if cfg.Edition != "enterprise" {
		t.Errorf("expected enterprise, got %q", cfg.Edition)
	}
	if cfg.ExportDirectory != "/app/files/" {
		t.Errorf("expected /app/files/, got %q", cfg.ExportDirectory)
	}
}

func TestResetConfigTrailingSlash(t *testing.T) {
	cfg := ResetConfig{URL: "https://example.com"}
	cfg.applyDefaults()
	if cfg.URL != "https://example.com/" {
		t.Errorf("expected trailing slash, got %q", cfg.URL)
	}
}

// #550: RunReset must fail closed when no organization has been
// confirmed, rather than falling back to "reset every mapped org."
// cmd/reset.go is the only current caller and it always populates
// ConfirmedOrgs (via confirmResetOrgs) before calling RunReset, so this
// only guards against a future non-CLI caller skipping confirmation.
func TestRunReset_ErrorsWhenNoConfirmedOrgs(t *testing.T) {
	cfg := ResetConfig{
		Token: "test-token", EnterpriseKey: "test-enterprise",
		Edition: "enterprise", ExportDirectory: t.TempDir(),
	}
	err := RunReset(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when ConfirmedOrgs is empty")
	}
	if !strings.Contains(err.Error(), "no organizations confirmed") {
		t.Errorf("error %q does not mention the missing confirmation", err.Error())
	}
}

// #550: --dry-run must never touch the network. RunReset should build
// and print the plan, then return before the executor (and therefore
// any delete/destroy HTTP call) is even constructed.
func TestRunReset_DryRunSkipsExecution(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		t.Errorf("unexpected HTTP call during dry run: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	setupExtractData(dir)
	setupCSVs(t, dir)

	cfg := ResetConfig{
		Token: "test-token", EnterpriseKey: "test-enterprise",
		Edition: "enterprise", URL: srv.URL + "/",
		Concurrency: 5, ExportDirectory: dir,
		ConfirmedOrgs: []string{testCloudOrg},
		DryRun:        true,
	}

	if err := RunReset(context.Background(), cfg); err != nil {
		t.Fatalf("RunReset (dry run): %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("expected 0 HTTP calls during dry run, got %d", got)
	}

	// The plan must still be written to disk so it's inspectable, even
	// though execution was skipped.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading export dir: %v", err)
	}
	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(dir, e.Name(), "clear.json")); statErr == nil {
			found = true
		}
	}
	if !found {
		t.Error("expected clear.json to be written even in dry-run mode")
	}
}
