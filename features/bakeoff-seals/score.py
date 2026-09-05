#!/usr/bin/env python3
"""Use the bound, isolated study scorer; legacy unbound results are not accepted."""
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "model-matrix"))
from scorecard import main


if __name__ == "__main__":
    raise SystemExit(main())
