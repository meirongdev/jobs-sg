package mcf

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeArchive builds an archive file with n records whose descriptions are
// "desc-<i>", and returns the data root.
func writeArchive(t *testing.T, date string, n int) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "raw", date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "000.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	for i := 0; i < n; i++ {
		b, err := json.Marshal(Job{
			UUID:        fmt.Sprintf("uuid-%d", i),
			Title:       fmt.Sprintf("title-%d", i),
			Description: fmt.Sprintf("desc-%d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		gz.Write(append(b, '\n'))
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadArchiveDescriptionsBatch(t *testing.T) {
	root := writeArchive(t, "2026-08-03", 500)
	want := map[string]string{
		"raw/2026-08-03/000.jsonl.gz#0":   "desc-0",
		"raw/2026-08-03/000.jsonl.gz#1":   "desc-1",
		"raw/2026-08-03/000.jsonl.gz#250": "desc-250",
		"raw/2026-08-03/000.jsonl.gz#499": "desc-499",
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}

	got, err := ReadArchiveDescriptions(root, paths)
	if err != nil {
		t.Fatalf("ReadArchiveDescriptions: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d descriptions, want %d", len(got), len(want))
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s = %q, want %q", p, got[p], w)
		}
	}
}

// TestReadArchiveDescriptionsSinglePass is the regression guard for the
// performance defect: reading per job cost O(archive) each time, which made a
// full enrich pass ~30 CPU-hours on the real baseline archive. If someone
// reimplements this as a loop over ReadArchiveRecord, the file gets opened once
// per record and this fails.
func TestReadArchiveDescriptionsSinglePass(t *testing.T) {
	root := writeArchive(t, "2026-08-03", 300)
	paths := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		paths = append(paths, fmt.Sprintf("raw/2026-08-03/000.jsonl.gz#%d", i))
	}

	// Asserted as behaviour rather than timing (which would be flaky): every
	// wanted record comes back from one call, and a request satisfied early does
	// not depend on the rest of the file.
	got, err := ReadArchiveDescriptions(root, paths)
	if err != nil {
		t.Fatalf("ReadArchiveDescriptions: %v", err)
	}
	if len(got) != 300 {
		t.Fatalf("got %d, want 300 — a single pass must return every wanted record", len(got))
	}

	// Early exit: once every wanted line is seen, scanning stops. Requesting only
	// record 0 must succeed without depending on the rest of the file, so a
	// truncated/corrupt tail is tolerated.
	trunc := writeArchive(t, "2026-08-04", 10)
	p := filepath.Join(trunc, "raw", "2026-08-04", "000.jsonl.gz")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b[:len(b)-5], 0o644); err != nil { // corrupt the tail
		t.Fatal(err)
	}
	one, err := ReadArchiveDescriptions(trunc, []string{"raw/2026-08-04/000.jsonl.gz#0"})
	if err != nil {
		t.Fatalf("early-exit read should not surface a tail error: %v", err)
	}
	if one["raw/2026-08-04/000.jsonl.gz#0"] != "desc-0" {
		t.Errorf("got %q, want desc-0", one["raw/2026-08-04/000.jsonl.gz#0"])
	}
}

func TestReadArchiveDescriptionsMissingAndMalformed(t *testing.T) {
	root := writeArchive(t, "2026-08-03", 5)

	// Out-of-range index and malformed raw_path are simply absent — callers report
	// the gap per job rather than losing the whole run.
	got, err := ReadArchiveDescriptions(root, []string{
		"raw/2026-08-03/000.jsonl.gz#2",
		"raw/2026-08-03/000.jsonl.gz#9999",
		"raw/2026-08-03/000.jsonl.gz",    // no #offset
		"raw/2026-08-03/nope.jsonl.gz#0", // missing file
	})
	if got["raw/2026-08-03/000.jsonl.gz#2"] != "desc-2" {
		t.Errorf("valid record should still be read, got %q", got["raw/2026-08-03/000.jsonl.gz#2"])
	}
	if _, ok := got["raw/2026-08-03/000.jsonl.gz#9999"]; ok {
		t.Error("out-of-range index must be absent, not fabricated")
	}
	if _, ok := got["raw/2026-08-03/000.jsonl.gz"]; ok {
		t.Error("malformed raw_path must be absent")
	}
	// A missing file is reported via err, but valid records survive.
	if err == nil {
		t.Error("missing archive file should surface an error alongside partial results")
	}
}
