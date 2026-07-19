---
name: pr-raise
description: PR-raise mechanics — human approval gate (Critical OR financial-paths-touched), PR title and body format, size gates, no-attribution rule. Owns Phase 4.5 of the Tasker workflow.
---

# PR Raise

**Precondition — a review panel must have run; no PR is ever raised with zero AI review.** For any change in the mandatory-panel set (Go/server/SQL/proto, money/wallet/auth paths, or Medium+ risk — see `tasker.md` Phase 3.4 "The panel is MANDATORY"): a completed full `cmd/reviewer` panel at `APPROVE`. For the client-only-presentation carve-out: at minimum a **single-reviewer** run (`-reviewers claude` or `-reviewers codex`) at `APPROVE`. "Read-path", "small", or "obvious" are not full-panel exemptions. A PR raised on mandatory-set code without the full panel — or any PR raised with no reviewer at all — is a process defect, corrected by running the appropriate panel retroactively.

**Post-merge — close the ticket (team policy, Andrew 2026-07-16).** When a PR MERGES, transition its ticket(s) to the terminal closed state — **SMG → `Done`, FSG → `Closed`** — regardless of assignee, with a comment referencing the merging PR. (SMG's Done transition has no resolution-screen field — don't pass `--resolution`.) Two guards: (1) a PR that only *partially* delivers a ticket does NOT close it — leave it open with a note; (2) a ticket explicitly gated on deployment (e.g. "close with next stage release") closes on deploy, not merge. This supersedes the older "humans transition" convention for the merge boundary.

Load this skill on `APPROVE` verdict. The skill owns:

1. The human approval gate — fires for Critical risk OR when changed files match the configured financial-paths list
2. PR title and body format
3. Size gates and split criteria
4. The no-attribution rule
5. The `Prepared PR` summary section format for human/dispatcher hand-off

---

## Human Approval Gate

**The gate fires iff either condition holds:**

1. **Risk == Critical** — balance mutations, bet settlement, payout calculations, withdrawals, gambling outcome determination, recovery/retry paths that replay financial or state-mutation operations.
2. **Any changed file matches the financial-paths list** — even when the Tasker classified the change as High (so a wallet payout misclassified as "state machine" still trips the gate by path).

When the gate fires, do NOT raise the PR yourself. Write the `Prepared PR` section into the summary file with everything ready (branch, title, body). The dispatcher in supervised mode prompts the human; in unattended mode it writes `Status: Blocked` with reason "awaiting human PR approval" and moves on. In standalone mode the Tasker prints the Prepared PR section and the human raises it manually.

### Default financial-paths list (evenplay-mono)

```
apps/finance-domain/wallet/**
apps/finance-domain/settlement/**
apps/finance-domain/recovery/**
apps/finance-domain/payout/**
```

The list is configurable via the `$FINANCIAL_PATHS` env var (comma-separated glob patterns) or via `dispatcher run --financial-paths '<patterns>'`. Override in `CLAUDE.md` per project — these defaults assume the wallet / settlement / recovery / payout module layout in this monorepo.

### Why path-based and not just tier-based

Risk tier is the Tasker's classification; the path list is a second deterministic check. The two disagree exactly when they should: a Coder-discovered wallet bug might land in a "state machine" classification (High), but the change still touches money. The path check catches that. Conversely, a state-machine schema change in `apps/platform-domain/bay-session/` is High but is **not** financial — auto-raising it preserves unattended throughput.

---

## Auto-Raise (everything else)

For everything that does not fire the gate above — i.e., High/Medium/Low with no financial-paths file touched — raise the PR immediately on APPROVE. No human pause. Use the title and body format below.

---

## PR Title Format

```
type(scope): [SMG-XXXX] imperative description in present tense
```

Examples:
- `fix(platform): [SMG-1653] resolve bet by hit-bound ID to prevent race condition`
- `feat(wallet): [SMG-1657] add escrow state to prevent silent payout loss`
- `refactor(engine): [SMG-2384] extract round-robin selector into dedicated package`

Keep the title under 70 characters when possible. Imperative mood: "add" / "fix" / "remove" / "refactor", not "added" / "fixes" / "removing".

---

## PR Body Template

```markdown
## What
[One paragraph — what was changed and why it matters.]

## Why
[Root cause or requirement. Reference the Jira ticket: SMG-XXXX. For bug fixes, the root cause comes from the failing-test docstring; see bug-fix-protocol.md.]

## How
[Key implementation decisions — notable patterns, locking strategy, idempotency approach, anything the reviewer would want to flag if it were buried.]

## Test evidence
```
[paste actual test output showing the new tests + the full domain suite passing under -race]
```

## Ticket
SMG-XXXX
```

The PR body is self-contained — a reviewer with no prior context must understand what and why.

---

## PR Rules

- **One PR per ticket** — never bundle multiple tickets.
- **Target `main`.**
- **No unrelated changes** — no reformatting, no style fixes for code you didn't touch.
- **No attribution of any kind** — no "Generated with Claude Code", no "Co-Authored-By", no author names, no tool references. Code and docs belong to the team.
- **PR description is self-contained** — a reviewer with no prior context understands what and why.

---

## Size Gates

| Lines changed | Action |
|---------------|--------|
| ≤ 250 | Proceed normally |
| 251–500 | Flag to human — confirm scope before raising PR |
| > 500 | Stop — split or get explicit human approval |

If the diff exceeds 500 lines and was not intended to be that large, the Coder bundled scope. Surface this in the summary file's `Key decisions` section so the human can see why.

For tasks that already fire the human gate (Critical or financial-paths-touched), the size gate prompt is folded into that pause — the human sees both the gate context and the size flag together.

---

## Prepared PR Section (gate-fired path)

When you stop at the human approval gate (Critical risk or financial-paths-touched), write this into the summary file under `## PR`:

```markdown
## PR
Prepared, awaiting human approval

### Prepared PR
**Title:** fix(wallet): [SMG-1657] add escrow state to prevent silent payout loss
**Branch:** feat/SMG-1657-escrow-state
**Lines changed:** 187 (within size gate)
**Body:**
```
## What
...

## Why
...

## How
...

## Test evidence
```
[test output]
```

## Ticket
SMG-1657
```
```

The dispatcher, in supervised mode + human approves, runs:

```bash
gh pr create \
  --title "<title from summary>" \
  --body-file <(echo "<body from summary>") \
  --base main \
  --head <branch from summary>
```

…captures the URL, and writes `pr_url:` to the YAML row.

In standalone mode (no dispatcher), the Tasker prints the Prepared PR section and lets the human run the `gh` command themselves.

---

## After PR Is Raised

For auto-raise (gate didn't fire) and for dispatcher-completed PRs (gate fired, human approved):

1. Update the summary file `## PR` line to the actual PR URL
2. Set `Status: Done` in the summary
3. Record `final_quality_score` and `deferred_findings_count` from the review consensus

The dispatcher copies these fields into the YAML.

---

## Common Failure Modes

| Failure | Cause | Fix |
|---------|-------|-----|
| PR title missing `[SMG-XXXX]` | Coder forgot the ticket prefix | Reject; rewrite the title before raising |
| PR body has Co-Authored-By or "Generated by" footer | Tool added attribution | Strip before raising; this is the no-attribution rule |
| PR is 800 lines, no advance flag | Coder bundled scope | Either split or get explicit human approval; do not auto-raise |
| PR targets a feature branch, not `main` | Wrong base | Re-target to `main`; never raise PRs against feature branches |
| PR raised when gate should have fired (Critical or financial-path change) | Auto-raise leaked past the gate check | Close the PR with apology comment; do not amend silently |
| Gate fired for a non-financial High task (e.g. state-machine schema) | Mis-applied tier-only logic; path check should have skipped the gate | Update the financial-paths list or fix the check; do not introduce manual overrides |
