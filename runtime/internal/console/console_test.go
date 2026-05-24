package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Tests for the console static handler. We drive it with a
// fstest.MapFS so the cases are independent of whatever
// runtime/internal/console/dist/ currently contains in this
// checkout.
//
// The contract these lock in:
//   - GET / returns index.html with the right Content-Type
//   - GET /_next/static/...  returns the file with an immutable
//     Cache-Control (a year)
//   - GET /<known.html> returns that file
//   - GET /<unknown> returns 404 with the 404 page body
//   - GET /<unknown> with no 404.html returns 404 + "not found"
//   - HEAD works
//   - POST returns 405
//   - Path traversal (..) returns 403
//   - The handler never falls back to index.html for unknown
//     paths (that would silently render the wizard for API typos)

func newHandler(t *testing.T, files map[string]string) http.Handler {
	t.Helper()
	mfs := fstest.MapFS{}
	for name, content := range files {
		mfs[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return HandlerFS(mfs)
}

func get(t *testing.T, h http.Handler, p string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, p, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestHandler_ServesIndex(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html": "<html>hello</html>",
	})
	resp := get(t, h, "/")
	if resp.StatusCode != 200 {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body(t, resp), "hello") {
		t.Errorf("body missing index content")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store for index.html", cc)
	}
}

func TestHandler_ServesHashedStaticAssetWithImmutableCache(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html":                             "<html>x</html>",
		"_next/static/chunks/main-abc123.js":     "console.log('x');",
		"_next/static/css/styles-def456.css":     "body{}",
		"_next/static/media/font-deadbeef.woff2": "binary",
	})

	cases := []struct {
		path string
		ct   string
	}{
		{"/_next/static/chunks/main-abc123.js", "text/javascript; charset=utf-8"},
		{"/_next/static/css/styles-def456.css", "text/css; charset=utf-8"},
		{"/_next/static/media/font-deadbeef.woff2", "font/woff2"},
	}
	for _, c := range cases {
		resp := get(t, h, c.path)
		if resp.StatusCode != 200 {
			t.Errorf("%s status %d, want 200", c.path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != c.ct {
			t.Errorf("%s Content-Type = %q, want %q", c.path, ct, c.ct)
		}
		cc := resp.Header.Get("Cache-Control")
		if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
			t.Errorf("%s Cache-Control = %q, want immutable + 1yr max-age", c.path, cc)
		}
	}
}

func TestHandler_DirectoryStyleRoute(t *testing.T) {
	// Next.js trailingSlash:true produces /<route>/index.html.
	// A request for "/about/" should serve about/index.html;
	// "/about" (no slash) should also resolve.
	h := newHandler(t, map[string]string{
		"index.html":       "<html>home</html>",
		"about/index.html": "<html>about</html>",
	})

	for _, p := range []string{"/about", "/about/"} {
		resp := get(t, h, p)
		if resp.StatusCode != 200 {
			t.Errorf("%s status %d, want 200", p, resp.StatusCode)
		}
		if !strings.Contains(body(t, resp), "about") {
			t.Errorf("%s body missing 'about'", p)
		}
	}
}

func TestHandler_UnknownPathReturns404Page(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html": "<html>home</html>",
		"404.html":   "<html>nope</html>",
	})
	resp := get(t, h, "/does-not-exist")
	if resp.StatusCode != 404 {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(body(t, resp), "nope") {
		t.Errorf("404 body should come from 404.html")
	}
}

func TestHandler_UnknownPathWithout404FileReturnsPlain404(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html": "<html>home</html>",
	})
	resp := get(t, h, "/does-not-exist")
	if resp.StatusCode != 404 {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
	if got := body(t, resp); got != "not found" {
		t.Errorf("body = %q, want 'not found'", got)
	}
}

func TestHandler_UnknownPathDoesNotFallBackToIndex(t *testing.T) {
	// A typo'd API call like /healhz (missing 't') reaches the
	// console handler because the mux's longest-pattern-wins
	// routing doesn't match it. If the handler fell back to
	// index.html, the caller would see the wizard HTML with a
	// 200 — silently masking the typo. The handler must NOT do
	// that.
	h := newHandler(t, map[string]string{
		"index.html": "<html>wizard</html>",
	})
	resp := get(t, h, "/healhz")
	if resp.StatusCode != 404 {
		t.Errorf("status %d, want 404 (no SPA fallback)", resp.StatusCode)
	}
	if strings.Contains(body(t, resp), "wizard") {
		t.Errorf("unknown path fell back to index.html — should 404 instead")
	}
}

func TestHandler_HEADWorks(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html": "<html>x</html>",
	})
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("status %d, want 200", resp.StatusCode)
	}
	if got := body(t, resp); got != "" {
		t.Errorf("HEAD response carried body: %q", got)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("HEAD Content-Type = %q", ct)
	}
}

func TestHandler_RejectsNonGETOnExistingPath(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html": "<html>x</html>",
	})
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(method, "/", strings.NewReader(""))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		resp := w.Result()
		if resp.StatusCode != 405 {
			t.Errorf("%s status %d, want 405", method, resp.StatusCode)
		}
		if resp.Header.Get("Allow") == "" {
			t.Errorf("%s response missing Allow header", method)
		}
	}
}

func TestHandler_NonGETOnMissingPathReturns404Not405(t *testing.T) {
	// The catch-all on the runtime mux sees POSTs to typo'd API
	// routes (e.g., /pari instead of /pair). Returning 405 there
	// would be misleading — the path doesn't exist at all. The
	// handler must check existence first and return 404 for
	// unknown paths regardless of method.
	h := newHandler(t, map[string]string{
		"index.html": "<html>x</html>",
	})
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(method, "/does-not-exist", strings.NewReader(""))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		resp := w.Result()
		if resp.StatusCode != 404 {
			t.Errorf("%s /does-not-exist status %d, want 404 (not 405)", method, resp.StatusCode)
		}
	}
}

func TestHandler_RejectsPathTraversal(t *testing.T) {
	h := newHandler(t, map[string]string{
		"index.html":      "<html>x</html>",
		"secret/data.txt": "should not be reachable via ..",
	})
	// path.Clean strips traversal that stays inside the root.
	// Anything that escapes (i.e., leaves a literal ".." after
	// cleaning) must be refused.
	cases := []string{
		"/../etc/passwd",
		"/foo/../../etc/passwd",
		"/../../runtime/internal/secrets",
	}
	for _, p := range cases {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.URL.Path = p // ensure no client-side normalisation
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		resp := w.Result()
		if resp.StatusCode == 200 {
			t.Errorf("%s returned 200 — path traversal not refused", p)
		}
	}
}

func TestHandler_DefaultEmbeddedHandlerServesPlaceholder(t *testing.T) {
	// Smoke check that the production-path Handler() (which uses
	// the //go:embed dist) actually returns something useful.
	// We don't assert on body content because the placeholder
	// may be overwritten by `make build` — we just want to know
	// the embed is wired and the root index loads.
	h := Handler()
	resp := get(t, h, "/")
	if resp.StatusCode != 200 {
		t.Errorf("default handler / status %d, want 200", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("default handler / Content-Type = %q, want text/html",
			resp.Header.Get("Content-Type"))
	}
}
