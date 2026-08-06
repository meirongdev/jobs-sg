package view

import (
	"fmt"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// Suppressed renders why a number is being withheld, and returns "" when the
// number is fine to show. A suppressed value is never rendered as 0: a
// fabricated zero reads as a real measurement, an admitted gap does not.
func Suppressed(c metric.Coverage) template.HTML {
	if !c.Suppressed {
		return ""
	}
	switch c.Reason {
	case metric.ReasonHistory:
		return template.HTML(fmt.Sprintf(
			`<span class="sup">needs %d weeks of history · have %d</span>`,
			c.WeeksRequired, c.WeeksAvailable))
	default:
		return template.HTML(fmt.Sprintf(`<span class="sup">—(n=%d)</span>`, c.Samples))
	}
}

// Pct formats a share as a percentage.
func Pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

// PP formats a percentage-point delta with an explicit sign.
func PP(f float64) string { return fmt.Sprintf("%+.1fpp", f*100) }

// SignedPct formats a relative change as an explicitly signed percentage.
// Not PP: percentage points are the unit of share deltas; a ratio-minus-one
// is a relative percent, and labeling it "pp" would lie about the unit.
func SignedPct(f float64) string { return fmt.Sprintf("%+.1f%%", f*100) }

// Money formats a monthly salary, or "n/a" when absent.
func Money(f float64) string {
	if f == 0 {
		return "n/a"
	}
	return fmt.Sprintf("S$%.0f", f)
}
