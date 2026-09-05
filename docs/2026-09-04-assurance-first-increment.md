# Assurance work: first increment

This is an implementation record, not a release approval or a revised numeric
quality rating. It follows the workflow/dispatcher assessments and the proposed
agent-neutral architecture dated 2026-09-04.

Follow-up: [acceptance-evidence increment](2026-09-04-assurance-evidence-increment.md)
records the next bounded repair and the remaining evidence-binding gaps.

## Implemented in the working trees

- **WF-02, gate selection:** reject unknown, empty, or non-applicable names;
  reject empty plans; validate before dry runs; make omitted required gates fail
  unless explicitly waived. Existing CLI exit codes and state schema remain.
  CLI regression tests check banners, return codes, non-execution, and unchanged
  state on invalid/dry runs; unit tests exercise normalized subsets.
- **DP-06, integration file preservation:** remove indiscriminate `git clean`
  from the caller's checkout. Inspect additions in the computed merge tree and
  refuse local path collisions before stashing or merging. Tests cover unrelated
  files, ignored/untracked collisions, rename destinations, newline-containing
  paths, directory/file and symlink collisions, transient YAML preservation,
  and preflight failure.
- **Evaluation pilot:** `evals/` packages the historical WF-02 defect as a Harbor
  task. A pinned source export and image digest, separate verifier, unprivileged
  candidate process, and positive/negative controls establish a first repeatable
  acceptance check. No live model adapter or paid comparison has run.

Both repositories remain in place. The evaluation directory accepts an explicit
source-repository path so it can move to the neutral repository without copying
either workflow's runtime implementation. The in-progress dispatcher run-state
split and existing model-matrix edits were left alone.

## Verification

At this increment, the dispatcher suite collected and passed **3,998 tests**.
Its T26 contract/documentation lint passed. The suite still emits the two
previously observed pre-PR2 boundary-degradation warnings; these were not repaired.

All seven workflow Go modules passed `go test -race -count=1 -cover ./...`.
The gate CLI tests build a temporary executable rather than overwriting the
tracked binary. Coverage from that child executable is not included in the Go
unit-test coverage percentage.

The Harbor controls each completed **25 acceptance checks**: historical source
was rejected, the reference repair accepted, and always-pass/always-fail mutants
both rejected. These are grader controls, not evidence about model quality or
financial correctness. The final control run also records harness hashes and
the Python version. Its evidence is retained at
`/var/tmp/assurance-repairs.iytpf9/controls-final/`. The six fast export/result-reader
unit tests and the pinned environment's dependency consistency check passed.

No code was committed, pushed, deployed, or approved by an independent review
panel. **Tracked Go executables were not replaced:** callers of the existing
`cmd/gates/gates` binary still use its previous build. Before adopting the repair,
review and commit the source, build the intended release artifact, and rerun
acceptance against that exact artifact/revision. Working-tree test success does
not satisfy the repository's committed-tree acceptance rule.

## What remains, in order

1. **Acceptance evidence:** repair loss of classification fields in state
   round-trips; invalidate all earlier verification after a code-changing retry;
   bind evidence to revision, invocation, and policy. Align this with the existing
   classification/gating boundary work and Phase 14 rather than adding a second
   competing state owner.
2. **Reviewer policy:** close demonstrated false approvals, including single-seat
   rejection handling, severity floors, incomplete inputs, and missing classifiers.
3. **Integration boundary:** move merging/checks into an owned temporary worktree,
   verify the merged candidate using the actual project test configuration, and
   handle concurrent branch changes and state restoration explicitly. The local
   preflight here does not provide transactional isolation from concurrent writers
   or solve all existing stash/error-handling hazards.
4. **Evaluation dataset:** grow to five real regressions and five representative
   changes, with human-agreed domain invariants and held-out cases. Add Go,
   TypeScript, and Python profiles around shared acceptance semantics.
5. **Model/workflow trials:** validate scoped provider adapters, then compare
   pinned complete configurations over repeated trials with all errors, retries,
   human interventions, and critical escapes visible. Approval for a live run
   should identify the models, run matrix, credentials, and spend limits.
6. **Consolidation:** move shared policy, evidence contracts, and evaluations
   into the neutral repository; retain separate interactive and dispatch modes.
   Keep strong defaults for both. Any sandbox profile must be explicitly chosen.

Independent domain/security review and production-like failure/recovery testing
remain necessary before increasing autonomy on money or gambling logic.
