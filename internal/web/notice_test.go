package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Between a fresh deploy and the first Monday 09:00 SGT report run there is no
// latest.html, and "/" is the first entry in the nav every other page renders.
// A visitor clicking it must not land on net/http's unstyled dead end.
func TestRootWithoutAReportExplainsItselfAndKeepsTheNav(t *testing.T) {
	s, dir := setupWebClock(t, nil)
	if err := os.Remove(filepath.Join(dir, "report", "latest.html")); err != nil {
		t.Fatal(err)
	}

	rec := get(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Errorf("/ = %d, want 200 — the page exists, its content is simply not generated yet", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "404 page not found") {
		t.Error("/ served net/http's bare 404 body")
	}
	if !strings.Contains(body, "No weekly report yet") {
		t.Errorf("/ should say why there is nothing here, got: %s", body)
	}
	// the way out: the live pages, which have numbers from day one
	for _, href := range []string{`href="/tech"`, `href="/pay"`} {
		if !strings.Contains(body, href) {
			t.Errorf("/ should link %s so a visitor is not stranded", href)
		}
	}
}

// A wrong week is a wrong turn, not an exit: still a 404, but one that carries
// the navigation.
func TestMissingWeekIsA404ThatKeepsTheNav(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/w/1999-W01")
	if rec.Code != http.StatusNotFound {
		t.Errorf("/w/1999-W01 = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "404 page not found") {
		t.Error("missing week served net/http's bare 404 body")
	}
	if !strings.Contains(body, `href="/tech"`) {
		t.Errorf("404 page should carry the nav, got: %s", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// The 404 body must not become an echo chamber for whatever the URL contained.
func TestNotFoundDoesNotReflectRequestInput(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/w/"+"%3Cscript%3Ealert(1)%3C/script%3E")
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Error("404 body reflected the requested path unescaped")
	}
}
