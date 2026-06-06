// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"context"
	"crypto/x509"
	"fmt"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

const (
	testSQServerURL  = "https://sq.example.com/"
	testCloudOrgKey  = "cloud-1"
	testEntKey       = "my-enterprise"
	errPromptExtract  = "promptExtractCredentials: %v"
	errExpectComplete = "expected COMPLETE, got %s"
)

// --- Phase 3: Org Mapping Tests ---

func TestPhaseOrgMappingMigrateAndSkip(t *testing.T) {
	dir := t.TempDir()

	orgs := []structure.Organization{
		{SonarQubeOrgKey: "org-1", ServerURL: testSQServerURL, ALM: "github", URL: "https://github.com/o1", ProjectCount: 3},
		{SonarQubeOrgKey: "org-2", ServerURL: testSQServerURL, ALM: "gitlab", URL: "https://gitlab.com/o2", ProjectCount: 1},
	}
	structure.ExportCSV(dir, "organizations", orgs)

	state := &WizardState{Phase: PhaseOrgMapping}
	p := &MockPrompter{
		URLResponses:      []string{testSQCloudURL},
		TextResponses:     []string{testEntKey, "cloud-org-1"},
		ReviewResponses:   []bool{true},
		ConfirmResponses:  []bool{true, false}, // org-1: migrate=yes, org-2: skip
		PasswordResponses: []string{},
	}

	err := phaseOrgMapping(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseOrgMapping: %v", err)
	}

	if state.Phase != PhaseMappings {
		t.Errorf("expected phase MAPPINGS, got %s", state.Phase)
	}
	if !state.OrganizationsMapped {
		t.Error("expected OrganizationsMapped=true")
	}
	if ptrStr(state.TargetURL) != testSQCloudURL {
		t.Errorf("expected TargetURL, got %q", ptrStr(state.TargetURL))
	}

	rows, err := structure.LoadCSV(dir, fileOrganizations)
	if err != nil {
		t.Fatalf("LoadCSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 orgs, got %d", len(rows))
	}
	if mapStr(rows[0], "sonarcloud_org_key") != "cloud-org-1" {
		t.Errorf("org-1: expected cloud-org-1, got %q", mapStr(rows[0], "sonarcloud_org_key"))
	}
	if mapStr(rows[1], "sonarcloud_org_key") != SkippedOrgSentinel {
		t.Errorf("org-2: expected SKIPPED, got %q", mapStr(rows[1], "sonarcloud_org_key"))
	}
}

func TestPhaseOrgMappingAlreadyMapped(t *testing.T) {
	dir := t.TempDir()

	orgs := []structure.Organization{
		{SonarQubeOrgKey: "org-1", SonarCloudOrgKey: "existing-cloud", ServerURL: testSQServerURL},
	}
	structure.ExportCSV(dir, "organizations", orgs)

	state := &WizardState{Phase: PhaseOrgMapping}
	p := &MockPrompter{
		URLResponses:    []string{testSQCloudURL},
		TextResponses:   []string{testEntKey},
		ReviewResponses: []bool{true},
	}

	err := phaseOrgMapping(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseOrgMapping: %v", err)
	}

	rows, _ := structure.LoadCSV(dir, fileOrganizations)
	if mapStr(rows[0], "sonarcloud_org_key") != "existing-cloud" {
		t.Errorf("expected existing mapping preserved, got %q", mapStr(rows[0], "sonarcloud_org_key"))
	}
}

// --- Phase 5: Validate Tests ---

func TestPhaseValidateAllPresent(t *testing.T) {
	dir := t.TempDir()

	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key", "binding_key", "server_url", "alm", "url", "is_cloud", "project_count"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{
		{"org-1", testCloudOrgKey, "binding-1", testSQServerURL, "github", "https://github.com/o1", "true", "3"},
	})
	writeCSV(t, dir, fileProjects, []string{"key", "name"}, [][]string{{"p1", "Project 1"}})
	writeCSV(t, dir, fileTemplates, []string{"name"}, [][]string{{"t1"}})
	writeCSV(t, dir, fileProfiles, []string{"name"}, [][]string{{"pr1"}})
	writeCSV(t, dir, fileGates, []string{"name"}, [][]string{{"g1"}})
	writeCSV(t, dir, fileGroups, []string{"name"}, [][]string{{"gr1"}})

	state := &WizardState{Phase: PhaseValidate}
	p := &MockPrompter{}

	err := phaseValidate(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseValidate: %v", err)
	}
	if state.Phase != PhaseMigrate {
		t.Errorf("expected phase MIGRATE, got %s", state.Phase)
	}
	if !state.ValidationPassed {
		t.Error("expected ValidationPassed=true")
	}
}

func TestPhaseValidateMissingFiles(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, fileOrganizations, []string{"sonarqube_org_key"}, nil)
	writeCSV(t, dir, fileProjects, []string{"key"}, nil)

	state := &WizardState{Phase: PhaseValidate}
	p := &MockPrompter{}

	err := phaseValidate(context.Background(), p, state, dir)
	if err == nil {
		t.Fatal("expected error for missing files")
	}
}

// --- Phase 6: Migrate Tests ---

func TestPhaseMigrateUserCancels(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{
		Phase:     PhaseMigrate,
		TargetURL: strPtr(testSQCloudURL),
	}
	p := &MockPrompter{
		ConfirmResponses: []bool{false},
	}

	err := phaseMigrate(context.Background(), p, state, dir)
	if err == nil {
		t.Fatal("expected error when user declines migration")
	}
	if state.Phase != PhaseMigrate {
		t.Errorf("expected phase unchanged, got %s", state.Phase)
	}
}

// --- Helpers for mocking external calls ---

func mockExtract(err error) func() {
	orig := runExtractFn
	runExtractFn = func(_ context.Context, _ extract.ExtractConfig) ([]string, error) { return nil, err }
	return func() { runExtractFn = orig }
}

func mockStructure(err error) func() {
	orig := runStructureFn
	runStructureFn = func(_ string) error { return err }
	return func() { runStructureFn = orig }
}

func mockMappings(err error) func() {
	orig := runMappingsFn
	runMappingsFn = func(_ string) error { return err }
	return func() { runMappingsFn = orig }
}

func mockMigrate(err error) func() {
	orig := runMigrateFn
	runMigrateFn = func(_ context.Context, _ migrate.MigrateConfig) (string, error) { return "test-run-01", err }
	return func() { runMigrateFn = orig }
}

// --- Phase 1: Extract with mocked RunExtract ---

func TestPhaseExtractSuccess(t *testing.T) {
	restore := mockExtract(nil)
	defer restore()

	dir := t.TempDir()
	state := &WizardState{Phase: PhaseExtract}
	p := &MockPrompter{
		URLResponses:      []string{testSQServerURL},
		PasswordResponses: []string{"token123"},
		ReviewResponses:   []bool{true},
	}

	err := phaseExtract(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseExtract: %v", err)
	}
	if state.Phase != PhaseStructure {
		t.Errorf("expected STRUCTURE, got %s", state.Phase)
	}
	if ptrStr(state.SourceURL) != testSQServerURL {
		t.Errorf("SourceURL: got %q", ptrStr(state.SourceURL))
	}
	if state.ExtractID == nil {
		t.Error("expected ExtractID to be set")
	}
}

func TestRunExtractWithRetrySuccess(t *testing.T) {
	restore := mockExtract(nil)
	defer restore()

	dir := t.TempDir()
	state := &WizardState{}
	p := &MockPrompter{}

	_, err := runExtractWithRetry(context.Background(), p, state, dir, testSQServerURL, "token")
	if err != nil {
		t.Fatalf("runExtractWithRetry: %v", err)
	}
	if state.Phase != PhaseStructure {
		t.Errorf("expected STRUCTURE, got %s", state.Phase)
	}
}

func TestRunExtractWithRetrySSLError(t *testing.T) {
	callCount := 0
	origFn := runExtractFn
	runExtractFn = func(_ context.Context, cfg extract.ExtractConfig) ([]string, error) {
		callCount++
		if callCount == 1 {
			return nil, fmt.Errorf("connect: %w", x509.UnknownAuthorityError{})
		}
		return nil, nil // second call succeeds with cert
	}
	defer func() { runExtractFn = origFn }()

	dir := t.TempDir()
	state := &WizardState{}
	p := &MockPrompter{
		TextResponses:     []string{"/cert.pem", ""},
		PasswordResponses: []string{""},
	}

	_, err := runExtractWithRetry(context.Background(), p, state, dir, testSQServerURL, "token")
	if err != nil {
		t.Fatalf("runExtractWithRetry with SSL retry: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 extract calls, got %d", callCount)
	}
}

func TestRunExtractWithRetryDecline(t *testing.T) {
	restore := mockExtract(fmt.Errorf("connection refused"))
	defer restore()

	dir := t.TempDir()
	state := &WizardState{}
	p := &MockPrompter{
		ConfirmResponses: []bool{false}, // decline retry
	}

	_, err := runExtractWithRetry(context.Background(), p, state, dir, testSQServerURL, "token")
	if err == nil {
		t.Fatal("expected error when declining retry")
	}
}

// --- Phase 2: Structure with mocked RunStructure ---

func TestPhaseStructureSuccess(t *testing.T) {
	dir := t.TempDir()
	// Create fixture CSVs that RunStructure would produce
	orgs := []structure.Organization{{SonarQubeOrgKey: "org-1", ProjectCount: 3}}
	structure.ExportCSV(dir, "organizations", orgs)
	projs := []structure.Project{{Key: "p1", Name: "P1"}}
	structure.ExportCSV(dir, "projects", projs)

	restore := mockStructure(nil)
	defer restore()

	state := &WizardState{Phase: PhaseStructure}
	p := &MockPrompter{}

	err := phaseStructure(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseStructure: %v", err)
	}
	if state.Phase != PhaseOrgMapping {
		t.Errorf("expected ORG_MAPPING, got %s", state.Phase)
	}
}

// --- Phase 4: Mappings with mocked RunMappings ---

func TestPhaseMappingsSuccess(t *testing.T) {
	dir := t.TempDir()
	// Create fixture CSVs that RunMappings would produce
	for _, name := range []string{"templates", "profiles", "gates", "groups", "portfolios"} {
		writeCSV(t, dir, name+".csv", []string{"name"}, [][]string{{"item1"}})
	}

	restore := mockMappings(nil)
	defer restore()

	state := &WizardState{Phase: PhaseMappings}
	p := &MockPrompter{}

	err := phaseMappings(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseMappings: %v", err)
	}
	if state.Phase != PhaseValidate {
		t.Errorf("expected VALIDATE, got %s", state.Phase)
	}
}

// --- Phase 6: Migrate with mocked RunMigrate ---

func TestPhaseMigrateSuccess(t *testing.T) {
	restore := mockMigrate(nil)
	defer restore()

	dir := t.TempDir()
	state := &WizardState{
		Phase:         PhaseMigrate,
		TargetURL:     strPtr(testSQCloudURL),
		EnterpriseKey: strPtr(testEntKey),
	}
	p := &MockPrompter{
		ConfirmResponses:  []bool{true},      // proceed with migration
		PasswordResponses: []string{"token"},  // cloud token
	}

	err := phaseMigrate(context.Background(), p, state, dir)
	if err != nil {
		t.Fatalf("phaseMigrate: %v", err)
	}
	if state.Phase != PhaseComplete {
		t.Errorf(errExpectComplete, state.Phase)
	}
	if state.MigrationRunID == nil {
		t.Error("expected MigrationRunID to be set")
	}
}

func TestRunMigrateWithRetryDecline(t *testing.T) {
	restore := mockMigrate(fmt.Errorf("auth error"))
	defer restore()

	dir := t.TempDir()
	state := &WizardState{TargetURL: strPtr(testSQCloudURL), EnterpriseKey: strPtr(testEntKey)}
	p := &MockPrompter{
		ConfirmResponses: []bool{false}, // decline retry
	}

	err := runMigrateWithRetry(context.Background(), p, state, dir, "token")
	if err == nil {
		t.Fatal("expected error when declining retry")
	}
}

func TestRunMigrateWithRetrySuccess(t *testing.T) {
	restore := mockMigrate(nil)
	defer restore()

	dir := t.TempDir()
	state := &WizardState{TargetURL: strPtr(testSQCloudURL), EnterpriseKey: strPtr(testEntKey)}
	p := &MockPrompter{}

	err := runMigrateWithRetry(context.Background(), p, state, dir, "token")
	if err != nil {
		t.Fatalf("runMigrateWithRetry: %v", err)
	}
	if state.Phase != PhaseComplete {
		t.Errorf(errExpectComplete, state.Phase)
	}
}

// --- Full wizard run with mocked commands ---

func TestRunFullWizardMocked(t *testing.T) {
	restoreE := mockExtract(nil)
	defer restoreE()
	restoreS := mockStructure(nil)
	defer restoreS()
	restoreM := mockMappings(nil)
	defer restoreM()
	restoreMig := mockMigrate(nil)
	defer restoreMig()

	dir := t.TempDir()

	// Create CSVs that structure/mappings would produce
	orgs := []structure.Organization{
		{SonarQubeOrgKey: "org-1", ServerURL: testSQServerURL, ProjectCount: 2},
	}
	structure.ExportCSV(dir, "organizations", orgs)
	projs := []structure.Project{{Key: "p1", Name: "P1"}}
	structure.ExportCSV(dir, "projects", projs)
	for _, name := range []string{"templates", "profiles", "gates", "groups", "portfolios"} {
		writeCSV(t, dir, name+".csv", []string{"name"}, [][]string{{"item1"}})
	}

	p := &MockPrompter{
		URLResponses: []string{testSQServerURL, testSQCloudURL},
		TextResponses: []string{
			testEntKey,   // enterprise key
			"cloud-org",  // cloud org key for org-1
		},
		PasswordResponses: []string{
			"server-token", // extract token
			"cloud-token",  // migrate token
		},
		ReviewResponses: []bool{
			true, // extract credentials review
			true, // cloud credentials review
		},
		ConfirmResponses: []bool{
			false, // include project data
			true,  // migrate org-1
			true,  // proceed with migration
		},
	}

	err := Run(context.Background(), p, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	loaded, _ := Load(dir)
	if loaded.Phase != PhaseComplete {
		t.Errorf(errExpectComplete, loaded.Phase)
	}
}

// --- Phase 1: Extract Credential Prompt Tests ---

func TestPromptExtractCredentialsAcceptFirst(t *testing.T) {
	state := &WizardState{}
	p := &MockPrompter{
		URLResponses:      []string{testSQServerURL},
		PasswordResponses: []string{"token123"},
		ReviewResponses:   []bool{true},
	}

	url, token, err := promptExtractCredentials(p, state)
	if err != nil {
		t.Fatalf(errPromptExtract, err)
	}
	if url != testSQServerURL {
		t.Errorf("url: got %q", url)
	}
	if token != "token123" {
		t.Errorf("token: got %q", token)
	}
}

func TestPromptExtractCredentialsRejectThenAccept(t *testing.T) {
	state := &WizardState{}
	p := &MockPrompter{
		URLResponses:      []string{testSQServerURL, "https://other.example.com/"},
		PasswordResponses: []string{"bad-token", "good-token"},
		ReviewResponses:   []bool{false, true},
	}

	url, token, err := promptExtractCredentials(p, state)
	if err != nil {
		t.Fatalf(errPromptExtract, err)
	}
	if url != "https://other.example.com/" {
		t.Errorf("url: got %q", url)
	}
	if token != "good-token" {
		t.Errorf("token: got %q", token)
	}
}

func TestPromptExtractCredentialsUsesExistingURL(t *testing.T) {
	state := &WizardState{SourceURL: strPtr("https://existing.example.com/")}
	p := &MockPrompter{
		PasswordResponses: []string{"token123"},
		ReviewResponses:   []bool{true},
	}

	url, _, err := promptExtractCredentials(p, state)
	if err != nil {
		t.Fatalf(errPromptExtract, err)
	}
	if url != "https://existing.example.com/" {
		t.Errorf("expected existing URL, got %q", url)
	}
}

// --- promptCertConfig ---

func TestPromptCertConfig(t *testing.T) {
	p := &MockPrompter{
		TextResponses:     []string{"/path/to/cert.pem", "/path/to/key.pem"},
		PasswordResponses: []string{"certpass"},
	}

	cfg, err := promptCertConfig(p)
	if err != nil {
		t.Fatalf("promptCertConfig: %v", err)
	}
	if cfg.pemFile != "/path/to/cert.pem" {
		t.Errorf("pemFile: got %q", cfg.pemFile)
	}
	if cfg.keyFile != "/path/to/key.pem" {
		t.Errorf("keyFile: got %q", cfg.keyFile)
	}
	if cfg.password != "certpass" {
		t.Errorf("password: got %q", cfg.password)
	}
}

// --- promptCloudCredentials ---

func TestPromptCloudCredentialsAcceptFirst(t *testing.T) {
	state := &WizardState{}
	p := &MockPrompter{
		URLResponses:    []string{testSQCloudURL},
		TextResponses:   []string{testEntKey},
		ReviewResponses: []bool{true},
	}

	err := promptCloudCredentials(p, state)
	if err != nil {
		t.Fatalf("promptCloudCredentials: %v", err)
	}
	if ptrStr(state.TargetURL) != testSQCloudURL {
		t.Errorf("TargetURL: got %q", ptrStr(state.TargetURL))
	}
	if ptrStr(state.EnterpriseKey) != testEntKey {
		t.Errorf("EnterpriseKey: got %q", ptrStr(state.EnterpriseKey))
	}
}

func TestPromptCloudCredentialsRejectThenAccept(t *testing.T) {
	state := &WizardState{}
	p := &MockPrompter{
		URLResponses:    []string{"https://wrong.io/", testSQCloudURL},
		TextResponses:   []string{"wrong-key", "correct-key"},
		ReviewResponses: []bool{false, true},
	}

	err := promptCloudCredentials(p, state)
	if err != nil {
		t.Fatalf("promptCloudCredentials: %v", err)
	}
	if ptrStr(state.TargetURL) != testSQCloudURL {
		t.Errorf("TargetURL: got %q", ptrStr(state.TargetURL))
	}
	if ptrStr(state.EnterpriseKey) != "correct-key" {
		t.Errorf("EnterpriseKey: got %q", ptrStr(state.EnterpriseKey))
	}
}

// --- displayStructureSummary and displayMappingsSummary ---

func TestDisplayStructureSummary(t *testing.T) {
	dir := t.TempDir()
	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{{"org-1", testCloudOrgKey}})
	writeCSV(t, dir, fileProjects, []string{"key"}, [][]string{{"p1"}, {"p2"}})

	p := &MockPrompter{}
	displayStructureSummary(p, dir)
}

func TestDisplayMappingsSummary(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"templates", "profiles", "gates", "groups", "portfolios"} {
		writeCSV(t, dir, name+".csv", []string{"name"}, [][]string{{"item1"}})
	}

	p := &MockPrompter{}
	displayMappingsSummary(p, dir)
}

// --- buildValidationSummary ---

func TestBuildValidationSummary(t *testing.T) {
	dir := t.TempDir()
	orgHeaders := []string{"sonarqube_org_key", "sonarcloud_org_key"}
	writeCSV(t, dir, fileOrganizations, orgHeaders, [][]string{
		{"org-1", testCloudOrgKey}, {"org-2", SkippedOrgSentinel},
	})
	writeCSV(t, dir, fileProjects, []string{"key"}, [][]string{{"p1"}})
	writeCSV(t, dir, fileTemplates, []string{"name"}, [][]string{{"t1"}})
	writeCSV(t, dir, fileProfiles, []string{"name"}, [][]string{{"pr1"}, {"pr2"}})
	writeCSV(t, dir, fileGates, []string{"name"}, [][]string{{"g1"}})
	writeCSV(t, dir, fileGroups, []string{"name"}, [][]string{{"gr1"}})

	stats, err := buildValidationSummary(dir)
	if err != nil {
		t.Fatalf("buildValidationSummary: %v", err)
	}

	findStat := func(key string) string {
		for _, s := range stats {
			if s.Key == key {
				return s.Value
			}
		}
		return ""
	}
	if findStat("Organizations (active)") != "1" {
		t.Errorf("active orgs: got %q", findStat("Organizations (active)"))
	}
	if findStat("Organizations (skipped)") != "1" {
		t.Errorf("skipped orgs: got %q", findStat("Organizations (skipped)"))
	}
	if findStat("Profiles") != "2" {
		t.Errorf("profiles: got %q", findStat("Profiles"))
	}
}

// --- Phase 2: Structure Tests ---

func TestPhaseStructureNoExtracts(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseStructure}
	p := &MockPrompter{}

	err := phaseStructure(context.Background(), p, state, dir)
	if err == nil {
		t.Fatal("expected error when no extracts exist")
	}
}

// --- Phase 4: Mappings Tests ---

func TestPhaseMappingsNoExtracts(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{Phase: PhaseMappings}
	p := &MockPrompter{}

	err := phaseMappings(context.Background(), p, state, dir)
	if err == nil {
		t.Fatal("expected error when no extracts exist")
	}
}

// --- countOrgStatus ---

func TestCountOrgStatus(t *testing.T) {
	orgs := []map[string]any{
		{"sonarcloud_org_key": testCloudOrgKey},
		{"sonarcloud_org_key": SkippedOrgSentinel},
		{"sonarcloud_org_key": "cloud-2"},
		{"sonarcloud_org_key": ""},
	}
	active, skipped := countOrgStatus(orgs)
	if active != 2 {
		t.Errorf("active: got %d, want 2", active)
	}
	if skipped != 1 {
		t.Errorf("skipped: got %d, want 1", skipped)
	}
}

func TestDisplaySkippedProjects(t *testing.T) {
	p := &MockPrompter{}
	displaySkippedProjects(p, []string{"proj-a", "proj-b"})

	if len(p.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %v", len(p.Messages), p.Messages)
	}
	if p.Messages[0] != "WARN:2 project(s) skipped (insufficient privileges):" {
		t.Errorf("unexpected warning: %s", p.Messages[0])
	}
}

func TestDisplaySkippedProjectsEmpty(t *testing.T) {
	p := &MockPrompter{}
	displaySkippedProjects(p, nil)

	if len(p.Messages) != 0 {
		t.Errorf("expected no messages for empty skipped, got %d", len(p.Messages))
	}
}

func TestGenerateAnalysisReportNoData(t *testing.T) {
	p := &MockPrompter{}
	// Call with a valid temp dir but no run data — exercises the function
	// regardless of whether analysis.GenerateReport errors or returns empty.
	generateAnalysisReport(p, t.TempDir(), "nonexistent-run")
}

func TestPhaseMigrateDecline(t *testing.T) {
	dir := t.TempDir()
	state := &WizardState{
		Phase:     PhaseMigrate,
		TargetURL: strPtr(testSQCloudURL),
	}
	p := &MockPrompter{
		ConfirmResponses: []bool{false},
	}

	err := phaseMigrate(context.Background(), p, state, dir)
	if err == nil {
		t.Fatal("expected error when user declines migration")
	}
}

func TestRunMigrateWithRetryRetryThenSucceed(t *testing.T) {
	callCount := 0
	origFn := runMigrateFn
	runMigrateFn = func(_ context.Context, cfg migrate.MigrateConfig) (string, error) {
		callCount++
		if callCount == 1 {
			return "", fmt.Errorf("transient error")
		}
		return "retry-run-01", nil
	}
	defer func() { runMigrateFn = origFn }()

	dir := t.TempDir()
	state := &WizardState{
		TargetURL:     strPtr(testSQCloudURL),
		EnterpriseKey: strPtr(testEntKey),
	}
	p := &MockPrompter{
		ConfirmResponses: []bool{true},
	}

	err := runMigrateWithRetry(context.Background(), p, state, dir, "token")
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestProcessOrgMappingAlreadyMapped(t *testing.T) {
	org := map[string]any{
		"sonarqube_org_key":  "org-1",
		"sonarcloud_org_key": "already-mapped",
	}
	p := &MockPrompter{}
	err := processOrgMapping(p, org, 1, 1)
	if err != nil {
		t.Fatalf("expected nil for already-mapped org, got %v", err)
	}
}
