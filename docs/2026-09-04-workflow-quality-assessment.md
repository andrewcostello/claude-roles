# Workflow quality assessment

Assessment date: 2026-09-04. Source revision: `3e225a7a1e951d0717472c33b3a17930c16bcd0a`.

Implementation follow-up: [first assurance increment](2026-09-04-assurance-first-increment.md).
The findings below describe the audited revision; the follow-up tracks subsequent repairs.

## Objective and scope

Evaluate the workflow for financial and gambling games, prioritizing correctness
over elapsed time and token cost, including an experimental move to Codex.
This assessment covers the main lifecycle instructions, Go orchestrators, CI,
and model-evaluation documents. It does not establish the behavior of the
external dispatcher, deployed repository protections, or product services.

The recommendations below were accepted for further planning. This document
records findings; it does not implement the repairs. Existing modifications to
`features/model-matrix/codex-family.yaml` and `convergence.yaml` were preserved.

Subsequent clarification: Tasker builds real, production-bound systems as well
as supporting collaborative discovery, vibe coding, prototypes, and feel-based
work. It is neither simply a smaller dispatcher nor an implicitly lax process.
Keep strong engineering assurance as the default in both workflows. A lighter
experimental profile should require explicit invocation and must not imply
production readiness. See [the combined architecture recommendation](2026-09-04-agent-neutral-architecture.md)
and [the dispatcher assessment](../../claude-dispatcher/docs/2026-09-04-dispatcher-quality-assessment.md).

## What to retain

- Deterministic tools own classification, gate execution, and verdict reduction.
- Separate design, implementation, verification, and review responsibilities.
- Explicit missing/error states and conservative risk classification.
- Behavioral regression tests, numeric properties, and mutation testing.
- Sibling-surface tracing and investigations of escaped defects.
- Model studies that distinguish measured results from inference.

## Confirmed implementation findings

The following were reproduced against temporary copies of the current source.
They do not depend on an LLM provider being available.

### WF-01 — Stage updates destroy fields owned by other stages

Priority: P1, repair before trusting automated acceptance.

`cmd/gates/main.go:121` defines only a subset of the classification. Its
`mergeGates` function at line 1316 reads and serializes the entire state through
that partial type. A gate update deletes `human_pr_gate`,
`financial_paths_touched`, `reviewer_args`, `recheck_min_severity`, `panel`, and
classification provenance. `cmd/iterate/main.go:440` does the same kind of
rewrite; its partial gate type also deletes commands, output paths, and times.

Consequence: downstream stages lose the information needed to enforce review
depth and human approval or inspect the evidence behind a passing gate.

Repair: use a shared versioned contract with lossless ownership-preserving
updates, or immutable artifacts per stage and a final validated aggregation.
Use atomic writes and reject incompatible versions.

Acceptance: an end-to-end classify → gates → review → iterate fixture preserves
all fields owned by other stages. Test interrupted writes and invalid schemas.

### WF-02 — Selecting no actual checks reports PASS

Priority: P1.

`cmd/gates/main.go:665` marks unselected gates skipped, while `finish` at line
294 counts only failures. A real CLI invocation with `-only typo` skipped the
only required test, printed `GATES: PASS`, and exited zero.

Repair: validate requested gate names; distinguish partial execution from
complete acceptance; require evidence for every required gate. A waiver must
be an explicit policy decision with scope and provenance, not an ordinary pass.

Acceptance: unknown names, an empty applicable plan, unapproved skips, and
incomplete required checks cannot produce overall PASS.

### WF-03 — An earlier approval can authorize different or unverified work

Priority: P1.

`cmd/iterate/main.go:198` stops at a previous APPROVE without checking that its
reviewed SHA matches the current state or that gates passed. A probe supplied a
new head SHA and a failed test gate and still received APPROVE.
`recordFull` at line 343 also accepts an existing approval artifact when the
reviewer process exits 2, meaning unavailable.

Repair: bind evidence to source SHA, base SHA, policy/configuration digest,
tool identity, and invocation ID. Reject artifacts from failed or different
invocations. Reclassification and new commits must invalidate old acceptance.

Acceptance: new source, changed policy, failed reviewers, stale output files,
and failed gates cannot reuse an old approval.

### WF-04 — Missing review input becomes approval

Priority: P1.

`cmd/recheck/main.go:117` approves when no prior findings are selected, before
validating the repository or computing its current diff. Running the CLI with
`{}` as the findings JSON and a nonexistent worktree returned APPROVE, exit 0.

Repair: validate required fields, schema, repository, reviewed commit, verdict,
and changed scope before deciding there is no work. Absence of findings is not
evidence of a successful review. Preserve non-finding review failures as well.

Acceptance: malformed/missing review metadata and changes since the prior
review cannot bypass validation or the required review of new changes.

### WF-05 — The money-path MEDIUM threshold is bypassed by initial approval

Priority: P1.

`cmd/reviewer/main.go:653` blocks HIGH/CRITICAL or explicitly blocking findings,
but not ordinary MEDIUMs. A full critical-wallet panel with passing dimensions
and an open MEDIUM returned APPROVE. The component-specific MEDIUM threshold
in recheck does not help when the initial approval terminates the loop.

Repair: enforce one configured severity threshold throughout all rounds and
final acceptance. Record evidence-based dispositions for rejected findings.

Acceptance: an unresolved MEDIUM on a component requiring zero MEDIUM-or-higher
prevents approval from the first round onward.

### WF-06 — Recheck cannot hand new findings to the next round

Priority: P1 for multi-round execution.

`cmd/recheck/main.go:170` serializes counts rather than the new finding objects.
`cmd/iterate/main.go:372` records a recheck without a findings path. The next
round falls back to a `findings-<task>-r3.json` file that the third round never
wrote. A probe reproduced that missing handoff for round four.

Repair: persist stable finding identities, full findings, evidence,
dispositions, and the commit each round reviewed.

Acceptance: exercise a complete four-round controller run, including new
findings from round three, without manually fabricating artifacts.

### WF-07 — Evidence-write failure does not invalidate success

Priority: P1 where evidence is required for acceptance.

`cmd/gates/main.go:767` discards raw-log write errors. A probe used a missing
log parent directory: the command passed and the gate reported PASS despite
the evidence file not existing. Gate-state write errors at line 303 are also
warnings rather than a non-success result.

Repair: make required artifact persistence part of gate success. Use immutable
per-run output paths and record artifact hashes.

Acceptance: unwritable output/state destinations cannot yield accepted evidence.

## Accepted workflow improvements

### Independent acceptance tests for Critical work

Remove the Critical-risk exclusion in `roles/regression-test-author.md:20`.
Independently running the implementer's tests establishes whether they pass;
independently writing acceptance tests assesses whether they cover the intended
behavior. Require both for money movement and outcome logic. Extend this to
new Critical features, not only reported bugs.

Give the test author approved requirements before implementation. Keep
assertions stable; changes to requirements or acceptance criteria require a
recorded decision. Independent authors can still share a specification error,
so review the specification and use domain reference examples as well.

### Financial state and failure testing

Keep the existing numeric property requirements. Add generated sequences and
controlled concurrent schedules for reserve, settle, expire, cancel, refund,
and recovery. Cover duplicate/conflicting idempotency keys, reordered events,
lost responses after commits, crashes across database/event boundaries,
reconciliation, currency precision, overflow, and rounding.

Use real databases and applicable service boundaries. The Go race detector
does not establish database transaction correctness. Test authorization,
player restrictions, and outcome/fairness rules when those surfaces apply.

References: [Go race detector](https://go.dev/doc/articles/race_detector),
[PostgreSQL transaction retries](https://www.postgresql.org/docs/current/mvcc-serialization-failure-handling.html).

### Language and dependency-aware verification

`cmd/gates/main.go:454` discovers modules only from changed `.go` files. Extend
selection to SQL, protobuf, dependency manifests, frontend code, and affected
consumers of shared libraries. Every affected component needs an explicit gate
outcome, including generated artifacts and integration tests.

Use one language-neutral quality contract with Go, TypeScript, and Python
adapters. Java can be an optional adapter. Do not copy the entire workflow into
three language-specific versions. Detailed architecture follows the dispatcher
assessment.

### Review scope follows semantic impact

Replace the unconditional round-two discovery cutoff in
`skills/iteration-protocol.md:119` with a scope decision. Narrow fixes can use
targeted verification; contract, transaction, authorization, or state-machine
changes trigger broader review of affected consumers. Critical acceptance needs
a final review of the complete change at the exact tested commit.

An iteration limit triggers escalation, never approval. Finding counts alone do
not establish that a change is converging or safe.

### One policy with agent-specific entry points

Resolve contradictory instructions: `roles/tasker.md:62` calls migrations Low;
other guidance treats them more strictly. `roles/coder.md:432` and the
Verification Agent still carry an obsolete mutation command. Executable gate
configuration should own commands and policy; instructions should describe
purpose and how to consume the result.

For Codex, add a concise `AGENTS.md` and discoverable skill wrappers. Keep
provider-specific invocation details in adapters. Pin model and supported
reasoning effort explicitly: `cmd/reviewer/main.go:1014` currently silently
coerces every effort other than `xhigh` to `high`.

References: [AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md),
[skills](https://learn.chatgpt.com/docs/build-skills).

### Tested artifact identity and a green baseline

Several tracked executables carry older source revisions and dirty-build
stamps. Source tests do not establish what those executables do. Complete the
existing GO-2 artifact work, build into separate output directories from pinned
source, test the produced executables, and record their identities with runs.

Restore a green CI baseline instead of permanently omitting checks. Require
fresh approval and required CI evidence at protected merge/release boundaries;
approval merely to create a PR does not establish those protections.

## Verification performed

- Go version: `go1.26.0 linux/amd64`.
- All seven modules passed `go test -race -count=1 -cover ./...`.
- Statement coverage: classify 45.9%, gates 76.5%, iterate 50.7%, recheck 29.5%,
  repro 22.9%, deepseek 44.9%, reviewer 25.5%.
- `go vet ./...` failed in deepseek: tests use `testing.Context` while the
  module declares Go 1.21. `gofmt -l` reported `cmd/repro/main.go`.
- Eight additional regression probes failed their desired safety assertions,
  reproducing the implementation findings above. Direct CLI checks also
  reproduced all-skipped PASS and empty-input recheck APPROVE.
- Temporary reproductions are in `/tmp/workflow-quality-audit.RVViMi/`.
  Temporary paths are not durable project test assets.
- No product integration tests, paid model panels, or external writes ran.

## Model evaluation and implementation order

Current model studies are limited by one task shape, small samples, grep-based
clause coverage, and saturated mutation scores, as their own documentation
states. Extend the wallet study with frozen requirements, withheld tests,
multiple runs, and seeded financial faults. If the oracle changes, rerun all
compared candidates. Requirements should be visible; test cases may be withheld.

Implement controller and evidence repairs first, independent Critical acceptance
tests second, and consolidated language/provider adapters third. Evaluate the
dispatcher before deciding which implementations to retain in a combined repo.
