package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
)

// Tests for the storage:s3 adapter. We spin up an in-process HTTP
// server that speaks the S3 wire enough to exercise PutObject /
// GetObject / HeadObject / DeleteObject / ListObjectsV2 /
// HeadBucket / presigned URLs.
//
// We deliberately don't reach for an external integration test
// (real AWS or a containerised MinIO) for the bulk of coverage —
// the wire shape is small enough that a fake server matches
// minio-go's expectations. The error-mapping + path manipulation
// logic on OUR side is what's most likely to regress; the fake
// gives that everything it needs.
//
// An optional integration test against real MinIO runs only when
// LOAMSS_S3_INTEGRATION_TEST=1 — never in CI by default.

// --- fake S3 server --------------------------------------------------------

type fakeS3 struct {
	mu      sync.Mutex
	bucket  string
	objects map[string][]byte
	srv     *httptest.Server
}

func newFakeS3(t *testing.T, bucket string) *fakeS3 {
	t.Helper()
	f := &fakeS3{
		bucket:  bucket,
		objects: map[string][]byte{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) endpoint() string {
	// minio-go expects host:port, not a full URL. Strip the scheme.
	return strings.TrimPrefix(f.srv.URL, "http://")
}

// handle implements just enough of the S3 wire for our adapter.
// minio-go uses virtual-host style by default, but pointing at
// an httptest server we can force path-style by always using
// "endpoint" as host. minio-go falls back to path-style for IP
// hosts, which httptest's 127.0.0.1:port satisfies.
func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// All requests look like /<bucket>/<key>... or /<bucket>?... for
	// list. Strip the bucket prefix.
	p := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 0 || parts[0] != f.bucket {
		http.Error(w, "bucket mismatch", http.StatusNotFound)
		return
	}
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}

	switch r.Method {
	case http.MethodHead:
		if key == "" {
			// HeadBucket — bucket exists.
			w.WriteHeader(http.StatusOK)
			return
		}
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.Header().Set("Content-Type", r.Header.Get("Accept"))
		w.Header().Set("ETag", `"deadbeef"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") != "" {
			f.writeListing(w, r.URL.Query().Get("prefix"))
			return
		}
		body, ok := f.objects[key]
		if !ok {
			f.writeError(w, http.StatusNotFound, "NoSuchKey", "key does not exist")
			return
		}
		// Range support, exercised by ReadStream tests.
		rng := r.Header.Get("Range")
		if rng != "" {
			start, end := parseRange(rng, int64(len(body)))
			if start >= 0 && end >= start && end < int64(len(body)) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[start : end+1])
				return
			}
		}
		w.Header().Set("ETag", `"deadbeef"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		// minio-go signs PUT bodies with the AWS chunked encoding
		// ("aws-chunked") when payload signing is on. The Content-
		// Encoding header carries the marker; if present we decode
		// the chunks to recover the raw payload. Otherwise the body
		// is the raw payload.
		// minio-go signals chunked-payload via either
		// Content-Encoding (aws-chunked) or the X-Amz-Content-Sha256
		// "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" sentinel. Test for
		// both to be robust across minio-go versions.
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
			strings.Contains(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING") {
			body = decodeAWSChunked(body)
		}
		f.objects[key] = body
		w.Header().Set("ETag", `"deadbeef"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// writeListing returns a minimal ListObjectsV2Result XML matching
// the AWS schema minio-go parses.
func (f *fakeS3) writeListing(w http.ResponseWriter, prefix string) {
	type Content struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
		ETag         string `xml:"ETag"`
	}
	type Result struct {
		XMLName  xml.Name  `xml:"ListBucketResult"`
		Name     string    `xml:"Name"`
		Prefix   string    `xml:"Prefix"`
		Contents []Content `xml:"Contents"`
	}
	result := Result{Name: f.bucket, Prefix: prefix}
	for k, v := range f.objects {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		result.Contents = append(result.Contents, Content{
			Key:          k,
			Size:         int64(len(v)),
			LastModified: time.Now().UTC().Format(time.RFC3339),
			ETag:         `"deadbeef"`,
		})
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(result)
}

// writeError returns the S3-XML error envelope minio-go expects.
func (f *fakeS3) writeError(w http.ResponseWriter, status int, code, message string) {
	type Body struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(Body{Code: code, Message: message})
}

// decodeAWSChunked decodes the AWS "aws-chunked" content encoding
// minio-go uses for signed PUT bodies. Each chunk is preceded by
// a hex-size + chunk-signature line and terminated by CRLF; a
// zero-size chunk marks the end of the stream. We don't verify
// the signatures (the fake doesn't have the secret); we just
// strip the framing.
func decodeAWSChunked(body []byte) []byte {
	out := make([]byte, 0, len(body))
	for len(body) > 0 {
		// Find the end of the chunk header line.
		nl := bytes.Index(body, []byte("\r\n"))
		if nl < 0 {
			return out
		}
		header := string(body[:nl])
		body = body[nl+2:]
		// Chunk header: "<hex-size>;chunk-signature=<sig>"
		sizeHex := header
		if semi := strings.Index(header, ";"); semi >= 0 {
			sizeHex = header[:semi]
		}
		var size int64
		_, err := fmt.Sscanf(sizeHex, "%x", &size)
		if err != nil {
			return out
		}
		if size == 0 {
			return out
		}
		if int64(len(body)) < size {
			return out
		}
		out = append(out, body[:size]...)
		body = body[size:]
		// Trailing \r\n after the payload.
		if len(body) >= 2 && body[0] == '\r' && body[1] == '\n' {
			body = body[2:]
		}
	}
	return out
}

func parseRange(header string, size int64) (int64, int64) {
	// Format: "bytes=START-END"; END may be empty for "to end."
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

func newAdapter(t *testing.T) (*Adapter, *fakeS3) {
	t.Helper()
	f := newFakeS3(t, "test-bucket")
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"endpoint":   f.endpoint(),
		"bucket":     "test-bucket",
		"access_key": "test",
		"secret_key": "test",
		"use_ssl":    false,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return a, f
}

// --- tests -----------------------------------------------------------------

func TestS3_WriteReadRoundTrip(t *testing.T) {
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
		t.Errorf("Read returned %q, want %q", got, want)
	}
}

func TestS3_ExistsAndDelete(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()
	if err := a.Write(ctx, "ephemeral.txt", []byte("temp")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ok, err := a.Exists(ctx, "ephemeral.txt")
	if err != nil || !ok {
		t.Errorf("Exists(present) = %v, %v", ok, err)
	}

	if err := a.Delete(ctx, "ephemeral.txt"); err != nil {
		t.Errorf("Delete: %v", err)
	}

	ok, err = a.Exists(ctx, "ephemeral.txt")
	if err != nil || ok {
		t.Errorf("Exists(deleted) = %v, %v; want false,nil", ok, err)
	}

	// Idempotent: deleting again should not error.
	if err := a.Delete(ctx, "ephemeral.txt"); err != nil {
		t.Errorf("Delete on missing should be nil, got %v", err)
	}
}

func TestS3_ReadOfMissingKeyReturnsNotFound(t *testing.T) {
	a, _ := newAdapter(t)
	_, err := a.Read(context.Background(), "no/such/key.txt")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Read of missing should wrap ErrNotFound, got %v", err)
	}
}

func TestS3_Metadata(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()
	if err := a.Write(ctx, "doc.json", []byte(`{"hello":"world"}`)); err != nil {
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
		t.Errorf("Size should be > 0 for non-empty object")
	}
	if md.ETag == "" {
		t.Errorf("ETag should be non-empty")
	}
}

func TestS3_List(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()
	for _, p := range []string{"docs/a.txt", "docs/b.txt", "other/c.txt"} {
		if err := a.Write(ctx, p, []byte("x")); err != nil {
			t.Fatalf("Write %s: %v", p, err)
		}
	}

	got, err := a.List(ctx, "docs/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var paths []string
	for entry := range got {
		if entry.Err != nil {
			t.Fatalf("List entry error: %v", entry.Err)
		}
		paths = append(paths, entry.Metadata.Path)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 entries under docs/, got %d: %v", len(paths), paths)
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "docs/") {
			t.Errorf("List returned path outside prefix: %s", p)
		}
	}
}

func TestS3_PresignedURL(t *testing.T) {
	a, _ := newAdapter(t)
	ctx := context.Background()

	got, err := a.SignedURL(ctx, "some/path.txt", 5*time.Minute, storage.OpRead)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	// minio-go signs with v4, so the canonical X-Amz-* params should
	// be present. Spot-check a few.
	for _, want := range []string{"X-Amz-Algorithm", "X-Amz-Signature", "X-Amz-Expires"} {
		if q.Get(want) == "" {
			t.Errorf("presigned URL missing %s in query: %s", want, u.RawQuery)
		}
	}
}

func TestS3_HealthCheck(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck on live adapter should pass, got %v", err)
	}
}

func TestS3_HealthCheck_BadBucket(t *testing.T) {
	// Point the adapter at a server whose bucket doesn't match.
	f := newFakeS3(t, "real-bucket")
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{
		"endpoint":   f.endpoint(),
		"bucket":     "wrong-bucket",
		"access_key": "test",
		"secret_key": "test",
		"use_ssl":    false,
	})
	if err == nil {
		t.Error("Init against wrong bucket should fail")
	}
}

func TestS3_RequiresBucket(t *testing.T) {
	a := &Adapter{}
	err := a.Init(context.Background(), map[string]any{
		"endpoint":   "s3.example.com",
		"access_key": "x",
		"secret_key": "y",
	})
	if err == nil {
		t.Error("Init without bucket should fail")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("error message should mention bucket, got: %v", err)
	}
}

func TestS3_Close_BlocksFurtherCalls(t *testing.T) {
	a, _ := newAdapter(t)
	if err := a.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := a.Read(context.Background(), "anything")
	if err == nil || !strings.Contains(err.Error(), "not initialised") {
		t.Errorf("Read after Close should fail with not-initialised, got %v", err)
	}
}

func TestS3_RegistryPicksUpAdapter(t *testing.T) {
	a, err := storage.New(adapterID)
	if err != nil {
		t.Fatalf("storage.New(%q): %v", adapterID, err)
	}
	if a == nil {
		t.Error("storage.New returned nil")
	}
}
