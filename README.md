# claude-roles

Shared Claude agent role definitions (Tasker, Coder, Reviewer, Security Linter).

## Roles

| File | Role | Purpose |
|------|------|---------|
| `tasker.md` | Orchestrator | Breaks down work, dispatches agents, manages review cycles |
| `design-agent.md` | Design Agent | Produces 2-3 competing designs for Tasker to select — mandatory for Critical/High risk |
| `coder.md` | Implementation Agent | Writes tested code against an approved design spec |
| `reviewer.md` | Code Reviewer | 8-dimension review with focused sub-agents and pass/fail critical gates |
| `security-linter.md` | Security Auditor | Focused SQL injection / PII / integer overflow audit — gates Critical review |
| `pr-reviewer.md` | PR Reviewer | Runs 8-dimension review against a GitHub PR and posts findings as inline code comments via `gh` |
| `release-notes-generator.md` | Release Notes Generator | Generates internal + external release notes from git range, Jira tickets, and merged PRs |

---

## Prerequisites

### Three-model review panel

The Tasker dispatches three independent reviewers in parallel. Install both CLIs:

**Codex CLI (Reviewer B):**
```bash
npm install -g @openai/codex
export OPENAI_API_KEY=sk-...
```

**Gemini CLI (Reviewer C):**
```bash
npm install -g @google/gemini-cli
export GEMINI_API_KEY=...
```

**Without the CLIs:** The Tasker falls back to additional Claude subagent reviewers.
All three reviewers are required for Critical and High risk consensus.

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

**To dispatch Reviewer B (Codex CLI via Bash):**
```bash
npx @openai/codex --quiet --approval-mode full-auto \
  "Read .claude/roles/reviewer.md for your role. Review: [Review Request]"
```

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

### 3. Configure project commands

The Coder role references `[project test command]`, `[project lint command]`, and
`[project complexity command]`. Common stacks:

| Stack | Test | Lint | Complexity |
|-------|------|------|------------|
| Go | `go test -race -cover ./...` | `golangci-lint run` | `go-complexity-lint ./...` |
| TypeScript (Nx) | `nx test [app]` | `nx lint [app]` | ESLint complexity rules |
| TypeScript (plain) | `vitest run` | `eslint src/` | `eslint --rule complexity` |
| Python | `pytest --cov` | `ruff check .` | `radon cc .` |

Install `go-complexity-lint` for Go projects:

```bash
go install github.com/glemzurg/go-complexity-lint/cmd/go-complexity-lint@latest
```

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

```
Human Request
     ↓
[Tasker] reads tasker.md — classifies risk, breaks down, finds references
     ↓
[Coder subagent] reads coder.md — TDD: tests first, then implement, then verify
     ↓
Completion Report → Tasker strips self-assessment
     ↓
[Reviewer A subagent]  +  [Reviewer B Codex]   ← dispatched in parallel
reads reviewer.md          reads reviewer.md
     ↓                          ↓
         Merged dual-review verdict
              ↓
    APPROVED ✅  |  ITERATE → fix CRITICAL/HIGH only → targeted re-review
                 |  REJECT → escalate to human
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
You did NOT write this code. Your job is to find defects, not confirm correctness.

[paste Review Request here — spec, file list, actual test output]
```

**Security linter subagent prompt template:**

```
Read the file `.claude/roles/security-linter.md` for your complete role instructions.
Audit only SQL injection, PII exposure, and integer overflow.

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
| Review a GitHub PR with inline comments | "Read `.claude/roles/pr-reviewer.md` and review PR #NNN" |
