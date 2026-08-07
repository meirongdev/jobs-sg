package view

import (
	"html/template"
	"strings"
)

// navItem is one entry of the site's main navigation.
type navItem struct {
	Href  string
	Label string
}

// navItems is the job-seeker navigation, in reading order. This slice is the
// single source of truth: every page renders it through Nav, so adding a page
// is one edit here instead of one per template. Operational pages (/ops) stay
// out on purpose — they are linked from page footers as data-freshness
// evidence, not offered to someone looking for work.
var navItems = []navItem{
	{"/", "Market"},
	{"/tech", "Tech"},
	{"/pay", "Pay"},
	{"/companies", "Employers"},
	{"/reports", "Weekly report"},
}

// Nav renders the main navigation, marking active as the current page. Pass ""
// from pages that are not in the nav (the ops pages), and nothing lights up.
func Nav(active string) template.HTML {
	var b strings.Builder
	b.WriteString(`<nav class="nav">`)
	for _, it := range navItems {
		if it.Href == active {
			b.WriteString(`<a class="on" href="` + it.Href + `">`)
		} else {
			b.WriteString(`<a href="` + it.Href + `">`)
		}
		b.WriteString(template.HTMLEscapeString(it.Label) + `</a>`)
	}
	b.WriteString(`</nav>`)
	return template.HTML(b.String())
}
