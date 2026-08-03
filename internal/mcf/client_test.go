package mcf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(b []byte, status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(b))),
	}
}

func testPageBytes(jobs ...Job) []byte {
	p := Page{Results: jobs, Total: len(jobs)}
	b, _ := json.Marshal(p)
	return b
}

func fakeJob(uuid, date string) Job {
	return Job{UUID: uuid, Title: "Software Engineer", Metadata: Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: date}}
}

func TestFetchPageSendsUAAndQuery(t *testing.T) {
	var gotUA, gotQuery string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.RawQuery
		return jsonResp(testPageBytes(fakeJob("a", "2026-08-01T00:00:00Z")), 200), nil
	})
	c := NewClientWithRT("https://api.test/v2", "jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)", 100, 10, 0, rt)
	page, err := c.FetchPage(context.Background(), 0)
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if gotUA != "jobs-sg-monitor/1.0 (+https://jobs.meirong.dev)" {
		t.Errorf("UA = %q", gotUA)
	}
	if gotQuery != "limit=100&page=0&sortBy=new_posting_date" {
		t.Errorf("query = %q", gotQuery)
	}
	if len(page.Results) != 1 {
		t.Errorf("results = %d, want 1", len(page.Results))
	}
}

func TestFetchPageRetries429WithBackoff(t *testing.T) {
	var attempts int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			return jsonResp(nil, http.StatusTooManyRequests), nil
		}
		return jsonResp(testPageBytes(fakeJob("a", "2026-08-01T00:00:00Z")), 200), nil
	})
	c := NewClientWithRT("https://api.test/v2", "ua", 100, 10, 0, rt)
	c.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	if _, err := c.FetchPage(context.Background(), 0); err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestFetchPageGivesUpAfterRetries(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(nil, http.StatusInternalServerError), nil
	})
	c := NewClientWithRT("https://api.test/v2", "ua", 100, 10, 0, rt)
	c.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	if _, err := c.FetchPage(context.Background(), 0); err == nil {
		t.Fatal("expected error after retries exhausted")
	}
}

func TestEachJobCircuitBreaker(t *testing.T) {
	var pages int32
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&pages, 1)
		return jsonResp(testPageBytes(fakeJob("a", "2026-08-01T00:00:00Z")), 200), nil
	})
	c := NewClientWithRT("https://api.test/v2", "ua", 100, 2, 0, rt) // maxPages=2
	summary, err := c.EachJob(context.Background(), func(j Job) error { return nil })
	if err != ErrCircuitOpen {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if summary.Pages != 2 {
		t.Errorf("pages = %d, want 2", summary.Pages)
	}
	if got := atomic.LoadInt32(&pages); got != 2 {
		t.Errorf("server pages = %d, want 2 (maxPages)", got)
	}
}

func TestEachJobStopsAtEmptyPage(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(testPageBytes(), 200), nil
	})
	c := NewClientWithRT("https://api.test/v2", "ua", 100, 10, 0, rt)
	summary, err := c.EachJob(context.Background(), func(j Job) error { return nil })
	if err != nil {
		t.Fatalf("EachJob: %v", err)
	}
	if summary.Pages != 1 || summary.Jobs != 0 {
		t.Errorf("summary = %+v, want 1 page 0 jobs", summary)
	}
}
