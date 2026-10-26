package syncserver

import (
	"io/fs"
	"net/http"
	"strings"
)

// webCSP lets the page run only its own scripts and WebAssembly, and talk
// only to this server. Inline styles are allowed because the text editor
// creates them.
const webCSP = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' blob:; connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// webHandler serves the static web frontend. A file with a ".gz" sibling is
// sent compressed to clients that accept it (the WebAssembly module is
// large).
func webHandler(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", webCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// Revalidate every time, so that an update is picked up at once.
		h.Set("Cache-Control", "no-cache")

		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasSuffix(name, ".wasm") && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			if f, err := files.Open(name + ".gz"); err == nil {
				f.Close()
				h.Set("Content-Type", "application/wasm")
				h.Set("Content-Encoding", "gzip")
				h.Add("Vary", "Accept-Encoding")
				r2 := r.Clone(r.Context())
				r2.URL.Path += ".gz"
				fileServer.ServeHTTP(w, r2)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
