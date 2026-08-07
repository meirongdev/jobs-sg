package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarketLensWhitelist(t *testing.T) {
	s := setupWeb(t)
	for _, ok := range []string{"/", "/?exp=0-2", "/?role=Backend", "/?exp=6%2B&role=SRE"} {
		if rec := get(t, s, ok); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", ok, rec.Code)
		}
	}
	// An unknown value is refused, not ignored: rendering numbers that
	// contradict the URL is worse, and free text would mint unbounded cache keys.
	for _, bad := range []string{"/?exp=0-3", "/?role=Wizard", "/?exp=", "/?role="} {
		code := get(t, s, bad).Code
		if strings.HasSuffix(bad, "=") {
			if code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 (empty means unfiltered)", bad, code)
			}
			continue
		}
		if code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", bad, code)
		}
	}
}

// Distinct lenses must not share a cache entry, or one visitor's filtered page
// is served to the next visitor unfiltered.
func TestMarketCacheKeyIncludesTheLens(t *testing.T) {
	s := setupWeb(t)
	all := get(t, s, "/").Body.String()
	lensed := get(t, s, "/?role=Backend").Body.String()
	if all == lensed {
		t.Error("lensed and unlensed / returned byte-identical pages")
	}
	if !strings.Contains(lensed, "Backend") {
		t.Error("the lensed page does not name the active lens")
	}
}

// Only /reports and /w/{week} touch the report directory now; / must render
// from the DB even when no report has ever been written.
func TestMarketRendersWithoutAnyReportOnDisk(t *testing.T) {
	s, dir := setupWebClock(t, nil)
	if err := removeReports(dir); err != nil {
		t.Fatal(err)
	}
	rec := get(t, s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ = %d with no report on disk, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "On the board now") {
		t.Error("/ did not render the market snapshot")
	}
}

// removeReports clears the report directory so the market page has to stand on
// the DB alone.
func removeReports(dir string) error {
	return os.RemoveAll(filepath.Join(dir, "report"))
}
