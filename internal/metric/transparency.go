package metric

import (
	"context"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// statedSalaryPredicate is what the transparency rate counts: the posting told
// you the pay, in whatever unit it chose. Deliberately NOT
// comparableSalaryPredicate — that one pins salary_type='Monthly' so medians
// are computed over one unit, and reusing it here would count an openly
// advertised annual salary as opaque against a denominator of every SWE
// posting, understating the published rate (spec §3.3). Queries using it MUST
// alias the job table as `j`.
const statedSalaryPredicate = `j.salary_hidden=0 AND j.salary_min IS NOT NULL`

// Transparency is the salary-disclosure rate over a window: how many postings
// state their pay — in any unit — out of how many exist at all.
//
// Every median in this package describes only the narrower comparable-salary
// subset, so the pair travels with the number rather than being recomputed per
// page — two hand-rolled copies would drift the moment "disclosed" changes
// meaning.
type Transparency struct {
	Disclosed int
	Total     int
}

// Pct is the disclosed share, or 0 for an empty window (never NaN — this value
// is printed beside every salary figure).
func (t Transparency) Pct() float64 {
	if t.Total == 0 {
		return 0
	}
	return float64(t.Disclosed) / float64(t.Total)
}

// windowTransparency is the disclosure rate over the whole window, and the one
// query behind every disclosure figure the site prints — /pay's headline rate
// and /tech's premium-baseline footnote both come from here.
//
// Both halves of the pair come out of a single row, count(*) beside a
// conditional sum over the same scan, and that is the point: Disclosed can then
// never exceed Total. Assembling the pair from two independent queries makes
// that state representable — the two counts can disagree over a lens, a window
// boundary or a concurrent write, and the page renders a plausible-looking
// "104.0%" rather than failing. One row makes the bad state unrepresentable, so
// nothing downstream has to decide whether to clamp, panic or tolerate it.
func windowTransparency(ctx context.Context, db *store.DB, w Window, lens Lens) (Transparency, error) {
	var t Transparency
	err := db.QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(CASE WHEN `+statedSalaryPredicate+` THEN 1 ELSE 0 END),0) `+
		swePosted+lens.Where(), w.Args()...).Scan(&t.Total, &t.Disclosed)
	return t, err
}
