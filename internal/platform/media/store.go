package media

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Store keeps processed images on local disk under content-hash names. Because
// names derive from content, uploading the same picture twice stores it once,
// and files can be cached forever. Swap in an object-storage implementation of
// the same Save/Delete/Handler surface when running several instances.
type Store struct{ dir string }

const urlPrefix = "/media/"

var nameRe = regexp.MustCompile(`^[0-9a-f]{20}(-t)?\.(jpg|png)$`)

// NewStore creates the directory if needed and proves it is writable, so a bad
// UPLOAD_DIR fails at startup rather than on the first upload.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("upload dir: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return nil, fmt.Errorf("upload dir %q is not writable: %w", dir, err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return &Store{dir: dir}, nil
}

// Save writes both sizes and returns their public URLs.
func (s *Store) Save(p Processed) (fullURL, thumbURL string, err error) {
	full, thumb := p.Hash+"."+p.Ext, p.Hash+"-t."+p.Ext
	if err := s.write(full, p.Full); err != nil {
		return "", "", err
	}
	if err := s.write(thumb, p.Thumb); err != nil {
		return "", "", err
	}
	return urlPrefix + full, urlPrefix + thumb, nil
}

// write stores data atomically (temp file + rename) and skips files that already exist.
func (s *Store) write(name string, data []byte) error {
	dst := filepath.Join(s.dir, name)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Delete removes the files behind URLs this store produced. Anything else is ignored.
func (s *Store) Delete(urls ...string) error {
	var errs []error
	for _, u := range urls {
		name, ok := strings.CutPrefix(u, urlPrefix)
		if !ok || !nameRe.MatchString(name) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Handler serves /media/<name>. Only names this package generates are
// reachable, so there are no directory listings and no path traversal.
func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, urlPrefix)
		if !nameRe.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(s.dir, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".png") {
			w.Header().Set("Content-Type", "image/png")
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, st.ModTime(), f)
	})
}
