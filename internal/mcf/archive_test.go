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
