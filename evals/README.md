# Workflow quality evaluations

This is the first portable evaluation slice for the proposed agent-neutral
repository. It does not replace either interactive Tasker or Dispatcher, and it
does not claim either workflow is production-safe or "high 90s."

The first case reproduces WF-02 from the workflow assessment: gate selection
could report acceptance without running required checks. It uses real source at
a pinned historical commit, not a simplified reimplementation. This is a
single-component diagnostic, not yet a comparison of complete development cycles.

## Run the offline controls

Requirements: local Docker with Linux containers, Git, and a Python virtual
environment. The harness is tested with Python 3.14.4. Harbor and its dependencies
are pinned in `requirements.lock`; task containers use an image digest.

From the workflow repository root:

```bash
python3 -m venv evals/.venv
evals/.venv/bin/python -m pip install -r evals/requirements.txt
EVAL_PARENT=$(mktemp -d /var/tmp/workflow-evals.XXXXXX)
TMPDIR="$EVAL_PARENT" evals/.venv/bin/python evals/selftest.py --output "$EVAL_PARENT/controls"
```

The command invokes only Harbor's `nop` and scripted `oracle` agents. It does
not invoke models, consume model tokens, push code, or upload results. The first
build may download a public container image. Candidate and verifier containers
have network access disabled; do not attach production credentials, data, host
directories, or the Docker socket to them.

The control suite requires all four outcomes:

| Control | Required result |
| --- | --- |
| Historical broken implementation | Rejected |
| Reference repair | Accepted |
| Always-pass implementation | Rejected |
| Always-fail implementation | Rejected |

An infrastructure error, missing result, or grader exception fails the control
suite. A zero reward alone is not evidence that a negative control worked.

Outputs include `controls.json`, per-case logs, grader reports, Harbor job/trial
configuration, and a `provenance.json` hashing the exported task inputs. Exports
refuse to overwrite an existing directory. Source repair tools can be tested
without letting an evaluated agent read or change the host checkout.

For export only:

```bash
python3 evals/prepare.py --output "$EVAL_PARENT/task"
```

After moving this directory to a neutral repository, pass
`--workflow-repo /path/to/claude-workflow`. The historical commit must remain
available there; the exporter does not silently substitute the current tree.

Fast export/result-reader tests, with no Docker or Harbor invocation:

```bash
python3 -m unittest discover -s evals -p 'test_*.py' -v
```

## Acceptance boundary

The agent receives only the case instruction and exported Go module. The
reference solution and grader are outside its container. Harbor transfers the
candidate source to a fresh verifier, which recompiles it and checks observable
CLI behavior: exit codes, state changes, gate statuses, command side effects,
and logs. Candidate processes run under a separate unprivileged UID; the grader
and reward directory are not writable by that UID.

This follows Harbor's documented [separate verifier and artifact
transfer](https://www.harborframework.com/docs/tasks) configuration. The fixture
is for local controlled evaluations, not a hardened service for hostile uploads.
Do not treat container isolation as permission to expose sensitive information.

## Next increments

1. Add the remaining historical regressions and representative changes. Agree
   the expected financial/game behavior with a human domain expert before using
   it as an oracle. Keep some cases held out from workflow tuning.
2. Introduce a small shared acceptance contract binding results to the exact
   source revision, complete changed-path manifest, policy version, and command
   invocation. Any code-changing retry invalidates earlier acceptance evidence.
3. Connect complete workflow configurations and a simpler single-agent baseline.
   Pin agent/runtime/model versions, effort, permissions, prompts, and toolchain;
   record human interventions and retries. Native Codex automation is available
   through the [Codex SDK](https://learn.chatgpt.com/docs/codex-sdk), but no custom
   SDK adapter or live model integration is implemented here yet.
4. Run repeated trials with identical starting snapshots and workload budgets.
   Report per-case correctness, critical escapes, false approvals, recovery,
   human intervention, latency, and cost separately. Do not average a critical
   escape away with easy-case successes or drop errored trials from reports.

Before live model trials, configure and test the provider's scoped credential
and network route, pin its runtime, and approve the intended run matrix. The
offline task intentionally cannot reach a provider API. Do not solve that by
giving every task unrestricted network access or mounting the host's agent
configuration. Both production workflows retain strong default assurance;
sandbox mode remains a separate explicit opt-in policy, not an inference from
interactive or loosely specified work.
