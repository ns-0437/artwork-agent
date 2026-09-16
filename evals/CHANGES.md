# Fixture manifest change log

This project's eval fixtures were built, then driven through the real stack
multiple times while the harness itself was still being debugged. Some
fixture expectations were corrected along the way. Per the brief's own
distinction between dev (expected to be tuned against) and held-out
(expected to generalize, untouched), this log exists so a reader can tell
which held-out results in any frozen report are genuinely blind evidence
and which are not - conflating the two would misrepresent what the
held-out numbers actually prove.

## Two different kinds of correction

**Design corrections, made by reading code, before ever running the
fixture through the harness.** These fix a wrong assumption baked into the
fixture's *design* - they say nothing about model behavior, because no
model (or even the deterministic rules) had been observed running against
them yet.

- `clean-a` / `clean-full-bleed-b`: originally designed to represent "a
  clean, full-bleed upload with bleed already correctly included,"
  expecting `RESOLVED` on first inspection with no repair. Tracing
  `services/image-python/app/checks/trim.py:resolve_trim` (before running
  either fixture) showed this scenario is impossible in v1: a fresh,
  non-repaired full-bleed upload can only resolve its trim rectangle when
  `artwork_is_trim_only=true`, which by definition makes trim = the whole
  image, which by definition makes available bleed margin zero. There is
  no code path where a fresh upload's trim can be confirmed AND have a
  nonzero margin. Corrected to represent "customer confirms bleed is
  already included, but the system can't locate the trim boundary" -
  expecting `NEEDS_REVIEW` (see CLAUDE.md point 44's related fix).
- `exact-bleed-boundary-a` / `-b`: designed to test the bleed check exactly
  at its 0.0625in threshold via a fresh upload with explicit trim
  dimensions. The live GraphQL API has no way to pass explicit trim
  dimensions on a fresh (non-repair) inspection (`trim_width_px`/
  `trim_height_px` on `/inspect` are only ever populated by Go from a
  repair's own known output - see CLAUDE.md point 33) - this scenario is
  only reachable post-repair, and the boundary math is already covered
  directly by `services/image-python/tests/test_bleed.py`'s
  `test_confirmed_trim_with_sufficient_margin_passes`. Removed from the
  live E2E manifest entirely rather than asserting an outcome the API
  cannot produce.

## Behavior-observed correction (the kind the held-out boundary is about)

- `mixed-issues-a` (dev) and `mixed-issues-b` (held-out): originally
  asserted `expect_repair: True, expect_repair_status: "REJECTED"` - i.e.
  that the agent would always attempt (and correctly get rejected on) a
  repair for this low-resolution-and-missing-bleed fixture. **A live run
  showed the model instead choosing to escalate directly** (no repair
  attempted at all), which is equally valid given the system prompt's "when
  in doubt, escalate" guidance and converges on the same correct final
  state. The assertion was relaxed (`repair_allowed: True`, no fixed
  `expect_repair`) *after observing this actual model behavior* - on both
  the dev fixture directly and, in the same batched run, the held-out one.

  **This means `mixed-issues-b`'s corrected expectation is not blind
  held-out evidence** - it was shaped by watching what the model actually
  did in a run that included it. Any report drawing "the agent generalizes
  correctly" conclusions from the held-out set must either exclude
  `mixed-issues-b` or footnote it as non-blind. It remains valid as a
  regression fixture (it still catches a real defect - repair attempted
  and *not* rejected, or a wrong final state) - it just isn't evidence of
  held-out generalization for the specific accepted-behavior question it
  was adjusted around.

## The manifest is frozen as of this log

`evals/fixtures/manifest.json` as it stands when this file was added is
the frozen suite. No further expectation edits should be made in response
to observed runs without adding a new dated entry above naming exactly
what changed and why - "we noticed a mismatch and fixed the fixture" is a
legitimate thing to do during development, but it must be visible, not
silently folded into what a report later calls "held-out results."

## Fresh held-out fixtures reserved for the frozen report

`evals/fixtures/held-out/fresh-*` (manifest entries with
`"reserved_for_frozen_report": true`) were added after the correction
above and had not been run through any mode of the harness until the run
logged below - not dev-tuned, not behavior-corrected, not previously
observed in any form up to that point.

## Reserved fixtures run (the one and only genuinely blind pass)

Run once, `--mode agent --include-reserved --reserved-only`, AFTER the
scripted-comparison fairness fix (escalation fallback + separate passed/
resolved metrics) was frozen - not before, and not re-run since. Result:
**4/4 evaluation cases passed, 2/4 orders resolved** (`fresh-clean-c`,
`fresh-missing-bleed-c` resolved via repair; `fresh-textured-edge-c`,
`fresh-lowres-c` correctly escalated to `NEEDS_REVIEW`), 0 falsely-resolved,
0 timed out - see `evals/results/eval_results_agent_reserved.json`.

**This is a small fresh check (n=4), not broad proof of reliability** - it
confirms the same pattern the 34-fixture set showed held on fixtures the
system had never seen in any form, nothing more. Report it as its own
line, separate from the 34-fixture numbers, in any summary - folding n=4
into "38 fixtures, X passed" would misrepresent four data points as
comparable in weight to thirty.

These fixtures are no longer reserved as of this run - they've been
observed now, and any future re-run of them is a regression check, not
blind evidence, exactly like the rest of the held-out split.
