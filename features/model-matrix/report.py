#!/usr/bin/env python3
"""Report bound scores; optional clause mentions are unverified text-search hints."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

from analyse import report
from study_common import StudyError, json_loads, merge_scores, rows_from

HERE = Path(__file__).resolve().parent


def clause_mentions(seal_files: list[Path]) -> tuple[int, list[str]]:
    """Count text matches, including comments; this does not measure assertions."""
    spec = json_loads((HERE / "clauses.json").read_text())
    text = "\n".join(path.read_text() for path in seal_files)
    hits = [c["id"] for c in spec["clauses"] if any(re.search(p, text, re.I) for p in c["probe"])]
    return len(spec["clauses"]), hits


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-yaml", type=Path, required=True)
    parser.add_argument("--spec", type=Path, required=True)
    parser.add_argument("--score-json", type=Path, required=True)
    parser.add_argument("--mention-files", type=Path, nargs="+")
    args = parser.parse_args()
    try:
        rows = rows_from(args.run_yaml, args.spec)
        merge_scores(rows, args.score_json)
        report(rows, "Study observations")
        if args.mention_files:
            total, hits = clause_mentions(args.mention_files)
            print(f"UNVERIFIED TEXT HINTS: {len(hits)}/{total} clause mentions: {','.join(hits)}")
            print("Comments count as matches. These files are not bound to the scores; no behavioral coverage is claimed.")
        return 0 if all(r["score_verified"] for r in rows) else 1
    except (StudyError, OSError, ValueError) as exc:
        print(f"REFUSED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
