---
name: bug-fix-protocol
description: RED-first protocol for any bug fix task (type Fix). Owns the failing-test-first requirement, automatic rejection rule, and Regression Test Author dispatch criteria.
---

# Bug Fix Protocol

Load this skill when the task has `type: Fix`. The protocol enforces that a bug fix is **proven to fix something** — not "this code is now different and we hope it's better."

---

## Phase 0: Evidence-State Triage (before any dispatch)

Route the ticket by what is actually known, not by what the reporter guessed:

| Evidence state | Route |
|----------------|-------|
| User/externally-reported symptom, root cause **unconfirmed** | Dispatch **Bug Reproducer** (`roles/bug-reproducer.md`) FIRST. Its RED harness spec + updated SMG ticket becomes the Coder's contract. Do not send an unreproduced user report straight to a Coder. |
| Root cause **confirmed**, user-reported, Medium/High risk | Regression Test Author Phase A (section below), then Coder. |
| Coder-discovered during other work, or trivially unit-reproducible (stack trace in hand, pure function) | Straight to Coder under the RED → GREEN protocol below. |

**When the Bug Reproducer ran first**, the Coder's Task Assignment gains one Definition of Done line:

> - [ ] The reproducer spec at `<path>` goes GREEN with your fix, **unmodified**. If the spec is wrong, stop and report — do not edit it to pass.

…and the Tasker re-runs that spec against the fix branch during Phase 3.2 verify-claims, via the repro runner (which enforces stack-version alignment mechanically and refuses to produce a verdict against the wrong build):

```bash
~/Project/claude-workflow/cmd/repro/repro run -worktree "$FIX_WORKTREE" \
  -spec <reproducer-spec-path> -clients <as the spec requires> -expect green
```

Exit 0 = the fix satisfies the user-reported contract on the right build; exit 1 = still red (iterate); exit 2 = flaky (investigate before concluding anything); exit 3 = the stack isn't serving the fix branch — fix the environment, not the code. See "Stack Version Alignment (Tilt)" in `bug-reproducer.md` for the manual mechanics; the same rules bind anyone running live-stack specs by hand.

---

## The Rule

**A bug fix without a prior failing test is REJECTED.**

A Completion Report that does not include actual RED test output before the fix is automatically rejected. Return it to Coder with the instruction to start over from step 1.

---

## RED → GREEN Protocol

The Tasker adds this section verbatim to the Task Assignment:

```markdown
### Bug Fix Protocol (type: Fix)

1. **Write a failing test first.** Name it after the ticket (e.g., `TestSMG1653_BetSettlementResolvesByHitBoundID`). The test docstring documents the root cause in one or two sentences.

2. **Confirm RED.** Paste the actual `go test` output (or equivalent in your stack) showing the test failing for the right reason — not a compile error, not a wrong assertion. The failure must prove the bug exists. Example of the right kind of failure:
   ```
   --- FAIL: TestSMG1653_BetSettlementResolvesByHitBoundID (0.00s)
       settlement_test.go:42:
           expected: bet resolved to user 12345
           actual:   bet resolved to user 67890
           reason:   resolver matched on hit_id when it should have matched on hit_bound_id
   ```

3. **Write the minimal fix.** Change only what is necessary to make the test pass. Do NOT improve surrounding code, refactor, or add defensive checks beyond what the bug requires.

4. **Confirm GREEN.** Paste the actual `go test -race ./...` output showing all tests pass — both the new RED-then-GREEN test and the existing suite.

A Completion Report missing the RED output, or missing the GREEN-after-fix output, is automatically rejected.
```

---

## What RED Must Look Like

Not RED:
- A compile error — proves the code doesn't build, not that the bug exists
- A `panic` with no assertion — proves something blew up, not what was wrong
- A `require.NoError` that fails on an unrelated dependency — the test fails for the wrong reason

RED:
- The specific assertion that encodes the user-reported (or Coder-observed) buggy behavior, evaluated against the current (pre-fix) code, and failing with a message that points at the bug.

The test docstring states the root cause. If the Coder cannot articulate the root cause in two sentences, they haven't understood the bug yet — block, don't fix.

---

## Regression Test Author Dispatch

For bug fixes that:
- Originated from a **user report** (not Coder-discovered during implementation), AND
- Are classified **Medium or High** risk, AND
- Have a **confirmed root cause** (not speculative)

…also dispatch the Regression Test Author **before** the Coder. The Regression Test Author works in two phases on the same agent:

**Phase A (before fix exists)** — translates the user-reported contract into a failing test, opens a draft PR. Black-box only (e2e, wire-effect, integration). Refuses to write unit tests at the layer the Coder's TDD covers.

**Phase B (after Coder's fix PR is up)** — cherry-picks the test onto the fix branch, runs it, and reports whether the fix satisfies the user-reported contract.

Dispatch:

```
Read the file `.claude/workflow/roles/regression-test-author.md` for your complete role instructions.

Phase: A (write failing test) | B (verify fix satisfies contract)

User report / SMG ticket: [paste]
Confirmed root cause: [from triage]
Risk: Medium | High
Fix branch (Phase B only): [branch-name]
```

### Skip the Regression Test Author for:

- **Trivial bugs (Low risk)** — Coder's TDD is sufficient.
- **Critical risk** — Verification Agent + multi-reviewer panel already produces independent ground truth at sufficient depth.
- **Coder-discovered bugs** — the RED-first protocol above handles them.
- **Unconfirmed root cause** — return to human first; the Regression Test Author refuses to write speculative tests against fuzzy causes.

If the Coder's PR ships thorough internal tests covering the same observable contract, the Regression Test Author's draft PR closes rather than merges. That's a successful outcome, not a failure — the duplicate signal isn't worth the maintenance overhead.

---

## Common Coder Failures (auto-reject)

| Failure | Required action |
|---------|-----------------|
| Completion Report has no RED output | Reject. Send back with "start from step 1: write the failing test." |
| RED output is a compile error | Reject. RED must be a test assertion failure. |
| RED output is a panic with no assertion | Reject. Test must encode the buggy behavior. |
| Fix touches more than the bug — "while I was here" refactor | Reject. Minimal diff only. |
| GREEN-after-fix run is missing | Reject. Both RED and GREEN runs must be in the report. |
| Test docstring doesn't name the root cause | Reject. The Coder must understand the bug before writing the fix. |

---

## Integration with Other Skills

- **Migration fixes** — if the bug is in a migration, also load `migration-checklist.md`. The RED test asserts the schema before/after the fix.
- **Iteration cycles** — bug-fix iterations follow the standard `iteration-protocol.md` rules. Minimal diff per cycle; no refactoring; CRITICAL/HIGH findings only.
- **PR raising** — PR body's `## Why` section must reference the root cause from the test docstring. See `pr-raise.md`.
