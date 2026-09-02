#!/usr/bin/env python3
"""Objective scorecard for a seals arm. No taste, no panel verdict.

Seals are unusually measurable, so measure them rather than judging them:

  MUTATION KILL RATE is the headline. Apply each plausible defect from
  mutations.json to production code and ask whether this arm's suite reddens.
  This is the only number that answers "do these tests detect anything", which
  a panel verdict does not — an arm can be panel-approved and kill nothing, or
  panel-blocked and kill everything.

  VACUITY: with the body absent, a seal that PASSES is asserting something the
  wiring does not decide. Counted, not judged.

  Complexity, staticcheck, verbosity and normalised findings are secondary and
  reported for completeness; a test file wants LOW complexity, so gocyclo is
  read here as a smell rather than an achievement.

A mutation no arm kills is a hole in the brief, not in the arm. A mutation
every arm kills carries no signal. Both are reported so the suite can be
judged alongside the arms.
"""
from __future__ import annotations

import argparse
import json
import re
import statistics
import subprocess
import time
from pathlib import Path


def _failing(mod: Path) -> set[str]:
    p = subprocess.run(["go", "test", "./..."], cwd=mod,
                       capture_output=True, text=True, timeout=900)
    return set(re.findall(r"^\s*--- FAIL: (\S+)", p.stdout, re.M))


def _passing(mod: Path) -> set[str]:
    p = subprocess.run(["go", "test", "-v", "./..."], cwd=mod,
                       capture_output=True, text=True, timeout=900)
    return set(re.findall(r"^\s*--- PASS: (\S+)", p.stdout, re.M))


def score_arm(worktree: Path, module: str, muts: dict) -> dict:
    mod = worktree / module
    out: dict = {"arm": worktree.name}
    if not mod.exists():
        return {**out, "error": "no module"}

    t0 = time.time()
    base_fail = _failing(mod)
    out["baseline_failing"] = len(base_fail)
    out["gate_green"] = not base_fail

    # Vacuity: with GO-1-3's body absent, a seal that passes is not watching the
    # wiring. Only rows this arm added are counted.
    seal_file = mod / "wiring_seal_test.go"
    added = set()
    if seal_file.exists():
        added = set(re.findall(r"^func (Test\w+)", seal_file.read_text(), re.M))
    out["seals_added"] = len(added)
    out["seals_passing_with_no_body"] = len(_passing(mod) & added)

    killed, survived = [], []
    for m in muts["mutations"]:
        target = mod / m["file"]
        text = target.read_text()
        if m["before"] not in text:
            survived.append({"id": m["id"], "note": "site absent"})
            continue
        target.write_text(text.replace(m["before"], m["after"], 1))
        try:
            now = _failing(mod)
        finally:
            target.write_text(text)
        (killed if (now - base_fail) else survived).append({"id": m["id"]})
    out["mutations_total"] = len(muts["mutations"])
    out["mutations_killed"] = len(killed)
    out["kill_rate"] = round(len(killed) / max(1, len(muts["mutations"])), 3)
    out["killed_ids"] = [k["id"] for k in killed]
    out["survived_ids"] = [s["id"] for s in survived]

    if seal_file.exists():
        src = seal_file.read_text().split("\n")
        out["seal_lines"] = len(src)
        out["lines_per_seal"] = round(len(src) / max(1, len(added)), 1)
        cyc = subprocess.run(["gocyclo", "-avg", str(seal_file)],
                             capture_output=True, text=True)
        mm = re.search(r"Average: ([\d.]+)", cyc.stdout)
        out["avg_cyclomatic"] = float(mm.group(1)) if mm else None
        sc = subprocess.run(["staticcheck", "./..."], cwd=mod,
                            capture_output=True, text=True)
        out["staticcheck_issues"] = len([l for l in sc.stdout.splitlines() if l.strip()])
    out["seconds"] = round(time.time() - t0, 1)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--mutations", default="features/model-matrix/mutations.json")
    ap.add_argument("--worktree-base", default="/home/andrew/Project")
    ap.add_argument("--arms", required=True, help="comma-separated worktree suffixes")
    ap.add_argument("--json-out")
    args = ap.parse_args()

    muts = json.loads(Path(args.mutations).read_text())
    rows = [score_arm(Path(args.worktree_base) / f"worktree-{a.strip()}",
                      muts["module"], muts)
            for a in args.arms.split(",") if a.strip()]

    print(f"{'arm':<22} {'kill':>6} {'seals':>6} {'vacuous':>8} {'gate':>6} "
          f"{'cyc':>5} {'l/seal':>7}")
    for r in rows:
        if r.get("error"):
            print(f"{r['arm']:<22} {r['error']}")
            continue
        print(f"{r['arm']:<22} {r['mutations_killed']}/{r['mutations_total']:<4} "
              f"{r['seals_added']:>6} {r['seals_passing_with_no_body']:>8} "
              f"{'green' if r['gate_green'] else 'red':>6} "
              f"{str(r.get('avg_cyclomatic','-')):>5} {str(r.get('lines_per_seal','-')):>7}")

    scored = [r for r in rows if not r.get("error")]
    if scored:
        every = set.intersection(*[set(r["killed_ids"]) for r in scored]) if scored else set()
        none_ = set.intersection(*[set(r["survived_ids"]) for r in scored]) if scored else set()
        print(f"\nkilled by EVERY arm (no signal): {sorted(every) or '-'}")
        print(f"killed by NO arm (hole in the brief, not the arms): {sorted(none_) or '-'}")
    if args.json_out:
        Path(args.json_out).write_text(json.dumps(rows, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
