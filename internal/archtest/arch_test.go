// Package archtest enforces the modular-monolith boundaries at test time:
//
//   - domain imports no other internal package
//   - app imports only its own domain and platform
//   - a module reaches another module only through its root (api.go) package
//   - platform never imports a business module
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const prefix = "github.com/NaheedRayan/goat-architecture/internal/"

var businessModules = map[string]bool{
	"admin": true, "cart": true, "catalog": true, "identity": true, "inventory": true, "order": true, "payment": true, "privacy": true, "shipping": true, "promotion": true, "content": true, "seo": true, "alerts": true, "review": true, "wishlist": true,
}

func TestBoundaries(t *testing.T) {
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "../")) // e.g. order/app/service.go
		parts := strings.Split(rel, "/")
		if len(parts) < 2 || parts[0] == "archtest" {
			return nil
		}
		owner := parts[0]
		layer := ""
		if len(parts) > 2 {
			layer = parts[1]
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			target := strings.TrimPrefix(p, prefix)
			tparts := strings.Split(target, "/")
			targetModule := tparts[0]
			bad := func(why string) { t.Errorf("%s imports %s: %s", rel, target, why) }

			switch {
			case owner == "platform":
				if businessModules[targetModule] {
					bad("platform must not depend on business modules")
				}
			case !businessModules[owner]:
			case layer == "domain" && targetModule != "platform" && !(targetModule == owner && len(tparts) > 1 && tparts[1] == "domain"):
				bad("domain may import only its own domain package (and nothing from platform's infra)")
			case layer == "domain" && targetModule == "platform":
				bad("domain must stay free of infrastructure")
			case layer == "app" && targetModule == owner && (len(tparts) < 2 || (tparts[1] != "domain" && tparts[1] != "app")):
				bad("app may import only its own domain and app packages")
			case layer == "app" && targetModule != owner && targetModule != "platform":
				bad("app must not import other modules; define a port and adapt it in adapters/")
			case targetModule != owner && businessModules[targetModule] && len(tparts) > 1:
				bad("other modules may be imported only through their root package")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
