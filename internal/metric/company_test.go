package metric

import (
	"context"
	"testing"
)

func TestCompanyReportRanksAndGatesEmployers(t *testing.T) {
	db := seedFixture(t)
	r, err := CompanyReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Top) == 0 {
		t.Fatal("no employers ranked")
	}
	if len(r.Top) > TopCompanyLimit {
		t.Errorf("Top has %d rows, want at most %d", len(r.Top), TopCompanyLimit)
	}
	for i := 1; i < len(r.Top); i++ {
		if r.Top[i-1].Postings < r.Top[i].Postings {
			t.Errorf("employers out of rank order at %d: %d then %d", i, r.Top[i-1].Postings, r.Top[i].Postings)
		}
	}
	for _, c := range r.Top {
		if c.Postings < MinPostingsPerCompanyStat && !c.Coverage.Suppressed {
			t.Errorf("%s has %d postings but is not suppressed (floor %d)", c.Name, c.Postings, MinPostingsPerCompanyStat)
		}
		if c.Coverage.Suppressed && c.AppsPerDay != 0 {
			t.Errorf("%s is suppressed but carries an apps/day figure", c.Name)
		}
		if c.Salary.Disclosed > c.Salary.Total {
			t.Errorf("%s discloses %d of %d postings", c.Name, c.Salary.Disclosed, c.Salary.Total)
		}
		if c.ActiveNow > c.Postings {
			t.Errorf("%s has %d active of %d postings in window", c.Name, c.ActiveNow, c.Postings)
		}
	}
}

// The lifetime figure describes postings that came down. Every one it counts
// must actually have a closed_at, or the median is measuring nothing.
func TestLifetimeCountsOnlyClosedPostings(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	roll := Rolling(fixtureNow, RollingDays)
	lt, err := LifetimeFor(ctx, db, roll, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var closedInWindow int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM job j
		WHERE j.is_swe=1 AND j.closed_at IS NOT NULL AND j.closed_at >= ? AND j.closed_at < ?`,
		roll.Args()...).Scan(&closedInWindow); err != nil {
		t.Fatal(err)
	}
	if lt.Closed != closedInWindow {
		t.Errorf("Lifetime.Closed = %d, want %d", lt.Closed, closedInWindow)
	}
	if lt.Closed == 0 {
		t.Fatal("fixture produced no closed postings in the window")
	}
	if lt.StillOpen == 0 {
		t.Error("StillOpen = 0; without the censored remainder the page cannot show its own bias")
	}
	if lt.Coverage.Suppressed {
		t.Fatalf("lifetime suppressed at n=%d (floor %d)", lt.Closed, MinClosedForLifetime)
	}
	// bands must partition the sample exactly
	var sum int
	for _, b := range lt.Bands {
		sum += b.Count
	}
	if sum != lt.Closed {
		t.Errorf("bands sum to %d, want %d — a posting fell between buckets", sum, lt.Closed)
	}
	if lt.MedianDays <= 0 {
		t.Errorf("MedianDays = %v, want a positive listing length", lt.MedianDays)
	}
}

// Below the floor the median must not be published at all — one employer's
// habits are not the market's.
func TestLifetimeSuppressedBelowFloor(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	roll := Rolling(fixtureNow, RollingDays)
	// leave fewer than the floor closed inside the window
	if _, err := db.ExecContext(ctx, `
		UPDATE job SET closed_at = NULL
		WHERE uuid IN (SELECT uuid FROM job WHERE closed_at IS NOT NULL ORDER BY uuid LIMIT -1 OFFSET ?)`,
		MinClosedForLifetime-1); err != nil {
		t.Fatal(err)
	}
	lt, err := LifetimeFor(ctx, db, roll, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if lt.Closed >= MinClosedForLifetime {
		t.Skipf("still %d closed postings; the trim did not go far enough", lt.Closed)
	}
	if !lt.Coverage.Suppressed {
		t.Errorf("n=%d is below the floor %d but was published", lt.Closed, MinClosedForLifetime)
	}
	if lt.MedianDays != 0 || len(lt.Bands) != 0 {
		t.Error("a suppressed lifetime must carry no figures at all")
	}
}

// Competition is a rate, so it must be normalised by how long a posting had
// been up — otherwise it measures posting age (spec §3.7-2).
func TestCompetitionIsNormalisedPerDay(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	rows, err := CompetitionByRole(ctx, db, Rolling(fixtureNow, RollingDays), Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no competition rows")
	}
	for _, r := range rows {
		if r.Coverage.Suppressed {
			if r.AppsPerDay != 0 || r.ViewsPerDay != 0 {
				t.Errorf("%s suppressed but carries figures", r.Key)
			}
			continue
		}
		// A raw cumulative count would routinely exceed the posting's age in
		// days; a per-day rate over this fixture stays modest.
		if r.AppsPerDay < 0 || r.ViewsPerDay < 0 {
			t.Errorf("%s has a negative rate", r.Key)
		}
		if r.Conversion < 0 || r.Conversion > 1.0001 {
			t.Errorf("%s conversion = %v, want a share of views", r.Key, r.Conversion)
		}
		if r.AppsPerDay > r.ViewsPerDay && r.ViewsPerDay > 0 {
			t.Errorf("%s: more applications per day (%v) than views (%v)", r.Key, r.AppsPerDay, r.ViewsPerDay)
		}
	}
}

func TestGhostSignalsAreSharesOfWhatIsOnTheBoard(t *testing.T) {
	db := seedFixture(t)
	today := fixtureNow.In(SGT).Format("2006-01-02")
	g, err := GhostSignalsFor(context.Background(), db, today, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if !g.HasSignal {
		t.Fatal("nothing on the board; the fixture should leave two thirds open")
	}
	if g.StaleOver60 > g.Active || g.Reposted > g.Active {
		t.Errorf("subset larger than the set: stale %d, reposted %d, active %d", g.StaleOver60, g.Reposted, g.Active)
	}
	if g.StaleShare < 0 || g.StaleShare > 1 || g.RepostShare < 0 || g.RepostShare > 1 {
		t.Errorf("shares out of range: %v, %v", g.StaleShare, g.RepostShare)
	}
}

func TestCompanyReportFollowsTheLens(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := CompanyReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	lensed, err := CompanyReportFor(ctx, db, fixtureNow, Lens{Exp: "0-2"})
	if err != nil {
		t.Fatal(err)
	}
	sum := func(rows []CompanyRow) int {
		n := 0
		for _, r := range rows {
			n += r.Postings
		}
		return n
	}
	if sum(lensed.Top) >= sum(all.Top) {
		t.Errorf("lensed postings %d not below unlensed %d", sum(lensed.Top), sum(all.Top))
	}
	if lensed.Lifetime.Closed >= all.Lifetime.Closed {
		t.Error("the lifetime figure ignored the lens")
	}
}
