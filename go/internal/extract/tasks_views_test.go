// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package extract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

// runGetPortfolios runs the getPortfolios task against a mock
// api/views/search holding three portfolios and returns the written keys.
func runGetPortfolios(t *testing.T, filter *common.PortfolioFilter) []string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/views/search", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": 1, "pageSize": 100, "total": 3},
			"components": []map[string]any{
				{"key": "team-a-pf", "name": "Team A"},
				{"key": "team-b-pf", "name": "Team B"},
				{"key": "legacy", "name": "Legacy Apps"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	store := NewDataStore(t.TempDir())
	e := &Executor{
		Raw: NewRawClient(srv.Client(), srv.URL+"/"), Store: store, ServerURL: srv.URL + "/",
		Sem: make(chan struct{}, 5), PortfolioFilter: filter,
	}
	if err := getPortfoliosTask()(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	items, err := store.ReadAll("getPortfolios")
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(items))
	for _, it := range items {
		keys = append(keys, extractField(it, "key"))
	}
	return keys
}

func TestGetPortfolios_Filtering(t *testing.T) {
	newFilter := func(key, name string) *common.PortfolioFilter {
		f, err := common.NewPortfolioFilter(key, name)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	tests := []struct {
		name   string
		filter *common.PortfolioFilter
		want   string
	}{
		{"nil filter writes everything", nil, "team-a-pf,team-b-pf,legacy"},
		{"inactive filter writes everything", newFilter("", ""), "team-a-pf,team-b-pf,legacy"},
		{"key regexp", newFilter("team-.*", ""), "team-a-pf,team-b-pf"},
		{"key regexp is anchored", newFilter("team", ""), ""},
		{"plain key matches only itself", newFilter("legacy", ""), "legacy"},
		{"name regexp", newFilter("", "Team .*"), "team-a-pf,team-b-pf"},
		{"name regexp matching nothing", newFilter("", "nope"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(runGetPortfolios(t, tt.filter), ","); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunExtractPortfolioRegexpConflict(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	cfg := ExtractConfig{
		URL: srv.URL, Token: testToken, ExportDirectory: t.TempDir(),
		PortfolioKeyRegexp: "a", PortfolioNameRegexp: "b",
	}
	_, err := RunExtract(context.Background(), cfg)
	if !errors.Is(err, common.ErrPortfolioRegexpConflict) {
		t.Fatalf("expected ErrPortfolioRegexpConflict, got %v", err)
	}
}

func TestRunExtractInvalidPortfolioRegexp(t *testing.T) {
	srv := newMockServer()
	defer srv.Close()

	for _, cfg := range []ExtractConfig{{PortfolioKeyRegexp: "("}, {PortfolioNameRegexp: "("}} {
		cfg.URL, cfg.Token, cfg.ExportDirectory = srv.URL, testToken, t.TempDir()
		_, err := RunExtract(context.Background(), cfg)
		if err == nil || !strings.Contains(err.Error(), "invalid portfolio") {
			t.Errorf("expected an 'invalid portfolio ... regexp' error, got %v", err)
		}
	}
}

func writePortfolioConfig(t *testing.T, body string) ExtractConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadExtractConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// #666: the portfolio regexps round-trip in every config shape, and in the
// unified shape the source block wins as a pair over the top-level one.
func TestLoadExtractConfigFilePortfolioRegexp(t *testing.T) {
	tests := []struct {
		name, body, wantKey, wantName string
	}{
		{"flat", `{"url":"u","token":"t","portfolio_key_regexp":"k.*"}`, "k.*", ""},
		{"flat name", `{"url":"u","token":"t","portfolio_name_regexp":"n.*"}`, "", "n.*"},
		{"legacy sonarqube", `{"sonarqube":{"url":"u","token":"t"},"portfolio_key_regexp":"k.*"}`, "k.*", ""},
		{"sectioned nested", `{"extract":{"url":"u","token":"t","portfolio_key_regexp":"k.*"}}`, "k.*", ""},
		{"sectioned outer wins", `{"portfolio_name_regexp":"n.*","extract":{"url":"u","token":"t","portfolio_key_regexp":"k.*"}}`, "", "n.*"},
		{"unified top-level fallback", `{"portfolio_key_regexp":"top.*","source":{"url":"u","token":"t"}}`, "top.*", ""},
		{"unified source wins", `{"portfolio_key_regexp":"top.*","source":{"url":"u","token":"t","portfolio_key_regexp":"src.*"}}`, "src.*", ""},
		{"unified source pair replaces top-level pair", `{"portfolio_name_regexp":"top.*","source":{"url":"u","token":"t","portfolio_key_regexp":"src.*"}}`, "src.*", ""},
		{"unset", `{"url":"u","token":"t"}`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := writePortfolioConfig(t, tt.body)
			if cfg.PortfolioKeyRegexp != tt.wantKey || cfg.PortfolioNameRegexp != tt.wantName {
				t.Errorf("got key=%q name=%q, want key=%q name=%q",
					cfg.PortfolioKeyRegexp, cfg.PortfolioNameRegexp, tt.wantKey, tt.wantName)
			}
		})
	}
}
