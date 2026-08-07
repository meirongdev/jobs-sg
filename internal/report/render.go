package report

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/view"
)

// RenderHTML produces a self-contained HTML report (inline CSS + SVG, no
// external resources — docs/02 §4.3).
func RenderHTML(r *Report) (string, error) {
	tmpl := template.Must(template.New("report").Funcs(template.FuncMap{
		"bar":    view.Bar,
		"pct":    view.Pct,
		"pp":     view.PP,
		"money":  view.Money,
		"rate":   view.Rate,
		"days":   view.Days,
		"sup":    view.Suppressed,
		"topn":   view.TopN,
		"fmtKV":  fmtKV,
		"mulPct": mulPct,
		"nav":    view.Nav,
	}).Parse(htmlTmpl))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RenderMarkdown produces a Markdown twin of the report.
func RenderMarkdown(r *Report) (string, error) {
	tmpl := template.Must(template.New("md").Funcs(template.FuncMap{
		"pct":   view.Pct,
		"pp":    view.PP,
		"money": view.Money,
		"rate":  view.Rate,
		"days":  view.Days,
		"fmtKV": fmtKV,
		"topn":  view.TopN,
	}).Parse(mdTmpl))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func fmtKV(kvs []KV) string {
	var parts []string
	for _, kv := range kvs {
		parts = append(parts, fmt.Sprintf("%s: %d", kv.Key, int(kv.Value)))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

const htmlTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>SWE Market Report {{.WeekLabel}}</title>
<style>` + view.BaseCSS + `</style>
</head>
<body><div class="wrap">
<h1>Singapore SWE Hiring Report</h1>
<div class="sub">Week {{.WeekLabel}} (ISO week starting {{.WeekStart}}, SGT) · jobs.meirong.dev</div>
{{nav "/"}}

<h2>1. Snapshot</h2>
<div class="cards">
  <div class="card"><div class="n">{{.NewJobs}}</div><div class="k">New SWE postings</div></div>
  <div class="card"><div class="n">{{if .PrevNewJobs}}{{printf "%+.1f" (mulPct .NewJobs .PrevNewJobs)}}%{{else}}—{{end}}</div><div class="k">vs the week before</div></div>
  <div class="card"><div class="n">{{.ActiveJobs}}</div><div class="k">On the board at publication</div></div>
  <div class="card"><div class="n">{{.TopRole}}</div><div class="k">Busiest discipline</div></div>
</div>
<h3>Where the demand was</h3>
<div>{{fmtKV .Roles}}</div>
<div>{{fmtKV .Seniorities}}</div>
<div>{{fmtKV .WorkModes}}</div>

<h2>2. Technology</h2>
<p>Share = postings mentioning the technology ÷ the {{.Tech.Denom}} enriched SWE postings this week. Postings still awaiting enrichment are excluded from the denominator, so a processing backlog cannot depress every share at once.</p>
{{bar .TopTechs 15}}
{{if .Tech.History.Suppressed}}
<p class="note">Momentum needs {{.Tech.History.WeeksRequired}} weeks of history; {{.Tech.History.WeeksAvailable}} available. It fills in as the archive grows.</p>
{{else if .Tech.Rising}}
<h3>Heating up</h3>
<table><tr><th>Technology</th><th>Share</th><th>Change</th><th>Postings</th></tr>
{{range .Tech.Rising}}<tr><td>{{.Slug}}</td><td>{{pct .Share}}</td><td>{{pp .MomentumPP}}</td><td>{{.Count}}</td></tr>{{end}}</table>
<p class="note">Change is in percentage points of share, not relative percent: a technology going from 1 to 3 postings would otherwise read as +200%.</p>
{{end}}
{{if .Tech.Falling}}
<h3>Cooling down</h3>
<table><tr><th>Technology</th><th>Share</th><th>Change</th><th>Postings</th></tr>
{{range .Tech.Falling}}<tr><td>{{.Slug}}</td><td>{{pct .Share}}</td><td>{{pp .MomentumPP}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{end}}

<h2>3. Pay</h2>
<p>Advertised monthly salary by stated experience, over the trailing {{.Pay.Days}} days ending with this week ({{.Pay.Window}}). {{pct .Pay.Salary.Pct}} of SWE postings state pay at all ({{.Pay.Salary.Disclosed}} of {{.Pay.Salary.Total}}, in any unit); every figure below describes only the narrower subset advertising a monthly range. Cells over fewer than {{.Pay.CellFloor}} postings are withheld rather than shown imprecisely.</p>
<table>
<tr><th>Experience</th><th>p25</th><th>Median</th><th>p75</th><th>Postings</th></tr>
{{range .Pay.Ladder}}<tr>
  <td>{{.Label}}</td>
  {{if .Coverage.Suppressed}}<td colspan="3">{{sup .Coverage}}</td>
  {{else}}<td>{{money .P25}}</td><td>{{money .P50}}</td><td>{{money .P75}}</td>{{end}}
  <td>{{.Postings}}</td>
</tr>{{end}}
</table>
<p class="note">"Unstated" is kept apart from "0 years": one means the employer said no experience is needed, the other means they did not say. For a reader those are different answers.</p>

<h2>4. Getting in</h2>
<p>Entry-level means at most 2 years required, or an Intern/Junior title with no stated requirement. <strong>{{.Market.EntryJobs}}</strong> of {{.Market.NewJobs}} new postings this week qualified, and <strong>{{.Market.ActiveEntry}}</strong> were on the board at publication.</p>
{{if .Market.EntryByRole}}{{bar .Market.EntryByRole 10}}{{end}}

<h2>5. Competition and listing length</h2>
{{if .Company.Competition}}
<table>
<tr><th>Discipline</th><th>Postings</th><th>Applications/day</th><th>Views/day</th><th>Apply rate</th></tr>
{{range .Company.Competition}}<tr>
  <td>{{.Key}}</td><td>{{.Postings}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{rate .AppsPerDay}}{{end}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{rate .ViewsPerDay}}{{end}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{pct .Conversion}}{{end}}</td>
</tr>{{end}}
</table>
<p class="note">Both counters are cumulative totals read at collection time, so they are divided by how long each posting had been listed — otherwise the figure measures how old a posting is rather than how contested. <strong>Snapshots, not trends</strong>: collection overwrites these counters, so no history is kept.</p>
{{end}}
{{if not .Company.Lifetime.Coverage.Suppressed}}
<p><strong>{{days .Company.Lifetime.MedianDays}}</strong> — median time a <em>closed</em> posting stayed up, over {{.Company.Lifetime.Closed}} postings that came down. {{.Company.Lifetime.StillOpen}} are still up and cannot be measured; since the ones that linger are exactly the ones excluded, this figure runs short. It is not "how long a posting lasts".</p>
{{end}}

<h2>6. Employers</h2>
<h3>Most postings this week</h3>
<table><tr><th>Employer</th><th>Postings</th></tr>{{range .TopCompanies}}<tr><td>{{.Name}}</td><td>{{.Count}}</td></tr>{{end}}</table>
<h3>By employer type</h3><div>{{fmtKV .CompanyTypes}}</div>

<h2>7. About these numbers</h2>
<p>Every figure is computed by SQL from public MyCareersFuture postings. The language model reads job descriptions to extract technology names and never produces a number (docs/01 §4). Where a sample is too thin to mean anything, the figure is withheld rather than rounded to zero.</p>

<div class="foot">Collected daily, so figures lag the live market by up to 24h and this report is a snapshot of the week it covers — not a live job source. Pipeline at publication: ingest {{.DataQuality.IngestStatus}} ({{.DataQuality.IngestLast}}) · enrich {{.DataQuality.EnrichStatus}}, backlog {{.DataQuality.EnrichBacklog}} · <a href="/ops">full collection history</a>. Methodology: docs/03-data-model.md · Compliance: aggregate stats only, no personal data.</div>
</div></body></html>`

const mdTmpl = `# Singapore SWE Hiring Report — Week {{.WeekLabel}}

Week starting {{.WeekStart}} (SGT) · jobs.meirong.dev

## 1. Snapshot
- New SWE postings: **{{.NewJobs}}** (previous week: {{.PrevNewJobs}})
- On the board at publication: **{{.ActiveJobs}}**
- Busiest discipline: **{{.TopRole}}**
- Roles: {{fmtKV .Roles}}
- Seniority: {{fmtKV .Seniorities}}
- Work modes: {{fmtKV .WorkModes}}

## 2. Technology
Share is out of {{.Tech.Denom}} enriched SWE postings this week.
{{range topn .TopTechs 30}}- {{.Key}}: {{printf "%.0f" .Value}}
{{end}}{{if .Tech.History.Suppressed}}
Momentum needs {{.Tech.History.WeeksRequired}} weeks of history; {{.Tech.History.WeeksAvailable}} available.
{{else}}{{if .Tech.Rising}}
Heating up: {{range .Tech.Rising}}{{.Slug}} {{pp .MomentumPP}}; {{end}}
{{end}}{{if .Tech.Falling}}Cooling down: {{range .Tech.Falling}}{{.Slug}} {{pp .MomentumPP}}; {{end}}
{{end}}{{end}}
## 3. Pay
Trailing {{.Pay.Days}} days ({{.Pay.Window}}). {{pct .Pay.Salary.Pct}} of postings state pay ({{.Pay.Salary.Disclosed}}/{{.Pay.Salary.Total}}).
{{range .Pay.Ladder}}- {{.Label}}: {{if .Coverage.Suppressed}}withheld (n={{.Coverage.Samples}}){{else}}{{money .P25}} / {{money .P50}} / {{money .P75}}{{end}} over {{.Postings}} postings
{{end}}
"Unstated" is kept apart from "0 years" — one means no experience is needed, the other means the employer did not say.

## 4. Getting in
**{{.Market.EntryJobs}}** of {{.Market.NewJobs}} new postings were entry-level; **{{.Market.ActiveEntry}}** were on the board at publication.

## 5. Competition and listing length
{{range .Company.Competition}}- {{.Key}}: {{if .Coverage.Suppressed}}withheld{{else}}{{rate .AppsPerDay}} applications/day, {{rate .ViewsPerDay}} views/day, {{pct .Conversion}} apply rate{{end}}
{{end}}Counters are cumulative and overwritten each collection: these are snapshots, not trends.
{{if not .Company.Lifetime.Coverage.Suppressed}}
Median time a **closed** posting stayed up: **{{days .Company.Lifetime.MedianDays}}** over {{.Company.Lifetime.Closed}} postings; {{.Company.Lifetime.StillOpen}} still up and unmeasurable, so this runs short.
{{end}}
## 6. Employers
- Most postings: {{range .TopCompanies}}{{.Name}} ({{.Count}}); {{end}}
- By type: {{fmtKV .CompanyTypes}}

## 7. About these numbers
Every figure is computed by SQL. The language model extracts technology names from descriptions and never produces a number.

Collected daily, so figures lag the live market by up to 24h; this is a snapshot of the week it covers, not a live job source.
Pipeline at publication: ingest {{.DataQuality.IngestStatus}} ({{.DataQuality.IngestLast}}) · enrich {{.DataQuality.EnrichStatus}}, backlog {{.DataQuality.EnrichBacklog}}. Full collection history: /ops
`

func mulPct(cur, prev int) float64 {
	if prev == 0 {
		return math.NaN()
	}
	return (float64(cur) - float64(prev)) / float64(prev) * 100
}
