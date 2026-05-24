package storage

import "time"

// ObjectMetadata describes a stored object without returning its
// content. Returned by the Metadata operation and embedded in
// ListEntry.
type ObjectMetadata struct {
	// Path is the object's address inside the storage namespace.
	Path string

	// Size is the object's content length in bytes.
	Size int64

	// ModTime is the last modification time of the object.
	ModTime time.Time

	// ContentType is a best-effort MIME guess (e.g., "video/mp4",
	// "application/json"). Adapters that don't have a notion of
	// content type may leave this empty.
	ContentType string

	// ETag, if set, is an opaque version identifier the backend
	// returned. Useful for caches and conditional fetches.
	ETag string

	// Custom holds backend-specific fields that the runtime doesn't
	// need but a capsule or operator might want (e.g., S3 storage
	// class, Postgres row id). Adapters populate at their discretion.
	Custom map[string]string
}

// ListEntry is a single result yielded by List. Either Metadata is
// populated (with a successful entry) or Err is non-nil (with a
// per-entry error that didn't terminate the listing).
//
// A listing terminates either by closing the channel cleanly (after
// the prefix is fully enumerated) or via context cancellation. A
// terminating error is sent as a final ListEntry with Err set, then
// the channel is closed.
type ListEntry struct {
	Metadata ObjectMetadata
	Err      error
}
