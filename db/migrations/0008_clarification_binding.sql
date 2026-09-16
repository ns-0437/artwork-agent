-- Binds every clarification to the artwork_version it was actually asked
-- about, and adds a way to invalidate one without conflating that with a
-- real customer answer. Without this, a replacement upload (which reopens
-- the case and bumps case_version) left an OLD unanswered clarification
-- fully answerable using the order's now-current case_version - silently
-- applying the customer's reply to artwork the question was never actually
-- about.
ALTER TABLE clarifications ADD COLUMN artwork_version INTEGER;
UPDATE clarifications c SET artwork_version = o.artwork_version
    FROM orders o WHERE o.id = c.order_id AND c.artwork_version IS NULL;
ALTER TABLE clarifications ALTER COLUMN artwork_version SET NOT NULL;

-- Distinct from answered_at: invalidated_at marks a question that was
-- superseded by a replacement upload before the customer ever answered it -
-- never actually answered, so conflating it with answered_at would misrepresent
-- the audit trail as if the customer had replied.
ALTER TABLE clarifications ADD COLUMN invalidated_at TIMESTAMPTZ;
