package metric

import (
	"context"
	"sort"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// MinClosedForLifetime gates the listing-length figures. Below this the median
// is one or two employers' habits rather than the market's.
const MinClosedForLifetime = 20

// LifetimeBand is one listing-length bucket and its share of closed postings.
type LifetimeBand struct {
	Label string
	Count int
	Share float64
}

// Lifetime describes how long postings stay listed — measured over postings
// that have come down, which is the whole of its bias.
//
// **Right-censored on purpose and unavoidably.** A posting still on the board
// has no end date, so it cannot enter the calculation; the ones that linger are
// exactly the ones excluded, and the median therefore runs short. The page must
// say "how long closed postings stayed up", never "how long postings last"
// (spec §3.5). Resolution is one day: the pipeline is a daily batch.
type Lifetime struct {
	MedianDays float64
	Bands      []LifetimeBand
	Closed     int      // postings behind the figures
	StillOpen  int      // the censored remainder, published so the bias is visible
	Coverage   Coverage // suppressed below MinClosedForLifetime
}

// lifetimeBandDefs are the buckets spec §3.5 reports, as half-open day ranges.
var lifetimeBandDefs = []struct {
	Label    string
	Min, Max int // Max < 0 means unbounded
}{
	{"<7 days", 0, 7},
	{"7-14 days", 7, 15},
	{"15-30 days", 15, 31},
	{"30-60 days", 31, 61},
	{"60+ days", 61, -1},
}

// LifetimeFor measures postings that closed within the window.
//
// Selection is by closed_at, not posting_date: the question is "of the postings
// that came down recently, how long had they been up", and windowing by
// posting_date would instead ask about a cohort whose slow half has not closed
// yet — guaranteeing a short answer.
func LifetimeFor(ctx context.Context, db *store.DB, w Window, lens Lens) (Lifetime, error) {
	var lt Lifetime
	rows, err := db.QueryContext(ctx, `
		SELECT julianday(date(j.closed_at)) - julianday(date(j.posting_date))
		`+sweFrom+`
		WHERE j.is_swe=1 AND j.closed_at IS NOT NULL AND j.closed_at >= ? AND j.closed_at < ?
		`+lens.Where()+`
		ORDER BY 1`, w.Args()...)
	if err != nil {
		return lt, err
	}
	defer rows.Close()
	var days []float64
	for rows.Next() {
		var d float64
		if err := rows.Scan(&d); err != nil {
			return lt, err
		}
		// A close recorded before the posting date is a data error, not a
		// negative lifetime; clamp rather than let it drag the median down.
		if d < 0 {
			d = 0
		}
		days = append(days, d)
	}
	if err := rows.Err(); err != nil {
		return lt, err
	}

	lt.Closed = len(days)
	lt.Coverage = SampleCoverage(lt.Closed, MinClosedForLifetime)
	if !lt.Coverage.Suppressed {
		lt.MedianDays = Percentile(days, 0.5)
		for _, b := range lifetimeBandDefs {
			n := 0
			for _, d := range days {
				if int(d) >= b.Min && (b.Max < 0 || int(d) < b.Max) {
					n++
				}
			}
			lt.Bands = append(lt.Bands, LifetimeBand{
				Label: b.Label, Count: n, Share: float64(n) / float64(lt.Closed),
			})
		}
	}

	// The censored remainder, so the page can show what the median cannot see.
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) `+sweFrom+` WHERE j.is_swe=1 AND j.closed_at IS NULL`+lens.Where()).
		Scan(&lt.StillOpen); err != nil {
		return lt, err
	}
	return lt, nil
}

// GhostSignals are the two cheap tells that a listing may not be a live
// vacancy: it has sat on the board for months, or it keeps being reposted.
//
// Signals, not verdicts — a genuinely hard-to-fill role looks the same from
// outside. The page reports the share and lets the reader weigh it.
type GhostSignals struct {
	Active       int     // postings on the board right now
	StaleOver60  int     // …listed more than 60 days
	StaleShare   float64 //
	Reposted     int     // …with repost_count > 0
	RepostShare  float64 //
	HasSignal    bool    // false when nothing is on the board to describe
	StaleDaysCut int     // the threshold applied, so the page states its own bar
}

// GhostSignalsFor measures the postings currently on the board.
func GhostSignalsFor(ctx context.Context, db *store.DB, now string, lens Lens) (GhostSignals, error) {
	g := GhostSignals{StaleDaysCut: 60}
	err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       coalesce(sum(CASE WHEN julianday(?) - julianday(date(j.posting_date)) > 60 THEN 1 ELSE 0 END),0),
		       coalesce(sum(CASE WHEN j.repost_count > 0 THEN 1 ELSE 0 END),0)
		`+sweFrom+` WHERE j.is_swe=1 AND `+activePredicate+lens.Where(),
		now, now).Scan(&g.Active, &g.StaleOver60, &g.Reposted)
	if err != nil {
		return g, err
	}
	if g.Active > 0 {
		g.HasSignal = true
		g.StaleShare = float64(g.StaleOver60) / float64(g.Active)
		g.RepostShare = float64(g.Reposted) / float64(g.Active)
	}
	return g, nil
}

// CompetitionRow is one group's application pressure.
type CompetitionRow struct {
	Key         string
	Postings    int
	AppsPerDay  float64 // median, not mean: a viral posting should not move it
	ViewsPerDay float64 // median
	Conversion  float64 // applications per view, over the group's totals
	Coverage    Coverage
}

// CompetitionByRole gives the median daily application and view pressure per
// discipline, over postings in the window.
//
// Both counters are normalised by how long the posting had been up when it was
// last seen. view_count and application_count are running totals read at
// collection time, so comparing them raw measures how OLD a posting is, not how
// contested (spec §3.7-2); the +1 keeps a same-day posting from dividing by
// zero. Medians, not means, so one viral posting does not carry a discipline.
//
// **This is the ceiling of what the data supports.** Collection overwrites both
// counters every run, leaving no history to difference — "applications this
// week" is not answerable from this schema, and the page must not imply these
// numbers carry a time dimension (spec §3.6).
func CompetitionByRole(ctx context.Context, db *store.DB, w Window, lens Lens) ([]CompetitionRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT j.role_family,
		       CAST(j.application_count AS REAL) / max(1.0, julianday(date(j.last_seen_at)) - julianday(date(j.posting_date)) + 1),
		       CAST(j.view_count AS REAL)        / max(1.0, julianday(date(j.last_seen_at)) - julianday(date(j.posting_date)) + 1),
		       coalesce(j.application_count,0), coalesce(j.view_count,0)
		`+swePosted+lens.Where()+`
		  AND j.role_family IS NOT NULL AND j.role_family <> ''
		  AND j.application_count IS NOT NULL AND j.view_count IS NOT NULL
		ORDER BY j.role_family`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type acc struct {
		apps, views       []float64
		totalApp, totalVw int
	}
	byRole := map[string]*acc{}
	var order []string
	for rows.Next() {
		var role string
		var a, v float64
		var ta, tv int
		if err := rows.Scan(&role, &a, &v, &ta, &tv); err != nil {
			return nil, err
		}
		if _, ok := byRole[role]; !ok {
			byRole[role] = &acc{}
			order = append(order, role)
		}
		x := byRole[role]
		x.apps = append(x.apps, a)
		x.views = append(x.views, v)
		x.totalApp += ta
		x.totalVw += tv
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]CompetitionRow, 0, len(order))
	for _, role := range order {
		x := byRole[role]
		r := CompetitionRow{Key: role, Postings: len(x.apps)}
		r.Coverage = SampleCoverage(r.Postings, MinPostingsPerCompanyStat)
		if !r.Coverage.Suppressed {
			sort.Float64s(x.apps)
			sort.Float64s(x.views)
			r.AppsPerDay = Percentile(x.apps, 0.5)
			r.ViewsPerDay = Percentile(x.views, 0.5)
			if x.totalVw > 0 {
				r.Conversion = float64(x.totalApp) / float64(x.totalVw)
			}
		}
		out = append(out, r)
	}
	return out, nil
}
