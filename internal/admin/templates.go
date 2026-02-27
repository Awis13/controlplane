package admin

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"controlplane/internal/node"
)

//go:embed templates/*.html templates/partials/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

var funcs = template.FuncMap{
	"sub": func(a, b int) int { return a - b },
	"add": func(a, b int) int { return a + b },
	"fmtTime": func(t time.Time) string {
		return t.Format("2006-01-02 15:04")
	},
	"joinInts": func(ints []int) string {
		s := make([]string, len(ints))
		for i, v := range ints {
			s[i] = strconv.Itoa(v)
		}
		return strings.Join(s, ", ")
	},
	"freeRAM": func(n node.Node) int {
		return n.TotalRAMMB - n.AllocatedRAMMB
	},
	"ramPercent": func(n node.Node) int {
		if n.TotalRAMMB == 0 {
			return 0
		}
		return n.AllocatedRAMMB * 100 / n.TotalRAMMB
	},
	"ramColor": func(n node.Node) string {
		if n.TotalRAMMB == 0 {
			return "green"
		}
		pct := n.AllocatedRAMMB * 100 / n.TotalRAMMB
		if pct > 90 {
			return "red"
		}
		if pct > 70 {
			return "orange"
		}
		return "green"
	},
	"derefInt": func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	},
	"derefStr": func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	},
	"derefTime": func(p *time.Time) time.Time {
		if p == nil {
			return time.Time{}
		}
		return *p
	},
}

// Templates holds parsed page templates.
type Templates struct {
	pages      map[string]*template.Template
	standalone map[string]*template.Template
}

// ParseTemplates parses all embedded templates and returns a Templates instance.
func ParseTemplates() (*Templates, error) {
	layout, err := fs.ReadFile(templateFS, "templates/layout.html")
	if err != nil {
		return nil, fmt.Errorf("read layout: %w", err)
	}

	// Read all partials
	partials, err := fs.Glob(templateFS, "templates/partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("glob partials: %w", err)
	}
	var partialContent []byte
	for _, p := range partials {
		data, err := fs.ReadFile(templateFS, p)
		if err != nil {
			return nil, fmt.Errorf("read partial %s: %w", p, err)
		}
		partialContent = append(partialContent, data...)
	}

	// Pages to parse
	pageNames := []string{"dashboard", "nodes", "projects", "tenants", "node_detail", "project_detail", "tenant_detail", "audit", "settings"}
	pages := make(map[string]*template.Template)

	for _, name := range pageNames {
		pageData, err := fs.ReadFile(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("read page %s: %w", name, err)
		}

		t, err := template.New("").Funcs(funcs).Parse(string(layout))
		if err != nil {
			return nil, fmt.Errorf("parse layout for %s: %w", name, err)
		}
		if _, err := t.Parse(string(partialContent)); err != nil {
			return nil, fmt.Errorf("parse partials for %s: %w", name, err)
		}
		if _, err := t.Parse(string(pageData)); err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}

		pages[name] = t
	}

	// Parse partials standalone (for htmx partial responses)
	partialTmpl, err := template.New("").Funcs(funcs).Parse(string(partialContent))
	if err != nil {
		return nil, fmt.Errorf("parse standalone partials: %w", err)
	}
	pages["partials"] = partialTmpl

	// Parse standalone pages (no layout)
	standalone := make(map[string]*template.Template)
	standaloneNames := []string{"login"}
	for _, name := range standaloneNames {
		data, err := fs.ReadFile(templateFS, "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("read standalone %s: %w", name, err)
		}
		t, err := template.New(name).Funcs(funcs).Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("parse standalone %s: %w", name, err)
		}
		standalone[name] = t
	}

	return &Templates{pages: pages, standalone: standalone}, nil
}

// RenderPage renders a full page into the response writer.
func (t *Templates) RenderPage(w http.ResponseWriter, page string, data any) error {
	tmpl, ok := t.pages[page]
	if !ok {
		return fmt.Errorf("unknown page: %s", page)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return fmt.Errorf("execute %s: %w", page, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

// RenderPartial renders a named partial template into the response writer.
func (t *Templates) RenderPartial(w http.ResponseWriter, name string, data any) error {
	tmpl, ok := t.pages["partials"]
	if !ok {
		return fmt.Errorf("partials not loaded")
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("execute partial %s: %w", name, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

// RenderStandalone renders a standalone page (no layout) into the response writer.
func (t *Templates) RenderStandalone(w http.ResponseWriter, name string, data any) error {
	tmpl, ok := t.standalone[name]
	if !ok {
		return fmt.Errorf("unknown standalone page: %s", name)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute standalone %s: %w", name, err)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

// StaticFS returns the embedded static filesystem for serving.
func StaticFS() http.FileSystem {
	sub, _ := fs.Sub(staticFS, "static")
	return http.FS(sub)
}
