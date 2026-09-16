---
spec_id: SPEC-015
title: Auto-Tuning & Performance Optimization
status: draft
priority: P1
epic: "Performance"
depends_on: [SPEC-014, SPEC-016]
estimated_effort: M
cloudvoyager_ref: "src/shared/config/auto-tune.js"
---

# SPEC-015: Auto-Tuning & Performance Optimization
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

Migration performance is critically dependent on concurrency settings. Too few concurrent operations and a migration that could complete in 30 minutes takes 8 hours. Too many concurrent operations and the tool overwhelms the SonarQube Server or Cloud instance with API requests, triggering rate limits, causing timeouts, and potentially destabilizing the target platform for other users. CloudVoyager addresses this with an `--auto-tune` flag that detects system resources (CPU cores, available memory) and computes optimal concurrency settings for each phase of the migration pipeline.

The sonar-migration-tool already has a `--concurrency` flag for general parallelism control. This spec extends that with intelligent auto-tuning that sets phase-specific concurrency levels based on the host machine's capabilities. The auto-tune system detects available CPU cores and system memory, applies CloudVoyager's empirically-derived formulas to compute concurrency levels for extraction, sync, and migration phases, and configures the tool accordingly. These computed values serve as defaults that can be overridden by config file settings or CLI flags, following a clear precedence hierarchy.

Go's runtime is fundamentally different from Node.js: goroutines are much cheaper than worker threads (8 KB vs 1-2 MB per isolate), the scheduler is preemptive and work-stealing, and GOMAXPROCS already handles CPU-bound work distribution. This means the auto-tune formulas from CloudVoyager need adaptation for Go — specifically, I/O-bound concurrency can be set higher relative to CPU cores, and there is no need for the Node.js-specific heap management workarounds that CloudVoyager implements.

## Problem Statement

Migration operators currently must manually tune the `--concurrency` flag through trial and error. Setting it too low wastes time on machines with ample resources. Setting it too high causes 429 rate limit responses, connection timeouts, and potential service disruption. There is no built-in intelligence to detect the machine's capabilities and set appropriate defaults. Furthermore, different migration phases have different concurrency characteristics: extraction is I/O-bound and benefits from high concurrency, while report building is CPU-bound and benefits from GOMAXPROCS-level parallelism. A single `--concurrency` flag cannot optimize for both.

## User Stories

- **As a** migration operator running on a 16-core server with 64 GB RAM, **I want to** use `--auto-tune` and have the tool automatically configure high-concurrency extraction and sync, **so that** I maximize throughput without manual tuning.
- **As a** migration operator running on a 2-core laptop with 8 GB RAM, **I want to** use `--auto-tune` and have the tool configure conservative concurrency, **so that** the migration completes without overwhelming my machine or the target server.
- **As a** migration operator, **I want to** override specific auto-tuned values via CLI flags or config file, **so that** I can fine-tune for my environment (e.g., a rate-limited SC instance).
- **As a** migration operator, **I want to** see the auto-tuned values logged at startup, **so that** I can verify they are appropriate for my environment before the migration begins.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Implement `--auto-tune` CLI flag that enables automatic concurrency configuration | Must |
| FR-2 | Detect available CPU cores via `runtime.NumCPU()` or `runtime.GOMAXPROCS(0)` | Must |
| FR-3 | Detect total system memory (platform-specific: sysctl on macOS, /proc/meminfo on Linux, Windows API on Windows) | Must |
| FR-4 | Compute `sourceExtraction` concurrency = CPU cores x 2 | Must |
| FR-5 | Compute `hotspotExtraction` concurrency = CPU cores x 2 | Must |
| FR-6 | Compute `issueSync` concurrency = CPU cores | Must |
| FR-7 | Compute `hotspotSync` concurrency = min(max(CPU cores / 2, 3), 5) | Must |
| FR-8 | Compute `projectMigration` concurrency = max(1, CPU cores / 3) | Must |
| FR-9 | Apply memory limit = min(totalRAM x 0.75, 16 GB) as a soft cap for in-memory data structures | Must |
| FR-10 | Configuration precedence: CLI flags > config file > auto-tune > defaults. When auto-tune is active AND explicit concurrency values are set in config or CLI flags, explicit values take precedence. | Must |
| FR-11 | Log all auto-tuned values at INFO level at startup | Must |
| FR-12 | Log which values were overridden by config file or CLI flags | Should |
| ~~FR-13~~ | ~~Set `GOMAXPROCS` to `runtime.NumCPU()` if not already set by the environment~~ | ~~Removed~~ |

> **FR-13 removed:** GOMAXPROCS defaults to `runtime.NumCPU()` since Go 1.5. Explicitly setting it is a no-op at best, and harmful in container environments at worst (where cgroup-aware libraries like `go.uber.org/automaxprocs` may have already set it correctly).
| FR-14 | Detect container environments (cgroup CPU limits, memory limits) and use those instead of host values | Should |
| FR-15 | Store computed values in a `TuningProfile` struct accessible throughout the application | Must |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Auto-tune computation time | < 100ms (system calls only, no benchmarking) |
| NFR-2 | Cross-platform support | macOS (darwin), Linux, Windows |
| NFR-3 | Container awareness | Correctly detect cgroup v1 and v2 CPU/memory limits |
| NFR-4 | Zero external dependencies | Use only Go standard library and `golang.org/x/sys` if needed |
| NFR-5 | Graceful degradation | If any detection fails, use conservative defaults (4 cores, 8 GB) |

## Technical Design

### Architecture

```
go/internal/tuning/
├── tuning.go            # TuningProfile struct, ComputeProfile() function
├── tuning_test.go       # Profile computation tests with various core/memory combinations
├── detect.go            # CPU and memory detection (dispatches to platform-specific)
├── detect_darwin.go     # macOS: sysctl hw.memsize
├── detect_linux.go      # Linux: /proc/meminfo, cgroup detection
├── detect_windows.go    # Windows: GlobalMemoryStatusEx
├── detect_test.go       # Detection tests
├── cgroup.go            # cgroup v1/v2 CPU and memory limit detection
├── cgroup_test.go       # cgroup tests with mock filesystems
└── override.go          # Precedence resolution: auto-tune, config, CLI
```

### Key Algorithms

#### System Resource Detection

```go
package tuning

import "runtime"

// SystemResources holds detected hardware capabilities.
type SystemResources struct {
    CPUCores    int     // Logical CPU cores (GOMAXPROCS-aware)
    TotalMemMB  int64   // Total system memory in megabytes
    IsContainer bool    // Running inside a container (cgroup detected)
}

// DetectResources probes the system for available CPU and memory.
// In container environments, uses cgroup limits instead of host values.
func DetectResources() (SystemResources, error) {
    cores := runtime.NumCPU()
    mem, err := detectTotalMemory()  // platform-specific
    if err != nil {
        // Graceful degradation: assume 8 GB
        log.Warn("Failed to detect system memory: %v, assuming 8192 MB", err)
        mem = 8192
    }
    
    isContainer := false
    
    // Check for cgroup limits (Linux only)
    if cgroupCores, cgroupMem, ok := detectCgroupLimits(); ok {
        isContainer = true
        if cgroupCores > 0 && cgroupCores < cores {
            cores = cgroupCores
        }
        if cgroupMem > 0 && cgroupMem < mem {
            mem = cgroupMem
        }
    }
    
    return SystemResources{
        CPUCores:    cores,
        TotalMemMB:  mem,
        IsContainer: isContainer,
    }, nil
}
```

#### Concurrency Formula Computation

```
FUNCTION ComputeProfile(resources SystemResources) TuningProfile:
    cores = resources.CPUCores
    memMB = resources.TotalMemMB
    
    // Memory limit: 75% of total, capped at 16 GB
    memLimitMB = min(memMB * 75 / 100, 16384)
    
    profile = TuningProfile{
        // I/O-bound extraction phases: 2x cores (waiting on network, not CPU)
        SourceExtraction:   cores * 2,
        HotspotExtraction:  cores * 2,
        
        // Sync phases: moderate concurrency (mix of I/O and CPU for diff computation)
        IssueSync:          cores,
        HotspotSync:        clamp(cores / 2, 3, 5),
        
        // Project-level migration: low concurrency (each project is heavy)
        ProjectMigration:   max(1, cores / 3),
        
        // Memory budget for in-memory data structures
        MemoryLimitMB:      memLimitMB,
        
        // Metadata
        DetectedCores:      cores,
        DetectedMemMB:      memMB,
        IsContainer:        resources.IsContainer,
    }
    
    RETURN profile

FUNCTION clamp(value, low, high int) int:
    IF value < low: RETURN low
    IF value > high: RETURN high
    RETURN value
```

#### Configuration Precedence Resolution

```
FUNCTION ResolveProfile(autoProfile TuningProfile, configFile ConfigFile, cliFlags CLIFlags) TuningProfile:
    resolved = autoProfile   // Start with auto-tuned values
    overrides = []string{}
    
    // Config file overrides auto-tune
    IF configFile.SourceExtraction > 0:
        resolved.SourceExtraction = configFile.SourceExtraction
        overrides = append(overrides, "sourceExtraction (config file)")
    IF configFile.IssueSync > 0:
        resolved.IssueSync = configFile.IssueSync
        overrides = append(overrides, "issueSync (config file)")
    // ... repeat for all fields
    
    // CLI flags override both auto-tune and config file
    IF cliFlags.Concurrency > 0:
        // Global --concurrency overrides all phase-specific values
        resolved.SourceExtraction = cliFlags.Concurrency
        resolved.HotspotExtraction = cliFlags.Concurrency
        resolved.IssueSync = cliFlags.Concurrency
        resolved.ProjectMigration = cliFlags.Concurrency
        overrides = append(overrides, "all (--concurrency CLI flag)")
    IF cliFlags.SyncWorkers > 0:
        resolved.IssueSync = cliFlags.SyncWorkers
        overrides = append(overrides, "issueSync (--sync-workers CLI flag)")
    
    // Log the resolved profile
    log.Info("Auto-tune profile: %d cores detected, %d MB memory", 
        resolved.DetectedCores, resolved.DetectedMemMB)
    log.Info("  sourceExtraction=%d, hotspotExtraction=%d, issueSync=%d, hotspotSync=%d, projectMigration=%d",
        resolved.SourceExtraction, resolved.HotspotExtraction, 
        resolved.IssueSync, resolved.HotspotSync, resolved.ProjectMigration)
    IF len(overrides) > 0:
        log.Info("  Overrides applied: %s", strings.Join(overrides, ", "))
    
    RETURN resolved
```

#### Container Detection (Linux cgroup v2)

```
FUNCTION detectCgroupLimits() (cores int, memMB int64, ok bool):
    // cgroup v2: /sys/fs/cgroup/cpu.max and /sys/fs/cgroup/memory.max
    cpuMax, err = readFile("/sys/fs/cgroup/cpu.max")
    IF err == nil AND cpuMax != "max":
        // Format: "quota period" e.g., "200000 100000" = 2 cores
        parts = split(cpuMax, " ")
        quota = parseInt(parts[0])
        period = parseInt(parts[1])
        cores = quota / period   // integer division, rounds down
    
    memMax, err = readFile("/sys/fs/cgroup/memory.max")
    IF err == nil AND memMax != "max":
        memBytes = parseInt(memMax)
        memMB = memBytes / (1024 * 1024)
    
    // cgroup v1 fallback: /sys/fs/cgroup/cpu/cpu.cfs_quota_us, etc.
    IF cores == 0:
        cores, _ = detectCgroupV1CPU()
    IF memMB == 0:
        memMB, _ = detectCgroupV1Memory()
    
    ok = cores > 0 || memMB > 0
    RETURN
```

### Data Flow

1. **CLI Parsing**: `--auto-tune` flag is detected. If not set, skip auto-tuning entirely.
2. **Resource Detection**: Call `DetectResources()` to probe CPU cores and memory. Handle container environments.
3. **Profile Computation**: Call `ComputeProfile()` with detected resources to produce `TuningProfile`.
4. **Precedence Resolution**: Call `ResolveProfile()` to merge auto-tuned values with config file and CLI overrides.
5. **Profile Injection**: The resolved `TuningProfile` is stored in the application context and accessed by each migration phase to configure its concurrency.
6. **Phase Execution**: Each phase (extraction, sync, migration) reads its specific concurrency value from the profile and configures its worker pool accordingly.

### API Dependencies

This spec has no external API dependencies. All detection is performed via local system calls and filesystem reads.

| Resource | Method | Platform |
|----------|--------|----------|
| `runtime.NumCPU()` | Go stdlib | All |
| `runtime.GOMAXPROCS(0)` | Go stdlib | All |
| `sysctl hw.memsize` | syscall | macOS |
| `/proc/meminfo` | File read | Linux |
| `GlobalMemoryStatusEx` | Windows API | Windows |
| `/sys/fs/cgroup/cpu.max` | File read | Linux (cgroup v2) |
| `/sys/fs/cgroup/memory.max` | File read | Linux (cgroup v2) |
| `/sys/fs/cgroup/cpu/cpu.cfs_quota_us` | File read | Linux (cgroup v1) |
| `/sys/fs/cgroup/memory/memory.limit_in_bytes` | File read | Linux (cgroup v1) |

## Acceptance Criteria

- [ ] AC-1: `--auto-tune` flag is recognized and triggers automatic concurrency configuration
- [ ] AC-2: CPU core count is correctly detected on macOS, Linux, and Windows
- [ ] AC-3: Total system memory is correctly detected on macOS, Linux, and Windows
- [ ] AC-4: On a 4-core machine: sourceExtraction=8, hotspotExtraction=8, issueSync=4, hotspotSync=3, projectMigration=1
- [ ] AC-5: On a 16-core machine: sourceExtraction=32, hotspotExtraction=32, issueSync=16, hotspotSync=5, projectMigration=5
- [ ] AC-6: On a 2-core machine: sourceExtraction=4, hotspotExtraction=4, issueSync=2, hotspotSync=3, projectMigration=1
- [ ] AC-7: Memory limit is 75% of total RAM, capped at 16 GB
- [ ] AC-8: Config file values override auto-tuned values
- [ ] AC-9: CLI flag values override both auto-tuned and config file values
- [ ] AC-10: All auto-tuned values and overrides are logged at INFO level at startup
- [ ] AC-11: Container cgroup v2 CPU limits are detected and used instead of host CPU count
- [ ] AC-12: Container cgroup v2 memory limits are detected and used instead of host memory
- [ ] AC-13: When resource detection fails, conservative defaults are used (4 cores, 8192 MB)
- [ ] AC-14: Without `--auto-tune`, the tool behaves exactly as before (backward compatible)
- [ ] ~~AC-15: Removed (GOMAXPROCS defaults to NumCPU() since Go 1.5; explicit setting is unnecessary)~~

## CloudVoyager Reference

| Area | Path |
|------|------|
| Auto-tune implementation | `src/shared/config/auto-tune.js` |
| CPU detection | `src/shared/config/auto-tune.js#detectCPU` |
| Memory detection | `src/shared/config/auto-tune.js#detectMemory` |
| Concurrency formulas | `src/shared/config/auto-tune.js#computeConcurrency` |
| Config hierarchy | `src/shared/config/config-resolver.js` |

## Known Limitations

- Auto-tuning is based on the host machine's resources, not the SonarQube Server/Cloud instance's capacity. A powerful machine running against a small SQ Server could still overwhelm it. For target-aware tuning, consider integrating with SPEC-016 (rate limiting) to dynamically reduce concurrency based on 429 responses.
- Windows support for memory detection requires CGo or the `golang.org/x/sys/windows` package. If neither is available, the tool falls back to the 8 GB default. This is acceptable since most production migrations run on Linux or macOS.
- cgroup v1 detection is best-effort. Some container runtimes use non-standard cgroup mount points. The tool checks the standard paths and falls back to host values if the cgroup filesystem is not found.
- The concurrency formulas are directly ported from CloudVoyager and tuned for Node.js worker threads. Go's goroutines are significantly cheaper, so higher multipliers might be optimal. Empirical benchmarking against real SQ instances should inform future formula adjustments.
- `GOMAXPROCS` setting only affects CPU-bound goroutines. Since most migration work is I/O-bound (waiting for API responses), the value of `GOMAXPROCS` has minimal impact on overall throughput. It primarily affects protobuf encoding speed during the report building phase. Note: GOMAXPROCS defaults to `runtime.NumCPU()` since Go 1.5, so explicit setting has been removed from this spec (see FR-13 removal).

## Sonar Documentation Reference

For full Sonar product documentation, see: https://docs.sonarsource.com/llms.txt

## Open Questions

- Q1: Should auto-tune be the default behavior (no flag needed), with an `--no-auto-tune` flag to disable it? CloudVoyager requires the flag, but a "just works" default would improve UX.
- Q2: Should the tool perform a lightweight benchmark at startup (e.g., 5 test API calls) to measure actual network latency to the SQ Server and adjust concurrency accordingly?
- Q3: Should the `TuningProfile` be written to the migration state file so that subsequent runs use the same profile unless resources change?
- Q4: Should there be a `--dry-run-tune` flag that prints the computed profile without running the migration, so operators can review before committing?
- Q5: Are the CloudVoyager formulas optimal for Go, or should we run benchmarks to derive Go-specific multipliers? The cost of a goroutine vs a Node.js worker thread may warrant higher concurrency values.
