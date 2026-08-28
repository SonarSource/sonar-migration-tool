// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package gui

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"

	"github.com/sonar-solutions/sonar-migration-tool/internal/version"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// PageData is the base data passed to every page template.
type PageData struct {
	ActiveNav string
	Data      any
}

// Templates holds parsed page templates ready to execute.
type Templates struct {
	pages map[string]*template.Template
}

// ParseTemplates parses all page templates from the embedded filesystem.
// Each page template includes base.html as its layout.
func ParseTemplates() (*Templates, error) {
	funcMap := template.FuncMap{
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s) //nolint:gosec // trusted server-generated HTML
		},
		// toolName / toolVersion expose the build identity to base.html's
		// footer, shared by every page without threading it through PageData.
		"toolName":    func() string { return version.ToolName },
		"toolVersion": func() string { return version.Version },
	}

	baseTmpl, err := fs.ReadFile(templateFS, "templates/base.html")
	if err != nil {
		return nil, fmt.Errorf("read base template: %w", err)
	}

	pages := []string{"wizard", "history", "run_detail", "report"}
	t := &Templates{pages: make(map[string]*template.Template, len(pages))}

	for _, page := range pages {
		tmpl, buildErr := buildPageTemplate(page, baseTmpl, funcMap)
		if buildErr != nil {
			return nil, buildErr
		}
		t.pages[page] = tmpl
	}

	return t, nil
}

// buildPageTemplate creates a single page template by combining the base
// layout, the page content, and any partials.
func buildPageTemplate(page string, baseTmpl []byte, funcMap template.FuncMap) (*template.Template, error) {
	pageFile := fmt.Sprintf("templates/%s.html", page)
	pageContent, err := fs.ReadFile(templateFS, pageFile)
	if err != nil {
		return nil, fmt.Errorf("read %s template: %w", page, err)
	}

	tmpl, err := template.New("base.html").Funcs(funcMap).Parse(string(baseTmpl))
	if err != nil {
		return nil, fmt.Errorf("parse base for %s: %w", page, err)
	}

	if _, err = tmpl.Parse(string(pageContent)); err != nil {
		return nil, fmt.Errorf("parse %s: %w", page, err)
	}

	if err = parsePartials(tmpl); err != nil {
		return nil, err
	}

	return tmpl, nil
}

// parsePartials adds any partial templates found in the partials directory.
func parsePartials(tmpl *template.Template) error {
	partials, _ := fs.Glob(templateFS, "templates/partials/*.html")
	for _, p := range partials {
		content, err := fs.ReadFile(templateFS, p)
		if err != nil {
			return fmt.Errorf("read partial %s: %w", p, err)
		}
		if _, err = tmpl.Parse(string(content)); err != nil {
			return fmt.Errorf("parse partial %s: %w", p, err)
		}
	}
	return nil
}

// Render executes a named page template, writing HTML to w.
func (t *Templates) Render(w io.Writer, page string, data PageData) error {
	tmpl, ok := t.pages[page]
	if !ok {
		return fmt.Errorf("template %q not found", page)
	}
	return tmpl.Execute(w, data)
}

// RenderHTTP renders a page template as an HTTP response.
func (t *Templates) RenderHTTP(w http.ResponseWriter, page string, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Render(w, page, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// StaticHandler returns an http.Handler serving embedded static files.
func StaticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
