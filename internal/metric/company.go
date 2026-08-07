package metric

import (
	"context"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// TopCompanyLimit is how many employers the persistent-hirer table shows.
const TopCompanyLimit = 25

// CompanyRow is one employer's line on /companies.
type CompanyRow struct {
	Name        string
	CompanyType string
	Postings    int          // postings in the window
	ActiveNow   int          // …still on the board
	AppsPerDay  float64      // median daily applications across its postings
	Salary      Transparency // does it tell you what it pays
	Coverage    Coverage     // suppressed below MinPostingsPerCompanyStat
}

// CompanyReport is the /companies page model: who is hiring, and what applying
// to them looks like.
type CompanyReport struct {
	Window string // inclusive SGT date range
	Days   int    // RollingDays, so the page states its own window
	Floor  int    // MinPostingsPerCompanyStat, ditto

	Top    []CompanyRow // by postings desc, capped at TopCompanyLimit
	ByType []KV         // employer-type distribution over the window

	Lifetime    Lifetime
	Ghost       GhostSignals
	Competition []CompetitionRow

	Lens Lens
}

// CompanyReportFor builds /companies over the trailing RollingDays window.
func CompanyReportFor(ctx context.Context, db *store.DB, now time.Time, lens Lens) (*CompanyReport, error) {
	roll := Rolling(now, RollingDays)
	today := now.In(SGT).Format("2006-01-02")
	r := &CompanyReport{
		Window: roll.RangeLabel(),
		Days:   RollingDays,
		Floor:  MinPostingsPerCompanyStat,
		Lens:   lens,
	}

	var err error
	if r.Top, err = topCompanies(ctx, db, roll, today, lens); err != nil {
		return nil, err
	}
	if r.ByType, err = companyTypeMix(ctx, db, roll, lens); err != nil {
		return nil, err
	}
	if r.Lifetime, err = LifetimeFor(ctx, db, roll, lens); err != nil {
		return nil, err
	}
	if r.Ghost, err = GhostSignalsFor(ctx, db, today, lens); err != nil {
		return nil, err
	}
	if r.Competition, err = CompetitionByRole(ctx, db, roll, lens); err != nil {
		return nil, err
	}
	return r, nil
}

// topCompanies ranks employers by postings in the window.
//
// Grouped by UEN, not by name: the UEN is Singapore's legal-entity key and is
// the reason this system needs no fuzzy matching (docs/02 §0). Two spellings of
// one employer are one row; two employers sharing a trading name are two.
//
// The per-company statistics carry their own Coverage: below the floor, one
// posting's applicant count would be presented as an employer's character, and
// a disclosure rate over three postings says nothing about a company's policy.
func topCompanies(ctx context.Context, db *store.DB, w Window, today string, lens Lens) ([]CompanyRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.uen, c.name, coalesce(c.company_type,''), count(*),
		       coalesce(sum(CASE WHEN `+activePredicate+` THEN 1 ELSE 0 END),0),
		       coalesce(sum(CASE WHEN `+statedSalaryPredicate+` THEN 1 ELSE 0 END),0)
		`+sweFrom+` JOIN company c ON c.uen = j.company_uen
		`+sweWhere+lens.Where()+`
		GROUP BY c.uen
		ORDER BY count(*) DESC, c.name ASC
		LIMIT ?`, append(append([]any{today}, w.Args()...), TopCompanyLimit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CompanyRow
	var uens []string
	for rows.Next() {
		var cr CompanyRow
		var uen string
		if err := rows.Scan(&uen, &cr.Name, &cr.CompanyType, &cr.Postings, &cr.ActiveNow, &cr.Salary.Disclosed); err != nil {
			return nil, err
		}
		cr.Salary.Total = cr.Postings
		cr.Coverage = SampleCoverage(cr.Postings, MinPostingsPerCompanyStat)
		out = append(out, cr)
		uens = append(uens, uen)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Application pressure needs the per-posting values, not an average of
	// averages, so it is a second pass over the same employers.
	for i := range out {
		if out[i].Coverage.Suppressed {
			continue
		}
		// Keyed by UEN, matching how the ranking grouped: two employers sharing
		// a trading name are two rows, and matching by name here would blend
		// their applicant counts back together.
		vals, err := companyAppsPerDay(ctx, db, w, lens, uens[i])
		if err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			out[i].AppsPerDay = Percentile(vals, 0.5)
		}
	}
	return out, nil
}

// companyAppsPerDay returns one employer's per-posting daily application rates,
// ascending. Keyed by UEN, the legal-entity identifier the ranking groups on.
func companyAppsPerDay(ctx context.Context, db *store.DB, w Window, lens Lens, uen string) ([]float64, error) {
	args := append(w.Args(), uen)
	rows, err := db.QueryContext(ctx, `
		SELECT CAST(j.application_count AS REAL) /
		       max(1.0, julianday(date(j.last_seen_at)) - julianday(date(j.posting_date)) + 1)
		`+sweFrom+` JOIN company c ON c.uen = j.company_uen
		`+sweWhere+lens.Where()+`
		  AND c.uen = ? AND j.application_count IS NOT NULL
		ORDER BY 1`, args...)
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

// companyTypeMix is the employer-type distribution over the window.
func companyTypeMix(ctx context.Context, db *store.DB, w Window, lens Lens) ([]KV, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.company_type, count(*)
		`+sweFrom+` JOIN company c ON c.uen = j.company_uen
		`+sweWhere+lens.Where()+`
		  AND c.company_type IS NOT NULL AND c.company_type <> ''
		GROUP BY c.company_type ORDER BY count(*) DESC, c.company_type ASC`, w.Args()...)
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
		out = append(out, KV{Key: k, Value: float64(n)})
	}
	return out, rows.Err()
}
