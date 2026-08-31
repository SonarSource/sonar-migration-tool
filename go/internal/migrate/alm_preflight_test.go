// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// warnUnboundOrgs exists so an unbound target organization is reported once,
// before any write, instead of once per affected project deep into phase 4.
func TestWarnUnboundOrgs(t *testing.T) {
	cases := []struct {
		name     string
		handler  func(w http.ResponseWriter, r *http.Request)
		wantWarn bool
	}{
		{
			name: "bound organization stays silent",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"almOrganization": map[string]any{
						"key": "Acme", "almUrl": "https://github.com/Acme",
					},
				})
			},
			wantWarn: false,
		},
		{
			name: "empty binding warns",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"almOrganization": map[string]any{}})
			},
			wantWarn: true,
		},
		{
			// SonarQube Cloud answers 500 for an unbound org (issue #505),
			// so an error here must be read as "no binding", not swallowed.
			name: "HTTP 500 is read as unbound",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"errors":[{"msg":"An unexpected error occurred."}]}`))
			},
			wantWarn: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/alm_integration/show_bound_organization", c.handler)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			var buf strings.Builder
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			raw := common.NewRawClient(srv.Client(), srv.URL+"/")

			cfg := MigrateConfig{DefaultOrganization: "acme-prod"}
			warnUnboundOrgs(context.Background(), raw, cfg, true, logger)

			warned := strings.Contains(buf.String(), "not bound to a DevOps platform")
			if warned != c.wantWarn {
				t.Errorf("warned = %v, want %v; log:\n%s", warned, c.wantWarn, buf.String())
			}
			if c.wantWarn && !strings.Contains(buf.String(), "acme-prod") {
				t.Errorf("the warning must name the organization; got:\n%s", buf.String())
			}
		})
	}
}

// With no default organization applied, the org list comes from
// organizations.csv, and SKIPPED / blank rows must not be probed.
func TestMigrateOrgKeysFromCSV(t *testing.T) {
	dir := t.TempDir()
	csv := "sonarqube_org_key,sonarcloud_org_key\n" +
		"srcA,acme-one\n" +
		"srcB,SKIPPED\n" +
		"srcC,\n" +
		"srcD,acme-two\n" +
		"srcE,acme-one\n" // duplicate target
	if err := os.WriteFile(filepath.Join(dir, orgCSVFileName), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := migrateOrgKeys(MigrateConfig{ExportDirectory: dir}, false)
	if err != nil {
		t.Fatalf("migrateOrgKeys: %v", err)
	}
	want := []string{"acme-one", "acme-two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// The default-organization path short-circuits the CSV entirely.
func TestMigrateOrgKeysUsesDefaultOrganization(t *testing.T) {
	got, err := migrateOrgKeys(MigrateConfig{DefaultOrganization: "acme-prod"}, true)
	if err != nil {
		t.Fatalf("migrateOrgKeys: %v", err)
	}
	if len(got) != 1 || got[0] != "acme-prod" {
		t.Errorf("got %v, want [acme-prod]", got)
	}
	if empty, err := migrateOrgKeys(MigrateConfig{}, true); err != nil || len(empty) != 0 {
		t.Errorf("no default organization must yield no probes, got %v (err %v)", empty, err)
	}
}
