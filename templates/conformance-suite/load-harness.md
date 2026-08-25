# The load harness — shape, and the rules it taught

The worked example is `ep2.0/leaderboard/load/main.go` (Go; go-redis, pgx),
with `README.md` beside it. One program, one subcommand per measurement the
decisions named, one JSON object per run on stdout, progress on stderr.

Rules that came out of building it:

- **The measurements are named in the decisions before any number exists**,
  with their pass marks (a rate, a p99, a wall time). A failed measurement
  changes numbers, not tables.
- **Never disarm anything.** Every run keeps every constraint and the
  registry guard armed. The audited path calls the suite's reference
  functions and nothing else — what is measured is the model's own write
  path under its own guards, not a shortcut.
- **Find the ceiling, then measure at the workload.** An unthrottled run
  shows where the model breaks and at what volume; a throttled run at the
  named rate (and at 4× it) gives the p99 the mark is about. Report both.
- **Concurrency is the point.** N producers, a correction racing them, a
  reading taken mid-run; afterwards reproduce the reading at its fence and
  compare the aggregate with the fold. Count deadlocks and retry once on
  the apply side if the decisions say so; count everything else as an
  error and fail fast on a systematic one.
- **Estate is not timed.** Build the world with COPY, then start the clock.
- **Type every parameter** (`$2::timestamptz`), give every board its own
  anchor, name things with `fmt.Sprintf` not SQL concatenation — the six
  harness iterations before the first number were all of that class.
- **One host means one host.** Say so; the cost per operation is real, the
  ceilings are that host's.
- **Where it broke goes in the results and in the decisions** with a
  `[rN]` marker: the load run is a review seat that reads contention.
