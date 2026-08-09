package mcf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrCircuitOpen is returned when pagination exceeds MAX_PAGES. The design
// treats this as a partial run (never silent data loss on backlog peaks),
// docs/02 §4.1.
var ErrCircuitOpen = errors.New("mcf: page limit exceeded (circuit open)")

// DefaultUserAgent declares identity transparently per the compliance red
// line (docs/01 §5): no browser impersonation.
const DefaultUserAgent = "jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)"

// Summary describes one paginated sweep for ingest_run auditing.
//
// Total is the API-reported total as of the *last* page walked, not the largest
// seen along the way. A full sweep takes ~25 minutes and MCF's total moves under
// it as postings appear and expire; latching the maximum made any transient
// spike permanent, because Jobs is a sum accumulated across the whole window
// while Total was a high-water mark. Comparing a peak against a sum biases every
// figure derived from the pair upward by construction — on 2026-08-09 that read
// as a 4.5% deviation (85487 seen vs 89531 "total") on a sweep that had in fact
// walked the board to its last page, and the reconcile skipped its close pass
// over it. The freshest reading is the one that describes the board the close
// pass is about to act on. MinTotal/MaxTotal are kept so the spread across the
// sweep stays visible afterwards instead of having to be reconstructed from logs.
type Summary struct {
	Pages    int
	Jobs     int
	Total    int // API-reported total as of the last page walked
	MinTotal int
	MaxTotal int
}

// Client is a thin, rate-limited, retrying MCF API client.
type Client struct {
	baseURL  string
	ua       string
	hc       *http.Client
	limit    int
	maxPages int
	delay    time.Duration
	// RetryDelays are the exponential backoff waits after a 429/5xx.
	// Default [2,4,8]s per design; tests override with tiny values.
	RetryDelays []time.Duration
}

// NewClient builds a client. baseURL is e.g. https://api.mycareersfuture.gov.sg/v2.
func NewClient(baseURL, ua string, limit, maxPages int, delay time.Duration) *Client {
	return NewClientWithRT(baseURL, ua, limit, maxPages, delay, http.DefaultTransport)
}

// NewClientWithRT is NewClient with an injectable transport (used by tests;
// the sandbox forbids binding listeners, so we avoid httptest servers).
func NewClientWithRT(baseURL, ua string, limit, maxPages int, delay time.Duration, rt http.RoundTripper) *Client {
	if ua == "" {
		ua = DefaultUserAgent
	}
	return &Client{
		baseURL:     baseURL,
		ua:          ua,
		hc:          &http.Client{Timeout: 60 * time.Second, Transport: rt},
		limit:       limit,
		maxPages:    maxPages,
		delay:       delay,
		RetryDelays: []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second},
	}
}

// FetchPage retrieves one page of /v2/jobs sorted by newest posting date,
// retrying transient failures (429/5xx) with exponential backoff.
func (c *Client) FetchPage(ctx context.Context, page int) (*Page, error) {
	u := fmt.Sprintf("%s/jobs?limit=%d&page=%d&sortBy=new_posting_date", c.baseURL, c.limit, page)
	var lastErr error
	attempts := 1 + len(c.RetryDelays)
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-time.After(c.RetryDelays[i-1]):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.ua)
		req.Header.Set("Accept", "application/json")
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusOK {
			var pg Page
			if err := json.Unmarshal(body, &pg); err != nil {
				return nil, fmt.Errorf("mcf: decode page %d: %w", page, err)
			}
			return &pg, nil
		}
		lastErr = fmt.Errorf("mcf: page %d http %d: %s", page, resp.StatusCode, truncate(body, 200))
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, lastErr // non-transient
		}
	}
	return nil, fmt.Errorf("mcf: page %d failed after retries: %w", page, lastErr)
}

// EachPage walks pages 0..N until the callback returns stop=true, an empty
// page, the circuit breaker trips, or an error. It rate-limits (delay between
// requests) and applies the page-limit circuit breaker from docs/02 §4.1.
// The callback receives the raw page results and the API-reported total.
func (c *Client) EachPage(ctx context.Context, fn func(jobs []Job, total int) (bool, error)) (Summary, error) {
	var s Summary
	page := 0
	for {
		p, err := c.FetchPage(ctx, page)
		if err != nil {
			return s, err
		}
		s.Pages++
		// A zero never overwrites a real reading. The terminating empty page
		// does carry the count in practice (verified against the live API), but
		// a spurious 0 there would otherwise zero out Total and silently blind
		// the reconcile's deviation gate on the one page it depends on.
		if p.Total > 0 {
			if s.MinTotal == 0 || p.Total < s.MinTotal {
				s.MinTotal = p.Total
			}
			if p.Total > s.MaxTotal {
				s.MaxTotal = p.Total
			}
			s.Total = p.Total
		}
		stop, err := fn(p.Results, p.Total)
		if err != nil {
			return s, err
		}
		s.Jobs += len(p.Results)
		if stop || len(p.Results) == 0 {
			return s, nil
		}
		if page+1 >= c.maxPages {
			return s, ErrCircuitOpen
		}
		page++
		if c.delay > 0 {
			select {
			case <-time.After(c.delay):
			case <-ctx.Done():
				return s, ctx.Err()
			}
		}
	}
}

// EachJob walks pages 0..N invoking fn per job; a convenience wrapper around
// EachPage.
func (c *Client) EachJob(ctx context.Context, fn func(Job) error) (Summary, error) {
	return c.EachPage(ctx, func(jobs []Job, _ int) (bool, error) {
		for _, j := range jobs {
			if err := fn(j); err != nil {
				return true, err
			}
		}
		return false, nil
	})
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
