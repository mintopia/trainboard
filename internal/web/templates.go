package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

// embeddedFS holds the server's templates and static assets, baked into the
// binary. static/htmx.min.js is htmx 2.0.10, vendored verbatim from
// https://unpkg.com/htmx.org@2/dist/htmx.min.js (no build step, no CDN
// dependency at runtime).
//
//go:embed templates/* static/*
var embeddedFS embed.FS

// parseTemplates parses the shared layout, which defines the "layout" root
// template plus default "title"/"content" blocks. Each page clones this base
// (see the page template vars below) and parses its own {{define "title"}}
// and {{define "content"}} blocks into the clone, so pages never stomp on
// each other's block definitions.
func parseTemplates() *template.Template {
	return template.Must(template.New("layout.html").ParseFS(embeddedFS, "templates/layout.html"))
}

var (
	baseTemplate      = parseTemplates()
	setupTemplate     = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/setup.html"))
	loginTemplate     = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/login.html"))
	statusTemplate    = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/status.html"))
	configTemplate    = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/config.html"))
	appliedTemplate   = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/applied.html"))
	actionsTemplate   = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/actions.html"))
	rebootingTemplate = template.Must(template.Must(baseTemplate.Clone()).ParseFS(embeddedFS, "templates/rebooting.html"))
)

// staticFS returns the embedded static/ subtree for the file server.
func staticFS() http.FileSystem {
	sub, err := fs.Sub(embeddedFS, "static")
	if err != nil {
		panic("web: static subtree: " + err.Error())
	}
	return http.FS(sub)
}
