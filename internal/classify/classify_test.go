package classify

import (
	"testing"

	"github.com/meirongdev/jobs-sg/internal/mcf"
)

func y(i int) *int { return &i }

func jobWithSSOC(ssoc string) mcf.Job {
	return mcf.Job{SSOCCode: ssoc, Title: "Some Job", Categories: []mcf.Category{{Category: "Administration"}}}
}

func TestSSOCPrimaryHit(t *testing.T) {
	cl := New(map[string]string{"25121": "Backend"})
	j := jobWithSSOC("25121")
	res := cl.Classify(j)
	if !res.IsCandidate || res.HitLayer != HitSSOC {
		t.Fatalf("want ssoc hit, got %+v", res)
	}
	if res.RoleFamily != "Backend" {
		t.Errorf("role_family = %s, want Backend", res.RoleFamily)
	}
	if !res.IsSWE {
		t.Errorf("25121 Backend should be is_swe")
	}
}

func TestCategoryFallbackHit(t *testing.T) {
	cl := New(nil)
	j := jobWithSSOC("99999")
	j.Categories = []mcf.Category{{Category: "Information Technology"}}
	res := cl.Classify(j)
	if !res.IsCandidate || res.HitLayer != HitCategory {
		t.Fatalf("want category hit, got %+v", res)
	}
}

func TestTitleFallbackHit(t *testing.T) {
	cl := New(nil)
	j := jobWithSSOC("99999")
	j.Categories = []mcf.Category{{Category: "Engineering"}}
	j.Title = "Senior Software Engineer"
	res := cl.Classify(j)
	if !res.IsCandidate || res.HitLayer != HitTitle {
		t.Fatalf("want title hit, got %+v", res)
	}
}

func TestAllMissNotCandidate(t *testing.T) {
	cl := New(nil)
	j := jobWithSSOC("99999")
	j.Categories = []mcf.Category{{Category: "Administration"}}
	j.Title = "Receptionist"
	res := cl.Classify(j)
	if res.IsCandidate {
		t.Fatal("Receptionist must not be a candidate")
	}
	if res.IsSWE {
		t.Fatal("non-candidate must not be is_swe")
	}
}

func TestIsSWEIndependentOfCandidate(t *testing.T) {
	// candidate but role_family maps to Other-IT (e.g. unclassified 25191)
	cl := New(map[string]string{"25191": "Other-IT"})
	j := jobWithSSOC("25191")
	res := cl.Classify(j)
	if !res.IsCandidate {
		t.Fatal("25191 should be candidate")
	}
	if res.IsSWE {
		t.Fatal("Other-IT must not be is_swe")
	}
}

func TestSeniorityTitleWins(t *testing.T) {
	cl := New(nil)
	j := jobWithSSOC("25121")
	j.Title = "Staff Engineer"
	j.PositionLevels = []mcf.PositionLevel{{Position: "Professional"}}
	j.MinimumYearsExperience = y(6)
	res := cl.Classify(j)
	if res.Seniority != "Staff+" {
		t.Errorf("seniority = %s, want Staff+ (title wins)", res.Seniority)
	}
}

func TestSeniorityVoteNoTitle(t *testing.T) {
	cl := New(nil)
	j := jobWithSSOC("25121")
	j.Title = "Software Engineer"
	j.PositionLevels = []mcf.PositionLevel{{Position: "Professional"}}
	j.MinimumYearsExperience = y(3)
	res := cl.Classify(j)
	if res.Seniority != "Mid" {
		t.Errorf("seniority = %s, want Mid", res.Seniority)
	}
}

func TestWorkModeRemoteAndInferred(t *testing.T) {
	cl := New(nil)
	j := jobWithSSOC("25121")
	j.FlexibleWorkArrangements = []string{"remote"}
	res := cl.Classify(j)
	if res.WorkMode != "Remote" || res.WorkModeInferred {
		t.Errorf("work_mode = %s inferred=%v, want Remote/false", res.WorkMode, res.WorkModeInferred)
	}
	j.FlexibleWorkArrangements = nil
	res = cl.Classify(j)
	if res.WorkMode != "Onsite" || !res.WorkModeInferred {
		t.Errorf("work_mode = %s inferred=%v, want Onsite/true", res.WorkMode, res.WorkModeInferred)
	}
}

func TestCompanyTypeRules(t *testing.T) {
	cl := New(nil)
	cases := []struct {
		name string
		j    mcf.Job
		want string
	}{
		{"gov via uen prefix", mcf.Job{PostedCompany: &mcf.PostedCompany{UEN: "T08GB0001A", Name: "XYZ Board", SSICCode: "84120"}}, "Government"},
		{"bank", mcf.Job{PostedCompany: &mcf.PostedCompany{UEN: "U12345", Name: "DBS", SSICCode: "65110", EmployeeCount: y(10000)}}, "Bank & FinTech"},
		{"startup small", mcf.Job{PostedCompany: &mcf.PostedCompany{UEN: "U12346", Name: "ACME Pte Ltd", SSICCode: "62011", EmployeeCount: y(50)}}, "Startup"},
		{"mnc large", mcf.Job{PostedCompany: &mcf.PostedCompany{UEN: "U12347", Name: "BigCorp", SSICCode: "62011", EmployeeCount: y(5000)}}, "MNC"},
		{"consulting", mcf.Job{PostedCompany: &mcf.PostedCompany{UEN: "U12348", Name: "McKinsey", SSICCode: "70201", EmployeeCount: y(500)}}, "Consulting"},
		{"local tech", mcf.Job{PostedCompany: &mcf.PostedCompany{UEN: "U12349", Name: "ShopTech", SSICCode: "62011", EmployeeCount: y(500)}}, "Local Tech"},
	}
	for _, tc := range cases {
		got := cl.Classify(tc.j).CompanyType
		if got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
