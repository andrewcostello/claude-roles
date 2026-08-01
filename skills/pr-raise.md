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

### The financial-paths check is computed, not recited

`cmd/classify` owns it. `classification.financial_paths_touched` in the run state is the OR over every rule carrying `financial: true` in `config/risk-paths.json`, matched against the changed files. **Read that field; do not maintain a second list here.**

```bash
git -C "$WORKTREE" diff origin/main...HEAD | ~/Project/claude-workflow/cmd/classify/classify \
  -worktree "$WORKTREE" -base origin/main -task "$TASK_KEY" -out "$RUN_STATE"
# → classification.human_pr_gate is true iff risk == critical OR financial_paths_touched
```

`classification.human_pr_gate` is exactly the two-condition gate above, already evaluated. Use it.

> **Correction 2026-07-29 — the list previously documented here matched nothing.** It named `apps/finance-domain/settlement/**`, `apps/finance-domain/recovery/**`, and `apps/finance-domain/payout/**`. None of those directories exist: `apps/finance-domain/` contains only `paygate/` and `wallet/`. Settlement, refunds, and dispute reversal live under `apps/platform-domain/bay-session/store/` — which the paragraph below then named as the example of a *non*-financial path. Net effect: the path check — whose whole job is to be the backstop when tier judgment misses — was dead for every money path outside `wallet/`. A change to `admin_bet_force_refund.go` or `admin_bet_dispute_reverse.go` still fired the gate *if* the Tasker classified it Critical; if it classified it High, nothing caught it. And the paragraph below actively invited exactly that call, naming `apps/platform-domain/bay-session/` as High-but-not-financial. `config/risk-paths.json` now classifies those paths critical + financial; `cmd/classify`'s `TestClassify_BaySessionMoneyPathsAreFinancial` locks it in.

**Fallback for agent-driven runs with no classify available.** This list is a DUPLICATE and therefore a drift hazard — it is the `financial: true` rules from the project's `.agent/risk-paths.json`, copied. It drifted within a day of being written (8 rules added by the 98-PR validation sweep never landed here; caught by the panel on dispatcher PR #71, 2026-08-01). Prefer `classification.human_pr_gate`; reach for this only when the binary is genuinely unavailable, and re-derive it rather than hand-editing:

```bash
jq -r '.rules[] | select(.financial) | .paths[]' .agent/risk-paths.json
```

```
apps/finance-domain/wallet/**
apps/finance-domain/paygate/**
apps/platform-domain/bay-session/store/accept_bet*
apps/platform-domain/bay-session/store/wager*
apps/platform-domain/bay-session/store/arm_*
apps/platform-domain/bay-session/store/bet_amount_bounds*
apps/platform-domain/bay-session/store/bet_mutation_*
apps/platform-domain/bay-session/store/advantage_tier_caps*
apps/platform-domain/bay-session/store/bet_settle*
apps/platform-domain/bay-session/store/*settlement*
apps/platform-domain/bay-session/store/sqlc/bet_settle*
apps/platform-domain/bay-session/store/admin_bet_dispute_reverse*
apps/platform-domain/bay-session/store/admin_bet_force_refund*
apps/platform-domain/bay-session/store/*refund*
apps/platform-domain/bay-session/cmd/admin-bet/**
apps/platform-domain/bay-session/cmd/*-recovery/**
apps/platform-domain/bay-session/cmd/*recovery*/**
apps/platform-domain/core/dao/payout*
apps/platform-domain/core/model/payout*
apps/platform-domain/core/model/tournament_payout*
apps/platform-domain/core/service/tournament/*payout*
apps/game-domain/engine/dao/payout*
apps/game-domain/engine/model/payout*
apps/game-domain/station-state-computer/model/payout*
libs/go/wallet/**
```

`$FINANCIAL_PATHS` (comma-separated globs) or `dispatcher run --financial-paths '<patterns>'` still override, for projects with a different layout. A duplicated list is a drift hazard — prefer `-config` pointed at a project-specific `risk-paths.json`.

### Why path-based and not just tier-based

Risk tier answers "how deep does review go"; the path check answers "may this merge unattended". They disagree exactly when they should: a Coder-discovered wallet bug might land in a "state machine" classification (High), but the change still touches money — the path check catches that.

The converse also holds, which is what keeps unattended throughput: `apps/platform-domain/bay-session/store/bay_station_register.go` is bay-session state-machine code, classified **High and non-financial** — no human gate, auto-raise on APPROVE. The distinction inside bay-session is per-file, not per-directory: settlement/refund/wager files are financial, station and session lifecycle files are not. This is precisely why the check belongs in a rule table with tests rather than in a directory-level generalization.

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
