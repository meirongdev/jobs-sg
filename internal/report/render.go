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
		"pct":    pct,
		"money":  money,
		"topn":   topn,
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
		"pct":   pct,
		"money": money,
		"fmtKV": fmtKV,
		"topn":  topn,
	}).Parse(mdTmpl))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func pct(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
}

func money(f float64) string {
	if f == 0 {
		return "n/a"
	}
	return fmt.Sprintf("S$%.0f", f)
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

func topn(kvs []KV, n int) []KV {
	if len(kvs) > n {
		return kvs[:n]
	}
	return kvs
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

<h2>1. Executive Snapshot</h2>
<div class="cards">
  <div class="card"><div class="n">{{.NewJobs}}</div><div class="k">New SWE jobs</div></div>
  <div class="card"><div class="n">{{if .PrevNewJobs}}{{printf "%+.1f" (mulPct .NewJobs .PrevNewJobs)}}%{{else}}—{{end}}</div><div class="k">WoW change</div></div>
  <div class="card"><div class="n">{{.ActiveJobs}}</div><div class="k">Active postings</div></div>
  <div class="card"><div class="n">{{.TopRole}}</div><div class="k">Hottest role family</div></div>
</div>

<h2>2. Hiring Trends</h2>
<h3>Role families</h3><div>{{fmtKV .Roles}}</div>
<h3>Seniority</h3><div>{{fmtKV .Seniorities}}</div>
<h3>Work modes</h3><div>{{fmtKV .WorkModes}}</div>
<h3>Company types</h3><div>{{fmtKV .CompanyTypes}}</div>
<h3>Top 10 hiring companies</h3>
<table><tr><th>Company</th><th>Postings</th></tr>{{range .TopCompanies}}<tr><td>{{.Name}}</td><td>{{.Count}}</td></tr>{{end}}</table>

<h2>3. Tech Trends</h2>
<p>Frequency = number of new SWE postings mentioning the technology.</p>
{{bar .TopTechs 15}}

<h2>4. Compensation</h2>
<p>Monthly salary median (public salaries only): <strong>{{money .SalaryMedian}}</strong></p>
<table><tr><th>Role family</th><th>Median (Monthly)</th></tr>{{range .SalaryByRole}}<tr><td>{{.Key}}</td><td>{{money .Value}}</td></tr>{{end}}</table>

<h2>5. Demand Signals</h2>
<table>
<tr><th>Metric</th><th>Value</th></tr>
<tr><td>Avg views per posting</td><td>{{printf "%.0f" .AvgViews}}</td></tr>
<tr><td>Avg applications per posting</td><td>{{printf "%.1f" .AvgApps}}</td></tr>
<tr><td>Median competition (applications / vacancies)</td><td>{{printf "%.1f" .AvgCompetition}}</td></tr>
</table>

<h2>6. Skills-first</h2>
<p>Postings with no experience requirement: <strong>{{pct .NoExpRatio}}</strong></p>

<h2>7. Insights</h2>
<p>Rule-generated from computed numbers (LLM insights arrive with Phase 2 enrichment):
{{if .NewJobs}}This week saw {{.NewJobs}} new SWE postings{{else}}No new SWE postings recorded{{end}}
{{if .PrevNewJobs}}{{if gt .NewJobs .PrevNewJobs}}, up from {{.PrevNewJobs}} last week{{else if lt .NewJobs .PrevNewJobs}}, down from {{.PrevNewJobs}} last week{{end}}{{end}}.
{{if .TopRole}}The hottest role family was {{.TopRole}}{{end}}
{{if .TopTechs}}; leading tech: {{(index .TopTechs 0).Key}} ({{(index .TopTechs 0).Value}} postings){{end}}.
{{if eq .SalaryMedian 0.0}}Salary data was sparse this week.{{else}}Median advertised monthly salary was {{money .SalaryMedian}}.{{end}}</p>

<h2>8. Data Quality</h2>
<table>
<tr><th>Signal</th><th>Value</th></tr>
<tr><td>Last ingest</td><td>{{.DataQuality.IngestStatus}} · {{.DataQuality.IngestLast}}</td></tr>
<tr><td>Last reconcile</td><td>{{.DataQuality.ReconcileLast}}</td></tr>
<tr><td>Enrich status</td><td>{{.DataQuality.EnrichStatus}}</td></tr>
<tr><td>Enrich backlog</td><td>{{.DataQuality.EnrichBacklog}}</td></tr>
<tr><td>LLM calls / cache hits</td><td>{{.DataQuality.LLMCalls}} / {{.DataQuality.LLMCached}}</td></tr>
<tr><td>Unmapped tech terms</td><td>{{.DataQuality.UnmappedTech}}</td></tr>
</table>

<div class="foot">Numbers computed by SQL from public MyCareersFuture data. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate stats only, no personal data.</div>
</div></body></html>`

const mdTmpl = `# Singapore SWE Hiring Report — Week {{.WeekLabel}}

Week starting {{.WeekStart}} (SGT) · jobs.meirong.dev

## 1. Executive Snapshot
- New SWE jobs: **{{.NewJobs}}** (prev week: {{.PrevNewJobs}})
- Active postings: **{{.ActiveJobs}}**
- Hottest role family: **{{.TopRole}}**

## 2. Hiring Trends
- Roles: {{fmtKV .Roles}}
- Seniority: {{fmtKV .Seniorities}}
- Work modes: {{fmtKV .WorkModes}}
- Company types: {{fmtKV .CompanyTypes}}
- Top companies: {{range .TopCompanies}}{{.Name}} ({{.Count}}); {{end}}

## 3. Tech Trends
{{range topn .TopTechs 30}}- {{.Key}}: {{printf "%.0f" .Value}}{{end}}

## 4. Compensation
Monthly median (public): **{{money .SalaryMedian}}**
{{range .SalaryByRole}}- {{.Key}}: {{money .Value}}{{end}}

## 5. Demand Signals
- Avg views: {{printf "%.0f" .AvgViews}} · Avg applications: {{printf "%.1f" .AvgApps}} · Median competition: {{printf "%.1f" .AvgCompetition}}

## 6. Skills-first
Postings with no experience requirement: **{{pct .NoExpRatio}}**

## 7. Insights
Rule-generated summary of computed numbers (LLM insights in Phase 2).

## 8. Data Quality
- Ingest: {{.DataQuality.IngestStatus}} (last {{.DataQuality.IngestLast}})
- Reconcile: {{.DataQuality.ReconcileLast}}
- Enrich: {{.DataQuality.EnrichStatus}} · backlog {{.DataQuality.EnrichBacklog}} · LLM calls {{.DataQuality.LLMCalls}} / cache hits {{.DataQuality.LLMCached}}
- Unmapped tech: {{.DataQuality.UnmappedTech}}
`

func mulPct(cur, prev int) float64 {
	if prev == 0 {
		return math.NaN()
	}
	return (float64(cur) - float64(prev)) / float64(prev) * 100
}
