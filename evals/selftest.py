"""Run offline positive and negative controls through the pinned Harbor runner."""

import argparse
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import subprocess
import sys
import sysconfig

from prepare import prepare


def checked_result(job: Path, expected: int) -> dict:
    results = list(job.glob("*/result.json"))
    if len(results) != 1:
        raise ValueError(f"expected one trial result in {job}, got {len(results)}")
    trial = json.loads(results[0].read_text())
    if trial.get("exception_info"):
        raise ValueError(f"trial infrastructure error: {trial['exception_info']}")
    report = json.loads((results[0].parent / "verifier" / "report.json").read_text())
    if report["status"] not in {"passed", "failed"} or not report["cases"]:
        raise ValueError(f"grader did not complete acceptance checks: {report}")
    names = set()
    for case in report["cases"]:
        if not isinstance(case.get("name"), str) or case["name"] in names or type(case.get("passed")) is not bool:
            raise ValueError("malformed or duplicate acceptance check")
        names.add(case["name"])
    if (report["status"] == "passed") != all(case["passed"] for case in report["cases"]):
        raise ValueError("grader status disagrees with acceptance checks")
    actual = (trial.get("verifier_result") or {}).get("rewards", {}).get("reward")
    if actual != expected or (report["status"] == "passed") != bool(expected):
        raise ValueError(f"control expected reward {expected}, got {actual}; report: {report['status']}")
    return {"reward": actual, "checks": len(report["cases"]), "status": report["status"]}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workflow-repo", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--output", type=Path, required=True, help="New output directory; parent must exist")
    args = parser.parse_args()
    if version("harbor") != "0.22.0":
        parser.error("run with the environment installed from evals/requirements.txt")
    harbor = Path(sysconfig.get_path("scripts")) / "harbor"
    args.output.mkdir()
    controls = (("broken", "reference", "nop", 0), ("fixed", "reference", "oracle", 1),
                ("always-pass", "always-pass", "oracle", 0), ("always-fail", "always-fail", "oracle", 0))
    harness = Path(__file__).resolve().parent
    summary = {
        "schema_version": 1, "harbor_version": version("harbor"), "python_version": sys.version,
        "harness_sha256": {name: hashlib.sha256((harness / name).read_bytes()).hexdigest()
                           for name in ("prepare.py", "selftest.py", "requirements.lock")},
        "controls": {},
    }
    env = {key: os.environ[key] for key in ("PATH", "TMPDIR", "LANG", "XDG_RUNTIME_DIR") if key in os.environ}
    for name, control, agent, expected in controls:
        task = args.output / f"task-{name}"
        prepare(args.workflow_repo.resolve(), task, control)
        print(f"Running {name}: expect reward {expected}", flush=True)
        command = [str(harbor), "run", "-p", str(task.resolve()), "-a", agent,
                   "--jobs-dir", str((args.output / "jobs").resolve()), "--job-name", name,
                   "--n-concurrent", "1", "--n-attempts", "1", "--max-retries", "0", "--quiet"]
        with (args.output / f"{name}.log").open("w") as log:
            subprocess.run(command, cwd=args.output, env=env, stdout=log, stderr=subprocess.STDOUT, check=True, timeout=600)
        summary["controls"][name] = checked_result(args.output / "jobs" / name, expected)
    (args.output / "controls.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"All controls matched expectations. Evidence: {args.output / 'controls.json'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
