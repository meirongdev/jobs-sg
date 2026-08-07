package mcf

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeArchiveFileAt writes one archive file with the given raw lines, creating
// the date directory. Lines are written verbatim so a test can plant a
// malformed or truncated record.
func writeArchiveFileAt(t *testing.T, root, date, name string, lines []string) {
	t.Helper()
	dir := filepath.Join(root, "raw", date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	for _, l := range lines {
		if _, err := gz.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func jobLine(t *testing.T, uuid, title string) string {
	t.Helper()
	b, err := json.Marshal(Job{UUID: uuid, Title: title, Description: "d-" + uuid})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWalkArchivesVisitsChronologicallyAndDecodesWholeJobs pins the ordering
// contract callers depend on: reprocessing keeps the last value it sees per
// uuid, so "last wins" must mean "newest archived copy wins". If the walk ever
// visits 000 after 001, or a later date before an earlier one, a stale sighting
// would silently overwrite a fresh one.
func TestWalkArchivesVisitsChronologicallyAndDecodesWholeJobs(t *testing.T) {
	root := t.TempDir()
	writeArchiveFileAt(t, root, "2026-08-05", "001.jsonl.gz", []string{jobLine(t, "u1", "third")})
	writeArchiveFileAt(t, root, "2026-08-05", "000.jsonl.gz", []string{jobLine(t, "u1", "second")})
	writeArchiveFileAt(t, root, "2026-08-03", "000.jsonl.gz", []string{jobLine(t, "u1", "first"), jobLine(t, "u2", "other")})

	var order []string
	var paths []string
	if err := WalkArchives(root, func(rawPath string, j Job) error {
		order = append(order, j.Title)
		paths = append(paths, rawPath)
		return nil
	}); err != nil {
		t.Fatalf("WalkArchives: %v", err)
	}

	want := []string{"first", "other", "second", "third"}
	if len(order) != len(want) {
		t.Fatalf("visited %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("visit %d = %q, want %q (chronological order broken)", i, order[i], want[i])
		}
	}
	wantPaths := []string{
		"raw/2026-08-03/000.jsonl.gz#0",
		"raw/2026-08-03/000.jsonl.gz#1",
		"raw/2026-08-05/000.jsonl.gz#0",
		"raw/2026-08-05/001.jsonl.gz#0",
	}
	for i := range wantPaths {
		if paths[i] != wantPaths[i] {
			t.Errorf("raw_path %d = %q, want %q", i, paths[i], wantPaths[i])
		}
	}
	// The decoded value must be the whole Job, not just the fields the
	// description reader needs.
	if err := WalkArchives(root, func(_ string, j Job) error {
		if j.UUID == "u2" && j.Description != "d-u2" {
			t.Errorf("description = %q, want the full record decoded", j.Description)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWalkArchivesSkipsAMalformedLine: the archive is append-only and
// immutable, so a bad record can never be repaired. Failing the pass over it
// would make the whole archive permanently unusable for reprocessing, which is
// a far worse outcome than losing one posting.
func TestWalkArchivesSkipsAMalformedLine(t *testing.T) {
	root := t.TempDir()
	writeArchiveFileAt(t, root, "2026-08-03", "000.jsonl.gz", []string{
		jobLine(t, "u1", "good"),
		"{not json at all",
		jobLine(t, "u2", "also good"),
	})

	var seen []string
	if err := WalkArchives(root, func(_ string, j Job) error {
		seen = append(seen, j.UUID)
		return nil
	}); err != nil {
		t.Fatalf("WalkArchives: %v", err)
	}
	if len(seen) != 2 || seen[0] != "u1" || seen[1] != "u2" {
		t.Errorf("seen = %v, want the two good records", seen)
	}
}

func TestWalkArchivesPropagatesCallbackError(t *testing.T) {
	root := t.TempDir()
	writeArchiveFileAt(t, root, "2026-08-03", "000.jsonl.gz", []string{jobLine(t, "u1", "a"), jobLine(t, "u2", "b")})

	boom := fmt.Errorf("stop")
	calls := 0
	err := WalkArchives(root, func(_ string, _ Job) error {
		calls++
		return boom
	})
	if err == nil {
		t.Fatal("want the callback error to stop the walk")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 — the walk must stop at the first error", calls)
	}
}

func TestWalkArchivesReportsAMissingRoot(t *testing.T) {
	if err := WalkArchives(t.TempDir(), func(string, Job) error { return nil }); err == nil {
		t.Error("want an error when raw/ does not exist")
	}
}
