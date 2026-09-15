-- Repair jobs carry their own client-supplied idempotency key (distinct from
-- the "one active job per order+type" partial index that already covers
-- in-flight duplicates). This lets a retried requestRepair with the same
-- key find its way back to the repairs row a prior job attempt produced,
-- via repairs.job_id, instead of creating a duplicate repair.
ALTER TABLE jobs ADD COLUMN idempotency_key TEXT;
ALTER TABLE repairs ADD COLUMN job_id UUID REFERENCES jobs(id);
