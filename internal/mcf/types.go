// Package mcf models MyCareersFuture's public JSON API and provides a
// compliant client. List items and detail items have identical fields, so no
// second fetch is needed (2026-08-02 site survey,
// docs/archive/2026-08-02-site-survey.md).
//
// The survey's *field shapes* were wrong, though: employmentTypes, schemes,
// flexibleWorkArrangements, salary.type and address.districts are all objects
// (or lists of objects) on the live API, not strings. They were modeled as
// strings until 2026-08-03, which made encoding/json reject every page — and
// because a Page decodes as a whole, one bad job discarded all 100. Verified
// against 500 live postings; see the enumeration in TestJobDecodeLiveShapes.
package mcf

import "time"

// Page is the /v2/jobs paginated response envelope.
type Page struct {
	Results             []Job      `json:"results"`
	Total               int        `json:"total"`
	Links               *PageLinks `json:"_links"`
	CountWithoutFilters *int       `json:"countWithoutFilters,omitempty"`
}

// PageLinks is the HAL-style envelope links. Each entry is an object with an
// href, not a bare URL string — modeling them as *string made the whole page
// fail to decode. Nothing reads these yet (paging is driven by the page param
// plus total); they are modeled so the envelope decodes.
type PageLinks struct {
	Self  *Link `json:"self"`
	Next  *Link `json:"next"`
	First *Link `json:"first"`
	Last  *Link `json:"last"`
}

// Link is a HAL link, e.g. {"href":"https://api.../v2/jobs?page=1"}.
type Link struct {
	Href string `json:"href"`
}

// Job is a single MCF job posting. All fields are public business data;
// publisher-personal fields (createdBy / emailRecipient) are deliberately
// NOT modeled (compliance red line, docs/01 §5).
type Job struct {
	UUID                     string                    `json:"uuid"`
	Title                    string                    `json:"title"`
	Description              string                    `json:"description"`
	Metadata                 Metadata                  `json:"metadata"`
	SSOCCode                 string                    `json:"ssocCode"`
	OccupationID             string                    `json:"occupationId"`
	SSOCVersion              string                    `json:"ssocVersion"`
	PositionLevels           []PositionLevel           `json:"positionLevels"`
	MinimumYearsExperience   *int                      `json:"minimumYearsExperience"`
	Salary                   *Salary                   `json:"salary"`
	EmploymentTypes          []EmploymentType          `json:"employmentTypes"`
	Categories               []Category                `json:"categories"`
	Schemes                  []Scheme                  `json:"schemes"`
	FlexibleWorkArrangements []FlexibleWorkArrangement `json:"flexibleWorkArrangements"`
	Skills                   []Skill                   `json:"skills"`
	PostedCompany            *PostedCompany            `json:"postedCompany"`
	Address                  *Address                  `json:"address"`
	Status                   *JobStatus                `json:"status"`
	NumberOfVacancies        *int                      `json:"numberOfVacancies"`
	ScreeningQuestions       []interface{}             `json:"screeningQuestions"`
}

// WorkArrangements projects flexibleWorkArrangements to the plain labels
// classify.WorkMode consumes.
func (j Job) WorkArrangements() []string {
	out := make([]string, 0, len(j.FlexibleWorkArrangements))
	for _, f := range j.FlexibleWorkArrangements {
		out = append(out, f.FlexibleWorkArrangement)
	}
	return out
}

type Metadata struct {
	JobPostID                 string `json:"jobPostId"`
	NewPostingDate            string `json:"newPostingDate"`
	OriginalPostingDate       string `json:"originalPostingDate"`
	ExpiryDate                string `json:"expiryDate"`
	RepostCount               int    `json:"repostCount"`
	TotalNumberOfView         int    `json:"totalNumberOfView"`
	TotalNumberJobApplication int    `json:"totalNumberJobApplication"`
	IsHideSalary              bool   `json:"isHideSalary"`
}

// ParsePostingDate parses the Metadata date fields (newPostingDate,
// originalPostingDate, expiryDate). The live API returns date-only values
// ("2026-08-03") — parsing them as RFC3339 only made ingest's incremental
// early-stop dead code, so every nightly scan ran to the page-limit circuit
// breaker and finished partial. RFC3339 stays accepted as a fallback in case
// the upstream format changes.
func ParsePostingDate(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

type PositionLevel struct {
	Position string `json:"position"`
}

type Salary struct {
	Minimum float64    `json:"minimum"`
	Maximum float64    `json:"maximum"`
	Type    SalaryType `json:"type"`
}

// SalaryType is salary.type — an object, e.g. {"id":4,"salaryType":"Monthly"}.
type SalaryType struct {
	ID         int    `json:"id"`
	SalaryType string `json:"salaryType"`
}

// EmploymentType e.g. {"id":8,"employmentType":"Full Time"}.
type EmploymentType struct {
	ID             int    `json:"id"`
	EmploymentType string `json:"employmentType"`
}

// FlexibleWorkArrangement e.g. {"id":6,"flexibleWorkArrangement":"Creative Scheduling"}.
// Non-empty on ~6% of postings.
type FlexibleWorkArrangement struct {
	ID                      int    `json:"id"`
	FlexibleWorkArrangement string `json:"flexibleWorkArrangement"`
}

// Scheme is a government scheme attached to a posting (~2% of postings).
// Nothing downstream reads it yet; modeled only so it decodes.
type Scheme struct {
	StartDate  string `json:"startDate"`
	ExpiryDate string `json:"expiryDate"`
	Scheme     struct {
		ID     int    `json:"id"`
		Scheme string `json:"scheme"`
	} `json:"scheme"`
}

type Category struct {
	Category    string `json:"category"`
	SubCategory string `json:"subCategory"`
}

type Skill struct {
	Skill      string `json:"skill"`
	IsKeySkill bool   `json:"isKeySkill"`
}

type PostedCompany struct {
	UEN           string `json:"uen"`
	Name          string `json:"name"`
	SSICCode      string `json:"ssicCode"`
	EmployeeCount *int   `json:"employeeCount"`
}

type Address struct {
	PostalCode string     `json:"postalCode"`
	Districts  []District `json:"districts"`
	Lat        *float64   `json:"lat"`
	Lng        *float64   `json:"lng"`
	IsOverseas bool       `json:"isOverseas"`
}

// District e.g. {"id":998,"location":"Islandwide","region":"Islandwide"}.
type District struct {
	ID       int    `json:"id"`
	Location string `json:"location"`
	Region   string `json:"region"`
}

type JobStatus struct {
	JobStatus string `json:"jobStatus"`
}
