package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestTechPageRenders(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/tech")
	if rec.Code != http.StatusOK {
		t.Fatalf("/tech = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	for _, want := range []string{"Tech Demand", "Momentum", "Salary premium", "Entry-friendly"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/tech missing %q", want)
		}
	}
}

func TestTechPageRejectsUnknownLensValues(t *testing.T) {
	s := setupWeb(t)
	for _, path := range []string{
		"/tech?exp=0-3",
		"/tech?exp=junior",
		"/tech?role=backend",
		"/tech?role=Nonexistent",
	} {
		if rec := get(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

func TestTechPageAcceptsAllowlistedLens(t *testing.T) {
	s := setupWeb(t)
	for _, path := range []string{"/tech?exp=0-2", "/tech?role=Backend", "/tech?exp=6%2B&role=Data"} {
		if rec := get(t, s, path); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestTechPagesAreCachedPerLens(t *testing.T) {
	s := setupWeb(t)
	get(t, s, "/tech")
	get(t, s, "/tech?exp=0-2")
	get(t, s, "/tech?exp=0-2&role=Backend")
	now := s.now()
	for _, key := range []string{
		"tech:exp=;role=",
		"tech:exp=0-2;role=",
		"tech:exp=0-2;role=Backend",
	} {
		if _, ok := s.cache.get(key, now); !ok {
			t.Errorf("cache missing entry %q", key)
		}
	}
}

func TestTechPageShowsSuppressionInsteadOfZero(t *testing.T) {
	// setupWeb seeds a single posting on one day, so momentum has nowhere near
	// 5 weeks of history: the page must say so rather than draw a flat zero.
	s := setupWeb(t)
	body := get(t, s, "/tech").Body.String()
	if !strings.Contains(body, "needs 5 weeks of history") {
		t.Errorf("/tech must explain short history, got:\n%s", body)
	}
}
