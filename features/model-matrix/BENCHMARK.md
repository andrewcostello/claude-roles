# Repeatable model studies

Status: experimental measurement tools, not production acceptance or a validated
model ranking. The 2026-09-05 repair replaces unsafe and unbound scoring. Original
numbers have not been rescored, and the old studies do not establish model
equivalence. See [the repair record](../../docs/2026-09-05-study-assurance-increment.md).

## Preserve history; start each study with separate inputs

The YAML files in this directory, and the bakeoff/dogfood task files, are
historical experiment or worklist snapshots. They contain completed, blocked,
and resumed attempts, old branches, costs, and sometimes missing pins. They
are not clean templates or permission to resume work. Do not fill missing
historical evidence with invented values.

For a new study, author a fresh specification and create a separate run manifest
outside candidate write access. Give each independent attempt a distinct task
key and invocation identity. Freeze the public brief, source, oracle, harness,
agent/runtime configuration, effort, permissions, and measurement environment
before running. Changing a frozen input creates a new study version.

The original Go wiring base resolves to the full commit
b0313fa6dad1f0cd89395feb95d0bc8689364b0e. It is not an ancestor of main; preserve
access to it through a controlled source export or retained Git ref before
pruning historical branches. A branch name or abbreviated SHA is not an
acceptable base in a new specification.

## Install and test

The tools require Python 3.12 or newer. YAML parsing has explicit dependencies;
JSON input uses only the standard library. No dispatcher checkout is imported.

~~~sh
python3 -m pip install -r features/model-matrix/requirements.txt
python3 -m unittest discover -s features/model-matrix -p 'test_*.py' -v
~~~

Execution controls use POSIX, Git, and Go. They create their own tiny repositories
and never invoke models. The repository gate and CI include these controls.

## Freeze the measurement protocol

This read-only command reports the object to put in the specification's
measurement field. It invokes only Go's version query, not candidate code.

~~~sh
python3 features/model-matrix/scorecard.py \
  --mutations /path/to/oracle.json --timeout-seconds 30 --print-protocol
~~~

The object binds the oracle and harness digests, ordered mutation IDs, module,
Go command, Go version, and Python/platform identity. Source formatting or
harness changes invalidate its digest. Keep the same object for every arm.
The mutation inventory must be nonempty, have distinct IDs, and target
production Go files through safe relative paths. Each before string must
occur exactly once in the delivered source; a missing site is not a survivor.

A new JSON or YAML specification has these fields:

| Location | Required content |
| --- | --- |
| base_branch | Full frozen Git commit ID |
| measurement | Exact object from --print-protocol |
| tasks | Nonempty list with distinct keys; explicit scaffold roles are excluded |
| each measured task | key, role, description, agent, model, effort, agent_runtime |
| agent_runtime | Explicit CLI/runtime version and configuration identity, not a provider default |

A separate run manifest retains the same task identity, public description,
pins, role, and agent_runtime, plus status and optional nonnegative cost_usd.
Each observed task's study_evidence mapping requires:

| Field | Meaning |
| --- | --- |
| base_revision | Exact authored base commit |
| subject_revision | Full committed delivery SHA; must equal the submitted checkout's HEAD |
| invocation_id | Unique identity for this provider invocation/attempt |
| agent_runtime | Must match the authored runtime/configuration identity |

The trusted operator or runner records these fields. This repository does not
yet generate provider attestations or verify a provider signature. Hash binding
detects inconsistent or stale inputs; it does not authenticate a self-authored
claim about who produced the code. Do not let the candidate write the spec,
run manifest, or score file.

## Measure trusted local code only

~~~sh
python3 features/model-matrix/scorecard.py \
  --spec /path/to/authored-spec.json \
  --run /path/to/run.json \
  --mutations /path/to/oracle.json \
  --worktree-base /path/to/deliveries \
  --json-out /path/to/new-score.json \
  --trusted-local-code
~~~

Each task KEY resolves to worktree-KEY beneath the explicit worktree parent.
Only completed, pinned arms with matching protocols are executed. Every intended
and observed arm remains in the output, including missing or invalid attempts.
Results never overwrite an existing file.

The scorer checks the submitted checkout, creates independent clones of the
exact revision, and never writes mutations into the submission. It runs two
uncached baselines and one fresh clone per mutation. A valid kill requires a
previously passing test to fail in a complete Go execution. Build/setup failures,
panics with incomplete events, skipped/empty suites, source-changing tests,
timeouts, and missing or ambiguous sites are invalid measurements, not credit.
An invalid inventory has no aggregate kill rate. Results retain raw Go JSON and
exit records so the reader can recheck the derived claims.

Each command has a maximum outer timeout of 120 seconds; the default Go limit
is 30 seconds. The timeout applies to each command, not the whole study. A
protocol needs a new version to change its timeout. Process-group cleanup does
not contain a child that deliberately detaches into another session.

**A disposable clone is not a security sandbox.** Local tests still execute
with the caller's operating-system permissions. Ambient credentials and Go/Git
configuration are not forwarded, but code can still read accessible host files
or reach the network. Use an appropriately isolated verifier for untrusted
agent output. The separate [evaluation harness](../../evals/README.md) is the
offline starting point; do not mount production secrets or the Docker socket
into candidate environments.

The legacy bakeoff score.py now delegates to this scorer with the same flags.
Its old unbound gate/mutation functions, implicit home-directory worktree default,
and --json interface are retired.

## Inspect results without inventing a ranking

~~~sh
python3 features/model-matrix/analyse.py \
  --study name:/path/to/run.json:/path/to/authored-spec.json:/path/to/new-score.json

python3 features/model-matrix/report.py \
  --run-yaml /path/to/run.json --spec /path/to/authored-spec.json \
  --score-json /path/to/new-score.json
~~~

Readers require the complete arm inventory, exact input hashes and measurement
protocol, independent invocation identities, and raw executions consistent with
each claimed outcome. They reject legacy or stale score files, mismatched
effort/runtime/revision, invalid baselines, contradictory scores, and pooled
comparisons across different briefs, bases, or mutation inventories. Exit 0
means complete, internally consistent measurements, not release acceptance.

Reports show verified observations versus total attempts, descriptive rates and
spread, and recorded cost completeness. They do not infer a winner, equivalence,
statistical significance, or safety from a small sample. Incomplete trials stay
visible rather than disappearing from denominators.

Optional --mention-files on report.py produces explicitly unverified text-search
hints. Comments count. Mentions do not prove assertions, execution, correct
behavior, or coverage. The old journal-time and unverified complexity/staticcheck
columns are retired rather than silently presented as verified evidence.

## Build better evaluations

Harvest representative product changes and historical defects, with a frozen
public contract and independently validated held-out tests. Do not hide
requirements. Validate each oracle with known-good, known-bad, always-pass,
always-fail, and infrastructure-error controls.

Keep some cases held out from workflow tuning. Repeated trials measure variation;
rerun every arm when the oracle changes. Report critical invariant failures,
false approvals, recovery, and human intervention separately from speed and cost.
A critical money-path failure must not disappear into an average of easy passes.
