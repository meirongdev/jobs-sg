package mcf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadArchiveRecordRoundTrip(t *testing.T) {
	// dataDir/raw is the layout used by ingest; raw_path is relative to dataDir
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, "raw")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := NewArchiveWriter(root, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	job := fakeJob("abc", "2026-08-03T00:00:00Z")
	job.Description = "<p>Go API</p>"
	loc, err := w.Write(job)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	got, err := ReadArchiveRecord(dataDir, loc)
	if err != nil {
		t.Fatalf("ReadArchiveRecord: %v", err)
	}
	if got.UUID != "abc" || got.Description != "<p>Go API</p>" {
		t.Errorf("got %+v", got)
	}
}
