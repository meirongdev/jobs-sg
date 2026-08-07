package metric

import (
	"context"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// SkillDemandLimit is how many skill tags the table shows.
const SkillDemandLimit = 15

// SkillDemand is one of MCF's own skill tags and how often employers ask for it.
//
// Deliberately NOT the tech stack. MCF's skills[] carries business
// competencies — "Problem solving", "Stakeholder management" — which is exactly
// why this system runs an LLM over the description at all: the languages and
// frameworks appear nowhere else (docs/02 §4.2). The two answer different
// questions and must never be merged into one ranking.
type SkillDemand struct {
	Skill    string
	Postings int     // postings listing it
	Share    float64 // of every posting in the window
	KeyShare float64 // of the postings listing it, the share marking it a must-have
}

// SkillDemandFor ranks the skill tags employers attach to postings in the
// window.
//
// The must-have split is the part worth reading: a tag that appears everywhere
// but is rarely marked key is table stakes, while one that is usually key is a
// filter someone is actually applying.
func SkillDemandFor(ctx context.Context, db *store.DB, w Window, lens Lens) ([]SkillDemand, int, error) {
	var denom int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) `+swePosted+lens.Where(), w.Args()...).Scan(&denom); err != nil {
		return nil, 0, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT s.skill,
		       count(DISTINCT j.uuid),
		       count(DISTINCT CASE WHEN s.is_key_skill=1 THEN j.uuid END)
		`+sweFrom+` JOIN job_skill s ON s.job_uuid = j.uuid
		`+sweWhere+lens.Where()+`
		  AND s.skill IS NOT NULL AND s.skill <> ''
		GROUP BY s.skill
		ORDER BY count(DISTINCT j.uuid) DESC, s.skill ASC
		LIMIT ?`, append(w.Args(), SkillDemandLimit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []SkillDemand
	for rows.Next() {
		var s SkillDemand
		var key int
		if err := rows.Scan(&s.Skill, &s.Postings, &key); err != nil {
			return nil, 0, err
		}
		if denom > 0 {
			s.Share = float64(s.Postings) / float64(denom)
		}
		if s.Postings > 0 {
			s.KeyShare = float64(key) / float64(s.Postings)
		}
		out = append(out, s)
	}
	return out, denom, rows.Err()
}
