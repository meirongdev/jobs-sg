package web

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The TTL collapses requests arriving after a build finishes, not during it.
// On a cold cache every concurrent request used to start its own render — ~45
// queries each for /tech, against a 4-connection read pool and a 200m CPU
// limit — so a crawler burst was the situation most likely to blow the 5s
// handler timeout, and it did it to itself.
func TestCacheBuildsOnceUnderConcurrentMisses(t *testing.T) {
	c := newPageCache(time.Minute, 8)
	now := time.Now()

	var builds atomic.Int32
	release := make(chan struct{})
	build := func() (string, error) {
		builds.Add(1)
		<-release // hold the leader inside the build so the others pile up
		return "page", nil
	}

	const callers = 20
	var wg sync.WaitGroup
	results := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.do("tech:exp=;role=", now, build)
		}()
	}
	// let every caller reach do() before the leader is allowed to finish
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := builds.Load(); n != 1 {
		t.Errorf("built %d times for one key, want 1", n)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i] != "page" {
			t.Errorf("caller %d got %q, want the leader's page", i, results[i])
		}
	}
}

// Different keys must not serialise behind each other — the lens combinations
// are exactly what makes the key space wide.
func TestCacheDoesNotSerialiseDistinctKeys(t *testing.T) {
	c := newPageCache(time.Minute, 8)
	now := time.Now()

	var inFlight atomic.Int32
	var maxParallel atomic.Int32
	start := make(chan struct{})
	build := func() (string, error) {
		n := inFlight.Add(1)
		for {
			m := maxParallel.Load()
			if n <= m || maxParallel.CompareAndSwap(m, n) {
				break
			}
		}
		<-start
		inFlight.Add(-1)
		return "page", nil
	}

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.do(string(rune('a'+i)), now, build)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(start)
	wg.Wait()

	if maxParallel.Load() < 2 {
		t.Errorf("max concurrent builds = %d — distinct keys should not block each other", maxParallel.Load())
	}
}

// A failed build must not be cached, and must not leave the key wedged so the
// next request waits forever on a channel nobody will close.
func TestCacheRetriesAfterAFailedBuild(t *testing.T) {
	c := newPageCache(time.Minute, 8)
	now := time.Now()
	boom := errors.New("db exploded")

	if _, err := c.do("k", now, func() (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the build's error", err)
	}
	if _, ok := c.get("k", now); ok {
		t.Error("a failed build must not be cached")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		html, err := c.do("k", now, func() (string, error) { return "recovered", nil })
		if err != nil || html != "recovered" {
			t.Errorf("retry = %q, %v", html, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry after a failed build blocked — the in-flight entry was not cleared")
	}
}

// Waiters share the leader's failure. Pinned deliberately: it is the accepted
// cost of collapsing the herd, and worth noticing if it ever changes.
func TestCacheWaitersShareTheLeadersError(t *testing.T) {
	c := newPageCache(time.Minute, 8)
	now := time.Now()
	boom := errors.New("leader failed")
	release := make(chan struct{})

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.do("k", now, func() (string, error) {
				<-release
				return "", boom
			})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, boom) {
			t.Errorf("caller %d err = %v, want the leader's error", i, err)
		}
	}
}
