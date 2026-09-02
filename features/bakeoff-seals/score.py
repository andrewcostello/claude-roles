#!/usr/bin/env python3
"""Score a bakeoff, refusing any arm that did not run what it was pinned to.

The refusal is the point. The 2026-09-01 run compared four arms and two of them
had cascaded to claude-opus-5[1m] after their pinned CLI exited 1. The
dispatcher recorded that faithfully — `agent` and `model` on each row are the
ones that actually produced the attempt — and the scoring read neither, so
run-to-run variance between three Claude runs was reported as a model ranking.

Nothing here needs the dispatcher to record anything new. It needs the score to
read what is already there and refuse to rank an arm whose actual != pinned.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, "/home/andrew/Project/claude-dispatcher/src")
from claude_dispatcher import yaml_io  # noqa: E402


def pinned_from(spec_path: Path) -> dict[str, tuple[str, str]]:
    """key -> (agent, model) AS AUTHORED. Read from the spec, never the run."""
    doc = yaml_io.load(spec_path)
    return {t["key"]: (t.get("agent") or "claude", t.get("model") or "")
            for t in doc["tasks"] if t.get("status") != "Done"}


def actual_from(run_path: Path) -> dict[str, tuple[str, str]]:
    """key -> (agent, model) AS RUN. The dispatcher stamps these on cascade."""
    doc = yaml_io.load(run_path)
    return {t["key"]: (t.get("agent") or "claude", t.get("model") or "")
            for t in doc["tasks"] if t.get("status") != "Done"}


def gate(worktree: Path, module: str) -> tuple[int, int]:
    """(exit_code, failing_row_count) for `go test ./...` in one module."""
    p = subprocess.run(["go", "test", "./..."], cwd=worktree / module,
                       capture_output=True, text=True, timeout=600)
    fails = len(re.findall(r"^\s*--- FAIL: (\S+)", p.stdout, re.M))
    return p.returncode, fails


def failing_set(worktree: Path, module: str) -> set[str]:
    p = subprocess.run(["go", "test", "./..."], cwd=worktree / module,
                       capture_output=True, text=True, timeout=600)
    return set(re.findall(r"^\s*--- FAIL: (\S+)", p.stdout, re.M))


def mutation_caught(worktree: Path, module: str, site: Path,
                    before: str, after: str) -> bool | None:
    """Apply a mutation, diff the failing set, revert. None if the site is gone.

    A seal that only reaches production through an unimplemented stub is red
    whatever the mutation does; this is what separates it from one that watches
    the thing the mutation changes.
    """
    text = site.read_text()
    if before not in text:
        return None
    base = failing_set(worktree, module)
    site.write_text(text.replace(before, after, 1))
    try:
        mutated = failing_set(worktree, module)
    finally:
        site.write_text(text)
    return bool(mutated - base)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--spec", required=True, help="tasks.yaml as AUTHORED (the pins)")
    ap.add_argument("--run", required=True, help="tasks.yaml as RUN (post-dispatch)")
    ap.add_argument("--worktree-base", default="/home/andrew/Project")
    ap.add_argument("--module", default="cmd/classify")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    pinned, actual = pinned_from(Path(args.spec)), actual_from(Path(args.run))
    rows, refused = [], []

    for key, (want_agent, want_model) in sorted(pinned.items()):
        got_agent, got_model = actual.get(key, ("?", "?"))
        row = {"key": key, "pinned": f"{want_agent}/{want_model}",
               "actual": f"{got_agent}/{got_model}"}
        # THE REFUSAL. A model pin is a claim about who wrote the code; if the
        # run says otherwise, every number below is about a different model and
        # ranking it is worse than having no number at all.
        if got_agent != want_agent or (want_model and got_model != want_model):
            row["scored"] = False
            row["refused"] = "cascaded — did not run its pin"
            refused.append(key)
            rows.append(row)
            continue
        wt = Path(args.worktree_base) / f"worktree-{key}"
        if not wt.exists():
            row["scored"] = False
            row["refused"] = "no worktree"
            rows.append(row)
            continue
        code, fails = gate(wt, args.module)
        row.update(scored=True, gate_exit=code, failing=fails)
        rows.append(row)

    if args.json:
        print(json.dumps({"rows": rows, "refused": refused}, indent=2))
    else:
        w = max(len(r["key"]) for r in rows) if rows else 8
        for r in rows:
            mark = "  " if r.get("scored") else "!!"
            print(f"{mark} {r['key']:<{w}}  pinned={r['pinned']:<28} actual={r['actual']:<28}"
                  + ("" if r.get("scored") else f"  REFUSED: {r['refused']}"))
        if refused:
            print(f"\nREFUSED {len(refused)} arm(s): {', '.join(refused)}")
            print("A cascaded arm is not a data point about its pinned model.")
    return 1 if refused else 0


if __name__ == "__main__":
    raise SystemExit(main())
