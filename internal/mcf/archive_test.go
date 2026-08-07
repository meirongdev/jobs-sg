package mcf

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveWriterAppendsAndTracksLocation(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	w, err := NewArchiveWriter(root, now)
	if err != nil {
		t.Fatalf("NewArchiveWriter: %v", err)
	}
	loc1, err := w.Write(fakeJob("a", "2026-08-03T00:00:00Z"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	loc2, err := w.Write(fakeJob("b", "2026-08-03T01:00:00Z"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if want := "raw/2026-08-03/000.jsonl.gz#0"; loc1 != want {
		t.Errorf("loc1 = %q, want %q", loc1, want)
	}
	if want := "raw/2026-08-03/000.jsonl.gz#1"; loc2 != want {
		t.Errorf("loc2 = %q, want %q", loc2, want)
	}
	// file exists under root, gzip valid, two records readable
	path := filepath.Join(root, "2026-08-03", "000.jsonl.gz")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	var ids []string
	sc := bufio.NewScanner(gz)
	for sc.Scan() {
		var j Job
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		ids = append(ids, j.UUID)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("ids = %v, want [a b]", ids)
	}
}

// The nightly ingest fires at 02:15 SGT, which is 18:15 UTC the day before.
// Bucketing the archive by the UTC date filed every run under the previous
// day, so the directory disagreed with the SGT day /ops reports the run on.
func TestArchiveWriterBucketsBySGTCalendarDay(t *testing.T) {
	root := t.TempDir()
	// 18:15 UTC on the 6th == 02:15 SGT on the 7th: the nightly run's real time
	runAt := time.Date(2026, 8, 6, 18, 15, 0, 0, time.UTC)

	w, err := NewArchiveWriter(root, runAt)
	if err != nil {
		t.Fatalf("NewArchiveWriter: %v", err)
	}
	loc, err := w.Write(fakeJob("a", "2026-08-07"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if want := "raw/2026-08-07/000.jsonl.gz#0"; loc != want {
		t.Errorf("loc = %q, want %q — 02:15 SGT on the 7th belongs to the 7th", loc, want)
	}
	if _, err := os.Stat(filepath.Join(root, "2026-08-07")); err != nil {
		t.Errorf("archive directory for the SGT day is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "2026-08-06")); err == nil {
		t.Error("archive landed in the UTC day's directory")
	}
}
