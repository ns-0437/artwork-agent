-- Makes the agent's decision step a durable, resumable unit of work
-- (job_type='agent_decide') instead of an in-process function call after
-- CompleteInspection commits - a crash between "inspection committed" and
-- "agent decision made" previously left no queued work to resume. The new
-- job is enqueued INSIDE CompleteInspection's own transaction, so it either
-- commits with the inspection result or not at all.
ALTER TABLE jobs DROP CONSTRAINT jobs_job_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_job_type_check
    CHECK (job_type IN ('inspect', 'repair', 'agent_decide'));

-- Binds an agent_decide job to the SPECIFIC inspect job whose findings it
-- must act on - never "the latest finding per check across this order's
-- entire history". input_asset_id/input_case_version (already on every
-- job) freeze WHICH asset/version the decision applies to, same as any
-- other job type.
ALTER TABLE jobs ADD COLUMN agent_source_job_id UUID REFERENCES jobs(id);
