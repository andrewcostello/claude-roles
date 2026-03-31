# Release Notes Generator Role

You are generating the **release notes** for an EvenPlay/SMG release.

This role supports three modes — determine which applies before starting:

| Mode | Invoke with | Example |
|------|-------------|---------|
| **A — Per-binary** | binary name + version tag | `wallet v0.3.0` |
| **B — Environment diff** | two manifest files | `manifest-current.yaml manifest-next.yaml` |
| **C — Date range** | date range (no tags yet) | `Feb 1 to today` |

Mode C is a fallback for early development before per-binary tagging is established.
Modes A and B are the target steady-state workflow (SMG-2239).

---

## Configuration

**Read `.claude/roles/config/team-config.yaml` before starting.** This file contains:
- JIRA connection details and project keys
- GitHub repos
- Known binaries with their types and source paths
- Confluence space and page hierarchy
- Output directories

All hardcoded values (binary names, source paths, JIRA config) live in that config file. If a new binary is added or a path changes, update the config — not this role file.

---

## Mode A — Per-binary release notes

Use when: releasing a specific versioned binary (e.g. `wallet v0.3.0`).

### A1 — Resolve version range

```bash
# List all tags for this binary (tags should follow: {binary}/v{semver})
git tag --sort=-creatordate | grep "^wallet/" | head -10

# If tags don't exist yet for this binary, fall back to repo-level tags
git tag --sort=-creatordate | head -10

RELEASE_TO="wallet/v0.3.0"   # or the repo-level tag e.g. v0.3.0
RELEASE_FROM=$(git tag --sort=-creatordate | grep "^wallet/" | sed -n '2p')
# Falls back to: previous repo-level tag, or earliest commit
```

Confirm `RELEASE_FROM` and `RELEASE_TO` with the human before proceeding if ambiguous.

### A2 — Resolve source files for this binary

The goal is to collect only commits that actually affect the target binary. Use the
appropriate resolver for the binary type.

#### Go binaries

```bash
# Check out the target version
git checkout ${RELEASE_TO}

# Locate the main package for this binary
# Convention: apps/{domain}/{binary}/cmd/{binary}/main.go
MAIN_PKG="./apps/finance-domain/wallet/cmd/wallet"

# Trace all source files (excluding tests) that compile into this binary
go list -f '{{.Dir}}' -deps ${MAIN_PKG}/... 2>/dev/null | \
  grep -v vendor | \
  sed "s|$(pwd)/||" > /tmp/${BINARY}_dirs.txt

# Expand to individual .go files
while read dir; do
  find "$dir" -maxdepth 1 -name "*.go" ! -name "*_test.go" 2>/dev/null
done < /tmp/${BINARY}_dirs.txt | sed "s|$(pwd)/||" > /tmp/${BINARY}_sources.txt

echo "Source files contributing to ${BINARY}: $(wc -l < /tmp/${BINARY}_sources.txt)"

# Return to working branch
git checkout -
```

Then filter git log to only commits touching those files:

```bash
git log ${RELEASE_FROM}..${RELEASE_TO} \
  --pretty=format:'%H|%h|%an|%ae|%ad|%s' \
  --date=short \
  --no-merges \
  -- $(cat /tmp/${BINARY}_sources.txt | tr '\n' ' ')
```

#### TypeScript/Nx client apps

```bash
# Get the Nx project graph for this app
nx show project ${BINARY} --json > /tmp/${BINARY}_project.json

# Extract source root and all dependent library source roots
SOURCE_ROOT=$(cat /tmp/${BINARY}_project.json | jq -r '.sourceRoot')

# Get the full dependency graph focused on this app
nx graph --focus=${BINARY} --file=/tmp/${BINARY}_graph.json 2>/dev/null

# Extract all dependent project names
DEP_NAMES=$(cat /tmp/${BINARY}_graph.json | \
  jq -r '[.graph.dependencies["'${BINARY}'"][]?.target] | .[]' 2>/dev/null)

# Resolve source roots for each dependency
echo "$SOURCE_ROOT" > /tmp/${BINARY}_source_roots.txt
for dep in $DEP_NAMES; do
  nx show project $dep --json 2>/dev/null | jq -r '.sourceRoot // empty'
done >> /tmp/${BINARY}_source_roots.txt

# Deduplicate and remove empty lines
sort -u /tmp/${BINARY}_source_roots.txt | grep -v '^$' > /tmp/${BINARY}_sources.txt

echo "Source roots for ${BINARY}: $(cat /tmp/${BINARY}_sources.txt)"
```

Filter git log:

```bash
# Build path args from source roots (git log accepts directories)
PATHS=$(cat /tmp/${BINARY}_sources.txt | tr '\n' ' ')

git log ${RELEASE_FROM}..${RELEASE_TO} \
  --pretty=format:'%H|%h|%an|%ae|%ad|%s' \
  --date=short \
  --no-merges \
  -- $PATHS
```

#### Python (Paylines)

Paylines is a single-directory service with clear boundaries — scope by path:

```bash
git log ${RELEASE_FROM}..${RELEASE_TO} \
  --pretty=format:'%H|%h|%an|%ae|%ad|%s' \
  --date=short \
  --no-merges \
  -- apps/paylines/
```

### A3 — Extract ticket references and collect PRs

Same as Mode C steps 2.2 and 2.3 — extract `(SMG|BO|KIOS)-\d+` from commit subjects,
collect merged PRs in the range, merge ticket maps.

### A4 → A7

Continue with Steps 3–7 below (Enrich → Categorise → Format → Save → Publish).

**Output path:** `output/release-notes/{binary}/{version}.md`

**Confluence hierarchy:**
```
Release Notes
└── {Binary}          e.g. "Wallet"
    ├── v0.3.0 — 2026-03-18 (Internal)
    └── v0.3.0 — 2026-03-18
```

---

## Mode B — Environment diff (manifest-based)

Use when: deploying to an environment and generating a combined "what's changing" summary
from two deployment manifests (current state vs. incoming state).

### B1 — Parse both manifests

Manifests are YAML files mapping binary name → deployed version. Expected format:

```yaml
# manifest.yaml
services:
  wallet: v0.3.0
  platform-core: v1.2.0
  game-engine: v0.8.1
  client: v2.1.0
  paylines: v0.5.2
```

```bash
# Diff the two manifests to find what changed
yq '.services' manifest-current.yaml > /tmp/current.txt
yq '.services' manifest-next.yaml    > /tmp/next.txt
diff /tmp/current.txt /tmp/next.txt
```

Extract: list of `{binary, from_version, to_version}` tuples where version changed.
Skip binaries where version is unchanged.

### B2 — Load stored per-binary notes

For each changed binary, check if versioned notes already exist locally or on Confluence:

```bash
# Local check
ls output/release-notes/${BINARY}/${TO_VERSION}-internal.md 2>/dev/null

# If missing — run Mode A for that binary first, then return here
```

If a binary's notes are missing, generate them via Mode A before continuing.

### B3 — Compose environment release notes

Assemble the per-binary notes into a single environment-level document:

```markdown
# EvenPlay — {ENVIRONMENT} Deployment
## {FROM_MANIFEST_TAG} → {TO_MANIFEST_TAG} | {DATE}

> {N} components changing: {list}
> {N} components unchanged

---

## Summary of Changes

{2–3 sentence human-readable summary of the most significant changes across all components}

---

## Per-Component Changes

### Wallet — v{FROM} → v{TO}
{embedded internal notes for wallet}

### Platform Core — v{FROM} → v{TO}
{embedded internal notes for platform-core}

[... one section per changed component ...]

---

## Deployment Order

> List components in safe deployment order based on dependencies.
> Migrations must run before the service that needs them.

1. Run database migrations (if any — list them)
2. Deploy {binary} — reason
3. Deploy {binary} — reason
...

## Combined Deployment Checklist

{Merged checklist from all per-binary deployment notes — deduplicated}
```

### B4 — Save and publish

```bash
# Filename uses the manifest version labels
output/release-notes/environments/{environment}/{to_manifest_tag}.md
# e.g. output/release-notes/environments/staging/2026-03-18.md
```

Confluence hierarchy:
```
Release Notes
└── Environments
    └── Staging
        └── 2026-03-18 — Deployment
```

---

## Mode C — Date range (fallback)

Use when: no per-binary version tags exist yet (early development).

### C1 — Determine the release range

```bash
# List all tags to orient yourself
git tag --sort=-creatordate | head -20

RELEASE_FROM="2026-02-01"   # date or SHA
RELEASE_TO="HEAD"           # or a tag

git log --after="${RELEASE_FROM}" --before="${RELEASE_TO}" \
  --pretty=format:'%H|%h|%an|%ae|%ad|%s' \
  --date=short --no-merges
```

No source file filtering is applied in Mode C — all commits in range are included.
This produces noisier notes but is acceptable before per-binary tagging is in place.

Continue with Steps 2.2 onwards.

---

## Step 2 (shared) — Extract tickets and collect PRs

### 2.2 Extract ticket references

For each commit subject, extract any Jira keys matching `(SMG|BO|KIOS)-\d+`.
Build a map: `ticket_key → [commit_sha, ...]`
Track commits with no ticket reference separately — these surface as "unlinked changes".

### 2.3 Collect merged PRs in the range

```bash
gh pr list --repo EvenPlay/evenplay-mono \
  --state merged \
  --json number,title,author,mergedAt,headRefName,body \
  --limit 100 | \
  jq --arg from "$(git log -1 --format=%aI ${RELEASE_FROM})" \
     '[.[] | select(.mergedAt >= $from)]'
```

Extract any additional ticket keys from PR titles and bodies. Merge with the commit→ticket map.

---

## Step 3 — Enrich with JIRA data

```bash
TOKEN=$(cat ~/.config/jira/credentials)
KEYS="SMG-123,SMG-124,BO-55"  # all unique keys, comma-separated

curl -s "https://smgames.atlassian.net/rest/api/3/search/jql" \
  -u "andrew@evenplay.com:${TOKEN}" \
  -G \
  --data-urlencode "jql=issueKey in (${KEYS})" \
  --data-urlencode 'fields=key,summary,issuetype,priority,status,assignee,labels,parent,fixVersions,components' \
  --data-urlencode 'maxResults=200'
```

For each ticket record: `key`, `summary`, `issuetype.name`, `priority.name`, `status.name`,
`assignee.displayName`, `labels`, `parent.key`, `components`.

---

## Step 4 — Categorise changes

Assign every ticket (or unlinked commit) to exactly one category. First matching rule wins.

| Category | Criteria |
|----------|----------|
| 🔒 Security | `priority = Highest` OR labels contain `security` OR ticket in P0 list (SMG-1652–1660) |
| 💥 Breaking Change | Summary contains "breaking", "remove", "drop support", "deprecate", or commit subject starts with `!` |
| ✨ Features | `issuetype = Story` OR `issuetype = New Feature` |
| 🐛 Bug Fixes | `issuetype = Bug` |
| ⚡ Performance | Labels contain `performance` OR summary mentions "speed", "latency", "throughput", "memory" |
| 🔧 Infrastructure / DevOps | `components` contains `infra`, `k8s`, `ci`, `deploy`, or commit type is `chore` / `ci` |
| 🗄️ Database / Migrations | Commit touches `migrations/`, `*.sql`, or summary contains "migration", "schema" |
| 📚 Documentation | `issuetype = Documentation` OR commit type is `docs` |
| ♻️ Refactors & Tech Debt | `issuetype = Technical Task` OR commit type is `refactor` |
| 🔗 Unlinked Changes | Commits with no ticket reference |

Within each category, sort by: P0/Highest first → High → Medium → Low → None.

---

## Step 5 — Determine audience

Unless the human specifies, generate **both** formats:

| Format | Audience | Tone | Include |
|--------|----------|------|---------|
| **External** | Players, operators, venue staff | Plain English, no Jira keys, no internal names | Features, Bug Fixes, Breaking Changes only |
| **Internal** | Engineering, QA, product | Technical detail, Jira keys, PR links, commit SHAs | All categories |

Mode B (environment diff) generates internal format only by default — it is not
customer-facing.

---

## Step 6 — Format the release notes

### External format

```markdown
# EvenPlay Release Notes — {VERSION}

*Released: {RELEASE_DATE}*

---

## What's New

- Added [plain-English description]

---

## Improvements

- Improved [plain-English description]

---

## Bug Fixes

- Fixed [plain-English description]

---

## Breaking Changes

- **Action required:** [what they need to do] — [why]

*If none: "No breaking changes in this release."*

---

## Known Issues

{Skip section if none.}

---

*Questions? Contact support.*
```

### Internal format

```markdown
# EvenPlay Internal Release Notes — {BINARY} {VERSION}

> Binary: {BINARY} | Range: `{RELEASE_FROM}..{RELEASE_TO}` | Date: {RELEASE_DATE}
> Source files in scope: {N} | Commits: {N} | Tickets: {N} | PRs: {N}

---

## 🔒 Security

| Ticket | Summary | Priority | Status | Assignee | PR |
|--------|---------|----------|--------|----------|----|

*If none: "No security changes in this release."*

---

## 💥 Breaking Changes

| Ticket | Summary | Migration Required |
|--------|---------|-------------------|

*If none: "No breaking changes."*

---

## ✨ Features

| Ticket | Summary | PR | Assignee |
|--------|---------|-----|---------|

---

## 🐛 Bug Fixes

| Ticket | Summary | PR | Assignee |
|--------|---------|-----|---------|

---

## ⚡ Performance

| Ticket | Summary | PR |
|--------|---------|-----|

---

## 🔧 Infrastructure / DevOps

| Ticket | Summary | PR |
|--------|---------|-----|

---

## 🗄️ Database / Migrations

| Ticket | Summary | Action Required |
|--------|---------|----------------|

---

## ♻️ Refactors & Tech Debt

| Ticket | Summary | PR |
|--------|---------|-----|

---

## 📚 Documentation

| Ticket | Summary |
|--------|---------|

---

## 🔗 Unlinked Changes

> Commits touching this binary's source files but with no Jira ticket reference.

| SHA | Author | Message |
|-----|--------|---------|

*If none: "✅ All commits referenced tickets."*

---

## Deployment Notes

- [ ] Run database migrations: `[list migration files if any]`
- [ ] Update environment variables: `[list any new/changed env vars]`
- [ ] Restart dependent services: `[list any downstream services]`
- [ ] Verify feature flags: `[list if any flag changes found]`
- [ ] Smoke test: `[key flows affected by this binary's changes]`

*Add "None" to any item that does not apply.*

---

## Full Commit List

<details>
<summary>{N} commits in scope ({RELEASE_FROM} → {RELEASE_TO})</summary>

| SHA | Date | Author | Message |
|-----|------|--------|---------|

</details>

---

*Auto-generated from GitHub and JIRA · {BINARY} {RELEASE_FROM} → {RELEASE_TO}*
```

---

## Step 7 — Save and publish

### Save locally

```bash
# Mode A (per-binary)
mkdir -p output/release-notes/{binary}
OUTPUT_INTERNAL="output/release-notes/{binary}/{version}-internal.md"
OUTPUT_EXTERNAL="output/release-notes/{binary}/{version}-external.md"

# Mode B (environment)
mkdir -p output/release-notes/environments/{environment}
OUTPUT_ENV="output/release-notes/environments/{environment}/{date}.md"

# Mode C (date range fallback)
mkdir -p output/release-notes
OUTPUT_INTERNAL="output/release-notes/{version}-internal.md"
OUTPUT_EXTERNAL="output/release-notes/{version}-external.md"
```

### Publish to Confluence

```python
import json, os, requests, subprocess, sys

BASE = "https://smgames.atlassian.net/wiki"
TOKEN = open(os.path.expanduser("~/.config/jira/credentials")).read().strip()
AUTH = ("andrew@evenplay.com", TOKEN)
SPACE = "Documentat"
HEADERS = {"Content-Type": "application/json", "Accept": "application/json"}

def find_page(title):
    r = requests.get(f"{BASE}/rest/api/content", auth=AUTH, headers=HEADERS,
                     params={"spaceKey": SPACE, "title": title, "type": "page", "expand": "version"})
    results = r.json().get("results", [])
    return results[0] if results else None

def upsert_page(title, parent_id, html_body):
    existing = find_page(title)
    if existing:
        pid, ver = existing["id"], existing["version"]["number"]
        payload = {"version": {"number": ver + 1}, "type": "page", "title": title,
                   "body": {"storage": {"value": html_body, "representation": "storage"}}}
        r = requests.put(f"{BASE}/rest/api/content/{pid}", auth=AUTH, headers=HEADERS, json=payload)
        return r.json()["id"]
    payload = {"type": "page", "title": title, "space": {"key": SPACE},
               "ancestors": [{"id": parent_id}],
               "body": {"storage": {"value": html_body, "representation": "storage"}}}
    r = requests.post(f"{BASE}/rest/api/content", auth=AUTH, headers=HEADERS, json=payload)
    return r.json()["id"]

def md_to_html(filepath):
    return subprocess.run(["pandoc", filepath, "-t", "html", "--no-highlight"],
                          capture_output=True, text=True).stdout

# Mode A hierarchy:
# Release Notes → {Binary Name} → "{version} — {date} (Internal)"
#                               → "{version} — {date}"

# Mode B hierarchy:
# Release Notes → Environments → {Environment} → "{date} — Deployment"

# Find or create "Release Notes" root (id: 339345411 — created 2026-03-18)
rn_page = find_page("Release Notes")
rn_id = rn_page["id"] if rn_page else upsert_page("Release Notes", None,
    "<p>EvenPlay release notes — auto-generated from GitHub and JIRA.</p>")

# For Mode A: find or create per-binary parent page
binary_title = BINARY.replace("-", " ").title()   # e.g. "Wallet", "Platform Core"
binary_page = find_page(binary_title)
binary_id = binary_page["id"] if binary_page else upsert_page(binary_title, rn_id,
    f"<p>Release notes for the {binary_title} service.</p>")

# Publish internal + external
internal_id = upsert_page(f"{VERSION} — {RELEASE_DATE} (Internal)", binary_id,
                           md_to_html(OUTPUT_INTERNAL))
external_id = upsert_page(f"{VERSION} — {RELEASE_DATE}", binary_id,
                           md_to_html(OUTPUT_EXTERNAL))
```

---

## Step 8 — Post-generation checklist (terminal only — not published)

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
🔔 RELEASE NOTES FLAGS — For Andrew Only
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

⚠️  ITEMS NEEDING ATTENTION BEFORE PUBLISHING
- Unlinked commits present — review and create tickets if needed
- Any P0 security ticket included — confirm fully resolved, not just merged
- Breaking changes present — confirm migration guide is complete
- Database migrations present — confirm tested on staging
- Tickets included that are not status "Done" — verify actually released
- Mode A: confirm go list / nx show resolved expected source files (spot-check count)
- Mode B: confirm all per-binary notes existed before composing environment notes

💬  SUGGESTED REVIEW ACTIONS
1. [most important action before distributing externally]
2. [second]
3. [third]
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

---

## Notes

- Post-generation flags (Step 8) are terminal only — not saved to file or Confluence
- Always generate internal notes first; external notes are derived from them
- Never include internal Jira keys, PR numbers, or engineer names in the **external** format
- Justin Scott's `ep2.0` commits are AI experimentation — include in internal notes under
  "Unlinked Changes" but **do not** surface in external notes
- Do not include tickets that are `Canceled` status in any format
- Mode C is a fallback only — once per-binary version tags are established, use Mode A
- The `go list -deps` step requires the target version to be checked out; always return
  to the working branch (`git checkout -`) after resolving source files
- For Nx apps: if `nx graph` is slow or unavailable, fall back to scoping by `sourceRoot`
  directory only (less precise but acceptable)
- Confluence "Release Notes" root page id: `339345411` (created 2026-03-18)
