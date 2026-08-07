package view

import (
	"bytes"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// marketPage is parsed once at init so a template syntax error fails the
// build's tests instead of surfacing as a 500 (the page renders live).
var marketPage = template.Must(template.New("market").Funcs(template.FuncMap{
	"bar":    Bar,
	"col":    Column,
	"spct":   SignedPct,
	"nav":    Nav,
	"lens":   lensNav,
	"topn":   TopN,
	"commas": Commas,
}).Parse(marketTmpl))

// MarketPage renders /.
func MarketPage(r *metric.MarketReport) (string, error) {
	var buf bytes.Buffer
	if err := marketPage.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const marketTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Singapore SWE jobs — the market right now</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap">
<h1>Singapore SWE Jobs</h1>
<div class="sub">How busy the market is · live counts as of now, weekly figures for {{.Week}} ({{.WeekRange}}, SGT){{if .Lens.Label}} · {{.Lens.Label}}{{end}}</div>
{{nav "/"}}
{{lens "/" .Lens}}

<div class="cards">
  <div class="card"><div class="n">{{commas .Active}}</div><div class="k">On the board now</div></div>
  <div class="card"><div class="n">{{commas .ActiveEntry}}</div><div class="k">…asking 0-2 years</div></div>
  <div class="card"><div class="n">{{commas .NewJobs}}</div><div class="k">New in {{.Week}}</div></div>
  <div class="card"><div class="n">{{if .HasWoW}}{{spct .WoW}}{{else}}—{{end}}</div><div class="k">{{if .HasWoW}}vs the week before{{else}}no previous week yet{{end}}</div></div>
</div>
<p class="note">"On the board now" counts postings that are neither taken down nor past their advertised closing date, whenever they were posted — it is a figure about today, not about {{.Week}}. Weekly figures cover the last <em>completed</em> ISO week: the week in progress is always partial, and reporting it would show a crash every Monday. Data is refreshed daily, so it lags the live market by up to 24h.</p>

<h2>1. Weekly new postings</h2>
<p class="note">The last {{len .Trend}} completed weeks. A week at zero is a real zero — either a quiet market or a pipeline that did not run, and <a href="/ops">data freshness</a> tells you which.</p>
{{if .Trend}}{{col .Trend ""}}{{else}}<p class="mut">No completed weeks with data yet.</p>{{end}}

<h2>2. Where the demand is</h2>
{{if .Roles}}
<h3>By discipline</h3>
{{bar .Roles 10}}
{{else}}<p class="mut">No classified postings in {{.Week}}.</p>{{end}}
{{if .Seniorities}}
<h3>By seniority</h3>
{{bar .Seniorities 10}}
<p class="note">Seniority is inferred from title, position level and stated years — read it as a band, not as a job ladder.</p>
{{end}}
{{if .WorkModes}}
<h3>By work arrangement</h3>
{{bar .WorkModes 5}}
<p class="note">Postings that say nothing about arrangement are counted as Onsite, which is what MCF's field implies but not what every employer means.</p>
{{end}}

<h2>3. Getting in</h2>
<p class="note">Entry-level means at most 2 years required, or an Intern/Junior title with no stated requirement. <strong>{{commas .EntryJobs}}</strong> of {{commas .NewJobs}} postings in {{.Week}} qualified, and <strong>{{commas .ActiveEntry}}</strong> are on the board right now. Counts, not shares: "{{commas .EntryJobs}} postings this week" is something you can act on.</p>
{{if .EntryByRole}}{{bar .EntryByRole 10}}{{else}}<p class="mut">No entry-level postings in {{.Week}}.</p>{{end}}

<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
