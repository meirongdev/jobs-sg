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

// ambiguousAliases are aliases that are also ordinary English words, where a
// plain word-boundary match is mostly wrong. They are matched only in a
// list context (see listContextRegex).
//
// Membership is earned by measurement, not by suspicion — the alias must be
// shown to be mostly wrong on real postings before it is gated, because the
// gate also costs recall (it drops "Node.js Express REST APIs", where the
// alias is real but sits in prose). Precision of a bare boundary match,
// hand-scored over the 483 IT postings (SSOC 25xxx) in one day of the raw
// archive, 2026-08-24:
//
//	express  24 hits, 6 real   (25%) — "express themselves" in Meta's
//	                                   boilerplate, "Recruit Express Pte Ltd"
//	                                   in every posting by that agency
//	go       26 hits, 17 real  (65%) — "go-to-market", "go-live", "go-getter",
//	                                   "go beyond", "go/no-go", "go the extra mile"
//
// Gated, on the same corpus: express 5 hits 5 real, go 17 hits 16 real (the
// survivor is "go/no-go decisions"). Aliases that look ambiguous but measured
// clean inside IT postings are deliberately NOT here — spark 10/10 real,
// node 27/29, swift 9/10, git and js 100% — because gating them would drop
// "Spark MLlib" and "Swift and SwiftUI" to fix nothing.
var ambiguousAliases = map[string]bool{
	"express": true,
	"go":      true,
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
		re := aliasRegex(r[0])
		if ambiguousAliases[strings.ToLower(r[0])] {
			re = listContextRegex(r[0])
		}
		entries = append(entries, aliasEntry{re: re, slug: r[1], kind: r[2]})
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

// Separator classes that mark an enumeration of technologies. '.' is accepted
// only on the right ("Express.js", "…, or Go.") — on the left it would accept
// the start of a new sentence, which is where most of the false positives are
// ("Start somewhere. Go somewhere."). '-' is in neither, which is what rejects
// "go-to-market", "go-live" and "go-getter".
const (
	listBefore = `[(,/;|\[]`
	listAfter  = `[(),./;|\]]`
)

// listContextRegex matches an alias only where it sits in a list of
// technologies: a separator on one side or the other, allowing spaces between
// ("Java / Go / Rust", "Python, Go, or Shell", "(FastAPI/Express/Java)").
//
// Written as two alternatives rather than one pattern with lookaround because
// RE2 has none. Either side satisfying it is enough; requiring both would drop
// "or Go." and "Express.js", which are the commonest real forms.
//
// Start- and end-of-text count as separators, so NormalizeTerm still maps a
// bare LLM term ("Go", "Express") — there the term is already isolated, so the
// word-sense ambiguity this guards against cannot arise.
func listContextRegex(alias string) *regexp.Regexp {
	q := regexp.QuoteMeta(alias)
	return regexp.MustCompile(`(?i)` +
		`(?:(?:^|` + listBefore + `\s*)` + q + `(?:[^A-Za-z0-9]|$)` + `)` +
		`|` +
		`(?:(?:^|[^A-Za-z0-9])` + q + `(?:\s*` + listAfter + `|$)` + `)`)
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

// NormalizeTerm maps a single raw term (e.g. from LLM output) to its canonical
// slug + kind via the alias table. Returns ok=false when unmapped.
func (t *Taxonomy) NormalizeTerm(term string) (slug, kind string, ok bool) {
	if t == nil {
		return "", "", false
	}
	for _, a := range t.aliases {
		if a.re.MatchString(term) {
			return a.slug, a.kind, true
		}
	}
	return "", "", false
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
