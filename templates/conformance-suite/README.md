# conformance-suite — template

Start a data-model design's suite from here, not from a blank file.

| File | Copy to | What it is |
|---|---|---|
| `verify-operations.template.sh` | `<design>/verify-operations.sh` | the helpers (`op` / `refuse` / `mutant`, invariants re-checked after every operation, `rpsql` that disarms only the registry guard in one transaction, the registry-to-trigger installer, the census as the last operation) with four sections to fill |
| `check-claims.py` | run per round | the claims ledger: every "what changed" cell in a disposition names something that exists in the suite |
| `round-metrics.py` | run per round | appends the round's verdicts, counts and suite census to `docs/reviews/metrics.jsonl` |
| `load-harness.md` | read before `03-load-results.md` | the shape of the load harness and the rules it taught |

Worked examples: `ep2.0/wallet/verify-operations.sh` (the first) and
`ep2.0/leaderboard/verify-operations.sh` (336 operations, 90 mutants, the
executable registry — the origin of these helpers). The loop that uses them
is `skills/design-iteration.md`; the reviewer is `roles/design-reviewer.md`.
