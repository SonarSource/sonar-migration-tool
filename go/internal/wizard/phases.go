// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package wizard

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/sonar-solutions/sonar-migration-tool/internal/analysis"
	"github.com/sonar-solutions/sonar-migration-tool/internal/extract"
	"github.com/sonar-solutions/sonar-migration-tool/internal/migrate"
	"github.com/sonar-solutions/sonar-migration-tool/internal/report/summary"
	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

// Package-level function vars for external commands. Tests override these.
var (
	runExtractFn = func(ctx context.Context, cfg extract.ExtractConfig) ([]string, error) { return extract.RunExtract(ctx, cfg) }
	runStructureFn = func(exportDir string) error { return structure.RunStructure(exportDir) }
	runMappingsFn  = func(exportDir string) error { return structure.RunMappings(exportDir) }
	runMigrateFn = func(ctx context.Context, cfg migrate.MigrateConfig) (string, error) { return migrate.RunMigrate(ctx, cfg) }
)

// CSV file names used across phases.
const (
	fileOrganizations = "organizations.csv"
	fileProjects      = "projects.csv"
	fileTemplates     = "templates.csv"
	fileProfiles      = "profiles.csv"
	fileGates         = "gates.csv"
	fileGroups        = "groups.csv"
	filePortfolios    = "portfolios.csv"
)

// requiredMappingFiles are the CSV files that must exist before migration.
var requiredMappingFiles = []string{
	fileOrganizations, fileProjects, fileTemplates,
	fileProfiles, fileGates, fileGroups,
}

// --- Phase 1: Extract ---

func phaseExtract(ctx context.Context, p Prompter, state *WizardState, exportDir string) error {
	sourceURL, token, err := promptExtractCredentials(p, state)
	if err != nil {
		return err
	}

	certCfg, err := runExtractWithRetry(ctx, p, state, exportDir, sourceURL, token)
	if err != nil {
		return err
	}
	_ = certCfg // used internally by runExtractWithRetry
	return nil
}

func promptExtractCredentials(p Prompter, state *WizardState) (string, string, error) {
	for {
		sourceURL := ptrStr(state.SourceURL)
		if sourceURL == "" {
			var err error
			sourceURL, err = p.PromptURL("SonarQube Server URL:", true)
			if err != nil {
				return "", "", err
			}
		}

		token, err := p.PromptPassword("Admin token:")
		if err != nil {
			return "", "", err
		}

		ok, err := p.ConfirmReview("Source Server Credentials", []KV{
			{"URL", sourceURL},
			{"Token", "********"},
		})
		if err != nil {
			return "", "", err
		}
		if ok {
			return sourceURL, token, nil
		}
		state.SourceURL = nil
	}
}

func runExtractWithRetry(ctx context.Context, p Prompter, state *WizardState, exportDir, sourceURL, token string) (certConfig, error) {
	var cert certConfig
	for {
		extractID := generateRunID(exportDir)
		cfg := extract.ExtractConfig{
			URL:                sourceURL,
			Token:              token,
			ExportDirectory:    exportDir,
			ExtractID:          extractID,
			Timeout:            120,
			PEMFilePath:        cert.pemFile,
			KeyFilePath:        cert.keyFile,
			CertPassword:       cert.password,
			IncludeScanHistory: true,
		}

		skipped, err := runExtractFn(ctx, cfg)
		if err == nil {
			state.SourceURL = strPtr(sourceURL)
			state.ExtractID = strPtr(extractID)
			state.SkippedProjects = skipped
			state.Phase = PhaseStructure
			displaySkippedProjects(p, skipped)
			return cert, state.Save(exportDir)
		}

		if isSSLError(err) {
			p.DisplayWarning("SSL/TLS certificate error: " + err.Error())
			var promptErr error
			cert, promptErr = promptCertConfig(p)
			if promptErr != nil {
				return cert, promptErr
			}
			continue
		}

		p.DisplayError(err.Error())
		retry, retryErr := p.Confirm("Retry extraction?", true)
		if retryErr != nil {
			return cert, retryErr
		}
		if !retry {
			state.SourceURL = nil
			return cert, fmt.Errorf("extraction cancelled by user")
		}
	}
}

func displaySkippedProjects(p Prompter, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	p.DisplayWarning(fmt.Sprintf("%d project(s) skipped (insufficient privileges):", len(skipped)))
	for _, key := range skipped {
		p.DisplayMessage("  - " + key)
	}
	p.DisplayMessage("  These projects will be excluded from migration.")
}

type certConfig struct {
	pemFile  string
	keyFile  string
	password string
}

func promptCertConfig(p Prompter) (certConfig, error) {
	var cfg certConfig
	var err error
	cfg.pemFile, err = p.PromptText("Path to PEM certificate file:", "")
	if err != nil {
		return cfg, err
	}
	cfg.keyFile, err = p.PromptText("Path to key file (leave empty if included in PEM):", "")
	if err != nil {
		return cfg, err
	}
	cfg.password, err = p.PromptPassword("Certificate password (leave empty if none):")
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

// --- Phase 2: Structure ---

func phaseStructure(ctx context.Context, p Prompter, state *WizardState, exportDir string) error {
	if err := runStructureFn(exportDir); err != nil {
		return err
	}

	displayStructureSummary(p, exportDir)

	state.Phase = PhaseOrgMapping
	return state.Save(exportDir)
}

func displayStructureSummary(p Prompter, exportDir string) {
	orgs, _ := structure.LoadCSV(exportDir, fileOrganizations)
	projects, _ := structure.LoadCSV(exportDir, fileProjects)
	p.DisplaySummary("Structure Analysis", []KV{
		{"Organizations", strconv.Itoa(len(orgs))},
		{"Projects", strconv.Itoa(len(projects))},
	})
}

// --- Phase 3: Organization Mapping ---

func phaseOrgMapping(ctx context.Context, p Prompter, state *WizardState, exportDir string) error {
	if err := promptCloudCredentials(p, state); err != nil {
		return err
	}

	if err := mapAllOrganizations(p, exportDir); err != nil {
		return err
	}

	state.OrganizationsMapped = true
	state.Phase = PhaseMappings
	return state.Save(exportDir)
}

func promptCloudCredentials(p Prompter, state *WizardState) error {
	for {
		targetURL := ptrStr(state.TargetURL)
		if targetURL == "" {
			var err error
			targetURL, err = p.PromptURL("SonarQube Cloud URL:", true)
			if err != nil {
				return err
			}
		}

		entKey := ptrStr(state.EnterpriseKey)
		if entKey == "" {
			var err error
			entKey, err = p.PromptText("Enterprise key:", "")
			if err != nil {
				return err
			}
		}

		ok, err := p.ConfirmReview("Cloud Credentials", []KV{
			{"URL", targetURL},
			{"Enterprise Key", entKey},
		})
		if err != nil {
			return err
		}
		if ok {
			state.TargetURL = strPtr(targetURL)
			state.EnterpriseKey = strPtr(entKey)
			return nil
		}
		state.TargetURL = nil
		state.EnterpriseKey = nil
	}
}

func mapAllOrganizations(p Prompter, exportDir string) error {
	rows, err := structure.LoadCSV(exportDir, fileOrganizations)
	if err != nil {
		return fmt.Errorf("loading %s: %w", fileOrganizations, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("no organizations found — run 'structure' command first")
	}

	for i := 0; i < len(rows); i++ {
		p.SetBackEnabled(i > 0)
		if err := processOrgMapping(p, rows[i], i+1, len(rows)); err != nil {
			if errors.Is(err, ErrBack) && i > 0 {
				rows[i-1]["sonarcloud_org_key"] = ""
				i -= 2 // loop increment brings it to i-1
				continue
			}
			p.SetBackEnabled(false)
			return err
		}
	}
	p.SetBackEnabled(false)

	orgs := orgsFromMaps(rows)
	return structure.ExportCSV(exportDir, "organizations", orgs)
}

func processOrgMapping(p Prompter, org map[string]any, index, total int) error {
	orgKey := mapStr(org, "sonarqube_org_key")
	existing := mapStr(org, "sonarcloud_org_key")

	if existing != "" && existing != SkippedOrgSentinel {
		p.DisplayMessage(fmt.Sprintf("  [%d/%d] %s → %s (already mapped)", index, total, orgKey, existing))
		return nil
	}

	p.DisplaySummary(fmt.Sprintf("Organization %d/%d", index, total), []KV{
		{"Organization Key", orgKey},
		{"Integration Key", mapStr(org, "binding_key")},
		{"ALM", mapStr(org, "alm")},
		{"DevOps URL", mapStr(org, "url")},
		{"Projects", strconv.Itoa(mapInt(org, "project_count"))},
	})

	doMigrate, err := p.Confirm("Migrate this organization?", true)
	if err != nil {
		return err
	}

	if !doMigrate {
		org["sonarcloud_org_key"] = SkippedOrgSentinel
		p.DisplayWarning("Organization skipped")
		return nil
	}

	cloudKey, err := p.PromptText("SonarCloud organization key:", "")
	if err != nil {
		return err
	}
	org["sonarcloud_org_key"] = cloudKey
	p.DisplaySuccess(fmt.Sprintf("  Mapped %s → %s", orgKey, cloudKey))
	return nil
}

// --- Phase 4: Mappings ---

func phaseMappings(ctx context.Context, p Prompter, state *WizardState, exportDir string) error {
	if err := runMappingsFn(exportDir); err != nil {
		return err
	}

	displayMappingsSummary(p, exportDir)

	state.Phase = PhaseValidate
	return state.Save(exportDir)
}

func displayMappingsSummary(p Prompter, exportDir string) {
	files := []struct{ name, label string }{
		{fileTemplates, "Templates"},
		{fileProfiles, "Profiles"},
		{fileGates, "Gates"},
		{fileGroups, "Groups"},
		{filePortfolios, "Portfolios"},
	}
	var stats []KV
	for _, f := range files {
		rows, _ := structure.LoadCSV(exportDir, f.name)
		stats = append(stats, KV{f.label, strconv.Itoa(len(rows))})
	}
	p.DisplaySummary("Mappings Generated", stats)
}

// --- Phase 5: Validate ---

func phaseValidate(ctx context.Context, p Prompter, state *WizardState, exportDir string) error {
	missing := checkRequiredFiles(exportDir, requiredMappingFiles)
	if len(missing) > 0 {
		return fmt.Errorf("missing required files: %v", missing)
	}

	stats, err := buildValidationSummary(exportDir)
	if err != nil {
		return err
	}

	p.DisplaySummary("Migration Summary", stats)

	state.ValidationPassed = true
	state.Phase = PhaseMigrate
	return state.Save(exportDir)
}

func buildValidationSummary(exportDir string) ([]KV, error) {
	orgs, err := structure.LoadCSV(exportDir, fileOrganizations)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", fileOrganizations, err)
	}

	active, skipped := countOrgStatus(orgs)
	projects, _ := structure.LoadCSV(exportDir, fileProjects)
	templates, _ := structure.LoadCSV(exportDir, fileTemplates)
	profiles, _ := structure.LoadCSV(exportDir, fileProfiles)
	gates, _ := structure.LoadCSV(exportDir, fileGates)
	groups, _ := structure.LoadCSV(exportDir, fileGroups)

	return []KV{
		{"Organizations (active)", strconv.Itoa(active)},
		{"Organizations (skipped)", strconv.Itoa(skipped)},
		{"Projects", strconv.Itoa(len(projects))},
		{"Templates", strconv.Itoa(len(templates))},
		{"Profiles", strconv.Itoa(len(profiles))},
		{"Gates", strconv.Itoa(len(gates))},
		{"Groups", strconv.Itoa(len(groups))},
	}, nil
}

func countOrgStatus(orgs []map[string]any) (active, skipped int) {
	for _, org := range orgs {
		key := mapStr(org, "sonarcloud_org_key")
		if key == SkippedOrgSentinel {
			skipped++
		} else if key != "" {
			active++
		}
	}
	return
}

// --- Phase 6: Migrate ---

func phaseMigrate(ctx context.Context, p Prompter, state *WizardState, exportDir string) error {
	p.DisplayWarning("Migration will create and modify resources in SonarQube Cloud.")
	ok, err := p.Confirm("Proceed with migration?", false)
	if err != nil {
		return err
	}
	if !ok {
		p.DisplayMessage("Migration skipped. You can resume later.")
		return fmt.Errorf("migration declined by user")
	}

	token, err := p.PromptPassword("Cloud admin token:")
	if err != nil {
		return err
	}

	return runMigrateWithRetry(ctx, p, state, exportDir, token)
}

func runMigrateWithRetry(ctx context.Context, p Prompter, state *WizardState, exportDir, token string) error {
	for {
		runID := generateRunID(exportDir)
		cfg := migrate.MigrateConfig{
			Token:              token,
			EnterpriseKey:      ptrStr(state.EnterpriseKey),
			URL:                ptrStr(state.TargetURL),
			ExportDirectory:    exportDir,
			IncludeScanHistory: true,
		}

		resultID, err := runMigrateFn(ctx, cfg)
		if err == nil {
			if resultID != "" {
				runID = resultID
			}
			state.MigrationRunID = strPtr(runID)
			state.Phase = nextPhase(PhaseMigrate)
			p.DisplaySuccess(fmt.Sprintf("Migration complete: %s", runID))
			generateAnalysisReport(p, exportDir, runID)
			return state.Save(exportDir)
		}

		p.DisplayError(err.Error())
		retry, retryErr := p.Confirm("Retry migration?", true)
		if retryErr != nil {
			return retryErr
		}
		if !retry {
			return fmt.Errorf("migration cancelled by user")
		}
	}
}

func generateAnalysisReport(p Prompter, exportDir, runID string) {
	runDir := filepath.Join(exportDir, runID)
	rows, err := analysis.GenerateReport(runDir)
	if err != nil {
		p.DisplayWarning("Could not generate analysis report: " + err.Error())
		return
	}
	if len(rows) > 0 {
		p.DisplayMessage(fmt.Sprintf("Analysis report: %d entries written to %s/final_analysis_report.csv", len(rows), runID))
	}

	pdfPath, mdPath, err := summary.GenerateReports(runDir, exportDir, exportDir)
	if err != nil {
		p.DisplayWarning("Could not generate summary reports: " + err.Error())
		return
	}
	p.DisplayMessage(fmt.Sprintf("PDF summary report: %s", pdfPath))
	p.DisplayMessage(fmt.Sprintf("Markdown summary report: %s", mdPath))
}

