-- Adds proof preparation: loop step 5 of the brief ("Verify authorized
-- repairs and recheck blockers. Prepare a proof, update the artwork state,
-- and retain the complete audit trail."). This was previously the one
-- unimplemented step - proof_status could never leave NOT_PREPARED because
-- nothing ever created and stored the proof artifact that transition
-- requires (see CLAUDE.md point 3).
--
-- A 'proof' asset kind: the rendered proof image (final artwork + a caption
-- banner identifying order/version, distinct from the repair preview's
-- trim/bleed overlays), tied to the order and its artwork/case version per
-- the brief's security essentials.
ALTER TABLE assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE assets ADD CONSTRAINT assets_kind_check
    CHECK (kind IN ('original', 'repaired', 'preview', 'proof'));

-- A 'prepare_proof' job type: enqueued atomically (same transaction) by
-- CompleteInspection or CompleteRepair whenever their result is RESOLVED,
-- the same durable job-queue pattern already used for agent_decide - a
-- crash between "case resolved" and "proof prepared" must leave a queued
-- job to resume, not silently drop the step.
ALTER TABLE jobs DROP CONSTRAINT jobs_job_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_job_type_check
    CHECK (job_type IN ('inspect', 'repair', 'agent_decide', 'prepare_proof'));
