# claude-roles

Shared Claude agent role definitions for the full development lifecycle — from task breakdown through implementation, review, and PR feedback.

## Roles

| File | Role | Purpose |
|------|------|---------|
| `tasker.md` | Orchestrator | Breaks down work, dispatches agents, manages review cycles |
| `design-agent.md` | Design Agent | Produces 2-3 competing designs for Tasker to select — mandatory for Critical/High risk |
| `coder.md` | Implementation Agent | Writes tested code against an approved design spec |
| `verification-agent.md` | Verification Agent | Independent ground-truth runner — fresh-checkout build/test/lint/static-analysis/mutation/bench with no Coder context. Gates Critical review before any reviewer runs. |
| `regression-test-author.md` | Regression Test Author | Independent test author for confirmed user-reported bugs at Medium/High risk. Two phases on the same agent: writes a failing test from the user's reported contract before the fix exists, then verifies the fix satisfies it once the fix PR is up. Black-box only — refuses to write unit tests at the layer the Coder's TDD covers. |
| `reviewer.md` | Code Reviewer | 8-dimension review with data flow tracing, dedicated test quality audit, design coherence checks, and focused sub-agents |
| `security-linter.md` | Security Auditor | Focused SQL injection, PII exposure, integer overflow, and auth bypass audit with severity grading — gates Critical review |
| `config/team-config.yaml` | Shared Configuration | Team roster, JIRA/GitHub settings, tracked epics, P0 tickets, known binaries — single source of truth for project-specific config |
| `pr-reviewer.md` | PR Reviewer | Interactive PR review: 8-dimension analysis, severity calibration with human reviewer, medium+ issue walkthrough with teaching, PR tour, and combined human+agent summary comment |
| `pr-responder.md` | PR Responder | Triages and responds to PR review comments — fixes valid issues, replies with evidence, reports summary to human |
| `standup-reporter.md` | Standup Reporter | Generates daily engineering standup from JIRA + GitHub — Priority Watch, team status, PR attention list, P0 tracker, publishes to Confluence |
| `release-notes-generator.md` | Release Notes Generator | Produces structured release notes from merged PRs and JIRA tickets — categorised by feature, fix, and breaking change |

---

## Prerequisites

### Three-model review panel

The Tasker dispatches three independent reviewers in parallel using Claude Code plugins:

**Codex Plugin (Reviewer B):**
Install the Codex plugin for Claude Code. Requires an OpenAI API key.

**Gemini Plugin (Reviewer C):**
Install [cc-gemini-plugin](https://github.com/thepushkarp/cc-gemini-plugin) for Claude Code. Requires a Gemini API key.

**Fallback:** If either plugin is unavailable, the Tasker dispatches additional Claude subagent reviewers. Two-reviewer consensus is the minimum for High risk; three is required for Critical.

---

## Setup

### 1. Add as git submodule

```bash
# New project
git submodule add https://github.com/andrewcostello/claude-roles.git .claude/roles

# Project that already has .claude/
git submodule add https://github.com/andrewcostello/claude-roles.git .claude/roles
```

After cloning a project that already uses this submodule:

```bash
git submodule update --init --recursive
```

### 2. Add to CLAUDE.md

Paste this block into your project's `CLAUDE.md`. Fill in the project-specific commands for your stack.

```markdown
## Agent Workflow

Role definitions live in `.claude/roles/`. For non-trivial tasks, use the three-agent
workflow:

### How Claude Uses These Roles

**To start a task as Tasker:**
Read `.claude/roles/tasker.md` and adopt the Tasker role.

**To dispatch the Coder (subagent):**
Create a general-purpose subagent with this prompt:
"Read `.claude/roles/coder.md` for your role instructions, then implement: [Task Assignment]"

**To dispatch Reviewer A (subagent):**
Create a general-purpose subagent with this prompt:
"Read `.claude/roles/reviewer.md` for your role instructions, then review: [Review Request]"

**To dispatch Reviewer B (Codex plugin):**
Dispatch via the Codex Claude Code plugin with the reviewer.md prompt.

**To dispatch Reviewer C (Gemini plugin):**
Dispatch via the cc-gemini-plugin with the reviewer.md prompt.

**To dispatch the Design Agent (subagent — Critical/High risk):**
Create a general-purpose subagent with this prompt:
"Read `.claude/roles/design-agent.md` for your role instructions, then produce designs for: [Task Assignment]"

**To dispatch the Security Linter (subagent — Critical risk gate):**
Create a general-purpose subagent with this prompt:
"Read `.claude/roles/security-linter.md` for your role instructions, then audit: [file list]"

### Project Commands

The Coder role uses placeholders — replace these here so subagents know the actual commands:

- **Test:** `[YOUR TEST COMMAND]`       e.g. `go test -race -cover ./...`
- **Lint:** `[YOUR LINT COMMAND]`       e.g. `golangci-lint run`
- **Complexity:** `[YOUR COMPLEXITY COMMAND]` e.g. `go-complexity-lint ./...`

### Risk Classification

| Risk | Applies To | Review Depth |
|------|-----------|--------------|
| High | Wallet, payments, auth, state mutations | Full dual review |
| Medium | Repos, validation, read-only services | Single review |
| Low | Config, docs, migrations | Self-review only |

When in doubt, go one level higher.
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
Read .claude/roles/tasker.md and act as the Tasker. I need: [task description]
```

With a plan file already written:

```
Read .claude/roles/tasker.md. Execute the plan at docs/plans/2025-01-01-feature-name.md
```

### Workflow overview

**Implementation workflow (Tasker-driven):**

```
Human Request
     ↓
[Tasker] reads tasker.md — classifies risk, breaks down, finds references
     ↓
(Critical/High) → [Design Agent] → 2-3 designs → Tasker selects one
     ↓
[Coder subagent] reads coder.md — TDD: tests first, then implement, then verify
     ↓
Completion Report → Tasker strips self-assessment
     ↓
(Critical only) → [Security Linter] → PASS / FLAG / FAIL
     ↓
[Reviewer A: Claude]  +  [Reviewer B: Codex plugin]  +  [Reviewer C: Gemini plugin]
     ↓                          ↓                              ↓
                    Three-model merged verdict
                           ↓
    APPROVED ✅  |  ITERATE → fix CRITICAL/HIGH only → targeted re-review
                 |  REJECT → escalate to human
```

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
Read the file `.claude/roles/coder.md` for your complete role instructions.

Project commands:
- Test: [project test command]
- Lint: [project lint command]
- Complexity: [project complexity command]

Then implement this task:
[paste Task Assignment here]
```

**Reviewer subagent prompt template:**

```
Read the file `.claude/roles/reviewer.md` for your complete role instructions.
You did NOT write this code. Your job is to find defects, teach principles, and raise the bar.

[paste Review Request here — spec, file list, actual test output]
```

**PR Reviewer prompt (human-partnered, interactive):**

```
Read the file `.claude/roles/pr-reviewer.md` for your complete role instructions.
Review PR #NNN in [owner/repo].
```

**PR Responder prompt (after review comments are posted):**

```
Read the file `.claude/roles/pr-responder.md` for your complete role instructions.
Respond to comments on PR #NNN in [owner/repo].
```

**Security linter subagent prompt template:**

```
Read the file `.claude/roles/security-linter.md` for your complete role instructions.
Audit SQL injection, PII exposure, integer overflow, and auth/permission bypass.

Risk context: [what this code touches — SQL, auth, money, etc.]

Files to audit:
[list files]
```

---

## Updating

```bash
# In any project using this submodule
git submodule update --remote .claude/roles
git add .claude/roles
git commit -m "chore: update claude-roles submodule"
```

---

## Quick reference

| Want to... | Say to Claude |
|------------|---------------|
| Run the full workflow | "Read `.claude/roles/tasker.md` and act as Tasker. Task: ..." |
| Design before coding | "Read `.claude/roles/design-agent.md` and produce designs for: ..." |
| Just implement something | "Read `.claude/roles/coder.md` and implement: ..." |
| Review existing code | "Read `.claude/roles/reviewer.md` and review: ..." |
| Security audit only | "Read `.claude/roles/security-linter.md` and audit: ..." |
| Execute a written plan | "Read `.claude/roles/tasker.md`. Execute plan: `docs/plans/...`" |
| Review a GitHub PR interactively | "Read `.claude/roles/pr-reviewer.md` and review PR #NNN" |
| Respond to PR review comments | "Read `.claude/roles/pr-responder.md` and respond to comments on PR #NNN" |
| Generate daily standup | "Read `.claude/roles/standup-reporter.md` and generate the standup" |
| Generate release notes | "Read `.claude/roles/release-notes-generator.md` and generate release notes" |
