# Repair gate-selection acceptance

The Go command in `/app/gates` runs required checks from a project configuration
and records their outcomes. Its `-only` option can currently produce a successful
aggregate result without running required checks. Repair that behavior.

Preserve the CLI and existing successful full-run behavior. Required behavior:

- Unknown, empty, or non-applicable names in a nonempty `-only` argument are
  invalid input, including during `-dry-run`. Exit 3 without executing commands
  or changing the input state.
- A plan with no applicable checks is invalid input, not acceptance.
- A valid partial run executes the selected checks, records omitted required
  checks as failures, and exits 1. An explicit named waiver with a reason may
  retain the existing waiver behavior.
- Full successful runs exit 0. An executed failing check still exits 1.
- Failed, invalid, and dry runs must not print `GATES: PASS`.
- Whitespace around names and duplicate names may be normalized.

Add appropriate tests. Do not weaken other verification behavior. The submitted
work product is the source tree at `/app/gates`; prose claims and agent-written
reward files are not acceptance evidence. External acceptance tests run against
a freshly compiled command in a separate environment.
