package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// assets is populated by pnpm build. The tracked anchor keeps regular Go
// development commands working before the first frontend build.
//
//go:embed all:dist
var assets embed.FS

// Handler returns the embedded SPA only when a production index exists.
func Handler() (http.Handler, bool) {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return nil, false
	}
	server := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleaned := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if cleaned == "." {
			cleaned = ""
		}
		if cleaned != "" {
			if info, err := fs.Stat(dist, cleaned); err == nil && !info.IsDir() {
				server.ServeHTTP(w, r)
				return
			}
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		server.ServeHTTP(w, clone)
	}), true
}
