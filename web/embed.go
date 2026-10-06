// Package web embeds the static assets (compiled Tailwind CSS, Alpine.js, product images).
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"embed"
)

//go:embed static
var static embed.FS

func Static() fs.FS {
	sub, _ := fs.Sub(static, "static")
	return sub
}

var (
	versionOnce sync.Once
	version     string
)

// Version fingerprints the CSS and JS. Templates add it as ?v=..., so a deploy
// that changes them busts every browser cache while unchanged assets stay cached.
func Version() string {
	versionOnce.Do(func() {
		h := sha256.New()
		for _, name := range []string{"app.css", "alpine.min.js"} {
			b, _ := fs.ReadFile(Static(), name)
			h.Write(b)
		}
		version = hex.EncodeToString(h.Sum(nil))[:10]
	})
	return version
}

// Handler serves /static/*: no directory listings, long-lived caching for
// fingerprinted requests and a short cache otherwise.
func Handler() http.Handler {
	files := http.StripPrefix("/static/", http.FileServerFS(Static()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
}
