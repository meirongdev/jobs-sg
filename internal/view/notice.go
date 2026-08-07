package view

import (
	"bytes"
	"html/template"
)

// noticePage is parsed once at init so a template error fails the build's tests
// rather than surfacing as a 500 on the page that exists to explain a problem.
var noticePage = template.Must(template.New("notice").Funcs(template.FuncMap{
	"nav": Nav,
}).Parse(noticeTmpl))

// Notice renders a standing-in page: the site's own chrome plus a sentence
// saying what is not here and where to go instead.
//
// It exists because the weekly report is written by a CronJob, so between a
// fresh deploy and the first Monday 09:00 SGT run there is no latest.html — and
// "/" is the first item in the nav every other page renders. Serving net/http's
// bare "404 page not found" there hands a visitor an unstyled dead end with no
// way back into the site, on a site whose own spec (§5) makes "not enough data
// yet" a first-class state everywhere else.
//
// active marks the nav entry to light up, or "" for none.
func Notice(title, heading, body, active string) (string, error) {
	var buf bytes.Buffer
	err := noticePage.Execute(&buf, struct {
		Title, Heading, Body, Active string
	}{title, heading, body, active})
	return buf.String(), err
}

const noticeTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Singapore SWE jobs</title>
<style>` + BaseCSS + SuppressedCSS + `</style>
</head>
<body><div class="wrap">
<h1>{{.Heading}}</h1>
<div class="sub">{{.Body}}</div>
{{nav .Active}}
<div class="foot">Numbers computed by SQL from public MyCareersFuture data; data is refreshed daily, so it lags the live market by up to 24h. Methodology: docs/03-data-model.md · <a href="/ops">data freshness</a> · Compliance: aggregate statistics only, no personal data.</div>
</div></body></html>`
