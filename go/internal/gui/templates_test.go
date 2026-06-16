// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mustParseTemplates is a test helper that parses templates or fails the test.
func mustParseTemplates(t *testing.T) *Templates {
	t.Helper()
	tmpl, err := ParseTemplates()
	if err != nil {
		t.Fatalf("ParseTemplates() error: %v", err)
	}
	return tmpl
}

// renderToRecorder renders a template page and returns the recorder.
func renderToRecorder(t *testing.T, tmpl *Templates, page string, data PageData) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	tmpl.RenderHTTP(w, page, data)
	if w.Code != http.StatusOK {
		t.Fatalf("RenderHTTP %s status = %d, want 200", page, w.Code)
	}
	return w
}

// assertContains checks that body contains substr.
func assertContains(t *testing.T, body, substr, label string) {
	t.Helper()
	if !strings.Contains(body, substr) {
		t.Errorf("rendered HTML missing %s (%q)", label, substr)
	}
}

func TestParseTemplates(t *testing.T) {
	tmpl := mustParseTemplates(t)
	if tmpl == nil {
		t.Fatal("ParseTemplates() returned nil")
	}

	pages := []string{"wizard", "history", "run_detail", "report"}
	for _, page := range pages {
		if _, ok := tmpl.pages[page]; !ok {
			t.Errorf("missing template: %s", page)
		}
	}
}

func TestRender(t *testing.T) {
	tmpl := mustParseTemplates(t)

	var buf bytes.Buffer
	err := tmpl.Render(&buf, "wizard", PageData{ActiveNav: "wizard"})
	if err != nil {
		t.Fatalf("Render wizard error: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, "SonarQube Server To Cloud Migration") {
		t.Error("rendered HTML missing title")
	}
	if !strings.Contains(html, "htmx.min.js") {
		t.Error("rendered HTML missing HTMX script")
	}
	if !strings.Contains(html, `class="active"`) {
		t.Error("rendered HTML missing active nav class")
	}
}

func TestRenderHTTP(t *testing.T) {
	tmpl := mustParseTemplates(t)

	w := httptest.NewRecorder()
	tmpl.RenderHTTP(w, "history", PageData{ActiveNav: "history"})

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestRenderUnknownPage(t *testing.T) {
	tmpl := mustParseTemplates(t)

	var buf bytes.Buffer
	err := tmpl.Render(&buf, "nonexistent", PageData{})
	if err == nil {
		t.Error("expected error for unknown page, got nil")
	}
}

func TestWizardTemplateContent(t *testing.T) {
	tmpl := mustParseTemplates(t)

	var buf bytes.Buffer
	err := tmpl.Render(&buf, "wizard", PageData{ActiveNav: "wizard"})
	if err != nil {
		t.Fatalf("Render wizard error: %v", err)
	}

	html := buf.String()
	checks := []struct {
		name    string
		contain string
	}{
		{"stepper", `id="wizard-stepper"`},
		{"event log", `id="event-log"`},
		{"prompt area", `id="prompt-area"`},
		{"start button", `id="start-btn"`},
		{"cancel button", `id="cancel-btn"`},
		{"websocket script", `new WebSocket`},
		{"phases card", `id="phases-card"`},
		{"wizard controls", `id="wizard-controls"`},
		{"PHASES array", `key: 'extract'`},
		{"phase inputs", `capturePhaseInput`},
		{"htmx-ws script", `htmx-ws.js`},
	}
	for _, c := range checks {
		if !strings.Contains(html, c.contain) {
			t.Errorf("wizard template missing %s (%q)", c.name, c.contain)
		}
	}
}

func TestBuildPageTemplateUnknownPage(t *testing.T) {
	baseTmpl := []byte(`<!doctype html><html>{{block "content" .}}{{end}}</html>`)
	funcMap := template.FuncMap{}
	_, err := buildPageTemplate("nonexistent_page", baseTmpl, funcMap)
	if err == nil {
		t.Error("expected error for nonexistent page template")
	}
}

func TestBuildPageTemplateBadBase(t *testing.T) {
	badBase := []byte(`{{define "base"}}{{.Foo`)
	funcMap := template.FuncMap{}
	_, err := buildPageTemplate("wizard", badBase, funcMap)
	if err == nil {
		t.Error("expected error for malformed base template")
	}
}

func TestRenderHTTPErrorPage(t *testing.T) {
	tmpl := mustParseTemplates(t)
	w := httptest.NewRecorder()
	tmpl.RenderHTTP(w, "bogus_page", PageData{})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestStaticHandler(t *testing.T) {
	handler := StaticHandler()
	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
