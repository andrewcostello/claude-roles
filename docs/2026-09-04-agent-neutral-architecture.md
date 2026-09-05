# Agent-neutral workflow architecture recommendation

Date: 2026-09-04. Status: recommendation, not an implementation or migration.

Work has begun with [assurance repairs and a portable evaluation pilot](2026-09-04-assurance-first-increment.md).
The repository consolidation described below has not happened.

## Decision

Create one modular, agent-neutral repository containing **two distinct
workflows and one shared quality foundation**. Keep independently usable Tasker
and dispatcher entry points. Do not merge them into one mandatory lifecycle,
and do not create separate copies for every programming language or model.

**Both workflows build real systems and should default to strong engineering
assurance.** Tasker is not the lax workflow. Add a separate, explicitly invoked
experimental profile for the occasions when lighter ceremony is useful.

This recommendation follows the operator's clarification, not merely the
repositories' existing names:

| | Interactive engineering / Tasker | Contract-led construction / dispatcher |
|---|---|---|
| Typical work | Real systems and changes with evolving requirements; also creative/feel-based work and explicitly chosen prototypes | Larger services/products with reviewed PRD, data model, scaffold, and contracts |
| Primary objective | Build correct software while resolving intent and design collaboratively | Execute a dependency graph faithfully with sustained supervised autonomy |
| Specification | May evolve through conversation; not every session needs a formal PRD | Versioned planning baseline; deviations are explicit and adjudicated |
| Human involvement | Frequent collaboration and judgment | Planned reviews, monitoring, escalated decisions, and product testing |
| Successful output | Verified implementation; or explicitly experimental learning/artifacts when that profile was selected | Assembled implementation with evidence, ready for the next testing stage |
| Default quality bar | Full risk-appropriate engineering assurance | The same full risk-appropriate engineering assurance |

Neither is simply the small or large version of the other. Interactive work
need not end in dispatch. A well-scoped interactive change can go straight through
normal change acceptance without acquiring a large-project task graph.

## What is shared, and what remains different

Share the rules that define trustworthy evidence and accepted code:

- Risk and domain policies, severity meanings, and required human decisions.
- Classification, gate planning/execution, review reduction, and evidence
  validation. One decision implementation per question, not two translations.
- Language-specific discovery, test commands, dependency analysis, and artifacts.
- Versioned schemas for requirements, subjects, findings, gate results, and
  approval. Bind evidence to exact source/base and policy/tool identities.
- Provider invocation, capability checks, model/effort pins, isolation, and
  error handling. Provider success is not code-quality approval.
- Regression suites, representative model evaluations, and acceptance oracles.

Keep these workflow-specific:

- How intent is discovered, planning is developed, and questions are answered.
  Interactive work need not have a dispatch-ready PRD, but production-bound
  acceptance criteria and critical assumptions still need to be established.
- Interactive coaching versus dependency scheduling, parallel workers, retries,
  crash recovery, and supervised long-running execution.
- Stage readiness: an experiment, a scaffold, an independent test contract, an
  implemented component, and a testing candidate are different outputs.

Use the shared quality machinery from each flow; do not run the interactive
Tasker orchestration loop inside dispatcher workers. Preserve the dispatcher's
existing single-orchestrator ownership rule.

## Separate execution mode from assurance, risk, and stage

These are independent dimensions:

- **Execution mode:** interactive engineering or contract-led dispatch.
- **Assurance profile:** full engineering by default; sandbox experimentation
  only when the operator explicitly selects it.
- **Risk:** the actual affected financial, game, security, data, or presentation
  surfaces, regardless of ticket size or the model used.
- **Stage:** disposable experiment, construction checkpoint, accepted change,
  or candidate for the product testing/release process.

The normal profile retains requirements clarification, risk classification,
design/contract review where appropriate, independent verification, meaningful
tests, review, and current-revision evidence. Uncertainty changes how design
decisions are reached; it does not justify quietly dropping those protections.

The optional experimental profile should be a deliberate invocation, not an
inference from words such as "vibe", "quick", or "prototype". It can shorten
planning/reporting ceremony and defer full acceptance activities while trying
disposable alternatives. Capture the important assumptions and limitations,
use synthetic data and isolated environments, and withhold production/payment
credentials and release authority. Label the output experimental/unverified.

Keeping or promoting experimental code requires explicit re-entry into normal
assurance: classify the actual change, establish its required behavior, and run
the applicable tests and reviews against the current revision. No old informal
approval or skipped gate carries over as a pass. The implementation may be
reused, but it has not earned acceptance merely by looking promising.

Full assurance does not mean every small change needs a large-project PRD or
the same amount of documentation. Scale artifacts to the affected risk and
behavior, while retaining the meaning of verified and accepted.

Intermediate dispatch checkpoints may legitimately contain stubs or planned-red
acceptance tests. Name that state accurately and scope allowances to the
relevant role/task/phase. They are not ordinary passing implementation tests.
The operator's development branch is not itself production; final product
testing and release authority remain separate.

## Preserve and strengthen the construction handoff

The operator already does valuable work before dispatch. Make its outputs
usable throughout the run, rather than creating more planning ceremony:

1. Version the PRD, acceptance requirements, assumptions, and decisions.
2. Retain the reviewed data model and actual load-test workload/results,
   including invariants under failure and concurrency, not just throughput.
3. Name the scaffold and contract baseline: interfaces, schemas, error
   semantics, idempotency, money representation, and compatibility expectations.
4. Link critical logic/state flows to those contracts and independent tests.
5. Dispatch against that baseline, with explicit ownership and readiness edges.
6. Treat a substantive deviation as a decision: pause affected tasks, obtain
   adjudication, version the amendment, revalidate affected evidence, then resume.

Prioritize critical foundations in the task graph. Before fanning out dependent
work, review and verify the critical component's promised readiness. The
milestone need not mean the whole service is complete.

For critical flows, documentation should identify states, transitions, guards,
durable writes, external effects, retries, recovery, and invariants. Connect it
to executable checks. Generating a large amount of prose after implementation
does not by itself verify the design or its behavior.

The supervising agent is an observer/operator within declared authority, not
an alternative acceptance engine. It can monitor, diagnose, pause, retry approved
transient failures, and elevate decisions. Contract changes, risk downgrades,
critical waivers, and release approval must retain their designated owners.

## Language support: profiles, not workflow forks

Build first-class Go, TypeScript, and Python profiles. Leave Java optional
until a real project requires it. Use the same acceptance meanings across
profiles; specialize discovery, tooling, failure modes, and test strategies.

| Profile | Specific assurance to provide |
|---|---|
| Go | Module/workspace and downstream-consumer discovery; build/vet/static checks; race-enabled tests; fuzz/property tests; cancellation, goroutine/resource lifecycle, and real database concurrency checks |
| TypeScript | Package/project-reference discovery; explicit type checking alongside builds; strict null/index handling; runtime boundary validation; asynchronous failure tests; API/browser integration where relevant; money serialization and precision tests |
| Python | Package discovery; configured static typing, lint/format checks, and pytest; stateful/property tests; runtime input validation; Decimal/integer money semantics with explicit precision/rounding; real database/service integration |

Go has native coverage-guided fuzzing and a race detector; the latter only
observes exercised runtime races, so it does not replace database/distributed
concurrency tests. See [Go fuzzing](https://go.dev/doc/security/fuzz/) and
[the race detector](https://go.dev/doc/articles/race_detector).

For TypeScript, make the stricter settings explicit and test upgrades rather
than assuming all safety options are included in one flag. See
[`strict`](https://www.typescriptlang.org/tsconfig/strict) and
[`noUncheckedIndexedAccess`](https://www.typescriptlang.org/tsconfig/noUncheckedIndexedAccess.html).

For Python, Hypothesis supports generated action sequences against stateful
systems; `Decimal` supports configurable precision, rounding, and exception
traps. These are useful mechanisms, not automatic correctness guarantees. See
[stateful testing](https://hypothesis.readthedocs.io/en/latest/stateful.html) and
[`decimal`](https://docs.python.org/3/library/decimal.html).

Cross-language contracts need shared test vectors for serialization, currency
scale, rounding, bounds, nullability, timestamps, idempotency keys, and error
semantics. SQL, migrations, API schemas, generated code, and dependency manifests
must trigger the relevant consumers' checks, not disappear between profiles.

Each profile should specify capabilities, pinned toolchains, commands,
applicability, evidence parsing, and what unavailable/failed/not-applicable
mean. Critical acceptance must not fall back silently to a weaker profile.
Test every advertised language/provider/mode combination; an adapter's existence
alone does not establish support.

## Repository shape

Illustrative boundaries, not a requirement to move every file immediately:

```text
engineering-workflows/
  workflows/
    interactive/         # Full-strength Tasker; evolving requirements
    dispatch/            # Contract-led graph, recovery, milestones, supervision
  quality/               # Shared classifier, gates, verdict and evidence logic
  contracts/             # Versioned wire schemas and compatibility fixtures
  policies/              # Risk, domain rules, normal and explicit sandbox profiles
  languages/
    go/
    typescript/
    python/
  providers/             # Agent-specific invocation and instruction adapters
  integrations/          # Git/PR systems, notifications, issue trackers
  evals/                 # Regression, interactive, dispatch, and sandbox checks
  docs/
```

Do not rewrite Python orchestration into Go, or Go quality tools into Python,
merely to achieve neutrality. Preserve suitable implementations, repair them,
and share authoritative behavior through a versioned library/CLI boundary.
Avoid separately implemented policy reducers behind identical-looking schemas.

Product-specific paths, credentials, channels, routing preferences, and
jurisdiction-specific requirements belong in project configuration or policy
packs, not the neutral core. Keep runtime journals and generated reports outside
authored task definitions. Phase 14 of the dispatcher already proposes the
appropriate separation and journal-backed reconstruction.

## Why one repository

The strongest reason is coordinated changes, not convenience of installation.
These repositories already share classifier assumptions and concepts but have
different effective HIGH-finding acceptance rules. A policy fix should change
the producer, both consumers, their tests, and relevant instructions together.

A single repository makes those changes reviewable as one unit and permits
end-to-end compatibility tests. Components can still be installed or released
separately, so using only the Tasker need not require the dispatcher runtime.

Separate repositories would be preferable if there were genuinely different
owners, access controls, independent release obligations, or unrelated users.
None of those requirements has been stated. If one emerges, split on package
boundaries with versioned dependencies; do not return to copy/pasted policy.

## Migration sequence

1. Preserve both current repositories and the passing baseline. Record these
   audit findings and convert confirmed defects into durable failing safety
   tests when implementing repairs.
2. Define the shared acceptance/evidence contract and the distinct workflow
   stage semantics. Reconcile the existing classification/gating redesign;
   do not start a competing boundary specification from scratch.
3. Repair file preservation, stale evidence, malformed classification, panel
   acceptance, and review subject binding before trusting automated promotion.
4. Import existing components with history where practical. Keep compatibility
   entry points while removing host-specific sibling-path discovery.
5. Implement and test Go/TypeScript/Python profiles against representative
   projects. Preserve provider-specific behavior behind explicit adapters.
6. Pilot both workflows: one real interactive change with evolving requirements
   and one contract-led multi-component service. Separately verify explicit
   sandbox opt-in and promotion back to normal assurance. Include critical
   money/state-machine behavior, concurrent work, failures, resume, human
   escalation, and assembled-tree tests.
7. Cut over consumers only after behavior and evidence are verified; retain a
   rollback path. Freeze old repos to migration pointers once they stop being
   independent sources of truth.

Do not change repository layout, orchestrator language, provider, model, prompts,
and acceptance policy all at once. Keep comparisons attributable. Evaluate the
interactive flow for useful learning, human control, and verified implementation;
evaluate dispatch for contract fidelity, integration correctness, and recovery.
Measure defect escape and assurance integrity in both.
Neither elapsed time nor reviewer agreement alone is the quality objective.

## First implementation slice

The next practical change should be a small shared acceptance boundary with
negative end-to-end tests, plus the most urgent data-preservation fixes—not a
large folder rename. Make it impossible to call changed, incompletely reviewed,
or malformed-evidence work verified. Both workflows can then adopt that boundary
without losing the interaction model that makes each useful.
