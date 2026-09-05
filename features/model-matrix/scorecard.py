#!/usr/bin/env python3
"""Measure trusted local Go suites in disposable clones; never infer acceptance."""
from __future__ import annotations

import argparse
import importlib.metadata
import platform
import sys
import tempfile
from pathlib import Path

from study_common import (StudyError, digest, file_digest, load_document, mutations_from,
                          rows_from, write_new_json)
from study_execution import check_source, execution_env, git, observe, run_command, test_command

HERE = Path(__file__).resolve().parent


def measurement_protocol(mutations: dict, seconds: int, private: Path, env: dict) -> dict:
    """Describe the exact oracle, measurement code, command, and runtime."""
    inventory = mutations_from(mutations)
    command = test_command(seconds)
    version = run_command(["go", "version"], private, env, 10)
    if version.returncode or version.stderr or not version.stdout.startswith("go version "):
        raise StudyError("Go toolchain identity is unavailable")
    try:
        yaml_runtime = importlib.metadata.version("ruamel.yaml")
    except importlib.metadata.PackageNotFoundError:
        yaml_runtime = "unavailable"
    return {"oracle_sha256": digest(mutations), "module": mutations["module"],
            "mutation_ids": [m["id"] for m in inventory],
            "harness_sha256": digest({name: file_digest(HERE / name) for name in
                                      ("scorecard.py", "study_execution.py", "study_common.py", "requirements.txt")}),
            "command": command, "go_version": version.stdout.strip(),
            "platform": f"{sys.platform}/{platform.machine()};python={platform.python_version()};yaml={yaml_runtime}"}


def score_arm(worktree: Path, module: str, mutations: dict, *, revision: str,
              context: dict | None = None, seconds: int = 30,
              scratch_parent: Path | None = None, trusted_local_code: bool = False) -> dict:
    """Record invalid observations separately; only complete valid trials get a rate."""
    out = {"schema_version": 2, "arm": (context or {}).get("key", worktree.name),
           "context": context, "gate_green": False, "complete": False,
           "mutations": [], "kill_rate": None, "errors": []}
    try:
        if not trusted_local_code:
            raise StudyError("local tests require explicit trusted-local-code authorization")
        worktree = worktree.resolve()
        inventory = mutations_from(mutations)
        if module != mutations["module"]:
            raise StudyError("module differs from the oracle")
        with tempfile.TemporaryDirectory(prefix="workflow-study-", dir=scratch_parent) as name:
            private = Path(name)
            env = execution_env(private)
            check_source(worktree, revision, env)
            if context is not None:
                if context.get("subject_revision") != revision:
                    raise StudyError("context is bound to a different subject revision")
                git(worktree, env, "merge-base", "--is-ancestor", context["base_revision"], revision)
            out["measurement"] = measurement_protocol(mutations, seconds, private, env)
            baselines = [observe(worktree, revision, module, private, env, seconds) for _ in range(2)]
            out["baseline_runs"] = [b.raw for b in baselines]
            if not all(b.green for b in baselines) or baselines[0].passing != baselines[1].passing:
                raise StudyError("baseline is red, empty, skipped, invalid, or unstable: " +
                                 "; ".join(e for b in baselines for e in b.errors))
            out["gate_green"] = True
            out["baseline_tests"] = sorted([list(t) for t in baselines[0].passing])
            for mutation in inventory:
                record = {"id": mutation["id"], "status": "invalid"}
                try:
                    observed = observe(worktree, revision, module, private, env, seconds, mutation)
                    record["execution"] = observed.raw
                    if not observed.valid:
                        record["reason"] = "; ".join(observed.errors)
                    elif observed.failing & baselines[0].passing:
                        record["status"] = "killed"
                    elif observed.green and observed.passing == baselines[0].passing:
                        record["status"] = "survived"
                    else:
                        record["reason"] = "test inventory changed without a baseline test detecting the defect"
                except (StudyError, OSError) as exc:
                    record["reason"] = str(exc)
                out["mutations"].append(record)
            check_source(worktree, revision, env)
            if out["measurement"] != measurement_protocol(mutations, seconds, private, env):
                raise StudyError("measurement harness or runtime changed during execution")
            out["complete"] = all(m["status"] in ("killed", "survived") for m in out["mutations"])
            if out["complete"]:
                out["kill_rate"] = sum(m["status"] == "killed" for m in out["mutations"]) / len(inventory)
    except (StudyError, OSError) as exc:
        out["errors"].append(str(exc))
        out["complete"] = False
        out["kill_rate"] = None
    return out


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mutations", type=Path, default=HERE / "mutations.json")
    parser.add_argument("--spec", type=Path)
    parser.add_argument("--run", type=Path)
    parser.add_argument("--worktree-base", type=Path)
    parser.add_argument("--json-out", type=Path)
    parser.add_argument("--timeout-seconds", type=int, default=30)
    parser.add_argument("--scratch-parent", type=Path)
    parser.add_argument("--trusted-local-code", action="store_true")
    parser.add_argument("--print-protocol", action="store_true", help="print settings to freeze in the authored spec")
    args = parser.parse_args()
    try:
        mutations = load_document(args.mutations)
        with tempfile.TemporaryDirectory(prefix="study-protocol-", dir=args.scratch_parent) as name:
            private = Path(name)
            protocol = measurement_protocol(mutations, args.timeout_seconds, private, execution_env(private))
        if args.print_protocol:
            import json
            print(json.dumps(protocol, indent=2))
            return 0
        if not args.run or not args.worktree_base or not args.json_out or not args.trusted_local_code:
            raise StudyError("--spec, --run, --worktree-base, --json-out, and --trusted-local-code are required")
        if args.json_out.exists():
            raise StudyError("output already exists")
        rows = rows_from(args.run, args.spec)
        results = []
        for row in rows:
            if not row["provenance_ok"] or row["status"] != "Done" or row["measurement"] != protocol:
                results.append({"schema_version": 2, "arm": row["key"], "complete": False,
                                "errors": row["errors"] + ["incomplete arm or mismatched measurement protocol"]})
                continue
            results.append(score_arm(args.worktree_base / f"worktree-{row['key']}", mutations["module"],
                                     mutations, revision=row["context"]["subject_revision"],
                                     context=row["context"], seconds=args.timeout_seconds,
                                     scratch_parent=args.scratch_parent, trusted_local_code=True))
            if results[-1].get("measurement") != protocol:
                results[-1].update(complete=False, kill_rate=None)
                results[-1]["errors"].append("measurement differs from the frozen invocation protocol")
        if rows_from(args.run, args.spec) != rows:
            raise StudyError("specification or run changed during measurement")
        write_new_json(args.json_out, results)
        for result in results:
            print(f"{result['arm']}: {'complete' if result['complete'] else 'INVALID'}")
        return 0 if all(r["complete"] for r in results) else 1
    except (StudyError, OSError, ValueError) as exc:
        print(f"REFUSED: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
