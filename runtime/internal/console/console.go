// Package console serves the Loamss web console — the user-facing
// configuration and management UI — from inside the runtime binary.
//
// The console is a Next.js static export (HTML + JS + CSS) that
// lives in the parallel `console/` subtree at the repo root. The
// build pipeline copies its production output into
// runtime/internal/console/dist/, which this package embeds at
// compile time via //go:embed.
//
// Architectural intent:
//
//   - Single binary, no separate process. `loamss start` serves
//     the daemon AND the console from the same listener.
//   - Same-origin everything. The console fetches /console/init,
//     /healthz, /mcp from its own origin — no CORS preflight, no
//     dev-vs-prod URL branching.
//   - Version monotonicity. The console's build is welded to the
//     runtime's build; the wizard and the daemon can never drift.
//
// Mounting: the runtime's HTTP mux registers API routes with
// specific patterns (`GET /healthz`, `POST /console/init`, etc.)
// and this package's Handler() under `/` as the catch-all. Go's
// http.ServeMux longest-pattern-wins routing keeps the API routes
// from being shadowed.
//
// Build coupling: a clean Go checkout has an empty dist/ (just a
// .gitkeep marker so //go:embed has something to point at). The
// handler detects the empty case and serves an inline "console not
// built" page so `go build ./...` works without bun installed.
// `make build` invokes the console's `bun run build` and copies
// its output into dist/ before linking the binary.
package console

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// distFS is the embedded static export. In a clean checkout the
// directory holds only a .gitkeep — `make console-build` populates
// it with the Next.js production output before linking the binary.
// The handler tolerates an empty dist/ by serving an inline
// "console not built" placeholder, so `go build ./...` works
// without bun on the developer's machine.
//
//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded console.
//
// Responsibilities:
//
//   - Serve files under the embedded dist/ tree.
//   - Set sensible cache headers (immutable for /_next/static/*,
//     no-store for index.html so the wizard always boots from the
//     latest deploy).
//   - Fall back to 404.html (the static export's not-found page)
//     for unknown paths rather than returning the bare net/http
//     "404 page not found" text.
//   - Refuse to serve outside dist/ (path traversal guard).
//
// The handler is read-only — there is no upload, no eval, no shell
// out. Its only job is to deliver bytes the build pipeline already
// produced.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Sub on an embed.FS only fails if the path is malformed,
		// which is a programming error (the directory is a literal
		// constant in the //go:embed directive). Panic at startup
		// rather than ship a binary that 500s on every console hit.
		panic("console: embedded dist/ unreachable: " + err.Error())
	}
	// If the embedded FS has no index.html (the .gitkeep-only case
	// in a pure-Go checkout), wrap with the placeholder fallback so
	// GET / still returns something readable.
	if _, statErr := fs.Stat(sub, "index.html"); errors.Is(statErr, fs.ErrNotExist) {
		return HandlerFS(placeholderFS{})
	}
	return HandlerFS(sub)
}

// HandlerFS is the test-friendly variant: the same handler logic,
// but the caller provides the filesystem. Production callers use
// Handler(). Tests pass a fstest.MapFS to drive edge cases (missing
// 404 page, non-canonical paths, etc.) without depending on what's
// in the embedded dist/.
func HandlerFS(root fs.FS) http.Handler {
	return &consoleHandler{root: root}
}

type consoleHandler struct {
	root fs.FS
}

func (h *consoleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Normalise the request path. http.ServeMux already strips
	// query strings; we resolve "." and ".." and lop the leading
	// slash so fs.FS sees a clean relative path.
	urlPath := path.Clean(r.URL.Path)
	if urlPath == "/" || urlPath == "." {
		urlPath = "index.html"
	} else {
		urlPath = strings.TrimPrefix(urlPath, "/")
	}

	// Path-traversal guard. path.Clean strips ".." segments that
	// don't escape, but a request like /../../etc/passwd would
	// arrive as "../../etc/passwd" after the prefix strip. Reject
	// any remaining ".." segment outright.
	if strings.HasPrefix(urlPath, "..") || strings.Contains(urlPath, "/../") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Resolve the path against the embedded filesystem BEFORE
	// checking the method. Reasoning: as the runtime's catch-all,
	// this handler sees every URL the API mux didn't claim,
	// including typo'd API routes (POST /pari, POST /helathz, etc.).
	// If we 405'd on POST first, callers would see a misleading
	// "method not allowed" for paths that don't exist at all. Doing
	// the existence check first gives the more accurate signal:
	// 404 for unknown paths, 405 for known paths with the wrong
	// method.
	relPath, ok := h.resolve(urlPath)
	if !ok {
		h.serveNotFound(w, r)
		return
	}

	// Only GET / HEAD make sense for static content.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := h.tryServeFile(w, r, relPath, true); err != nil {
		// The body may be partially written; we can't recover.
		// Falling through to the 404 path would double-write the
		// status. Leave it as-is and let the caller's logger pick
		// it up.
		_ = err
	}
}

// resolve returns the relative path inside the embedded FS that
// urlPath should serve, or ok=false if no candidate exists. Mirrors
// the candidate set in tryServe: direct hit for paths with an
// extension; <path>/index.html or <path>.html for directory-style
// routes.
func (h *consoleHandler) resolve(urlPath string) (string, bool) {
	if hasExt(urlPath) {
		if h.exists(urlPath) {
			return urlPath, true
		}
		return "", false
	}
	for _, candidate := range []string{
		path.Join(urlPath, "index.html"),
		urlPath + ".html",
	} {
		if h.exists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func (h *consoleHandler) exists(relPath string) bool {
	f, err := h.root.Open(relPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func (h *consoleHandler) serveNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}
	if ok, _ := h.tryServeFile(w, r, "404.html", false); ok {
		return
	}
	_, _ = w.Write([]byte("not found"))
}

// tryServeFile attempts to write the file at relPath. The bool
// return distinguishes "wrote a response" (true) from "file not
// found, try something else" (false). Any other error (read
// failure, etc.) returns (false, err) — the caller turns that into
// a 500.
//
// setStatusOK controls whether we write http.StatusOK explicitly.
// The 404 fallback path leaves status setting to the caller so the
// 404 doesn't get bumped back to 200.
func (h *consoleHandler) tryServeFile(w http.ResponseWriter, r *http.Request, relPath string, setStatusOK bool) (bool, error) {
	f, err := h.root.Open(relPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		http.Error(w, "console asset unreadable", http.StatusInternalServerError)
		return true, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "console asset stat failed", http.StatusInternalServerError)
		return true, err
	}
	if info.IsDir() {
		return false, nil
	}

	// Content-Type: derive from extension. The mime package's
	// defaults cover the small set we ship (html, js, css, woff2,
	// png, svg). Setting it explicitly avoids the surprising
	// "text/plain" content-sniffing fallback some clients use.
	if ct := contentTypeFor(relPath); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	// Cache headers.
	//
	// Next.js puts every hashed asset under /_next/static/. Those
	// filenames change on every build, so they're safe to mark
	// immutable for a year — the browser will fetch the new ones
	// the moment the HTML pointing at them changes.
	//
	// Everything else (index.html, 404.html, the static index.txt)
	// must NOT be cached: a stale index.html that points at
	// long-deleted hashed bundles is the classic "I deployed but
	// my browser shows the old app" footgun.
	if strings.HasPrefix(relPath, "_next/static/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}

	if setStatusOK {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		return true, nil
	}
	if _, err := io.Copy(w, f); err != nil {
		// Body already partially written; nothing useful we can do
		// beyond returning so the caller's logger can pick it up.
		return true, err
	}
	return true, nil
}

func hasExt(p string) bool {
	return path.Ext(p) != ""
}

// contentTypeFor returns the MIME type to advertise for a given
// path, using a tiny in-package table rather than the stdlib mime
// package. The set is small enough that the explicit table is
// cheaper and more obvious than runtime registration.
func contentTypeFor(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	}
	return ""
}

// placeholderFS is the fallback filesystem used when the binary
// was built without the production console bundle (the
// .gitkeep-only state of a fresh Go checkout). It serves an inline
// "console not built" page at the typical entry points and
// otherwise behaves as an empty fs.FS — Open returns
// fs.ErrNotExist for anything else, which the handler maps to a
// 404 cleanly.
//
// We use a tiny in-memory FS here rather than testing/fstest's
// MapFS so production code doesn't import the testing package.
type placeholderFS struct{}

func (placeholderFS) Open(name string) (fs.File, error) {
	switch name {
	case "index.html":
		return newPlaceholderFile("index.html", placeholderIndex), nil
	case "404.html":
		return newPlaceholderFile("404.html", placeholder404), nil
	}
	return nil, fs.ErrNotExist
}

type placeholderFile struct {
	name string
	data []byte
	off  int
}

func newPlaceholderFile(name, content string) *placeholderFile {
	return &placeholderFile{name: name, data: []byte(content)}
}

func (f *placeholderFile) Stat() (fs.FileInfo, error) {
	return placeholderInfo{name: f.name, size: int64(len(f.data))}, nil
}

func (f *placeholderFile) Read(p []byte) (int, error) {
	if f.off >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.off:])
	f.off += n
	return n, nil
}

func (f *placeholderFile) Close() error { return nil }

type placeholderInfo struct {
	name string
	size int64
}

func (i placeholderInfo) Name() string       { return i.name }
func (i placeholderInfo) Size() int64        { return i.size }
func (i placeholderInfo) Mode() fs.FileMode  { return 0o444 }
func (i placeholderInfo) ModTime() time.Time { return time.Time{} }
func (i placeholderInfo) IsDir() bool        { return false }
func (i placeholderInfo) Sys() any           { return nil }

const placeholderIndex = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>Loamss — console not built</title>
<meta name="viewport" content="width=device-width, initial-scale=1" />
<style>
body { font-family: ui-monospace, "SF Mono", Menlo, monospace; background: #faf8f3; color: #2b261d; max-width: 36rem; margin: 6rem auto; padding: 0 1.5rem; line-height: 1.6; }
h1 { font-family: Georgia, "Times New Roman", serif; font-weight: 400; font-size: 2rem; letter-spacing: -0.01em; margin: 0 0 1rem; }
code { background: rgba(0,0,0,0.05); padding: 0.1rem 0.35rem; border-radius: 3px; font-size: 0.92em; }
p { color: #5a554a; }
hr { border: 0; border-top: 1px solid rgba(0,0,0,0.1); margin: 2rem 0; }
small { color: #8a8478; }
</style>
</head>
<body>
<h1>Console not built.</h1>
<p>This binary was compiled without the Next.js console bundle. The runtime
itself is fine &mdash; every API endpoint (<code>/healthz</code>, <code>/version</code>,
<code>/console/init</code>, <code>/mcp</code>, &hellip;) is serving normally. Only the
web UI is missing.</p>
<p>To produce a binary that ships the console:</p>
<pre><code>make build</code></pre>
<p>The Makefile invokes <code>bun run build</code> in <code>console/</code>, copies the
static export into <code>runtime/internal/console/dist/</code>, and re-links
<code>loamss</code>.</p>
<hr>
<small>Inline placeholder &mdash; <code>runtime/internal/console/dist/</code> is empty in this build.</small>
</body>
</html>
`

const placeholder404 = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8" /><title>Not found</title>
<style>body{font-family:ui-monospace,Menlo,monospace;background:#faf8f3;color:#2b261d;max-width:36rem;margin:6rem auto;padding:0 1.5rem}h1{font-family:Georgia,serif;font-weight:400}</style>
</head>
<body><h1>Not found.</h1><p>The requested console path doesn&rsquo;t exist in this build.</p></body>
</html>
`
