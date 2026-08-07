package report

import (
	"bytes"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"github.com/meirongdev/jobs-sg/internal/view"
)

// Templates are parsed once at init, not per request: a syntax error then
// fails the build's tests instead of surfacing as a 500 in production, and the
// daily pages are rendered live on every hit.
var (
	dailyPage = template.Must(newDailyTemplate("overview").Parse(dailyTmpl))
	dayPage   = template.Must(newDailyTemplate("day").Parse(dayTmpl))
)

func newDailyTemplate(name string) *template.Template {
	return template.New(name).Funcs(template.FuncMap{
		"bar":    view.Bar,
		"col":    view.Column,
		"fmtKV":  fmtKV,
		"pill":   statusPill,
		"kinds":  kindBadges,
		"dur":    humanDuration,
		"runAgo": runAgo,
		"nav":    view.Nav,
	})
}

// RenderDailyOverviewHTML renders the /ops index: one row per SGT day of
// crawl activity. Self-contained (inline CSS + SVG, no external resources —
// docs/02 §4.3), same constraint as the weekly report.
func RenderDailyOverviewHTML(o *DailyOverview) (string, error) {
	return execDaily(dailyPage, o)
}

// RenderDayDetailHTML renders the /ops/{YYYY-MM-DD} drill-down.
func RenderDayDetailHTML(d *DayDetail) (string, error) {
	return execDaily(dayPage, d)
}

func execDaily(tmpl *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// statusPill colours a run status; an empty status means the day had no run
// at all, which is itself the signal worth seeing.
func statusPill(status string) template.HTML {
	label, class := status, "s-"+status
	if status == "" {
		label, class = "no run", "s-idle"
	}
	return template.HTML(fmt.Sprintf(`<span class="pill %s">%s</span>`,
		class, template.HTMLEscapeString(label)))
}

// kindBadges labels a day's run kinds in pipeline order (ingest → enrich →
// report) regardless of the order they were folded in.
func kindBadges(kinds []string) template.HTML {
	if len(kinds) == 0 {
		return template.HTML(`<span class="mut">—</span>`)
	}
	short := map[string]string{
		"incremental":    "incr",
		"full_reconcile": "reconcile",
		"enrich":         "enrich",
		"report":         "report",
	}
	order := map[string]int{"incremental": 0, "full_reconcile": 1, "enrich": 2, "report": 3}
	sorted := slices.Clone(kinds)
	slices.SortStableFunc(sorted, func(a, b string) int { return order[a] - order[b] })

	var b strings.Builder
	for _, k := range sorted {
		s := short[k]
		if s == "" {
			s = k
		}
		fmt.Fprintf(&b, `<span class="kind">%s</span>`, template.HTMLEscapeString(s))
	}
	return template.HTML(b.String())
}

func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// runAgo describes how stale the last run is — the first thing you want to
// know when the page looks empty.
func runAgo(r *RunRow) string {
	if r == nil {
		return "no runs recorded"
	}
	when := r.StartedAt
	if !r.EndedAt.IsZero() {
		when = r.EndedAt
	}
	return fmt.Sprintf("%s · %s", r.Kind, when.In(sgt).Format("2006-01-02 15:04 SGT"))
}

// dailyCSS extends view.BaseCSS with the bits only the daily pages need.
const dailyCSS = `
.pill{display:inline-block;padding:1px 8px;border-radius:999px;font-size:12px;font-weight:600;white-space:nowrap}
.s-success{background:#064e3b;color:#6ee7b7}.s-partial{background:#78350f;color:#fcd34d}
.s-failed{background:#7f1d1d;color:#fca5a5}.s-running{background:#1e3a8a;color:#93c5fd}
.s-idle{background:#334155;color:#94a3b8}
.kind{display:inline-block;background:#334155;color:#cbd5e1;border-radius:4px;padding:1px 6px;margin-right:4px;font-size:12px;white-space:nowrap}
.wide{max-width:1160px}
.scroll{overflow-x:auto;-webkit-overflow-scrolling:touch}
.scroll table{min-width:840px}table.detail td,table.detail th{padding:5px 8px;font-size:14px;white-space:nowrap}
td.n,th.n{text-align:right;font-variant-numeric:tabular-nums}
tr.idle td{color:var(--mut)}tr:hover td{background:#1e293b}
/* The sticky first column in view.SuppressedCSS sets its own opaque background
   (0,2,2), which outranks the row-hover rule above (0,1,2). Restore hover on
   that column with a matching-specificity rule; it lives here rather than
   beside the sticky rule because only these pages have row hover at all. */
tr:hover td:first-child{background:#1e293b}
td a{color:#60a5fa;text-decoration:none}td a:hover{text-decoration:underline}
.swe{color:#6ee7b7;font-weight:600}
.pager{margin-top:18px;font-size:14px}.pager a{color:#60a5fa;text-decoration:none;margin-right:14px}
`

const dailyFoot = `<div class="foot">Counters come from <code>ingest_run</code>; stored/closed counts from <code>job</code>, bucketed by SGT calendar day.
Numbers are computed by SQL from public MyCareersFuture data. Methodology: docs/03-data-model.md · Compliance: public job and company data only, no personal data.</div>
</div></body></html>`

const dailyTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Daily Crawl Stats · jobs-sg</title>
<style>` + view.BaseCSS + view.SuppressedCSS + dailyCSS + `</style>
</head>
<body><div class="wrap wide">
<h1>Daily Crawl Statistics</h1>
<div class="sub">{{.DaysLabel}} · {{.From}} → {{.To}} (SGT) · rendered {{.Generated}}</div>
{{nav ""}}

<div class="cards">
  <div class="card"><div class="n">{{.NewSWE7d}}</div><div class="k">New SWE postings (7d)</div></div>
  <div class="card"><div class="n">{{.Archived7d}}</div><div class="k">Postings archived (7d)</div></div>
  <div class="card"><div class="n">{{.ActiveJobs}}</div><div class="k">Active postings</div></div>
  <div class="card"><div class="n">{{.EnrichBacklog}}</div><div class="k">Enrich backlog</div></div>
</div>
<p class="note">Last run: {{runAgo .LastRun}}{{if .LastRun}} · {{pill .LastRun.Status}}{{end}} · unmapped tech terms: {{.UnmappedTech}}</p>

<h2>1. New SWE postings per day</h2>
{{col .Trend "new SWE postings"}}

<h2>2. Daily crawl detail</h2>
<div class="scroll">
<table class="detail">
<tr>
  <th>Date (SGT)</th><th>Runs</th><th>Status</th><th class="n">Ingest time</th><th class="n">Pages</th>
  <th class="n">Archived</th><th class="n">New</th><th class="n">SWE</th><th class="n">Updated</th>
  <th class="n">Closed</th><th class="n">Errors</th><th class="n">LLM calls/hits</th>
</tr>
{{range .Days}}<tr{{if .Idle}} class="idle"{{end}}>
  <td><a href="/ops/{{.Date}}">{{.Date}}</a></td>
  <td>{{kinds .Kinds}}</td>
  <td>{{pill .Status}}</td>
  <td class="n">{{dur .Duration}}</td>
  <td class="n">{{.Pages}}</td>
  <td class="n">{{.Archived}}</td>
  <td class="n">{{.New}}</td>
  <td class="n">{{.NewSWE}}</td>
  <td class="n">{{.Updated}}</td>
  <td class="n">{{.Closed}}</td>
  <td class="n">{{.Errors}}</td>
  <td class="n">{{.LLMCalls}} / {{.LLMCached}}</td>
</tr>{{end}}
</table>
</div>
<p class="note">Archived = every posting written to <code>raw/</code> (all categories, before filtering). New = candidate postings first stored that day; SWE = the subset classified as software engineering. Closed = postings whose <code>closed_at</code> landed that day.</p>

<h2>3. Tech stack in postings crawled (last 7 days)</h2>
{{if .Techs}}{{bar .Techs 15}}{{else}}<p class="mut">No enriched postings in the window yet.</p>{{end}}
` + dailyFoot

const dayTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Crawl Detail {{.Date}} · jobs-sg</title>
<style>` + view.BaseCSS + view.SuppressedCSS + dailyCSS + `</style>
</head>
<body><div class="wrap wide">
<h1>Crawl Detail — {{.Date}}</h1>
<div class="sub">One SGT calendar day (02:15 SGT ingest, 03:10 SGT enrich)</div>
{{nav ""}}

<div class="cards">
  <div class="card"><div class="n">{{.Summary.Archived}}</div><div class="k">Postings archived</div></div>
  <div class="card"><div class="n">{{.Summary.New}}</div><div class="k">New candidates stored</div></div>
  <div class="card"><div class="n">{{.Summary.NewSWE}}</div><div class="k">Of which SWE</div></div>
  <div class="card"><div class="n">{{.Summary.Closed}}</div><div class="k">Postings closed</div></div>
</div>

<h2>1. Runs</h2>
{{if .Runs}}
<div class="scroll">
<table class="detail">
<tr>
  <th>Kind</th><th>Status</th><th>Start</th><th>End</th><th class="n">Duration</th><th class="n">Pages</th>
  <th class="n">Archived</th><th class="n">New</th><th class="n">Updated</th><th class="n">Closed</th>
  <th class="n">LLM calls/hits</th><th class="n">Errors</th><th>Watermark</th>
</tr>
{{range .Runs}}<tr>
  <td>{{.Kind}}</td>
  <td>{{pill .Status}}</td>
  <td>{{.StartedSGT}}</td>
  <td>{{.EndedSGT}}</td>
  <td class="n">{{.DurationLabel}}</td>
  <td class="n">{{.Pages}}</td>
  <td class="n">{{.Seen}}</td>
  <td class="n">{{.New}}</td>
  <td class="n">{{.Updated}}</td>
  <td class="n">{{.Closed}}</td>
  <td class="n">{{.LLMCalls}} / {{.LLMCached}}</td>
  <td class="n">{{.Errors}}</td>
  <td class="mut">{{if .Watermark}}{{.Watermark}}{{else}}—{{end}}</td>
</tr>{{end}}
</table>
</div>
{{else}}<p class="mut">No run started on this day.</p>{{end}}

<h2>2. What was crawled</h2>
<h3>Role families (new SWE postings)</h3><div>{{fmtKV .Roles}}</div>
<h3>Seniority (new SWE postings)</h3><div>{{fmtKV .Seniorities}}</div>

<h2>3. Tech stack in postings crawled this day</h2>
{{if .Techs}}{{bar .Techs 15}}{{else}}<p class="mut">No enriched postings for this day.</p>{{end}}

<h2>4. Postings first seen this day</h2>
{{if .Jobs}}
<p class="note">{{.JobsTotal}} candidate postings stored{{if .Truncated}}; showing the first {{len .Jobs}} (SWE first){{end}}.</p>
<div class="scroll">
<table class="detail">
<tr><th>Seen</th><th>Title</th><th>Company</th><th>Role</th><th>Seniority</th><th>Salary</th><th>Posted</th></tr>
{{range .Jobs}}<tr>
  <td class="mut">{{.FirstSeen}}</td>
  <td>{{if .IsSWE}}<span class="swe">●</span> {{end}}{{.Title}}</td>
  <td>{{if .Company}}{{.Company}}{{else}}<span class="mut">—</span>{{end}}</td>
  <td>{{if .RoleFamily}}{{.RoleFamily}}{{else}}<span class="mut">—</span>{{end}}</td>
  <td>{{if .Seniority}}{{.Seniority}}{{else}}<span class="mut">—</span>{{end}}</td>
  <td class="mut">{{.Salary}}</td>
  <td class="mut">{{.PostingDate}}</td>
</tr>{{end}}
</table>
</div>
<p class="note"><span class="swe">●</span> = classified as a software engineering role.</p>
{{else}}<p class="mut">No candidate postings were stored on this day.</p>{{end}}

<div class="pager">
  <a href="/ops/{{.Prev}}">← {{.Prev}}</a>
  {{if .Next}}<a href="/ops/{{.Next}}">{{.Next}} →</a>{{end}}
  <a href="/ops">All days</a>
</div>
` + dailyFoot
