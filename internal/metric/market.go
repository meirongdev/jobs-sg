package metric

import (
	"context"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/store"
)

// TrendWeeks is how many completed ISO weeks the front page charts.
const TrendWeeks = 12

// MarketReport is the / page model: what a job seeker means by "is it busy
// right now".
type MarketReport struct {
	Week      string // reported ISO week, the last completed one
	WeekRange string // its inclusive SGT date range

	Active      int // SWE postings on the board right now
	ActiveEntry int // the entry-level subset of Active

	NewJobs   int     // new SWE postings in the reported week
	PrevJobs  int     // the week before, for WoW
	EntryJobs int     // entry-level new postings in the reported week
	WoW       float64 // (NewJobs − PrevJobs) / PrevJobs, as a fraction
	HasWoW    bool    // false when the previous week is empty: 0 → n is not a percentage

	Trend       []KV // new postings per week, oldest first, TrendWeeks long
	Roles       []KV // reported week's role_family distribution, count desc
	Seniorities []KV // reported week's seniority distribution, career order
	WorkModes   []KV // reported week's work-mode distribution, count desc
	EntryByRole []KV // entry-level new postings by role, count desc

	Lens Lens
}

// activePredicate is "on the board right now" (spec §3.4): not taken down and
// not past its advertised expiry. Takes one bind argument, today's SGT date.
//
// Unlike every other predicate here it is deliberately NOT windowed by
// posting_date: "how many jobs are there" is a question about the present, not
// about a reporting period, and serving a week-old count is the exact staleness
// that made the front page useless as a static report (spec §2.2).
const activePredicate = `j.closed_at IS NULL AND (j.expiry_date IS NULL OR j.expiry_date >= ?)`

// MarketReportFor builds the / model for the last completed ISO week, plus
// live counts as of now.
func MarketReportFor(ctx context.Context, db *store.DB, now time.Time, lens Lens) (*MarketReport, error) {
	week := LastCompletedWeek(now)
	r := &MarketReport{Week: week.WeekLabel(), WeekRange: week.RangeLabel(), Lens: lens}
	today := now.In(SGT).Format("2006-01-02")

	var err error
	if r.Active, err = activeCount(ctx, db, today, lens, ""); err != nil {
		return nil, err
	}
	if r.ActiveEntry, err = activeCount(ctx, db, today, lens, "AND "+EntryPredicate); err != nil {
		return nil, err
	}

	if r.NewJobs, err = weekCount(ctx, db, week, lens, ""); err != nil {
		return nil, err
	}
	if r.EntryJobs, err = weekCount(ctx, db, week, lens, "AND "+EntryPredicate); err != nil {
		return nil, err
	}
	prev := PrevWeeks(week, 1)[0]
	if r.PrevJobs, err = weekCount(ctx, db, prev, lens, ""); err != nil {
		return nil, err
	}
	// A week-over-week move needs something to move from. With no previous week
	// the honest answer is "no comparison yet", not +100% — the page reads
	// HasWoW and says so.
	if r.PrevJobs > 0 {
		r.WoW, r.HasWoW = float64(r.NewJobs-r.PrevJobs)/float64(r.PrevJobs), true
	}

	// Trend covers the reported week and the 11 before it, oldest first, so a
	// gap week renders as a real zero — that is a quiet pipeline, worth seeing.
	for _, w := range append(PrevWeeks(week, TrendWeeks-1), week) {
		n, err := weekCount(ctx, db, w, lens, "")
		if err != nil {
			return nil, err
		}
		r.Trend = append(r.Trend, KV{Key: w.WeekLabel(), Value: float64(n)})
	}

	if r.Roles, err = weekGroup(ctx, db, week, lens, "j.role_family", ""); err != nil {
		return nil, err
	}
	// Seniority is the one distribution that is not ranked by count: it has an
	// inherent order, and a bar chart running Senior/Junior/Mid reads as noise
	// where Intern→Manager reads as a shape. Same order /pay's grid rows use.
	if r.Seniorities, err = weekGroup(ctx, db, week, lens, "j.seniority", ""); err != nil {
		return nil, err
	}
	r.Seniorities = inOrder(r.Seniorities, classify.SeniorityLevels())
	if r.WorkModes, err = weekGroup(ctx, db, week, lens, "j.work_mode", ""); err != nil {
		return nil, err
	}
	// Entry-level demand gets its own breakdown rather than only a share:
	// "37 postings asking 0-2 years" is actionable where "18.4%" is not
	// (spec §3.4).
	if r.EntryByRole, err = weekGroup(ctx, db, week, lens, "j.role_family", "AND "+EntryPredicate); err != nil {
		return nil, err
	}
	return r, nil
}

// inOrder reorders kvs to follow order, dropping nothing: values absent from
// order keep their relative position at the end, so a family added to classify
// without being added here still appears rather than vanishing.
func inOrder(kvs []KV, order []string) []KV {
	rank := make(map[string]int, len(order))
	for i, k := range order {
		rank[k] = i
	}
	known := make([]KV, 0, len(kvs))
	rest := make([]KV, 0)
	for _, kv := range kvs {
		if _, ok := rank[kv.Key]; ok {
			known = append(known, kv)
		} else {
			rest = append(rest, kv)
		}
	}
	sort.SliceStable(known, func(i, j int) bool { return rank[known[i].Key] < rank[known[j].Key] })
	return append(known, rest...)
}

// activeCount counts postings currently on the board, optionally narrowed by an
// extra predicate.
func activeCount(ctx context.Context, db *store.DB, today string, lens Lens, extra string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) `+sweFrom+` WHERE j.is_swe=1 AND `+activePredicate+lens.Where()+` `+extra,
		today).Scan(&n)
	return n, err
}

// weekCount counts new SWE postings in one window.
func weekCount(ctx context.Context, db *store.DB, w Window, lens Lens, extra string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) `+swePosted+lens.Where()+` `+extra, w.Args()...).Scan(&n)
	return n, err
}

// weekGroup returns a count per distinct value of col within the window,
// highest first. Rows with no value are skipped rather than bucketed under an
// empty label: an unclassified posting is a data gap, and naming it "" in a
// distribution chart would present it as a category.
func weekGroup(ctx context.Context, db *store.DB, w Window, lens Lens, col, extra string) ([]KV, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+col+`, count(*) `+swePosted+lens.Where()+` `+extra+`
		 GROUP BY `+col+` ORDER BY count(*) DESC, `+col+` ASC`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KV
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		if k == "" {
			continue
		}
		out = append(out, KV{Key: k, Value: float64(n)})
	}
	return out, rows.Err()
}
