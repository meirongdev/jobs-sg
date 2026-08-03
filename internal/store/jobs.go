package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
)

// ActiveJob is a minimal projection used by reconcile.
type ActiveJob struct {
	UUID       string
	ExpiryDate sql.NullString
	ClosedAt   sql.NullString
}

// UpsertJob inserts or updates one candidate job (uuid is the exact primary
// key; refreshes UPDATE, never INSERT — docs/03 §6, BDD dedup scenarios).
// Returns new=true when a row was inserted.
func (d *DB) UpsertJob(ctx context.Context, j mcf.Job, res classify.Result, rawPath string) (bool, error) {
	now := NowUTC()
	hash := hashHex(j.Description)
	fp := fingerprint(j)
	uen, cname := "", ""
	ssic, ctype := "", ""
	var emp *int
	if j.PostedCompany != nil {
		uen = j.PostedCompany.UEN
		cname = j.PostedCompany.Name
		ssic = j.PostedCompany.SSICCode
		emp = j.PostedCompany.EmployeeCount
		ctype = res.CompanyType
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if uen != "" {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO company(uen, name, name_normalized, ssic_code, employee_count, company_type, first_seen_at, last_seen_at)
			VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(uen) DO UPDATE SET name=excluded.name, name_normalized=excluded.name_normalized,
			  ssic_code=excluded.ssic_code, employee_count=excluded.employee_count, company_type=excluded.company_type,
			  last_seen_at=excluded.last_seen_at`,
			uen, cname, strings.ToUpper(strings.TrimSpace(cname)), ssic, emp, ctype, now, now); err != nil {
			return false, err
		}
	}

	posting := j.Metadata.NewPostingDate
	original := j.Metadata.OriginalPostingDate
	if original == "" {
		original = posting
	}
	empType, cat, posLevel := "", "", ""
	if len(j.EmploymentTypes) > 0 {
		empType = j.EmploymentTypes[0].EmploymentType
	}
	if len(j.Categories) > 0 {
		cat = j.Categories[0].Category
	}
	if len(j.PositionLevels) > 0 {
		posLevel = j.PositionLevels[0].Position
	}
	jobStatus := ""
	if j.Status != nil {
		jobStatus = j.Status.JobStatus
	}

	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM job WHERE uuid=?`, j.UUID).Scan(&exists)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO job(uuid, job_post_id, title, description_sha256, source, canonical_fp,
			  company_uen, ssoc_code, occupation_id, category, position_level, employment_type,
			  min_years_exp, salary_min, salary_max, salary_type, salary_hidden, vacancies,
			  role_family, seniority, work_mode, is_swe, posting_date, original_posting_date,
			  expiry_date, repost_count, status, first_seen_at, last_seen_at, miss_count, closed_at,
			  view_count, application_count, district, postal_code, lat, lng, is_overseas, raw_path)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,NULL,?,?,?,?,?,?,?,?)`,
			j.UUID, j.Metadata.JobPostID, j.Title, hash, "mcf", fp,
			nullStr(uen), j.SSOCCode, j.OccupationID, cat, posLevel, empType,
			nullInt(j.MinimumYearsExperience), salaryMin(j.Salary), salaryMax(j.Salary), salaryType(j.Salary),
			boolInt(j.Metadata.IsHideSalary), nullInt(j.NumberOfVacancies),
			res.RoleFamily, res.Seniority, res.WorkMode, boolInt(res.IsSWE),
			posting, original, j.Metadata.ExpiryDate, j.Metadata.RepostCount, jobStatus,
			now, now, j.Metadata.TotalNumberOfView, j.Metadata.TotalNumberJobApplication,
			firstDistrict(j.Address), addressString(j, "postal"), addressFloat(j, "lat"), addressFloat(j, "lng"),
			boolInt(overseas(j)), rawPath)
		if err != nil {
			return false, err
		}
		if err := upsertSkills(ctx, tx, j); err != nil {
			return false, err
		}
		return true, tx.Commit()
	} else if err != nil {
		return false, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE job SET job_post_id=?, title=?, description_sha256=?, company_uen=?,
		  ssoc_code=?, occupation_id=?, category=?, position_level=?, employment_type=?,
		  min_years_exp=?, salary_min=?, salary_max=?, salary_type=?, salary_hidden=?, vacancies=?,
		  role_family=?, seniority=?, work_mode=?, is_swe=?, posting_date=?,
		  expiry_date=?, repost_count=?, status=?, last_seen_at=?, miss_count=0,
		  view_count=?, application_count=?, district=?, postal_code=?, lat=?, lng=?, is_overseas=?,
		  raw_path=? WHERE uuid=?`,
		j.Metadata.JobPostID, j.Title, hash, nullStr(uen),
		j.SSOCCode, j.OccupationID, cat, posLevel, empType,
		nullInt(j.MinimumYearsExperience), salaryMin(j.Salary), salaryMax(j.Salary), salaryType(j.Salary),
		boolInt(j.Metadata.IsHideSalary), nullInt(j.NumberOfVacancies),
		res.RoleFamily, res.Seniority, res.WorkMode, boolInt(res.IsSWE), posting,
		j.Metadata.ExpiryDate, j.Metadata.RepostCount, jobStatus, now,
		j.Metadata.TotalNumberOfView, j.Metadata.TotalNumberJobApplication,
		firstDistrict(j.Address), addressString(j, "postal"), addressFloat(j, "lat"), addressFloat(j, "lng"),
		boolInt(overseas(j)), rawPath, j.UUID)
	if err != nil {
		return false, err
	}
	if err := upsertSkills(ctx, tx, j); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// QueryWatermark returns the max posting_date in job (NULL on first run).
func (d *DB) QueryWatermark(ctx context.Context) (sql.NullString, error) {
	var wm sql.NullString
	err := d.QueryRowContext(ctx, `SELECT max(posting_date) FROM job`).Scan(&wm)
	return wm, err
}

// ActiveCandidates lists candidate jobs currently considered open
// (closed_at IS NULL), for reconcile.
func (d *DB) ActiveCandidates(ctx context.Context) ([]ActiveJob, error) {
	rows, err := d.QueryContext(ctx, `SELECT uuid, expiry_date, closed_at FROM job WHERE closed_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveJob
	for rows.Next() {
		var a ActiveJob
		if err := rows.Scan(&a.UUID, &a.ExpiryDate, &a.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkSeen refreshes a job seen during reconcile: last_seen_at, counts,
// miss_count=0, and clears closed_at (reopen — never counts as new).
func (d *DB) MarkSeen(ctx context.Context, uuid string, view, app int) error {
	_, err := d.ExecContext(ctx, `
		UPDATE job SET last_seen_at=?, miss_count=0, closed_at=NULL,
		  view_count=?, application_count=? WHERE uuid=?`,
		NowUTC(), view, app, uuid)
	return err
}

// CloseExpired closes (closed_at=now) jobs whose expiry_date < today —
// only safe when the reconcile scan was status='success' (docs/02 §4.1).
func (d *DB) CloseExpired(ctx context.Context, today string) (int, error) {
	res, err := d.ExecContext(ctx, `
		UPDATE job SET closed_at=?, miss_count=0
		WHERE closed_at IS NULL AND expiry_date IS NOT NULL AND expiry_date < ?`,
		NowUTC(), today)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// MissAndClose increments miss_count for open jobs not seen in this round and
// closes those with miss_count >= 2 (two consecutive weeks unseen — the
// anti-race guard from docs/02 §4.1). Returns number newly closed.
func (d *DB) MissAndClose(ctx context.Context, seen map[string]bool) (int, error) {
	rows, err := d.QueryContext(ctx, `SELECT uuid FROM job WHERE closed_at IS NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var toBump []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return 0, err
		}
		if !seen[u] {
			toBump = append(toBump, u)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	closed := 0
	for _, u := range toBump {
		var cur int
		err := d.QueryRowContext(ctx, `SELECT miss_count FROM job WHERE uuid=? AND closed_at IS NULL`, u).Scan(&cur)
		if err == sql.ErrNoRows {
			continue // already closed by another path
		}
		if err != nil {
			return closed, err
		}
		next := cur + 1
		if next >= 2 {
			if _, err := d.ExecContext(ctx, `UPDATE job SET miss_count=?, closed_at=? WHERE uuid=?`, next, NowUTC(), u); err != nil {
				return closed, err
			}
			closed++
		} else {
			if _, err := d.ExecContext(ctx, `UPDATE job SET miss_count=? WHERE uuid=?`, next, u); err != nil {
				return closed, err
			}
		}
	}
	return closed, nil
}

func upsertSkills(ctx context.Context, tx *sql.Tx, j mcf.Job) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_skill WHERE job_uuid=?`, j.UUID); err != nil {
		return err
	}
	for _, s := range j.Skills {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO job_skill(job_uuid, skill, is_key_skill) VALUES(?,?,?)`,
			j.UUID, s.Skill, boolInt(s.IsKeySkill)); err != nil {
			return err
		}
	}
	return nil
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// fingerprint is a placeholder canonical identity for Phase 3 multi-source
// dedup (docs/06). MVP: title+UEN hash; repost merging is out of scope.
func fingerprint(j mcf.Job) string {
	uen := ""
	if j.PostedCompany != nil {
		uen = j.PostedCompany.UEN
	}
	return hashHex(strings.ToLower(strings.TrimSpace(j.Title)) + "|" + uen)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
func salaryMin(s *mcf.Salary) any {
	if s == nil {
		return nil
	}
	return int(s.Minimum)
}
func salaryMax(s *mcf.Salary) any {
	if s == nil {
		return nil
	}
	return int(s.Maximum)
}
func salaryType(s *mcf.Salary) any {
	if s == nil || s.Type.SalaryType == "" {
		return nil
	}
	return s.Type.SalaryType
}
func firstDistrict(a *mcf.Address) any {
	if a == nil || len(a.Districts) == 0 {
		return nil
	}
	return a.Districts[0].Location
}
func addressString(j mcf.Job, field string) any {
	if j.Address == nil {
		return nil
	}
	switch field {
	case "postal":
		if j.Address.PostalCode == "" {
			return nil
		}
		return j.Address.PostalCode
	}
	return nil
}
func addressFloat(j mcf.Job, field string) any {
	if j.Address == nil {
		return nil
	}
	switch field {
	case "lat":
		if j.Address.Lat == nil {
			return nil
		}
		return *j.Address.Lat
	case "lng":
		if j.Address.Lng == nil {
			return nil
		}
		return *j.Address.Lng
	}
	return nil
}
func overseas(j mcf.Job) bool {
	return j.Address != nil && j.Address.IsOverseas
}
