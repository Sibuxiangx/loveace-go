// Package cache implements the read-through cache with single-flight,
// stale-if-error fallback and stale-while-revalidate (DESIGN.md §11.3):
// school outages serve the last good value flagged stale=true instead of
// failing, and an expired value is served IMMEDIATELY with the refresh
// running in the background (2026-10-17: the first screen after a cold
// session paid ~2-5s of gateway round trips per endpoint — now it paints
// from cache while the session revalidates).
//
// Keys must be user-scoped (they always include the user id); nothing is
// ever shared between users.
package cache

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// revalidateTimeout bounds one detached background refresh.
const revalidateTimeout = 90 * time.Second

type entry struct {
	value     any
	fetchedAt time.Time
}

// Entry is one cache lookup result.
type Entry struct {
	Value     any
	FetchedAt time.Time
	// Source is "live" when the fetch ran and "cache" when served from cache.
	Source string
	// Stale marks a stale-if-error fallback served after a fetch failure.
	Stale bool
	// Revalidating marks an expired value served immediately while the
	// background refresh runs (stale-while-revalidate).
	Revalidating bool
}

// Cache is a safe-for-concurrency read-through cache.
type Cache struct {
	mu    sync.RWMutex
	items map[string]entry
	good  map[string]entry // last good value for stale-if-error
	sf    singleflight.Group
	now   func() time.Time
}

// New creates an empty cache.
func New() *Cache {
	return &Cache{
		items: map[string]entry{},
		good:  map[string]entry{},
		now:   time.Now,
	}
}

// GetOrLoad returns the cached value while fresh, otherwise fetches under
// single-flight (concurrent callers for the same key share one fetch).
// On fetch failure the last good value is returned with Stale=true.
func (c *Cache) GetOrLoad(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) (any, error)) (Entry, error) {
	if e, ok := c.fresh(key, ttl); ok {
		return e, nil
	}
	return c.load(ctx, key, fetch)
}

// GetOrLoadSWR is GetOrLoad with stale-while-revalidate: an EXPIRED value
// is served immediately (Source "cache", Revalidating) while the fetch
// runs detached in the background, so a user returning after a cold
// session sees the first screen without waiting on gateway round trips.
// A key with no stored value still blocks (nothing to serve).
//
// Read paths only: mutation preconditions must keep using GetOrLoad (or
// no cache at all).
func (c *Cache) GetOrLoadSWR(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) (any, error)) (Entry, error) {
	if e, ok := c.fresh(key, ttl); ok {
		return e, nil
	}
	if g, ok := c.lastGood(key); ok {
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), revalidateTimeout)
			defer cancel()
			_, _ = c.load(bg, key, fetch)
		}()
		return Entry{Value: g.value, FetchedAt: g.fetchedAt, Source: "cache", Revalidating: true}, nil
	}
	return c.load(ctx, key, fetch)
}

// load runs the fetch under the key's single-flight (foreground and
// background refreshes for one key share one fetch) with the
// stale-if-error fallback.
func (c *Cache) load(ctx context.Context, key string, fetch func(context.Context) (any, error)) (Entry, error) {
	v, err, _ := c.sf.Do(key, func() (any, error) {
		value, ferr := fetch(ctx)
		if ferr != nil {
			if g, ok := c.lastGood(key); ok {
				return Entry{Value: g.value, FetchedAt: g.fetchedAt, Source: "cache", Stale: true}, nil
			}
			return nil, ferr
		}
		e := Entry{Value: value, FetchedAt: c.now(), Source: "live"}
		c.store(key, entry{value: value, fetchedAt: e.FetchedAt})
		return e, nil
	})
	if err != nil {
		return Entry{}, err
	}
	return v.(Entry), nil
}

func (c *Cache) fresh(key string, ttl time.Duration) (Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok || c.now().Sub(e.fetchedAt) > ttl {
		return Entry{}, false
	}
	return Entry{Value: e.value, FetchedAt: e.fetchedAt, Source: "cache"}, true
}

func (c *Cache) lastGood(key string) (entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.good[key]
	return e, ok
}

func (c *Cache) store(key string, e entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = e
	c.good[key] = e
}
