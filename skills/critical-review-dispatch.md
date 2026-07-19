---
name: critical-review-dispatch
description: Pre-review gates + 4-reviewer parallel panel dispatch for Critical/High risk tasks. Owns Design Agent, Verification Agent, Security Linter, Static Analysis Gate, and reviewer panel mechanics.
---

# Critical / High Review Dispatch

The Tasker loads this skill for Critical and High risk tasks. It owns:

1. Design Agent dispatch and design selection (Phase 2.3)
2. Pre-review gates (Phase 3.0): Verification Agent, Security Linter, Static Analysis Gate
3. 4-reviewer parallel panel dispatch (Phase 3.3) including plugin/CLI invocation and mid-flight failure handling

---

## Phase 2.3: Design Agent

Mandatory for Critical/High; optional for Medium; skip for Low. If `$SKIP_DESIGN` is set, skip entirely (Phase 1 analysis still runs).

### Dispatch

Use the Task tool with `subagent_type: general-purpose`:

```
Read the file `.claude/workflow/roles/design-agent.md` for your complete role instructions.
Risk level: [Critical/High]
Task Assignment: [paste full Task Assignment]
Reference implementations to study: [paste file paths from Phase 1]
```

### Select a Design

The agent returns 2-3 competing designs with trade-offs. **Tasker selects one** based on fit with codebase patterns, simplicity and correctness, sub-agent findings (financial invariants, schema — Critical only), and judgment as technical lead. If you cannot choose, escalate with the options laid out — do NOT flip a coin. If all designs carry unacceptable risk, escalate immediately.

### Pass Design to Coder

Append as `### Approved Design Spec` in the Task Assignment with this priority clause:

> The approved design takes precedence over the original spec. On a **meaningful conflict** (different approach, data model, or behavior) the Coder does NOT pick a side — they stop and present the divergence: (1) what the spec says, (2) what the design says, (3) where they conflict, (4) recommendation. Tasker escalates to human. For **minor deviations** (naming, parameter order, implementation details), follow the design and document under `⚠️ Interface Deviations`.

---

## Phase 3.0.1: Verification Agent (Critical only)

Dispatch **before** any reviewer. Produces independent ground truth — the agent does NOT see the Coder's Completion Report.

```
Read the file `.claude/workflow/roles/verification-agent.md` for your complete role instructions.
Branch: [branch-name]
Files changed (from Completion Report): [list]
Risk tier: Critical
Project commands (from CLAUDE.md): Build / Test / Lint / Complexity / Static analysis (gosec + staticcheck + semgrep) / Mutation if financial / Benchmark if benched.
Run from a clean worktree. Stop on first failure. Report raw output.
```

**FAIL** → return to Coder with failing step + raw output. Do not dispatch any reviewer. Iteration counter advances.
**PASS** → proceed to 3.0.2.

---

## Phase 3.0.2: Security Linter (Critical only)

```
Read the file `.claude/workflow/roles/security-linter.md` for your complete role instructions.

Risk context: [what this code touches — SQL, auth, money, etc.]

Files to audit:
[list files from Completion Report]
```

Verdicts:
- **PASS** → proceed to Phase 3.3 reviewer panel
- **FAIL** (Critical/High findings) → remediation path below
- **FLAG** (Medium findings, no Critical/High) → present to human for accept/reject

### Security Linter FAIL — remediation path

Do NOT proceed to reviewer panel. Send a targeted security fix request to Coder:

````markdown
## Security Fix Required: [Task ID]
**Security Linter Verdict:** FAIL
**Linter Cycle:** N/2

### Vulnerabilities Found (Must Fix)
- [ ] [File:Line] [Vulnerability] — surface: [SQL Injection / PII Exposure / Integer Overflow / Auth Bypass]
  - **Attack vector:** [how it could be exploited]
  - **Suggested fix:** [from linter output]

### Instructions
1. Fix ONLY the vulnerabilities listed above
2. Each fix must NOT introduce new attack surface
3. Re-run tests, confirm no regressions
4. Submit updated Completion Report listing ONLY the files you changed
````

Re-run the Security Linter on changed files after the fix.

**Cap at 2 linter cycles.** If still FAIL after 2 attempts, escalate to human — persistent vulnerability needs a design-level discussion, not another code fix.

Linter cycles are separate from review iteration cycles. A task can use 2 linter cycles AND have its full `MAX_ITERATIONS` (default 2) review iterations available — they address different concerns (exploitability vs. correctness).

If `$SKIP_SECURITY_LINTER` is set, skip this phase. Verification Agent still runs for Critical.

---

## Phase 3.0.3: Static Analysis Gate (High only)

For High risk (Critical already covers this via Verification Agent), run static analyzers as a Tasker-side gate:

```bash
gosec ./...
go list ./... | grep -vE '/pb$|/sqlc$|/mock$|/migration$' | xargs staticcheck
semgrep --config [project semgrep rules path] --error
```

Zero findings allowed. Suppressions in changed files are noted for the reviewer to validate; a suppression without rationale comment is a finding.

**Any analyzer fires** → return to Coder. Iteration counter advances.

> **Why High runs this Tasker-side** rather than trusting Coder's Phase 4.5 output: Tasker's job is to verify, not trust. Re-running the cheap deterministic checks catches pasted-but-not-actually-run output.

---

## Phase 3.3: Multi-Agent Review Panel

The multi-agent panel is fully encapsulated in the `cmd/reviewer` CLI tool. The orchestrator automatically handles reviewer isolation, reasoning depth scaling based on risk tier, fallback and retries, and merges the findings into a consensus verdict.

### Dispatching the Panel

Run the `reviewer` binary inside the worktree, passing the diff via stdin:

```bash
cd ~/Project/claude-workflow
git diff main...HEAD | ./cmd/reviewer/main -cwd "$WORKTREE" -risk [critical|high|medium|low] \
  [-component wallet,bet-settlement,...]   # when the diff touches a floor component (table below)
```

The CLI defaults to the 5-seat `claude,claude-scouts,codex,grok,agy` panel (claude mono restored 2026-07-16 after the 10-minute timeout misconfiguration was fixed — monitor its unique-finding contribution across upcoming reviews before revisiting). It outputs the full merged review report, a deduplicated Critical & High findings section (near-duplicate findings are clustered with all sources listed; severity is the max across the cluster; nothing is dropped), and the final `Status` + `Final Verdict`.

### Consensus and Mid-Flight Failures

Each reviewer is retried once on failure. The CLI then applies the tier consensus floor to whatever completed:

| Tier | Floor (N-seat panel) | 5-seat example |
|------|----------------------|----------------|
| Critical | N/N — full panel required | 5/5 |
| High | max(2, ceil(3N/4)) | 4/5 |
| Medium | majority (ceil(N/2)) | 3/5 |
| Low | 1 | 1/5 |

At or above the floor with absences, the review proceeds and every absence is listed under **Reviewer Availability** plus a **Consensus** line in the report header. Below the floor → `REVIEW_UNAVAILABLE`: do not send the output to the Coder; re-run the CLI once (provider outages are usually transient), then escalate to human naming which reviewers failed (paths to their `.err` dumps are in the report log output).

---

## Component-specific dimension floors

Floors are enforced mechanically by the CLI. Pass `-component` with any preset the diff touches; `-floors "idem=5,obs=5"` sets custom per-dimension overrides. Floors only raise the tier baseline (4 for critical/high, 3 for medium/low), never lower it.

| `-component` preset | Perf | Idem | Resil | Obs |
|---------------------|------|------|-------|-----|
| `wallet` | 5 | 5 | 5 | — |
| `bet-settlement` | 5 | 5 | 5 | 5 |
| `bet-placement` | 5 | 5 | 5 | — |
| `jackpot` | 4 | 5 | 5 | — |
| `responsible-gambling` | — | — | — | 5 |

Why each floor was set where it was:

- **Wallet / settlement / placement writes (Perf 5, Idem 5, Resil 5).** Money-movement paths at high TPS with real concurrent-duplicate risk. 5/5 on performance, idempotency, and resilience reflects what correctness means at this scale, not aspirational engineering. A 4/5 here means "good enough most of the time" — which is the same as "duplicate-pays under load."
- **Bet settlement also requires Obs 5.** Dispute resolution and regulatory inquiries need the full bet lifecycle reconstructed (placed → odds locked → event resolved → outcome determined → payout calculated → wallet credited) with timing at each state transition. 4/5 observability is "logged the result" — not enough. 5/5 is "I can answer the player and the regulator without paging an SRE."
- **Jackpot award paths (Perf 4, Idem 5, Resil 5).** Low TPS but catastrophic per-event risk — every duplicate jackpot pays out the full prize, every dropped jackpot is a regulatory incident. Idem and Resil 5/5 are non-negotiable; Perf 4/5 (bounded queries, documented indexes, no obvious N+1) is sufficient given the rate.
- **Responsible gambling enforcement (Obs 5).** The compliance pass/fail dimension covers "did you log it?" — observability 5/5 covers "can you reconstruct the decision?" Regulators require the full decision path: what checks ran, what data was consulted, what the exclusion status returned, why a player was allowed through (or stopped). Anything below 5/5 here makes audit responses qualitative when they need to be reproducible.

If a reviewer scores a floor dimension at less-than-floor, the iteration request must name the specific gap (which call site, which log statement, which test) — not a generic "raise the score." The Coder targets the gap; the re-review verifies the gap closed.

---

## Review Request Format

The Reviewer receives this — never include the Coder's self-assessment:

````markdown
## Review Request
**Task ID:** [from assignment]
**Risk:** Critical/High/Medium/Low

### Review Metadata
**Worktree:** [absolute path]
**Branch:** [branch name]
**Reviewed SHA:** [git rev-parse HEAD]
**Base ref:** [base branch/commit, or "not provided"]
**Dirty worktree:** true/false

#### Git Status
```text
[git status --porcelain output, or "(clean)"]
```

#### Changed Files
| Path | Status | Exists in worktree |
|------|--------|--------------------|
| [path] | added/modified/deleted | true/false |

### Original Spec
[Copy Task Assignment — Objective through Definition of Done]

### Precomputed Context
[Tasker/CLI should include cheap deterministic context so every reviewer starts from the same facts:]
- Sibling-surface trace from `rg` for changed symbols / tables / exported functions
- Relevant callers/readers/writers found
- Relevant project conventions from CLAUDE.md or architecture docs

### Implementation
[List of files to review]

### Verification Output
[Paste actual test output]
[Paste actual lint output]
[Paste actual complexity output]

### Project Conventions
[Paste relevant sections from CLAUDE.md or architectural guidelines so the reviewer can accurately judge Design Coherence]

### Files for Review
[Code files attached / paths listed — Prefer providing diffs and paths so active agents can use their tools to actively explore the codebase]
````

> Pre-review gates already cleared by the time you dispatch the panel — Critical has Verification Agent + Security Linter PASS; High has Static Analysis Gate PASS. Reviewers audit code that's already verified to compile, test green, lint clean, and pass deterministic security/quality scans.

Before dispatching CLI reviewers, validate that every non-deleted changed file exists in the reviewed worktree. If not, report `INVALID_INPUT` and do not spend reviewer tokens.
