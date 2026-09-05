"""Grade observable CLI behavior, outside the agent's writable environment."""

import hashlib
import itertools
import json
import os
from pathlib import Path
import signal
import stat
import subprocess
import tempfile


CANDIDATE_UID = 10001
SOURCE = Path("/app/gates")
LOGS = Path("/logs/verifier")


def candidate_dir(parent: Path) -> Path:
    path = Path(tempfile.mkdtemp(dir=parent))
    os.chown(path, CANDIDATE_UID, CANDIDATE_UID)
    return path


def invoke(args: list[str], cwd: Path, env: dict, log: Path, timeout: int = 10) -> tuple[int, str]:
    with log.open("wb") as stream:
        proc = subprocess.Popen(
            args, cwd=cwd, env=env, stdout=stream, stderr=subprocess.STDOUT,
            user=CANDIDATE_UID, group=CANDIDATE_UID, extra_groups=[],
            start_new_session=True,
        )
        try:
            code = proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            code = 124
        finally:
            # Also reap background descendants of an otherwise completed command.
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            proc.wait(timeout=5)
    with log.open("rb") as stream:
        output = stream.read(1024 * 1024)
    return code, output.decode("utf-8", errors="replace")


def source_hashes() -> dict[str, str]:
    hashes = {}
    size = 0
    if not SOURCE.is_dir() or SOURCE.is_symlink():
        raise ValueError("missing source directory or symlink source")
    for path in sorted(SOURCE.rglob("*")):
        mode = path.lstat().st_mode
        if stat.S_ISDIR(mode):
            path.chmod(0o755)
            os.chown(path, 0, 0)
            continue
        if not stat.S_ISREG(mode):
            raise ValueError("source contains a symlink or special file")
        size += path.stat().st_size
        if size > 20 * 1024 * 1024 or len(hashes) >= 512:
            raise ValueError("source exceeds fixture limits")
        hashes[path.relative_to(SOURCE).as_posix()] = hashlib.sha256(path.read_bytes()).hexdigest()
        path.chmod(0o644)
        os.chown(path, 0, 0)
    SOURCE.chmod(0o755)
    os.chown(SOURCE, 0, 0)
    return hashes


def cases():
    gates = ("build", "test", "lint")
    for size in range(1, len(gates) + 1):
        for subset in itertools.combinations(gates, size):
            yield {"name": "subset-" + "-".join(subset), "only": ",".join(subset), "code": 0 if size == 3 else 1}
    yield {"name": "full", "code": 0}
    yield {"name": "execution-failure", "failing": True, "code": 1}
    yield {"name": "waived", "only": "build,test", "waiver": "lint=approved diagnostic", "code": 0}
    yield {"name": "duplicate", "only": " build, test,lint,build ", "code": 0}
    for only in ("typo", "build,typo", "build,", ",,", " ", "security"):
        for dry in (False, True):
            yield {"name": f"invalid-{only!r}-{dry}", "only": only, "dry": dry, "code": 3}
    yield {"name": "dry-partial", "only": "build", "dry": True, "code": 0}
    yield {"name": "empty-plan", "empty_plan": True, "code": 3}


def check_case(case: dict, binary: Path, parent: Path, env: dict, index: int) -> dict:
    work = candidate_dir(parent)
    state = {"schema_version": 1, "repo": {"worktree": str(work)}, "classification": {
        "risk": "low", "changed_files": [{"path": "main.go"}],
    }}
    trigger = "high_or_critical" if case.get("empty_plan") else "always"
    config = {"schema_version": 1, "module_marker": "go.mod", "gates": {
        gate: {"trigger": trigger, "scope": "module", "command": f"echo checked; touch ran-{gate}; " + ("false" if case.get("failing") and gate == "test" else "true")}
        for gate in ("build", "test", "lint")
    }}
    config["gates"]["security"] = {"trigger": "high_or_critical", "scope": "module", "command": "false"}
    contents = {"run.json": json.dumps(state), "gates.json": json.dumps(config), "go.mod": "module fixture\n"}
    for name, content in contents.items():
        path = work / name
        path.write_text(content)
        os.chown(path, CANDIDATE_UID, CANDIDATE_UID)
    args = [str(binary), "-run-state", str(work / "run.json"), "-config", str(work / "gates.json")]
    if "only" in case:
        args.extend(["-only", case["only"]])
    if case.get("dry"):
        args.append("-dry-run")
    if "waiver" in case:
        args.extend(["-waive", case["waiver"]])
    code, output = invoke(args, work, env, LOGS / f"case-{index}.log")
    failures = []
    if code != case["code"]:
        failures.append(f"exit {code}, expected {case['code']}")
    if (case["code"] != 0 or case.get("dry")) and "GATES: PASS" in output:
        failures.append("false PASS banner")
    if case["code"] == 3 or case.get("dry"):
        if (work / "run.json").read_text() != contents["run.json"] or (work / "gate-output").exists() or list(work.glob("ran-*")):
            failures.append("invalid/dry run mutated state or executed gates")
    else:
        recorded = json.loads((work / "run.json").read_text()).get("gates", {})
        selected = {part.strip() for part in case.get("only", "build,test,lint").split(",")}
        for gate in ("build", "test", "lint"):
            expected = "pass" if gate in selected else "fail"
            if case.get("failing") and gate == "test":
                expected = "fail"
            if "waiver" in case and gate == "lint":
                expected = "skipped"
            outcome = recorded.get(f"{gate}:.", {})
            if outcome.get("status") != expected:
                failures.append(f"{gate} did not record {expected}")
            if (work / f"ran-{gate}").is_file() != (gate in selected):
                failures.append(f"{gate} execution does not match selection")
            if expected == "pass":
                output_path = outcome.get("output_path")
                if not output_path or not Path(output_path).resolve().is_relative_to(work / "gate-output") or not Path(output_path).is_file():
                    failures.append(f"{gate} has no execution log")
    return {"name": case["name"], "passed": not failures, "failures": failures}


def main() -> None:
    LOGS.mkdir(parents=True, exist_ok=True)
    LOGS.chmod(0o700)
    reward = LOGS / "reward.txt"
    reward.write_text("0\n")
    report = {"schema_version": 1, "status": "error", "cases": []}
    try:
        report["source_sha256"] = source_hashes()
        parent = Path(tempfile.mkdtemp(prefix="grading-"))
        parent.chmod(0o755)
        build = candidate_dir(parent)
        cache = candidate_dir(parent)
        env = {"PATH": "/usr/local/go/bin:/usr/bin:/bin", "GOTOOLCHAIN": "local", "GOENV": "off",
               "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "CGO_ENABLED": "0",
               "GOCACHE": str(cache), "GOPATH": str(build / "gopath"), "TMPDIR": str(build)}
        binary = build / "gates"
        code, _ = invoke(["/usr/local/go/bin/go", "build", "-o", str(binary), "."], SOURCE, env, LOGS / "build.log", timeout=90)
        if code != 0:
            report.update(status="failed", reason=f"candidate build exited {code}")
        else:
            report["cases"] = [check_case(case, binary, parent, env, index) for index, case in enumerate(cases())]
            report["status"] = "passed" if all(case["passed"] for case in report["cases"]) else "failed"
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    (LOGS / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    if report["status"] == "passed" and report["cases"]:
        reward.write_text("1\n")


if __name__ == "__main__":
    main()
