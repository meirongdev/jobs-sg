package metric

// Suppression thresholds. Every number the site refuses to show is refused
// here, so the bar can be raised or lowered in one place (spec §5).
const (
	// MinWeeksForMomentum is 1 reported week + 4 baseline weeks.
	MinWeeksForMomentum = 5
	// MinTechCountForMomentum keeps a 1 -> 3 posting swing off the rising board.
	MinTechCountForMomentum = 10
	// MinSalarySamplesPerTech gates the salary premium per technology.
	MinSalarySamplesPerTech = 20
	// MinSalarySamplesPerCell gates one cell of the seniority x role grid. It
	// also keeps a cell from effectively exposing a single employer's posting.
	MinSalarySamplesPerCell = 5
	// MinPostingsPerCompanyStat gates per-company competition and transparency.
	MinPostingsPerCompanyStat = 5
)

// Suppression reasons.
const (
	ReasonSample  = "sample"
	ReasonHistory = "history"
)

// EntryPredicate is the single definition of an entry-level posting (spec
// §3.4). Queries using it MUST alias the job table as `j`.
const EntryPredicate = `((j.min_years_exp IS NOT NULL AND j.min_years_exp <= 2)
	OR (j.min_years_exp IS NULL AND j.seniority IN ('Intern','Junior')))`

// Coverage says whether a number is trustworthy enough to show, and why not
// when it is not. A suppressed value renders as "—(n=3)" or an explanation,
// never as 0 — a fabricated zero is worse than an admitted gap.
//
// Construct via SampleCoverage/HistoryCoverage — a hand-built Coverage can
// express states the renderer must never show (e.g. suppressed with no
// reason, which would fall through to the sample default and print the
// fabricated —(n=0)).
type Coverage struct {
	Samples        int
	WeeksAvailable int
	WeeksRequired  int
	Suppressed     bool
	Reason         string
}

// SampleCoverage suppresses a value computed from fewer than min observations.
func SampleCoverage(n, threshold int) Coverage {
	c := Coverage{Samples: n}
	if n < threshold {
		c.Suppressed, c.Reason = true, ReasonSample
	}
	return c
}

// HistoryCoverage suppresses a trend that does not have enough weeks behind it.
func HistoryCoverage(available, required int) Coverage {
	c := Coverage{WeeksAvailable: available, WeeksRequired: required}
	if available < required {
		c.Suppressed, c.Reason = true, ReasonHistory
	}
	return c
}
