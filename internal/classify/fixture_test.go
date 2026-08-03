package classify

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

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
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var j mcf.Job
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
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
	if count != 100 {
		t.Fatalf("fixture count = %d, want 100", count)
	}
	// sanity: the 100-row mix (22 unique rows cycled) has both candidates and
	// non-candidates
	if candidates == 0 || candidates == count {
		t.Fatalf("candidates = %d/%d, expected a mixed set", candidates, count)
	}
	t.Logf("fixture: %d jobs, %d candidates, %d is_swe", count, candidates, swe)
}
