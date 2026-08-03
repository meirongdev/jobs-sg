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

// ReadArchiveRecord reads one job back from the raw archive given its
// raw_path (e.g. "raw/2026-08-03/000.jsonl.gz#0") and the data root that
// contains raw/. The DB stores only description_sha256; the full HTML
// description lives in the archive (docs/03 §3).
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
