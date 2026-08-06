package metric

import (
	"fmt"
	"time"
)

// SGT is the site timezone: every bucket is an SGT calendar period while
// timestamps are stored as UTC (docs/03 §2).
var SGT = time.FixedZone("SGT", 8*3600)

// RollingDays is the standard trailing window for salary and company stats.
// One window length for every rolling metric, on purpose — per-metric windows
// would make two numbers on the same page silently incomparable.
const RollingDays = 90

// Window is a half-open UTC interval [Start, End) derived from an SGT period.
//
// Bounds are always SGT midnights, which render as 16:00Z the previous day.
// That is load-bearing: posting_date is date-only on the live API
// ("2026-08-03"), and comparing a date-only string against these bounds is
// correct ONLY because the bound's UTC calendar date is never an in-window SGT
// date. Do NOT "simplify" any bound to UTC midnight — it shifts the window by
// a day. Pinned by report.TestWeekWindowDateOnlyBoundaries.
type Window struct {
	Start time.Time
	End   time.Time
}

// Args renders the window as SQL bind arguments, in [start, end) order.
func (w Window) Args() []any {
	return []any{w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339)}
}

// WeekLabel is the YYYY-Www ISO label of the window's first SGT day.
func (w Window) WeekLabel() string {
	y, wk := w.Start.In(SGT).ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, wk)
}

// Day returns the SGT calendar day containing t.
func Day(t time.Time) Window {
	d := t.In(SGT)
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, SGT)
	return Window{Start: start.UTC(), End: start.AddDate(0, 0, 1).UTC()}
}

// ISOWeekOf returns the SGT ISO week (Monday-based) containing t.
func ISOWeekOf(t time.Time) Window {
	d := t.In(SGT)
	// time.Weekday counts Sunday as 0; ISO weeks start on Monday.
	back := (int(d.Weekday()) + 6) % 7
	monday := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, SGT).AddDate(0, 0, -back)
	return Window{Start: monday.UTC(), End: monday.AddDate(0, 0, 7).UTC()}
}

// LastCompletedWeek returns the most recent ISO week whose Sunday 24:00 SGT has
// passed. The in-progress week is always partial data: including it would show
// every technology crashing (spec §3.1).
func LastCompletedWeek(now time.Time) Window {
	return weekBefore(ISOWeekOf(now), 1)
}

// PrevWeeks returns the n ISO weeks immediately before w, oldest first.
func PrevWeeks(w Window, n int) []Window {
	out := make([]Window, 0, n)
	for i := n; i >= 1; i-- {
		out = append(out, weekBefore(w, i))
	}
	return out
}

func weekBefore(w Window, n int) Window {
	monday := w.Start.In(SGT).AddDate(0, 0, -7*n)
	return Window{Start: monday.UTC(), End: monday.AddDate(0, 0, 7).UTC()}
}

// Rolling returns the trailing days-long window ending at the end of the SGT
// day containing now.
func Rolling(now time.Time, days int) Window {
	end := Day(now).End
	return Window{Start: end.In(SGT).AddDate(0, 0, -days).UTC(), End: end}
}
