---
spec_id: SPEC-023
title: Desktop Application (Electron)
status: draft
priority: P3
epic: "User Experience"
depends_on: []
estimated_effort: XL
cloudvoyager_ref: "desktop/"
---

# SPEC-023: Desktop Application (Electron)
<!-- updated: 2026-05-26_01:00:00 -->

## Overview

CloudVoyager ships with a full Electron desktop application that wraps the CLI binary and provides a guided wizard UI for all migration operations. The application features nine wizard screens, real-time progress tracking with an animated whale mascot, encrypted configuration storage, checkpoint/resume detection, and a comprehensive results browser. This spec defines the path to delivering an equivalent desktop experience for the sonar-migration-tool, with the key difference that the Go tool already has a functional browser-based GUI (Svelte frontend, Go backend with WebSocket communication).

The recommended approach is Option 1: wrap the existing Go binary and browser GUI inside an Electron shell. This leverages the existing Svelte frontend investment, avoids duplicating UI work, and uses Electron primarily for native OS integration (file dialogs, menu bar, auto-update, token encryption at rest, system tray). The Go binary serves the web UI on a local port, and the Electron `BrowserWindow` loads `http://localhost:{port}` instead of a local HTML file. This is architecturally simpler than CloudVoyager's approach (Option 2), which built a vanilla HTML/CSS/JS renderer from scratch and communicated with the CLI binary via IPC.

CloudVoyager's Electron application uses Electron v33 with full security hardening: `contextIsolation: true`, `nodeIntegration: false`, sandbox mode, and a strict Content Security Policy. Configuration is stored via `electron-store` with an encryption key for tokens at rest. The IPC architecture exposes seven modules through `contextBridge`: config, checkpoint, cli, enterprise, dialog, reports, and app. The main process handles window lifecycle, CLI process spawning/killing (including orphan cleanup on restart), and theme detection.

## Problem Statement

While the browser-based GUI serves developers and technical operators well, non-technical stakeholders (project managers, compliance officers) often prefer a native desktop application that feels like a "real" tool rather than a web page opened via a terminal command. A desktop application also solves several practical problems: it can auto-start the Go backend (no terminal needed), persist window state, encrypt tokens at rest using OS-level keychain integration, and provide system tray presence for long-running migrations. Additionally, distribution via `.dmg` (macOS), `.exe` installer (Windows), and `.AppImage` (Linux) is more accessible to enterprise IT departments than distributing a CLI binary.

## User Stories

- **As a** migration operator without CLI experience, **I want to** download and install a desktop application, **so that** I can run migrations without using a terminal.
- **As a** security-conscious admin, **I want** my SonarQube and SonarCloud tokens encrypted at rest, **so that** credentials are not stored in plaintext configuration files on disk.
- **As a** migration operator, **I want** the application to detect interrupted migrations and offer to resume, **so that** I do not lose progress from previous runs.
- **As a** migration operator, **I want** real-time progress tracking with ETA estimation, **so that** I can plan my time during long-running migrations.
- **As a** migration operator, **I want** to browse and open generated report files directly from the application, **so that** I do not need to navigate the filesystem manually.
- **As a** manager, **I want** to view migration history (last 50 runs with timestamps, duration, and status), **so that** I can track progress over multiple migration batches.

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|------------|----------|
| FR-1 | Package the Go binary and Svelte web assets into an Electron application | Must |
| FR-2 | Auto-start the Go backend on application launch and kill it on quit | Must |
| FR-3 | Load the browser GUI in an Electron BrowserWindow via `http://localhost:{port}` | Must |
| FR-4 | Detect a free port dynamically (avoid hardcoded ports that may conflict) | Must |
| FR-5 | Encrypt sensitive tokens at rest using electron-store or OS keychain | Must |
| FR-6 | Detect and kill orphan Go processes from previous crashed sessions on startup | Must |
| FR-7 | Implement single-instance lock to prevent multiple app instances | Must |
| FR-8 | Persist window bounds (position, size) across sessions | Should |
| FR-9 | Implement system theme detection (light/dark) and pass to Svelte frontend | Should |
| FR-10 | Provide native file/folder picker dialogs for configuration and output paths | Should |
| FR-11 | Implement real-time progress parsing from Go CLI log output with ETA estimation | Must |
| FR-12 | Implement checkpoint/resume detection: scan for incomplete migration journals on startup | Must |
| FR-13 | Implement results browser: list and open report files (JSON, MD, TXT, PDF) from run directories | Must |
| FR-14 | Implement migration history: store last 50 runs with command, timestamp, duration, status, and reports directory | Should |
| FR-15 | Build distributable packages: `.dmg` (macOS), `.exe`/NSIS installer (Windows), `.AppImage` (Linux) | Must |
| FR-16 | Implement auto-update via electron-updater with GitHub Releases | Should |
| FR-17 | Provide system tray icon with status indicator during active migrations | Should |
| FR-18 | Implement F6 debug screenshot capture (save to temp directory) | Should |

### Non-Functional Requirements

| ID | Requirement | Target |
|----|------------|--------|
| NFR-1 | Application startup to usable UI must complete in under 3 seconds | < 3s |
| NFR-2 | Packaged application size must not exceed 150 MB (includes Go binary + Electron runtime) | < 150 MB |
| NFR-3 | Memory usage at idle must not exceed 200 MB | < 200 MB |
| NFR-4 | Must run on macOS 12+, Windows 10+, Ubuntu 20.04+ | Cross-platform |
| NFR-5 | Must enforce Electron security best practices: contextIsolation, no nodeIntegration, CSP | Security |
| NFR-6 | Go backend must be health-checked before loading the URL in BrowserWindow | Reliability |

## Technical Design

### Architecture

The application follows a three-layer architecture:

```
┌─────────────────────────────────────────────────┐
│                 Electron Shell                     │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────┐│
│  │ Main Process  │  │  Preload     │  │ Renderer ││
│  │ - Window mgmt │  │ - Bridge API │  │ - Loads  ││
│  │ - Go spawn    │  │ - IPC relay  │  │   Svelte ││
│  │ - Config store│  │              │  │   GUI at ││
│  │ - File dialog │  │              │  │   :port  ││
│  │ - Auto-update │  │              │  │          ││
│  └──────┬───────┘  └──────────────┘  └──────────┘│
│         │                                          │
│         │ spawn/kill                               │
│         ▼                                          │
│  ┌──────────────────┐                              │
│  │ Go Binary         │                              │
│  │ (sonar-migration  │◄─── WebSocket ──── Svelte   │
│  │  -tool gui)       │                    frontend  │
│  └──────────────────┘                              │
└─────────────────────────────────────────────────────┘
```

**Main Process** (`desktop/src/main/main.js`):
- Spawns the Go binary with `gui --port {dynamic_port}` on app ready.
- Waits for the Go HTTP server to become healthy (poll `http://localhost:{port}/health`).
- Creates `BrowserWindow` pointing to `http://localhost:{port}`.
- Handles lifecycle: single-instance lock, orphan cleanup, graceful shutdown.
- Registers IPC handlers for native features (file dialogs, config store, reports browser).

**Preload** (`desktop/src/preload/preload.js`):
- Exposes a `contextBridge` API with seven modules (mirroring CloudVoyager):
  - `config` -- Load/save encrypted configuration.
  - `checkpoint` -- Detect incomplete migration journals.
  - `cli` -- Status of the embedded Go process (not direct CLI spawning, since the Go backend manages its own execution).
  - `dialog` -- Native file/folder picker.
  - `reports` -- List, read, and open report files.
  - `app` -- Version, resources path, default reports directory.
  - `theme` -- System theme detection and change events.

**Renderer**:
- The Svelte frontend is served by the Go backend. No separate build step needed for the renderer.
- The Electron shell adds native OS integration on top of the existing web GUI.

### Go Backend Lifecycle Management

```
algorithm spawnGoBackend():
    port = findFreePort()  // net.Listen(":0") then close
    process = spawn("./resources/cli/sonar-migration-tool", ["gui", "--port", port])
    
    // Health check loop
    for attempt in 1..30:
        try:
            response = HTTP GET http://localhost:{port}/health
            if response.status == 200:
                return { process, port }
        catch:
            sleep(100ms)
    
    process.kill()
    throw "Go backend failed to start within 3 seconds"
```

### Progress Parser (Ported from CloudVoyager)

The progress parser reads log lines from the Go backend's WebSocket stream and computes real-time progress percentages. The algorithm is ported directly from CloudVoyager's `progress-parser.js`:

**Migrate/Sync-Metadata Pipeline (0-100%)**:
```
0-10%   : Setup (connect, extract server data, generate mappings)
10-15%  : Org setup (groups, permissions, quality gates/profiles, templates)
15-95%  : Per-project migration (divided equally among N projects)
          Per project: 0-30% scanner report, 30-70% issue sync, 70-90% hotspot sync, 90-100% config
95-100% : Finalization (reports, portfolios)
```

**Transfer Pipeline (0-100%)**:
```
0-5%    : Connection test + setup
5-45%   : Data extraction (Steps 1-10, linearly distributed)
45-55%  : Preparing upload (build protobuf messages)
55-65%  : Encoding upload
65-95%  : Upload + wait for analysis
95-100% : Completion
```

**Verify Pipeline (0-100%)**:
```
0-5%    : Connect (Step 1)
5-10%   : Fetch projects (Step 2)
10-15%  : Build mappings (Step 3) + org-wide checks
15-93%  : Per-project verification (divided equally among N projects)
93-98%  : Portfolios + summary
```

**ETA Estimation**:
```
algorithm estimateETA(progressHistory):
    if progressHistory.length < 3 or currentPercent < 5:
        return null
    recent = last 5 entries from progressHistory
    pctDiff = recent.last.percent - recent.first.percent
    timeDiff = recent.last.time - recent.first.time
    if pctDiff <= 0 or timeDiff <= 0:
        return null
    msPerPct = timeDiff / pctDiff
    msRemaining = (100 - currentPercent) * msPerPct
    return msRemaining
```

### Checkpoint/Resume Detection

On startup, the main process scans the reports directory for incomplete migration journals:

```
algorithm detectCheckpoint(configType):
    baseDir = loadConfig("reportsDir") or getDefaultReportsDir()
    entries = listDirectories(baseDir).filter(name starts with "run-").sortDescending()
    
    for each entry in entries:
        if configType == "transfer":
            journal = parseTransferJournal(entry.path + "/.checkpoint-journal.json")
        else:
            journal = parseMigrationJournal(entry.path + "/state/migration.journal")
        
        if journal != null and journal.status != "completed":
            return {
                found: true,
                runDir: entry.path,
                startedAt: journal.startedAt,
                status: journal.status,
                progress: journal.computedProgress
            }
    
    return { found: false }
```

### Encrypted Configuration Storage

Configuration is stored using `electron-store` with schema validation and AES encryption:

```javascript
const store = new Store({
    name: 'sonar-migration-tool-config',
    schema: {
        lastCommand: { type: 'string', default: '' },
        transferConfig: { type: 'object', default: { /* ... */ } },
        migrateConfig: { type: 'object', default: { /* ... */ } },
        envVars: { type: 'object', default: {} },
        reportsDir: { type: 'string', default: '' },
        migrationHistory: { type: 'array', default: [] },
        ui: { type: 'object', default: { theme: 'system', windowBounds: { width: 1400, height: 850 } } }
    },
    // SECURITY: This hardcoded key is for development only.
    // Production builds MUST use platform-specific secure storage:
    // macOS Keychain, Windows Credential Manager, Linux Secret Service API.
    encryptionKey: 'sonar-migration-tool-desktop-v1'
});
```

### Build and Distribution

```
desktop/
├── package.json              # Electron + electron-builder deps
├── electron-builder.yml      # Build configuration for all platforms
├── scripts/
│   └── prepare-cli.js        # Copy Go binary into resources/cli/
├── resources/
│   └── cli/                  # Go binary (platform-specific, copied at build time)
├── src/
│   ├── main/
│   │   ├── main.js           # Entry point
│   │   ├── cli-runner.js     # Go process spawn/kill
│   │   ├── config-store.js   # Encrypted config persistence
│   │   └── ipc-handlers.js   # IPC handler registration
│   ├── preload/
│   │   └── preload.js        # contextBridge API
│   └── renderer/
│       └── index.html        # Minimal shell that loads localhost:{port}
```

Build pipeline:
1. `go build` the CLI for target platform (GOOS/GOARCH).
2. `node scripts/prepare-cli.js` copies the binary into `resources/cli/`.
3. `electron-builder` packages everything into platform-specific installers.

### Data Flow

1. User launches the desktop application (double-click `.app` / `.exe`).
2. Electron main process starts, acquires single-instance lock.
3. Main process kills any orphan Go processes from previous sessions.
4. Main process finds a free port and spawns the Go binary with `gui --port {port}`.
5. Main process polls `http://localhost:{port}/health` until the Go server is ready.
6. Main process creates BrowserWindow and loads `http://localhost:{port}`.
7. Svelte frontend renders the wizard UI, communicating with Go backend via WebSocket.
8. For native operations (file dialogs, encrypted config), the renderer calls `window.sonarMigrationTool.*` which routes through preload/IPC to the main process.
9. During migration execution, the Go backend streams log lines via WebSocket. The Svelte frontend can optionally forward these to the Electron main process for native progress indicator (taskbar/dock badge).
10. On migration completion, results are available in the Svelte UI and also browsable via the Electron reports IPC module.
11. On app quit, main process sends SIGTERM to the Go process and waits up to 5 seconds before SIGKILL.

### API Dependencies

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `http://localhost:{port}/health` | GET | Health check for Go backend readiness |
| `http://localhost:{port}/` | GET | Serve Svelte frontend assets |
| `ws://localhost:{port}/ws` | WebSocket | Real-time communication between frontend and backend |

## Acceptance Criteria

- [ ] AC-1: Application launches on macOS, Windows, and Linux without errors.
- [ ] AC-2: Go backend starts automatically on launch and is killed cleanly on quit.
- [ ] AC-3: Svelte GUI renders correctly inside the Electron BrowserWindow.
- [ ] AC-4: File picker dialog opens native OS dialog and returns selected path to the Svelte frontend.
- [ ] AC-5: Tokens stored via the desktop app are encrypted at rest (not visible in plaintext in config files).
- [ ] AC-6: Single-instance lock prevents launching a second copy of the application.
- [ ] AC-7: Window position and size are restored on next launch.
- [ ] AC-8: Progress bar shows real-time percentage with ETA during a migration run.
- [ ] AC-9: Checkpoint detection correctly identifies incomplete migration journals and prompts for resume.
- [ ] AC-10: Results browser lists all report files from the latest run and opens them in the OS default viewer.
- [ ] AC-11: Migration history records the last 50 runs and displays them in the status screen.
- [ ] AC-12: Application startup (from launch to usable UI) completes in under 3 seconds.
- [ ] AC-13: Orphan Go processes from crashed sessions are detected and killed on startup.
- [ ] AC-14: System theme changes (light/dark) are reflected in the UI without restart.

## CloudVoyager Reference

| Area | Path |
|------|------|
| Main process entry point | `desktop/src/main/main.js` |
| CLI runner (spawn/kill) | `desktop/src/main/cli-runner.js` |
| Config store (encrypted) | `desktop/src/main/config-store.js` |
| IPC handlers | `desktop/src/main/ipc-handlers.js` |
| Preload bridge | `desktop/src/preload/preload.js` |
| Renderer entry | `desktop/src/renderer/index.html` |
| App controller | `desktop/src/renderer/js/app.js` |
| Progress parser | `desktop/src/renderer/js/components/progress-parser.js` |
| Whale animator | `desktop/src/renderer/js/components/whale-animator.js` |
| Wizard navigation | `desktop/src/renderer/js/components/wizard-nav.js` |
| Log viewer | `desktop/src/renderer/js/components/log-viewer.js` |
| Config form | `desktop/src/renderer/js/components/config-form.js` |
| Welcome screen | `desktop/src/renderer/js/screens/welcome.js` |
| Transfer config | `desktop/src/renderer/js/screens/transfer-config.js` |
| Migrate config | `desktop/src/renderer/js/screens/migrate-config.js` |
| Verify config | `desktop/src/renderer/js/screens/verify-config.js` |
| Execution screen | `desktop/src/renderer/js/screens/execution.js` |
| Results screen | `desktop/src/renderer/js/screens/results.js` |
| Status screen | `desktop/src/renderer/js/screens/status.js` |
| Connection test | `desktop/src/renderer/js/screens/connection-test.js` |
| Sync metadata config | `desktop/src/renderer/js/screens/sync-metadata-config.js` |
| Migration graph | `desktop/src/renderer/js/components/migration-graph.js` |
| Build config | `desktop/electron-builder.yml` |
| CLI preparation | `desktop/scripts/prepare-cli.js` |

## Known Limitations

- Electron adds approximately 80-120 MB to the distributable size due to the Chromium runtime. This is unavoidable with Electron.
- The Go binary must be compiled separately for each target platform (darwin/amd64, darwin/arm64, linux/amd64, win32/amd64). Cross-compilation is straightforward with `GOOS`/`GOARCH` but requires CI matrix builds.
- On macOS, the application must be code-signed and notarized for distribution outside the Mac App Store. This requires an Apple Developer account and adds CI complexity.
- **Security Warning**: The `electron-store` encryption key shown above (`'sonar-migration-tool-desktop-v1'`) is hardcoded in source code. Token encryption keys MUST NOT be hardcoded in source code for production releases. Use platform-specific secure storage: macOS Keychain, Windows Credential Manager, Linux Secret Service API (via `libsecret`). Alternatively, use a key derived from the user's OS account. The hardcoded key is acceptable only for development/prototype builds.
- WebSocket communication between the Svelte frontend and Go backend uses `localhost` only. The Electron CSP must allow `ws://localhost:*` connections.

## Open Questions

- Should we adopt Electron Forge or continue with electron-builder for the build system? Electron Forge is the officially recommended toolchain as of 2025.
- Should the whale animator mascot from CloudVoyager be ported, or should a new brand-aligned animation be created?
- Should auto-update check for updates on every launch, or only when explicitly triggered by the user?
- Is the `contextBridge` API needed at all in Option 1, or can all functionality be handled by the Go backend's WebSocket? The bridge is only needed for truly native operations (file dialogs, OS keychain, system theme).
- Should we support Apple Silicon natively (arm64) or rely on Rosetta 2 translation?
