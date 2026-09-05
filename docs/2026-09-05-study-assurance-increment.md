# Study scoring repair and unpublished-history disposition

Date: 2026-09-05. This is an implementation and verification record, not
production acceptance or a model ranking. Both development workflows remain
separate. No dispatcher runtime or staged boundary package was changed here.

## Why this increment exists

The review covered the 30 commits from `bd832823` through `3e225a7`, preceding
the newer assurance commits `143a6af` and `c83a0ba`. They touch 23 paths without
changing Go runtime code. The recommendation was to retain the full history
and correct the current scoring tools and claims before pushing.

Counterexamples against the committed `c83a0ba` tree established:

| Original problem | Observed result |
| --- | --- |
| Passing test followed by failing TestMain | Go exited 1; old scorecard reported green |
| Undefined identifier introduced into assertion-free suite | Old scorecard credited 1/1 mutation kills |
| Comments without tests or assertions | Old report counted 12/12 clauses as coverage |
| Missing authored spec or mismatched effort | Old analyser accepted provenance |
| Correctly pinned completed arm | Old bakeoff scorer excluded it, then reported a model substitution |

Source inspection also found in-place mutation/restoration of submitted
worktrees, task-name-only score joins, and historical run state mixed into
purported reusable inputs. A limited credential-shape scan of the 55 distinct
changed blobs found no matching private-key headers, provider-token patterns,
or credential-bearing HTTP URLs; that was not a comprehensive secret audit.

## Implemented corrections

- All score consumers share explicit specification/run parsing and complete
  arm selection. Completed arms are retained; missing, extra, incomplete,
  duplicate, or unverified data does not silently become a successful study.
- Author intent is separate from run metadata. Agent, model, effort, public
  brief, role, runtime/configuration identity, full base and delivery revisions,
  and unique invocation identities are checked. Each score binds the exact
  specification/run hashes and measurement protocol.
- Mutation observations execute in independent Git clones of the committed
  delivery. The harness does not rewrite or restore submitted source. Dirty or
  moved submissions, unsafe paths, symlinks/submodules, ambiguous/missing sites,
  and source changes made by tests are refused.
- Two uncached green baselines with the same test inventory are required.
  Process exits, package completion, and package-qualified test identities are
  checked. Build/setup failures, incomplete events, empty/skipped suites, and
  timeouts cannot earn mutation credit. Invalid inventories get no aggregate
  kill rate.
- Raw Go JSON and exit records accompany results. Readers reparse them and
  reject contradictions between the claimed baseline, inventory, mutation
  outcome, rate, and execution records. Different briefs, bases, or measurement
  inventories are not pooled as one comparable task.
- Commands use bounded process groups, disposable local state, and explicit
  local-code trust. Provider credentials and ambient Go/Git configuration are
  not forwarded. The caller must still isolate untrusted code separately.
- Reports show descriptive observations, incomplete-trial denominators, and
  recorded cost completeness. Keyword searches are explicitly unverified clause
  mentions, never behavioral coverage or a ranking. Unverified journal-time,
  complexity, and staticcheck columns are retired.
- The absolute dispatcher import dependency is removed. YAML dependencies are
  explicit; JSON parsing is standard-library-only. Generated Python bytecode
  was removed from the tracked tip and ignored; Git history retains the old
  file, so no history rewrite or loss of source was required.
- Historical model-routing statements are marked provisional. The wallet
  proposal now requires public requirements, versioned held-out tests, oracle
  controls, and separate critical-invariant outcomes. Hidden tests are not
  described as immune to gaming or guaranteed to discriminate.
- Comment guidance preserves useful specification traceability and requires
  ownership coordination for paired-package edits. Directive changes remain
  forbidden in cleanup work, but can be deliberately reviewed as behavior
  changes in implementation tasks.

The archived task YAMLs were not rewritten, repinned, resumed, or converted into
invented successful runs. See [the study guide](../features/model-matrix/BENCHMARK.md)
for the separate authored-spec/run-manifest contract for future experiments.

## Shared-contract deviation

- **kind:** shared-contract, limited to experimental study tools and their
  readers/legacy bakeoff entry point.
- **original:** unbound score rows, mutable worktree scoring, implicit local
  imports/paths, and statistical or coverage claims stronger than the evidence.
- **changed:** score schema version 2, explicit immutable-input bindings,
  complete raw-execution validation, isolated source copies, and refusal of
  unverifiable legacy scores. The legacy bakeoff entry point delegates to the
  same scorer; old helper APIs and CLI arguments are retired rather than
  retaining an unsafe alternate path.
- **reason:** prevent demonstrated false-green and attribution errors before
  these experiments inform higher-autonomy or money-path decisions.
- **blast radius:** study scripts and documented commands; Python tests now run
  in the repository gate and CI. Existing snapshots remain historical data,
  not valid new-study manifests or production model-routing evidence.
- **disposition:** repair scope authorized by the user after review. Mechanical
  self-review and offline controls only; no independent cross-family panel or
  formal scaffold/seals/bodies/adjudicate run is claimed. Production adoption
  and provider-evidence authenticity remain outside this increment.

## Baseline CI repair

The unrelated formatting failures in deepseek/repro were formatting-only edits.
Two deepseek HTTP tests now use cancellable contexts available under the
module's declared Go version; no module requirement or tracked binary changed.
The repository gate now runs formatting, vet, uncached race tests, and both
Python suites. GitHub Actions also exercises the Python tools on 3.12 and 3.14.

## Verification scope

Local development checks passed all 47 study-tool tests using Go 1.26.0,
Python 3.14.4, and the pinned YAML parser. The tests include real Go executions and independent Git fixtures,
command-line scoring through the bakeoff entry point, stale/contradictory
execution records, every binding field, missing/duplicate arms, mixed briefs,
invalid mutants, timeout/process cleanup, and concurrent-edit preservation.

The Go module gate and six evaluation export/result-reader tests were also
rerun. Final verification must be repeated from the committed tree before
pushing; a dirty development-tree result is not committed-tree evidence.
The earlier Docker/Harbor controls and live model studies were not rerun here.
No paid model calls, deployments, production data, or agent-neutral repository
consolidation were involved.

## Limits that remain explicit

Hash consistency is not provider authentication. A trusted operator/runner must
record the actual invocation metadata and protect the spec, run manifest, and
score output from candidate writes. These tools do not issue signed provider
attestations or prove the declared actor performed the work.

Disposable clones protect submissions from the harness's mutation writes, not
the host from hostile tests. Local code retains the caller's OS permissions
and can reach the network or accessible host files. A deliberately detached
child can escape process-group cleanup. Use an isolated verifier for untrusted
agent output; do not supply production credentials or sensitive mounts.

A mutation score does not isolate the new arm's contribution from pre-existing
tests, establish complete behavioral coverage, or authorize release. Historical
model claims have not been validated by rescoring. Representative held-out
domain oracles, repeated trials, human domain review, and protected integrated
release evidence are still required before stronger production autonomy.
