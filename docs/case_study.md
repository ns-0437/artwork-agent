# Case study: Artwork Exception Resolution Agent

An independent six-day portfolio build (build brief: `docs/build-brief.pdf`) implementing the workflow a print shop needs to resolve an artwork blocker on a sticker order: inspect the file, clarify customer intent when needed, perform one eligible repair, verify it, prepare a proof, and persist the case so work survives a crash or a delayed reply. This is an independent prototype based on public requirements. It has no access to any real company's production pipeline, customer files, printer profiles, or operational metrics, and makes no claim of measured savings or private integration.

**What this document is about:** the engineering - a durable, verifiable execution path built on deterministic rules, with one bounded, replaceable decision-making component sitting on top of it. The headline evidence is the durability, the verification discipline, and the honesty of the measurement process itself - not a claim that adding an LLM made the system better at resolving cases than a well-written script would. Section "What the evaluation actually shows" is explicit about this: for the fixture set tested, a deterministic script given the same tools resolved the identical set of cases the agent did.

## The workflow, as built

```
blocked order → inspect → clarify if needed → repair or escalate → verify → prepare proof
```

Three deterministic checks (`services/image-python/app/checks/`) compute effective PPI at print size, color mode/profile, and available bleed - each is a pure function of decoded image bytes plus declared physical size/intent/trim-confirmation, unit-tested, versioned (`rule_version`), and never recomputed or overridden by anything downstream, including the agent. One repair - uniform-background bleed extension - runs when eligibility (opaque, confirmed trim, uniform edge color) holds, verified by exact decoded-pixel equality against the original content region, not a perceptual score. A bounded decision-making step (an LLM by default, but any `agent.Provider` implementation) chooses among ask-one-clarification / request-repair / escalate, with every choice validated by Go against the actual findings before being acted on - the provider is a suggestion, never trusted at face value.

## Engineering evidence: what's actually been verified, and how

**Durable execution, not just a design that should be safe.** Every step of the loop - inspect, the agent's decision, a clarification reply, a repair, proof preparation - is its own job-queue entry (Postgres, lease-based claiming, `FOR UPDATE SKIP LOCKED`), enqueued in the SAME transaction as the state change that produces it. A crash between any two steps leaves a queued job to resume, not a stuck case. This has been tested against hand-reproduced crash STATES (an expired lease with no completed result; a storage write that succeeded with no corresponding database commit) - not an actual killed process. Both reproductions confirmed: zero side effects from the "crashed" attempt, and a fresh worker instance reclaiming and completing the work exactly once, with no duplicated assets, no double-counted budget, and the case reaching its correct final state. An actual process-kill test (verifying behavior under a real SIGKILL mid-syscall) has not been done - see `CLAUDE.md` points 21 and 48.

**Verified repair, checked from outside the system's own claim.** The repair endpoint verifies pixel-identical preservation of the original content region internally (`services/image-python/app/repair/verify.py`). The Day 5 eval harness re-checks this independently: it re-reads the actual bytes of the original and repaired assets from the storage backend (not database metadata) and re-compares the trim region pixel-for-pixel using a separate code path. Across every repaired case tested, this held.

**A working clarification loop, correctly scoped.** The system supports exactly one clarification (is the artwork trim-only), asked with a fixed, deliberately-worded question rather than a model's own free-form phrasing (a real polarity-inversion bug was found and fixed here during development - see `CLAUDE.md` point 43), answered through explicit Yes/No UI controls, and bound to the specific artwork version it was asked about (a replacement upload invalidates any still-pending question, closing a real gap where an old question could otherwise be answered against new artwork - point 46).

**Proof preparation, not just a status flag.** `proof_status` only ever reaches `AWAITING_CUSTOMER_APPROVAL` after a real proof image is rendered and stored; the eval harness independently confirms every such proof asset actually opens as a valid image.

**Honest measurement, including the harness's own mistakes.** The evaluation harness itself went through multiple rounds of review and correction before its results were trustworthy: an early version graded a timed-out run as if it had settled, compared upload-time database metadata instead of re-reading actual stored bytes, and ran a "baseline" comparison against a worker that still had the real agent quietly active in the background. All three were found and fixed (see `evals/CHANGES.md` and the harness-integrity commit) before any result was reported as final. This document and the numbers below reflect the corrected harness.

**The web UI, driven live, not just the API.** Beyond the harness's GraphQL-level checks, the actual browser UI (`web/src/pages/CaseView.tsx`) was driven end to end through a real order: upload → the agent's fixed clarification question appearing → answering it via the Yes/No buttons → automatic resumption → auto-repair → `RESOLVED`/`AWAITING_CUSTOMER_APPROVAL`, and separately a low-resolution border-intent case reaching `NEEDS_REVIEW` with zero clarifications or repair attempts. See `docs/demo_script.md` for the exact steps, rehearsed live immediately before that script was written.

## What the evaluation actually shows

34 fixtures (16 dev, 18 held-out; four more reserved and not yet run - see below) were driven through the real stack end to end, three ways:

- **agent** - the real stack, an LLM (Groq; see "Provider note" below) making the bounded decision.
- **baseline** - a genuinely agent-disabled worker (no provider key configured at all, so the decision step never runs) - deterministic rules only, nothing else.
- **scripted** - the same agent-disabled worker, but the harness itself supplies the same tool calls (confirm trim, request repair, and - after an initial version of this comparison was flagged as unfair for omitting it - escalate with a rule-based reason) a hand-written, non-AI script would use.

**Two different numbers, reported separately on purpose:**

| | agent (Groq) | baseline (rules only) | scripted (rules + deterministic script incl. escalation) |
|---|---|---|---|
| evaluation cases passed (correct final state) | 34/34 | n/a (descriptive only) | n/a (descriptive only) |
| orders RESOLVED | 18/34 | 12/34 | 18/34 |
| orders NEEDS_REVIEW (escalated) | 16/34 | 14/34 | 16/34 |
| orders BLOCKED (nothing ever proposed a next step) | 0/34 | 8/34 | 0/34 |
| avg latency | 18.3s | 14.8s | 17.9s |
| total provider tokens (cost proxy) | 19,650 | 0 | 0 |

"Passed" grades whether a case ended in the state it *should* have - a correctly-escalated `NEEDS_REVIEW` passes without the order ever resolving. Reporting only "34/34 passed" without also reporting "18/34 resolved" would make correct escalations look indistinguishable from unresolved failures; conflating the two overstates what either number means on its own.

**The honest finding, once the comparison was made fair:** once the scripted workflow was given the same escalation capability the agent has (`escalateCase`, a new mutation, called with a fixed rule: "if nothing else applies, escalate with a reason"), its outcome matched the agent's **exactly, fixture for fixture, on all 34 cases** - same 18 resolved, same 16 escalated, same 0 left stuck, at effectively the same latency and zero token cost. An earlier pass reported the agent "escalating 6 cases the script left silently `BLOCKED`" - that was an artifact of comparing against a script that had not been given an escalation rule, not evidence that judgment beyond deterministic rules was required. A recorded `NEEDS_REVIEW` also demonstrates only that the SYSTEM logged an escalation with a reason - it says nothing about whether a person actually received or acted on it; that hand-off is out of scope for this build.

**What this is not evidence of:** a demonstration that AI improves resolution over a well-designed deterministic workflow. For this fixture set, it doesn't. What the LLM component does provide is a single, swappable decision point that can be extended to genuinely ambiguous judgment calls a fixed rule set can't anticipate - the current fixture set doesn't happen to exercise that difference, and this project does not claim otherwise.

**Boundaries on this pass:**
- 34 fixtures generated by this same project, not an independent test set.
- One fixture's expectation (`mixed-issues-a`/`-b`) was relaxed after observing actual model behavior in an early run - disclosed in `evals/CHANGES.md`, and excluded from any "blind held-out" claim.
- No cost-per-case dollar figure or Grok/Claude comparison is included - out of scope for this pass per the brief's own "defer this comparison before compromising reliability."

See "The reserved fixtures: a small fresh check, not broad proof" below for the four held-out fixtures withheld from the numbers above.

## Provider note

The active decision-making provider is **Groq** (a fast inference host for open-weight models), **not Claude and not xAI's Grok** - the Anthropic account available for this build had no usable credits, so Groq was substituted as a documented, honest stand-in. Groq and Grok are unrelated companies with similar-sounding names; conflating them anywhere in this project (code, commits, or this document) would misrepresent what actually ran. The provider is a single, swappable interface (`internal/agent.Provider`) - trying a different model means implementing that interface again, nothing else in the codebase changes.

## The reserved fixtures: a small fresh check, not broad proof

Four held-out fixtures (`fresh-clean-c`, `fresh-missing-bleed-c`, `fresh-textured-edge-c`, `fresh-lowres-c`) were deliberately withheld from every run above until the scripted-comparison fix was frozen. Run once, after that freeze, in agent mode: **4/4 evaluation cases passed, 2/4 orders resolved** (the clean and repair-eligible cases resolved; the texture-edge and low-resolution cases correctly escalated), 0 falsely resolved, 0 timed out. This is consistent with the pattern the 34-fixture set showed, on fixtures the system had genuinely never seen in any form - but **it is a small fresh check (n=4), not broad proof of reliability**, and is reported here as its own line rather than folded into the 34-fixture numbers above, which would misrepresent four data points as carrying the same weight as thirty. See `evals/CHANGES.md` for the exact run record; these fixtures are no longer "reserved" as of this pass.

## Cloud deployment

Cloud Run service configs for the API, the (IAM-gated) image service, and the worker (`infra/gcp/`), plus a GCS storage backend (`internal/storage/gcs.go`), have been deployed and verified against a real GCP project - live at [ns-0437.github.io/artwork-agent](https://ns-0437.github.io/artwork-agent/), see the README's "Live demo" section for the deployed commit and what was verified. The worker's current code is a persistent poll loop, not a task that runs to completion, so it's deployed as a single-instance Cloud Run service rather than the brief's suggested Cloud Run Job shape; that refactor remains follow-up work, not done here.

## What's prepared but not executed

- **An actual process-kill test.** The crash-recovery evidence above reproduces the DB/storage state a crash leaves, by hand; it has not been validated against a real SIGKILL mid-syscall.

## Known, tracked gaps

- No authentication or ownership check on orders (`ownerId` is a free-text, unverified client field) - dev ports are bound to `127.0.0.1` specifically because of this.
- Upload tickets are replayable until they expire (10 minutes) rather than enforced single-use.

See `docs/architecture.md` for the day-by-day technical build log and `CLAUDE.md` for the full list of load-bearing constraints and the fixes each one addresses.
