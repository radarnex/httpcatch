package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// allowedExtensions lists the file extensions served from /static/*. Entry
// pages (index.html, login.html) are excluded because they are served via
// their own authenticated or templated handlers.
var allowedExtensions = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
}

// buildEtags walks the embedded FS and computes a strong ETag (first 16 hex
// characters of the SHA-256 digest) for each file under ui/. The returned map
// is keyed by the fs path ("ui/app.css", etc.).
func buildEtags(fsys fs.FS) map[string]string {
	tags := make(map[string]string)
	_ = fs.WalkDir(fsys, "ui", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		tags[p] = `"` + hex.EncodeToString(sum[:])[:16] + `"`
		return nil
	})
	return tags
}

// staticHandler returns an http.HandlerFunc that serves embedded CSS/JS assets
// from /static/*. Files are served with a 1-hour Cache-Control and a strong
// ETag derived from their SHA-256 digest. It supports conditional GET via
// If-None-Match. Only extensions listed in allowedExtensions are served; all
// other paths return 404.
func staticHandler(etags map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// chi wildcard includes the leading slash, e.g. "/app.css"
		tail := strings.TrimPrefix(r.URL.Path, "/static/")
		if tail == "" || strings.Contains(tail, "/") {
			http.NotFound(w, r)
			return
		}

		ext := path.Ext(tail)
		ct, ok := allowedExtensions[ext]
		if !ok {
			http.NotFound(w, r)
			return
		}

		fsPath := "ui/" + tail
		etag, hasEtag := etags[fsPath]
		if !hasEtag {
			http.NotFound(w, r)
			return
		}

		// Conditional GET: 304 when client already holds the current version.
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		data, err := fs.ReadFile(uiFS, fsPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}
}

