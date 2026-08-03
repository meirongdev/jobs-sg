package web

import (
	"sync"
	"time"
)

// pageCache is a tiny TTL cache for rendered pages.
//
// The daily pages are recomputed from SQL on every request and the site is
// public with no auth, so a crawler walking /daily/{date} would otherwise pin
// the pod's CPU. Cache-Control alone does not help: nothing in front of the
// pod caches. Entries are pure derived data — dropping them on restart costs
// one recompute, so this keeps the "state lives in the DB" rule intact.
type pageCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
	max     int
}

type cacheEntry struct {
	html    string
	expires time.Time
}

func newPageCache(ttl time.Duration, max int) *pageCache {
	return &pageCache{entries: make(map[string]cacheEntry), ttl: ttl, max: max}
}

// get returns a cached page that has not expired as of now.
func (c *pageCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.After(e.expires) {
		return "", false
	}
	return e.html, true
}

// put stores a page. At capacity the whole map is dropped rather than evicted
// one by one: entries are cheap to rebuild and this bounds memory with no
// bookkeeping (the web pod's budget is 64Mi).
func (c *pageCache) put(key, html string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		clear(c.entries)
	}
	c.entries[key] = cacheEntry{html: html, expires: now.Add(c.ttl)}
}
