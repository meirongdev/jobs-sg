package metric

import (
	"strings"
	"testing"
)

func TestParseLensAcceptsAllowlistedValues(t *testing.T) {
	for _, tc := range []struct{ exp, role string }{
		{"", ""},
		{"0-2", ""},
		{"3-5", "Backend"},
		{"6+", "Data"},
		{"unstated", "AI-ML"},
	} {
		if _, err := ParseLens(tc.exp, tc.role); err != nil {
			t.Errorf("ParseLens(%q,%q) = %v, want nil", tc.exp, tc.role, err)
		}
	}
}

func TestParseLensRejectsAnythingElse(t *testing.T) {
	// Free-text values would let a crafted URL mint unbounded cache keys, and a
	// silently ignored filter shows numbers that do not match the URL.
	for _, tc := range []struct{ exp, role string }{
		{"0-3", ""},
		{"junior", ""},
		{"0-2'; DROP TABLE job--", ""},
		{"", "backend"}, // case matters: role_family values are capitalised
		{"", "Nonexistent"},
	} {
		if _, err := ParseLens(tc.exp, tc.role); err == nil {
			t.Errorf("ParseLens(%q,%q) = nil error, want rejection", tc.exp, tc.role)
		}
	}
}

func TestLensWhereBuildsQualifiedPredicates(t *testing.T) {
	l, err := ParseLens("3-5", "Backend")
	if err != nil {
		t.Fatal(err)
	}
	where := l.Where()
	for _, want := range []string{"j.min_years_exp BETWEEN 3 AND 5", "j.role_family = 'Backend'"} {
		if !strings.Contains(where, want) {
			t.Errorf("Where() = %q, missing %q", where, want)
		}
	}
	if !strings.HasPrefix(where, " AND ") {
		t.Errorf("Where() must be appendable to a WHERE clause, got %q", where)
	}
}

func TestEmptyLensWhereIsEmpty(t *testing.T) {
	var l Lens
	if got := l.Where(); got != "" {
		t.Errorf("empty lens Where() = %q, want \"\"", got)
	}
}

func TestUnstatedExperienceIsItsOwnBand(t *testing.T) {
	// spec §3.7-1: "no requirement" (0) and "did not say" (NULL) must never be
	// merged — for a job seeker that is "I can apply" vs "unknown".
	zero, _ := ParseLens("0-2", "")
	unstated, _ := ParseLens("unstated", "")
	if strings.Contains(zero.Where(), "IS NULL") {
		t.Errorf("0-2 band must exclude NULL, got %q", zero.Where())
	}
	if !strings.Contains(unstated.Where(), "j.min_years_exp IS NULL") {
		t.Errorf("unstated band must select NULL, got %q", unstated.Where())
	}
}

func TestLensKeyIsStableAndDistinct(t *testing.T) {
	a, _ := ParseLens("3-5", "Backend")
	b, _ := ParseLens("3-5", "Frontend")
	if a.Key() == b.Key() {
		t.Errorf("different lenses share cache key %q", a.Key())
	}
	c, _ := ParseLens("3-5", "Backend")
	if a.Key() != c.Key() {
		t.Errorf("same lens gave different keys %q vs %q", a.Key(), c.Key())
	}
}
