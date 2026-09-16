package store

import (
	"context"
	"time"
)

// ToolEvent is the audit-trail record of one agent attempt (or a script's
// deterministic escalation, via store.EscalateCase) - never load-bearing
// for any decision (see LogAgentToolEvent's comment), read-only from here.
type ToolEvent struct {
	ID        string
	OrderID   string
	JobID     *string
	EventType string
	Detail    string // JSON-encoded
	CreatedAt time.Time
}

// ListToolEventsForOrder returns every tool_events row for an order, oldest
// first - used by evals/scripts/run_eval.py to aggregate provider token
// usage for the agent-vs-scripted cost comparison (CLAUDE.md's Day 5
// section), and generally as the audit trail the brief asks for.
func (s *Store) ListToolEventsForOrder(ctx context.Context, orderID string) ([]ToolEvent, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, order_id, job_id, event_type, detail::text, created_at
		FROM tool_events WHERE order_id = $1 ORDER BY created_at ASC
	`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ToolEvent
	for rows.Next() {
		var e ToolEvent
		if err := rows.Scan(&e.ID, &e.OrderID, &e.JobID, &e.EventType, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
