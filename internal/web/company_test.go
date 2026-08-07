package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestCompaniesRouteAndLens(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/companies")
	if rec.Code != http.StatusOK {
		t.Fatalf("/companies = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Employers", "Who is posting", "How long postings stay up", "How contested"} {
		if !strings.Contains(body, want) {
			t.Errorf("/companies missing %q", want)
		}
	}
	if rec := get(t, s, "/companies?role=Wizard"); rec.Code != http.StatusBadRequest {
		t.Errorf("/companies?role=Wizard = %d, want 400", rec.Code)
	}
	if get(t, s, "/companies").Body.String() == get(t, s, "/companies?exp=0-2").Body.String() {
		t.Error("lensed and unlensed /companies returned identical pages")
	}
}

// The page must never present the lifetime median as "how long a posting
// lasts" — it only sees postings that came down (spec §3.5).
func TestCompaniesLabelsTheRightCensoring(t *testing.T) {
	s := setupWeb(t)
	body := get(t, s, "/companies").Body.String()
	if !strings.Contains(body, "closed postings") {
		t.Error("/companies must scope the lifetime figure to closed postings")
	}
	if !strings.Contains(body, "snapshots, not trends") {
		t.Error("/companies must state that the demand counters carry no time dimension")
	}
}
