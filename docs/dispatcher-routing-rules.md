# Dispatcher routing rules

Written 2026-09-02 from the model studies in `features/model-matrix/`. Each rule
is marked **MEASURED** (from data in this repo), **INFERRED** (a defensible
reading, not directly tested), or **OPEN** (would change the rule if measured).

The studies used one task shape: writing seals against a tight contract. Rules
about *that* shape are strong; rules about design work are inferred.

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

No model choice reduces review time. Only fewer rounds does. **So a model that
covers more of the contract on its first pass is worth more than a faster one**,
and the fastest implementer can be the most expensive overall.

**Actionable:** budget wall clock as `dev x 1.4`, and treat any arm needing more
than ~4 rounds as a routing mistake rather than a slow success.

## 2. Never pin Haiku to a task above `size:S`.

**MEASURED, and this is the strongest negative result in the data.** On a
315-line contract Haiku ran 20 iterate rounds, 251m dev, 163m of reviewer time,
the most output tokens of any arm (458k) and 76M cache reads — and produced
**no seal file at all**. It iterated the maximum and delivered nothing.

Haiku remains right for bounded mechanical work: `spawn.py` uses it to summarise
a log tail, which is the shape it suits.

**Actionable:** `model: claude-haiku-*` only on rows labelled `size:XS`/`size:S`
with a single mechanical deliverable. Never on scaffold, seals, or bodies.

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

## 5. Judge a test suite by clause coverage, not by kill rate or size.

**MEASURED.** All 13 mutations lived inside one of the contract's 12 clauses, so
every arm that delivered scored 11/13 — the metric saturated. Clause coverage
separated them cleanly:

| arm | clauses | seals | verdict |
|---|---|---|---|
| CODEX `gpt-5.6-sol@high` | **12/12** | 17 | complete |
| FABLE `claude-fable-5-1` | **12/12** | 15 | complete, $38.71 |
| GROK `grok-4.6@high` | **12/12** | ~13 | complete, **$4.56** |
| OPUS `claude-opus-5` | 6/12 | 5 | fast partial |
| SONNET `claude-sonnet-5` | 4/12 | 2 | looks cheap, 8 clauses unguarded |
| DEEPSEEK `deepseek-v4-pro` | 3/12 | — | thin |

A suite that is green, fast and cheap while guarding 4 of 12 clauses is not a
bargain — it is an unguarded contract that looks like one.

**Actionable:** number the clauses in your contract, and check the delivered
suite against that list before accepting it. `features/model-matrix/report.py`
does this mechanically.

## 6. Current picks, for contract-first Go work

**MEASURED at n=1 per model — see the caveat below.**

| use | pick | why |
|---|---|---|
| default, seals/scaffold | `codex` / `gpt-5.6-sol` @ `high`+ | 12/12 clauses, 0 staticcheck issues, kept iterating |
| cost-sensitive, same coverage | `grok` / `grok-4.6` @ `high` | 12/12 at $4.56 — cheapest complete arm |
| fast first pass you will review | `claude` / `claude-opus-5` | 6/12 in 17m, 0 rounds |
| adjudication, review seats | `claude` / `claude-fable-5-1` | judgement no gate can grade; 12/12 when implementing |
| bounded mechanical work | `claude-haiku-*` | log summaries, single-file edits |
| never | Haiku on `size:L`; Sonnet where the suite is the safety net | |

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
  That is a finding, but it also means this task cannot rank models on quality.

## 8. The rule that outranks model choice

**MEASURED.** Two of 13 mutations were killed by NO arm:
`empty-digest-allowed` and `invalid-input-exits-ok`. Nobody was asked to seal
them, so nobody did.

Across every study, the largest quality differences traced to the brief and the
contract — not to the model. A precise contract made a 7x price range
irrelevant to detection; an imprecise brief left holes every model shared.

**Spend the effort on the specification first.** Model routing is a
second-order optimisation on top of it.
