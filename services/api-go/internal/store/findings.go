package store

import "context"

func (s *Store) SaveFinding(ctx context.Context, orderID string, jobID *string, checkName, result, evidenceJSON, ruleVersion string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO findings (order_id, job_id, check_name, result, evidence, rule_version)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
	`, orderID, jobID, checkName, result, evidenceJSON, ruleVersion)
	return err
}

func (s *Store) ListFindings(ctx context.Context, orderID string) ([]Finding, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, order_id, job_id, check_name, result, evidence::text, rule_version, created_at
		FROM findings WHERE order_id = $1 ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.ID, &f.OrderID, &f.JobID, &f.CheckName, &f.Result, &f.Evidence, &f.RuleVersion, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
