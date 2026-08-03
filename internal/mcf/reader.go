package mcf

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadArchiveDescriptions reads the description field for many raw_paths using
// a single pass per archive file.
//
// Why this exists: ReadArchiveRecord re-opens the gzip and scans from the start
// on every call, so reading N descriptions costs N × O(archive). On a baseline
// archive (88,258 records, 402MB decompressed, one file) that is ~290MB of
// decompression per job, and enrich reads every description twice — once in the
// rule layer, once in the LLM layer. Measured on homelab: 75 jobs in 14 minutes
// with the CPU limit saturated, i.e. ~30 CPU-hours for one full pass over 4,912
// jobs. One pass per file makes a run O(archive) instead of O(archive × jobs).
//
// Only the description field is decoded, so whole Job values are never
// materialised. Paths that cannot be found are simply absent from the result —
// callers decide how to report them, which keeps this fail-open.
func ReadArchiveDescriptions(dataDir string, rawPaths []string) (map[string]string, error) {
	// Group the wanted line numbers per archive file, so each file is opened once.
	byFile := make(map[string]map[int][]string)
	for _, rp := range rawPaths {
		rel, line, err := splitRawPath(rp)
		if err != nil {
			continue // malformed raw_path: absent from the result, reported by caller
		}
		if byFile[rel] == nil {
			byFile[rel] = make(map[int][]string)
		}
		byFile[rel][line] = append(byFile[rel][line], rp)
	}

	out := make(map[string]string, len(rawPaths))
	var firstErr error
	for rel, byLine := range byFile {
		path := filepath.Join(dataDir, "raw", filepath.FromSlash(rel))
		if err := scanDescriptions(path, byLine, out); err != nil && firstErr == nil {
			// Keep going: one unreadable archive file must not lose the others.
			firstErr = err
		}
	}
	return out, firstErr
}

// scanDescriptions walks one archive file once, filling out[rawPath] for every
// wanted line. It stops as soon as every wanted line has been seen.
func scanDescriptions(path string, byLine map[int][]string, out map[string]string) error {
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
	remaining := len(byLine)
	for line := 0; remaining > 0 && sc.Scan(); line++ {
		paths, ok := byLine[line]
		if !ok {
			continue
		}
		var rec struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return fmt.Errorf("archive %s line %d: %w", path, line, err)
		}
		for _, p := range paths {
			out[p] = rec.Description
		}
		remaining--
	}
	if remaining == 0 {
		// Every wanted record was read, so we never needed the rest of the file:
		// a truncated or corrupt tail (e.g. a run killed mid-append) must not fail
		// a run whose data is already in hand.
		return nil
	}
	return sc.Err()
}

// splitRawPath splits "raw/2026-08-03/000.jsonl.gz#12" into the archive-relative
// file ("2026-08-03/000.jsonl.gz") and the record index (12).
func splitRawPath(rawPath string) (rel string, line int, err error) {
	hashIdx := strings.LastIndex(rawPath, "#")
	if hashIdx < 0 {
		return "", 0, fmt.Errorf("raw_path %q missing #offset", rawPath)
	}
	line, err = strconv.Atoi(rawPath[hashIdx+1:])
	if err != nil {
		return "", 0, fmt.Errorf("raw_path %q bad offset: %w", rawPath, err)
	}
	rel = rawPath[:hashIdx]
	if strings.HasPrefix(rel, "raw/") || rel == "raw" {
		rel = strings.TrimPrefix(rel, "raw/")
	}
	return rel, line, nil
}

// ReadArchiveRecord reads one job back from the raw archive given its
// raw_path (e.g. "raw/2026-08-03/000.jsonl.gz#0") and the data root that
// contains raw/. The DB stores only description_sha256; the full HTML
// description lives in the archive (docs/03 §3).
//
// For more than a handful of records use ReadArchiveDescriptions instead: this
// scans from the start of the file on every call.
func ReadArchiveRecord(dataDir, rawPath string) (Job, error) {
	hashIdx := strings.LastIndex(rawPath, "#")
	if hashIdx < 0 {
		return Job{}, fmt.Errorf("raw_path %q missing #offset", rawPath)
	}
	lineNo, err := strconv.Atoi(rawPath[hashIdx+1:])
	if err != nil {
		return Job{}, fmt.Errorf("raw_path %q bad offset: %w", rawPath, err)
	}
	rel := rawPath[:hashIdx]
	if strings.HasPrefix(rel, "raw/") || rel == "raw" {
		rel = strings.TrimPrefix(rel, "raw/")
	}
	f, err := os.Open(filepath.Join(dataDir, "raw", filepath.FromSlash(rel)))
	if err != nil {
		return Job{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Job{}, err
	}
	defer gz.Close()
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	line := 0
	for sc.Scan() {
		if line == lineNo {
			var j Job
			if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
				return Job{}, err
			}
			return j, nil
		}
		line++
	}
	if err := sc.Err(); err != nil {
		return Job{}, err
	}
	return Job{}, fmt.Errorf("archive record %q not found (file has %d lines)", rawPath, line)
}
