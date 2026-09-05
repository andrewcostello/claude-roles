# Acceptance evidence: second increment

This is a working-tree implementation record, not release approval. It continues
the [first increment](2026-09-04-assurance-first-increment.md). The two workflows
remain separate; no dispatcher runtime or planned boundary package was changed
in this increment.

## Implemented scope

- The classifier, gate runner and iteration controller update only their owned
  fields using a shared raw-JSON updater. Unowned policy fields, historical
  results, unknown fields and large integers survive without lossy conversion.
- Updates use a same-directory temporary file, file sync, atomic replacement
  and directory sync under a fail-fast sidecar lock. Duplicate JSON keys,
  unsupported state versions, non-regular targets and conflicting writes are
  refused. Existing file permissions are retained. Errors do not authorize a
  successful gate or review result.
- Gate recording refuses changes to its classification/repository snapshot.
  Round recording also checks gate and controller inputs and refuses duplicate
  round numbers. These checks preserve concurrent foreign updates or refuse a
  conflict; they do not silently overwrite them.
- Each executed review uses a fresh private output directory. Failed reviewers
  cannot supply a completed round through an existing output file. Missing,
  malformed, duplicate-key, contradictory and incorrectly scoped outputs fail
  closed. Recheck exit codes must agree with its verdict and finding counts;
  its prior revision and checked count must match the supplied findings, which
  must remain unchanged throughout execution.
- Full reviews use the classified base commit and worktree. The controller
  checks the live commit and clean worktree before and after execution, checks
  the returned revision, and refuses an earlier approval after HEAD changes.
  Approvals also require nonempty, successful or explicitly waived stored gates
  and no recorded unresolved findings at the configured floor.
- Terminal decisions no longer append fictitious review rounds. Dry runs do
  not modify state. Failed evidence persistence cannot print a completed-round
  success banner or return approval.

## Change record / review boundary

- **kind:** new-surface (shared local persistence module), plus tightened legacy
  consumer validation. No v1 JSON fields or v2 boundary contracts were added.
- **original:** independent, whole-file typed rewrites and predictable output
  paths; review completion could be inferred despite tool or persistence errors.
- **changed:** shared cooperative update protocol and invocation-specific result
  ingestion, with revision and input-snapshot checks in the existing controller.
- **reason:** reproduce and prevent loss of acceptance evidence and demonstrated
  stale-result approvals before increasing agent autonomy.
- **blast_radius:** builds of classify/gates/iterate must retain `shared/statefile`
  beside `cmd/`; CI and the dispatcher test loop include the shared module. New
  readers refuse abbreviated/missing review commit IDs and incomplete legacy
  results. Keep run state and attempt output outside the tracked worktree.
- **review status:** not independently adjudicated or committed. This is not
  implementation or acceptance of the dispatcher's staged boundary work.

## Operational limits

All writers of one run-state path must use the new protocol. Old binaries and
manual whole-file writers do not participate in its lock. Use one canonical
regular-file path, not hard-linked aliases. The lock is local coordination on a
filesystem supporting atomic replacement, not protection from a process with
arbitrary filesystem access.

A crash can leave `<run-state>.lock`. Confirm the writer has stopped before
removing it; do not break locks automatically based on age. A sync or cleanup
failure can be reported after replacement occurred: inspect the state and rerun
verification, never reinterpret the error as success. Attempt directories are
retained for diagnosis.

## Still required before stronger autonomy

1. **Complete evidence binding:** v1 gate records lack revision, invocation and
   policy identity. Nonempty passing gate records do not prove the complete
   required gate plan ran against this commit. Snapshot checks only detect
   changes during this invocation. They do not establish protected policy,
   artifact authenticity or durable cross-invocation authority. Align that work
   with the existing planned boundary, rather than treating this patch as v2.
2. **Dispatcher retries:** code-changing mechanical, verifier and panel retries
   still need to restart all affected acceptance checks. Earlier green results
   must not certify changed code. This was the next runtime increment at this
   handoff; see the subsequent
   [dispatcher revalidation record](../../claude-dispatcher/docs/2026-09-04-assurance-revalidation-increment.md)
   for the repair and its remaining boundaries.
3. **Remaining reviewer/recheck issues:** standalone reviewer policy and direct
   recheck paths retain audit findings. The controller rejects recheck's
   no-prior-findings shortcut when it lacks the current revision; that is a
   refusal, not a repair of the standalone shortcut. Recheck still does not
   export the next round's complete findings chain.
4. **Execution isolation:** before/after Git checks cannot detect a mutation that
   is made and reverted between checks, or prevent edits immediately afterward.
   They are not a sandbox. Final integration still needs a protected exact
   candidate and verification of the merged tree using project-specific tests.
5. **Evidence logs and deployment:** raw gate log-write failures are not all
   repaired. Existing tracked executables remain unchanged. Review, commit,
   build intended artifacts and test those exact artifacts before adoption.

## Verification

All **eight Go modules** (the seven CLIs plus the shared updater) passed
`go test -race -count=1 -cover ./...` with Go 1.26.0 on Linux/amd64. All four
modified/new modules passed `go vet ./...`; edited Go files passed formatting
checks and `git diff --check` was clean. CI module paths and dispatcher YAML
were validated. The previously documented unrelated baseline lint problems
were not repaired.

The iteration controller passed three repeated race-enabled runs; the shared
updater passed five. Tests include **16 controller CLI scenarios** using real
temporary Git repositories, plus **three actual recheck-executable scenarios**
with an offline provider: completed verification, the incomplete no-findings
shortcut, and provider failure. The original numeric-loss, metadata-loss and
failed-reviewer stale-approval regressions were observed failing before repair.

The shared updater's measured unit coverage is **81.9%**. CLI tests build child
executables; their execution is not included in the package unit-coverage
percentage. Passing these tests is not a completeness or correctness guarantee.
The six evaluation export/result-reader unit tests also passed. The pinned
Harbor Docker controls and the unchanged dispatcher Python suite were not rerun
in this increment.

Local verification outputs and hashes of the modified modules' source/config
are retained at `/var/tmp/assurance-evidence.jlTErm/verification.json`. This is
a diagnostic record of an uncommitted working tree, not protected acceptance
evidence. No live model trials, paid review panel, commits, production operations
or repository migration were performed in this increment.
