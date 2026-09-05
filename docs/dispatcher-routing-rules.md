# Historical dispatcher routing observations

Status: provisional, not production routing policy. The 2026-09-05 review
reproduced false-green and provenance defects in the original measurement
tools. Clause counts below are keyword mentions, not verified behavioral
coverage. The original trials have not been rescored with the repaired tools.
Treat the recorded numbers as historical observations, not established model
superiority. Existing task pins remain explicit choices, not proven winners.

Written 2026-09-02 from the model studies in `features/model-matrix/`. Each rule
is marked **MEASURED** (from data in this repo), **INFERRED** (a defensible
reading, not directly tested), or **OPEN** (would change the rule if measured).

The studies used one task shape: writing seals against a tight contract. They
do not establish reliable rules for design work or production money paths.

---

## 1. Round count dominates cost. Optimise for first-pass coverage.

**MEASURED.** Review is 27–50% of wall clock, charged per round by three
reviewer seats, and paid whichever model implements:

| arm | dev | review | rounds |
|---|---|---|---|
| OPUS | 17m | 11m | 0 |
| SONNET | 20m | 20m | 2 |
| FABLE | 87m | 47m | 4 |
| GROK | 176m | 68m | 7 |
| CODEX | 175m | 65m | 8 |
| HAIKU | 251m | **163m** | 20 |

These observations motivate measuring review time and retries separately.
They do not prove that model choice cannot affect time per review round.

Do not use a fixed review-time multiplier or retry threshold as a quality gate.
Re-measure representative work before adopting a routing threshold.

## 2. A historical failure on a large seals task

**MEASURED, and this is the strongest negative result in the data.** On a
315-line contract Haiku ran 20 iterate rounds, 251m dev, 163m of reviewer time,
the most output tokens of any arm (458k) and 76M cache reads — and produced
**no seal file at all**. It iterated the maximum and delivered nothing.

Haiku remains right for bounded mechanical work: `spawn.py` uses it to summarise
a log tail, which is the shape it suits.

This is a reason to evaluate task fit, not enough evidence for a universal
model/size prohibition. Keep critical acceptance requirements independent of
the selected implementer.

## 3. Pin the model AND the effort explicitly. Never leave either default.

**MEASURED, twice over.**

* All four dogfood-go scaffolds ran on Haiku because nobody pinned them; the
  resulting contract took 7 dispatched + ~27 hand rounds to converge.
* `codex` accepts six effort levels (low|medium|high|xhigh|max|ultra) and its
  own default is **low**. The dispatcher knew three, so an authored `xhigh` was
  validated down to `high` — two tiers lost silently.

**Actionable:** every row carries `agent:`, `model:` and `effort:`. Treat an
unpinned row as a bug. `AGENT_EFFORTS` in `plan.py` now refuses an effort the
agent cannot take rather than downgrading it.

## 4. Use `--stay-in-family` whenever you will compare or attribute the work.

**MEASURED.** The quality cascade resets the worktree and lets a stronger family
redo a blocked task — correct for shipping, fatal for attribution. Three arms
had their output replaced by `claude-opus-5[1m]` and recorded under the original
agent's name. Two earlier arms cascaded on a bad model id the same way.

**Actionable:** `--stay-in-family` for any run whose output you will measure,
compare, or attribute to a model. Leave it off for production runs where you
want the work finished by whoever can finish it.

## 5. Clause mentions are review hints, not coverage

**MEASURED.** All 13 mutations lived inside one of the contract's 12 clauses, so
every arm that delivered was reported as 11/13. Text searches produced the
following mention counts, which have not been validated as assertions:

| arm | clause mentions | seals | historical interpretation (not verified) |
|---|---|---|---|
| CODEX `gpt-5.6-sol@high` | 12/12 | 17 | assertion coverage unknown |
| FABLE `claude-fable-5-1` | 12/12 | 15 | $38.71; assertion coverage unknown |
| GROK `grok-4.6@high` | 12/12 | ~13 | $4.56; assertion coverage unknown |
| OPUS `claude-opus-5` | 6/12 | 5 | assertion coverage unknown |
| SONNET `claude-sonnet-5` | 4/12 | 2 | assertion coverage unknown |
| DEEPSEEK `deepseek-v4-pro` | 3/12 | — | assertion coverage unknown |

Number the clauses and check actual assertions against them. Per-clause
negative controls can test whether violations are detected. The text search in
`features/model-matrix/report.py` does not perform that verification.

## 6. Historical picks, not a validated routing recommendation

**MEASURED at n=1 per model — see the caveat below.**

| use | pick | why |
|---|---|---|
| default, seals/scaffold | `codex` / `gpt-5.6-sol` @ `high`+ | historical choice based partly on unverified mention counts |
| cost-sensitive candidate | `grok` / `grok-4.6` @ `high` | historical cost observation; equivalent coverage not established |
| first-pass candidate | `claude` / `claude-opus-5` | historical time observation; coverage not established |
| adjudication, review seats | `claude` / `claude-fable-5-1` | historical choice; reviewer effectiveness not measured here |
| bounded mechanical work | `claude-haiku-*` | log summaries, single-file edits |
| further evaluation needed | all candidates on representative critical paths | no universal exclusion or safety claim follows from this study |

## 7. What would change these rules

**OPEN.** Be sceptical of rule 6 in particular.

* **n=1 per model.** Two arms of the SAME model, same brief, differed 1.9x in
  seal count and inverted on the headline metric. The replicate study (7 models
  x 5 runs) exists to quantify that noise floor; until it lands, rule 6 is
  single samples.
* **One task shape.** Writing tests against a tight contract. Nothing here
  tests design under ambiguity, integration with existing code, or a domain
  where being wrong costs money.
* **Clause coverage is grep-based.** It proves a suite MENTIONS a clause, not
  that the assertion is correct. Per-clause mutations would verify it.
* **Kill rate saturated at 11/13.** On a well-specified contract, model choice
  made no measurable difference to defect detection across a 7x price range.
  The original instrument and results need validation before comparison.

## 8. The rule that outranks model choice

**MEASURED.** Two of 13 mutations were killed by NO arm:
`empty-digest-allowed` and `invalid-input-exits-ok`. Nobody was asked to seal
them, so nobody did.

Specification quality and shared blind spots deserve their own controls.
The limited, instrument-affected observations do not establish that model
choice is irrelevant to defect detection.

**Spend the effort on the specification first.** Model routing is a
second-order optimisation on top of it.
