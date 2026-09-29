---
spec_id: SPEC-025
title: Configuration & Validation (Enhanced)
status: draft
priority: P2
epic: "User Experience"
depends_on: []
estimated_effort: M
cloudvoyager_ref: "src/shared/config/, src/commands/validate/, src/commands/test/"
---

# SPEC-025: Configuration & Validation (Enhanced)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

CloudVoyager uses Ajv (Another JSON Schema Validator) for rigorous, schema-driven validation of all configuration files before any API calls are made. Every configuration property has type constraints, minimum/maximum bounds, default values, and human-readable descriptions defined in JSON Schema format. The Go tool currently relies on ad-hoc validation scattered across command handlers -- checking for empty strings, parsing flags, and failing at runtime when API calls return errors due to misconfiguration. This spec defines a comprehensive configuration and validation system for the Go tool that matches CloudVoyager's rigor using Go-idiomatic approaches (struct tags with the `go-playground/validator` library) while also introducing two new utility commands: `validate` (offline config validation) and `test` (connectivity verification).

CloudVoyager's configuration system spans multiple files: `src/shared/config/schema.js` defines the transfer config schema, `src/shared/config/schema-migrate.js` defines the migrate config schema, `src/shared/config/schema-shared/` contains shared sub-schemas for performance tuning and rate limiting, and `src/shared/config/loader-migrate/` contains the loading pipeline (file read, schema validation, default application, environment variable overrides). The validation pipeline is: (1) read JSON file, (2) validate against Ajv schema with `allErrors: true` and `useDefaults: true`, (3) apply environment variable overrides, (4) return validated config with defaults filled in.

The Go tool must implement equivalent functionality using Go's type system and struct tags. Configuration structs serve as both the schema definition and the deserialization target. The `go-playground/validator` library provides `validate:"required,url"`, `validate:"min=1,max=100"`, and custom validators. Unknown fields must trigger warnings (not silently ignored), which requires custom JSON unmarshalling with `DisallowUnknownFields()` or a post-parse field comparison.

## Problem Statement

The current Go tool has three configuration gaps that cause friction and preventable errors:

1. **Late failure**: Misconfigured URLs, missing tokens, and invalid concurrency values are only detected when the corresponding API call fails -- often minutes into a migration run. CloudVoyager validates all configuration upfront before any work begins.

2. **No schema enforcement**: There are no type constraints, range bounds, or required-field checks on configuration values. A user can set `concurrency: -5` or `url: "not-a-url"` and the tool will attempt to use these values. CloudVoyager's Ajv schemas catch all such errors at parse time with precise field-path error messages.

3. **No standalone validation**: Users cannot verify their configuration file is correct without actually running a migration. CloudVoyager provides a `validate` command for offline validation and a `test` command for connectivity verification. These reduce the feedback loop from "run migration, wait 5 minutes, see error" to "validate config, see error immediately."

Additionally, the new commands specified in other specs (sync-metadata, transfer, verify) each have their own configuration requirements. A unified configuration system ensures consistency across all commands.

## User Stories

- **As a** migration operator, **I want** the tool to validate my configuration file at startup and report all errors at once, **so that** I fix all problems before any API calls are made.
- **As a** migration operator, **I want** field-level error messages with JSON paths (e.g., "performance.issueSync.concurrency must be >= 1"), **so that** I know exactly which configuration value to fix.
- **As a** migration operator, **I want** a `validate` command that checks my config file without making any API calls, **so that** I can verify correctness in a CI/CD pipeline or air-gapped environment.
- **As a** migration operator, **I want** a `test` command that validates my config and tests connectivity to both SQ Server and SC, **so that** I can verify my tokens and URLs are correct before starting a migration.
- **As a** migration operator, **I want** environment variables (`SONARQUBE_TOKEN`, `SONARCLOUD_TOKEN`, `SONARQUBE_URL`, `SONARCLOUD_URL`) to override config file values, **so that** I can inject secrets without storing them in files.
- **As a** migration operator, **I want** warnings on unknown configuration fields, **so that** typos like `"sonaqube"` instead of `"sonarqube"` are caught immediately.
- **As a** migration operator, **I want** sensible defaults for all optional configuration fields, **so that** I only need to specify the values I want to change from defaults.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Define Go configuration structs with `json` tags for deserialization and `validate` tags for validation | Must |
| FR-2 | Validate all configuration at startup (before any API calls) for every command that loads a config file | Must |
| FR-3 | Report all validation errors at once (not fail on first error) with JSON-path field references | Must |
| FR-4 | Warn on unknown/unrecognized fields in the configuration file (do not silently ignore them) | Must |
| FR-5 | Apply sensible defaults for all optional fields (matching CloudVoyager defaults) | Must |
| FR-6 | Support environment variable overrides: `SONARQUBE_TOKEN`, `SONARCLOUD_TOKEN`, `SONARQUBE_URL`, `SONARCLOUD_URL` | Must |
| FR-7 | Implement `validate` command: parse config, validate schema, report errors/warnings, exit 0/1 | Must |
| FR-8 | Implement `test` command: validate config, test connectivity to SQ Server and SC, report versions and auth status | Must |
| FR-9 | Support unified config file schema that covers all commands (transfer, migrate, sync-metadata, verify) | Must |
| FR-10 | Validate URL fields as valid HTTP/HTTPS URLs with no trailing slash | Must |
| FR-11 | Validate token fields as non-empty strings (no format enforcement since tokens vary by version) | Must |
| FR-12 | Validate concurrency fields as positive integers within defined ranges (1-128 for general, 1-16 for project-level) | Must |
| FR-13 | Validate rate limit fields: maxRetries (0-20), baseDelay (0-60000 ms), minRequestInterval (0-10000 ms) | Must |
| FR-14 | Validate transfer mode as one of "incremental" or "full" | Must |
| FR-15 | Validate checkpoint configuration: enabled (bool), cacheMaxAgeDays (1-365), strictResume (bool) | Should |
| FR-16 | Precedence order (lowest to highest): defaults, config file, environment variables, CLI flags. CLI flags take highest precedence. | Must |
| FR-17 | Print a human-readable validation summary showing all errors and warnings with suggested fixes | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Configuration validation must complete in under 100 ms | < 100 ms |
| NFR-2 | `validate` command must work completely offline (no network calls) | Offline capable |
| NFR-3 | `test` command must complete in under 10 seconds (including network calls) | < 10s |
| NFR-4 | Configuration structs must be the single source of truth for schema (no separate schema files) | Single source |
| NFR-5 | Validation error messages must be actionable (include field path, constraint, and current value) | Actionable errors |
| NFR-6 | Must support both flat and sectioned config file formats for backward compatibility | Backward compat |

## Technical Design

### Architecture

The configuration system is centralized in a new `internal/config` package:

```
internal/config/
├── config.go          # Top-level UnifiedConfig struct with validation tags
├── sonarqube.go       # SonarQubeConfig sub-struct
├── sonarcloud.go      # SonarCloudConfig sub-struct (with organizations)
├── transfer.go        # TransferConfig sub-struct
├── migrate.go         # MigrateConfig sub-struct
├── performance.go     # PerformanceConfig sub-struct
├── ratelimit.go       # RateLimitConfig sub-struct
├── checkpoint.go      # CheckpointConfig sub-struct
├── defaults.go        # Default value constants and application
├── loader.go          # Load from file + apply env overrides + validate
├── validator.go       # Custom validators and error formatting
├── unknown_fields.go  # Unknown field detection
└── config_test.go     # Comprehensive validation tests

cmd/
├── validate.go        # NEW: validate command
├── test_connection.go # NEW: test command
```

### Key Data Structures

```go
// UnifiedConfig is the top-level configuration struct used by all commands.
// It is the single source of truth for config file schema, validation rules,
// and default values.
type UnifiedConfig struct {
    SonarQube   SonarQubeConfig   `json:"sonarqube" validate:"required"`
    SonarCloud  SonarCloudConfig  `json:"sonarcloud" validate:"required"`
    Transfer    TransferConfig    `json:"transfer"`
    Migrate     MigrateConfig     `json:"migrate"`
    Performance PerformanceConfig `json:"performance"`
    RateLimit   RateLimitConfig   `json:"rateLimit"`
}

type SonarQubeConfig struct {
    URL        string `json:"url" validate:"required,url" default:"" description:"SonarQube Server base URL"`
    Token      string `json:"token" validate:"required" default:"" description:"SonarQube Server authentication token"`
    ProjectKey string `json:"projectKey,omitempty" validate:"omitempty" description:"Single project key (transfer command only)"`
}

type SonarCloudConfig struct {
    URL            string                `json:"url" validate:"omitempty,url" default:"https://sonarcloud.io" description:"SonarQube Cloud base URL"`
    Token          string                `json:"token,omitempty" validate:"omitempty" description:"SonarQube Cloud token (single-org mode)"`
    Organization   string                `json:"organization,omitempty" validate:"omitempty" description:"SC organization key (single-org mode)"`
    ProjectKey     string                `json:"projectKey,omitempty" validate:"omitempty" description:"SC project key (transfer command only)"`
    Enterprise     EnterpriseConfig      `json:"enterprise,omitempty" description:"Enterprise configuration (multi-org mode)"`
    Organizations  []OrganizationConfig  `json:"organizations,omitempty" validate:"omitempty,dive" description:"Organization list (multi-org mode)"`
}

type EnterpriseConfig struct {
    Key string `json:"key,omitempty" validate:"omitempty" description:"Enterprise key for multi-org migrations"`
}

type OrganizationConfig struct {
    Key   string `json:"key" validate:"required" description:"SC organization key"`
    Token string `json:"token" validate:"required" description:"SC organization token"`
    URL   string `json:"url,omitempty" validate:"omitempty,url" description:"SC URL override for this org"`
}

type TransferConfig struct {
    Mode             string           `json:"mode" validate:"omitempty,oneof=incremental full" default:"incremental" description:"Transfer mode"`
    BatchSize        int              `json:"batchSize" validate:"omitempty,min=1,max=10000" default:"100" description:"Batch size for paginated API calls"`
    SyncAllBranches  bool             `json:"syncAllBranches" default:"true" description:"Sync all branches or main only"`
    ExcludeBranches  []string         `json:"excludeBranches,omitempty" description:"Branch name patterns to exclude"`
    Checkpoint       CheckpointConfig `json:"checkpoint" description:"Checkpoint/resume configuration"`
}

type CheckpointConfig struct {
    Enabled          bool `json:"enabled" default:"true" description:"Enable checkpoint-based resume"`
    CacheExtractions bool `json:"cacheExtractions" default:"true" description:"Cache extraction results for resume"`
    CacheMaxAgeDays  int  `json:"cacheMaxAgeDays" validate:"omitempty,min=1,max=365" default:"7" description:"Max age of cached extractions in days"`
    StrictResume     bool `json:"strictResume" default:"false" description:"Fail if checkpoint state is inconsistent"`
}

type MigrateConfig struct {
    OutputDir               string `json:"outputDir" default:"./migration-output" description:"Output directory for reports and logs"`
    DryRun                  bool   `json:"dryRun" default:"false" description:"Simulate migration without making changes"`
    SkipIssueMetadataSync   bool   `json:"skipIssueMetadataSync" default:"false" description:"Skip issue metadata synchronization"`
    SkipHotspotMetadataSync bool   `json:"skipHotspotMetadataSync" default:"false" description:"Skip hotspot metadata synchronization"`
    SkipQualityProfileSync  bool   `json:"skipQualityProfileSync" default:"false" description:"Skip quality profile synchronization"`
}

type PerformanceConfig struct {
    AutoTune            bool                   `json:"autoTune" default:"false" description:"Auto-detect hardware and set optimal values"`
    MaxConcurrency      int                    `json:"maxConcurrency" validate:"omitempty,min=1,max=128" default:"64" description:"General concurrency limit"`
    MaxMemoryMB         int                    `json:"maxMemoryMB" validate:"omitempty,min=0,max=32768" default:"8192" description:"Max memory in MB (0 = Go default)"`
    SourceExtraction    ConcurrencyConfig      `json:"sourceExtraction" description:"Source extraction concurrency"`
    HotspotExtraction   ConcurrencyConfig      `json:"hotspotExtraction" description:"Hotspot extraction concurrency"`
    IssueSync           ConcurrencyConfig      `json:"issueSync" description:"Issue sync concurrency"`
    HotspotSync         ConcurrencyConfig      `json:"hotspotSync" description:"Hotspot sync concurrency"`
    ProjectMigration    ProjectConcurrencyConfig `json:"projectMigration" description:"Project migration concurrency"`
    ProjectVerification ProjectConcurrencyConfig `json:"projectVerification" description:"Project verification concurrency"`
}

type ConcurrencyConfig struct {
    Concurrency int `json:"concurrency" validate:"omitempty,min=1,max=100" default:"50" description:"Max concurrent operations"`
}

type ProjectConcurrencyConfig struct {
    Concurrency int `json:"concurrency" validate:"omitempty,min=1,max=16" default:"8" description:"Max concurrent project operations"`
}

type RateLimitConfig struct {
    MaxRetries         int `json:"maxRetries" validate:"omitempty,min=0,max=20" default:"3" description:"Max retry attempts on 429/503"`
    BaseDelay          int `json:"baseDelay" validate:"omitempty,min=0,max=60000" default:"1000" description:"Initial retry delay in ms"`
    MinRequestInterval int `json:"minRequestInterval" validate:"omitempty,min=0,max=10000" default:"0" description:"Min ms between POST requests"`
}
```

### Key Algorithms

**Configuration Loading Pipeline**:

```
algorithm loadConfig(filePath, command):
    // Step 1: Read and parse JSON
    raw = readFile(filePath)
    
    // Step 2: Detect unknown fields
    unknownFields = detectUnknownFields(raw, UnifiedConfig{})
    for each field in unknownFields:
        warnings.append("Unknown field '%s' -- did you mean '%s'?", field, closestMatch(field))
    
    // Step 3: Unmarshal into struct (with defaults applied)
    config = UnifiedConfig{}
    applyDefaults(&config)
    json.Unmarshal(raw, &config)
    
    // Step 4: Apply environment variable overrides
    applyEnvOverrides(&config)
    
    // Step 4.5: Apply CLI flag overrides (highest precedence)
    applyCLIFlagOverrides(&config, command)
    
    // Step 5: Validate
    errors = validate(config)
    
    // Step 6: Return
    if len(errors) > 0:
        return nil, ValidationError{Errors: errors, Warnings: warnings}
    return config, nil (with warnings logged)
```

**Unknown Field Detection**:

```
algorithm detectUnknownFields(rawJSON, targetStruct):
    // Parse JSON into a generic map
    var rawMap map[string]json.RawMessage
    json.Unmarshal(rawJSON, &rawMap)
    
    // Get known field names from struct tags
    knownFields = set()
    for each field in reflect.TypeOf(targetStruct).Fields:
        jsonTag = field.Tag.Get("json")
        name = strings.Split(jsonTag, ",")[0]
        knownFields.add(name)
    
    // Find unknown fields
    unknown = []
    for each key in rawMap.keys:
        if key not in knownFields:
            unknown.append(key)
    
    // Recurse into nested objects
    for each field in reflect.TypeOf(targetStruct).Fields:
        if field.Type.Kind == struct and rawMap[field.jsonName] exists:
            nested = detectUnknownFields(rawMap[field.jsonName], reflect.Zero(field.Type))
            unknown.append(nested with path prefix)
    
    return unknown
```

**Levenshtein Distance for "Did You Mean?" Suggestions**:

```
algorithm closestMatch(unknown, knownFields):
    bestMatch = ""
    bestDistance = MaxInt
    
    for each known in knownFields:
        dist = levenshteinDistance(strings.ToLower(unknown), strings.ToLower(known))
        if dist < bestDistance and dist <= 3:  // Only suggest if close enough
            bestDistance = dist
            bestMatch = known
    
    return bestMatch
```

**Environment Variable Override Application**:

```
algorithm applyEnvOverrides(config):
    if env("SONARQUBE_TOKEN") != "":
        config.SonarQube.Token = env("SONARQUBE_TOKEN")
    if env("SONARQUBE_URL") != "":
        config.SonarQube.URL = env("SONARQUBE_URL")
    if env("SONARCLOUD_TOKEN") != "":
        // Apply to single-org token
        config.SonarCloud.Token = env("SONARCLOUD_TOKEN")
        // Also apply to all organizations (CloudVoyager behavior)
        for each org in config.SonarCloud.Organizations:
            org.Token = env("SONARCLOUD_TOKEN")
    if env("SONARCLOUD_URL") != "":
        config.SonarCloud.URL = env("SONARCLOUD_URL")
```

**Validation Error Formatting**:

```
algorithm formatValidationErrors(errors):
    output = "Configuration validation failed with %d error(s):\n"
    
    for i, err in enumerate(errors):
        // err has Namespace (e.g., "UnifiedConfig.Performance.IssueSync.Concurrency")
        // Convert to JSON path (e.g., "performance.issueSync.concurrency")
        jsonPath = namespaceToJSONPath(err.Namespace)
        
        switch err.Tag:
            case "required":
                output += "  %d. %s is required but was not provided\n"
            case "url":
                output += "  %d. %s must be a valid URL (got: %s)\n"
            case "min":
                output += "  %d. %s must be >= %s (got: %s)\n"
            case "max":
                output += "  %d. %s must be <= %s (got: %s)\n"
            case "oneof":
                output += "  %d. %s must be one of [%s] (got: %s)\n"
    
    return output
```

### Validate Command Implementation

```go
// cmd/validate.go
var validateCmd = &cobra.Command{
    Use:   "validate",
    Short: "Validate a configuration file without making any API calls",
    Long:  "Parse and validate a configuration file, reporting all errors and warnings. Exit 0 if valid, exit 1 if invalid.",
    RunE: func(cmd *cobra.Command, args []string) error {
        configPath, _ := cmd.Flags().GetString("config")
        cfg, err := config.LoadAndValidate(configPath)
        if err != nil {
            var validationErr *config.ValidationError
            if errors.As(err, &validationErr) {
                fmt.Fprintln(os.Stderr, validationErr.FormatHuman())
                os.Exit(1)
            }
            return err
        }
        for _, w := range cfg.Warnings {
            fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
        }
        fmt.Println("Configuration is valid.")
        return nil
    },
}
```

### Test Command Implementation

```go
// cmd/test_connection.go
var testCmd = &cobra.Command{
    Use:   "test",
    Short: "Test connectivity to SonarQube Server and SonarQube Cloud",
    Long:  "Validate configuration and test API connectivity to both SQ Server and SC. Reports version, edition, and auth status.",
    RunE: func(cmd *cobra.Command, args []string) error {
        configPath, _ := cmd.Flags().GetString("config")
        cfg, err := config.LoadAndValidate(configPath)
        if err != nil {
            return err
        }
        
        // Test SQ Server
        fmt.Printf("Testing SonarQube Server at %s...\n", cfg.SonarQube.URL)
        sqStatus, err := testSQConnection(cfg.SonarQube)
        if err != nil {
            fmt.Printf("  FAILED: %s\n", err)
        } else {
            fmt.Printf("  OK: version %s, edition %s\n", sqStatus.Version, sqStatus.Edition)
        }
        
        // Test SC
        fmt.Printf("Testing SonarQube Cloud at %s...\n", cfg.SonarCloud.URL)
        scStatus, err := testSCConnection(cfg.SonarCloud)
        if err != nil {
            fmt.Printf("  FAILED: %s\n", err)
        } else {
            fmt.Printf("  OK: authenticated as %s\n", scStatus.User)
        }
        
        if sqStatus == nil || scStatus == nil {
            os.Exit(1)
        }
        return nil
    },
}
```

### Configuration Scopes per Command

| Command | Required Sections | Optional Sections |
|---------|-------------------|-------------------|
| `transfer` | sonarqube (with projectKey), sonarcloud (single-org with projectKey) | transfer, rateLimit, performance |
| `migrate` | sonarqube, sonarcloud (with organizations or single-org) | transfer, migrate, rateLimit, performance |
| `sync-metadata` | sonarqube, sonarcloud (with organizations) | migrate (skip flags), rateLimit, performance |
| `verify` | sonarqube, sonarcloud (with organizations) | performance |
| `validate` | any (validates whatever is present) | all |
| `test` | sonarqube, sonarcloud | none |
| `status` | sonarqube | sonarcloud |

Each command calls `config.LoadAndValidate(path)` with a `CommandScope` parameter that determines which required fields are enforced. For example, `transfer` requires `sonarqube.projectKey` and `sonarcloud.projectKey`, but `migrate` does not.

### Data Flow

1. User invokes any command with `--config <path>` (or legacy positional args).
2. `config.LoadAndValidate(path, scope)` is called.
3. JSON file is read from disk.
4. Unknown field detection runs, collecting warnings.
5. JSON is unmarshalled into `UnifiedConfig` with defaults pre-applied.
6. Environment variable overrides are applied.
7. `go-playground/validator` validates the struct with scope-specific rules.
8. If errors exist, a `ValidationError` with all errors and warnings is returned.
9. If valid, the config is returned and the command proceeds.
10. For `validate` command: print result and exit.
11. For `test` command: proceed to API connectivity checks.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/system/status` | GET | Test SQ Server connectivity (test command) |
| `/api/system/status` | GET | Test SC connectivity (test command) |
| `/api/authentication/validate` | GET | Validate auth token (test command) |
| `/api/navigation/organization` | GET | Validate organization key exists (test command) |

## Acceptance Criteria

- [ ] AC-1: A config file with missing required fields (empty sonarqube.url) produces a validation error with the JSON path `sonarqube.url`.
- [ ] AC-2: A config file with `concurrency: -5` produces a validation error: "performance.issueSync.concurrency must be >= 1 (got: -5)".
- [ ] AC-3: A config file with an unknown field `sonaqube` produces a warning: "Unknown field 'sonaqube' -- did you mean 'sonarqube'?".
- [ ] AC-4: A config file with no optional fields produces a valid config with all defaults applied (mode=incremental, batchSize=100, etc.).
- [ ] AC-5: Setting `SONARQUBE_TOKEN=abc` environment variable overrides the token in the config file.
- [ ] AC-6: Setting `SONARCLOUD_TOKEN=xyz` overrides tokens for all organizations in the config.
- [ ] AC-7: `sonar-migration-tool validate --config good.json` exits with code 0 and prints "Configuration is valid."
- [ ] AC-8: `sonar-migration-tool validate --config bad.json` exits with code 1 and prints all errors.
- [ ] AC-9: `sonar-migration-tool test --config config.json` reports SQ Server version and SC auth status.
- [ ] AC-10: `sonar-migration-tool test --config config.json` exits with code 1 if either connection fails.
- [ ] AC-11: Validation completes in under 100 ms for a config file with 50 organizations.
- [ ] AC-12: The `validate` command works completely offline (no network calls).
- [ ] AC-13: All validation errors are reported at once (not fail-fast on first error).
- [ ] AC-14: Existing `migrate` and `extract` commands use the new validation system without breaking backward compatibility.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Transfer config schema | `src/shared/config/schema/helpers/config-schema.js` |
| Transfer SonarQube sub-schema | `src/shared/config/schema/helpers/transfer-sonarqube-schema.js` |
| Transfer SonarCloud sub-schema | `src/shared/config/schema/helpers/transfer-sonarcloud-schema.js` |
| Transfer options sub-schema | `src/shared/config/schema/helpers/transfer-options-schema.js` |
| Migrate config schema | `src/shared/config/schema-migrate/helpers/migrate-config-schema.js` |
| Migrate options sub-schema | `src/shared/config/schema-migrate/helpers/migrate-options-schema.js` |
| Performance sub-schema | `src/shared/config/schema-shared/helpers/performance-schema.js` |
| Rate limit sub-schema | `src/shared/config/schema-shared/helpers/rate-limit-schema.js` |
| Config loader (transfer) | `src/shared/config/loader/index.js` |
| Config loader (migrate) | `src/shared/config/loader-migrate/index.js` |
| Schema validation | `src/shared/config/loader-migrate/helpers/validate-migrate-schema.js` |
| Default application | `src/shared/config/loader-migrate/helpers/apply-migrate-defaults.js` |
| Env override application | `src/shared/config/loader-migrate/helpers/apply-migrate-env-overrides.js` |
| Error handling | `src/shared/config/loader-migrate/helpers/handle-config-load-error.js` |
| Validate command | `src/commands/validate/` |
| Test command | `src/commands/test-connection/` |
| Desktop config store | `desktop/src/main/config-store.js` |

## Known Limitations

- The `go-playground/validator` library does not support JSON Schema `$ref` or conditional schemas (e.g., "if transfer mode is incremental, then checkpoint must be enabled"). Complex cross-field validations must be implemented as custom validator functions.
- Go's `json.Unmarshal` silently ignores unknown fields by default. The unknown field detection must use a separate parsing pass with `json.Decoder.DisallowUnknownFields()` or a reflection-based field comparison against a generic `map[string]interface{}`. This adds a small overhead to config loading.
- Go's `encoding/json` does NOT honor `default` struct tags. The `default` tags shown in the struct definitions above are for documentation purposes only. Default values must be applied via a separate `applyDefaults()` function after unmarshaling. The function must walk the struct via reflection and set zero-valued fields to their documented defaults before `json.Unmarshal` is called (so that explicit JSON values overwrite the defaults).
- Environment variable overrides for nested fields (e.g., a hypothetical `SONARQUBE_CONCURRENCY`) are not supported in CloudVoyager and are out of scope. Only the four token/URL variables are implemented.
- Backward compatibility with the existing flat config format (used by `extract` and `migrate` commands) requires supporting both the legacy format and the new unified format. The loader must auto-detect the format by checking for the presence of top-level keys like `sonarqube` (unified) vs. `url` (legacy flat).

## Open Questions

- Should the `test` command also verify that the SonarCloud organization exists and is accessible, or is token authentication sufficient?
- Should validation support `--strict` mode that treats warnings (unknown fields) as errors?
- Should the tool support YAML configuration files in addition to JSON, or is JSON sufficient?
- Consider naming the `test` command `test-connection` instead of `test` to avoid confusion with `go test` during development.
- Should the `validate` command output machine-readable JSON errors (in addition to human-readable text) for CI/CD integration?
- Should configuration support profiles/presets (e.g., `--profile large-migration` that sets high concurrency and batch sizes)?
- How should the tool handle the transition period where existing users have legacy flat config files? Should the tool auto-migrate them to the new format?
