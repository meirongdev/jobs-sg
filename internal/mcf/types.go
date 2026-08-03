// Package mcf models MyCareersFuture's public JSON API and provides a
// compliant client. Field shapes follow the 2026-08-02 site survey
// (docs/archive/2026-08-02-site-survey.md): list items and detail items have
// identical fields, so no second fetch is needed.
package mcf

// Page is the /v2/jobs paginated response envelope.
type Page struct {
	Results              []Job   `json:"results"`
	Total                int     `json:"total"`
	Links                *struct {
		Self *string `json:"self"`
		Next *string `json:"next"`
	} `json:"_links"`
	CountWithoutFilters *int `json:"countWithoutFilters,omitempty"`
}

// Job is a single MCF job posting. All fields are public business data;
// publisher-personal fields (createdBy / emailRecipient) are deliberately
// NOT modeled (compliance red line, docs/01 §5).
type Job struct {
	UUID                    string            `json:"uuid"`
	Title                   string            `json:"title"`
	Description             string            `json:"description"`
	Metadata                Metadata          `json:"metadata"`
	SSOCCode                string            `json:"ssocCode"`
	OccupationID            string            `json:"occupationId"`
	SSOCVersion             string            `json:"ssocVersion"`
	PositionLevels          []PositionLevel   `json:"positionLevels"`
	MinimumYearsExperience  *int              `json:"minimumYearsExperience"`
	Salary                  *Salary           `json:"salary"`
	EmploymentTypes         []string          `json:"employmentTypes"`
	Categories              []Category        `json:"categories"`
	Schemes                 []string          `json:"schemes"`
	FlexibleWorkArrangements []string         `json:"flexibleWorkArrangements"`
	Skills                  []Skill           `json:"skills"`
	PostedCompany           *PostedCompany    `json:"postedCompany"`
	Address                 *Address          `json:"address"`
	Status                  *JobStatus        `json:"status"`
	NumberOfVacancies       *int              `json:"numberOfVacancies"`
	ScreeningQuestions      []interface{}     `json:"screeningQuestions"`
}

type Metadata struct {
	JobPostID            string `json:"jobPostId"`
	NewPostingDate       string `json:"newPostingDate"`
	OriginalPostingDate  string `json:"originalPostingDate"`
	ExpiryDate           string `json:"expiryDate"`
	RepostCount          int    `json:"repostCount"`
	TotalNumberOfView    int    `json:"totalNumberOfView"`
	TotalNumberJobApplication int `json:"totalNumberJobApplication"`
	IsHideSalary         bool   `json:"isHideSalary"`
}

type PositionLevel struct {
	Position string `json:"position"`
}

type Salary struct {
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
	Type    string  `json:"type"`
}

type Category struct {
	Category    string `json:"category"`
	SubCategory string `json:"subCategory"`
}

type Skill struct {
	Skill       string `json:"skill"`
	IsKeySkill  bool   `json:"isKeySkill"`
}

type PostedCompany struct {
	UEN           string `json:"uen"`
	Name          string `json:"name"`
	SSICCode      string `json:"ssicCode"`
	EmployeeCount *int   `json:"employeeCount"`
}

type Address struct {
	PostalCode  string   `json:"postalCode"`
	Districts   []string `json:"districts"`
	Lat         *float64 `json:"lat"`
	Lng         *float64 `json:"lng"`
	IsOverseas  bool     `json:"isOverseas"`
}

type JobStatus struct {
	JobStatus string `json:"jobStatus"`
}
