// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sonar-solutions/sonar-migration-tool/internal/common"
)

func newLookupServer(handlers map[string]string) *httptest.Server {
	mux := http.NewServeMux()
	for path, body := range handlers {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})
	}
	return httptest.NewServer(mux)
}

func TestLookupExistingProject(t *testing.T) {
	got := lookupExistingProject("my-org", "my-proj")
	if got != "my-org_my-proj" {
		t.Errorf("got %q, want %q", got, "my-org_my-proj")
	}
}

func TestLookupExistingProfile(t *testing.T) {
	srv := newLookupServer(map[string]string{
		"/api/qualityprofiles/search": `{"profiles":[{"key":"prof-1","name":"MyProfile","language":"java"}]}`,
	})
	defer srv.Close()
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	key, err := lookupExistingProfile(context.Background(), raw, "MyProfile", "java", "org")
	if err != nil {
		t.Fatal(err)
	}
	if key != "prof-1" {
		t.Errorf("got %q, want %q", key, "prof-1")
	}
}

func TestLookupExistingProfileNotFound(t *testing.T) {
	srv := newLookupServer(map[string]string{
		"/api/qualityprofiles/search": `{"profiles":[{"key":"prof-1","name":"Other","language":"java"}]}`,
	})
	defer srv.Close()
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	_, err := lookupExistingProfile(context.Background(), raw, "Missing", "java", "org")
	if err == nil {
		t.Fatal("expected error for missing profile")
	}
}

func TestLookupExistingGate(t *testing.T) {
	srv := newLookupServer(map[string]string{
		"/api/qualitygates/list": `{"qualitygates":[{"id":42,"name":"Better"}]}`,
	})
	defer srv.Close()
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	id, err := lookupExistingGate(context.Background(), raw, "Better", "org")
	if err != nil {
		t.Fatal(err)
	}
	if id != "42" {
		t.Errorf("got %q, want %q", id, "42")
	}
}

func TestLookupExistingGroup(t *testing.T) {
	srv := newLookupServer(map[string]string{
		"/api/user_groups/search": `{"groups":[{"id":7,"name":"Admins"}]}`,
	})
	defer srv.Close()
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	id, err := lookupExistingGroup(context.Background(), raw, "Admins", "org")
	if err != nil {
		t.Fatal(err)
	}
	if id != "7" {
		t.Errorf("got %q, want %q", id, "7")
	}
}

// pagedGroupSearch serves api/user_groups/search the way SonarQube Cloud
// does for q=dev in an org holding many "*dev*" groups: `fillers` groups
// that sort first, then `tail`, paged by the request's ps/p. It records the
// pages asked for.
func pagedGroupSearch(t *testing.T, fillers int, tail ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var all []string
	for i := 1; i <= fillers; i++ {
		all = append(all, fmt.Sprintf("a-dev-filler-%03d", i))
	}
	all = append(all, tail...)
	var mu sync.Mutex
	var pages []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user_groups/search", func(w http.ResponseWriter, r *http.Request) {
		ps, _ := strconv.Atoi(r.URL.Query().Get("ps"))
		p, _ := strconv.Atoi(r.URL.Query().Get("p"))
		if ps <= 0 {
			ps = 100
		}
		if p <= 0 {
			p = 1
		}
		mu.Lock()
		pages = append(pages, r.URL.Query().Get("p")+"/"+r.URL.Query().Get("ps"))
		mu.Unlock()
		var groups []map[string]any
		for i := (p - 1) * ps; i < p*ps && i < len(all); i++ {
			groups = append(groups, map[string]any{"id": i + 1, "name": all[i]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"paging": map[string]any{"pageIndex": p, "pageSize": ps, "total": len(all)},
			"groups": groups,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &pages
}

// #685 live repro: with 120 "a-dev-filler-*" groups in the org, "dev" is on
// page 2 of q=dev. Reading only page 1 reported an existing group missing.
func TestLookupExistingGroupFindsExactMatchPastFirstPage(t *testing.T) {
	srv, pages := pagedGroupSearch(t, 120, "dev", "developers")
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	id, err := lookupExistingGroup(context.Background(), raw, "dev", "org")
	if err != nil {
		t.Fatalf("dev exists on page 2, got %v", err)
	}
	if id != "121" {
		t.Errorf("id = %q, want 121 (the exact match, not a filler)", id)
	}
	if got := strings.Join(*pages, ","); got != "1/100,2/100" {
		t.Errorf("pages requested = %s, want 1/100,2/100", got)
	}
}

func TestLookupExistingGroupStopsAtLastPageWhenAbsent(t *testing.T) {
	srv, pages := pagedGroupSearch(t, 150, "developers")
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	_, err := lookupExistingGroup(context.Background(), raw, "dev", "org")
	if !errors.Is(err, errLookupNotFound) {
		t.Fatalf("err = %v, want errLookupNotFound", err)
	}
	if len(*pages) != 2 {
		t.Errorf("pages requested = %v, want exactly 2 (151 results at 100 per page)", *pages)
	}
}

func TestLookupExistingTemplate(t *testing.T) {
	srv := newLookupServer(map[string]string{
		"/api/permissions/search_templates": `{"permissionTemplates":[{"id":"tpl-1","name":"Default Template"}]}`,
	})
	defer srv.Close()
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	id, err := lookupExistingTemplate(context.Background(), raw, "default template", "org")
	if err != nil {
		t.Fatal(err)
	}
	if id != "tpl-1" {
		t.Errorf("got %q, want %q", id, "tpl-1")
	}
}

func TestLookupExistingTemplateNotFound(t *testing.T) {
	srv := newLookupServer(map[string]string{
		"/api/permissions/search_templates": `{"permissionTemplates":[]}`,
	})
	defer srv.Close()
	raw := common.NewRawClient(srv.Client(), srv.URL+"/")

	_, err := lookupExistingTemplate(context.Background(), raw, "Missing", "org")
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}
