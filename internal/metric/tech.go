package metric

import (
	"context"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// RankedTechLimit is how many technologies the demand table shows.
const RankedTechLimit = 30

// TechStat is one technology's row on /tech.
type TechStat struct {
	Slug  string
	Kind  string
	Count int     // postings in the reported week mentioning it
	Share float64 // Count / TechReport.Denom

	MomentumPP float64  // Share − mean(previous 4 weeks' share), as a fraction
	Momentum   Coverage // suppressed when the week is thin or history is short

	PremiumPct float64  // median(with it) / median(all) − 1
	Premium    Coverage // suppressed below MinSalarySamplesPerTech

	EntryFriendly float64 // share of postings mentioning it that are entry-level
}

// TechReport is the /tech page model.
type TechReport struct {
	Week        string     // reported ISO week, e.g. "2026-W32"
	Denom       int        // enriched SWE postings in that week (the share denominator)
	Ranked      []TechStat // by Count desc, capped at RankedTechLimit
	Rising      []TechStat // by MomentumPP desc, unsuppressed only
	Falling     []TechStat // by MomentumPP asc, unsuppressed only
	MedianAll   float64    // rolling-90d median monthly salary, the premium baseline
	SalaryN     int        // disclosed monthly salaries behind MedianAll
	SalaryTotal int        // every SWE posting in the same window — the transparency denominator
	History     Coverage   // how many of the 5 momentum windows had data
	Lens        Lens
}

// swePosted is the shared predicate: SWE postings whose posting_date falls in
// the window. Callers append Lens.Where(), which qualifies columns with `j.`.
const swePosted = `FROM job j WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`

// enrichedPredicate marks a posting the enrichment pipeline has finished with.
// enrich_done matters on its own: a posting whose extraction found nothing has
// no job_tech rows but is not backlog (see internal/store/schema.go).
const enrichedPredicate = `AND (EXISTS(SELECT 1 FROM job_tech t WHERE t.job_uuid=j.uuid)
	 OR EXISTS(SELECT 1 FROM enrich_done e WHERE e.job_uuid=j.uuid))`

// TechReportFor builds the /tech model for the last completed ISO week.
func TechReportFor(ctx context.Context, db *store.DB, now time.Time, lens Lens) (*TechReport, error) {
	week := LastCompletedWeek(now)
	r := &TechReport{Week: week.WeekLabel(), Lens: lens}

	var err error
	if r.Denom, err = enrichedCount(ctx, db, week, lens); err != nil {
		return nil, err
	}
	counts, kinds, err := techCounts(ctx, db, week, lens)
	if err != nil {
		return nil, err
	}
	for slug, n := range counts {
		s := TechStat{Slug: slug, Kind: kinds[slug], Count: n}
		if r.Denom > 0 {
			s.Share = float64(n) / float64(r.Denom)
		}
		r.Ranked = append(r.Ranked, s)
	}
	sort.SliceStable(r.Ranked, func(i, j int) bool {
		if r.Ranked[i].Count != r.Ranked[j].Count {
			return r.Ranked[i].Count > r.Ranked[j].Count
		}
		return r.Ranked[i].Slug < r.Ranked[j].Slug
	})
	if len(r.Ranked) > RankedTechLimit {
		r.Ranked = r.Ranked[:RankedTechLimit]
	}
	return r, nil
}

// enrichedCount is the share denominator: SWE postings in the window that the
// enrichment pipeline has already processed. Using all postings instead would
// let the enrich backlog systematically depress every technology's share
// (spec §3.7-3).
func enrichedCount(ctx context.Context, db *store.DB, w Window, lens Lens) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) `+swePosted+lens.Where()+` `+enrichedPredicate, w.Args()...).Scan(&n)
	return n, err
}

// techCounts returns slug -> distinct postings, and slug -> kind.
//
// count(DISTINCT j.uuid), not count(*): job_tech's primary key includes
// `source`, so a posting carrying the same technology from both the rule and
// LLM layers has two rows and would otherwise be counted twice.
func techCounts(ctx context.Context, db *store.DB, w Window, lens Lens) (map[string]int, map[string]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tech_slug, min(t.tech_kind), count(DISTINCT j.uuid)
		FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`+lens.Where()+`
		GROUP BY t.tech_slug`, w.Args()...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	counts, kinds := map[string]int{}, map[string]string{}
	for rows.Next() {
		var slug, kind string
		var n int
		if err := rows.Scan(&slug, &kind, &n); err != nil {
			return nil, nil, err
		}
		counts[slug], kinds[slug] = n, kind
	}
	return counts, kinds, rows.Err()
}
