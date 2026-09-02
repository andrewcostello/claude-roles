# Repeating this when a new model ships

grok-4.7 is ~10 days out and others follow. This is what is reusable, what is
not, and why the answer is not "build a big benchmark suite".

## A benchmark task is three things

Everything needed to re-measure a model is:

1. **A frozen base commit SHA** — not a branch.
2. **A brief** the arm receives, byte-identical across arms.
3. **A withheld oracle** — assertions the arm never sees, run against what it
   delivers.

We built that tonight without naming it. What is missing is that only one task
exists, its oracle saturates, and its base is not frozen.

## Reusable as-is

| asset | what it does |
|---|---|
| `codex-family.yaml`, `replicates.yaml`, `effort.yaml` | arm definitions — a new model is one row per replicate |
| `scorecard.py` | mutation kill rate, `go test -json` parsing |
| `clauses.json` + `report.py` | clause coverage, dev-vs-review split, rounds, tokens |
| `analyse.py` | noise-aware comparison that REFUSES a ranking it cannot support |
| `--stay-in-family`, `--pin-effort` | keep an arm measuring what it was pinned to |

That harness is the expensive part and it is done. Adding grok-4.7 is a
find-and-replace.

## Not reusable, in priority order

### 1. The base is a BRANCH. Fixed below.

`--base-branch feat/GO-1-1-scaffold-the-wiring-that-decides` moves every time
that branch does — it moved 30+ times tonight. A re-run six months from now
forks from a different tree and is not comparable to tonight's numbers.

**Pin the SHA.** Tonight's runs used `b0313fa`. Every future re-run of THIS
task must use it, or it is a different task wearing the same name.

### 2. The oracle saturates.

Every arm that delivered scored **11/13** mutations. Seven of nine were killed
by every arm; two by none. Clause coverage separated them (12/12 vs 6/12 vs
4/12) only because GO-1-1's contract is unusually precise — and it is grep-based,
so it proves a suite MENTIONS a clause rather than asserting correctly about it.

Adding grok-4.7 to this task produces 11/13 and tells you nothing.

### 3. One task shape.

Writing tests against a tight contract. Nothing here measures design under
ambiguity, integration with existing code, debugging, or a domain where being
wrong costs money.

## The recommendation: harvest, do not build

**Do not build a synthetic benchmark suite.** Two reasons.

It would be a second job with no product value, and — worse — a synthetic task
is one whose difficulty we chose, which is how instruments end up saturated.
Tonight's task saturated precisely because it was well-specified, and we chose
that.

**Instead, freeze each wallet unit as it is built.** Wallet v2 is 73 rows across
13 units we are building anyway, and each one yields a benchmark task for free:

  * a frozen base SHA (the commit before the unit's scaffold landed)
  * a brief (the row's own description)
  * a withheld oracle (`studies/wallet-oracle/` — 19 assertions for WAL-SETTLE
    already, and each unit adds its own)

They span real difficulty without us tuning it: `WAL-STMT` (read-only
reconstruction) is genuinely easier than `WAL-BLAPSE` (progressive forfeiture,
where the answer depends on how much already converted). And they are money
paths, so the oracles are hard invariants rather than taste.

**Cost of harvesting: capture the SHA before each unit starts.** One line. The
oracle is being written anyway because we need it to accept the work.

## When a new model lands

1. Add rows to the arm file — model, effort tiers the CLI accepts, N replicates.
2. Probe the CLI first. Both defects here came from assuming: `grok-build` was a
   UI alias, not an API id; and grok takes `xhigh`, which `AGENT_EFFORTS` denied.
   **Ask the CLI what it accepts; do not infer it from a sibling.**
3. Run with `--stay-in-family --pin-effort` against the frozen SHA.
4. Score with `analyse.py`, which refuses to rank when the between-cell range
   sits inside the noise floor.
5. Report the spread, not the mean. Two arms of the same model differed 1.9x.

## What would make this a real answer rather than a plausible one

The replicate study (7 models x 5 runs) is queued and unfinished. Until it
lands there is no noise floor, and **every per-model claim in this repo is n=1**.
A new model measured against n=1 baselines is two guesses being compared.

That is the honest state: the harness is good, the instrument is saturated, and
the statistics do not exist yet.
