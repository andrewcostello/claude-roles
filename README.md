# claude-workflow

Shared Claude agent role definitions and dispatcher-friendly workflow skills for the full development lifecycle — from task breakdown through implementation, review, and PR feedback.

```
claude-workflow/
├── roles/    # Agent role definitions — read by Claude as "be this role"
├── skills/   # Composable workflow skills the Tasker loads on demand
├── cmd/      # Go orchestrators — deterministic control flow and verdicts
├── shared/   # Common state persistence used by the Go tools
├── config/   # Shared configuration, including the risk-path rule table
├── evals/    # Portable, independently graded workflow regression cases
└── docs/     # Supporting documentation
```

Quality improvement work: [acceptance-evidence increment](docs/2026-09-04-assurance-evidence-increment.md),
[first increment](docs/2026-09-04-assurance-first-increment.md)
and [the offline evaluation pilot](evals/README.md).

**The division of labour:** `cmd/` owns control flow and truth — what runs, in what order, whether it passed, what runs next. `roles/` and `skills/` own discovery and judgment — reading code, finding defects, weighing designs. A decision that is a glob lookup, a regex scan, an exit code, or arithmetic belongs in `cmd/`; a decision that requires reading the code belongs in a role. When a role file recites a number or a path list that `cmd/` also knows, the two drift and the role loses.

## Orchestrators (`cmd/`)

Each CLI has its own Go module. The state writers depend on `shared/statefile`
through a local module replacement; keep the repository layout intact when
building. There are no third-party Go dependencies. Build into a separate output
directory, for example `go build -o /var/tmp/workflow-build/gates .` from
`cmd/gates` after creating that output directory, so tracked binaries stay untouched.

Run-state writers use a fail-fast sidecar lock and atomic replacement. Do not run
old binaries or scripts that rewrite the same state concurrently with these
writers. A leftover `<run-state>.lock` requires operator inspection: confirm no
writer is active before removing it; age alone is not proof. An I/O error is a
failed operation even when replacement happened before a later sync failed.

`iterate run` retains each attempt's output in a fresh private subdirectory of
`-findings-dir` (by default, beside the run state). Store these artifacts outside
the tracked worktree. Reviews require a clean, committed, freshly classified
revision; changed or incomplete evidence must not be reused. Terminal decisions
do not append synthetic review rounds, and `-dry-run` never writes state.

| Binary | Owns | Exit codes |
|---|---|---|
| `cmd/classify` | Diff → risk tier, component presets, **panel preset (solo/standard/full/deep, graded by risk AND production diff size)**, financial/migration flags, flow-diagram request, human-PR-gate decision, skill routing, and the argv for `cmd/reviewer`. `-panel <preset>` overrides at or above the computed floor; below-floor overrides are rejected. Resolves `base_ref` once for the whole run and rejects a stale local base. Writes the run state. | 0 classified, 3 INVALID_INPUT |
| `cmd/gates` | Deterministic verification gates: build, test, coverage, lint, complexity, gosec, staticcheck, semgrep, mutation, benchmarks. Derives the gate set from the classification, runs each **per Go module**, writes raw output to disk and status into the run state. A gate that cannot run FAILS unless waived by name with a reason. | 0 all pass, 1 a gate failed or could not run, 3 INVALID_INPUT |
| `cmd/reviewer` | Parallel review panel: seat dispatch, per-scout role slicing, finding dedup, tier consensus floors, component dimension floors, merged verdict, `-findings-out` JSON | 0 complete, 2 REVIEW_UNAVAILABLE, 3 INVALID_INPUT |
| `cmd/recheck` | Rounds 3+ targeted verification: verifies each prior at-or-above-floor finding as RESOLVED/STILL_OPEN/REGRESSED, hunts new findings in changed files only, computes the convergence verdict mechanically | 0 APPROVE, 1 ITERATE, 2 ESCALATE |
| `cmd/repro` | Stack preflight and repeated-run bug reproduction with a JIRA-ready report | 0 reproduced / verdict per flags |
| `cmd/deepseek` | DeepSeek provider shim used by the `deepseek-scouts` seat | — |

Typical front of a run:

```bash
RUN_STATE=/tmp/run-$TASK_KEY.json

# 1. Classify — risk, components, panel shape, gate flags, reviewer argv
git -C "$WORKTREE" diff origin/main...HEAD | ~/Project/claude-workflow/cmd/classify/classify \
  -worktree "$WORKTREE" -base origin/main -task "$TASK_KEY" -out "$RUN_STATE"

# 2. Gates — deterministic checks, per module, before spending reviewer tokens
~/Project/claude-workflow/cmd/gates/gates -run-state "$RUN_STATE" || exit 1

# 3. Panel — argv comes from the classification; never hand-assemble -risk / -component
git -C "$WORKTREE" diff origin/main...HEAD | ~/Project/claude-workflow/cmd/reviewer/main \
  $(jq -r '.classification.reviewer_args | join(" ")' "$RUN_STATE") \
  -findings-out "/tmp/findings-$TASK_KEY-r1.json"
```

Note `cmd/gates` runs each gate **inside the owning module**. This repo and `evenplay-mono` both have no root `go.mod`, so `go build ./...` from the repo root fails outright — gates discovers the module for every changed file and sets `cwd` accordingly. That is also why the binaries above are built per directory (`cd cmd/gates && go build -o gates .`).

Configuration:

| File | Owns |
|---|---|
| `config/risk-paths.json` | Path → risk/component/financial/presentation rules, per-rule `panel_floor` pins (e.g. client money UI floors at `full` despite medium risk), plus the gate-signal patterns that revoke the reduced-panel carve-out |
| `config/gates.json` | Gate commands, triggers, scopes, timeouts, coverage floors and exemptions, mutation/benchmark thresholds |
| `config/run-state.schema.json` | The run-state contract and which node owns which field |

## Roles (`roles/`)

| File | Role | Purpose |
|------|------|---------|
| `roles/tasker.md` | Orchestrator | Breaks down work, dispatches agents, manages review cycles. Now a router that loads skills/ as needed. |
| `roles/design-agent.md` | Design Agent | Produces 2-3 competing designs for Tasker to select — mandatory for Critical/High risk |
| `roles/coder.md` | Implementation Agent | Writes tested code against an approved design spec |
| `roles/verification-agent.md` | Verification Agent | Independent ground-truth runner — fresh-checkout build/test/lint/static-analysis/mutation/bench with no Coder context. Gates Critical review before any reviewer runs. |
| `roles/bug-reproducer.md` | Bug Reproducer | Investigation phase before root cause is confirmed. Pulls a ticket (FSG/SMG/customer report), writes a cross-app harness spec to reproduce the reported behavior, files/updates the SMG ticket with findings, opens a PR. |
| `roles/regression-test-author.md` | Regression Test Author | Independent test author for confirmed user-reported bugs at Medium/High risk. Two phases on the same agent. Black-box only — refuses to write unit tests at the layer the Coder's TDD covers. |
| `roles/reviewer.md` | Code Reviewer | 8-dimension review with data flow tracing, dedicated test quality audit, design coherence checks, and focused sub-agents |
| `roles/security-linter.md` | Security Auditor | Focused SQL injection, PII exposure, integer overflow, and auth bypass audit with severity grading — gates Critical review |
| `roles/pr-reviewer.md` | PR Reviewer | Interactive PR review: 8-dimension analysis, severity calibration with human reviewer, medium+ issue walkthrough with teaching, PR tour, and combined human+agent summary comment |
| `roles/pr-responder.md` | PR Responder | Triages and responds to PR review comments — fixes valid issues, replies with evidence, reports summary to human |
| `roles/standup-reporter.md` | Standup Reporter | Generates daily engineering standup from JIRA + GitHub — Priority Watch, team status, PR attention list, P0 tracker, publishes to Confluence |
| `roles/release-notes-generator.md` | Release Notes Generator | Produces structured release notes from merged PRs and JIRA tickets — categorised by feature, fix, and breaking change |

## Skills (`skills/`)

Composable workflow primitives the Tasker loads on demand. Each skill is self-contained, ≤ 200 lines, with a clear scope statement. The Tasker's skill-routing table maps trigger conditions to skills.

| File | When Loaded | Owns |
|------|-------------|------|
| `skills/critical-review-dispatch.md` | Risk = Critical or High | Design Agent dispatch, Verification Agent, Security Linter, Static Analysis Gate, 4-reviewer parallel panel (Claude + Codex + Gemini + Grok), mid-flight retry, component-specific dimension floors rationale |
| `skills/migration-checklist.md` | Task touches DB schema | Filename convention, PK type rules, idempotency + FK indexes + NOT NULL splits |
| `skills/bug-fix-protocol.md` | `type: Fix` | RED-first protocol, Regression Test Author dispatch criteria |
| `skills/git-worktree-setup.md` | First dispatch on a ticket | Branch naming, container vs host paths, one-ticket-one-worktree |
| `skills/pr-raise.md` | Verdict = APPROVE | Human PR gate (Critical OR financial-paths-touched), title/body format, size gates, no-attribution rule |
| `skills/plan-based-execution.md` | `docs/plans/*.md` exists | Plan-based dispatch with batch checkpoints |
| `skills/iteration-protocol.md` | Verdict = ITERATE | Targeted re-review scope, full domain test gate, iteration cap |
| `skills/explicit-state.md` | Task touches a gate, guard, verdict or auth check | The no-implicit-states constraint: absence must be a named state, never a default or a falsy value |
| `skills/review-language.md` | Every review and PR comment | Plain-English contract for an ESL team: ≤ 15-word sentences, no idioms, imperative fixes, the What-is-wrong / What-happens / What-to-do finding template |

## Other directories

| Path | Purpose |
|------|---------|
| `config/team-config.yaml` | Team roster, JIRA/GitHub settings, tracked epics, P0 tickets, known binaries — single source of truth for project-specific config used by standup-reporter and release-notes-generator |
| `docs/` | Supporting documentation, migration notes, examples |

---

## Prerequisites

### Panel presets

`cmd/classify` grades the panel by risk tier AND production diff size (tests and generated files do not count), and emits the seat list as `-reviewers` — never hand-assemble it:

| Preset | Seats | Typical trigger |
|--------|-------|-----------------|
| `solo` | claude | client-only presentation (the old carve-out, unchanged) |
| `standard` | claude, codex | Low/Medium risk, ≤ 400 production lines, no blockers |
| `full` | + claude-scouts, grok | High risk, gate signals, migrations, components, or Medium at size |
| `deep` | + grok-scouts | Critical risk, financial paths, scaffold config, unmatched paths, or a full floor at > 400 lines |

The floor (money, Critical, gate signals, …) can never be overridden downward. `classify -panel <preset>` records a human's explicit choice at or above the floor; the report always prints recommendation, floor, and how to override.

### Review seats

`cmd/reviewer` dispatches the seats classify names (its `-reviewers` default is `claude,claude-scouts,codex,grok`):

| Seat | Model | Transport | Setup |
|------|-------|-----------|--------|
| **claude** | Claude — broad, full-context | `claude` CLI | Built-in |
| **claude-scouts** | Claude — 8 parallel dimension scouts, fanned out and reduced to one verdict | `claude` CLI | Built-in |
| **codex** | Codex | `codex` CLI | Codex CLI + OpenAI auth |
| **grok** | Grok | `grok` CLI | [Grok CLI](https://grok.com) on `PATH`, `grok login` once per host |
| **agy** | Gemini | `agy` CLI | Antigravity / Gemini credentials. **In no preset** — pass `-reviewers ...,agy` to dispatch it. Removed 2026-08-18: 2-4 findings per run across four runs against 8-47 for its siblings, and 0/2 verified precision on the one hand-checked run. |

Optional additional seats: `deepseek-scouts`, `kimi`.

**Consensus floors are computed, not recited** — `consensusFloor()` in `cmd/reviewer/main.go` scales the floor with panel size and risk tier: Critical demands the full panel, High tolerates one absence (`ceil(3N/4)`, min 2), Medium a majority, Low one. Completed reviews below the floor produce `REVIEW_UNAVAILABLE` (exit 2), not a verdict. Do not hardcode floor numbers in role files; read them from the CLI's output.

Shared review tooling:

```bash
sudo apt update
sudo apt install ripgrep
```

`rg` is used for sibling-surface traces before and during reviewer dispatch.

---

## Setup

### 1. Add as git submodule

```bash
# New project
git submodule add https://github.com/andrewcostello/claude-workflow.git .claude/workflow

# Project that already has .claude/
git submodule add https://github.com/andrewcostello/claude-workflow.git .claude/workflow
```

After cloning a project that already uses this submodule:

```bash
git submodule update --init --recursive
```

> **Migrating from claude-roles?** The repo was renamed from `claude-roles` to `claude-workflow` and restructured to add a `skills/` sibling directory. Update your `.gitmodules` to point at the new URL (GitHub redirects from the old URL automatically), rename the local checkout from `.claude/roles/` to `.claude/workflow/`, and update any project CLAUDE.md / scripts that reference the old `.claude/roles/<role>.md` paths to the new `.claude/workflow/roles/<role>.md` form.

### 2. Add to CLAUDE.md

Paste this block into your project's `CLAUDE.md`. Fill in the project-specific commands for your stack.

```markdown
## Agent Workflow

Role definitions live in `.claude/workflow/roles/`. For non-trivial tasks, use the three-agent
workflow:

### How Claude Uses These Roles

**To start a task as Tasker:**
Read `.claude/workflow/roles/tasker.md` and adopt the Tasker role.

**To dispatch the Coder (subagent):**
Create a general-purpose subagent with this prompt:
"Read `.claude/workflow/roles/coder.md` for your role instructions, then implement: [Task Assignment]"

**To dispatch Reviewer A (subagent):**
Create a general-purpose subagent with this prompt:
"Read `.claude/workflow/roles/reviewer.md` for your role instructions, then review: [Review Request]"

**To dispatch Reviewer B (Codex CLI):**
Write the shared Review Request prompt to a file, then from the review worktree run:
`codex exec -C "$(pwd)" -s read-only --ephemeral -o "/tmp/review-codex-$TASK_ID.md" - < "/tmp/review-request-$TASK_ID.md"`

**To dispatch Reviewer C (Gemini / agy CLI):**
Write the shared Review Request prompt to a file, then:
`agy --print-timeout 15m --print "$(cat /tmp/review-request-$TASK_ID.md)"`

**To dispatch Reviewer D (Grok CLI):**
Same prompt file as C, then from the review worktree run:
`grok --prompt-file /tmp/review-request-$TASK_ID.md --cwd "$(pwd)" --always-approve --tools "read_file,grep,list_dir,run_terminal_cmd" --disallowed-tools "search_replace,write" --max-turns 50 --reasoning-effort high --output-format plain`
Full dispatch mechanics (and multiline form) live in `.claude/workflow/skills/critical-review-dispatch.md`.

**To dispatch the Design Agent (subagent — Critical/High risk):**
Create a general-purpose subagent with this prompt:
"Read `.claude/workflow/roles/design-agent.md` for your role instructions, then produce designs for: [Task Assignment]"

**To dispatch the Security Linter (subagent — Critical risk gate):**
Create a general-purpose subagent with this prompt:
"Read `.claude/workflow/roles/security-linter.md` for your role instructions, then audit: [file list]"

### Project Commands

The Coder role uses placeholders — replace these here so subagents know the actual commands:

- **Test:** `[YOUR TEST COMMAND]`       e.g. `go test -race -cover ./...`
- **Lint:** `[YOUR LINT COMMAND]`       e.g. `golangci-lint run`
- **Complexity:** `[YOUR COMPLEXITY COMMAND]` e.g. `go-complexity-lint ./...`

### Risk Classification

Do not classify by hand — run `cmd/classify`, which derives the tier from `config/risk-paths.json`:

| Risk | Applies To | Review Depth |
|------|-----------|--------------|
| Critical | Balance mutations, bet settlement, payout calculations, outcome determination, withdrawals | 5-seat panel (full consensus) + Verification Agent + Security Linter + Design Agent |
| High | Auth, session, state machines, audit trail, ledger, migrations, wire contracts | 5-seat panel (N−1 consensus) + Static Analysis Gate + Design Agent |
| Medium | Repositories, validation, queries, read-only, dependency bumps | 5-seat panel (majority consensus) |
| Low | Client-only presentation, docs, test helpers | Reduced single-reviewer panel — never zero |

Risk is the MAX over every rule matched by every changed file, so adding a rule can only raise a tier. A path no rule covers fails closed to High. When in doubt, go one level higher — and add a rule so the next run doesn't need judgment.
```

### 3. Configure team settings

Edit `config/team-config.yaml` with your project-specific values:
- **Team roster** — add/remove team members with their GitHub and JIRA usernames
- **JIRA** — your instance URL, email, project keys
- **GitHub repos** — repos to scan for commits and PRs
- **Binaries** — known binaries with source paths (used by release notes)
- **Tracked epics** — epic keys for the weekly project health report
- **P0 tickets** — security/critical tickets to track in standups

This is the single source of truth for project-specific config. The standup reporter and release notes generator read from this file — update it here, not in the role files.

### 4. Configure project commands

The Coder and Verification Agent roles reference these placeholders. Define each in your project's `CLAUDE.md`:

| Placeholder | Common Go value | Common TS value |
|-------------|-----------------|-----------------|
| `[project build command]` | `go build ./...` | `nx build [app]` |
| `[project test command]` | `go test -race -cover ./...` | `nx test [app]` or `vitest run` |
| `[project lint command]` | `golangci-lint run` | `nx lint [app]` or `eslint src/` |
| `[project complexity command]` | `go-complexity-lint ./...` | ESLint complexity rules |
| `[project semgrep rules path]` | `tools/semgrep/rules.yml` | same |

Install the supporting tools:

```bash
# Complexity linter
go install github.com/glemzurg/go-complexity-lint/cmd/go-complexity-lint@latest

# Static analysis (Critical/High pre-review gate)
go install github.com/securego/gosec/v2/cmd/gosec@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
# semgrep: pipx install semgrep   OR   brew install semgrep

# Mutation testing (Critical financial code only)
go install github.com/go-gremlins/gremlins/cmd/gremlins@latest

# Benchmark comparison
go install golang.org/x/perf/cmd/benchstat@latest
```

Project-specific semgrep rules live at the configured path. A starter rules file
should encode project invariants:
- Forbidden patterns in financial code (raw SQL, `errors.New`, missing auth checks)
- Required patterns (sentinel errors, structured logging, context propagation)
- Anti-patterns specific to your domain

---

## Usage

### Starting a task

Tell Claude:

```
Read .claude/workflow/roles/tasker.md and act as the Tasker. I need: [task description]
```

With a plan file already written:

```
Read .claude/workflow/roles/tasker.md. Execute the plan at docs/plans/2025-01-01-feature-name.md
```

### Workflow overview

**Implementation workflow (Tasker-driven):**

```
Human Request
     ↓
[Tasker] reads tasker.md — breaks down, finds references, enumerates edge cases
     ↓
(Critical/High) → [Design Agent] → 2-3 designs → Tasker selects one
     ↓
[Coder subagent] reads coder.md — TDD: tests first, then implement, then verify
     ↓
Completion Report → Tasker strips self-assessment
     ↓
cmd/classify  → risk · components · panel shape · gates · reviewer argv → run.json
     ↓            (mechanical: no model judgment, base_ref resolved once)
(Critical only) → [Security Linter] → PASS / FLAG / FAIL
     ↓
cmd/reviewer — 5 seats in parallel, argv from classification.reviewer_args
  claude · claude-scouts (8 dimension scouts) · codex · grok
     ↓
  dedup → tier consensus floor → component dimension floors → merged verdict
     ↓
    APPROVE ✅ → pr-raise (human gate iff classification.human_pr_gate)
    ITERATE    → fix CRITICAL/HIGH only
                   round 2:  full re-audit (cmd/reviewer)
                   round 3+: targeted verification (cmd/recheck, -min-severity
                             from classification.recheck_min_severity)
    REJECT     → escalate to human
```

Risk classification sits after the Coder because the diff is the input. The Tasker still classifies provisionally in Phase 1 to pick the Design Agent and Coder gates; `cmd/classify` is the authority once code exists, and it can raise the tier the Tasker guessed.

**PR review workflow (human-partnered):**

```
Human: "Review PR #NNN"
     ↓
[PR Reviewer] reads pr-reviewer.md
     ↓
Phase 1-4: Fetch PR → Understand intent → Risk-classify files → 8-dimension review
           + data flow tracing + dedicated test quality audit → Post inline comments
     ↓
Phase 5: Severity calibration with human (30-second alignment)
     ↓
Phase 6: Interactive walkthrough of all medium+ findings
         (code in context → problem → principle → fix → human decision)
     ↓
Phase 7: PR tour — walk through important non-flagged parts
     ↓
Phase 8: Combined summary — merge human + agent feedback into one authoritative comment
     ↓
Human: "Respond to review comments on PR #NNN"
     ↓
[PR Responder] reads pr-responder.md — triage, fix, reply, report
```

### Invoking roles as subagents

When Claude (as Tasker) spawns a Coder or Reviewer, it must explicitly pass the
role file path in the subagent prompt — subagents do not inherit conversation context.

**Coder subagent prompt template:**

```
Read the file `.claude/workflow/roles/coder.md` for your complete role instructions.

Project commands:
- Test: [project test command]
- Lint: [project lint command]
- Complexity: [project complexity command]

Then implement this task:
[paste Task Assignment here]
```

**Reviewer subagent prompt template:**

```
Read the file `.claude/workflow/roles/reviewer.md` for your complete role instructions.
You did NOT write this code. Your job is to find defects, teach principles, and raise the bar.

[paste Review Request here — spec, file list, actual test output]
```

**PR Reviewer prompt (human-partnered, interactive):**

```
Read the file `.claude/workflow/roles/pr-reviewer.md` for your complete role instructions.
Review PR #NNN in [owner/repo].
```

**PR Responder prompt (after review comments are posted):**

```
Read the file `.claude/workflow/roles/pr-responder.md` for your complete role instructions.
Respond to comments on PR #NNN in [owner/repo].
```

**Security linter subagent prompt template:**

```
Read the file `.claude/workflow/roles/security-linter.md` for your complete role instructions.
Audit SQL injection, PII exposure, integer overflow, and auth/permission bypass.

Risk context: [what this code touches — SQL, auth, money, etc.]

Files to audit:
[list files]
```

---

## Updating

```bash
# In any project using this submodule
git submodule update --remote .claude/workflow
git add .claude/workflow
git commit -m "chore: update claude-workflow submodule"
```

---

## Quick reference

| Want to... | Say to Claude |
|------------|---------------|
| Run the full workflow | "Read `.claude/workflow/roles/tasker.md` and act as Tasker. Task: ..." |
| Design before coding | "Read `.claude/workflow/roles/design-agent.md` and produce designs for: ..." |
| Just implement something | "Read `.claude/workflow/roles/coder.md` and implement: ..." |
| Review existing code | "Read `.claude/workflow/roles/reviewer.md` and review: ..." |
| Security audit only | "Read `.claude/workflow/roles/security-linter.md` and audit: ..." |
| Execute a written plan | "Read `.claude/workflow/roles/tasker.md`. Execute plan: `docs/plans/...`" |
| Review a GitHub PR interactively | "Read `.claude/workflow/roles/pr-reviewer.md` and review PR #NNN" |
| Respond to PR review comments | "Read `.claude/workflow/roles/pr-responder.md` and respond to comments on PR #NNN" |
| Generate daily standup | "Read `.claude/workflow/roles/standup-reporter.md` and generate the standup" |
| Generate release notes | "Read `.claude/workflow/roles/release-notes-generator.md` and generate release notes" |
