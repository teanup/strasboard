// Package source defines the contract between external data sources and the
// background jobs that refresh them.
package source

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by ItemRefresher implementations when the requested
// item does not exist upstream. The HTTP layer maps it to 404.
var ErrNotFound = errors.New("not found")

// Resource is a named, refreshable collection of items.
//
// Items are keyed by a string ID. Single-document resources (such as the
// weather forecast) return a single item with a fixed ID such as "current".
type Resource interface {
	// Name is the resource key used in URLs and the store, e.g. "weather".
	Name() string

	// NextRefresh returns when the resource next needs to be refreshed. The
	// scheduler sleeps until then, so a source can avoid polling when its data
	// cannot change (e.g. electricity data only arrives once a day).
	NextRefresh(now time.Time) time.Time

	// Refresh fetches data from the external source and returns the items to
	// publish, keyed by item ID. It must not be called concurrently with
	// itself. Returning an error leaves the previously stored items in place
	// (degraded mode).
	Refresh(ctx context.Context) (map[string]any, error)
}

// ItemRefresher is implemented by resources that can re-fetch a single item on
// demand, bypassing the regular schedule. It powers
// POST /api/v1/{resource}/{id}/refresh.
//
// The returned value is the refreshed item payload, which the caller stores
// under the same ID. Returning an error leaves the stored item untouched.
type ItemRefresher interface {
	RefreshItem(ctx context.Context, id string) (any, error)
}
