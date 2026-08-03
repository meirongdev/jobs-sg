// Package tech implements the rule-layer technology-stack extraction
// (docs/02 §4.2, docs/03 §7): a tech_taxonomy alias table + word-boundary
// regex scan. Runs before any LLM pass and always runs.
package tech

import (
	"regexp"
	"sort"
	"strings"
)

// Source constants for job_tech.source.
const (
	SourceRule = "rule"
	SourceLLM  = "llm"
)

// Tech is one normalized technology extracted from a job.
type Tech struct {
	Slug string
	Kind string
}

type aliasEntry struct {
	re   *regexp.Regexp
	slug string
	kind string
}

// Taxonomy is an in-memory compiled alias table.
type Taxonomy struct {
	aliases []aliasEntry
}

// LoadTaxonomy builds a Taxonomy from rows of (alias, tech_slug, tech_kind).
// Longer aliases are matched first to avoid sub-string shadowing.
func LoadTaxonomy(rows [][3]string) *Taxonomy {
	entries := make([]aliasEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, aliasEntry{re: aliasRegex(r[0]), slug: r[1], kind: r[2]})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return len(entries[i].re.String()) > len(entries[j].re.String())
	})
	return &Taxonomy{aliases: entries}
}

// aliasRegex builds a boundary-bounded regex for an alias. Go's RE2 has no
// lookaround, so we use explicit boundary classes; since we only test
// presence (MatchString) per alias, consuming the boundary char is fine.
// This avoids the \b pitfalls of non-word trailing chars ("C++", "C#") and
// prevents matching a shorter alias inside a longer word ("py" in "python").
func aliasRegex(alias string) *regexp.Regexp {
	q := regexp.QuoteMeta(alias)
	return regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])` + q + `(?:[^A-Za-z0-9]|$)`)
}

// Extract scans text for known aliases and returns deduplicated canonical
// techs (first occurrence wins for slug/kind).
func (t *Taxonomy) Extract(text string) []Tech {
	if t == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []Tech
	for _, a := range t.aliases {
		if a.re.MatchString(text) {
			if seen[a.slug] {
				continue
			}
			seen[a.slug] = true
			out = append(out, Tech{Slug: a.slug, Kind: a.kind})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// StripHTML removes tags and unescapes common entities from a JD description
// so rule/LLM layers see plain text.
func StripHTML(s string) string {
	re := regexp.MustCompile(`(?s)<[^>]*>`)
	s = re.ReplaceAllString(s, " ")
	repl := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'",
	)
	s = repl.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
