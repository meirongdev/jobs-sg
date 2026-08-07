package metric

import (
	"context"
	"testing"
)

func TestSkillDemandRanksAndSharesCorrectly(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	roll := Rolling(fixtureNow, RollingDays)

	skills, denom, err := SkillDemandFor(ctx, db, roll, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if denom == 0 {
		t.Fatal("no postings in the window")
	}
	if len(skills) == 0 {
		t.Fatal("no skill tags ranked")
	}
	if len(skills) > SkillDemandLimit {
		t.Errorf("returned %d rows, want at most %d", len(skills), SkillDemandLimit)
	}
	for i := 1; i < len(skills); i++ {
		if skills[i-1].Postings < skills[i].Postings {
			t.Errorf("out of rank order at %d: %d then %d", i, skills[i-1].Postings, skills[i].Postings)
		}
	}
	for _, s := range skills {
		if s.Postings > denom {
			t.Errorf("%s listed on %d postings, more than the %d in the window", s.Skill, s.Postings, denom)
		}
		if s.Share < 0 || s.Share > 1 {
			t.Errorf("%s share = %v, want a fraction", s.Skill, s.Share)
		}
		if s.KeyShare < 0 || s.KeyShare > 1 {
			t.Errorf("%s key share = %v, want a fraction of its own postings", s.Skill, s.KeyShare)
		}
	}
}

// MCF's skill tags are business competencies; job_tech holds the stack this
// system extracts from descriptions. Merging them would rank "Problem solving"
// against "kubernetes" — the whole reason docs/02 §4.2 gives for running an LLM
// at all is that these are different things.
func TestSkillDemandIsSeparateFromTheTechRanking(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skills) == 0 || len(r.Ranked) == 0 {
		t.Fatal("need both rankings populated to compare them")
	}
	tech := map[string]bool{}
	for _, s := range r.Ranked {
		tech[s.Slug] = true
	}
	for _, s := range r.Skills {
		if tech[s.Skill] {
			t.Errorf("%q appears in both the tech ranking and the skill tags", s.Skill)
		}
	}
	// and the report carries its own window label, since the skills window is
	// the rolling one rather than the reported week
	if r.SkillWindow == "" {
		t.Error("skill section has no window label; the page would imply it covers the reported week")
	}
}

// A must-have subset cannot exceed the postings it is a subset of.
func TestSkillKeyShareIsASubset(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	roll := Rolling(fixtureNow, RollingDays)
	skills, _, err := SkillDemandFor(ctx, db, roll, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var sawPartial bool
	for _, s := range skills {
		if s.KeyShare > 0 && s.KeyShare < 1 {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Error("every tag is all-or-nothing must-have; the fixture no longer exercises the split")
	}
}

func TestSkillDemandFollowsTheLens(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	roll := Rolling(fixtureNow, RollingDays)
	_, allDenom, err := SkillDemandFor(ctx, db, roll, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	_, lensDenom, err := SkillDemandFor(ctx, db, roll, Lens{Role: "Backend"})
	if err != nil {
		t.Fatal(err)
	}
	if lensDenom >= allDenom {
		t.Errorf("lensed denominator %d not below unlensed %d", lensDenom, allDenom)
	}
}

// employment_type was collected from day one and read by nothing. Contract and
// part-time work is a different search from permanent.
func TestMarketReportsEmploymentTypes(t *testing.T) {
	db := seedFixture(t)
	r, err := MarketReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Employment) == 0 {
		t.Fatal("no employment types reported")
	}
	var sum float64
	for _, kv := range r.Employment {
		sum += kv.Value
	}
	if int(sum) > r.NewJobs {
		t.Errorf("employment types sum to %v, more than the %d postings counted", sum, r.NewJobs)
	}
	if len(r.Employment) < 2 {
		t.Errorf("only %d employment type(s); the fixture should carry a mix", len(r.Employment))
	}
}
