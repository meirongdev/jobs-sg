package view

import (
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// docs/01 §5 makes the data-delay notice a red line, not a nicety: the site
// publishes daily-batch data to job seekers, and a page that reads as a live
// job source sends someone to apply for a posting that closed yesterday.
//
// Every outward page must carry it. This test covers the ones internal/view
// renders; the weekly report's own footer is pinned in internal/report.
func TestOutwardPagesDiscloseDataLag(t *testing.T) {
	notice, err := Notice("t", "h", "b", "/")
	if err != nil {
		t.Fatal(err)
	}
	tech, err := TechPage(&metric.TechReport{Week: "2026-W32"})
	if err != nil {
		t.Fatal(err)
	}
	pay, err := PayPage(&metric.PayReport{})
	if err != nil {
		t.Fatal(err)
	}
	market, err := MarketPage(&metric.MarketReport{Week: "2026-W32"})
	if err != nil {
		t.Fatal(err)
	}

	for name, html := range map[string]string{"notice": notice, "/tech": tech, "/pay": pay, "/": market} {
		if !strings.Contains(html, "lags the live market by up to 24h") {
			t.Errorf("%s does not disclose the data lag (docs/01 §5)", name)
		}
	}
}
