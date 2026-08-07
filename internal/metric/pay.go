package metric

import (
	"context"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/store"
)

// PayCell is one (seniority, role) cell of the percentile grid, or a total.
type PayCell struct {
	P25, P50, P75 float64
	Coverage      Coverage // suppressed below MinSalarySamplesPerCell
}

// PayRow is one seniority row of the grid.
type PayRow struct {
	Seniority string
	Cells     []PayCell // index-aligned with PayReport.Roles
	All       PayCell   // the row across every role
}

// PayBand is one rung of the experience ladder.
type PayBand struct {
	Label         string // "0" | "1-2" | "3-5" | "6+" | "unstated"
	P25, P50, P75 float64
	Postings      int      // every SWE posting in the band, disclosed or not
	Coverage      Coverage // suppressed below MinSalarySamplesPerCell
}

// TransparencyRow is one company type's disclosure rate.
type TransparencyRow struct {
	CompanyType string
	Transparency
	Coverage Coverage // suppressed below MinPostingsPerCompanyStat
}

// PayReport is the /pay page model.
type PayReport struct {
	Window     string    // inclusive SGT date range of the rolling window
	Days       int       // RollingDays, so the page can state its own window
	Roles      []string  // grid columns, career-neutral alphabetical order
	Grid       []PayRow  // rows in classify.SeniorityLevels order
	RoleTotals []PayCell // each role across every seniority, index-aligned with Roles
	Overall    PayCell
	Ladder     []PayBand
	Salary     Transparency // the whole window: disclosed vs all postings
	ByCompany  []TransparencyRow
	Lens       Lens
}

// ladderBands are the experience rungs, in career order.
//
// Deliberately finer than the lens bands (spec §2.3 uses 0-2 as one band):
// the ladder answers "what does the first year buy", so 0 and 1-2 must stay
// apart, and "unstated" is never folded into "no requirement" (spec §3.7-1).
//
// "<= 0", not "= 0": min_years_exp is an unvalidated pass-through of the
// MCF field (no CHECK in the schema), so a negative value is representable.
// An exact "= 0" leaves it matching no band at all, and a posting that
// matches no band disappears from every rung's count without a trace — the
// silent-gap failure this package exists to prevent. A negative requirement
// is a data error whose only sensible reading is "no experience required",
// so it belongs on this rung.
var ladderBands = []struct {
	Label     string
	Predicate string
}{
	{"0", `j.min_years_exp <= 0`},
	{"1-2", `j.min_years_exp BETWEEN 1 AND 2`},
	{"3-5", `j.min_years_exp BETWEEN 3 AND 5`},
	{"6+", `j.min_years_exp >= 6`},
	{"unstated", `j.min_years_exp IS NULL`},
}

// PayReportFor builds the /pay model over the trailing RollingDays window.
//
// A rolling window, not one week: a single week's disclosed salaries do not
// survive being split across a seniority × role grid.
func PayReportFor(ctx context.Context, db *store.DB, now time.Time, lens Lens) (*PayReport, error) {
	w := Rolling(now, RollingDays)
	r := &PayReport{
		Window: w.RangeLabel(),
		Days:   RollingDays,
		Roles:  RoleFamilies(),
		Lens:   lens,
	}

	cells, err := gridSamples(ctx, db, w, lens)
	if err != nil {
		return nil, err
	}
	var overall []float64
	for _, row := range cells {
		for _, vals := range row {
			overall = append(overall, vals...)
		}
	}
	sort.Float64s(overall)
	r.Overall = cellOf(overall)

	perRole := make([][]float64, len(r.Roles))
	for _, sen := range classify.SeniorityLevels() {
		row := PayRow{Seniority: sen, Cells: make([]PayCell, len(r.Roles))}
		var rowVals []float64
		for i, role := range r.Roles {
			vals := cells[sen][role]
			row.Cells[i] = cellOf(vals)
			rowVals = append(rowVals, vals...)
			perRole[i] = append(perRole[i], vals...)
		}
		sort.Float64s(rowVals)
		row.All = cellOf(rowVals)
		r.Grid = append(r.Grid, row)
	}
	// Column totals: a role's pay across every level. Re-sorted because the
	// per-cell samples were concatenated, and Percentile panics on unsorted
	// input rather than quietly returning a plausible wrong number.
	r.RoleTotals = make([]PayCell, len(r.Roles))
	for i := range perRole {
		sort.Float64s(perRole[i])
		r.RoleTotals[i] = cellOf(perRole[i])
	}

	// The ladder is the experience dimension itself, so it drops the
	// experience lens and keeps only the role filter.
	if r.Ladder, err = ladder(ctx, db, w, lens.RoleOnly()); err != nil {
		return nil, err
	}
	if r.Salary, err = windowTransparency(ctx, db, w, lens); err != nil {
		return nil, err
	}
	if r.ByCompany, err = transparencyByCompanyType(ctx, db, w, lens); err != nil {
		return nil, err
	}
	return r, nil
}

// cellOf turns an ascending sample into a cell, suppressing thin ones. A
// suppressed cell carries no values: a percentile over four salaries is both
// pseudo-precise and close to publishing one employer's advertised range.
func cellOf(sorted []float64) PayCell {
	c := PayCell{Coverage: SampleCoverage(len(sorted), MinSalarySamplesPerCell)}
	if c.Coverage.Suppressed {
		return c
	}
	c.P25 = Percentile(sorted, 0.25)
	c.P50 = Percentile(sorted, 0.5)
	c.P75 = Percentile(sorted, 0.75)
	return c
}

// gridSamples returns seniority -> role -> ascending disclosed midpoints.
//
// ORDER BY seniority, role_family, midpoint is load-bearing: Percentile panics
// on unsorted input, and grouping in Go preserves the per-group ascending order
// this ORDER BY produces.
func gridSamples(ctx context.Context, db *store.DB, w Window, lens Lens) (map[string]map[string][]float64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT coalesce(j.seniority,''), coalesce(j.role_family,''), `+salaryMidpoint+` `+
		swePosted+lens.Where()+` `+disclosedSalary+`
		ORDER BY 1, 2, 3`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string][]float64{}
	for rows.Next() {
		var sen, role string
		var v float64
		if err := rows.Scan(&sen, &role, &v); err != nil {
			return nil, err
		}
		if out[sen] == nil {
			out[sen] = map[string][]float64{}
		}
		out[sen][role] = append(out[sen][role], v)
	}
	return out, rows.Err()
}

// ladder returns one rung per experience band: the disclosed-salary
// percentiles plus the band's full posting count, so a reader can see that a
// median rests on a fraction of the rung.
func ladder(ctx context.Context, db *store.DB, w Window, lens Lens) ([]PayBand, error) {
	out := make([]PayBand, 0, len(ladderBands))
	for _, b := range ladderBands {
		band := PayBand{Label: b.Label}
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) `+swePosted+lens.Where()+` AND `+b.Predicate,
			w.Args()...).Scan(&band.Postings); err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(ctx,
			`SELECT `+salaryMidpoint+` `+swePosted+lens.Where()+` `+disclosedSalary+
				` AND `+b.Predicate+` ORDER BY 1`, w.Args()...)
		if err != nil {
			return nil, err
		}
		var vals []float64
		for rows.Next() {
			var v float64
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			vals = append(vals, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		cell := cellOf(vals)
		band.P25, band.P50, band.P75, band.Coverage = cell.P25, cell.P50, cell.P75, cell.Coverage
		out = append(out, band)
	}
	return out, nil
}

// windowTransparency is the disclosure rate over the whole window.
func windowTransparency(ctx context.Context, db *store.DB, w Window, lens Lens) (Transparency, error) {
	var t Transparency
	err := db.QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(CASE WHEN `+disclosedSalaryPredicate+` THEN 1 ELSE 0 END),0) `+
		swePosted+lens.Where(), w.Args()...).Scan(&t.Total, &t.Disclosed)
	return t, err
}

// transparencyByCompanyType answers "who tells you the number before you
// apply", ordered by disclosure rate so the transparent employers surface.
// Types below MinPostingsPerCompanyStat are suppressed rather than ranked on a
// handful of postings.
func transparencyByCompanyType(ctx context.Context, db *store.DB, w Window, lens Lens) ([]TransparencyRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT coalesce(c.company_type,'Other'), count(*),
		       coalesce(sum(CASE WHEN `+disclosedSalaryPredicate+` THEN 1 ELSE 0 END),0)
		FROM job j LEFT JOIN company c ON c.uen=j.company_uen
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`+lens.Where()+`
		GROUP BY 1`, w.Args()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TransparencyRow
	for rows.Next() {
		var row TransparencyRow
		if err := rows.Scan(&row.CompanyType, &row.Total, &row.Disclosed); err != nil {
			return nil, err
		}
		row.Coverage = SampleCoverage(row.Total, MinPostingsPerCompanyStat)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Coverage.Suppressed != out[j].Coverage.Suppressed {
			return !out[i].Coverage.Suppressed
		}
		if out[i].Pct() != out[j].Pct() {
			return out[i].Pct() > out[j].Pct()
		}
		return out[i].CompanyType < out[j].CompanyType
	})
	return out, nil
}
