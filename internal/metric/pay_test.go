package metric

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func TestPayGridPercentilesAreRealAdvertisedSalaries(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := PayReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Overall.Coverage.Suppressed {
		t.Fatalf("overall cell suppressed with the full fixture: %+v", r.Overall.Coverage)
	}
	for _, q := range []float64{r.Overall.P25, r.Overall.P50, r.Overall.P75} {
		var n int
		if err := db.QueryRowContext(ctx, `
			SELECT count(*) FROM job
			WHERE is_swe=1 AND salary_hidden=0 AND salary_type='Monthly'
			  AND salary_min IS NOT NULL AND salary_max IS NOT NULL
			  AND (salary_min+salary_max)/2.0 = ?`, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("percentile %v was never advertised by any posting", q)
		}
	}
	if !(r.Overall.P25 <= r.Overall.P50 && r.Overall.P50 <= r.Overall.P75) {
		t.Errorf("percentiles out of order: %v / %v / %v", r.Overall.P25, r.Overall.P50, r.Overall.P75)
	}
}

func TestPayGridShapeFollowsTheVocabularies(t *testing.T) {
	r, err := PayReportFor(context.Background(), seedFixture(t), fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Roles) != len(RoleFamilies()) {
		t.Errorf("grid has %d role columns, want %d", len(r.Roles), len(RoleFamilies()))
	}
	if len(r.RoleTotals) != len(r.Roles) {
		t.Errorf("%d column totals for %d columns", len(r.RoleTotals), len(r.Roles))
	}
	if len(r.Grid) != len(classify.SeniorityLevels()) {
		t.Errorf("grid has %d seniority rows, want %d", len(r.Grid), len(classify.SeniorityLevels()))
	}
	for i, row := range r.Grid {
		if row.Seniority != classify.SeniorityLevels()[i] {
			t.Errorf("row %d = %q, want %q (career order)", i, row.Seniority, classify.SeniorityLevels()[i])
		}
		if len(row.Cells) != len(r.Roles) {
			t.Fatalf("row %q has %d cells, want %d", row.Seniority, len(row.Cells), len(r.Roles))
		}
	}
}

func TestPayCellSuppressionBoundary(t *testing.T) {
	// Four disclosed postings in one (seniority, role) cell must suppress; five
	// must not. Seeded rather than fixture-derived so the boundary is exact.
	ctx := context.Background()
	for _, tc := range []struct {
		n          int
		suppressed bool
	}{{4, true}, {5, false}} {
		db := seedControlledPay(t, tc.n)
		r, err := PayReportFor(ctx, db, fixtureNow, Lens{})
		if err != nil {
			t.Fatal(err)
		}
		cell, ok := findCell(r, "Senior", classify.FamilyBackend)
		if !ok {
			t.Fatalf("n=%d: seeded cell missing from the grid", tc.n)
		}
		if cell.Coverage.Suppressed != tc.suppressed {
			t.Errorf("n=%d: suppressed = %v, want %v (samples=%d)",
				tc.n, cell.Coverage.Suppressed, tc.suppressed, cell.Coverage.Samples)
		}
		if cell.Coverage.Samples != tc.n {
			t.Errorf("n=%d: samples = %d, want the real count", tc.n, cell.Coverage.Samples)
		}
		if tc.suppressed && (cell.P50 != 0 || cell.P25 != 0 || cell.P75 != 0) {
			t.Errorf("n=%d: suppressed cell carries values %v/%v/%v", tc.n, cell.P25, cell.P50, cell.P75)
		}
	}
}

func TestLadderKeepsZeroAndOneToTwoApart(t *testing.T) {
	// spec §3.7-1: "no experience required" (0) and "1-2 years" are different
	// answers to "can I apply", and both differ from "did not say".
	r, err := PayReportFor(context.Background(), seedFixture(t), fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0", "1-2", "3-5", "6+", "unstated"}
	if len(r.Ladder) != len(want) {
		t.Fatalf("ladder has %d rungs, want %d", len(r.Ladder), len(want))
	}
	for i, label := range want {
		if r.Ladder[i].Label != label {
			t.Errorf("rung %d = %q, want %q", i, r.Ladder[i].Label, label)
		}
	}
	var zero, oneTwo *PayBand
	for i := range r.Ladder {
		switch r.Ladder[i].Label {
		case "0":
			zero = &r.Ladder[i]
		case "1-2":
			oneTwo = &r.Ladder[i]
		}
	}
	if zero.Postings == 0 || oneTwo.Postings == 0 {
		t.Fatalf("fixture must populate both rungs: 0=%d, 1-2=%d", zero.Postings, oneTwo.Postings)
	}
	// A merged "0-2" band would carry both rungs' postings; keeping them apart
	// means neither rung can equal the sum.
	var unstated *PayBand
	for i := range r.Ladder {
		if r.Ladder[i].Label == "unstated" {
			unstated = &r.Ladder[i]
		}
	}
	if unstated.Postings == 0 {
		t.Error("fixture has null-experience rows; the unstated rung must not be empty")
	}
	if zero.Postings == zero.Postings+oneTwo.Postings {
		t.Error("rung 0 carries 1-2's postings; the bands were merged")
	}
}

func TestLadderIgnoresTheExperienceLensButFollowsRole(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := PayReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	expLens, err := ParseLens("6+", "")
	if err != nil {
		t.Fatal(err)
	}
	lensed, err := PayReportFor(ctx, db, fixtureNow, expLens)
	if err != nil {
		t.Fatal(err)
	}
	for i := range all.Ladder {
		if all.Ladder[i].Postings != lensed.Ladder[i].Postings {
			t.Errorf("rung %q changed under an experience lens (%d -> %d); the ladder IS that dimension",
				all.Ladder[i].Label, all.Ladder[i].Postings, lensed.Ladder[i].Postings)
		}
	}
	// The grid, by contrast, must narrow.
	if lensed.Salary.Total >= all.Salary.Total {
		t.Errorf("lensed window total %d must be smaller than %d", lensed.Salary.Total, all.Salary.Total)
	}
	roleLens, err := ParseLens("", classify.FamilyBackend)
	if err != nil {
		t.Fatal(err)
	}
	byRole, err := PayReportFor(ctx, db, fixtureNow, roleLens)
	if err != nil {
		t.Fatal(err)
	}
	var allTotal, roleTotal int
	for i := range all.Ladder {
		allTotal += all.Ladder[i].Postings
		roleTotal += byRole.Ladder[i].Postings
	}
	if roleTotal >= allTotal || roleTotal == 0 {
		t.Errorf("ladder totals under a role lens = %d, want 0 < n < %d", roleTotal, allTotal)
	}
}

func TestTransparencyByCompanyTypeSuppressesThinTypes(t *testing.T) {
	r, err := PayReportFor(context.Background(), seedFixture(t), fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Salary.Total == 0 {
		t.Fatal("no postings in the window")
	}
	if p := r.Salary.Pct(); p <= 0 || p > 1 {
		t.Errorf("overall transparency = %v, want (0,1]", p)
	}
	var shown, hidden int
	for _, row := range r.ByCompany {
		if row.Coverage.Suppressed {
			hidden++
			if row.Total >= MinPostingsPerCompanyStat {
				t.Errorf("%s suppressed with %d postings", row.CompanyType, row.Total)
			}
		} else {
			shown++
			if row.Total < MinPostingsPerCompanyStat {
				t.Errorf("%s shown with only %d postings", row.CompanyType, row.Total)
			}
			if row.Disclosed > row.Total {
				t.Errorf("%s discloses %d of %d", row.CompanyType, row.Disclosed, row.Total)
			}
		}
	}
	if shown == 0 {
		t.Error("no company type cleared the threshold; the fixture has several")
	}
	_ = hidden
}

// seedControlledPay builds a DB with exactly n disclosed Senior/Backend
// postings in the rolling window, for exact-boundary assertions.
func seedControlledPay(t *testing.T, n int) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	cl := classify.New(map[string]string{"25121": classify.FamilyBackend})
	day := LastCompletedWeek(fixtureNow).Start.In(SGT).Format("2006-01-02")
	for i := 0; i < n; i++ {
		j := mcf.Job{
			UUID: fmt.Sprintf("ctl-%03d", i), Title: "Senior Backend Engineer",
			Description: "d",
			Metadata: mcf.Metadata{JobPostID: fmt.Sprintf("MCF-ctl-%03d", i),
				NewPostingDate: day, ExpiryDate: "2026-12-31"},
			SSOCCode:   "25121",
			Categories: []mcf.Category{{Category: "Information Technology"}},
			Salary: &mcf.Salary{Minimum: float64(6000 + i*100), Maximum: float64(8000 + i*100),
				Type: mcf.SalaryType{SalaryType: "Monthly"}},
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/ctl#0"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func findCell(r *PayReport, seniority, role string) (PayCell, bool) {
	for _, row := range r.Grid {
		if row.Seniority != seniority {
			continue
		}
		for i, col := range r.Roles {
			if col == role {
				return row.Cells[i], true
			}
		}
	}
	return PayCell{}, false
}
