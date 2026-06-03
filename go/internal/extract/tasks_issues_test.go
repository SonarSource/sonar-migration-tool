package extract

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// Issue #278: /api/hotspots/search uses parameter "projectKey" on SQS 9.9
// and "project" on 10.x+. safeHotspotsTask must pick the right name based
// on the detected server version.
func TestSafeHotspotsTask_VersionedProjectParam(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		wantParam string
		otherName string // server rejects this name to lock in the version dispatch
	}{
		{name: "SQS 9.9 requires projectKey", version: "9.9", wantParam: "projectKey", otherName: "project"},
		{name: "SQS 9.9.3 (with patch) still requires projectKey", version: "9.9.3.12345", wantParam: "projectKey", otherName: "project"},
		{name: "SQS 10.0 requires project", version: "10.0", wantParam: "project", otherName: "projectKey"},
		{name: "SQS 10.7 requires project", version: "10.7", wantParam: "project", otherName: "projectKey"},
		{name: "Year-versioned 2026.4 uses project", version: "2026.4.0.123541", wantParam: "project", otherName: "projectKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured string
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/hotspots/search", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get(tc.otherName) != "" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"errors":[{"msg":"wrong param"}]}`))
					return
				}
				captured = r.URL.Query().Get(tc.wantParam)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"paging":   map[string]any{"total": 0},
					"hotspots": []map[string]any{},
				})
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			store := NewDataStore(dir)
			ww, err := store.Writer("getProjects")
			if err != nil {
				t.Fatal(err)
			}
			if err := ww.WriteOne(json.RawMessage(`{"key":"my-project"}`)); err != nil {
				t.Fatal(err)
			}

			e := &Executor{
				Raw:       NewRawClient(srv.Client(), srv.URL+"/"),
				Store:     store,
				ServerURL: srv.URL,
				Version:   common.ParseVersion(tc.version),
				Sem:       make(chan struct{}, 2),
				Logger:    slog.New(slog.NewTextHandler(&discardWriter{}, nil)),
			}
			if err := safeHotspotsTask()(context.Background(), e); err != nil {
				t.Fatalf("safeHotspotsTask: %v", err)
			}
			if captured != "my-project" {
				t.Errorf("%s=my-project: got %q", tc.wantParam, captured)
			}
		})
	}
}

func TestHotspotsProjectParam(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"9.9", "projectKey"},
		{"9.9.3", "projectKey"},
		{"9.9.99999", "projectKey"},
		{"10.0", "project"},
		{"10.0.1", "project"},
		{"10.7", "project"},
		{"2025.1", "project"},
		{"2026.4.0.123541", "project"},
	}
	for _, tc := range cases {
		got := hotspotsProjectParam(common.ParseVersion(tc.version))
		if got != tc.want {
			t.Errorf("version %q: got %q, want %q", tc.version, got, tc.want)
		}
	}
}
