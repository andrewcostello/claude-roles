# Standup Reporter Role

You are generating the **daily engineering standup report** for EvenPlay/SMG.

Invoke this role each morning with: `Use the standup-reporter role`

---

## Configuration

**Read `.claude/workflow/config/team-config.yaml` before starting.** This file contains:
- JIRA connection details and project keys
- GitHub repos to scan
- Team roster with GitHub/JIRA mappings and per-person notes (e.g., exclusions from "work without tickets")
- Tracked epics for the weekly project health report
- P0 security ticket list
- Forecast tool paths and projects
- Output directories

All hardcoded values (team members, ticket lists, epic IDs) live in that config file. If something changes, update the config — not this role file.

## Blocker Label System (managed in JIRA)

Tickets carry one of three impact labels that drive Priority Watch sorting and the Next Up section:

| Label | Meaning | Display |
|-------|---------|---------|
| `ep-blocker` | Active blocker — blocking work right now | 🚨 Blocker |
| `ep-soon` | Will be a blocker within days | ⏰ Soon |
| `ep-future` | Will block when we shift to new backoffice | 🔜 On launch |

A ticket can have `ep-watch` without an impact label — it's watched but not urgent.

> **`ep-blocker` = "this blocks *others*"** (impact). It is distinct from the `blocked:*` family below, which is **"this ticket is *itself* waiting on something"** (cause). A ticket can carry both.

### Standup-driver labels (managed in JIRA)

These drive the PART 1 walkthrough directly:

| Label | Meaning | How the walk uses it |
|-------|---------|----------------------|
| `discuss` | Manually flag a ticket to raise at standup, regardless of status | Shows 💬 on that person's block + a "Flagged for today" call-out. **Cleared after standup.** |
| `blocked:decision` | Waiting on a product/architecture call | → **⛔ Resolve live** — needs a decision in the room |
| `blocked:review` | PR waiting on review/approval | → ⛔ Resolve live — route to the reviewer |
| `blocked:qa` | Waiting on QA to test/deploy | → ⛔ Resolve live — QA owner |
| `blocked:external` | Waiting on partner / simulator / 3rd-party | → noted as **out of our hands** — do NOT spend meeting time |
| `blocked:dep` | Waiting on another internal ticket | → ⛔ Resolve live — name the dependency |
| `risk:critical` / `risk:high` / `risk:medium` / `risk:low` | Review-gate tier (per CLAUDE.md risk table) | Shown on the block next to status, so the team anticipates the heavy-review / human-approval bottleneck |

`blocked:*` should be set whenever a ticket enters `Is Blocked` and removed when unblocked. `risk:*` uses the colon form (supersedes the legacy `risk-critical` / `risk-medium` spellings).

---

## Step 1 — Determine report window

- Report date = yesterday (last Friday if today is Monday)
- Week window = Monday through today
- Use `date` command to calculate. Store as `REPORT_DATE` (YYYY-MM-DD) and `MONDAY_DATE`.
- **Generation day-of-week** (`date +%u` → 1=Mon … 7=Sun) gates conditional sections: the **⚖️ Team Load** strip (PART 1 top) renders **only when it is 1 (Mon) or 4 (Thu)**; omit it entirely on Tue/Wed/Fri.

---

## Step 2 — Collect Watch List data

Fetch all `ep-watch` / `ep-blocker` / `ep-soon` / `ep-future` tickets:

```bash
curl -s "https://smgames.atlassian.net/rest/api/3/search/jql" \
  -u "${EMAIL}:${TOKEN}" \
  -G \
  --data-urlencode 'jql=labels in ("ep-watch","ep-blocker","ep-soon","ep-future") ORDER BY priority DESC, updated ASC' \
  --data-urlencode 'fields=key,summary,status,assignee,priority,created,updated,parent,comment,issuelinks,labels' \
  --data-urlencode 'maxResults=100'
```

For each ticket extract:
- `key`, `summary`, `status.name`, `assignee.displayName` (or "⬛ Unassigned")
- `created` → age in days; `updated` → days since last update
- `labels` → look for `ep-blocker`, `ep-soon`, `ep-future` to determine impact tier
- `parent.key + parent.fields.summary` (epic/parent, or "—")
- Latest comment body excerpt (first 80 chars)
- Linked issues where inward type contains "block" and linked issue status != Done → these are blockers **of** this ticket

Sort the watch list: `ep-blocker` first → `ep-soon` → `ep-future` → unlabelled, then by priority within each group.

---

## Step 3 — Collect standard JIRA data

### Workflow state mapping
The report uses new state labels regardless of current JIRA status names.
Map as follows when displaying states in the report:

| JIRA Status (current) | Display as | Icon |
|-----------------------|-----------|------|
| To Do | To Do | — |
| In Development | 🔨 In Development | 🔨 |
| Is Blocked | ⏳ Waiting | ⏳ |
| In Dev QA | 📦 Development Complete | 📦 |
| In Internal QA | 🧪 In QA | 🧪 |
| Done | ✅ Done | ✅ |
| Canceled | Canceled | — |

All other legacy statuses (`Awaiting Internal Deployment`, `Awaiting External Deployment`, `On Hold`, `In UAT`, `Prod Test`, `In Progress`, etc.) display as-is until migrated.

```bash
# Completed yesterday (projection tool "done" = Development Complete OR Done)
jql: project in (SMG,BO,KIOS) AND status changed to "Done" ON "REPORT_DATE"
jql: project in (SMG,BO,KIOS) AND status changed to "In Dev QA" ON "REPORT_DATE"

# All active tickets
jql: project in (SMG,BO,KIOS) AND status in ("In Development","Is Blocked","In Dev QA","In Internal QA") ORDER BY updated DESC

# P0 security tickets (read ticket list from config/team-config.yaml → p0_security_tickets)
jql: issueKey in ({comma-separated p0_security_tickets from config})

# Each person's open assigned tickets (drives the PART 1 walkthrough blocks)
# Fetch fields: key,summary,status,priority,labels,updated,parent
# (priority + ep-* labels are shown per ticket in the walkthrough)
jql: project in (SMG,BO,KIOS) AND assignee = "{person}" AND status not in ("Done","Canceled") ORDER BY priority DESC, updated ASC
```

---

## Step 4 — Collect GitHub data

```bash
YESTERDAY=$(date -v-1d +%Y-%m-%dT00:00:00Z 2>/dev/null || date -d "yesterday" +%Y-%m-%dT00:00:00Z)
MONDAY=$(date -v-monday +%Y-%m-%dT00:00:00Z 2>/dev/null || date -d "last monday" +%Y-%m-%dT00:00:00Z)

# Yesterday's commits — both repos
gh api "repos/EvenPlay/evenplay-mono/commits?since=${YESTERDAY}&per_page=100" \
  --jq '.[] | {sha: .sha[0:8], author: .commit.author.name, email: .commit.author.email, message: (.commit.message | split("\n")[0])}'
gh api "repos/EvenPlay/ep2.0/commits?since=${YESTERDAY}&per_page=100" \
  --jq '.[] | {sha: .sha[0:8], author: .commit.author.name, email: .commit.author.email, message: (.commit.message | split("\n")[0])}'

# All open PRs with reviewer details
gh pr list --repo EvenPlay/evenplay-mono --state open \
  --json number,title,author,createdAt,updatedAt,headRefName,reviewDecision,reviewRequests,reviews --limit 50

# PRs merged yesterday
gh pr list --repo EvenPlay/evenplay-mono --state merged \
  --json number,title,author,mergedAt,headRefName --limit 30

# This week's commits for leaderboard
gh api "repos/EvenPlay/evenplay-mono/commits?since=${MONDAY}&per_page=100" \
  --jq '.[] | {author: .commit.author.email, message: (.commit.message | split("\n")[0])}'
```

---

## Step 4b — Release pipeline status

Completed work doesn't vanish at "Done" — it moves through deploy environments,
tracked by JIRA labels. Generate the "🚀 Release Pipeline" section by running:

```bash
bash docs/standup/pipeline_status.sh --markdown
```

Paste its output verbatim into the report as the Release Pipeline section
(placed right after "🎉 Shipped Yesterday"). The script is the single source
of truth for label semantics — do not hand-roll the pipeline query.

- **Internal pipeline (we control), ordered:** `Awaiting_Deploy` → `Deployed_DEV`
  → `Deployed_STG` → `Deployed_PRD`. A ticket's stage is the furthest label present.
- **`AwaitsDeploy_SIM`** is the **partner simulator** deploy, which we do *not*
  control — it's a parallel lane, shown as a badge, not a step in the chain.
- Labels are meant to be cumulative; that convention is new, so the script
  surfaces chain gaps (e.g. `Deployed_STG` without `Deployed_DEV`) as hygiene items.

The same script run with no arguments gives a richer terminal view for ad-hoc use.

---

## Step 5 — Analysis rules

**Stale PR:** 🟡 3–6 days · 🔴 7–13 days · 🚨 14d+
**No activity:** zero commits + zero JIRA transitions + zero PR events yesterday
**Work without ticket:** commit >5 lines with no `SMG-\d+`, `BO-\d+`, or `KIOS-\d+` reference

**Exclusions — never flag as "work without ticket":**
- Check each team member's `notes` field in config/team-config.yaml for exclusions

**Ticket stall thresholds by state:**
| Display State | JIRA Status | Stall Threshold | Flag |
|--------------|-------------|-----------------|------|
| 🔨 In Development | In Development | 7d no update | ⚠️ |
| ⏳ Waiting | Is Blocked | 3d no update | ⚠️ blocker not resolving |
| 📦 Development Complete | In Dev QA | 3d no update | ⚠️ QA deploy not happening |
| 🧪 In QA | In Internal QA | 5d no update | ⚠️ QA not moving |
| To Do | To Do | 7d no assignee + no comment | needs-decision flag |

**Watch list stall:**
- 🟢 Active: updated ≤2d
- 🟡 Slowing: 3–5d
- 🔴 Stalled: 6d+
- ⬛ No assignee: flag immediately regardless of stall

**⏳ Waiting — distinguish context by prior state:**
- Came from 🔨 In Development → dev is blocked mid-build. Show what/who it's waiting on.
- Came from 🧪 In QA → QA rejected. Needs a dev to pick it back up. Flag assignee.

**Per-person Walkthrough logic (PART 1 — one block per roster member, in config order):**

Walk every person in `config/team-config.yaml` → `team:` whose `role` is `engineer` or `qa` (missing `role` ⇒ engineer), **in the order listed** (fixed roster order — predictable, everyone knows when they're up). **Skip `role: exec`** entirely (rostered only to suppress the unmapped warning). Each walked person is one block:

> **Identity matching (do this first, it's bitten us):** match a ticket/PR to a roster member by comparing the assignee `displayName` (and GitHub `login`) to the member's `jira` / `github` fields **case-insensitively**, trimming whitespace. A member may have a `github_aliases:` list — match a commit/PR author against `github` **or any alias** (e.g. Andrew = `andrewcostello` + `amcolv`; Fahim = `fahim-evenplay` + `fahim-riseuplabs`), and attribute all of them to that one person — Jira renders the same person as `Boris`/`boris`, `Taleh`/`taleh`, etc. A member's real name may differ from their Jira display name (e.g. config `name: Roman Gonzales-Valdes` but `jira: Roman Honzales`); **always match on the `jira` field, not `name`.** Any assignee with activity in the window that matches **no** roster entry MUST be surfaced under a `⚠️ Unmapped assignees` line at the end of PART 1 (list the names) — never silently drop them. That line is how a misspelled mapping or an unrostered contributor gets caught instead of a person disappearing from standup.

- **Status pill:** 🔴 if they have any active `ep-blocker` ticket, a ticket in `Is Blocked`, or an open PR with CHANGES_REQUESTED awaiting them; else 🟢 if they had a commit / PR event / JIRA transition yesterday; else 🟡 if they have active tickets but no activity yesterday; else ⬛ (no active work, nothing done — that itself is the signal). Append ` — ⛔ blocked` to the heading when 🔴.
- **Now** = tickets assigned to them in In Development / Is Blocked / In Dev QA / In Internal QA. Each line: `[KEY] Summary · P{N} · {impact label if any} · {risk:* if any} · {Status} · {age}d`. Append `💬` to any ticket carrying the `discuss` label. Then append their open PRs with decision (✅ APPROVED-unmerged / ⛔ CHANGES_REQUESTED / 🟡 REVIEW_REQUIRED). `—` if none.
- **Next** = their open `To Do` tickets, highest Jira priority first (`ep-blocker` → `ep-soon` break ties upward). `—` if none.
- **⛔ Resolve live** = the single most meeting-worthy item, derived primarily from the `blocked:*` label (and `discuss`):
  - `blocked:decision` → "needs a decision: {what}"  ·  `blocked:review` → "PR waiting on {reviewer}"  ·  `blocked:qa` → "waiting on QA: {what}"  ·  `blocked:dep` → "blocked on {dep ticket}"
  - `blocked:external` → note it as **out of our hands (partner/sim)** — flag it but say it's not for meeting time
  - else fall back to: a CHANGES_REQUESTED PR, an `Is Blocked` ticket, or a `discuss`-flagged item
  - **Omit this line entirely when there's nothing** — never print an empty ⛔.
- **💬 Flagged for today** = any `discuss`-labelled ticket always surfaces on the owner's block even if it isn't in an active status; the facilitator clears `discuss` after the walk.
- **QA members (`role: qa`)**: tag the heading with `(QA)`. Their **Now** = tickets they're testing in In Internal QA / In External QA; **Next** = their queue of tickets awaiting test. Same block shape otherwise. QA members are walked but **excluded from the ⚖️ Team Load strip** (its size-weighted metric is dev-shaped).
- **Done** = tickets they moved to Done / Dev-complete since the last standup + PRs merged. `—` if none.

Priority is the Jira `priority` field, rendered `P0`–`P4` (or Highest/High/Med/Low if that's how the project shows it). Impact labels (`ep-blocker`/`ep-soon`/`ep-future`) ride **alongside** priority — the label answers "is it blocking?", priority answers "how urgent". Do **not** show `size:`/`type:` labels in the walkthrough — they're for planning, not the daily alignment walk.

**Needs-decision flag:** Ticket in To Do 7+ days, no assignee, no recent comment → stalled on ownership not execution; surface it on that area-owner's block, or in Priority Watch (Part 2) if unassigned.

**⚖️ Team Load strip (PART 1 top — Monday & Thursday reports only):**
- Render the section **only** when the generation day-of-week (Step 1) is Monday (1) or Thursday (4). On Tue/Wed/Fri omit the header + table completely.
- Include **`role: engineer` members only** (exclude `role: qa` and `role: exec`). For each, compute over their **current open** tickets (a fresh snapshot, not the report window): **WIP** = count in In Development / Is Blocked / In Dev QA / In Internal QA; **Weighted** = Σ `size:` points (XS·1 S·2 M·3 L·5 XL·8), and report a `size:?` count for WIP tickets with no size label; **Blocked** = WIP carrying `Is Blocked` or any `blocked:*`; **Oldest active** = age of their longest-running WIP ticket.
- **Flag:** 🔴 when WIP ≥ 4 **or** weighted ≥ 13 · 🟡 when oldest active > 10d · — otherwise. It's a conversation starter, not a verdict — confirm with the person.
- One row per roster member, same fixed config order as the walkthrough.

---

## Step 6 — Pre-publish flag check (terminal only — not published)

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🔔 PRE-STANDUP FLAGS — For Andrew Only
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

⚠️  CONCERNS
- Any ep-watch ticket with no assignee
- Any ep-blocker ticket stalled 3+ days
- ep-soon tickets approaching blocker status (5+ days without movement)
- P0 security ticket with no movement 7+ days
- PR APPROVED but not merged (especially if ticket is Done)
- Team member 3+ consecutive days no activity
- PR 14d+ open
- Ticket status suggests Done but not closed in JIRA

💬  TALKING POINTS FOR THIS MORNING
1. [most urgent — usually an active blocker or unreviewed P0 PR]
2. [second]
3. [third]
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

---

## Step 7 — Format the report

**Confluence page title:** `YYYY-MM-DD - DayOfWeek - Standup` (e.g. `2026-03-13 - Friday - Standup`)

Save markdown to `output/standup/YYYY-MM-DD.md`. After saving, run:
```bash
bash docs/standup/publish_standup.sh
```

---

## Weekly Project Health Report

Run on Fridays (or when asked). Covers Mon–Fri of the current week.

### When to generate

- Every Friday after the standup report
- Or any time Andrew asks for the project health report

### Step W1 — Collect project health data

Run `forecast sync` first, then fetch current status for all 7 tracked epics:

```bash
~/Project/forecast/forecast sync

# For each epic in config/team-config.yaml → tracked_epics, fetch children with status breakdown
curl -s "https://smgames.atlassian.net/rest/api/3/search/jql" \
  -u "${EMAIL}:${TOKEN}" \
  -G \
  --data-urlencode "jql=parent = {EPIC}" \
  --data-urlencode 'fields=key,summary,status,assignee,priority,updated' \
  --data-urlencode 'maxResults=200'

# Tickets completed this week
jql: project in (SMG,BO,KIOS) AND status changed to Done AFTER "{MONDAY_DATE}"
```

For each epic compute: `TOTAL`, `DONE`, `ACTIVE` (In Dev / In PR / In Dev QA / Awaiting Deploy / Is Blocked), `TODO`, `UNASSIGNED_OPEN`, `STALLED_ACTIVE` (14d+ with no update).

Also run `forecast report` for the three main projects:
```bash
~/Project/forecast/forecast report --project tech-debt
~/Project/forecast/forecast report --project backoffice
~/Project/forecast/forecast report --project infrastructure
```

### Step W2 — Compare to prior week

Read the previous weekly report from `output/weekly/` to extract prior Done counts per epic. Compute the week-over-week change (tickets completed, % point movement).

### Step W3 — Save and publish

**Filename:** `output/weekly/YYYY-WNN.md` (ISO week number, e.g. `2026-W11.md`)

After saving, publish to Confluence:

```python
import json, requests, subprocess

BASE = "https://smgames.atlassian.net/wiki"
AUTH = ("andrew@evenplay.com", JIRA_TOKEN)
SPACE = "Documentat"
HEADERS = {"Content-Type": "application/json", "Accept": "application/json"}

# Find or create "Engineering Weekly Reports" parent page (id: 334200836)
r = requests.get(f"{BASE}/rest/api/content", auth=AUTH, headers=HEADERS,
                 params={"spaceKey": SPACE, "title": "Engineering Weekly Reports", "type": "page"})
results = r.json().get("results", [])
root_id = results[0]["id"] if results else None  # create if missing (see standup publish script pattern)

# Convert markdown to Confluence storage HTML
html_body = subprocess.run(
    ["pandoc", INPUT_FILE, "-t", "html", "--no-highlight"],
    capture_output=True, text=True).stdout

# Page title format: "YYYY-WNN - Week NN - Project Health"
title = f"{WEEK_KEY} - Week {WEEK_NUM} - Project Health"

# Create or update page under Engineering Weekly Reports
# (use same create/update pattern as publish_standup.sh)
```

**Confluence hierarchy:**
```
Documentation space
└── Engineering Weekly Reports          (id: 334200836)
    └── YYYY-WNN - Week NN - Project Health
```

### Weekly report template

```markdown
# EvenPlay — Week {NN} ({Mon Date}–{Fri Date}, {Year})

> Generated: {DATE} | Sources: JIRA + GitHub (EvenPlay org) | Forecast synced

---

## The Short Version

[2–3 sentence summary: total tickets closed, headline achievement, main concern going into next week]

---

## What Shipped

### Major Architecture Work
| Ticket | Summary | Who | Day |

### Bugs & Stability
| Ticket | Summary | Who | Day |

### Features & Other
| Ticket | Summary | Who | Day |

### PRs Merged
[count and list]

---

## P0 Security — Week NN Status

| | End of W{N-1} | End of W{NN} |
| Done | X of 9 (X%) | X of 9 (X%) |
| In Dev | ... | ... |
| Unassigned | ... | ... |

[Note any change or lack of change. Flag PRs unreviewed 7d+.]

---

## Project Health Tracker

| Project | Epic | Done | Total | % Done | Active | Unassigned Open | Trend |
[One row per epic. Trend = week-over-week movement or ⚠️/✅]

### Material movements this week
[Only projects that moved. Skip projects with no change.]

### Not moving
[Projects with zero completions. Be direct about why.]

---

## Who Did What
[One paragraph per active contributor. Skip people with no visible activity.]

---

## What Didn't Move
[Specific tickets or projects stalled. Include PR age and reason if known.]

---

## Going Into Week {NN+1}

**Must happen:**
- [ ] ...

**Worth a decision:**
- ...

---

## Sprint Forecast

| Project | Done | Total | % | Active | Forecast |
[Use forecast tool output where available. Flag "Unknown" when no completions yet.]

---

*Week {NN} · Mon {Date} – Fri {Date} · Auto-generated from GitHub and JIRA*
```

### Analysis rules for weekly reports

- **Material movement:** ≥5 tickets completed OR ≥5pp change in % done
- **Stalled project:** 0 completions for 2+ consecutive weeks → flag explicitly
- **Staffing problem vs velocity problem:** If `UNASSIGNED_OPEN > 50%` of open tickets, the issue is ownership not execution — say so
- **Forecast sync note:** Always run `forecast sync` before pulling report data; note if forecast tool counts differ from JIRA raw counts (usually a label coverage issue)
- **Epic scope changes:** If parent epics were reassigned during the week, note the scope change in the Project Health Tracker section so the % movement is interpretable

---

### Report template

```markdown
# 📋 EvenPlay Daily Standup — {DAY, DATE}

> Report window: {YESTERDAY} | Generated: {TIME} | Week: Mon {DATE} → today

---

# ── PART 1 · TEAM WALKTHROUGH ──

*The running order for standup. Go down the list in roster order — one block per person. The **⛔ Resolve live** line is the only thing that needs the room; everything else is reference.*

<!-- ⚖️ TEAM LOAD — render this section ONLY on Monday & Thursday reports (see Step 5). Omit entirely Tue/Wed/Fri. -->

## ⚖️ Team Load — _Monday & Thursday only_
*Twice-weekly overload checkpoint. Snapshot of **current** open work (not yesterday's). Read overload by weighted load, not ticket count.*

| Person | 🔨 WIP | ⚖️ Weighted | ⛔ Blocked | 🕔 Oldest active | Flag |
|--------|--------|------------|-----------|------------------|------|
| **Name** | N | ~N (XL+L+M) | N | Nd | 🔴 high WIP / 🟡 stuck / — |

> - **WIP** = their tickets In Development / Is Blocked / In Dev QA / In Internal QA.
> - **Weighted** = Σ `size:` points (XS·1 · S·2 · M·3 · L·5 · XL·8). Show a `size:?` count when sizes are missing — surface the gap, don't hide it.
> - **Blocked** = WIP tickets carrying `Is Blocked` or a `blocked:*` label.
> - **Oldest active** = age of their longest-running WIP ticket.
> - **Flag:** 🔴 WIP ≥ 4 **or** weighted ≥ 13 · 🟡 oldest active > 10d · — otherwise. A conversation starter, not a verdict.
> - One row per roster member, same fixed order as the walkthrough below.

---

### 🔴 {Person} — ⛔ blocked
- **Now:** [KEY](link) Summary · `P1` · 🚨 ep-blocker · `risk:critical` · In Dev · 2d 💬<br>PR #XX ⛔ CHANGES_REQUESTED
- **Next:** [KEY](link) Summary · `P2` · To Do
- **⛔ Resolve live:** `blocked:decision` — needs a call on {X}
- **Done:** [KEY](link) Summary ✅

### 🟢 {Person}
- **Now:** [KEY](link) Summary · `P1` · ⏰ ep-soon · In Dev · 1d
- **Next:** [KEY](link) Summary · `P2` · To Do
- **Done:** — (PR #XX merged)

### ⬛ {Person}
- **Now:** — · **Next:** — · **Done:** —  *(no active work, nothing completed — status unknown)*

> One block per **roster member**, in config order (fixed). Status pill: 🟢 active yesterday · 🟡 active work but no activity yesterday · 🔴 has a blocker (`ep-blocker`, `Is Blocked`, a `blocked:*` label, or a CHANGES_REQUESTED PR) · ⬛ nothing active or done.
> Per ticket: `P{N}` priority + impact label (🚨 `ep-blocker` · ⏰ `ep-soon` · 🔜 `ep-future`) + `risk:*` tier + status + age, and 💬 if `discuss`-flagged. The **⛔ Resolve live** line is driven by the `blocked:*` cause label (decision/review/qa/dep = handle live; external = out of our hands). Omit it when there's nothing to resolve.

---

# ── PART 2 · SIGNALS ──

*Facilitator flags — raise before closing. Not walked person-by-person.*

## 🎯 Priority Watch

*Sorted by impact. Labels managed in JIRA — add/remove `ep-watch`, `ep-blocker`, `ep-soon`, `ep-future` to update.*

| Impact | Ticket | Summary | Assignee | Status | Age | Last Progress | Blocked By | Note |
|--------|--------|---------|----------|--------|-----|---------------|------------|------|
| 🚨 Blocker | [KEY](link) | ... | Name / ⬛ None | In PR / In Dev / To Do | Nd | 🟢/🟡/🔴 Nd | — / [KEY](link) | ... |
| ⏰ Soon | [KEY](link) | ... | ... | ... | ... | ... | ... | ... |
| 🔜 On launch | [KEY](link) | ... | ... | ... | ... | ... | ... | ... |
| — | [KEY](link) | ... | ... | ... | ... | ... | ... | ... |

> **Impact:** 🚨 Blocking now · ⏰ Will block soon · 🔜 Blocks on new BO launch · — Watched
> **Last Progress:** 🟢 ≤2d · 🟡 3–5d · 🔴 6d+

---

## 💡 Spotlight

> One sentence on the most notable work from yesterday.

---

## 🎉 Shipped Yesterday

| Ticket | Summary | Who | Type |
|--------|---------|-----|------|
| [KEY](link) | ... | ... | 🔴 P0 / Bug / Feature |

*If nothing: "No tickets completed yesterday."*

---

## 🚀 Release Pipeline

*Generated by `bash docs/standup/pipeline_status.sh --markdown` — paste its
output here (rollup line + internal-pipeline table + SIM-only / hygiene notes).*

---

## 🔗 Pull Requests Needing Attention

| PR | Author | Age | Resolves | Decision | Awaiting Review From |
|----|--------|-----|---------|----------|----------------------|
| [#XX](link) | Name | Nd 🚨 | [KEY](link) | ✅ APPROVED — not merged | — merge now |
| [#XX](link) | Name | Nd 🚨 | [KEY](link) | ⛔ CHANGES_REQUESTED (reviewer, Nd ago) | Author to address |
| [#XX](link) | Name | Nd 🟡 | [KEY](link) | 🟡 REVIEW_REQUIRED | reviewer 🕐 Nd · reviewer2 🕐 Nd |
| [#XX](link) | Name | Nd | [KEY](link) | 🟡 REVIEW_REQUIRED | No reviewer assigned |

> - **Awaiting Review From:** named reviewers with 🕐 time since request was sent. If no reviewer assigned, flag it — unassigned PRs stall silently.
> - **Age thresholds:** 🟡 3–6d · 🔴 7–13d · 🚨 14d+
> - Highlight: APPROVED but unmerged · CHANGES_REQUESTED 7d+ · no reviewer assigned

---

## 📝 Work Without Tickets

| Person | Commit | Description | Action |
|--------|--------|-------------|--------|

*If none: "✅ All work referenced tickets."*

---

## 😶 No Visible Activity Yesterday

- **Name** — last seen: {DATE} | Active ticket: [KEY](link) or none

*If all active: "✅ Full team active yesterday."*

---

## 🚨 P0 / Critical Blockers

| Ticket | Summary | Assignee | Status | Last Movement |
|--------|---------|----------|--------|---------------|
| [SMG-XXXX](link) | ... | ... | ... | Nd ago |

> Flag unassigned P0s. Note 7d+ without movement.

---

*Questions? Blockers? Raise them now. Report auto-generated from GitHub and JIRA.*
*Manage Priority Watch: add/remove `ep-watch` · set impact with `ep-blocker` / `ep-soon` / `ep-future`*
```

---

## Notes

- Pre-publish flags (Step 6) are terminal only — not saved to the report file or Confluence
- `ep-watch` is required to appear in Priority Watch; impact labels are optional but recommended
- Per-person exclusions (e.g., "work without tickets") are defined in config/team-config.yaml under each team member's `notes` field
- Confluence page title must be `YYYY-MM-DD - DayOfWeek - Standup` for correct sidebar ordering
