#!/usr/bin/env python3
"""Report verified observations and incomplete trials without ranking models."""
from __future__ import annotations

import argparse
import statistics
import sys
from pathlib import Path

from study_common import StudyError, merge_scores, rows_from


def report(rows: list[dict], label: str) -> None:
    """Show descriptive rates only; every excluded trial remains visible."""
    print(f"\n{label}: {sum(r['score_verified'] for r in rows)}/{len(rows)} verified measurements")
    cells = {}
    for row in rows:
        if not row["score_verified"]:
            print(f"UNVERIFIED {row['key']} ({row['status']}): {'; '.join(row['errors'])}")
        cell = f"{row['pinned_agent']}/{row['pinned_model']}@{row['effort']} ({row['agent_runtime']})"
        cells.setdefault(cell, []).append(row)
    for cell, attempts in sorted(cells.items()):
        rates = [r["kill_rate"] for r in attempts if r["score_verified"]]
        costs = [r["cost_usd"] for r in attempts if r["cost_usd"] is not None]
        spread = statistics.stdev(rates) if len(rates) > 1 else None
        print(f"{cell}: n={len(rates)}/{len(attempts)}, "
              f"mean kill rate={statistics.mean(rates) if rates else None}, stdev={spread}, "
              f"recorded cost={sum(costs) if costs else None} ({len(costs)}/{len(attempts)} attempts)")
    print("Descriptive observations only. No model ranking, equivalence, or production acceptance is inferred.")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--study", action="append", required=True,
                        metavar="LABEL:RUN_YAML:SPEC_YAML:SCORE_JSON")
    args = parser.parse_args()
    success = True
    for study in args.study:
        try:
            parts = study.split(":")
            if len(parts) != 4 or not all(parts):
                raise StudyError("each study requires label, run, authored spec, and score file")
            label, run, spec, score = parts
            rows = rows_from(Path(run), Path(spec))
            merge_scores(rows, Path(score))
            report(rows, label)
            success = success and all(r["score_verified"] for r in rows)
        except (StudyError, OSError, ValueError) as exc:
            print(f"REFUSED: {exc}", file=sys.stderr)
            success = False
    return 0 if success else 1


if __name__ == "__main__":
    raise SystemExit(main())
