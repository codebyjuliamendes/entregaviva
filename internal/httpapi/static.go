package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHandler serves the embedded frontend with strong ETags so browsers
// revalidate cheaply (files are not fingerprinted, hence Cache-Control: no-cache).
type staticHandler struct {
	files http.Handler
	etags map[string]string
}

func newStaticHandler(fsys fs.FS) (*staticHandler, error) {
	if fsys == nil {
		return nil, errors.New("httpapi: static FS is required")
	}
	etags := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		etags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &staticHandler{files: http.FileServerFS(fsys), etags: etags}, nil
}

func (s *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := path.Clean("/" + r.URL.Path)
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	if tag, ok := s.etags[p]; ok {
		w.Header().Set("ETag", tag)
		w.Header().Set("Cache-Control", "no-cache")
	}
	s.files.ServeHTTP(w, r)
}
