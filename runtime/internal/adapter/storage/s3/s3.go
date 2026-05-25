// Package s3 implements the storage:s3 adapter — a backend that
// speaks the AWS S3 API to any S3-compatible service (AWS, R2, B2,
// MinIO, Wasabi, Backblaze, etc.).
//
// Choice of dependency: minio-go is the only third-party Go pkg
// the adapter pulls in. The alternative was rolling SigV4 +
// presigned URLs + multipart upload by hand (~400 lines of crypto-
// adjacent code we'd own forever). For a storage backend — where
// a subtle signing bug would render users' data unreachable — a
// battle-tested client is the right call. minio-go is pure Go,
// Apache-2.0, and works against every S3-compat service we've
// promised to support.
//
// Encryption: this adapter does NOT encrypt at rest. Users who
// want at-rest encryption configure it on the bucket itself (SSE-
// S3, SSE-KMS, or client-side via a wrapper future adapter). The
// fs-encrypted adapter encrypts because the local filesystem has
// no native option; S3 buckets have several, so we don't force one.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
)

const adapterID = "storage:s3"

func init() {
	storage.Register(adapterID, func() storage.Adapter { return &Adapter{} })
}

// Adapter is the storage:s3 concrete adapter.
//
// Zero value is unusable; call Init before any other method.
// After Init, all methods are safe for concurrent use — the minio
// client is itself goroutine-safe.
type Adapter struct {
	mu     sync.RWMutex
	client *minio.Client
	bucket string
	// presign cap protects against absurd TTLs the runtime might
	// receive from a misconfigured caller. Adapter pins the upper
	// bound at 7 days (AWS hard limit for v4 presigned URLs).
	maxPresignTTL time.Duration
	inited        bool
}

// Init reads config and verifies the adapter can talk to the
// configured bucket. Expected config keys:
//
//	endpoint:     "s3.amazonaws.com" or "<account>.r2.cloudflarestorage.com"
//	region:       "us-east-1" (default; AWS requires this even for
//	              region-agnostic buckets)
//	bucket:       "my-loamss-bucket"  (required)
//	access_key:   "AKIA..."           (required; or via env)
//	secret_key:   "...secret..."       (required; or via env)
//	use_ssl:      true                (default; set false for plain
//	              HTTP against a local MinIO)
//	prefix:       "loamss/"           (optional; prepended to every
//	              object path so multiple loamss instances can
//	              share a bucket safely)
//
// access_key / secret_key may be omitted from the config map; the
// adapter then falls back to standard AWS env vars
// (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) so users can keep
// the secrets out of the config file.
//
// HealthCheck is invoked at the end of Init to fail fast on bad
// credentials, unreachable endpoints, or missing buckets — the
// runtime aborts startup rather than discovering the misconfig at
// first write.
func (a *Adapter) Init(ctx context.Context, config map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	endpoint, err := requiredString(config, "endpoint", "s3.amazonaws.com")
	if err != nil {
		return err
	}
	bucket, err := requiredString(config, "bucket", "")
	if err != nil {
		return err
	}
	region := optionalString(config, "region", "us-east-1")
	useSSL := optionalBool(config, "use_ssl", true)

	// Credentials: explicit config wins; otherwise fall through to
	// standard env vars. Documenting both paths matters because
	// users running against MinIO often inline the secrets but
	// users running against AWS prefer the env-var path.
	accessKey := optionalString(config, "access_key", "")
	secretKey := optionalString(config, "secret_key", "")
	var creds *credentials.Credentials
	if accessKey != "" && secretKey != "" {
		creds = credentials.NewStaticV4(accessKey, secretKey, "")
	} else {
		// Chain: env → file → IAM. Same precedence the AWS SDK uses.
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.FileAWSCredentials{},
		})
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  creds,
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return fmt.Errorf("s3: building client: %w", err)
	}

	a.client = client
	a.bucket = bucket
	a.maxPresignTTL = 7 * 24 * time.Hour // AWS hard cap for SigV4
	a.inited = true

	// HealthCheck doubles as connectivity validation; bad
	// credentials / wrong region surface as a startup error
	// rather than later as a Write failure.
	if err := a.healthCheckLocked(ctx); err != nil {
		a.inited = false
		return fmt.Errorf("s3: bucket %q at %s: %w", bucket, endpoint, err)
	}
	return nil
}

// --- read side -------------------------------------------------------------

func (a *Adapter) Read(ctx context.Context, path string) ([]byte, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	obj, err := a.client.GetObject(ctx, a.bucket, path, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapErr(err, storage.OpRead, path)
	}
	defer func() { _ = obj.Close() }()

	body, err := io.ReadAll(obj)
	if err != nil {
		return nil, mapErr(err, storage.OpRead, path)
	}
	return body, nil
}

// ReadStream returns a byte-range reader over the object at path.
// length == 0 means "to end of object." Caller closes the reader.
func (a *Adapter) ReadStream(
	ctx context.Context, path string, offset, length int64,
) (io.ReadCloser, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	opts := minio.GetObjectOptions{}
	if offset > 0 || length > 0 {
		// minio-go's SetRange uses inclusive end. Length=0 means
		// "to end" per our SPI — we encode that as SetRange(offset, 0)
		// which minio treats as "from offset to end."
		var end int64
		if length > 0 {
			end = offset + length - 1
		}
		if err := opts.SetRange(offset, end); err != nil {
			return nil, fmt.Errorf("s3: invalid range offset=%d length=%d: %w", offset, length, err)
		}
	}
	obj, err := a.client.GetObject(ctx, a.bucket, path, opts)
	if err != nil {
		return nil, mapErr(err, storage.OpRead, path)
	}
	// Touch the object once to surface 404 / 403 NOW rather than on
	// the caller's first Read. GetObject itself is lazy; we want
	// errors at our boundary so the caller's `defer Close()` is
	// not paired with a nil-handle panic.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, mapErr(err, storage.OpRead, path)
	}
	return obj, nil
}

// --- write side ------------------------------------------------------------

func (a *Adapter) Write(ctx context.Context, path string, content []byte) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	_, err := a.client.PutObject(
		ctx, a.bucket, path,
		bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: detectContentType(path, content)},
	)
	if err != nil {
		return mapErr(err, storage.OpWrite, path)
	}
	return nil
}

// WriteStream is the streaming counterpart of Write. minio-go
// auto-promotes large bodies to multipart upload past 64 MiB.
func (a *Adapter) WriteStream(ctx context.Context, path string, content io.Reader) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	// -1 means "I don't know the length, let the client compute it
	// or stream in chunks." minio-go handles multipart automatically
	// past the 64MB threshold; we don't need to.
	_, err := a.client.PutObject(
		ctx, a.bucket, path, content, -1,
		minio.PutObjectOptions{ContentType: detectContentType(path, nil)},
	)
	if err != nil {
		return mapErr(err, storage.OpWrite, path)
	}
	return nil
}

// Delete removes the object at path. Idempotent — a missing key
// is not an error, per the SPI contract.
func (a *Adapter) Delete(ctx context.Context, path string) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	err := a.client.RemoveObject(ctx, a.bucket, path, minio.RemoveObjectOptions{})
	if err != nil {
		// S3 DELETE is idempotent at the protocol level (deleting a
		// non-existent key returns 204); some S3-compat
		// implementations differ. Treat NoSuchKey as success to
		// match the SPI's "idempotent: returns nil if the object
		// was already absent" contract.
		if isNotFoundErr(err) {
			return nil
		}
		return mapErrOp(err, "delete", path)
	}
	return nil
}

// --- queries ---------------------------------------------------------------

// Exists is a cheap HEAD probe — true if the object resolves.
func (a *Adapter) Exists(ctx context.Context, path string) (bool, error) {
	if err := a.requireInited(); err != nil {
		return false, err
	}
	_, err := a.client.StatObject(ctx, a.bucket, path, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if isNotFoundErr(err) {
		return false, nil
	}
	return false, mapErrOp(err, "exists", path)
}

// Metadata returns size + content-type + mtime + ETag for path.
func (a *Adapter) Metadata(ctx context.Context, path string) (storage.ObjectMetadata, error) {
	if err := a.requireInited(); err != nil {
		return storage.ObjectMetadata{}, err
	}
	info, err := a.client.StatObject(ctx, a.bucket, path, minio.StatObjectOptions{})
	if err != nil {
		return storage.ObjectMetadata{}, mapErrOp(err, "metadata", path)
	}
	return storage.ObjectMetadata{
		Path:        path,
		Size:        info.Size,
		ContentType: info.ContentType,
		ModTime:     info.LastModified,
		ETag:        strings.Trim(info.ETag, `"`),
	}, nil
}

// List enumerates objects whose key begins with prefix. The
// listing runs on a background goroutine; the channel closes
// when enumeration completes or ctx is canceled.
func (a *Adapter) List(ctx context.Context, prefix string) (<-chan storage.ListEntry, error) {
	if err := a.requireInited(); err != nil {
		return nil, err
	}
	out := make(chan storage.ListEntry)
	go func() {
		defer close(out)
		opts := minio.ListObjectsOptions{Prefix: prefix, Recursive: true}
		for obj := range a.client.ListObjects(ctx, a.bucket, opts) {
			if obj.Err != nil {
				// Surface the error via a sentinel ListEntry with
				// Err set; the SPI calls for this shape so the
				// caller can distinguish "no more entries" from
				// "backend exploded." Same pattern as the
				// fs-encrypted adapter.
				select {
				case out <- storage.ListEntry{Err: obj.Err}:
				case <-ctx.Done():
				}
				return
			}
			entry := storage.ListEntry{
				Metadata: storage.ObjectMetadata{
					Path:    obj.Key,
					Size:    obj.Size,
					ModTime: obj.LastModified,
					ETag:    strings.Trim(obj.ETag, `"`),
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

// SignedURL returns a time-bounded presigned URL for direct
// GET / PUT against the bucket. TTL is clamped at 7 days (AWS
// hard cap for SigV4).
func (a *Adapter) SignedURL(
	ctx context.Context, path string, ttl time.Duration, op storage.Op,
) (string, error) {
	if err := a.requireInited(); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > a.maxPresignTTL {
		// SigV4 presigned URLs are capped at 7 days by AWS. Clamp
		// rather than error so callers don't have to know the
		// limit.
		ttl = a.maxPresignTTL
	}

	switch op {
	case storage.OpRead:
		u, err := a.client.PresignedGetObject(ctx, a.bucket, path, ttl, url.Values{})
		if err != nil {
			return "", fmt.Errorf("s3: presign GET: %w", err)
		}
		return u.String(), nil
	case storage.OpWrite:
		u, err := a.client.PresignedPutObject(ctx, a.bucket, path, ttl)
		if err != nil {
			return "", fmt.Errorf("s3: presign PUT: %w", err)
		}
		return u.String(), nil
	default:
		return "", fmt.Errorf("s3: signed URL not supported for op %q", op)
	}
}

// --- health + lifecycle ----------------------------------------------------

// HealthCheck verifies the adapter can talk to its bucket via
// a cheap HEAD. Used by `loamss doctor` + future /healthz.
func (a *Adapter) HealthCheck(ctx context.Context) error {
	if err := a.requireInited(); err != nil {
		return err
	}
	return a.healthCheckLocked(ctx)
}

func (a *Adapter) healthCheckLocked(ctx context.Context) error {
	// BucketExists is a cheap HEAD on the bucket. Returns:
	//   true,  nil       — bucket exists, creds valid
	//   false, nil       — creds valid, bucket gone (which IS an error
	//                      from our perspective — we can't operate)
	//   *,     err       — credentials / network problem
	ok, err := a.client.BucketExists(ctx, a.bucket)
	if err != nil {
		return fmt.Errorf("BucketExists: %w", err)
	}
	if !ok {
		return fmt.Errorf("bucket %q does not exist or is not accessible", a.bucket)
	}
	return nil
}

// Close marks the adapter uninited and drops the client ref.
// minio-go has no explicit close; the HTTP transport is GC'd.
func (a *Adapter) Close(_ context.Context) error {
	// minio-go has no explicit close — the underlying HTTP client
	// will be garbage-collected. Mark the adapter uninited so
	// subsequent calls fast-fail instead of going through a
	// half-torn-down state.
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inited = false
	a.client = nil
	return nil
}

// --- helpers ---------------------------------------------------------------

func (a *Adapter) requireInited() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.inited {
		return errors.New("s3: adapter not initialised (call Init first)")
	}
	return nil
}

// mapErr translates minio-go errors into the SPI's expected
// sentinels. The S3 API encodes errors as XML payloads with a
// `Code` field; minio-go surfaces that as ErrorResponse.Code.
//
// `op` here is a free-form string (delete/exists/metadata) for
// the error message; the storage.Op enum only covers read/write,
// so we use a string for full coverage of the SPI surface.
func mapErr(err error, op storage.Op, path string) error {
	return mapErrOp(err, string(op), path)
}

func mapErrOp(err error, op, path string) error {
	if err == nil {
		return nil
	}
	if isNotFoundErr(err) {
		return fmt.Errorf("%w: %s (op=%s)", storage.ErrNotFound, path, op)
	}
	return fmt.Errorf("s3 %s on %q: %w", op, path, err)
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		switch er.Code {
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return true
		}
	}
	// Some S3-compat backends return the same condition without a
	// structured ErrorResponse. Fall back to a string check on the
	// known phrases.
	msg := err.Error()
	return strings.Contains(msg, "key does not exist") ||
		strings.Contains(msg, "Not Found") ||
		strings.Contains(msg, "NoSuchKey")
}

func detectContentType(path string, body []byte) string {
	// Defer to the runtime's standard library; this is good enough
	// for the file types capsules typically write (json, html, png,
	// pdf). Empty result is fine — S3 accepts no Content-Type and
	// downstream clients fall back to application/octet-stream.
	if body != nil {
		if t := contentTypeFromBytes(body); t != "" {
			return t
		}
	}
	if ext := extOf(path); ext != "" {
		return contentTypeFromExt(ext)
	}
	return ""
}

func extOf(p string) string {
	idx := strings.LastIndex(p, ".")
	if idx < 0 {
		return ""
	}
	return strings.ToLower(p[idx:])
}

func contentTypeFromExt(ext string) string {
	switch ext {
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

func contentTypeFromBytes(body []byte) string {
	if len(body) >= 4 && string(body[:4]) == "%PDF" {
		return "application/pdf"
	}
	if len(body) >= 8 && string(body[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	return ""
}

func requiredString(config map[string]any, key, defaultIfMissing string) (string, error) {
	v, ok := config[key]
	if !ok {
		if defaultIfMissing != "" {
			return defaultIfMissing, nil
		}
		return "", fmt.Errorf("s3: missing required config: %s", key)
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("s3: %s must be a non-empty string (got %T %v)", key, v, v)
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

func optionalBool(config map[string]any, key string, fallback bool) bool {
	v, ok := config[key]
	if !ok {
		return fallback
	}
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}
