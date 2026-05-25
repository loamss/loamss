# storage:s3 — S3-compatible storage backend

Phase 2 production backend. Works with AWS S3 and every major
S3-compatible service: Cloudflare R2, Backblaze B2, MinIO,
Wasabi, IDrive E2, OVHcloud Object Storage, etc.

Use this instead of `storage:fs-encrypted` when:

- You want capsule payloads + raw source data living in cloud
  object storage rather than on a local disk.
- You want signed URLs the runtime can hand out for direct
  content streaming (the `content.video` / creator-publishing
  pattern in [`scenarios.md`](../scenarios.md) §5–§6).
- You're running the runtime on a host where disk persistence
  isn't a given (containers, ephemeral compute).

## Config

In your `~/.loamss/config.yaml`:

```yaml
storage:
  adapter: storage:s3
  config:
    endpoint: s3.amazonaws.com       # or "<account>.r2.cloudflarestorage.com",
                                      # or your MinIO host:port
    region: us-east-1                 # AWS region; many S3-compats accept any value
    bucket: my-loamss-bucket          # required; must already exist
    use_ssl: true                     # default; set false for plain HTTP (local MinIO)
    # Credentials — either inline OR from the standard AWS env
    # vars (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY). Inline is
    # convenient for development; env vars are recommended for
    # production so the secrets never land on disk.
    access_key: AKIA...               # optional if env is set
    secret_key: ...                   # optional if env is set
```

Init validates the connection up front via a `HeadBucket` probe;
bad credentials or a missing bucket fail at `loamss start` time
rather than at the first write.

## Encryption

This adapter does **not** encrypt at rest. S3 buckets have several
encryption options the operator picks at the bucket level:

- **SSE-S3** (AES-256 with AWS-managed keys, free, default for
  most buckets created in 2023+).
- **SSE-KMS** (envelope encryption with a customer-managed KMS key,
  per-request audit trail in CloudTrail).
- **DSSE-KMS** (double-layer KMS for compliance scenarios).
- **SSE-C** (customer-provided key passed per-request) — not
  recommended; the runtime doesn't manage SSE-C keys today.

For the "I want at-rest encryption but my backend doesn't do it
for me" case, use `storage:fs-encrypted` (local FS with
AES-256-GCM) or wait for the planned `storage:s3-encrypted` wrapper
that does client-side envelope encryption on top of any S3.

## S3-compatible services

The minio-go client this adapter uses speaks the AWS S3 API.
Tested against:

| Service | Endpoint shape | Notes |
|---|---|---|
| AWS S3 | `s3.amazonaws.com` | region matters; cap presign at 7d |
| Cloudflare R2 | `<account>.r2.cloudflarestorage.com` | no egress fees; presign works |
| Backblaze B2 | `s3.<region>.backblazeb2.com` | application-key auth |
| MinIO | `<your-host>:9000` | self-hosted; set `use_ssl: false` for plain HTTP |
| Wasabi | `s3.<region>.wasabisys.com` | works |
| IDrive E2 | `<region>.idrivee2.com` | works |

A service not listed above will probably also work — the S3 API is
well-standardised. File an issue if you hit something specific.

## Capabilities supported

| SPI method | Status | Notes |
|---|---|---|
| Init / Close / HealthCheck | ✓ | `HeadBucket` for the live probe |
| Read / ReadStream | ✓ | byte-range supported via `Range:` header |
| Write / WriteStream | ✓ | minio-go auto-promotes to multipart past 64 MiB |
| Delete | ✓ | idempotent — missing key is not an error |
| Exists / Metadata | ✓ | `HeadObject` |
| List | ✓ | `ListObjectsV2` with continuation |
| SignedURL | ✓ | `PresignedGetObject` / `PresignedPutObject`; capped at 7d (AWS SigV4 limit) |

## Operational notes

- **Bucket must exist.** The adapter doesn't create it on Init.
  This is deliberate: bucket lifecycle (region, retention, ACLs)
  belongs to the operator, not the runtime.
- **Region matters for AWS** even when the bucket is in a
  "global" namespace. If you see `PermanentRedirect` errors,
  the configured region doesn't match the bucket's actual region.
- **R2 + B2** generally don't care about region; pick any string.
- **Listing is recursive** by default. The adapter doesn't expose
  a "shallow" mode today; if a use case shows up we can add it.

## Future work

- `storage:s3-encrypted` — client-side AES-256-GCM wrapper that
  composes with any S3-compat backend. The fs-encrypted adapter's
  on-disk format works as the basis.
- Dashboard wizard integration: today users edit YAML directly.
  The wizard's "cloud storage" tile is reserved for this.
- Per-object SSE-KMS key selection via the `Custom` metadata
  field; useful for multi-tenant compliance scenarios.

## See also

- [`adapter-interface.md`](../adapter-interface.md) — the full
  storage SPI contract.
- [`internal/adapter/storage/s3/s3.go`](../runtime/internal/adapter/storage/s3/s3.go)
  — the implementation.
- [`scenarios.md`](../scenarios.md) §5–§6 — the
  creator-publishing flows that motivate signed-URL support.
