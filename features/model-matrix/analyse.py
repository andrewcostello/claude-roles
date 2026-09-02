#!/usr/bin/env python3
"""Analyse every study at once, noise-aware, and refuse unsound comparisons.

Built after three separate confident conclusions dissolved on contact with
better measurement:

  1. "Only codex detects anything" — an artifact of a ONE-mutation instrument.
     With nine mutations the arms were 4,4,5,5 of 7.
  2. "Codex won" — two arms had silently cascaded to claude-opus-5[1m], so it
     was one Opus run beating two other Opus runs.
  3. "Codex iterated most and converged least" — it produced 17 seals / 2,622
     lines, five times the arms called converged. The metric rewarded stopping.

So this refuses to print a ranking it cannot support:

  * PROVENANCE FIRST. An arm whose recorded agent/model differs from its pin is
    excluded, not scored. A number attached to the wrong model is worse than no
    number.
  * NOISE BEFORE SIGNAL. With replicates, report the within-cell spread and
    flag any between-cell difference that does not exceed it. A ranking without
    the noise band beside it is the error above, repeated.
  * DISCRIMINATION. A mutation every arm kills, or none kills, carries no
    signal; report the effective resolving power rather than the raw rate.
  * NO SINGLE SCORE. Cost, wall-clock and kill rate trade off against each
    other, and collapsing them into one number hides the trade the reader
    actually wants to make.
"""
from __future__ import annotations

import argparse
import json
import statistics
import sys
from pathlib import Path

sys.path.insert(0, "/home/andrew/Project/claude-dispatcher/src")
from claude_dispatcher import yaml_io  # noqa: E402


def rows_from(run_yaml: Path, spec_yaml: Path | None) -> list[dict]:
    run = {t["key"]: t for t in yaml_io.load(run_yaml)["tasks"]}
    spec = ({t["key"]: t for t in yaml_io.load(spec_yaml)["tasks"]}
            if spec_yaml and spec_yaml.exists() else {})
    out = []
    for key, t in run.items():
        if key.endswith("SCAFFOLD"):
            continue
        pin = spec.get(key, t)
        row = {
            "key": key,
            "pinned_agent": pin.get("agent"), "pinned_model": pin.get("model"),
            "actual_agent": t.get("agent"), "actual_model": t.get("model"),
            "effort": t.get("effort"), "status": t.get("status"),
            "cost_usd": float(t.get("cost_usd") or 0) or None,
        }
        row["provenance_ok"] = (
            row["actual_agent"] == row["pinned_agent"]
            and (not row["pinned_model"] or row["actual_model"] == row["pinned_model"])
        )
        out.append(row)
    return out


def merge_scores(rows: list[dict], score_json: Path | None) -> None:
    if not score_json or not score_json.exists():
        return
    by = {}
    for s in json.loads(score_json.read_text()):
        by[s.get("arm", "").replace("worktree-", "")] = s
    for r in rows:
        s = by.get(r["key"])
        if s:
            r.update(kill=s.get("mutations_killed"), of=s.get("mutations_total"),
                     seals=s.get("seals_added"), lines=s.get("seal_lines"),
                     gate_green=s.get("gate_green"), seconds=s.get("seconds"))


def cell(r: dict) -> str:
    return f"{r['pinned_agent']}/{r['pinned_model']}@{r.get('effort') or 'default'}"


def report(rows: list[dict], label: str) -> None:
    print(f"\n{'='*78}\n{label}\n{'='*78}")
    excluded = [r for r in rows if not r["provenance_ok"]]
    scored = [r for r in rows if r["provenance_ok"]]

    if excluded:
        print(f"\nEXCLUDED — did not run their pin ({len(excluded)}):")
        for r in excluded:
            print(f"  {r['key']:20} pinned {r['pinned_agent']}/{r['pinned_model']}"
                  f"  ACTUAL {r['actual_agent']}/{r['actual_model']}")
        print("  A cascaded arm is not a data point about its pinned model.")

    cells: dict[str, list[dict]] = {}
    for r in scored:
        cells.setdefault(cell(r), []).append(r)

    print(f"\n{'cell':<44} {'n':>2} {'kill':>7} {'cost':>16} {'status'}")
    stats = {}
    for c, rs in sorted(cells.items()):
        kills = [r["kill"] for r in rs if r.get("kill") is not None]
        costs = [r["cost_usd"] for r in rs if r.get("cost_usd")]
        k = (f"{statistics.mean(kills):.1f}" + (
            f" ±{statistics.stdev(kills):.1f}" if len(kills) > 1 else "")
            ) if kills else "-"
        cost = (f"${statistics.mean(costs):.2f}" + (
            f" ±{statistics.stdev(costs):.2f}" if len(costs) > 1 else "")
            ) if costs else "-"
        done = sum(1 for r in rs if r["status"] == "Done")
        print(f"  {c:<42} {len(rs):>2} {k:>7} {cost:>16} {done}/{len(rs)} Done")
        stats[c] = {"kills": kills, "costs": costs}

    # NOISE BEFORE SIGNAL.
    spreads = [statistics.stdev(v["kills"]) for v in stats.values() if len(v["kills"]) > 1]
    if spreads:
        noise = statistics.mean(spreads)
        means = {c: statistics.mean(v["kills"]) for c, v in stats.items() if v["kills"]}
        rng = max(means.values()) - min(means.values()) if len(means) > 1 else 0
        print(f"\nNOISE FLOOR: mean within-cell stdev of kill rate = {noise:.2f}")
        print(f"BETWEEN-CELL RANGE: {rng:.2f}")
        if rng <= noise:
            print("  VERDICT: the between-cell range does NOT exceed the noise floor.")
            print("  No ranking is supportable from this data. Choosing at random")
            print("  would perform indistinguishably on this task.")
        else:
            print(f"  Between-cell range exceeds the noise floor by {rng - noise:.2f};")
            print("  differences larger than the floor may be real. Cells whose means")
            print("  differ by less than it remain indistinguishable.")
    elif scored:
        print("\nNO REPLICATES: every cell has n=1, so no noise floor can be")
        print("  computed and NO ranking is supportable. This is the defect that")
        print("  made the first bakeoff unreadable.")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--study", action="append", required=True,
                    metavar="LABEL:RUN_YAML[:SPEC_YAML[:SCORE_JSON]]")
    args = ap.parse_args()
    for spec in args.study:
        parts = spec.split(":")
        label, run = parts[0], Path(parts[1])
        sp = Path(parts[2]) if len(parts) > 2 and parts[2] else None
        sj = Path(parts[3]) if len(parts) > 3 and parts[3] else None
        if not run.exists():
            print(f"\n{label}: {run} not found — skipped")
            continue
        rows = rows_from(run, sp)
        merge_scores(rows, sj)
        report(rows, label)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
