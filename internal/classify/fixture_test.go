package classify

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/mcf"
)

// TestFixtureReplay runs the classification over the record-shaped fixture
// (docs/05 Phase 1 DoD: real-API-shaped fixture replay for criteria trust).
func TestFixtureReplay(t *testing.T) {
	f, err := os.Open("../../testdata/fixture/jobs.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	cl := New(map[string]string{
		"25121": "Backend", "25122": "Backend", "25131": "Frontend",
		"25132": "Mobile", "25141": "Backend", "25211": "Data",
		"25221": "Platform", "25231": "Security", "21221": "Data", "21222": "Data",
	})
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	count := 0
	candidates := 0
	swe := 0
	weeks := map[[2]int]int{}
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var j mcf.Job
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		d, err := time.Parse("2006-01-02", j.Metadata.NewPostingDate)
		if err != nil {
			t.Fatalf("%s: bad newPostingDate %q: %v", j.UUID, j.Metadata.NewPostingDate, err)
		}
		y, w := d.ISOWeek()
		weeks[[2]int{y, w}]++
		res := cl.Classify(j)
		count++
		if res.IsCandidate {
			candidates++
			if res.HitLayer == "" {
				t.Errorf("%s: candidate without hit layer", j.UUID)
			}
			if res.RoleFamily == "" {
				t.Errorf("%s: candidate without role_family", j.UUID)
			}
			if res.Seniority == "" {
				t.Errorf("%s: candidate without seniority", j.UUID)
			}
			if res.IsSWE && sweFamilies[res.RoleFamily] == false {
				t.Errorf("%s: is_swe but role_family %s not in SWE families", j.UUID, res.RoleFamily)
			}
			if res.IsSWE {
				swe++
			}
		} else {
			if res.HitLayer != "" {
				t.Errorf("%s: non-candidate with hit layer %s", j.UUID, res.HitLayer)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// 360 = 6 个完整 ISO 周 × 60 行（scripts/genfixture）。动量指标需要 5 个已完成
	// 周的历史，7 天的旧 fixture 撑不起来。
	if count != 360 {
		t.Fatalf("fixture count = %d, want 360", count)
	}
	// Week-shape guard, not just cardinality: the metric tests' fixed clock
	// (2026-08-10, W33 Monday) needs exactly 2026-W27..W32 populated at 60
	// rows each — 5 completed weeks behind the reported one. A reshuffled 360
	// (4 weeks × 90, or a shifted Monday leaving partial weeks) would pass a
	// bare count and silently break every momentum test.
	if len(weeks) != 6 {
		t.Fatalf("fixture spans %d ISO weeks, want 6: %v", len(weeks), weeks)
	}
	for wk := 27; wk <= 32; wk++ {
		if n := weeks[[2]int{2026, wk}]; n != 60 {
			t.Errorf("ISO week 2026-W%02d = %d rows, want 60", wk, n)
		}
	}
	// sanity: the 360-row mix (24 unique rows cycled) has both candidates and
	// non-candidates
	if candidates == 0 || candidates == count {
		t.Fatalf("candidates = %d/%d, expected a mixed set", candidates, count)
	}
	t.Logf("fixture: %d jobs, %d candidates, %d is_swe", count, candidates, swe)
}
