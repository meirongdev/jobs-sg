// Command genfixture writes testdata/fixture/jobs.jsonl — a deterministic,
// record-shaped sample of ~100 MCF job objects for fixture replay tests.
// Field shapes follow the 2026-08-02 site survey (docs/archive/2026-08-02-
// site-survey.md); this is sample data, not a live API capture (offline env).
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meirongdev/jobs-sg/internal/mcf"
)

type row struct {
	title    string
	ssoc     string
	category string
	posLevel string
	minYr    *int
	flex     []string
	desc     string
	company  string
	uen      string
	ssic     string
	emp      int
}

var rows = []row{
	{"Backend Engineer (Go)", "25121", "Information Technology", "Professional", intP(3), []string{"hybrid"}, "Building APIs with Go, Kubernetes and PostgreSQL.", "ShopBack", "201601111G", "62011", 400},
	{"Senior Backend Engineer", "25121", "Information Technology", "Professional", intP(5), []string{"hybrid"}, "Designing distributed systems with Go, Kafka, AWS.", "Grab", "200901111H", "62011", 9000},
	{"Frontend Engineer", "25131", "Information Technology", "Professional", intP(2), []string{"onsite"}, "React, TypeScript and modern CSS.", "Sea", "201401111K", "62011", 6000},
	{"Senior Frontend Developer", "25131", "Information Technology", "Professional", intP(6), []string{"remote"}, "Next.js, React, GraphQL.", "Carousell", "201201111E", "62011", 800},
	{"Fullstack Engineer", "25121", "Information Technology", "Professional", intP(3), []string{"hybrid"}, "Node.js, React, MongoDB, Docker.", "TikTok", "201501111F", "62011", 10000},
	{"Data Engineer", "21222", "Information Technology", "Professional", intP(3), []string{"hybrid"}, "Spark, Airflow, Python, Snowflake.", "DBS", "196800150E", "65110", 10000},
	{"Data Scientist", "21221", "Information Technology", "Professional", intP(4), []string{"hybrid"}, "Python, PyTorch, scikit-learn, RAG.", "OCBC", "193200032W", "65110", 8000},
	{"Machine Learning Engineer", "25121", "Information Technology", "Professional", intP(4), []string{"hybrid"}, "PyTorch, TensorFlow, LLM, Kubernetes.", "GovTech", "T08GB0001A", "84120", 3000},
	{"Mobile Developer (iOS)", "25132", "Information Technology", "Professional", intP(3), []string{"onsite"}, "Swift, UIKit, SwiftUI.", "Ninja Van", "201401111A", "49213", 3000},
	{"Android Developer", "25132", "Information Technology", "Professional", intP(3), []string{"onsite"}, "Kotlin, Jetpack Compose.", "foodpanda", "201201111B", "62011", 5000},
	{"Site Reliability Engineer", "25121", "Information Technology", "Professional", intP(5), []string{"hybrid"}, "Kubernetes, Terraform, Prometheus, Grafana.", "Stripe", "201401111C", "62011", 3000},
	{"Security Engineer", "25231", "Information Technology", "Professional", intP(4), []string{"onsite"}, "AWS security, penetration testing, IAM.", "Singtel", "199201623D", "61000", 7000},
	{"Platform Engineer", "25221", "Information Technology", "Professional", intP(4), []string{"hybrid"}, "AWS, Kubernetes, CI/CD, GitHub Actions.", "Shopee", "201507407E", "62011", 10000},
	{"DevOps Engineer", "25221", "Information Technology", "Professional", intP(3), []string{"hybrid"}, "Docker, Kubernetes, Ansible, Jenkins.", "Lazada", "201211110Z", "62011", 6000},
	{"QA Engineer", "25122", "Information Technology", "Professional", intP(2), []string{"onsite"}, "Selenium, Python, pytest.", "Dyson", "201309398N", "62011", 4000},
	{"Cloud Engineer", "25221", "Information Technology", "Professional", intP(3), []string{"hybrid"}, "AWS, Azure, Terraform.", "Singtel", "199201623D", "61000", 7000},
	{"Software Engineer Intern", "25121", "Information Technology", "Intern", intP(0), []string{"hybrid"}, "Internship: Go, React.", "Grab", "200901111H", "62011", 9000},
	{"Junior Software Developer", "25121", "Information Technology", "Fresh/entry level", intP(0), []string{"onsite"}, "Java, Spring Boot, MySQL.", "Accenture", "200010922R", "70201", 10000},
	{"Engineering Manager", "25121", "Information Technology", "Manager", intP(8), []string{"onsite"}, "Lead engineering teams, Java, AWS.", "DBS", "196800150E", "65110", 10000},
	{"Receptionist", "42210", "Administration", "Professional", intP(1), nil, "Front desk duties.", "Lobby Group", "201801111X", "94999", 50},
	{"Accountant", "24110", "Finance", "Professional", intP(3), nil, "Financial reporting.", "PwC", "200106708R", "70201", 9000},
	{"Customer Service Officer", "42230", "Administration", "Professional", intP(1), nil, "Handle customer queries.", "Singtel", "199201623D", "61000", 7000},
	// 年限未标注：spec §3.7-1 要求 NULL 与 0 分开统计，现有模板全部有明确年限
	{"Software Engineer", "25121", "Information Technology", "Professional", nil, []string{"hybrid"}, "Java, Spring Boot, AWS, Kubernetes.", "Zuellig Pharma", "201801111Y", "62011", 2000},
	{"Junior Frontend Engineer", "25131", "Information Technology", "Fresh/entry level", nil, []string{"onsite"}, "React, TypeScript, HTML, CSS.", "Ryde", "201501111M", "62011", 120},
}

func main() {
	rng := rand.New(rand.NewSource(20260803))
	// 2026-06-29 是 ISO 2026-W27 的周一；6 周 × 60 行铺到 W32（周一 2026-08-03）。
	// 测试用固定时钟 2026-08-10（W33 周一）：LastCompletedWeek=W32，基线 W31..W28，
	// 五个窗口全部有数据。日期是 date-only，与线上 API 一致（testdata/live）。
	firstMonday := time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC)
	const weeks, perWeek = 6, 60
	var jobs []mcf.Job
	for i := 0; i < weeks*perWeek; i++ {
		r := rows[i%len(rows)]
		week, dayInWeek := i/perWeek, i%7
		posting := firstMonday.AddDate(0, 0, week*7+dayInWeek)
		uuid := fmt.Sprintf("%032x", rng.Uint64()+rng.Uint64()*1<<32)
		j := mcf.Job{
			UUID:        uuid,
			Title:       r.title,
			Description: "<p>" + r.desc + "</p><ul><li>Relevant degree</li></ul>",
			Metadata: mcf.Metadata{
				JobPostID: fmt.Sprintf("MCF-2026-%07d", 1000000+i),
				// Date-only, matching the live API (testdata/live). RFC3339
				// fixtures once masked a parsing bug that killed the
				// incremental early stop.
				NewPostingDate:            posting.Format("2006-01-02"),
				ExpiryDate:                posting.AddDate(0, 0, 30).Format("2006-01-02"),
				RepostCount:               i % 3,
				TotalNumberOfView:         50 + rng.Intn(2000),
				TotalNumberJobApplication: 1 + rng.Intn(60),
				IsHideSalary:              i%5 == 0,
			},
			SSOCCode:                 r.ssoc,
			OccupationID:             "occ-" + r.ssoc,
			SSOCVersion:              "SSOC2020",
			PositionLevels:           []mcf.PositionLevel{{Position: r.posLevel}},
			MinimumYearsExperience:   r.minYr,
			Salary:                   &mcf.Salary{Minimum: 3000 + float64(rng.Intn(5000)), Maximum: 6000 + float64(rng.Intn(6000)), Type: mcf.SalaryType{ID: 4, SalaryType: "Monthly"}},
			EmploymentTypes:          []mcf.EmploymentType{{ID: 8, EmploymentType: "Full Time"}},
			Categories:               []mcf.Category{{Category: r.category, SubCategory: "Software"}},
			Schemes:                  []mcf.Scheme{},
			FlexibleWorkArrangements: flexOf(r.flex),
			Skills:                   []mcf.Skill{{Skill: "Communication", IsKeySkill: false}, {Skill: "Problem solving", IsKeySkill: true}},
			PostedCompany:            &mcf.PostedCompany{UEN: r.uen, Name: r.company, SSICCode: r.ssic, EmployeeCount: intP(r.emp)},
			Address:                  &mcf.Address{PostalCode: "018956", Districts: []mcf.District{{ID: 7, Location: "Central", Region: "Central"}}, Lat: fp(1.2902), Lng: fp(103.8519), IsOverseas: false},
			Status:                   &mcf.JobStatus{JobStatus: "Active"},
			NumberOfVacancies:        intP(1 + rng.Intn(5)),
		}
		jobs = append(jobs, j)
	}

	out := filepath.Join("testdata", "fixture", "jobs.jsonl")
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, j := range jobs {
		_ = enc.Encode(j)
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %d records to %s\n", len(jobs), out)
}

func intP(i int) *int       { return &i }
func fp(f float64) *float64 { return &f }

// flexOf wraps plain labels in the object shape the live API uses.
func flexOf(labels []string) []mcf.FlexibleWorkArrangement {
	out := make([]mcf.FlexibleWorkArrangement, 0, len(labels))
	for i, l := range labels {
		out = append(out, mcf.FlexibleWorkArrangement{ID: i + 1, FlexibleWorkArrangement: l})
	}
	return out
}
