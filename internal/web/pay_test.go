package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestPayPageRenders(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/pay")
	if rec.Code != http.StatusOK {
		t.Fatalf("/pay = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	for _, want := range []string{"What you are worth", "Seniority", "Experience ladder", "Who discloses"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("/pay missing %q", want)
		}
	}
}

func TestPayPageRejectsUnknownLensValues(t *testing.T) {
	s := setupWeb(t)
	for _, path := range []string{"/pay?exp=0-3", "/pay?role=backend", "/pay?exp=junior"} {
		if rec := get(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
}

func TestPayPagesAreCachedPerLens(t *testing.T) {
	s := setupWeb(t)
	get(t, s, "/pay")
	get(t, s, "/pay?exp=0-2&role=Backend")
	now := s.now()
	for _, key := range []string{"pay:exp=;role=", "pay:exp=0-2;role=Backend"} {
		if _, ok := s.cache.get(key, now); !ok {
			t.Errorf("cache missing entry %q", key)
		}
	}
}

func TestPayPageSuppressesInsteadOfShowingZero(t *testing.T) {
	// setupWeb seeds one posting with no salary, so every cell is thin: the
	// page must say so rather than print S$0.
	s := setupWeb(t)
	body := get(t, s, "/pay").Body.String()
	if !strings.Contains(body, "n=") {
		t.Errorf("/pay must show sample-size suppression markers, got:\n%s", body)
	}
	if strings.Contains(body, "S$0") {
		t.Error("/pay must never render a zero salary")
	}
}

func TestPayPageCarriesTheSharedNav(t *testing.T) {
	s := setupWeb(t)
	body := get(t, s, "/pay").Body.String()
	if !strings.Contains(body, `<a class="on" href="/pay">Pay</a>`) {
		t.Error("/pay must mark itself active in the shared nav")
	}
	if !strings.Contains(body, `href="/tech"`) {
		t.Error("/pay nav must link the sibling pages")
	}
}
