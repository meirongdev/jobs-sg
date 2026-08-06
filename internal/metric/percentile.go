package metric

import (
	"math"
	"sort"
)

// Percentile returns the nearest-rank value at q (0..1) over a slice sorted
// ascending.
//
// Nearest-rank, not interpolation: every number the site reports is a salary
// that actually appeared in a posting, never an averaged figure nobody
// advertised (docs/03 §6). q=0.5 reproduces the weekly report's existing upper
// median, vals[len(vals)/2], so the two can never disagree.
func Percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	// A caller bug, not a data condition: every producer sorts (SQL ORDER BY,
	// sort.Float64s). An unsorted slice would yield a plausible-but-wrong
	// number — the exact failure this package exists to prevent — so fail
	// loud at the moment the bug is introduced instead of when a user notices.
	if !sort.Float64sAreSorted(sorted) {
		panic("metric.Percentile: input not sorted ascending")
	}
	i := int(math.Floor(q * float64(len(sorted))))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	if i < 0 {
		i = 0
	}
	return sorted[i]
}
