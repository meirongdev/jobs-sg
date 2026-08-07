package view

import (
	"bytes"
	"fmt"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// companyPage is parsed once at init so a template syntax error fails the
// build's tests instead of surfacing as a 500.
var companyPage = template.Must(template.New("companies").Funcs(template.FuncMap{
	"bar":    Bar,
	"pct":    Pct,
	"sup":    Suppressed,
	"nav":    Nav,
	"lens":   lensNav,
	"commas": Commas,
	"rate":   Rate,
	"days":   Days,
}).Parse(companyTmpl))

// CompanyPage renders /companies.
func CompanyPage(r *metric.CompanyReport) (string, error) {
	var buf bytes.Buffer
	if err := companyPage.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Rate formats a per-day figure. Two decimals because these run below 1 for
// most postings, and rounding to whole numbers would print every discipline
// as "0".
func Rate(f float64) string { return fmt.Sprintf("%.2f", f) }

// Days formats a listing length.
func Days(f float64) string { return fmt.Sprintf("%.0f days", f) }

const companyTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Employers · Singapore SWE jobs</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap wide">
<h1>Employers</h1>
<div class="sub">Who is hiring, and what applying looks like · trailing {{.Days}} days ({{.Window}}, SGT){{if .Lens.Label}} · {{.Lens.Label}}{{end}}</div>
{{nav "/companies"}}
{{lens "/companies" .Lens}}

<h2>1. Who is posting</h2>
<p class="note">Grouped by UEN, Singapore's legal-entity number — two spellings of one employer are one row. Per-employer figures are withheld below {{.Floor}} postings: one posting's applicant count is not a company's character, and a disclosure rate over three postings says nothing about its policy. "Applications/day" divides each posting's applicant count by how long it had been listed, then takes the median across the employer's postings.</p>
{{if .Top}}
<div class="scroll">
<table class="detail">
<tr><th>Employer</th><th>Type</th><th class="n">Postings</th><th class="n">On board now</th><th class="n">Applications/day</th><th>States pay</th></tr>
{{range .Top}}<tr>
  <td>{{.Name}}</td><td class="mut">{{if .CompanyType}}{{.CompanyType}}{{else}}—{{end}}</td>
  <td>{{.Postings}}</td><td>{{.ActiveNow}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{rate .AppsPerDay}}{{end}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{pct .Salary.Pct}} ({{.Salary.Disclosed}}/{{.Salary.Total}}){{end}}</td>
</tr>{{end}}
</table>
</div>
{{else}}<p class="mut">No employers with postings in this window.</p>{{end}}

{{if .ByType}}
<h3>By employer type</h3>
{{bar .ByType 8}}
<p class="note">Type is derived from the company's SSIC industry code and headcount, not self-declared — read it as a bucket, not a badge.</p>
{{end}}

<h2>2. How long postings stay up</h2>
{{if .Lifetime.Coverage.Suppressed}}
<p class="note">{{sup .Lifetime.Coverage}} — listing length needs at least {{.Lifetime.Coverage.Samples}} closed postings before a median means anything. Postings have to come down before they can be measured, so this fills in as the site accumulates history.</p>
{{else}}
<div class="cards">
  <div class="card"><div class="n">{{days .Lifetime.MedianDays}}</div><div class="k">Median, closed postings</div></div>
  <div class="card"><div class="n">{{commas .Lifetime.Closed}}</div><div class="k">Postings measured</div></div>
  <div class="card"><div class="n">{{commas .Lifetime.StillOpen}}</div><div class="k">Still up, not measured</div></div>
</div>
<p class="note"><strong>This is how long closed postings stayed up — not how long a posting lasts.</strong> A posting still on the board has no end date, so it cannot be counted, and the ones that linger are exactly the ones missing. The figure therefore runs short, and the {{commas .Lifetime.StillOpen}} still up are the size of what it cannot see. Resolution is one day: collection is a daily batch.</p>
<table>
<tr><th>Listed for</th><th class="n">Postings</th><th class="n">Share</th></tr>
{{range .Lifetime.Bands}}<tr><td>{{.Label}}</td><td>{{.Count}}</td><td>{{pct .Share}}</td></tr>{{end}}
</table>
{{end}}

{{if .Ghost.HasSignal}}
<h3>Postings worth a second look</h3>
<p class="note">Of {{commas .Ghost.Active}} postings on the board, <strong>{{pct .Ghost.StaleShare}}</strong> have been listed over {{.Ghost.StaleDaysCut}} days ({{commas .Ghost.StaleOver60}}) and <strong>{{pct .Ghost.RepostShare}}</strong> have been reposted at least once ({{commas .Ghost.Reposted}}). Signals, not verdicts — a genuinely hard-to-fill role looks the same from outside as a listing nobody intends to close.</p>
{{end}}

<h2>3. How contested</h2>
<p class="note">Median applications and views per day, by discipline, over the trailing {{.Days}} days. Both counters are cumulative totals read at collection time, so they are divided by how long each posting had been listed — otherwise the figure measures how old a posting is rather than how contested. Withheld below {{.Floor}} postings.</p>
{{if .Competition}}
<table>
<tr><th>Discipline</th><th class="n">Postings</th><th class="n">Applications/day</th><th class="n">Views/day</th><th class="n">Apply rate</th></tr>
{{range .Competition}}<tr>
  <td>{{.Key}}</td><td>{{.Postings}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{rate .AppsPerDay}}{{end}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{rate .ViewsPerDay}}{{end}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{pct .Conversion}}{{end}}</td>
</tr>{{end}}
</table>
<p class="note"><strong>These are snapshots, not trends.</strong> Each collection overwrites the view and application counters, so no history is kept — "applications this week" and "competition over time" are questions this system cannot answer, and nothing here should be read as movement.</p>
{{else}}<p class="mut">No postings with demand counters in this window.</p>{{end}}

<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
