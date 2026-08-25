---
name: design-iteration
description: The loop for taking a DATA-MODEL DESIGN (pure-design repo, SQL + decisions + conformance suite) to sign-off before any code is built — gap analysis, owner rulings, decisions, suite, review rounds with a two-round stop rule, load results, memorialisation. Distinct from iteration-protocol.md, which is the code-review loop.
---

# Design Iteration

Load this skill when the task is a data model in a pure-design repo (the
`wallet/` and `leaderboard/` folders of `ep2.0` are the worked examples) that
must become the authoritative source a service is later built from. The
subject is SQL, a decisions document and a conformance suite — not code —
so the loop, the seats and the stop rule differ from `iteration-protocol.md`.

The template for the suite and its helpers is
`templates/conformance-suite/`; the reviewer role is
`roles/design-reviewer.md`; the writing style is `review-language.md`.

---

## The sequence

```
00-brief (given)
  ↓
01-gap-analysis.md      two axes: what is missing; what survives the volume
  ↓
STOP — the owner rules on the questions the analysis raises (tiers, scope, retention …)
  ↓
02-decisions.md         numbered, one decision per entry, with what was given up
sql/schemas.sql         loads clean
verify-operations.sh    from the template; green before the first round
  ↓
review rounds           1–2 full audit; 3+ changed-surface verification
  ↓ (stop rule below)
03-load-results.md      the measurements the decisions named BEFORE any number existed
  ↓
STOP — the owner rules on what the reviews and the load pushed the model into
  ↓
memorialise             the service document rewritten to the decisions; README; PR
```

Two stops, both mandatory. Everything between them is mechanical and must
not wait on the owner. Do not skip a stop because the answer seems obvious;
the wallet and the leaderboard both changed direction at one.

---

## Rules that came from the leaderboard run (2026-08-24/25, 13 rounds)

**Seats.** Two required seats (claude, codex); a round without both does not
count. Best-effort seats (grok, kimi, agy) are dropped on slowness or
non-compliance and the absence is written in the disposition. **Every seat
gets a read-only worktree and its own database snapshot** — a seat that
writes to the repository or runs the suite on a shared estate is withdrawn
from the panel (a seat once injected debug lines into the suite and ran it
under other seats' probes). Prompts say so, and the seat's first act is to
state its method.

**Rounds.** Rounds 1–2 are full audits over the six dimensions. Round 3+ is
targeted verification: for every accepted prior finding, RESOLVED /
STILL_OPEN / REGRESSED with a line citation or a probe; new findings only
in what changed. Each round ends with `disposition.md`: accepted (what
changed, by decision number), rejected with the reason, deferred with where
it lands, seat notes. The prompt for round N+1 links round N's disposition.

**Stop rule.** Stop when **both required seats report no new BLOCKING or
MAJOR for two consecutive rounds, and one of those two is a full re-audit,
not a changed-surface pass.** One clean round is not enough: rounds 10–13
of the leaderboard each found the edge of the previous round's fix. A
seat's ESCALATE on a STILL_OPEN item that is a missing sentence or a
mutant is answered in the next revision, not by another full round.

**The claims ledger.** Every "what changed" cell in a disposition names an
operation or an invariant that exists in the suite, by its title or its
message. `templates/conformance-suite/check-claims.py` fails the round if a
named thing is not in the suite. A disposition is not allowed to say
"stated" or "covered" without the thing behind it.

**The census.** The suite's last operation counts its own invariant
branches and mutants and fails if any branch has no mutant that names it
alone, or if a mutant's fragment matches two branches. The count in the
decisions is what the run prints, never a number typed by hand.

**Concurrency in the suite, from round 3.** A single-operation suite cannot
see contention. The two most important leaderboard defects (a
transaction-start timestamp violating a monotone rule under lock waits; a
void rebuilding the whole board under the lock) were invisible to 336
operations and obvious to sixteen concurrent producers. Add the concurrent
section of the template (N producers, a correction, a reading mid-run) as a
standing operation before round 3.

**Load before memorialising, never disarmed.** The load measurements are
named in the decisions before any number exists, with their pass marks, so
a failed measurement changes numbers, not tables. Every run keeps every
constraint and the registry guard armed. Report where the model breaks and
at what volume; report what was not measured, per store, at the end.

**Mutants disarm the guard and nothing else.** A mutant is what a bug does
behind the guard, not behind the database: foreign keys and CHECKs stay on;
restores are exact (keep tables), and the harness re-arms from `pg_trigger`,
not from the table under mutation.

**Aging time in the estate.** When the suite must move a clock, it moves the
whole fact (the board's clock, its resolve, and the record of the resolve)
behind the guard, in one statement. A shortcut that is also a legal
production transition is a hole a reviewer will walk through.

**Two commits per revision.** After each revision: run the suite twice
(idempotence), back up the schema and the suite to the scratchpad,
regenerate `dbdoc`, then write the disposition. Nothing is committed until
the owner says so; when they do, one commit for the design, one for any
operating note, seat logs excluded.

---

## Metrics — what the loop records so the process can be judged

Append one line per round to `docs/reviews/metrics.jsonl` with
`templates/conformance-suite/round-metrics.py` (it reads the seat files and
the suite's census line):

```json
{"round": 7, "revision": "3.4", "seats": {"codex": {"verdict": "ITERATE", "blocking": 1, "major": 2, "minor": 0, "still_open": 0, "regressed": 0}, "claude": {...}},
 "suite": {"operations": 232, "mutants": 65, "invariants": 53, "registry_refusals": 16},
 "accepted": 12, "rejected": 0, "deferred": 1, "minutes": 48}
```

The numbers that say whether the process is improving, project over project:

| Number | Leaderboard (2026-08) | Target for the next design |
|---|---|---|
| rounds to the stop rule | 13 (+1 for a ruling) | 6–7 |
| rounds with a REGRESSED finding | 0 | 0 |
| defects found by load that the suite could not | 2 | 0 (the concurrent section catches them first) |
| dispositions with a claim the ledger check would have failed | 1 | 0 |
| seat incidents (writes to the repo, shared-estate runs) | 1 | 0 |
| harness iterations before the first load number | 6 | 1–2 (start from the template) |

At the end of a design, write `docs/reviews/retro.md`: the table above with
the real numbers, the three findings that took longest to surface and why,
and what to add to this skill, the template or the reviewer role. The next
design starts by reading the last retro.

---

## Prompt shape for a round (copy, fill the brackets)

```
You are reviewing a DATA-MODEL DESIGN, not code — ROUND [N]. Adopt
roles/design-reviewer.md and write in the style of skills/review-language.md.
Work inside [repo]/[folder]. You have a read-only worktree and your own
database snapshot at [DSN]; wrap any probe that writes in a transaction you
ROLLBACK; do NOT modify or create any file in the repository; do NOT run the
suite; leave no background process waiting.

Round [N] is [full audit | targeted verification of the [K] accepted round-[N-1]
findings in docs/reviews/round-[N-1]/disposition.md].
Subjects: docs/02-decisions.md, sql/schemas.sql, verify-operations.sh
([counts as the census printed them], green twice).

Do exactly this: (1) [for each accepted finding: RESOLVED / STILL_OPEN /
REGRESSED with evidence]; (2) [accept or contest each rejection and deferral];
(3) hunt for NEW findings ONLY in what changed: [the changed surface, named].
A CHECK satisfied by NULL, an assertion that passes on empty input, an
invariant with no mutant, a mutant that would survive if the invariant were
deleted, a predicate that permits a forbidden transition or refuses a needed
one, or a decision claiming what the DDL does not deliver are findings.
(4) Do NOT re-litigate what is approved.

End with counts, a verdict of APPROVE / ITERATE / ESCALATE, and the three
highest-leverage remaining changes if not APPROVE.
```
