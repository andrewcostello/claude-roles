---
name: task-yaml-review
description: Validate + optimise a dispatcher tasks YAML. Checks order/parallelism correctness, applies the quality floor for model assignment, surfaces problems, and emits an optimisation diff (time | money | balanced). Load this before dispatching a generated YAML.
---

# Task YAML Review

Load this skill after writing or editing a tasks YAML, BEFORE dispatching. It validates the dependency graph, surfaces parallelism mistakes, applies the quality floor, and optionally re-optimises model assignments for time or money.

This skill never changes behavior the dispatcher hasn't already implemented — it's a static analyzer + advisor. It WILL edit the YAML in place if the user accepts the optimisation diff.

---

## Inputs

- Path to a tasks YAML (the file the dispatcher will run).
- Optimisation target: `time` | `money` | `balanced` (default: `balanced`).
- The repo root + base branch (used for ancestry / file-touch heuristics; default to git's current branch).

## Outputs

- Validation report: pass/fail per check, with concrete examples for any fails.
- Optimisation report: per-task suggested model + parallelism change vs the YAML's current state.
- Optionally: a write-back of the optimised values to the YAML if the user accepts.

---

## Step 1: Parse + structural checks

Load the YAML. Verify the top-level shape matches `forecast-fields.md`. Then per task:

### Required fields

Every task row MUST have: `key`, `summary`, `description`, `type`, `labels`. Missing fields → fail with the task index + missing field name.

### Size label

Every task MUST have exactly one `size:XS|S|M|L|XL` label. Multiple sizes → fail. No size → fail. Tasks with `size:XL` are warnings (preference is to split into multiple Ls).

### Description length

Description body < 200 chars → warn. Tasker iterations correlate strongly with description thoroughness; a one-liner forces the Tasker to guess.

### Key namespace consistency

If most keys share a prefix (`BSA-`, `BSA-E2E-`, etc.), flag rows that don't. Often the sign of a copy-paste typo.

## Step 2: DAG validity

### Cycles

Use a DFS over `blockedBy` edges. Any cycle → fail with the cycle path. Dispatcher would deadlock; this MUST be fixed before running.

### Dangling references

Every key in any `blockedBy` MUST be the `key` of some task in the same file. Unknown reference → fail with the typo. Common mode: a Tasker created a sub-ticket and forgot to add it to the YAML — the dispatcher will skip the parent forever.

### Unreachable tasks

A task with no incoming `blockedBy` references AND with `blockedBy: []` is fine (it's a root). But a task whose `blockedBy` lists itself (self-loop) → fail.

## Step 3: Parallelism shape analysis

Classify each task as **foundation** or **leaf**:

- **Foundation** = appears in some other task's `blockedBy` (has dependents)
- **Leaf** = no dependents

### Foundation chain check

Within the foundation tasks, the graph SHOULD be linear (chain), not branching. A branching foundation graph is a sign that some "foundation" task is actually parallel-safe and labeling is wrong, OR that two foundation pieces are truly independent (rare but valid).

- Linear foundation (each F has exactly 0 or 1 foundation dependent): pass.
- Branching foundation (some F has 2+ foundation dependents): warn with the branch point. Ask the user "are these truly independent or should one wait on the other?"

### Leaf-to-leaf chains

If a leaf `blockedBy` another leaf, that's a parallelism opportunity lost OR a hidden foundation. Surface as a warning:
> "BSA-E2E-1-3 blockedBy BSA-E2E-1-2; both are leaves. Either (a) BSA-E2E-1-2 is actually foundation (mark it so + redirect ALL leaves to blockedBy it), or (b) drop the BSA-E2E-1-3→1-2 edge if it was added defensively."

### Foundation hub width

A foundation task with >5 leaf dependents is fine but worth flagging: "BSA-E2E-0-3 has 12 leaves. Will dispatch 12 simultaneously when complete; ensure `--max-parallel` matches."

## Step 4: Apply the quality floor

These rules ALWAYS hold, regardless of the optimisation target:

| Rule | Trigger | Required model | Action on violation |
|---|---|---|---|
| Critical-risk → Opus | label or description mentions: balance mutation, settlement, payout, withdrawal, gambling outcome, idempotency key on financial flow | claude-opus-4-7 | upgrade and flag |
| Financial paths → Opus | description references any path matching a `financial: true` rule in `config/risk-paths.json` — `apps/finance-domain/wallet`, `apps/finance-domain/paygate`, the settlement/refund/wager/recovery files under `apps/platform-domain/bay-session`, or `payout*` under `platform-domain/core` / `game-domain/engine` | claude-opus-4-7 | upgrade and flag |
| High-risk → Opus | label or description mentions: auth surface, JWT, session, state machine, audit trail, ledger, public API contract, RBAC | claude-opus-4-7 | upgrade and flag |
| Foundation → Opus | task appears in ≥2 other tasks' blockedBy | claude-opus-4-7 | upgrade and flag |
| Size XL → split | size:XL label | (none) | reject; ask user to split into multiple L |

If any quality-floor rule fires AND the YAML currently has Sonnet (or no model) on that task, the model MUST become Opus. This is non-negotiable; the optimiser can't override it.

### How to detect "risk" without a label

Risk classification is sometimes encoded in labels (`area:wallet`, `area:auth`), sometimes only in the description (`mutates wallet balance`, `signs JWTs`). The heuristic:

1. Scan labels for `area:*` matching the trigger areas.
2. Scan the description for keywords: `balance`, `withdraw`, `payout`, `settle`, `JWT`, `auth`, `session`, `audit`, `ledger`, `idempot`, `state machine`, `state transition`, `RBAC`, `permission`.
3. If any match: classify as Critical or High (preferring the higher) and apply the floor.

## Step 5: Apply optimisation regime

Tasks NOT pinned by the quality floor are optimisable.

### `time` (optimise for wall-clock)

- All optimisable tasks → `model: claude-opus-4-7`. Opus's higher first-pass quality reduces review iterations.
- Recommend `--max-parallel` equal to the leaf-wave width (count of leaves that fan out from the final foundation task), up to a host-sustainable cap (≥ 4).
- Foundation chain is serialised by `blockedBy`; the dispatcher handles that automatically.

### `money` (optimise for $$)

- All optimisable tasks → `model: claude-sonnet-4-6`.
- Recommend `--max-parallel 1` for runs with few tasks (cache amortisation favours sequential). For runs with > 20 tasks, parallel-4 is fine — the cache cost is dominated by tooling overhead anyway.
- Note: Sonnet's iteration count is often higher; if the run shows Sonnet tasks averaging > 1.5 iterations to APPROVE, money optimisation has reversed itself. Capture this in the `dispatcher report` cost data.

### `balanced` (default)

- Optimisable foundation tasks (even those not pinned by the floor) → Opus. Foundation mistakes propagate.
- Optimisable Medium leaves in non-financial code → Sonnet. Cheap-fast.
- Optimisable Low risk → Sonnet.
- Recommend `--max-parallel 4` with `--auto-integrate`.

### Per-task overrides the user added

If a task row already has `model: ...` AND the quality floor doesn't override it, **keep the user's choice**. The user knows something the heuristic doesn't.

## Step 6: Render the validation + optimisation report

Output to the user:

### Validation section

```
=== VALIDATION ===
Pass: structural checks (all 24 tasks have key + summary + description + type + labels + size).
Pass: DAG validity (no cycles, no dangling references).
Pass: foundation chain is linear (F1 → F2 → F3 → F4).
Warn: leaf-to-leaf chain BSA-E2E-1-3 → BSA-E2E-1-2 — review labeling.
Warn: foundation hub BSA-E2E-0-4 has 8 leaf dependents — set --max-parallel ≥ 8 to use all capacity.
Fail: BSA-E2E-2-1 has no `size:` label.
Fail: BSA-E2E-3-1 blockedBy references unknown task `BSA-E2E-3-0`.
```

### Quality-floor section

```
=== QUALITY FLOOR ===
3 tasks pinned to Opus by quality floor:
  - BSA-E2E-1-5  (description mentions "JWT validation" → high-risk)
  - BSA-E2E-2-3  (touches apps/finance-domain/wallet/ → financial paths)
  - BSA-E2E-0-1  (foundation — 8 dependents)
```

### Optimisation section

```
=== OPTIMISATION (target: balanced) ===
Suggested changes (8 tasks):
  - BSA-E2E-1-1   sonnet → opus     (foundation, 3 dependents)
  - BSA-E2E-1-7   opus → sonnet     (medium leaf, no financial paths)
  - BSA-E2E-2-4   opus → sonnet     (low-risk docs task)
  ...
Estimated cost delta:  -$X.XX  (rough; depends on iteration counts)
Recommended dispatch: --max-parallel 4 --auto-integrate
```

### Decision

Ask the user: `Apply optimisation changes to the YAML? (y/n)`.

- On `y`: edit the YAML in place under the FileLock — preserve comments, only mutate `model:` and (if user agrees) the `--max-parallel` recommendation in the header comment.
- On `n`: print "No changes made. Re-run when ready."

## Step 7: Cost-data feedback loop

If the YAML has been run before AND `cost_usd` / `duration_ms` fields are present on completed tasks, layer that into the report:

```
=== ACTUAL COST FROM PRIOR RUN ===
Avg cost per task:  Opus  $0.42  (n=12)
Avg cost per task:  Sonnet $0.08 (n=8)
Avg iterations:     Opus   1.1  (1 retry across 12 tasks)
Avg iterations:     Sonnet 1.6  (4 retries across 8 tasks)

→ Money-optimised regime: net win of ~$3.20 across this batch
→ But Sonnet's retry rate is 1.6x Opus's — if a retry costs ~$0.16 (4 retries × $0.04 retry cost),
   the net savings is ~$2.56. Real money savings.
→ Time-optimised regime: Opus is faster per-task by 12 min on average. For a 20-task run, that's 4h saved.
```

This is the strongest reason to capture per-task cost data: future optimisation decisions are grounded in measured behaviour, not heuristics.

## Step 8: Hand back

End with:

```
Validation: <pass | N fail / N warn>
Optimisation: <N tasks changed | skipped>
Quality floor: <N tasks pinned to Opus>
Next: dispatcher run <path> ... (recommended args printed in Step 6 above)
```

Do NOT dispatch. Do NOT create Jira tickets. The user picks when to invoke `dispatcher run`.
