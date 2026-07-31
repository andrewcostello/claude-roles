# Design: Cross-Review Findings Cache (reviewer optimization #6)

Status: **SHELVED (2026-07-31)** — sizing dry-run shows low ROI. Design kept for
reference; do NOT build without new evidence that cross-review file stability rose.
Date: 2026-07-31

## Dry-run outcome (why shelved)

The cache keys on `sha256(file contents)`, so it only pays off when a review reads
a file **byte-identical** to a prior review. Three signals say that's rare here:

1. **Overlap is re-runs, not cross-task.** 46 run-records collapse to few distinct
   reviewed SHAs (one appeared 13×, others 4×) — most repeat is the SAME PR
   re-reviewed, which `recheck` already amortizes (rounds 3+).
2. **Hot files co-change.** 14/46 runs touch funds.go + playerfunds.go +
   xwalletfunds.go together — the siblings a scout reads have also just changed →
   content-hash miss exactly where review concentrates.
3. **Near-zero stability in the hot package.** bay-session has 235 non-test .go
   files and essentially all changed in the last 60 days — no stable siblings to
   cache where it matters.

Conclusion: the real repeat is within-task (recheck) and co-churn (uncacheable);
cross-task byte-identical overlap is thin. Redirect effort to the higher-ROI
levers (#1 model-tiering, #3 drop-broad-seat) validated by the closed-PR
experiment. Revisit only if a future measurement shows high stable-sibling reuse.

---


## Problem

The measured cost driver of a scout panel is **investigation** — scouts tool-read
files across many turns; on a real PR `cache_read` reached ~5M tokens/run, and the
long-pole scouts (dataflow-spec, test-docs) run 5-12 min re-reading files. Across
a critical PR's lifecycle (PR1380: 5+ passes) and across sibling tasks touching
the same packages, scouts re-read and re-analyze **byte-identical files** every
run.

`cmd/recheck` already amortizes *within* a task (rounds 3+ verify prior findings
against the iteration diff instead of re-discovering). Nothing amortizes **across**
tasks, or for the **unchanged sibling/context files** a scout reads while
reviewing a changed one.

## Non-goals

- Not a replacement for `recheck` (within-task rounds 3+).
- **Not a substitute for seam analysis.** A cached fact about file F never
  certifies F's interaction with a newly-changed file G. The cache economizes
  *reading*, never *cross-file reasoning*.

## What is cached

- **Key:** `sha256(file contents)`. Content-addressed → auto-invalidating: a
  changed file has a new key = a miss = full re-analysis. There is no stale-file
  failure mode by construction (this is the whole reason to key on hash, not path).
- **Value (per file-hash):**
  - `path`, `reviewed_sha`, `task`, `timestamp` (provenance)
  - `local_findings[]` — findings the prior review attributed to THIS file that are
    LOCAL to it (own logic/structure), each with severity/line/title
  - `cross_file` flag on any finding whose validity depends on another file
  - `locally_clean` — was the file locally clean at this hash?
  - optional `summary` — one line on what the file does (orientation, like the
    change-map but persistent + per-file)

## Soundness rules (the load-bearing part)

1. **Content-hash = auto-invalidation.** Changed file → new hash → miss → full
   re-analysis. No trigger, no "when to refresh" problem.
2. **Carry, then re-verify — never blind-trust.** A cached local finding for an
   unchanged file is a *candidate*; it is re-checked against the current diff
   (recheck-style: RESOLVED / STILL_OPEN) before being reported. The cache decides
   what to RE-DISCOVER, never what to assert.
3. **Cross-file findings are never carried on file-hash alone.** A finding tagged
   `cross_file` is re-derived whenever ANY involved file's hash changed. Only
   single-file-local findings are hash-cacheable.
4. **Miss is safe.** Absent / corrupt / unreadable entry → full analysis. The
   cache never blocks and never fails a review (fail-open to correctness).

## Integration points (increasing ambition)

- **(a) Scout context assist — SAFE, ship first.** When a scout is about to
  tool-read an UNCHANGED sibling/context file (not in the diff) whose hash is
  cached, inject the cached summary + local findings instead of a fresh read.
  Pure context economy — verdict logic unchanged. Attacks the investigation cost
  directly.
- **(b) Skip-clean-files — DEFER.** For a scoped file byte-identical to a cached
  clean review AND with no changed cross-file dependency, mark it "carried-clean"
  and skip full re-scout — but STILL run recheck-style re-verification of carried
  findings. Requires a cross-file dependency graph; higher risk. Not in the first
  cut.

## Storage

- `-findings-cache <dir>` flag, **default off**. Suggested default dir
  `~/.cache/reviewer/findings/`. One JSON per hash (or a keyed store). Machine-local
  for the first cut; a shared/team store is a later option.
- **Not** in the repo — it's a content-addressed cache. LRU/size-cap prune.
- Populated after each cache-enabled review from the findings + per-file
  attribution. Optionally pre-warmed at commit/CI time (hash-keyed → a miss just
  regenerates, so pre-warming is a pure optimization with no staleness risk).

## Failure modes / fallback

Cache dir missing/unwritable → proceed without it (log). Hash miss → full
analysis. Corrupt entry → ignore + re-analyze. Never blocks, never fails a review.

## Measurement / validation

- Log per-review cache hit-rate (files served from cache vs fresh-read) +
  estimated turns/tokens saved.
- **Correctness gate:** a cache-enabled run MUST produce the same findings as a
  cold run on the same PR — validate with the closed-PR ground-truth experiment
  (the cache must never hide a finding). This is non-negotiable before default-on.

## First-cut scope (recommended)

`-findings-cache <dir>` (default off) · content-addressed per-file store
(`sha(file) → {local findings, locally_clean, summary}`) · integration (a) only ·
re-verify carried findings against the diff (reuse recheck's verifier) · hit-rate
logging. **Deferred:** (b) skip-clean-files, pre-warming, cross-file dep graph.

## Open questions (answer before building)

1. **ROI first.** How much cross-review file *overlap* actually exists? If most
   re-runs are within-task, `recheck` already covers it and #6's payoff is modest.
   Measure overlap (from recent `/tmp/reviewer-*` runs or a hit-rate dry-run)
   BEFORE building the ambitious version — if overlap is low, say so and stop at
   (a) or skip entirely.
2. Cache location: machine-local dir (start here) vs a shared team store.
3. Is the value mostly the persistent per-file **summary** (orientation, cuts
   read turns) rather than carried **findings**? If so, #6 converges with a
   persistent change-map and should be built as that, not a findings ledger.
