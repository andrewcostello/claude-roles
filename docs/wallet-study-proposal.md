# Next study: wallet reserve/settle, judged by a hidden test suite

## Why this task

The original study used one task shape and reported saturated mutation scores.
Its measurement defects and limited scope do not establish model equivalence.
This proposal adds a representative money-path task; it is not release approval.

Wallet §8.1 (reserve → settle) is different in the ways that matter:

* **The design is underdetermined.** The PRD states properties, not an
  implementation. There are real choices to make, so there is something to be
  better or worse at.
* **It is money.** Getting it wrong has a cost, and it exercises the
  dispatcher's financial machinery (`FINANCIAL_PATHS`, money-net, risk-path
  elevation, fail-closed gates) that no study has touched.
* **Its invariants are machine-checkable.** Double-entry, append-only, a hold
  that must settle for exactly what was claimed. These are assertions, not
  opinions.
* **The PRD names a specific defect to test for**, which is the seed of the
  oracle:

  > "a hold of 100 settled for 100 while the transaction debits 500 is
  > internally consistent, and the player has been charged five times what they
  > authorised"

## The change in method: a HIDDEN test suite

Write an independent suite and run it against each delivered implementation.
Give every arm the same public requirements, interfaces, and acceptance rules;
withhold test cases, not requirements. A hidden suite is an additional oracle,
not a replacement for mutation controls or domain-expert review.

Freeze source, brief, oracle, harness, runtime, and execution policy for each
comparison. If new cases are added, version the oracle and rescore every arm
against that version. Keep some cases held out from workflow tuning.

Validate the suite against a known-good implementation, known defects,
always-pass and always-fail controls, infrastructure failures, retries, and
concurrent operations. Isolation must prevent a candidate from reading or
editing the verifier. Hidden tests can still miss behavior, leak, or saturate;
neither immunity to gaming nor discrimination is guaranteed.

One limit, stated plainly: a hidden suite tests an IMPLEMENTATION, so the task
must deliver working code rather than tests. That rules it out for a seals task
and makes wallet the natural place to introduce it.

## The task

**Implement reserve, settle, and expire against `wallet/sql/schemas.sql`.**

Given to every arm: the schema, PRD §3 (design principles) and §8.1, and the
table definitions for `reservation`, `transaction`, `entry`, `account`.

NOT given: the test suite, and the specific failure modes it probes.

## The hidden suite — what it asserts

Written from the PRD's own properties, one assertion per rung:

**Double-entry integrity**
- every transaction's entries sum to zero
- no transaction has fewer than two entries
- `entry.sequence_number` is gapless and unique per account

**Append-only**
- no UPDATE or DELETE against `entry` or `transaction` (checked by attempting one)
- a correction is a NEW transaction citing the original

**Hold semantics — the PRD's named defect**
- settling a hold of 100 for 100 moves EXACTLY 100, not 500
- settling for less than the hold releases the remainder
- settling for MORE than the hold is refused
- an expired hold restores spending power and moves nothing
- a settled hold cannot be settled twice

**Balance-as-cache**
- `account.balance` equals the sum of its entries at every commit
- a balance written without a corresponding entry is detected

**Idempotency**
- the same `idempotency_key` replayed returns the original result and creates
  no second transaction
- a DIFFERENT payload under the same key is refused rather than silently
  returning the first

**Play/real separation**
- no transaction has entries in both a play-money and a real-money account

## Scoring

Report each critical invariant separately. A critical failure is not averaged
away by unrelated passes. Missing execution, timeouts, infrastructure errors,
and abandoned attempts remain explicit outcomes, not passing negative controls.
Report ordinary pass rates, mutation controls, rounds, development and review
time, tokens, and cost separately, with repeated trials and exact evidence
bindings. A passing score alone does not authorize a release.

Plus one new column this task makes possible: **assertions the arm satisfied
that no other arm did**, which is where genuine design difference shows.

## What this study can and cannot settle

CAN: whether models differ on underdetermined design work in a domain with hard
invariants; whether the noise floor is smaller than the between-model gap on a
harder task; whether cheap-many-rounds beats expensive-few-rounds when quality
is measured independently.

CANNOT: anything about tasks unlike this one. Two task shapes is not a taxonomy.
