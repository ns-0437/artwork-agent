# Three-minute demo script

Matches the build brief's demo structure. Every beat below was rehearsed live against the running stack (`docker compose -f infra/docker-compose.yml up`, web UI at http://localhost:5173) immediately before this script was written - not simulated from memory. **Recording the actual video is a step for you to do**: this agent's browser tooling can drive the UI and verify behavior via text, but could not reliably capture screenshots or video in this session (the preview pane's screenshot capture kept timing out while hidden - a tooling limitation, not a claim that the UI doesn't render). Follow the beats below with your own screen recorder (OBS, Loom, Xbox Game Bar, QuickTime) - the app is deterministic enough that a dry run first will make the real take go smoothly.

## 0:00–0:30 — Blocked order, customer request, supported scope

1. Open http://localhost:5173.
2. Fill in the order form: Owner ID (anything), Product type `die-cut-sticker`, Width/Height `3`/`3` in, Intent `full_bleed`, Customer request "Is this ready to print?".
3. Click **Create order**. Point out: case status shows `BLOCKED`, `NOT_PREPARED`, `NOT_RELEASED` - three separate fields, not one flag, per the brief's state model.
4. Say on camera: "This is an independent prototype - no access to any real print shop's production systems, files, or metrics."

## 0:30–1:30 — Inspect → clarify → resume → eligible repair

5. Upload an artwork file (e.g. a solid-color 900×900 PNG - `evals/fixtures/dev/missing-bleed-a_base.png` works, or draw one).
6. Click **Start resolution**. Within a couple seconds: "The agent has a question" appears - **the fixed, deliberately-worded trim-only question**, not the model's own free-form phrasing (a real bug this exact substitution fixed during development - worth a sentence on camera).
7. Click **Yes**. Narrate: "A validated reply enqueues continuation automatically" - watch the Jobs list grow (`inspect → agent_decide → inspect → agent_decide → repair → prepare_proof`, all `SUCCEEDED`) without you doing anything else.
8. Point at the Findings list: the SECOND inspection's `bleed: NEEDS_REVIEW` finding is what made the agent's `request_repair` decision valid - Go re-validates this against the actual findings before acting on it, never trusting the model's stated reasoning at face value.

## 1:30–2:15 — Clear the blocker, show proof awaiting approval; demonstrate an unsafe case remaining in review

9. Final case status: `artworkStatus: RESOLVED`, `proofStatus: AWAITING_CUSTOMER_APPROVAL`. Say: "Resolved means only the supported artwork blocker is cleared - proof approval and production release are separate, unperformed steps. This system never sets `productionStatus` past `NOT_RELEASED`."
10. Start a second order: Intent `border`, a low-resolution upload (e.g. 600×600 PNG for a declared 3×3in size - well under the 300 PPI guideline). Click Start resolution.
11. Final state: `NEEDS_REVIEW`, zero repair attempts, zero clarifications. Say: "Nothing about this case is fixable by confirming trim or repairing bleed - the agent correctly escalates instead of guessing, with a persisted reason in the audit trail."

## 2:15–3:00 — Restart recovery, audit trail, baseline results

12. Say (no need to actually kill a process on camera - reference the written evidence instead): "Every step here - inspect, the agent's decision, a repair, proof preparation - is its own durable job in Postgres, enqueued in the same transaction as the state change that produces it. I verified this by hand-reproducing the exact database and storage state a crash mid-write would leave, twice, and confirming a fresh worker process resumes correctly with no duplicated work - documented honestly as a simulated crash state, not an actual killed process, in `CLAUDE.md` points 21 and 48."
13. Show (screen share or a slide) the results table from `docs/case_study.md`: **34/34 evaluation cases passed, 18/34 orders resolved** - two different numbers, reported separately. Once a deterministic script was given the same escalation capability the agent has, **it matched the agent exactly on all 34 cases**, at zero token cost.
14. Close with the framing: *"I built and evaluated an artwork exception-resolution workflow. The agent matched a deterministic implementation on the tested cases, so I would favor the scripted path for this scope while retaining the agent version for further evaluation."*

## Fallback if recording doesn't go well

The brief allows this explicitly: "If cloud setup blocks delivery, provide local containers and a recording, labeling GCP incomplete." The same principle applies to the demo - a live walkthrough of `docker compose up` plus this script, done in one continuous take without editing, is a legitimate substitute for a polished video if time runs short. Do not present a scripted mockup as if it were the running system.
