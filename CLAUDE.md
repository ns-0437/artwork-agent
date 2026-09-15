# Artwork Exception Resolution Agent

Independent six-day portfolio prototype built for a specific AI Agent Engineer application (stack: Go, TypeScript, GraphQL, Postgres, GCP). Not affiliated with, and has no access to, any real company's production systems.

**Deliverable:** resolve an artwork blocker on a simulated sticker order — inspect the file, clarify customer intent if needed, perform one eligible repair (uniform-background bleed extension), verify it, and prepare a proof — with the case persisted so work resumes after replies or failures.

Spec of record: `docs/build-brief.pdf` (the six-day build brief). When this file and the brief disagree, the brief wins; update this file to match.

## Code structure

```
web/                    TypeScript UI — case view, upload, clarification Q&A. Polls case status.
services/api-go/        Go GraphQL API + worker. Owns workflow, validation, state transitions, the agent loop.
services/image-python/  Python/Pillow. Owns ONLY deterministic image inspection + the one repair. No workflow logic.
db/migrations/          Postgres schema (orders, jobs, assets, findings, clarifications, repairs, tool_events).
evals/                  Fixture generation + evaluation harness (dev/held-out split, baseline vs agent).
infra/                  Docker Compose (local) + Cloud Run/GCP deploy config.
docs/                   Architecture, evaluation results, case study, demo notes.
```

(This section is a skeleton as of Day 0 — update it as real files land under each directory.)

## Points to remember

1. **Rules are deterministic; the model never computes or overrides a measurement.** PPI, color-mode/profile, and bleed-coverage math live in `services/image-python`, are unit-tested, and are versioned (`rule_version`). The agent (Go + LLM adapter) only chooses among `ask_clarification` / `request_repair` / `escalate` based on the rules' output — it cannot recalculate or second-guess a check result.
2. **REPAIRED is only set after exact pixel-equality verification** of the original content region (see `services/image-python/app/repair/verify.py` when it exists). Never accept a perceptual/similarity score as proof — an encode/decode change must not slip through.
3. **`proof_status` and `production_status` never advance past `NOT_PREPARED` / `NOT_RELEASED` in v1.** No proof-approval automation, no production release, anywhere in the code paths.
4. **Python owns only image inspection/repair; Go owns workflow, validation, and state.** Don't let business logic (state transitions, eligibility gating on order fields, retries) creep into `services/image-python` — it should be a pure function of image bytes + declared trim/intent in, structured findings/repair result out.
5. **Every mutation needs an idempotency key + expected `case_version` check** to prevent duplicate repairs or state corruption on retry. `requestRepair`, `answerClarification`, and any agent-triggered mutation must validate both before writing.
6. **Bounded agent loop:** cap at 5 tool calls + 2 transient retries per execution segment; persist the budget across replies so a resumed case doesn't reset it. One schema-repair attempt precedes deterministic fallback.
7. **No claim of company savings, private integration, or Sticker Mule pipeline access** anywhere in code, README, comments, or docs. This is an independent prototype based on public requirements only.
8. **`artwork_status` transitions:** BLOCKED → AWAITING_CLARIFICATION → BLOCKED (after reply) → RESOLVED (after all supported blockers verified clear); unsafe/unsupported cases go to NEEDS_REVIEW instead. New artwork or dimension changes reopen the case (back to BLOCKED).
9. **Result vocabulary per check is exactly:** PASS / WARNING / NEEDS_INPUT / NEEDS_REVIEW. `REPAIRED` is a repair-only status, set post-verification. Don't invent alternate strings — evals and the UI key off these exact values.
10. **CMYK inputs are inspect-only in v1** — no repair path for them. Only RGB/PNG-style uniform-background extension is implemented.
11. **Repair eligibility is a conservative demo rule, not a manufacturing-suitability proof.** Reject gradients, transparency, textured edges, and foreground objects touching the boundary — don't loosen this to get more fixtures to pass.
12. **Bleed check requires full-bleed intent AND a confirmed trim rectangle.** If either is missing, return NEEDS_INPUT — never infer trim placement by guessing from edge pixels.
13. **Out of scope, don't add:** Stores/Notify/Reply/Ship integration, SVG/PDF parsing, generative upscaling, automatic color conversion, print certification, payments, production release, factory integration, multi-agent fleet, arbitrary cut contours.
14. **Provider adapter is single and replaceable** (Claude first, through Day 4). A Grok comparison is optional Day-5 scope only, gated on core evaluation gates already passing — never let it displace reliability work.
15. **Untrusted input handling:** treat all customer text/files as untrusted — enforce file signature checks, 10MB upload cap, 25-megapixel decoded limit, sandboxed decoding, and timeouts in `services/image-python`'s entry points.

## Working rules

- Commit after every meaningful unit of work (passing test, working endpoint, migration, fixed bug, rule implemented). Messages explain *why*, not *what*.
- Update this file whenever the directory structure or a load-bearing constraint changes — it's living documentation.
