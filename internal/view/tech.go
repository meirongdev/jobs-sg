package view

import (
	"bytes"
	"html/template"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

// techPage is parsed once at init so a template syntax error fails the build's
// tests instead of surfacing as a 500 (the page renders live on every hit).
var techPage = template.Must(template.New("tech").Funcs(template.FuncMap{
	"bar":   Bar,
	"pct":   Pct,
	"pp":    PP,
	"spct":  SignedPct,
	"money": Money,
	"sup":   Suppressed,
	"nav":   Nav,
	"lens":  lensNav,
	"kvs":   techBars,
}).Parse(techTmpl))

// TechPage renders /tech.
func TechPage(r *metric.TechReport) (string, error) {
	var buf bytes.Buffer
	if err := techPage.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// techBars projects the demand ranking onto the bar chart's input.
func techBars(stats []metric.TechStat, n int) []metric.KV {
	out := make([]metric.KV, 0, len(stats))
	for _, s := range stats {
		out = append(out, metric.KV{Key: s.Slug, Value: float64(s.Count)})
	}
	return TopN(out, n)
}

// lensNav renders the experience/role pickers, marking the active values.
func lensNav(page string, active metric.Lens) template.HTML {
	var b bytes.Buffer
	b.WriteString(`<div class="lens">Experience: `)
	writeLensLink(&b, page, metric.Lens{Role: active.Role}, "all", active.Exp == "")
	for _, band := range metric.ExpBands() {
		writeLensLink(&b, page, metric.Lens{Exp: band, Role: active.Role}, band, active.Exp == band)
	}
	b.WriteString(`</div><div class="lens">Role: `)
	writeLensLink(&b, page, metric.Lens{Exp: active.Exp}, "all", active.Role == "")
	for _, role := range metric.RoleFamilies() {
		writeLensLink(&b, page, metric.Lens{Exp: active.Exp, Role: role}, role, active.Role == role)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func writeLensLink(b *bytes.Buffer, page string, l metric.Lens, label string, on bool) {
	q := ""
	if l.Exp != "" {
		q = "?exp=" + template.URLQueryEscaper(l.Exp)
	}
	if l.Role != "" {
		sep := "?"
		if q != "" {
			sep = "&"
		}
		q += sep + "role=" + template.URLQueryEscaper(l.Role)
	}
	class := ""
	if on {
		class = ` class="on"`
	}
	b.WriteString(`<a href="` + page + q + `"` + class + `>` + template.HTMLEscapeString(label) + `</a>`)
}

const techTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Tech Demand · Singapore SWE jobs</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap">
<h1>Tech Demand</h1>
<div class="sub">What is worth learning · reported week {{.Week}} (last completed ISO week, SGT){{if .Lens.Label}} · {{.Lens.Label}}{{end}}</div>
{{nav "/tech"}}
{{lens "/tech" .Lens}}

<h2>1. Demand ranking</h2>
<p class="note">{{.Denom}} enriched SWE postings in {{.Week}}. Share = postings mentioning the technology ÷ that number — postings still awaiting enrichment are excluded from the denominator, so a processing backlog cannot depress every share at once. The chart shows the top 15; the table in section 3 lists the top 30.</p>
{{if .Ranked}}{{bar (kvs .Ranked 15) 15}}{{else}}<p class="mut">No enriched postings in {{.Week}}.</p>{{end}}

<h2>2. Momentum (vs the previous 4 weeks)</h2>
{{if .History.Suppressed}}<p class="note">{{sup .History}} — momentum compares the reported week against the 4 completed weeks before it.</p>
{{else if not .MomentumEligible}}
<p class="note">No technology cleared the momentum bar this week ({{.MomentumFloor}}+ postings{{if .Lens.Label}} under this lens{{end}}): with counts that thin, a share delta would read noise as trend, so the boards stay empty rather than calling the market flat.</p>
{{else}}
<h3>Heating up</h3>
{{if .Rising}}<table><tr><th>Technology</th><th>Share</th><th>Change</th><th>Postings</th></tr>
{{range .Rising}}<tr><td>{{.Slug}}</td><td>{{pct .Share}}</td><td class="up">{{pp .MomentumPP}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{else}}<p class="mut">Nothing rose this week.</p>{{end}}
<h3>Cooling down</h3>
{{if .Falling}}<table><tr><th>Technology</th><th>Share</th><th>Change</th><th>Postings</th></tr>
{{range .Falling}}<tr><td>{{.Slug}}</td><td>{{pct .Share}}</td><td class="down">{{pp .MomentumPP}}</td><td>{{.Count}}</td></tr>{{end}}</table>
{{else}}<p class="mut">Nothing fell this week.</p>{{end}}
<p class="note">Change is in percentage points of share, not relative percent: a technology going from 1 to 3 postings would otherwise read as +200% and top the board. Boards consider every technology above the bar, not only the 30 the table below shows.</p>
{{end}}

<h2>3. Salary premium and entry-friendliness</h2>
<p class="note">Premium compares the median advertised monthly salary of postings mentioning a technology against the overall median, over the trailing 90 days. Baseline: <strong>{{money .MedianAll}}</strong>, the median of {{.MedianSample}} postings advertising a monthly range — medians pin the unit so the figures are comparable. Separately, {{pct .Salary.Pct}} of SWE postings state pay at all ({{.Salary.Disclosed}} of {{.Salary.Total}}, in any unit); the rest hide it, and no figure here describes them. Entry-friendly is computed over the same 90-day window. Premium mixes seniority in (senior roles name more infrastructure); pick an experience band above to compare within one. Entry-friendly = the share of postings mentioning the technology that ask for at most 2 years' experience, or are Intern/Junior roles with no stated requirement. The table lists the top 30 technologies by postings.</p>
{{if .Ranked}}
<table>
<tr><th>Technology</th><th>Kind</th><th>Postings</th><th>Share</th><th>Salary premium</th><th>Entry-friendly</th></tr>
{{range .Ranked}}<tr>
  <td>{{.Slug}}</td><td class="mut">{{.Kind}}</td><td>{{.Count}}</td><td>{{pct .Share}}</td>
  <td>{{if .Premium.Suppressed}}{{sup .Premium}}{{else}}{{spct .PremiumPct}}{{end}}</td>
  <td>{{pct .EntryFriendly}}</td>
</tr>{{end}}
</table>
{{else}}<p class="mut">No enriched postings in {{.Week}}.</p>{{end}}

<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
