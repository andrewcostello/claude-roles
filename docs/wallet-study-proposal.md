# Next study: wallet reserve/settle, judged by a hidden test suite

## Why this task

Every study so far used one shape — writing tests against a tight contract —
and it saturated: all arms that delivered scored 11/13 mutations, and a 7x price
range made no measurable difference to detection. That task cannot rank models
because the contract did the hard work.

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

Andrew's proposal, and it is better than the mutation approach it replaces.

Mutation testing breaks production code and asks whether an arm's own tests
notice. It can only probe what the production code expresses — which is why all
13 mutations landed inside one of twelve contract clauses and the metric
saturated at 11/13 for everyone.

Instead: **we write the test suite ourselves, withhold it from every arm, and
run it against each delivery.** That is a genuine oracle rather than a proxy —
it measures "did this arm build something that satisfies requirements it never
saw", which is the actual question.

Three properties the mutation approach lacks:

1. **It grows.** When an arm handles a case the suite missed, the case is added.
   The oracle improves with each delivery instead of staying frozen at whatever
   we imagined first.
2. **It cannot be gamed.** An arm cannot write a test that passes vacuously,
   because it is not writing the tests.
3. **It discriminates by construction.** Pass rate against N independent
   assertions has as many rungs as we write, so no ceiling like 11/13.

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

Pass rate against the hidden suite is the headline, replacing mutation kill
rate. Alongside it, unchanged: rounds, dev vs review time, tokens, cost, and
whether the mechanical gate is green.

Plus one new column this task makes possible: **assertions the arm satisfied
that no other arm did**, which is where genuine design difference shows.

## What this study can and cannot settle

CAN: whether models differ on underdetermined design work in a domain with hard
invariants; whether the noise floor is smaller than the between-model gap on a
harder task; whether cheap-many-rounds beats expensive-few-rounds when quality
is measured independently.

CANNOT: anything about tasks unlike this one. Two task shapes is not a taxonomy.
