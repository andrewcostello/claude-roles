---
name: iteration-protocol
description: Targeted re-review after Coder fixes CRITICAL/HIGH findings. Owns the iteration cap, full domain test gate, remediation ordering, and the iteration-request format.
---

# Iteration Protocol

Load this skill on `ITERATE` verdict from the review panel. The skill owns the loop mechanics: how to format the fix request, the gate before re-review, the targeted re-review scope, and the iteration cap.

---

## The Loop

```
ITERATE verdict
  ↓
Send CRITICAL/HIGH-only fix request to Coder (format below)
  ↓
Coder fixes — minimal diff, no refactoring
  ↓
Coder runs full domain test suite — must be GREEN
  ↓
Coder returns updated Completion Report
  ↓
Tasker verifies full domain suite output independently
  ↓
Re-review (two-tier — see below):
  Round 2:  FULL re-audit (cmd/reviewer, same as round 1)
  Round 3+: targeted verification (cmd/recheck)
  ↓
APPROVE → pr-raise.md
ITERATE → loop while converging (ceiling 4)
ESCALATE / REJECT → Blocked/Escalated summary
```

---

## Iteration Budget & Convergence Rules

The old flat cap of 2 conflated three concerns; they are now separate (decision 2026-07-16, informed by the PR 1285 cycle where a round-2 full re-audit caught a genuine round-1 miss):

**Escalate IMMEDIATELY — any round, don't wait for the counter:**
- Any prior CRITICAL/HIGH finding reported **STILL OPEN or REGRESSED** after its dedicated fix round. A finding that survives a clean, focused fix attempt is a design or spec problem — more patching won't help. (This was the real signal the old cap approximated.)
- Any critical dimension (Correctness/Security/Compliance/Exploitability) **FAILing two consecutive rounds**.

**Otherwise iterate while converging:**
- All prior CRITICAL/HIGH findings RESOLVED each round, **and**
- The count of NEW CRITICAL/HIGH findings is **strictly decreasing** round-over-round. New findings with prior ones resolved is the panel working, not the process failing — a stricter panel finds round-1 misses on a second look.

**Hard ceiling: 4 rounds** (`$MAX_ITERATIONS` overrides), purely as a runaway backstop — the escalation triggers above should fire long before it.

On escalation/ceiling: write `Status: Blocked`, include the full review history and per-round finding lineage in the Escalation reason. Recommendation menu unchanged: pair session, spec clarification, or scope reduction. The Security Linter's separate 2-cycle cap is unchanged — persistent vulnerabilities need design discussion at 2, full stop.

---

## Fix Request Format

Send this to the Coder — never include MEDIUM/LOW findings in the actionable list:

```markdown
## Iteration Required: [Task ID] — Round N (ceiling 4; convergence rules apply)

**Tier minimum:** every quality dimension ≥ [4/5 for Critical/High, 3/5 for Medium] with no CRITICAL/HIGH findings open
**Current weakest dimension:** [name] at [score]/5 from [reviewer(s)]

### CRITICAL Findings (Must Fix)
- [ ] [File:Line] [Finding] — flagged by: [reviewers]

### HIGH Findings (Must Fix)
- [ ] [File:Line] [Finding] — flagged by: [reviewers]

### MEDIUM/LOW Findings (Logged — DO NOT Fix Now)
These are tracked for future work. Fixing them in this iteration risks regressions.
- [ ] [File:Line] [Finding] — [severity]

### Instructions
1. Fix ONLY the CRITICAL and HIGH findings above
2. Each fix must include a regression test proving existing behavior still works
3. Do NOT refactor surrounding code while fixing — minimal diff only
4. Run the full domain test suite and paste the output in your Completion Report:
   - Wallet:      `go test -race ./apps/finance-domain/wallet/...`
   - Engine/Game: `go test -race ./apps/game-domain/...`
   - Platform:    `go test -race ./apps/platform-domain/core/...`
5. Submit updated Completion Report listing ONLY the files you changed
```

A finding gets **one dedicated fix round**. If the re-review reports it STILL OPEN or REGRESSED, the task escalates immediately — there is no second attempt at the same finding. New findings discovered by the re-review get their own round while the convergence rules hold (see Iteration Budget & Convergence Rules). Treat each finding as your one shot at it.

---

## Remediation Ordering

When multiple findings need fixing in one iteration:

1. Fix all CRITICAL findings first (small, targeted changes).
2. Fix HIGH findings next.
3. Commit and verify with a quick local re-run after each group.
4. **NEVER mix bug fixes with refactoring** in the same iteration.
5. Each fix has a minimal diff — do not "improve" surrounding code.

If a fix in step 1 makes a step-2 fix harder, stop and report back — do not improvise. The Tasker may decide to re-scope the iteration.

---

## Full Domain Test Suite Gate (before re-review)

A targeted fix to one file can regress a sibling file whose tests are not run by changed-files-only test commands. Catching that regression with the test suite is cheap; catching it post-merge is expensive.

**Before dispatching the targeted re-review:**

- The Coder's iteration Completion Report includes the full domain-scoped test command output (from step 4 of the Fix Request).
- The Tasker independently verifies that output — re-run from the worktree if the output looks suspicious.
- If any test fails: the iteration is rejected and returned to the Coder. Do NOT dispatch the targeted re-review on broken code.

This separates "are tests still passing across the domain" (Tasker's deterministic job) from "did the reviewer audit the universe" (which the workflow correctly avoids to prevent regression spirals).

---

## Two-Tier Re-Review

After the domain test gate is GREEN:

**Round 2 — full re-audit, deliberately.** Re-run `cmd/reviewer` on the full PR diff exactly like round 1 (`git diff BASE...HEAD | reviewer -cwd WT -risk TIER [-component ...] -findings-out /tmp/findings-TASK-r2.json`). One full second look catches what round 1 missed while attention was on the flagged items — evidence: PR 1285's round-2 re-audit found a real money-display bug (PRACTICE fallback) in a file round 1 had reviewed and passed. The Tasker adjudicates round-2 findings before dispatching fixes: dismissing a finding requires cited code evidence (see the 1285 funds.go dismissal for the standard).

**Rounds 3+ — targeted verification via `cmd/recheck`.** No more discovery passes; the question narrows to "did the fixes land, and did they break anything in the files they touched".

**Do not drive this loop by hand — `cmd/iterate` owns it:**

```bash
~/Project/claude-workflow/cmd/iterate/iterate run -run-state "$RUN_STATE"
# exit 0 APPROVE · 1 ITERATE · 2 ESCALATE · 3 INVALID_INPUT
```

It reads `rounds[]` from the run state and decides everything this section otherwise asks you to decide: which tool runs (full re-audit through round 2, `recheck` from round 3), the severity floor (from `classification.recheck_min_severity`), `-max-new`, and the convergence verdict — then appends the round to `rounds[]`. `iterate next` prints the decision and the exact command without running anything.

The equivalent manual invocation, if `iterate` is unavailable:

```bash
~/Project/claude-workflow/cmd/recheck/recheck \
  -worktree "$WT" -findings /tmp/findings-TASK-rN.json \
  -risk TIER [-min-severity high|medium] \
  -max-new <prior round's new-finding count, VERBATIM> \
  -out /tmp/round-TASK-rN.json
```

> **Correction 2026-07-31 — `-max-new` was documented one too low.** This skill previously said "prior round's new-finding count **minus 1**". `recheck` returns ITERATE only while `new < max-new`, so passing `P` already requires strictly fewer than `P`; passing `P−1` demands a drop of *two* and would escalate a genuinely converging 3 → 2 round as "not decreasing". Pass `P` verbatim. `cmd/iterate` computes it, and its `TestDecide_MaxNewComesFromPriorRound` locks the semantics in.

**Cost:** the iteration rounds are where a multi-round PR bleeds tokens (PR1380 exhausted an account over 5 passes). The saving is structural — `recheck` is a **single targeted verifier** over changed files only, not the full multi-seat panel, so rounds 3+ already cost a fraction of a full re-audit. It runs on **`claude-opus-5` by default (`-model`), and that is the default we keep** — verification of money findings stays on the strong model. A budget `-model claude-haiku-4-5-20251001` is available as an **experiment only** (low confidence — do not adopt for real iterations until an A/B shows haiku catches the same STILL_OPEN/REGRESSED cases opus does).

`recheck` verifies each prior finding **at or above the severity floor** as RESOLVED / STILL_OPEN / REGRESSED against the iteration diff, hunts new at-or-above-floor findings **only in files changed since the prior review**, and computes the verdict mechanically per the convergence rules: exit 0 APPROVE, 1 ITERATE, 2 ESCALATE (any STILL_OPEN/REGRESSED, or new findings ≥ `-max-new`).

**Severity floor (`-min-severity`, default `high`):**
- **`high`** — the always-on bar: converge to zero CRITICAL/HIGH; MEDIUMs are logged, not iterated. Use for all normal tasks.
- **`medium`** — critical-system bar: also verify prior MEDIUMs and hunt new MEDIUMs, converging to **zero MEDIUM-or-higher**. Use when the task is a critical system (wallet, bet-settlement, auth, RGL — the `-component` set) and the reviewer's `-findings-out` was written at a bar that includes mediums. At this floor, MEDIUMs are in the actionable Fix Request (the "do not iterate MEDIUM/LOW" default is lifted — see What NOT to Do).

The old scoped-re-review prompt below remains the fallback for agent-driven (non-CLI) runs:

```markdown
## Targeted Re-Review: [Task ID] — Round N/2

### Scope: Changed files only
[list of files changed by Coder in this iteration]

### Previous Findings Being Verified
[list of CRITICAL/HIGH findings with file:line references from the prior review]

### Domain Test Suite Status
✅ Full domain suite verified GREEN by Tasker before dispatch — you are reviewing code quality, not test sufficiency.

### Instructions
1. Verify each previous CRITICAL/HIGH finding is resolved
2. Check for regressions in changed files ONLY
3. Do NOT audit unchanged files for new issues
4. For each prior finding, report: RESOLVED / STILL OPEN / REGRESSED
5. New findings in changed files: categorize as CRITICAL/HIGH/MEDIUM/LOW
6. Only new CRITICAL/HIGH findings trigger another iteration
```

Continue until no open CRITICAL/HIGH findings remain, or the iteration cap is reached.

For Critical/High: dispatch the full panel (Claude + Codex + Gemini + Grok, or the configured `$REVIEWER_COUNT`) in parallel for the targeted re-review, same as the initial review. The fallback / mid-flight retry rules in `critical-review-dispatch.md` apply.

For Medium: a single reviewer for the targeted re-review is sufficient — they're verifying their own prior findings.

---

## Verdict at Each Cycle

"At-or-above-floor" below means CRITICAL/HIGH at the default `high` floor, or CRITICAL/HIGH/MEDIUM at the `medium` floor for critical systems.

- All prior at-or-above-floor findings RESOLVED + no new at-or-above-floor → **APPROVE**
- Any prior finding STILL OPEN or REGRESSED → **ESCALATE immediately** (write Blocked/Escalated — do not spend remaining rounds; the fix attempt failing is the design-problem signal)
- All prior RESOLVED + new at-or-above-floor findings, count strictly below the previous round's → **ITERATE** (new findings go in the next Fix Request)
- New at-or-above-floor count NOT decreasing → **ESCALATE** (the change is defect-dense; patching isn't converging)
- Ceiling reached with anything open → **Blocked**, reason "iteration ceiling", full per-round finding lineage in the summary. The default ceiling is 4 (runaway backstop). **Critical systems converging to the `medium` floor legitimately run longer** — raise or remove the cap with `$MAX_ITERATIONS`; the STILL_OPEN/REGRESSED escalation above is the real stop, not the round count, so a run that keeps cleanly RESOLVING-and-finding-lower-severity is converging, not looping.

---

## What NOT to Do

- Do not iterate on MEDIUM/LOW **at the default `high` floor**. They are logged in the summary's `Deferred findings` section. Fixing MEDIUM/LOW in a fix iteration risks regression because the Coder mixes scope. **Exception — critical systems at the `medium` floor:** when the task demands zero MEDIUM-or-higher (`recheck -min-severity medium`), MEDIUMs are in scope and iterated like HIGHs; LOW is still never iterated. Keep each fix round minimal-diff even so.
- Do not run full re-audits past round 2. Round 2's full re-audit is deliberate (catches round-1 misses — see Two-Tier Re-Review); from round 3 the discovery budget is spent and re-reading unchanged files creates the regression spiral this rule always guarded against. Rounds 3+ are `cmd/recheck` targeted verification only.
- Do not skip the full domain test suite gate. The cost is one test run; the cost of skipping it is finding regressions post-merge.

---

## Common Failure Modes

| Failure | Cause | Fix |
|---------|-------|-----|
| Re-review finds a new MEDIUM in changed files | Coder's fix touched surrounding code | Reject the iteration; require minimal diff |
| Domain tests fail after the fix | Fix regressed a sibling file | Iteration is rejected back to Coder; do not dispatch re-review on red |
| Reviewer audits unchanged files | Re-review prompt was unclear | Re-state the scope; the prompt template in this skill explicitly scopes the audit |
| Iteration count drifts above the cap silently | Tasker lost track of cycles | Track explicitly in the summary file's `Iterations` field; ceiling is 4 (`$MAX_ITERATIONS` overrides) per Iteration Budget above — the STILL_OPEN/REGRESSED escalation is the real stop |
| Coder bundles a refactor into an iteration | Treated the iteration as a general code-quality pass | Reject; iterations fix bugs, not style |
