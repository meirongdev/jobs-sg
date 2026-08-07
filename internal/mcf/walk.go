package mcf

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WalkArchives calls fn for every record in the raw archive, decoding each line
// into a full Job and passing the raw_path that identifies it.
//
// Files are visited in chronological order (raw/<date>/<seq>.jsonl.gz, both
// sorted ascending), so a caller that keeps the last value it sees per uuid ends
// up with the newest archived copy of that posting. The same posting appears in
// many days' archives — every reconcile re-sights the whole board — and the
// later sighting is the one that reflects what MCF says now.
//
// One pass per file, decoding straight from the gzip stream: nothing is held in
// memory but the current line. ReadArchiveRecord re-opens and re-scans on every
// call, which is O(archive) per record — see its comment for the measured cost
// of getting that wrong on a baseline archive.
//
// A file that cannot be opened or whose gzip framing is broken stops the walk;
// a truncated final line does not, because a run killed mid-append leaves one,
// and the records before it are perfectly good.
func WalkArchives(dataDir string, fn func(rawPath string, j Job) error) error {
	root := filepath.Join(dataDir, "raw")
	dates, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("archive root %s: %w", root, err)
	}
	names := make([]string, 0, len(dates))
	for _, d := range dates {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	sort.Strings(names)

	for _, date := range names {
		dir := filepath.Join(root, date)
		ents, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("archive dir %s: %w", dir, err)
		}
		files := make([]string, 0, len(ents))
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl.gz") {
				files = append(files, e.Name())
			}
		}
		sort.Strings(files)
		for _, name := range files {
			rel := date + "/" + name
			if err := walkArchiveFile(filepath.Join(dir, name), rel, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkArchiveFile(path, rel string, fn func(string, Job) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("archive %s: %w", path, err)
	}
	defer gz.Close()

	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for line := 0; sc.Scan(); line++ {
		var j Job
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			// A single unparseable line is skipped rather than fatal: the archive
			// is append-only and immutable, so one bad record can never be fixed,
			// and failing the whole pass over it would make the archive
			// permanently unusable for reprocessing.
			continue
		}
		if err := fn(fmt.Sprintf("raw/%s#%d", rel, line), j); err != nil {
			return err
		}
	}
	// A truncated tail (gzip.ErrUnexpectedEOF and friends) means the last append
	// was cut short; everything before it was already handed to fn.
	if err := sc.Err(); err != nil {
		return fmt.Errorf("archive %s: %w", path, err)
	}
	return nil
}
