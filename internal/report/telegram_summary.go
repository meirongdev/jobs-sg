package report

import (
	"fmt"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/view"
)

// TelegramSummary is what a job seeker gets in their feed on Monday morning.
//
// Reordered for that reader (spec §4.5): what got hotter, how many doors are
// open at their experience level, what the bands pay, and how fresh the data
// is. The previous version led with active-posting counts and a single median
// — an operator's summary of a pipeline, not an answer to "should I be looking
// this week".
func TelegramSummary(r *Report, baseURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🗓 *SG SWE — week %s*\n", r.WeekLabel)
	fmt.Fprintf(&b, "%d new postings", r.NewJobs)
	if r.PrevNewJobs > 0 {
		fmt.Fprintf(&b, " (%+.0f%% vs last week)", (float64(r.NewJobs)-float64(r.PrevNewJobs))/float64(r.PrevNewJobs)*100)
	}
	b.WriteString("\n")

	// Rising technologies, not the biggest — the ranking is on every page, the
	// change is the news. Silent when momentum lacks the history to be honest.
	if r.Tech != nil && !r.Tech.History.Suppressed && len(r.Tech.Rising) > 0 {
		b.WriteString("\n📈 *Heating up*\n")
		for i, t := range r.Tech.Rising {
			if i == 3 {
				break
			}
			fmt.Fprintf(&b, "· %s %s\n", t.Slug, view.PP(t.MomentumPP))
		}
	}

	if r.Market != nil {
		fmt.Fprintf(&b, "\n🚪 *%d entry-level* of %d new · %d on the board now\n",
			r.Market.EntryJobs, r.Market.NewJobs, r.Market.ActiveEntry)
	}

	// Pay by experience band rather than one median: "what do I get paid" has a
	// different answer at every rung, and a single figure answers it for nobody.
	if r.Pay != nil {
		var bands []string
		for _, band := range r.Pay.Ladder {
			if band.Coverage.Suppressed || band.P50 == 0 {
				continue
			}
			bands = append(bands, fmt.Sprintf("%s %s", band.Label, view.Money(band.P50)))
		}
		if len(bands) > 0 {
			fmt.Fprintf(&b, "\n💰 *Median by experience*\n%s\n", strings.Join(bands, " · "))
		}
	}

	fmt.Fprintf(&b, "\nData collected daily; lags the market by up to 24h.\n")
	fmt.Fprintf(&b, "%s/w/%s", strings.TrimRight(baseURL, "/"), r.WeekLabel)
	return b.String()
}
