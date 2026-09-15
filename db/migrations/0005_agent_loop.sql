-- Agent loop bookkeeping: how many decision calls/retries this case has
-- used. Persisted across replies (per the brief: "Persist the case budget
-- across replies") so the bounded loop - 5 tool calls + 2 transient retries
-- per execution segment - can't be reset just by answering a clarification.
-- Reset only on a genuinely new artwork upload (RecordArtworkUpload), never
-- on a clarification reply.
ALTER TABLE orders ADD COLUMN agent_tool_calls_used INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN agent_retries_used INTEGER NOT NULL DEFAULT 0;
