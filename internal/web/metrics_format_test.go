package web

import (
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// Every sample must be preceded by a HELP and a TYPE for its family, each
// declared exactly once. Without types Prometheus stores everything untyped,
// and a reader cannot tell a cumulative counter from a level that moves both
// ways — which is the difference between rate() meaning something and not.
func TestMetricsDeclareHelpAndTypeExactlyOnce(t *testing.T) {
	s := setupWeb(t)
	body := getFrom(t, s.MetricsHandler(), "/metrics").Body.String()

	help := map[string]int{}
	typ := map[string]int{}
	declared := map[string]bool{}
	sample := regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)(\{.*\})? `)

	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name := strings.Fields(line)[2]
			help[name]++
			declared[name] = true
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			typ[f[2]]++
			if got := f[3]; got != "gauge" && got != "counter" {
				t.Errorf("%s declared as %q, want gauge or counter", f[2], got)
			}
		case line == "":
		default:
			m := sample.FindStringSubmatch(line)
			if m == nil {
				t.Errorf("unparseable exposition line: %q", line)
				continue
			}
			if !declared[m[1]] {
				t.Errorf("sample %q appears before its HELP/TYPE header", m[1])
			}
		}
	}
	if len(help) == 0 {
		t.Fatal("no metric families rendered")
	}
	for name, n := range help {
		if n != 1 {
			t.Errorf("%s has %d HELP lines, want 1", name, n)
		}
		if typ[name] != 1 {
			t.Errorf("%s has %d TYPE lines, want 1", name, typ[name])
		}
	}
}

// A week in a label value mints one series per week and retires none. The
// series set then grows forever, which is the textbook way to bloat a
// Prometheus instance.
func TestMetricsCarryNoUnboundedLabel(t *testing.T) {
	s := setupWeb(t)
	body := getFrom(t, s.MetricsHandler(), "/metrics").Body.String()
	if strings.Contains(body, "week=") {
		t.Errorf("a week label is unbounded cardinality:\n%s", body)
	}
	// the labels that remain are all closed sets
	for _, allowed := range regexp.MustCompile(`(\w+)="`).FindAllStringSubmatch(body, -1) {
		switch allowed[1] {
		case "kind", "state":
		default:
			t.Errorf("unexpected label %q — is its value set bounded?", allowed[1])
		}
	}
}

// Reporting 0 because a query failed is worse than reporting nothing: an active
// count of 0 reads as a market that emptied overnight, and every alert resting
// on it fires on a lie.
//
// The job-count queries used to discard their error with `_ =`. Reaching that
// specific path means failing them while the earlier ingest_run queries still
// succeed — a whole-DB outage 500s on the first query either way and proves
// nothing. Dropping just the job table isolates it, standing in for the real
// cases: a mid-scrape context deadline or an IO error on one table.
func TestMetricsFailLoudlyWhenOnlyTheJobTableIsUnreadable(t *testing.T) {
	s, dir := setupWebClock(t, nil)

	w, err := store.Open(filepath.Join(dir, "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExecContext(context.Background(), `DROP TABLE job`); err != nil {
		t.Fatal(err)
	}
	w.Close()

	rec := getFrom(t, s.MetricsHandler(), "/metrics")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500 so Prometheus marks the target down", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `jobs_sg_jobs_total{state="active"} 0`) {
		t.Error("reported 0 active postings from a failed query — indistinguishable from an empty market")
	}
	if strings.Contains(body, "jobs_sg_jobs_total") {
		t.Error("emitted job counts despite the job table being unreadable")
	}
}

// Absent data is absent, not zero: before the first report run there is no
// week to report, and inventing 0 new jobs would be a fabricated number.
func TestMetricsOmitNewJobsBeforeAnyReport(t *testing.T) {
	s := setupWeb(t) // setup never materialises weekly_metric
	body := getFrom(t, s.MetricsHandler(), "/metrics").Body.String()
	if strings.Contains(body, "jobs_sg_jobs_new") {
		t.Errorf("reported a weekly figure before any report ran:\n%s", body)
	}
	// but the endpoint still works
	if !strings.Contains(body, "jobs_sg_jobs_total") {
		t.Error("the rest of the exposition should still render")
	}
}
