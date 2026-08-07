package view

import (
	"bytes"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// payPage is parsed once at init so a syntax error fails the build's tests
// instead of surfacing as a 500 (the page renders live on every hit).
var payPage = template.Must(template.New("pay").Funcs(template.FuncMap{
	"bar":      Bar,
	"barmoney": BarMoney,
	"pct":      Pct,
	"money":    Money,
	"sup":      Suppressed,
	"nav":      Nav,
	"lens":     lensNav,
	"cell":     payCell,
	"bars":     ladderBars,
}).Parse(payTmpl))

// PayPage renders /pay.
func PayPage(r *metric.PayReport) (string, error) {
	var buf bytes.Buffer
	if err := payPage.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// payCell renders one grid cell: the median with its quartiles, or the
// suppression marker. Never a zero — an unmeasured cell must not read as a
// measured S$0.
func payCell(c metric.PayCell) template.HTML {
	if c.Coverage.Suppressed {
		return Suppressed(c.Coverage)
	}
	return template.HTML(`<strong>` + Money(c.P50) + `</strong><br><span class="sup">` +
		Money(c.P25) + `–` + Money(c.P75) + `</span>`)
}

// ladderBars charts the ladder's medians, skipping suppressed rungs so the
// chart cannot imply a measured zero.
func ladderBars(bands []metric.PayBand) []metric.KV {
	out := make([]metric.KV, 0, len(bands))
	for _, b := range bands {
		if b.Coverage.Suppressed {
			continue
		}
		out = append(out, metric.KV{Key: b.Label, Value: b.P50})
	}
	return out
}

const payTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pay · Singapore SWE jobs</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap wide">
<h1>What you are worth</h1>
<div class="sub">Advertised monthly salaries · trailing {{.Days}} days ({{.Window}}, SGT){{if .Lens.Label}} · {{.Lens.Label}}{{end}}</div>
{{nav "/pay"}}
{{lens "/pay" .Lens}}

<p class="note">Every figure below is a salary a posting actually advertised — quartiles are picked from the sample, never interpolated. Only {{pct .Salary.Pct}} of postings disclose a monthly salary ({{.Salary.Disclosed}} of {{.Salary.Total}}), so these describe that disclosing subset, not the market. Cells with fewer than {{.CellFloor}} disclosed salaries are withheld rather than shown: a quartile over a handful of postings is both false precision and close to publishing one employer's range.</p>

<h2>1. Median by seniority and role</h2>
<p class="note">Each cell: median on top, 25th–75th percentile below.</p>
<div class="scroll">
<table class="detail">
<tr><th>Seniority</th>{{range .Roles}}<th>{{.}}</th>{{end}}<th>All roles</th></tr>
{{range .Grid}}<tr>
  <td><strong>{{.Seniority}}</strong></td>
  {{range .Cells}}<td>{{cell .}}</td>{{end}}
  <td>{{cell .All}}</td>
</tr>{{end}}
<tr><td><strong>All levels</strong></td>{{range .RoleTotals}}<td>{{cell .}}</td>{{end}}<td>{{cell .Overall}}</td></tr>
</table>
</div>

<h2>2. Experience ladder</h2>
<p class="note">What another year of experience is worth. "0" means the posting explicitly asks for no experience; "unstated" means it did not say — for someone deciding whether to apply those are different answers, so they are never merged. This ladder is the experience dimension itself, so the experience filter above does not narrow it (the role filter does). Seniority in the grid above is a classification from the job title, level and stated experience; the rungs here are the stated experience alone, so the two do not line up row for row.</p>
{{if bars .Ladder}}{{barmoney (bars .Ladder) 5}}{{else}}<p class="mut">No rung has enough disclosed salaries to chart; the table below still lists each rung's posting count.</p>{{end}}
<table>
<tr><th>Years required</th><th>Postings</th><th>p25</th><th>Median</th><th>p75</th></tr>
{{range .Ladder}}<tr>
  <td>{{.Label}}</td><td>{{.Postings}}</td>
  {{if .Coverage.Suppressed}}<td colspan="3">{{sup .Coverage}}</td>
  {{else}}<td>{{money .P25}}</td><td><strong>{{money .P50}}</strong></td><td>{{money .P75}}</td>{{end}}
</tr>{{end}}
</table>

<h2>3. Who discloses pay</h2>
<p class="note">Salary transparency by employer type. A type with fewer than {{.CompanyFloor}} postings in the window is withheld rather than ranked on a handful.</p>
{{if .ByCompany}}<table>
<tr><th>Employer type</th><th>Postings</th><th>Disclose a salary</th></tr>
{{range .ByCompany}}<tr>
  <td>{{.CompanyType}}</td><td>{{.Total}}</td>
  <td>{{if .Coverage.Suppressed}}{{sup .Coverage}}{{else}}{{pct .Pct}} ({{.Disclosed}}){{end}}</td>
</tr>{{end}}
</table>{{else}}<p class="mut">No employer type has postings in this window.</p>{{end}}

<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
