---
name: explicit-state
description: No implicit states. Every state a decision depends on must be nameable and named — never an absence, a default, or a falsy value. Load when writing or reviewing code at a gate, guard or verdict boundary.
---

# No Implicit States

> **Every state a decision depends on must be nameable, and named.**
> "I don't know" is a state. If it can't be told apart from "the answer is no",
> the gate has already failed and nobody will notice.

This is a project-wide constraint, not a style preference. It exists because the
same defect keeps recurring in different costumes, and every instance has the
identical shape: **a value that means "no answer" is indistinguishable from a
value that means "a passing answer."**

---

## The evidence it comes from

Every one of these was a real defect found in this system, all within a week:

| Where | The implicit state | What it cost |
|---|---|---|
| `classify_diff() -> Classification \| None` | `None` meant *not installed*, *failed*, **and** *no diff* | A crashed binary read as "no classifier here" → weaker gating, change self-approved |
| `_rank(risk)` returning `0` on an unknown tier | An unrecognised tier silently became the **weakest** one | `{}` produced a confident "low risk" that walked past a fail-closed guard |
| `parse_classification` coercing policy fields | `bool("false")` is `True` | A producer emitting a JSON string inverted the panel decision |
| `panel_required` duck-typing via `getattr` | A `ClassifyResult` has no `requires_full_panel`, so it read as `False` | A **critical, financial** diff returned "skip the panel" |
| `gremlins unleash ./...` | Matched nothing, printed "No results to report", **exited 0** | A mutation gate that had never run reported success for months |
| Coverage gate matching zero packages | Reported `worst_coverage_pct: 100` | A 53.8%-covered module passed a coverage check that evaluated nothing |
| A gate recorded `skipped` with no reason | Absence of a reason read as "checked and fine" | Indistinguishable from a pass in the run state |
| A test fixture emitting only `{"risk": "low"}` | Omission read as "the rest doesn't matter" | Four fixtures encoded the permissiveness they were meant to catch |

Note the last row. **Tests are subject to this rule too** — a fixture that omits
a field is asserting that the field is optional, whether or not anyone meant to.

---

## The rule

At any **decision boundary** — a gate, a guard, a verdict, an authorization
check, anything whose output changes what runs next:

1. **Enumerate the states.** If a function can answer "yes", "no" and "I
   couldn't tell", that is **three** states. Not two-plus-null.
2. **Name them.** A distinct constant, variant or status field per state.
   `CLASSIFY_OK` / `CLASSIFY_ABSENT` / `CLASSIFY_FAILED`, not `T | None`.
3. **Be exhaustive, and raise on the unknown.** See below — this is the step
   that actually bites, and naming the states does not give it to you for free.
4. **Make the caller handle them.** A caller that would *relax* a gate must be
   unable to conflate "failed" with "fine" — ideally because the type won't let
   it compile or the field won't parse.
5. **Never default a missing input to the permissive value.** No `risk` key is
   not `"low"`. No coverage line is not `100%`. No findings is not `APPROVE`.
6. **Validate, don't coerce.** A policy-bearing boolean must *be* a boolean.
   Coercion turns a producer bug into a silently inverted decision.

### Naming the states is not enough

> **Naming three states does nothing if a fourth can arrive and be silently
> treated as the permissive one.**

This is the failure mode that survives a first, careful attempt at the rule —
learned by writing this document and then violating it within the hour, in the
commit that applied it.

The fix under review had replaced `Classification | None` with a named
three-state `ClassifyResult`. Every state had a constant. The dispatch was:

```python
# WRONG — and it looks like it followed the rule
if hasattr(x, "status") and hasattr(x, "classification"):
    if getattr(x, "failed", False):
        return True
    inner = x.classification
    return inner is not None and inner.requires_full_panel
```

Three real inputs returned "skip the review gate":

| Input | Why it slipped through |
|---|---|
| `SimpleNamespace(status=..., classification=...)` | Matched on **shape**, so a lookalike walked in |
| `ClassifyResult(status="future-status")` | An unrecognised status fell out of the bottom as permissive |
| `ClassifyResult(status=OK, classification=None)` | Internally inconsistent, so `inner is not None` was False |

Two of those are *canonical* instances of the right type. So:

- **`isinstance`, never `hasattr`.** Structural matching is a type check's
  clothes, not a type check. `getattr(x, "attr", default)` on a union is the
  same mistake with a default attached.
- **Handle every status by name, and `raise` on one you don't recognise.** A
  `switch` whose default arm is the permissive branch has re-created the
  implicit state you just removed. The unknown case is not hypothetical: it is
  a future status, a partial rollout, an older build reading a newer producer.
- **Reject internally inconsistent combinations.** `status=OK` with no payload
  is not "no panel required" — it is a bug, and it must say so.
- **Do not over-correct into "always block".** `ABSENT` and `EMPTY` genuinely
  mean *no evidence either way*; forcing the gate on those makes it useless in
  the deployment it was designed to degrade into. Exhaustive means each state
  gets the **right** answer, not the same answer.

### Sealing it

The seal for this must use a **structural lookalike**, not a wrong-typed value.
The original test passed a string — which lacks the probed attributes and so
never reached the broken branch. It tested the easy case while three real shapes
failed open.

Add an exhaustive-status test, so a status added later without updating the
predicate fails the suite rather than the gate. And falsify: revert the fix, run
the test, confirm it fails.

### Where it binds, and where it doesn't

This is a rule about **decision boundaries**, not about every function in the
codebase. `Optional[str]` for a display name is fine. The constraint applies
when a wrong answer means a check does not happen:

- risk classification, panel gating, merge authorization
- verification gates and their statuses
- anything reading a policy file or rule table
- anything whose absence would let a caller skip work

Applying sum types everywhere else is ceremony, and ceremony gets ignored, which
weakens the rule where it matters. Be strict at the boundary and ordinary
elsewhere.

---

## Patterns

**Go** — an explicit status alongside the value, and a helper that forces the
question:

```go
type ClassifyResult struct {
    Classification *Classification
    Status         string // ok | absent | failed
    Detail         string
}

func (r ClassifyResult) Failed() bool { return r.Status == StatusFailed }
```

Prefer a status field over a bare `(T, error)` when *why there is no value*
changes the caller's decision — an error that callers `if err != nil { return }`
past is another implicit state.

**Python** — same shape; do not lean on `Optional` or duck typing:

```python
@dataclass(frozen=True)
class ClassifyResult:
    classification: Classification | None = None
    status: str = CLASSIFY_ABSENT
    detail: str | None = None

    @property
    def failed(self) -> bool: ...
```

**Never `getattr(x, "attr", False)` on a union.** That is duck typing standing
in for a type check, and the default is a silent implicit state. If a function
accepts two shapes, either normalise at the boundary or take one shape.

**Config and wire contracts** — require the fields the producer emits
unconditionally; reject wrong types rather than coercing. An absent field is
only acceptable when absence has exactly one meaning, and say so in a comment.

---

## Reviewing for it

Three questions, in order of how often they find something:

1. **What does absence mean here?** Follow every `None`, `nil`, zero value,
   empty collection and missing key to its consumer. If two different causes
   produce the same value and the consumer branches on it, that is a finding.
2. **Could this pass without doing anything?** Zero packages evaluated, zero
   mutants generated, zero findings, empty output, a skipped step. If "did
   nothing" and "succeeded" look the same to the caller, that is a finding.
3. **Does the seal exercise the production path?** Revert the fix and re-run its
   test. If the test still passes, the seal is vacuous — see `reviewer.md`
   Step 4. Fixtures that omit fields assert those fields are optional.

Severity: an implicit state at a gate that controls money, auth or merge is
**CRITICAL or HIGH**, not a style note. Elsewhere it is a MEDIUM at most.

---

## When you find one

Fixing the call site is usually the wrong move — the ambiguity is in the
*representation*, and guarding each consumer leaves the next one exposed. Two
rounds of patching this exact class in `classification.py` each introduced a new
fail-open at a different seam, because the type kept permitting the mistake.

Change the type. Then check every consumer, because narrowing a representation
is a breaking change and the compiler will not always tell you.
