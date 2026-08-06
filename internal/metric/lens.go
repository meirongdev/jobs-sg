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

var roleFamilies = map[string]bool{
	classify.FamilyBackend: true, classify.FamilyFrontend: true,
	classify.FamilyFullstack: true, classify.FamilyMobile: true,
	classify.FamilyPlatform: true, classify.FamilySRE: true,
	classify.FamilyData: true, classify.FamilyAIML: true,
	classify.FamilySecurity: true, classify.FamilyOther: true,
}

// ExpBands lists the allowlisted experience bands in display order, for
// building the lens picker.
func ExpBands() []string { return []string{"0-2", "3-5", "6+", "unstated"} }

// RoleFamilies lists the allowlisted role families in display order.
func RoleFamilies() []string {
	out := make([]string, 0, len(roleFamilies))
	for f := range roleFamilies {
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
	if role != "" && !roleFamilies[role] {
		return Lens{}, fmt.Errorf("unknown role family %q", role)
	}
	return Lens{Exp: exp, Role: role}, nil
}

// Where returns a fragment appendable to a WHERE clause, or "" for the empty
// lens. Every query using it MUST alias the job table as `j`.
//
// The role value is interpolated rather than bound because these fragments are
// concatenated into queries whose bind arguments are positional; interpolation
// is safe only because the value came through the allowlist above.
func (l Lens) Where() string {
	var b strings.Builder
	if p, ok := expBands[l.Exp]; ok {
		b.WriteString(" AND " + p)
	}
	if l.Role != "" {
		b.WriteString(" AND j.role_family = '" + l.Role + "'")
	}
	return b.String()
}

// Key is the canonical cache-key fragment for this lens.
func (l Lens) Key() string { return "exp=" + l.Exp + ";role=" + l.Role }

// Label describes the active lens for page headers, or "" when unfiltered.
func (l Lens) Label() string {
	var parts []string
	if l.Exp != "" {
		parts = append(parts, l.Exp+" yrs")
	}
	if l.Role != "" {
		parts = append(parts, l.Role)
	}
	return strings.Join(parts, " · ")
}
