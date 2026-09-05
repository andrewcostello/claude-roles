"""Export a pinned historical defect without exposing the host checkout."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess


BASELINE = "3e225a7a1e951d0717472c33b3a17930c16bcd0a"
FILES = ("go.mod", "main.go", "main_test.go", "testdata/example-gates.json")
TEMPLATE = Path(__file__).parent / "cases" / "gate-selection"


def prepare(repo: Path, output: Path, control: str = "reference") -> dict:
    if control not in {"reference", "always-pass", "always-fail"}:
        raise ValueError(f"unknown control: {control}")
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env.update(GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1", GIT_NO_REPLACE_OBJECTS="1")
    snapshot = {}
    for name in FILES:
        snapshot[name] = subprocess.run(
            ["git", "-C", str(repo), "show", f"{BASELINE}:cmd/gates/{name}"],
            env=env, check=True, capture_output=True, timeout=30,
        ).stdout
    # No overwrite or reuse: every trial starts with a newly exported task.
    output.mkdir()
    shutil.copytree(TEMPLATE, output, dirs_exist_ok=True, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
    source = output / "environment" / "gates"
    for name, content in snapshot.items():
        path = source / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
    if control != "reference":
        exit_code = 0 if control == "always-pass" else 1
        verdict = "PASS" if exit_code == 0 else "FAIL"
        mutant = (
            'package main\nimport ("fmt"; "os")\n'
            f'func main() {{ fmt.Println("=== GATES: {verdict} ==="); os.Exit({exit_code}) }}\n'
        )
        (output / "solution" / "main.go").write_text(mutant)
        (output / "solution" / "solve.sh").write_text(
            "#!/bin/bash\nset -euo pipefail\ncp /solution/main.go /app/gates/main.go\n"
        )
    hashes = {
        path.relative_to(output).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(output.rglob("*")) if path.is_file()
    }
    manifest = {"schema_version": 1, "baseline_commit": BASELINE, "control": control, "sha256": hashes}
    (output / "provenance.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workflow-repo", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--output", required=True, type=Path, help="New task directory; parent must exist")
    parser.add_argument("--control", choices=["reference", "always-pass", "always-fail"], default="reference")
    args = parser.parse_args()
    prepare(args.workflow_repo.resolve(), args.output, args.control)
    print(f"Prepared {args.output}; baseline {BASELINE}. No agent was run.")


if __name__ == "__main__":
    main()
