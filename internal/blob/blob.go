// Package blob defines the storage port of filekeeper and its implementations.
//
// The interface is deliberately tiny (Shrine-sized): the service above it owns
// naming, dedup, metadata and access control; a Store only moves bytes.
package blob

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Get/Delete when the key does not exist.
var ErrNotFound = errors.New("blob: not found")

// Object is the result of a Get. Body must be closed by the caller.
type Object struct {
	Body io.ReadCloser
	// Size is the number of bytes in Body (the full object, or the
	// requested range when ContentRange is set).
	Size int64
	// ContentRange is non-empty when Body is a partial response,
	// e.g. "bytes 0-1023/146515".
	ContentRange string
}

// Store moves bytes to and from a backing storage.
type Store interface {
	// Put stores the full content of r under key. size is the exact
	// content length; contentType is advisory metadata.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get retrieves the object. rangeHeader is an optional HTTP Range
	// header value ("bytes=0-1023"); pass "" for the whole object.
	Get(ctx context.Context, key string, rangeHeader string) (*Object, error)
	// Exists reports whether key is present.
	Exists(ctx context.Context, key string) (bool, error)
	// Delete removes the object. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
}
