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
	flight  map[string]*pageBuild
	ttl     time.Duration
	max     int
}

type cacheEntry struct {
	html    string
	expires time.Time
}

// pageBuild is one in-progress render that later arrivals wait on instead of
// starting their own.
type pageBuild struct {
	done chan struct{}
	html string
	err  error
}

func newPageCache(ttl time.Duration, max int) *pageCache {
	return &pageCache{
		entries: make(map[string]cacheEntry),
		flight:  make(map[string]*pageBuild),
		ttl:     ttl,
		max:     max,
	}
}

// do returns the cached page, or builds it — and while one build is running,
// every other request for the same key waits for it rather than starting a
// second.
//
// The TTL alone does not bound concurrent work: it collapses requests that
// arrive after a build finishes, not during it. A crawler hitting /tech across
// the 55 possible lens combinations on a cold cache had every request start its
// own render, each ~45 queries, against a read pool of 4 connections and a 200m
// CPU limit — so the requests that made it through were also the ones most
// likely to hit the 5s handler timeout.
//
// Waiters share the leader's outcome, including its failure. A leader whose
// client disconnected mid-build fails its waiters too; they see a 500 and a
// retry succeeds. That is worth the collapse, and the alternative — detaching
// the build from the requesting context — trades it for renders nobody is
// waiting for any more.
func (c *pageCache) do(key string, now time.Time, build func() (string, error)) (string, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && !now.After(e.expires) {
		c.mu.Unlock()
		return e.html, nil
	}
	if b, ok := c.flight[key]; ok {
		c.mu.Unlock()
		<-b.done
		return b.html, b.err
	}
	b := &pageBuild{done: make(chan struct{})}
	c.flight[key] = b
	c.mu.Unlock()

	b.html, b.err = build()

	c.mu.Lock()
	delete(c.flight, key)
	if b.err == nil {
		c.putLocked(key, b.html, now)
	}
	c.mu.Unlock()
	close(b.done) // after the fields are set: waiters read them on this signal

	return b.html, b.err
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
	c.putLocked(key, html, now)
}

// putLocked is put's body for callers already holding the lock.
func (c *pageCache) putLocked(key, html string, now time.Time) {
	if len(c.entries) >= c.max {
		clear(c.entries)
	}
	c.entries[key] = cacheEntry{html: html, expires: now.Add(c.ttl)}
}
