package gcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	storageadapter "github.com/loamss/loamss/runtime/internal/adapter/storage"
)

// mimeParse is a tiny wrapper so the parseMultipart helper can
// call mime.ParseMediaType without polluting the namespace.
var mimeParse = mime.ParseMediaType

// Tests for storage:gcs. Two tiers:
//
//   - PURE UNIT: spin up an httptest server speaking enough of
//     GCS's JSON API to drive the SDK. The SDK auto-targets it via
//     STORAGE_EMULATOR_HOST. Covers the full SPI.
//
//   - INTEGRATION: requires real GCS via GOOGLE_APPLICATION_CREDENTIALS
//     + LOAMSS_GCS_TEST_BUCKET. Skipped by default.

// --- fake GCS server -------------------------------------------------------

type fakeGCS struct {
	mu      sync.Mutex
	bucket  string
	objects map[string][]byte
	srv     *httptest.Server
}

func newFakeGCS(t *testing.T, bucket string) *fakeGCS {
	t.Helper()
	f := &fakeGCS{
		bucket:  bucket,
		objects: map[string][]byte{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	// The GCS SDK reads STORAGE_EMULATOR_HOST and redirects all
	// REST calls there. We strip the scheme; the SDK adds it back.
	host := strings.TrimPrefix(f.srv.URL, "http://")
	t.Setenv("STORAGE_EMULATOR_HOST", host)
	return f
}

// handle dispatches the GCS JSON API endpoints the SDK uses for
// the SPI methods we exercise. This isn't a full GCS emulator —
// just enough for our adapter's call patterns.
//
// Reference: https://cloud.google.com/storage/docs/json_api
func (f *fakeGCS) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	// GET /{bucket}/{key}  — XML-style download (the SDK uses
	// this for object Read, not the JSON API).
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/"+f.bucket+"/"):
		key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
		decoded, err := url.QueryUnescape(key)
		if err == nil {
			key = decoded
		}
		body, ok := f.objects[key]
		if !ok {
			http.Error(w, `<Error><Code>NoSuchKey</Code></Error>`, http.StatusNotFound)
			return
		}
		// Range support.
		if rng := r.Header.Get("Range"); rng != "" {
			start, end := parseRange(rng, int64(len(body)))
			if start >= 0 && end >= start && end < int64(len(body)) {
				w.Header().Set("Content-Range",
					fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[start : end+1])
				return
			}
		}
		w.Header().Set("ETag", `"deadbeef"`)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)

	// GET /storage/v1/b/{bucket}  — bucket attrs (HealthCheck)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/storage/v1/b/") &&
		!strings.Contains(r.URL.Path, "/o"):
		bucket := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/")
		if bucket != f.bucket {
			http.Error(w, `{"error":{"code":404,"message":"bucket not found"}}`, http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"kind":"storage#bucket","id":"%s","name":"%s"}`, bucket, bucket)

	// GET /storage/v1/b/{bucket}/o  — list objects
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/o"):
		// Detect read vs metadata via the `alt=media` query param.
		// Without alt=media → JSON metadata; with → raw bytes.
		isList := !strings.Contains(strings.TrimPrefix(r.URL.Path, "/storage/v1/b/"+f.bucket), "/o/")
		if isList {
			f.writeListing(w, r.URL.Query().Get("prefix"))
			return
		}
		// Object GET or metadata.
		obj := f.objectFromPath(r.URL.Path)
		body, ok := f.objects[obj]
		if !ok {
			http.Error(w, `{"error":{"code":404,"message":"NoSuchKey"}}`, http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			// Range support for ReadStream.
			if rng := r.Header.Get("Range"); rng != "" {
				start, end := parseRange(rng, int64(len(body)))
				if start >= 0 && end >= start && end < int64(len(body)) {
					w.Header().Set("Content-Range",
						fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(body[start : end+1])
					return
				}
			}
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
			_, _ = w.Write(body)
			return
		}
		// JSON metadata.
		fmt.Fprintf(w,
			`{"kind":"storage#object","name":"%s","bucket":"%s","size":"%d","updated":"%s","etag":"deadbeef","contentType":"text/plain"}`,
			obj, f.bucket, len(body), time.Now().UTC().Format(time.RFC3339))

	// POST /upload/storage/v1/b/{bucket}/o  — write object
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/"):
		f.handleUpload(w, r)

	// DELETE /storage/v1/b/{bucket}/o/{path}  — delete
	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/o/"):
		obj := f.objectFromPath(r.URL.Path)
		delete(f.objects, obj)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeGCS) objectFromPath(p string) string {
	// /storage/v1/b/{bucket}/o/{key}  → {key} (URL-decoded)
	prefix := "/storage/v1/b/" + f.bucket + "/o/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	name := strings.TrimPrefix(p, prefix)
	dec, err := url.QueryUnescape(name)
	if err != nil {
		return name
	}
	return dec
}

func (f *fakeGCS) handleUpload(w http.ResponseWriter, r *http.Request) {
	// Multipart upload: name comes from `?name=...` query.
	// Simple upload: name in form/multipart body — we handle just
	// the multipart case the SDK uses by default for small writes.
	name := r.URL.Query().Get("name")
	if name == "" {
		// Try to parse multipart for the object name.
		_ = r.ParseMultipartForm(32 << 20)
		if v := r.FormValue("name"); v != "" {
			name = v
		}
	}

	uploadType := r.URL.Query().Get("uploadType")
	body, _ := io.ReadAll(r.Body)

	switch uploadType {
	case "multipart":
		// Multipart upload: parse via Go's stdlib mime/multipart.
		// Boundary lives in the Content-Type header as
		// "multipart/related; boundary=...". Two parts:
		// (1) JSON metadata with the object name, (2) raw bytes.
		jsonName, payload, err := parseMultipart(r, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if name == "" {
			name = jsonName
		}
		f.objects[name] = payload
		fmt.Fprintf(w,
			`{"kind":"storage#object","name":"%s","bucket":"%s","size":"%d","updated":"%s","etag":"deadbeef"}`,
			name, f.bucket, len(payload), time.Now().UTC().Format(time.RFC3339))
	default:
		// Single-part / resumable / media — fall back to reading the
		// whole body.
		f.objects[name] = body
		fmt.Fprintf(w,
			`{"kind":"storage#object","name":"%s","bucket":"%s","size":"%d","updated":"%s","etag":"deadbeef"}`,
			name, f.bucket, len(body), time.Now().UTC().Format(time.RFC3339))
	}
}

// parseMultipart uses stdlib mime/multipart to split a GCS
// multipart upload body. The GCS SDK sends two parts in
// "multipart/related" form: a JSON metadata blob and a binary
// payload. We pull the name out of the JSON and return the
// payload bytes.
func parseMultipart(r *http.Request, body []byte) (string, []byte, error) {
	ct := r.Header.Get("Content-Type")
	// Content-Type carries the boundary as
	// `multipart/related; boundary=...`. Use mime/multipart's
	// reader to walk parts.
	_, params, err := mimeParse(ct)
	if err != nil {
		return "", nil, fmt.Errorf("parse content-type: %w", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", nil, fmt.Errorf("no boundary in content-type %q", ct)
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)

	var name string
	var payload []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
		data, _ := io.ReadAll(part)
		// First part: JSON metadata with the object name.
		// Subsequent parts: payload bytes (we keep the latest).
		ct := part.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/json") && name == "" {
			// Crude grep — fine, we know the SDK's exact shape.
			if idx := bytes.Index(data, []byte(`"name":"`)); idx >= 0 {
				rest := data[idx+len(`"name":"`):]
				if end := bytes.IndexByte(rest, '"'); end >= 0 {
					name = string(rest[:end])
				}
			}
			continue
		}
		payload = data
	}
	return name, payload, nil
}

func (f *fakeGCS) writeListing(w http.ResponseWriter, prefix string) {
	type Item struct {
		Name string `json:"name"`
		Size string `json:"size"`
	}
	var items []Item
	for k, v := range f.objects {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		items = append(items, Item{Name: k, Size: fmt.Sprintf("%d", len(v))})
	}
	fmt.Fprintf(w, `{"kind":"storage#objects","items":[`)
	for i, it := range items {
		if i > 0 {
			fmt.Fprintf(w, `,`)
		}
		fmt.Fprintf(w, `{"name":"%s","size":"%s","updated":"%s","etag":"deadbeef"}`,
			it.Name, it.Size, time.Now().UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(w, `]}`)
}

func parseRange(header string, size int64) (int64, int64) {
	v := strings.TrimPrefix(header, "bytes=")
	parts := strings.SplitN(v, "-", 2)
	if len(parts) != 2 {
		return -1, -1
	}
	var start, end int64
	_, err := fmt.Sscanf(parts[0], "%d", &start)
	if err != nil {
		return -1, -1
	}
	if parts[1] == "" {
		end = size - 1
	} else {
		_, err = fmt.Sscanf(parts[1], "%d", &end)
		if err != nil {
			return -1, -1
		}
	}
	return start, end
}

// --- helpers ---------------------------------------------------------------

func newAdapter(t *testing.T) (*Adapter, *fakeGCS) {
	t.Helper()
	f := newFakeGCS(t, "test-bucket")
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"bucket": "test-bucket",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a, f
}

// --- tests -----------------------------------------------------------------

func TestGCS_WriteReadRoundTrip(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()

	want := []byte("hello, loamss")
	if err := a.Write(ctx, "greetings/hello.txt", want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := a.Read(ctx, "greetings/hello.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Read = %q, want %q", got, want)
	}
}

func TestGCS_ExistsAndDelete(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()

	if err := a.Write(ctx, "tmp.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ok, _ := a.Exists(ctx, "tmp.txt")
	if !ok {
		t.Error("Exists(present) = false")
	}

	if err := a.Delete(ctx, "tmp.txt"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	ok, _ = a.Exists(ctx, "tmp.txt")
	if ok {
		t.Error("Exists(deleted) = true")
	}

	// Idempotent.
	if err := a.Delete(ctx, "tmp.txt"); err != nil {
		t.Errorf("Delete on missing should be nil, got %v", err)
	}
}

func TestGCS_ReadMissingReturnsNotFound(t *testing.T) {
	a, _ := newAdapter(t)
	_, err := a.Read(context.Background(), "no/such/key.txt")
	if !errors.Is(err, storageadapter.ErrNotFound) {
		t.Errorf("Read of missing should wrap ErrNotFound, got %v", err)
	}
}

func TestGCS_Metadata(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()
	if err := a.Write(ctx, "doc.json", []byte(`{"hi":1}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	md, err := a.Metadata(ctx, "doc.json")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if md.Path != "doc.json" {
		t.Errorf("Path = %q", md.Path)
	}
	if md.Size == 0 {
		t.Errorf("Size should be > 0")
	}
	if md.ETag == "" {
		t.Errorf("ETag should be non-empty")
	}
}

func TestGCS_List(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()
	for _, p := range []string{"docs/a.txt", "docs/b.txt", "other/c.txt"} {
		if err := a.Write(ctx, p, []byte("x")); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	ch, err := a.List(ctx, "docs/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var paths []string
	for entry := range ch {
		if entry.Err != nil {
			t.Fatalf("entry err: %v", entry.Err)
		}
		paths = append(paths, entry.Metadata.Path)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(paths), paths)
	}
}

func TestGCS_HealthCheck(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestGCS_RequiresBucket(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{})
	if err == nil {
		t.Error("Init without bucket should fail")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("error should mention bucket, got: %v", err)
	}
}

func TestGCS_Close_BlocksFurtherCalls(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := a.Read(context.Background(), "anything")
	if err == nil || !strings.Contains(err.Error(), "not initialised") {
		t.Errorf("Read after Close should fail, got %v", err)
	}
}

func TestGCS_RegistryPicksUpAdapter(t *testing.T) {
	a, err := storageadapter.New(adapterID)
	if err != nil {
		t.Fatalf("storage.New(%q): %v", adapterID, err)
	}
	if a == nil {
		t.Error("storage.New returned nil")
	}
}

// --- integration (LOAMSS_GCS_TEST_BUCKET) ----------------------------------

func TestIntegration_RoundTrip(t *testing.T) {
	bucket := os.Getenv("LOAMSS_GCS_TEST_BUCKET")
	if bucket == "" {
		t.Skip("set LOAMSS_GCS_TEST_BUCKET (plus GOOGLE_APPLICATION_CREDENTIALS) to run against real GCS")
	}
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{"bucket": bucket}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })

	ctx := context.Background()
	key := "loamss-integration-test/" + t.Name() + ".txt"
	want := []byte("hello from loamss")
	if err := a.Write(ctx, key, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	t.Cleanup(func() { _ = a.Delete(ctx, key) })

	got, err := a.Read(ctx, key)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Read mismatch")
	}
}
