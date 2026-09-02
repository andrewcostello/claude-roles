#!/usr/bin/env python3
"""One table, every axis, with the columns that actually decide the choice.

Written because a table showing kill rate, seals, lines and cost pointed at the
WRONG ANSWER. On that table Opus looked best — 11/13 killed, cheapest of the
frontier tiers, 31 minutes, zero iterations. But Opus sealed 4 of the
contract's 12 clauses and left clause 7 (never os.Chdir, the one guarding a
process-global race in a shared test binary) completely unguarded. Sonnet
looked like a bargain at $5.55 while sealing 2 clauses. Codex sealed 10 and
scored identically, because all 13 mutations lived inside clause 4.

So the axes here are chosen for what changes a decision:

  CLAUSE COVERAGE — of the 12 numbered clauses in the contract, how many does
  this suite make a failable assertion about. THE headline. A suite that seals
  one clause perfectly is not 83% of a suite that seals ten.

  MUTATION KILL — do the assertions detect real defects. Necessary but, on this
  task, saturated: every arm that produced a file scored 11/13.

  DELIVERED — did a suite exist at all. Haiku burned the most output tokens of
  any arm and produced no seal file; a cost-per-token ranking would have put it
  first.

  WALL CLOCK / ROUNDS / TOKENS / COST — the price of the coverage, kept as
  SEPARATE columns. Collapsing them into one score hides the trade the reader
  came to make.

No single number. The honest output is a table plus a stated recommendation
with its reason, because "best" depends on whether the reader is optimising
for a merge tomorrow or a foundation for the next six months.
"""
from __future__ import annotations

import argparse
import collections
import datetime as dt
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, "/home/andrew/Project/claude-dispatcher/src")
from claude_dispatcher import yaml_io  # noqa: E402

HERE = Path(__file__).parent


def clause_coverage(seal_files: list[Path]) -> tuple[int, list[str]]:
    spec = json.loads((HERE / "clauses.json").read_text())
    text = "\n".join(f.read_text() for f in seal_files if f.exists())
    hit = []
    for c in spec["clauses"]:
        if any(re.search(p, text, re.I) for p in c["probe"]):
            hit.append(c["id"])
    return len(spec["clauses"]), hit


def journal_facts(run_dir: Path) -> dict[str, dict]:
    out: dict[str, dict] = collections.defaultdict(
        lambda: {"tok_in": 0, "tok_out": 0, "rounds": 0, "t": [], "model": None,
                 # Review and development time, split. Review is charged per
                 # ROUND and is paid whichever model implements, so it is
                 # overhead the model choice cannot reduce — only the round
                 # count can. Measured 27-50% of wall clock.
                 "rev_s": 0.0, "dev_s": 0.0, "_rev_open": None, "_dev_open": None})
    jf = run_dir / "journal.jsonl"
    if not jf.exists():
        return out
    for line in jf.read_text().splitlines():
        if not line.strip():
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        k = r.get("task_key")
        if not k:
            continue
        d, pl = out[k], (r.get("payload") or {})
        d["t"].append(dt.datetime.fromisoformat(r["timestamp"]))
        if r["event_type"] == "panel_started":
            d["_rev_open"] = d["t"][-1]
        elif r["event_type"] == "panel_verdict" and d["_rev_open"]:
            d["rev_s"] += (d["t"][-1] - d["_rev_open"]).total_seconds()
            d["_rev_open"] = None
        if r["event_type"] in ("task_started", "panel_iterate"):
            d["_dev_open"] = d["t"][-1]
        elif (r["event_type"] == "task_spawn_finished"
              and pl.get("spawn_kind") in ("implementer", "panel-iterate")
              and d["_dev_open"]):
            d["dev_s"] += (d["t"][-1] - d["_dev_open"]).total_seconds()
            d["_dev_open"] = None
        if r["event_type"] == "panel_iterate":
            d["rounds"] += 1
        if pl.get("spawn_kind") in ("implementer", "panel-iterate"):
            d["model"] = pl.get("model") or d["model"]
            d["tok_in"] += pl.get("input_tokens") or 0
            d["tok_out"] += pl.get("output_tokens") or 0
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--run-yaml", required=True)
    ap.add_argument("--run-dir", required=True)
    ap.add_argument("--score-json")
    ap.add_argument("--worktree-base", default="/home/andrew/Project")
    ap.add_argument("--module", default="cmd/classify")
    args = ap.parse_args()

    tasks = {t["key"]: t for t in yaml_io.load(Path(args.run_yaml))["tasks"]}
    facts = journal_facts(Path(args.run_dir))
    scores = {}
    if args.score_json and Path(args.score_json).exists():
        for s in json.loads(Path(args.score_json).read_text()):
            scores[s.get("arm", "").replace("worktree-", "")] = s

    rows = []
    for key, t in sorted(tasks.items()):
        if key.endswith("SCAFFOLD"):
            continue
        wt = Path(args.worktree_base) / f"worktree-{key}" / args.module
        seals = [wt / "wiring_seal_test.go"]
        total, hit = clause_coverage(seals)
        f, s = facts.get(key, {}), scores.get(key, {})
        mins = ((max(f["t"]) - min(f["t"])).total_seconds() / 60) if f.get("t") else None
        rows.append({
            "arm": key.replace("CV-", "").replace("RP-", "").replace("EF-", ""),
            "model": f.get("model") or t.get("model"),
            "delivered": (wt / "wiring_seal_test.go").exists(),
            "clauses": len(hit), "clauses_of": total, "hit": hit,
            "kill": s.get("mutations_killed"), "kill_of": s.get("mutations_total"),
            "seals": s.get("seals_added"), "lines": s.get("seal_lines"),
            "gate": s.get("gate_green"), "status": t.get("status"),
            "mins": mins, "rounds": f.get("rounds"),
            "dev_min": (f.get("dev_s") or 0) / 60 or None,
            "rev_min": (f.get("rev_s") or 0) / 60 or None,
            "tok_out": f.get("tok_out"), "cost": float(t.get("cost_usd") or 0) or None,
        })

    hdr = (f"{'arm':<10}{'model':<24}{'CLAUSES':>9}{'kill':>7}{'seals':>6}"
           f"{'dev':>6}{'review':>8}{'rev%':>6}{'rnds':>5}{'out tok':>10}"
           f"{'cost':>8}  status")
    print(hdr); print("-" * len(hdr))
    for r in rows:
        cl = "NONE" if not r["delivered"] else f"{r['clauses']}/{r['clauses_of']}"
        print(f"{r['arm']:<10}{str(r['model'] or '-'):<26}{cl:>9}"
              f"{(f'{r['kill']}/{r['kill_of']}' if r['kill'] is not None else '-'):>7}"
              f"{(r['seals'] if r['seals'] is not None else '-'):>6}"
              f"{(f'{r['dev_min']:.0f}m' if r['dev_min'] else '-'):>6}"
              f"{(f'{r['rev_min']:.0f}m' if r['rev_min'] else '-'):>8}"
              f"{(f'{100*r['rev_min']/(r['dev_min']+r['rev_min']):.0f}%' if r['rev_min'] and r['dev_min'] else '-'):>6}"
              f"{(r['rounds'] if r['rounds'] is not None else '-'):>5}"
              f"{(f'{r['tok_out']:,}' if r['tok_out'] else '-'):>10}"
              f"{(f'${r['cost']:.2f}' if r['cost'] else '-'):>8}  {r['status']}")

    print("\nCLAUSE DETAIL (which of the 12 each suite guards):")
    for r in rows:
        if r["delivered"]:
            missing = [c["id"] for c in json.loads((HERE / "clauses.json").read_text())["clauses"]
                       if c["id"] not in r["hit"]]
            print(f"  {r['arm']:<10} has {','.join(r['hit']) or '-'}")
            print(f"  {'':<10} MISSING {','.join(missing) or 'none'}")
    print("\nREVIEW TIME IS OVERHEAD THE MODEL CANNOT REDUCE. It is charged per")
    print("round by three reviewer seats and is paid whichever model implements —")
    print("27-50% of wall clock here. Only the ROUND COUNT moves it, which makes")
    print("first-pass clause coverage worth more than raw implementation speed.")
    print("\nREAD THIS TABLE BY CLAUSES FIRST. Kill rate saturates — every arm that")
    print("delivered scored the same, because all 13 mutations sit inside clause 4.")
    print("An arm that is fast, cheap and green while guarding 2 of 12 clauses is")
    print("not a bargain; it is an unguarded contract that looks like one.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
