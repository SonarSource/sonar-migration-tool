package migrate

// Diagnostic-only: reproduces the EXACT scanner-report zip the tool would submit
// for the main branch, fully offline, by calling the production buildBranchReport
// with already-extracted data. Gated on SMT_DUMP_OUT so it never runs in CI.
//
//   SMT_DUMP_OUT=/tmp/our-report/our-scanner-report.zip \
//   SMT_EXPORT_DIR=/abs/path/to/migration-files \
//   go test ./internal/migrate -run TestDumpOurReport -count=1 -v
//
// Delete this file after the investigation.

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/structure"
)

func TestDumpOurReport(t *testing.T) {
	out := os.Getenv("SMT_DUMP_OUT")
	if out == "" {
		t.Skip("set SMT_DUMP_OUT to run the report dump")
	}
	exportDir := os.Getenv("SMT_EXPORT_DIR")
	if exportDir == "" {
		t.Fatal("set SMT_EXPORT_DIR to the migration-files root")
	}

	mapping, err := structure.GetUniqueExtracts(exportDir)
	if err != nil {
		t.Fatalf("GetUniqueExtracts: %v", err)
	}
	t.Logf("extract mapping: %#v", mapping)

	e := &Executor{
		ExportDir: exportDir,
		Mapping:   mapping,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		// Cloud / RawAPI deliberately nil: main-branch path is offline
		// (buildSCProfileMap is nil-safe; no create-analysis handshake for main).
	}

	const (
		serverURL = "http://localhost:9000/"
		serverKey = "okorach-oss_sonar-tools-test"
		cloudKey  = "open-digital-society-1_okorach-oss_sonar-tools-test"
		orgKey    = "open-digital-society-1"
	)

	branches := collectBranchInfo(e, serverURL, serverKey)
	t.Logf("collected %d branches", len(branches))
	var mainName string
	for _, b := range branches {
		t.Logf("  branch %q isMain=%v", b.Name, b.IsMain)
		if b.IsMain {
			mainName = b.Name
		}
	}
	if mainName == "" {
		mainName = "main"
	}

	input := importBranchInput{
		CloudKey:     cloudKey,
		OrgKey:       orgKey,
		ServerURL:    serverURL,
		ServerKey:    serverKey,
		Branch:       mainName,
		TargetBranch: mainName,
		IsMain:       true,
	}

	zipBytes, _, skip, err := buildBranchReport(context.Background(), e, input, mainName)
	if err != nil {
		t.Fatalf("buildBranchReport: %v", err)
	}
	if skip != nil {
		t.Fatalf("branch was skipped: status=%q err=%q", skip.Status, skip.Error)
	}
	if len(zipBytes) == 0 {
		t.Fatal("empty report zip")
	}

	if err := os.WriteFile(out, zipBytes, 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}
	t.Logf("wrote %d bytes to %s", len(zipBytes), out)
}
