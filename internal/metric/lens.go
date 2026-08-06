package metric

import (
	"fmt"
	"sort"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/classify"
)

// Lens narrows every statistic on a page to one experience band and/or role
// family (spec §2.3). Personas are lenses, not pages: a fresh graduate and a
// switcher ask the same questions from different experience bands, so splitting
// by persona would duplicate the same metrics across pages.
type Lens struct {
	Exp  string // "" | "0-2" | "3-5" | "6+" | "unstated"
	Role string // "" | a classify role family
}

// expBands maps an allowlisted band to its SQL predicate. Note that "0-2"
// excludes NULL: an unstated requirement is its own band, never folded into
// "no experience required" (spec §3.7-1).
var expBands = map[string]string{
	"0-2":      "j.min_years_exp IS NOT NULL AND j.min_years_exp <= 2",
	"3-5":      "j.min_years_exp BETWEEN 3 AND 5",
	"6+":       "j.min_years_exp >= 6",
	"unstated": "j.min_years_exp IS NULL",
}

// roleFamilyList enumerates the allowlist once; predicates and pickers both
// derive from it so a family added in classify shows up everywhere or nowhere.
var roleFamilyList = []string{
	classify.FamilyBackend, classify.FamilyFrontend, classify.FamilyFullstack,
	classify.FamilyMobile, classify.FamilyPlatform, classify.FamilySRE,
	classify.FamilyData, classify.FamilyAIML, classify.FamilySecurity,
	classify.FamilyOther,
}

// rolePredicates maps an allowlisted family to its canned predicate. Like
// expBands, only the map VALUE ever reaches the SQL text — built once at init
// from compile-time constants, so even a Lens constructed without ParseLens
// cannot inject through Where(). TestAllowlistValuesAreSQLLiteralAndKeySafe
// guards the constants themselves.
var rolePredicates = func() map[string]string {
	m := make(map[string]string, len(roleFamilyList))
	for _, f := range roleFamilyList {
		m[f] = "j.role_family = '" + f + "'"
	}
	return m
}()

// ExpBands lists the allowlisted experience bands in display order, for
// building the lens picker.
func ExpBands() []string { return []string{"0-2", "3-5", "6+", "unstated"} }

// RoleFamilies lists the allowlisted role families in display order.
func RoleFamilies() []string {
	out := make([]string, 0, len(rolePredicates))
	for f := range rolePredicates {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ParseLens validates raw query values against the allowlists. Unknown values
// are an error, not a silent no-op: silently ignoring a filter renders numbers
// that contradict the URL, and free-text values would let a crafted URL mint
// unbounded cache keys.
func ParseLens(exp, role string) (Lens, error) {
	if exp != "" {
		if _, ok := expBands[exp]; !ok {
			return Lens{}, fmt.Errorf("unknown exp band %q", exp)
		}
	}
	if role != "" {
		if _, ok := rolePredicates[role]; !ok {
			return Lens{}, fmt.Errorf("unknown role family %q", role)
		}
	}
	return Lens{Exp: exp, Role: role}, nil
}

// Where returns a fragment appendable to a WHERE clause, or "" for the empty
// lens. Every query using it MUST alias the job table as `j`.
//
// These fragments are concatenated into queries whose bind arguments are
// positional, so neither field can be bound; both resolve through allowlist
// maps whose VALUES are the only text that ever reaches the SQL — an
// unvalidated Lens contributes nothing rather than injecting.
func (l Lens) Where() string {
	var b strings.Builder
	if p, ok := expBands[l.Exp]; ok {
		b.WriteString(" AND " + p)
	}
	if p, ok := rolePredicates[l.Role]; ok {
		b.WriteString(" AND " + p)
	}
	return b.String()
}

// Key is the canonical cache-key fragment for this lens.
func (l Lens) Key() string { return "exp=" + l.Exp + ";role=" + l.Role }

// expLabels phrases each band for page headers. "unstated" is not a duration,
// so it must not take the "N yrs" suffix.
var expLabels = map[string]string{
	"0-2": "0-2 yrs", "3-5": "3-5 yrs", "6+": "6+ yrs",
	"unstated": "experience unstated",
}

// Label describes the active lens for page headers, or "" when unfiltered.
func (l Lens) Label() string {
	var parts []string
	if lbl, ok := expLabels[l.Exp]; ok {
		parts = append(parts, lbl)
	}
	if l.Role != "" {
		parts = append(parts, l.Role)
	}
	return strings.Join(parts, " · ")
}
