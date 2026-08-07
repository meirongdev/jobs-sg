package mcf

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// sgt is the archive's calendar.
//
// Every date bucket in this system is an SGT calendar day (docs/02 §4.4), and
// the daily ingest fires at 02:15 SGT — 18:15 UTC the day *before*. Naming the
// directory after the UTC date therefore filed each night's run under the
// previous day: /ops would show a crawl on 2026-08-07 while its archive sat in
// raw/2026-08-06/, so restoring "the archive for the 7th" handed you the 6th's.
//
// FixedZone, not LoadLocation: the scratch runtime image carries no tzdata
// (same reason as cmd/ingest).
var sgt = time.FixedZone("SGT", 8*3600)

// ArchiveWriter appends job records to a per-day gzip JSONL file, one JSON
// object per line. Archiving happens before any filtering/parsing (docs/02
// §4.1): the raw archive is the only non-rebuildable asset.
type ArchiveWriter struct {
	root  string // raw/ root
	date  string // YYYY-MM-DD
	seq   int
	count int // records written to this file
	f     *os.File
	gz    *gzip.Writer
}

// NewArchiveWriter opens (creating as needed) the archive file for t's SGT
// calendar day, using an increasing sequence number so one day may span files.
func NewArchiveWriter(root string, t time.Time) (*ArchiveWriter, error) {
	date := t.In(sgt).Format("2006-01-02")
	dir := filepath.Join(root, date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// pick next seq
	seq := 0
	for {
		name := fmt.Sprintf("%03d.jsonl.gz", seq)
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		seq++
	}
	name := fmt.Sprintf("%03d.jsonl.gz", seq)
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &ArchiveWriter{root: root, date: date, seq: seq, f: f, gz: gzip.NewWriter(f)}, nil
}

// Write appends one job and returns its raw_path location
// (raw/<date>/<seq>.jsonl.gz#<index>), suitable for job.raw_path.
func (w *ArchiveWriter) Write(j Job) (string, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	if _, err := w.gz.Write(b); err != nil {
		return "", err
	}
	loc := w.Location(w.count)
	w.count++
	return loc, nil
}

// Location returns the archive location for a 0-based record index, relative
// to the data root and prefixed with raw/ (matches job.raw_path in docs/03 §2).
func (w *ArchiveWriter) Location(index int) string {
	rel, err := filepath.Rel(w.root, filepath.Join(w.root, w.date, fmt.Sprintf("%03d.jsonl.gz", w.seq)))
	if err != nil {
		rel = fmt.Sprintf("%s/%03d.jsonl.gz", w.date, w.seq)
	}
	return "raw/" + filepath.ToSlash(rel) + fmt.Sprintf("#%d", index)
}

// Close flushes and closes the gzip file.
func (w *ArchiveWriter) Close() error {
	if err := w.gz.Close(); err != nil {
		return err
	}
	return w.f.Close()
}
