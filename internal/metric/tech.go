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
	// momentum: baseline is the 4 completed weeks before the reported one. The
	// in-progress week is never included — it is always partial data and would
	// show every technology crashing (spec §3.1).
	baseline := PrevWeeks(week, MinWeeksForMomentum-1)
	shares := make([]map[string]float64, 0, len(baseline))
	available := 0
	if r.Denom > 0 {
		available++
	}
	for _, bw := range baseline {
		denom, err := enrichedCount(ctx, db, bw, lens)
		if err != nil {
			return nil, err
		}
		if denom == 0 {
			continue
		}
		available++
		counts, _, err := techCounts(ctx, db, bw, lens)
		if err != nil {
			return nil, err
		}
		m := make(map[string]float64, len(counts))
		for slug, n := range counts {
			m[slug] = float64(n) / float64(denom)
		}
		shares = append(shares, m)
	}
	r.History = HistoryCoverage(available, MinWeeksForMomentum)
	for slug, n := range counts {
		s := TechStat{Slug: slug, Kind: kinds[slug], Count: n}
		if r.Denom > 0 {
			s.Share = float64(n) / float64(r.Denom)
		}
		s.Momentum = momentumCoverage(n, r.History)
		if !s.Momentum.Suppressed {
			var sum float64
			for _, m := range shares {
				sum += m[slug]
			}
			s.MomentumPP = s.Share - sum/float64(len(shares))
		}
		r.Ranked = append(r.Ranked, s)
	}
	sort.SliceStable(r.Ranked, func(i, j int) bool {
		if r.Ranked[i].Count != r.Ranked[j].Count {
			return r.Ranked[i].Count > r.Ranked[j].Count
		}
		return r.Ranked[i].Slug < r.Ranked[j].Slug
	})
	// Boards draw from the FULL universe: eligibility is count and history,
	// never demand rank. Built before the display cap on purpose.
	r.Rising, r.Falling = momentumBoards(r.Ranked)
	if len(r.Ranked) > RankedTechLimit {
		r.Ranked = r.Ranked[:RankedTechLimit]
	}
	// Premium is computed over a rolling window, not one week: a single week's
	// disclosed salaries do not survive being split per technology.
	roll := Rolling(now, RollingDays)
	allSalaries, err := salarySample(ctx, db, roll, lens, "")
	if err != nil {
		return nil, err
	}
	r.SalaryN = len(allSalaries)
	r.MedianAll = Percentile(allSalaries, 0.5)
	if r.SalaryTotal, err = sweCount(ctx, db, roll, lens); err != nil {
		return nil, err
	}

	// Entry-friendliness shares the premium's rolling window: two columns of
	// one table computed over different periods would be silently incomparable.
	entry, err := entryShare(ctx, db, roll, lens)
	if err != nil {
		return nil, err
	}
	for i := range r.Ranked {
		slug := r.Ranked[i].Slug
		r.Ranked[i].EntryFriendly = entry[slug]

		vals, err := salarySample(ctx, db, roll, lens, slug)
		if err != nil {
			return nil, err
		}
		r.Ranked[i].Premium = SampleCoverage(len(vals), MinSalarySamplesPerTech)
		if !r.Ranked[i].Premium.Suppressed && r.MedianAll > 0 {
			r.Ranked[i].PremiumPct = Percentile(vals, 0.5)/r.MedianAll - 1
		}
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

// momentumCoverage suppresses a technology's momentum when the page lacks
// history, or when the technology is too thin for a share delta to mean
// anything (a 1 -> 3 posting swing must not top the rising board).
func momentumCoverage(count int, history Coverage) Coverage {
	if history.Suppressed {
		// Keep the per-tech sample count even when page-level history is what
		// suppresses: a future "why is this suppressed" surface needs it.
		h := history
		h.Samples = count
		return h
	}
	return SampleCoverage(count, MinTechCountForMomentum)
}

// MomentumBoardLimit is how many technologies each momentum board shows.
const MomentumBoardLimit = 10

// momentumBoards splits the unsuppressed rows into rising and falling boards.
func momentumBoards(ranked []TechStat) (rising, falling []TechStat) {
	live := make([]TechStat, 0, len(ranked))
	for _, s := range ranked {
		if !s.Momentum.Suppressed {
			live = append(live, s)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].MomentumPP > live[j].MomentumPP })
	for _, s := range live {
		if s.MomentumPP > 0 && len(rising) < MomentumBoardLimit {
			rising = append(rising, s)
		}
	}
	// Walk the same desc sort backwards for ascending order instead of
	// re-sorting.
	for i := len(live) - 1; i >= 0; i-- {
		if live[i].MomentumPP < 0 && len(falling) < MomentumBoardLimit {
			falling = append(falling, live[i])
		}
	}
	return rising, falling
}

// disclosedSalary limits every salary figure to publicly advertised monthly
// ranges. The share of postings that disclose at all is itself a headline
// number (spec §3.3) — these medians describe only that subset.
const disclosedSalary = `AND j.salary_hidden=0 AND j.salary_type='Monthly'
	AND j.salary_min IS NOT NULL AND j.salary_max IS NOT NULL`

// salarySample returns the ascending midpoint salaries in the window, either
// overall (slug == "") or for postings mentioning one technology.
//
// The per-technology form groups by posting before taking the value: a posting
// carrying the technology from both the rule and LLM layers has two job_tech
// rows, and counting it twice would skew the median toward it.
func salarySample(ctx context.Context, db *store.DB, w Window, lens Lens, slug string) ([]float64, error) {
	q := `SELECT (j.salary_min+j.salary_max)/2.0 ` + swePosted + lens.Where() + ` ` + disclosedSalary + ` ORDER BY 1`
	args := w.Args()
	if slug != "" {
		q = `SELECT min((j.salary_min+j.salary_max)/2.0)
			FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
			WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?` +
			lens.Where() + ` ` + disclosedSalary + ` AND t.tech_slug = ?
			GROUP BY j.uuid ORDER BY 1`
		// lens.Where() contributes no bind placeholders by construction (its
		// fragments are canned literals), so appending slug here keeps positional
		// args aligned with the '?' order in the query text.
		args = append(args, slug)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// sweCount counts every SWE posting in the window under the lens, disclosed
// salary or not — the denominator of the salary transparency rate.
func sweCount(ctx context.Context, db *store.DB, w Window, lens Lens) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT count(*) `+swePosted+lens.Where(), w.Args()...).Scan(&n)
	return n, err
}

// TransparencyPct is the share of SWE postings in the rolling window that
// disclose a monthly salary. It is printed beside every salary figure so a
// median over the disclosing subset cannot read as a market-wide number.
func (r *TechReport) TransparencyPct() float64 {
	if r.SalaryTotal == 0 {
		return 0
	}
	return float64(r.SalaryN) / float64(r.SalaryTotal)
}

// entryShare returns slug -> share of postings mentioning it that are
// entry-level. It answers "what do they actually ask a junior for", which the
// overall ranking cannot. The window is the caller's choice; /tech passes the
// premium's rolling window so the two table columns stay comparable.
func entryShare(ctx context.Context, db *store.DB, w Window, lens Lens) (map[string]float64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tech_slug, count(DISTINCT j.uuid),
		       count(DISTINCT CASE WHEN `+EntryPredicate+` THEN j.uuid END)
		FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`+lens.Where()+`
		GROUP BY t.tech_slug`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var slug string
		var total, entry int
		if err := rows.Scan(&slug, &total, &entry); err != nil {
			return nil, err
		}
		if total > 0 {
			out[slug] = float64(entry) / float64(total)
		}
	}
	return out, rows.Err()
}
