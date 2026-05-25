// Package gcs implements the storage:gcs adapter — native Google
// Cloud Storage backend using Google's official Go SDK.
//
// Why a native adapter when storage:s3 already works against GCS
// via interoperability mode (HMAC keys)?
//
//  1. Workload Identity. On GKE / Cloud Run / Compute Engine, the
//     runtime authenticates as a service account without any
//     static credentials on disk. S3-compat requires HMAC keys
//     the operator has to mint and rotate; native GCS uses ADC
//     and the keys never leave Google's metadata server.
//  2. Native V4 signed URLs from service accounts. The S3-compat
//     surface can sign URLs too, but the signing key is the HMAC
//     one — rotating it invalidates every outstanding URL. The
//     native API signs with the service account's ephemeral
//     credentials; rotation policy is GCP's, not ours.
//  3. Customer-managed encryption keys (CMEK), uniform bucket-level
//     access, requester-pays, retention policies — features only
//     the native API exposes cleanly.
//
// Dep choice: cloud.google.com/go/storage is Google's official Go
// SDK. Pure Go, Apache-2.0, battle-tested in production. The
// alternative is rolling raw HTTP + V4 signing + chunked uploads
// + resumable-upload protocol by hand. Same justification as
// minio-go for storage:s3 — a subtle bug in storage signing makes
// users' data inaccessible.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	storageadapter "github.com/loamss/loamss/runtime/internal/adapter/storage"
)

const adapterID = "storage:gcs"

func init() {
	storageadapter.Register(adapterID, func() storageadapter.Adapter { return &Adapter{} })
}

// Adapter is the storage:gcs concrete adapter.
//
// Zero value is unusable; call Init before any other method.
// After Init, methods are safe for concurrent use — the GCS client
// is goroutine-safe.
type Adapter struct {
	mu     sync.RWMutex
	client *storage.Client
	bucket string
	// signServiceAccount is the email of the service account used
	// to sign URLs. Empty means "use the credentials from the
	// client" — works when ADC is a service-account key file but
	// NOT when the runtime is running under Workload Identity (the
	// metadata server can't directly produce signing material;
	// you need the IAM Credentials API + service-account
	// impersonation). Empty also works in the common case of a
	// keyfile under GOOGLE_APPLICATION_CREDENTIALS — Google's SDK
	// auto-extracts the email from it.
	signServiceAccount string
	inited             bool
}

// Init reads config + verifies the bucket is reachable. Expected
// config keys:
//
//	bucket:           "my-loamss-bucket"     (required)
//	credentials_file: "/path/to/sa.json"     (optional; default uses ADC)
//	sign_service_account: "sa@project.iam.gserviceaccount.com"
//	                  (optional; required for signed URLs under
//	                  Workload Identity, where the SDK can't
//	                  introspect the signing identity)
//
// Auth chain (when credentials_file is empty):
//  1. GOOGLE_APPLICATION_CREDENTIALS env var
//  2. gcloud user creds (~/.config/gcloud/application_default_credentials.json)
//  3. GCE / GKE / Cloud Run metadata server (Workload Identity)
//
// The SDK handles all three transparently — we don't have to.
func (a *Adapter) Init(ctx context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	bucket, err := requiredString(config, "bucket")
	if err != nil {
		return err
	}
	credFile := optionalString(config, "credentials_file", "")
	signSA := optionalString(config, "sign_service_account", "")

	var opts []option.ClientOption
	if credFile != "" {
		// WithCredentialsFile is deprecated in favour of
		// AuthCredentials, but it's still the simplest path for
		// loading a service-account JSON file. The deprecation
		// warning is about its security model (filesystem creds
		// are inferior to Workload Identity); for the
		// "credentials_file is set" case the user has explicitly
		// opted in. When unset, the SDK uses ADC which already
		// prefers Workload Identity.
		opts = append(opts, option.WithCredentialsFile(credFile)) //nolint:staticcheck // explicit file path is the v0.1 contract
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("gcs: building client: %w", err)
	}

	a.client = client
	a.bucket = bucket
	a.signServiceAccount = signSA
	a.inited = true

	// HealthCheck doubles as connectivity validation; bad creds,
	// missing bucket, or wrong project surface as a startup error
	// rather than later as a Write failure.
	if err := a.healthCheckLocked(ctx); err != nil {
		a.inited = false
		_ = a.client.Close()
		a.client = nil
		return fmt.Errorf("gcs: bucket %q: %w", bucket, err)
	}
	return nil
}

// --- read side -------------------------------------------------------------

// Read returns the entire object at path.
func (a *Adapter) Read(ctx context.Context, path string) ([]byte, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	r, err := a.client.Bucket(a.bucket).Object(path).NewReader(ctx)
	if err != nil {
		return nil, mapErr(err, "read", path)
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// ReadStream returns a byte-range reader over the object at path.
// length == 0 means "to end of object." Caller closes the reader.
func (a *Adapter) ReadStream(
	ctx context.Context, path string, offset, length int64,
) (io.ReadCloser, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	// GCS uses length = -1 for "to end"; we use 0. Translate.
	gcsLength := length
	if length == 0 {
		gcsLength = -1
	}
	r, err := a.client.Bucket(a.bucket).Object(path).NewRangeReader(ctx, offset, gcsLength)
	if err != nil {
		return nil, mapErr(err, "read-stream", path)
	}
	return r, nil
}

// --- write side ------------------------------------------------------------

// Write stores content at path. Existing objects are overwritten.
func (a *Adapter) Write(ctx context.Context, path string, content []byte) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	w := a.client.Bucket(a.bucket).Object(path).NewWriter(ctx)
	if ct := detectContentType(path, content); ct != "" {
		w.ContentType = ct
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return mapErr(err, "write", path)
	}
	if err := w.Close(); err != nil {
		return mapErr(err, "write", path)
	}
	return nil
}

// WriteStream is the streaming counterpart of Write. GCS handles
// chunked uploads transparently via the resumable-upload protocol
// past a configurable threshold (default 16 MiB).
func (a *Adapter) WriteStream(ctx context.Context, path string, content io.Reader) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	w := a.client.Bucket(a.bucket).Object(path).NewWriter(ctx)
	if ct := detectContentType(path, nil); ct != "" {
		w.ContentType = ct
	}
	if _, err := io.Copy(w, content); err != nil {
		_ = w.Close()
		return mapErr(err, "write-stream", path)
	}
	if err := w.Close(); err != nil {
		return mapErr(err, "write-stream", path)
	}
	return nil
}

// Delete removes the object at path. Idempotent — missing key is
// not an error.
func (a *Adapter) Delete(ctx context.Context, path string) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	err := a.client.Bucket(a.bucket).Object(path).Delete(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return mapErr(err, "delete", path)
}

// --- queries ---------------------------------------------------------------

// Exists is a cheap HEAD probe.
func (a *Adapter) Exists(ctx context.Context, path string) (bool, error) {
	if err := a.requireInited(); err != nil {
		return false, err
	}
	_, err := a.client.Bucket(a.bucket).Object(path).Attrs(ctx)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	return false, mapErr(err, "exists", path)
}

// Metadata returns size + content-type + mtime + ETag.
func (a *Adapter) Metadata(ctx context.Context, path string) (storageadapter.ObjectMetadata, error) {
	if err := a.requireInited(); err != nil {
		return storageadapter.ObjectMetadata{}, err
	}
	attrs, err := a.client.Bucket(a.bucket).Object(path).Attrs(ctx)
	if err != nil {
		return storageadapter.ObjectMetadata{}, mapErr(err, "metadata", path)
	}
	return storageadapter.ObjectMetadata{
		Path:        path,
		Size:        attrs.Size,
		ContentType: attrs.ContentType,
		ModTime:     attrs.Updated,
		ETag:        strings.Trim(attrs.Etag, `"`),
	}, nil
}

// List enumerates objects whose key begins with prefix. The
// listing runs on a background goroutine; the channel closes
// when enumeration completes or ctx is canceled.
func (a *Adapter) List(ctx context.Context, prefix string) (<-chan storageadapter.ListEntry, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	out := make(chan storageadapter.ListEntry)
	go func() {
		defer close(out)
		it := a.client.Bucket(a.bucket).Objects(ctx, &storage.Query{Prefix: prefix})
		for {
			attrs, err := it.Next()
			if errors.Is(err, iterator.Done) {
				return
			}
			if err != nil {
				select {
				case out <- storageadapter.ListEntry{Err: err}:
				case <-ctx.Done():
				}
				return
			}
			entry := storageadapter.ListEntry{
				Metadata: storageadapter.ObjectMetadata{
					Path:        attrs.Name,
					Size:        attrs.Size,
					ContentType: attrs.ContentType,
					ModTime:     attrs.Updated,
					ETag:        strings.Trim(attrs.Etag, `"`),
				},
			}
			select {
			case out <- entry:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// --- signed URLs -----------------------------------------------------------

// SignedURL returns a V4 presigned URL for direct GET / PUT.
// TTL is clamped at 7 days (V4 hard cap).
//
// Under Workload Identity, signing requires the IAM Credentials
// API and impersonation of a service account that holds
// roles/iam.serviceAccountTokenCreator on itself. The SDK
// auto-detects this when sign_service_account is set; otherwise
// it tries to sign with the local credentials, which only works
// when a key file is present.
func (a *Adapter) SignedURL(
	ctx context.Context, path string, ttl time.Duration, op storageadapter.Op,
) (string, error) {
	if err := a.requireInited(); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour
	}

	var method string
	switch op {
	case storageadapter.OpRead:
		method = "GET"
	case storageadapter.OpWrite:
		method = "PUT"
	default:
		return "", fmt.Errorf("gcs: signed URL not supported for op %q", op)
	}

	opts := &storage.SignedURLOptions{
		Scheme:  storage.SigningSchemeV4,
		Method:  method,
		Expires: time.Now().Add(ttl),
	}
	// When sign_service_account is set, ask the SDK to sign via
	// the IAM Credentials API impersonating that SA. This is the
	// only way to sign under Workload Identity.
	if a.signServiceAccount != "" {
		opts.GoogleAccessID = a.signServiceAccount
		opts.SignBytes = func(b []byte) ([]byte, error) {
			return iamSignBlob(ctx, a.signServiceAccount, b)
		}
	}

	url, err := a.client.Bucket(a.bucket).SignedURL(path, opts)
	if err != nil {
		return "", fmt.Errorf("gcs: signing URL: %w", err)
	}
	return url, nil
}

// iamSignBlob calls the IAM Credentials API to sign arbitrary
// bytes using the given service account's private key. Used by
// SignedURL under Workload Identity (where the runtime doesn't
// have direct access to a signing key, only the right to
// impersonate). Stubbed in this commit; full IAM Credentials
// integration is a follow-up.
func iamSignBlob(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, errors.New("gcs: signed URL under Workload Identity requires the IAM Credentials API integration (not yet wired); supply credentials_file instead")
}

// --- health + lifecycle ----------------------------------------------------

// HealthCheck verifies the adapter can reach its bucket via a
// cheap Attrs() call on the bucket itself.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	return a.healthCheckLocked(ctx)
}

func (a *Adapter) healthCheckLocked(ctx context.Context) error {
	_, err := a.client.Bucket(a.bucket).Attrs(ctx)
	if err != nil {
		return fmt.Errorf("bucket attrs: %w", err)
	}
	return nil
}

// Close releases the underlying client.
func (a *Adapter) Close(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		_ = a.client.Close()
	}
	a.inited = false
	a.client = nil
	return nil
}

// --- helpers ---------------------------------------------------------------

func (a *Adapter) requireInited() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return errors.New("gcs: adapter not initialised (call Init first)")
	}
	return nil
}

func mapErr(err error, op, path string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("%w: %s (op=%s)", storageadapter.ErrNotFound, path, op)
	}
	return fmt.Errorf("gcs %s on %q: %w", op, path, err)
}

func detectContentType(path string, body []byte) string {
	if body != nil {
		if len(body) >= 4 && string(body[:4]) == "%PDF" {
			return "application/pdf"
		}
		if len(body) >= 8 && string(body[:8]) == "\x89PNG\r\n\x1a\n" {
			return "image/png"
		}
	}
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	switch strings.ToLower(path[idx:]) {
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".txt", ".md":
		return "text/plain; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".pdf":
		return "application/pdf"
	}
	return ""
}

func requiredString(config map[string]any, key string) (string, error) {
	v, ok := config[key]
	if !ok {
		return "", fmt.Errorf("gcs: missing required config: %s", key)
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("gcs: %s must be a non-empty string (got %T %v)", key, v, v)
	}
	return s, nil
}

func optionalString(config map[string]any, key, fallback string) string {
	v, ok := config[key]
	if !ok {
		return fallback
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return fallback
	}
	return s
}
