package metric

import (
	"context"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/classify"
)

func TestMarketReportReportsLastCompletedWeek(t *testing.T) {
	db := seedFixture(t)
	r, err := MarketReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Week != "2026-W32" {
		t.Errorf("Week = %s, want 2026-W32 (the last completed week at fixtureNow)", r.Week)
	}
	if r.NewJobs == 0 {
		t.Error("NewJobs = 0; the fixture spreads postings across W27..W32")
	}
	if len(r.Trend) != TrendWeeks {
		t.Errorf("Trend has %d weeks, want %d", len(r.Trend), TrendWeeks)
	}
	// oldest first, ending on the reported week
	if last := r.Trend[len(r.Trend)-1]; last.Key != r.Week {
		t.Errorf("Trend ends on %s, want the reported week %s", last.Key, r.Week)
	}
	if float64(r.NewJobs) != r.Trend[len(r.Trend)-1].Value {
		t.Errorf("Trend's last point %v disagrees with NewJobs %d", r.Trend[len(r.Trend)-1].Value, r.NewJobs)
	}
}

// The in-progress week is always partial. Including it would make the front
// page report a crash every Monday morning (spec §3.1's reasoning, applied to
// the headline count rather than to momentum).
func TestMarketReportExcludesTheInProgressWeek(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	before, err := MarketReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}

	// The fixture stops at the end of W32, so the in-progress week is empty and
	// proves nothing on its own. Move one posting OUT of the reported week and
	// into the in-progress one: the reported count must drop by exactly that
	// posting and never pick it back up.
	inProgress := ISOWeekOf(fixtureNow).Start.In(SGT).Format("2006-01-02")
	week := LastCompletedWeek(fixtureNow)
	res, err := db.ExecContext(ctx, `
		UPDATE job SET posting_date = ?
		WHERE uuid = (SELECT uuid FROM job
		              WHERE is_swe=1 AND posting_date >= ? AND posting_date < ?
		              ORDER BY uuid LIMIT 1)`,
		append([]any{inProgress}, week.Args()...)...)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("moved %d postings into the in-progress week, want 1", n)
	}

	after, err := MarketReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Week != before.Week {
		t.Errorf("reported week moved to %s; the in-progress week must never be reported", after.Week)
	}
	// The moved posting left W32 and landed in W33, so the reported count drops
	// by one and never picks it back up.
	if after.NewJobs != before.NewJobs-1 {
		t.Errorf("NewJobs = %d, want %d — a posting in the current week was counted", after.NewJobs, before.NewJobs-1)
	}
	if last := after.Trend[len(after.Trend)-1]; last.Key != before.Week {
		t.Errorf("trend now ends on %s, want %s", last.Key, before.Week)
	}
}

// Active is a question about now, not about the reporting week: a posting that
// is closed or past its expiry is off the board whenever it was posted.
func TestActiveExcludesClosedAndExpired(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := MarketReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	today := fixtureNow.In(SGT).Format("2006-01-02")

	var allSWE, closed, expired int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM job j WHERE j.is_swe=1`).Scan(&allSWE); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM job j WHERE j.is_swe=1 AND j.closed_at IS NOT NULL`).Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM job j WHERE j.is_swe=1 AND j.closed_at IS NULL
		 AND j.expiry_date IS NOT NULL AND j.expiry_date < ?`, today).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if closed == 0 {
		t.Fatal("fixture seeded no closed postings; the lifetime bands never got applied")
	}
	if want := allSWE - closed - expired; r.Active != want {
		t.Errorf("Active = %d, want %d (all %d − closed %d − expired %d)", r.Active, want, allSWE, closed, expired)
	}
	if r.Active >= allSWE {
		t.Error("Active should be a strict subset once postings have closed")
	}
}

// Entry-level counts are a subset of the totals they sit beside — a card
// reading "40 entry-level of 30 new" is the kind of thing a reader notices
// before the author does.
func TestEntryCountsAreSubsetsOfTheirTotals(t *testing.T) {
	db := seedFixture(t)
	r, err := MarketReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.EntryJobs > r.NewJobs {
		t.Errorf("EntryJobs %d > NewJobs %d", r.EntryJobs, r.NewJobs)
	}
	if r.ActiveEntry > r.Active {
		t.Errorf("ActiveEntry %d > Active %d", r.ActiveEntry, r.Active)
	}
	var sum float64
	for _, kv := range r.EntryByRole {
		sum += kv.Value
	}
	if int(sum) != r.EntryJobs {
		t.Errorf("EntryByRole sums to %v, want EntryJobs %d", sum, r.EntryJobs)
	}
}

// A distribution that quietly drops rows misrepresents the market. Roles and
// work modes must account for every posting the headline counted, except the
// unclassified ones the query skips on purpose.
func TestRoleDistributionAccountsForEveryCountedPosting(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := MarketReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	week := LastCompletedWeek(fixtureNow)
	var unclassified int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM job j WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?
		 AND (j.role_family IS NULL OR j.role_family='')`, week.Args()...).Scan(&unclassified); err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, kv := range r.Roles {
		sum += kv.Value
	}
	if int(sum)+unclassified != r.NewJobs {
		t.Errorf("Roles sum %v + unclassified %d != NewJobs %d", sum, unclassified, r.NewJobs)
	}
}

func TestSeniorityDistributionUsesCareerOrder(t *testing.T) {
	db := seedFixture(t)
	r, err := MarketReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Seniorities) < 2 {
		t.Skip("fixture week has fewer than two seniority levels")
	}
	rank := map[string]int{}
	for i, s := range classify.SeniorityLevels() {
		rank[s] = i
	}
	for i := 1; i < len(r.Seniorities); i++ {
		prev, cur := r.Seniorities[i-1].Key, r.Seniorities[i].Key
		if rank[prev] > rank[cur] {
			t.Errorf("seniority out of career order: %s before %s", prev, cur)
		}
	}
}

// Every figure on the page has to move together under a lens, or the page is
// showing one question's answer next to another question's.
func TestMarketReportFollowsTheLens(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := MarketReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := MarketReportFor(ctx, db, fixtureNow, Lens{Role: classify.FamilyBackend})
	if err != nil {
		t.Fatal(err)
	}
	if backend.NewJobs >= all.NewJobs {
		t.Errorf("lensed NewJobs %d not below unlensed %d", backend.NewJobs, all.NewJobs)
	}
	if backend.Active >= all.Active {
		t.Errorf("lensed Active %d not below unlensed %d", backend.Active, all.Active)
	}
	for _, kv := range backend.Roles {
		if kv.Key != classify.FamilyBackend {
			t.Errorf("role lens leaked %q into the distribution", kv.Key)
		}
	}
	var trend float64
	for _, kv := range backend.Trend {
		trend += kv.Value
	}
	var allTrend float64
	for _, kv := range all.Trend {
		allTrend += kv.Value
	}
	if trend >= allTrend {
		t.Error("the trend chart ignored the lens")
	}
}

// 0 → n is not a percentage. With no previous week the page must say so rather
// than print +100%.
func TestWoWSuppressedWithoutAPreviousWeek(t *testing.T) {
	db := seedFixture(t)
	// No mutation needed: report the fixture's FIRST week (W27) and the week
	// before it is genuinely empty — the same state the site is in on launch.
	firstWeekNow := fixtureNow.AddDate(0, 0, -5*7)
	r, err := MarketReportFor(context.Background(), db, firstWeekNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.NewJobs == 0 {
		t.Fatalf("reported week %s is empty; pick a week the fixture populates", r.Week)
	}
	if r.PrevJobs != 0 {
		t.Fatalf("PrevJobs = %d, want 0 after wiping the baseline", r.PrevJobs)
	}
	if r.HasWoW {
		t.Error("HasWoW true with no previous week — the page would render a percentage of zero")
	}
	if r.WoW != 0 {
		t.Errorf("WoW = %v, want 0 when unavailable", r.WoW)
	}
}
