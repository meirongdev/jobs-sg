package view

import (
	"strings"
	"testing"
)

func TestNavListsEveryJobSeekerPageOnce(t *testing.T) {
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
	// navItems' doc comment promises reading order; without this, reversing the
	// slice passes every other test in the repo.
	html := string(Nav(""))
	want := []string{`href="/"`, `href="/tech"`, `href="/pay"`}
	at := make([]int, len(want))
	for i, w := range want {
		if at[i] = strings.Index(html, w); at[i] < 0 {
			t.Fatalf("nav missing %s: %s", w, html)
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
