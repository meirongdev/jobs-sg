// Package classify implements the SWE classification criteria from
// docs/03-data-model.md §4–§5: two-level predicates (is_candidate wide,
// is_swe strict), role_family, seniority, work_mode and company_type.
//
// Rules are pure functions over an in-memory ssoc_taxonomy map so they are
// trivially unit-testable; the caller loads the map from SQLite (store.Seed).
package classify

import (
	"regexp"
	"slices"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/mcf"
)

// HitLayer records which of the three ordered rules admitted a candidate
// (docs/03 §4) so criteria changes are re-traceable.
type HitLayer string

const (
	HitSSOC     HitLayer = "ssoc"
	HitCategory HitLayer = "category"
	HitTitle    HitLayer = "title"
	HitNone     HitLayer = ""
)

// Result is the full derived classification for one job.
type Result struct {
	IsCandidate      bool
	HitLayer         HitLayer
	RoleFamily       string
	IsSWE            bool
	Seniority        string
	WorkMode         string
	WorkModeInferred bool // true when work_mode is an inferred Onsite
	CompanyType      string
}

// Role families (docs/01 §3).
const (
	FamilyBackend   = "Backend"
	FamilyFrontend  = "Frontend"
	FamilyFullstack = "Fullstack"
	FamilyMobile    = "Mobile"
	FamilyPlatform  = "Platform"
	FamilySRE       = "SRE"
	FamilyData      = "Data"
	FamilyAIML      = "AI-ML"
	FamilySecurity  = "Security"
	FamilyOther     = "Other-IT"
)

// sweFamilies are the strict is_swe families (MVP default; pending Phase 0
// human verification per docs/03 §4).
var sweFamilies = map[string]bool{
	FamilyBackend: true, FamilyFrontend: true, FamilyFullstack: true,
	FamilyMobile: true, FamilyPlatform: true, FamilySRE: true,
	FamilyData: true, FamilyAIML: true, FamilySecurity: true,
}

// ssocWhitelist: 3-digit SSOC prefixes for the SSOC primary rule.
// docs/03 §4: 251 = Software and Applications Developers and Analysts.
var ssocWhitelist = []string{"251"}

var candidateTitleRe = regexp.MustCompile(`(?i)\b(software engineer|software developer|programmer|sre|site reliability engineer|backend engineer|frontend engineer|fullstack|full-stack|full stack|data engineer|data scientist|machine learning engineer|ml engineer|ai engineer|ai/ml|mobile engineer|android developer|ios developer|devops engineer|platform engineer|security engineer|cloud engineer|web developer|applications developer|software architect)\b`)

// roleFamilyTitleOverrides: title keyword -> role_family. Applied BEFORE
// ssoc_taxonomy (title is the strongest actual-hiring signal, docs/03 §5).
var roleFamilyTitleOverrides = []struct {
	re  *regexp.Regexp
	fam string
}{
	{regexp.MustCompile(`(?i)\bfrontend|front-end|front end\b`), FamilyFrontend},
	{regexp.MustCompile(`(?i)\bbackend|back-end|back end\b`), FamilyBackend},
	{regexp.MustCompile(`(?i)\bfullstack|full-stack|full stack\b`), FamilyFullstack},
	{regexp.MustCompile(`(?i)\bmachine learning|ml engineer|ai engineer|artificial intelligence|nlp|deep learning\b`), FamilyAIML},
	{regexp.MustCompile(`(?i)\bdata engineer|data scientist|data analyst|big data\b`), FamilyData},
	{regexp.MustCompile(`(?i)\bmobile|android|ios developer\b`), FamilyMobile},
	{regexp.MustCompile(`(?i)\bsite reliability|sre\b`), FamilySRE},
	{regexp.MustCompile(`(?i)\bsecurity engineer|cyber|infosec|application security\b`), FamilySecurity},
	{regexp.MustCompile(`(?i)\bdevops|platform engineer|cloud engineer|infrastructure\b`), FamilyPlatform},
}

var seniorityTitleRe = []struct {
	re  *regexp.Regexp
	lvl string
}{
	{regexp.MustCompile(`(?i)\bintern\b`), "Intern"},
	{regexp.MustCompile(`(?i)\bjunior\b`), "Junior"},
	{regexp.MustCompile(`(?i)\bstaff|principal\b`), "Staff+"},
	{regexp.MustCompile(`(?i)\blead\b`), "Lead"},
	{regexp.MustCompile(`(?i)\bmanager|director|head of\b`), "Manager"},
	{regexp.MustCompile(`(?i)\bsenior\b`), "Senior"},
}

// seniorityLevels is the seniority vocabulary in career order. It is the one
// definition: seniorityRank derives from it, and SeniorityLevels exports it for
// the pages that render seniority rows, so a level added here appears
// everywhere instead of being re-listed per consumer.
var seniorityLevels = []string{"Intern", "Junior", "Mid", "Senior", "Staff+", "Lead", "Manager"}

// SeniorityLevels returns the seniority vocabulary in career order.
func SeniorityLevels() []string { return slices.Clone(seniorityLevels) }

func seniorityRank(s string) int { return slices.Index(seniorityLevels, s) }

// Classifier holds the SSOC -> role_family taxonomy.
type Classifier struct {
	ssoc map[string]string
}

// New builds a Classifier from a 5-digit ssoc_code -> role_family map.
func New(ssoc map[string]string) *Classifier {
	return &Classifier{ssoc: ssoc}
}

// Classify derives the full Result for one job.
func (c *Classifier) Classify(j mcf.Job) Result {
	res := Result{WorkMode: "Onsite", WorkModeInferred: true} // default: inferred Onsite
	res.IsCandidate, res.HitLayer = isCandidate(j)
	res.RoleFamily = c.roleFamily(j.Title, j.SSOCCode)
	res.IsSWE = res.IsCandidate && sweFamilies[res.RoleFamily]
	res.Seniority = Seniority(j.Title, positionLevel(j), years(j))
	res.WorkMode, res.WorkModeInferred = WorkMode(j.WorkArrangements())
	res.CompanyType = CompanyType(j)
	return res
}

// isCandidate implements the three-layer union predicate (docs/03 §4).
func isCandidate(j mcf.Job) (bool, HitLayer) {
	if hasSSOCPrefix(j.SSOCCode, ssocWhitelist) {
		return true, HitSSOC
	}
	for _, cat := range j.Categories {
		if strings.EqualFold(cat.Category, "Information Technology") {
			return true, HitCategory
		}
	}
	if candidateTitleRe.MatchString(j.Title) {
		return true, HitTitle
	}
	return false, HitNone
}

func hasSSOCPrefix(code string, prefixes []string) bool {
	if len(code) < 3 {
		return false
	}
	for _, p := range prefixes {
		if strings.HasPrefix(code, p) {
			return true
		}
	}
	return false
}

// roleFamily resolves the family: title override first, then ssoc_taxonomy,
// then a 251-prefix default, else Other-IT.
func (c *Classifier) roleFamily(title, ssoc string) string {
	for _, o := range roleFamilyTitleOverrides {
		if o.re.MatchString(title) {
			return o.fam
		}
	}
	if f, ok := c.ssoc[ssoc]; ok && f != "" {
		return f
	}
	if strings.HasPrefix(ssoc, "251") {
		return FamilyBackend
	}
	return FamilyOther
}

func positionLevel(j mcf.Job) string {
	if len(j.PositionLevels) > 0 {
		return j.PositionLevels[0].Position
	}
	return ""
}

func years(j mcf.Job) int {
	if j.MinimumYearsExperience == nil {
		return -1
	}
	return *j.MinimumYearsExperience
}

// Seniority combines title, position_level and min_years_exp; on conflict the
// title wins (docs/03 §5, docs/08 BDD "资历冲突时以标题为准").
func Seniority(title, level string, years int) string {
	if t := seniorityFromTitle(title); t != "" {
		return t
	}
	l := seniorityFromLevel(level)
	y := seniorityFromYears(years)
	if l == "" {
		return y
	}
	if y == "" {
		return l
	}
	if seniorityRank(l) >= seniorityRank(y) {
		return l
	}
	return y
}

func seniorityFromTitle(title string) string {
	for _, t := range seniorityTitleRe {
		if t.re.MatchString(title) {
			return t.lvl
		}
	}
	return ""
}

func seniorityFromLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "intern":
		return "Intern"
	case "fresh/entry level", "entry level", "fresh":
		return "Junior"
	case "professional":
		return "Mid"
	case "manager", "senior management", "executive":
		return "Manager"
	}
	return ""
}

func seniorityFromYears(years int) string {
	switch {
	case years <= 1:
		return "Junior"
	case years <= 4:
		return "Mid"
	case years >= 5:
		return "Senior"
	}
	return ""
}

// WorkMode derives from flexibleWorkArrangements; empty => inferred Onsite.
func WorkMode(arrangements []string) (mode string, inferred bool) {
	for _, a := range arrangements {
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "remote", "fully remote":
			return "Remote", false
		case "hybrid", "hybrid work arrangement":
			return "Hybrid", false
		case "onsite", "on-site":
			return "Onsite", false
		}
	}
	return "Onsite", true
}

// CompanyType derives MNC/Local Tech/Bank&FinTech/Startup/Government/Consulting
// from UEN + name + SSIC + employee_count. MVP heuristic — documented as
// approximate; refinement (manual rule table) is Phase 0+ work (docs/03 §5).
func CompanyType(j mcf.Job) string {
	name := ""
	if j.PostedCompany != nil {
		name = j.PostedCompany.Name
	}
	uen := ""
	ssic := ""
	var emp *int
	if j.PostedCompany != nil {
		uen = j.PostedCompany.UEN
		ssic = j.PostedCompany.SSICCode
		emp = j.PostedCompany.EmployeeCount
	}
	if uen != "" && strings.HasPrefix(strings.ToUpper(uen), "T") || govName(name) {
		return "Government"
	}
	if bankSSIC(ssic) {
		return "Bank & FinTech"
	}
	if consultSSIC(ssic) || consultName(name) {
		return "Consulting"
	}
	if emp != nil {
		if *emp > 0 && *emp < 200 {
			return "Startup"
		}
		if *emp >= 1000 {
			return "MNC"
		}
	}
	if itSSIC(ssic) {
		return "Local Tech"
	}
	return "Other"
}

func govName(name string) bool {
	n := strings.ToUpper(name)
	for _, k := range []string{"GOVERNMENT", "MINISTRY", "STATUTORY", "PUBLIC SERVICE", "AUTHORITY"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}

func bankSSIC(ssic string) bool {
	// Financial services SSIC divisions 64, 65, 66
	for _, p := range []string{"64", "65", "66"} {
		if strings.HasPrefix(ssic, p) {
			return true
		}
	}
	return false
}

func consultSSIC(ssic string) bool {
	// 7020 business/management consultancy; 6202 computer consultancy
	return strings.HasPrefix(ssic, "702") || strings.HasPrefix(ssic, "6202")
}

func consultName(name string) bool {
	return strings.Contains(strings.ToUpper(name), "CONSULT")
}

func itSSIC(ssic string) bool {
	// 620x computer programming/consultancy; 631 data processing/hosting
	return strings.HasPrefix(ssic, "620") || strings.HasPrefix(ssic, "631")
}
