package view

import (
	"strings"
	"testing"
)

func TestNavListsEveryJobSeekerPageOnce(t *testing.T) {
	// Hardcoded on purpose, unlike TestNavRendersInReadingOrder: this test's
	// job is to assert the nav contains exactly the pages we intend, so
	// deriving its expectations from navItems would make it vacuous.
	html := string(Nav("/tech"))
	for _, want := range []string{`href="/"`, `href="/tech"`, `href="/pay"`} {
		if strings.Count(html, want) != 1 {
			t.Errorf("nav must link %s exactly once, got %d: %s", want, strings.Count(html, want), html)
		}
	}
	// /ops is operational telemetry: reachable from page footers, never here.
	if strings.Contains(html, "/ops") {
		t.Errorf("nav must not link /ops: %s", html)
	}
}

func TestNavMarksTheActivePage(t *testing.T) {
	html := string(Nav("/pay"))
	if !strings.Contains(html, `<a class="on" href="/pay">`) {
		t.Errorf("active page must carry class=on: %s", html)
	}
	if strings.Count(html, `class="on"`) != 1 {
		t.Errorf("exactly one active link expected: %s", html)
	}
}

func TestNavWithoutAnActivePageHighlightsNothing(t *testing.T) {
	// The ops pages are not in the nav, so they pass "" and no item lights up.
	html := string(Nav(""))
	if strings.Contains(html, `class="on"`) {
		t.Errorf("no item may be active when active is empty: %s", html)
	}
	if !strings.Contains(html, `href="/tech"`) {
		t.Errorf("nav still lists every page: %s", html)
	}
}

func TestNavRendersInReadingOrder(t *testing.T) {
	// The expected order is written out by hand on purpose. Deriving it from
	// navItems would compare the slice against itself and pass for every
	// permutation, including the full reversal this test exists to catch. The
	// length gate below is what keeps a newly added page from silently escaping
	// the order guarantee: add an item to navItems and this fails until someone
	// consciously decides where it belongs.
	want := []string{"/", "/tech", "/pay"}
	if len(want) != len(navItems) {
		// The remedy is spelled out here, not only in the doc comment above:
		// someone fixing a red run from the failure text alone would otherwise
		// reach for `for i, it := range navItems { want[i] = it.Href }`, which
		// keeps this gate tautologically true and silently restores the
		// compare-the-slice-against-itself bug the comment warns about.
		t.Fatalf("navItems has %d entries but this test pins %d — widen want BY HAND, placing the new page deliberately; do not derive want from navItems",
			len(navItems), len(want))
	}
	html := string(Nav(""))
	at := make([]int, len(want))
	for i, h := range want {
		if at[i] = strings.Index(html, `href="`+h+`"`); at[i] < 0 {
			t.Fatalf("nav missing href=%q: %s", h, html)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i-1] > at[i] {
			t.Errorf("%s must precede %s in the rendered nav: %s", want[i-1], want[i], html)
		}
	}
}

func TestNavMarksTheSiteRootActive(t *testing.T) {
	// The weekly report passes "/" — the one active value no test covered.
	html := string(Nav("/"))
	if !strings.Contains(html, `<a class="on" href="/">`) {
		t.Errorf("site root must mark itself active: %s", html)
	}
	if strings.Count(html, `class="on"`) != 1 {
		t.Errorf("exactly one active link expected: %s", html)
	}
}
