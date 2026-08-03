package store

import "context"

// LoadSSOCMap returns ssoc_code -> role_family for classification.
func (d *DB) LoadSSOCMap(ctx context.Context) (map[string]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT ssoc_code, role_family FROM ssoc_taxonomy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var code, fam string
		if err := rows.Scan(&code, &fam); err != nil {
			return nil, err
		}
		m[code] = fam
	}
	return m, rows.Err()
}

// LoadTechTaxonomy returns (alias, tech_slug, tech_kind) rows for the rule
// layer.
func (d *DB) LoadTechTaxonomy(ctx context.Context) ([][3]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT alias, tech_slug, tech_kind FROM tech_taxonomy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var a, slug, kind string
		if err := rows.Scan(&a, &slug, &kind); err != nil {
			return nil, err
		}
		out = append(out, [3]string{a, slug, kind})
	}
	return out, rows.Err()
}
