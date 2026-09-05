"""Run trusted local Go diagnostics in independent disposable Git clones.

This is source isolation, not a security sandbox for hostile candidate code.
"""
from __future__ import annotations

import contextlib
import os
import signal
import subprocess
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

from study_common import StudyError, digest, file_digest, json_loads, relative_path, require_sha

MAX_SECONDS = 120


def run_command(argv: list[str], cwd: Path, env: dict[str, str], seconds: int) -> subprocess.CompletedProcess:
    """Bound a POSIX process group, including children left after parent exit."""
    if os.name != "posix" or type(seconds) is not int or not 1 <= seconds <= MAX_SECONDS:
        raise StudyError("POSIX execution with a 1..120 second timeout is required")
    try:
        process = subprocess.Popen(argv, cwd=cwd, env=env, start_new_session=True,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    except OSError as exc:
        raise StudyError(f"cannot start {argv[0]}: {exc}") from exc
    try:
        stdout, stderr = process.communicate(timeout=seconds)
        return subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
    except subprocess.TimeoutExpired as exc:
        raise StudyError(f"{argv[0]} exceeded {seconds}s") from exc
    finally:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(process.pid, signal.SIGKILL)
        process.stdout.close()
        process.stderr.close()
        process.wait(timeout=1)


def execution_env(private: Path) -> dict[str, str]:
    """Do not inherit credentials, Go flags, workspaces, or user Git config."""
    for name in ("home", "tmp", "cache"):
        (private / name).mkdir(exist_ok=True)
    return {"PATH": os.environ.get("PATH", os.defpath), "LANG": "C.UTF-8",
            "HOME": str(private / "home"), "TMPDIR": str(private / "tmp"),
            "GOCACHE": str(private / "cache"), "GOPROXY": "off", "GOSUMDB": "off",
            "GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
            "CGO_ENABLED": "0", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0"}


def git(repo: Path, env: dict, *args: str) -> str:
    """Run a bounded Git command without global hooks or filesystem monitors."""
    result = run_command(["git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
                          "-C", str(repo), *args], repo, env, MAX_SECONDS)
    if result.returncode:
        raise StudyError(f"git {args[0]} failed: {result.stderr.strip()}")
    return result.stdout


def check_source(repo: Path, revision: str, env: dict) -> None:
    """Refuse a dirty, moved, or non-root submitted checkout."""
    require_sha(revision, "subject_revision")
    if Path(git(repo, env, "rev-parse", "--show-toplevel").strip()).resolve() != repo.resolve():
        raise StudyError("worktree must be the repository root")
    if git(repo, env, "rev-parse", "HEAD").strip() != revision:
        raise StudyError("submitted HEAD differs from subject_revision")
    if git(repo, env, "status", "--porcelain", "--untracked-files=all"):
        raise StudyError("submitted worktree is dirty")
    entries = git(repo, env, "ls-tree", "-r", revision).splitlines()
    if any(line.split()[0] not in ("100644", "100755") for line in entries):
        raise StudyError("symlinks and submodules are not supported study inputs")


def clone_snapshot(repo: Path, revision: str, destination: Path, env: dict) -> None:
    """Create a full independent clone without mutating the submitted checkout."""
    result = run_command(["git", "clone", "--quiet", "--no-local", "--no-hardlinks",
                          "--no-checkout", "--", str(repo), str(destination)],
                         destination.parent, env, MAX_SECONDS)
    if result.returncode:
        raise StudyError(f"snapshot clone failed: {result.stderr.strip()}")
    git(destination, env, "checkout", "--quiet", "--detach", revision)


def tree_digest(root: Path) -> str:
    """Fingerprint snapshot files and modes without traversing symbolic links."""
    files = []
    for parent, directories, names in os.walk(root, followlinks=False):
        if Path(parent) == root:
            directories[:] = [d for d in directories if d != ".git"]
        for name in directories + names:
            path = Path(parent) / name
            if path.is_symlink():
                raise StudyError("snapshot contains a symbolic link")
        for name in names:
            path = Path(parent) / name
            if not path.is_file():
                raise StudyError("snapshot contains a nonregular file")
            files.append([path.relative_to(root).as_posix(), path.stat().st_mode & 0o777,
                          file_digest(path)])
    return digest(sorted(files))


@dataclass
class GoResult:
    """Complete test observations, separated from execution/protocol errors."""
    valid: bool = False
    passing: set[tuple[str, str]] = field(default_factory=set)
    failing: set[tuple[str, str]] = field(default_factory=set)
    errors: list[str] = field(default_factory=list)
    raw: dict = field(default_factory=dict)

    @property
    def green(self) -> bool:
        return self.valid and bool(self.passing) and not self.failing


def parse_go_result(process: subprocess.CompletedProcess) -> GoResult:
    """Require complete package/test event streams and consistent process status."""
    result = GoResult(raw={"exit_code": process.returncode, "stdout": process.stdout, "stderr": process.stderr})
    packages, tests = {}, {}
    try:
        if process.stderr.strip():
            raise StudyError("Go emitted build/setup diagnostics on stderr")
        for line in process.stdout.splitlines():
            event = json_loads(line)
            if not isinstance(event, dict):
                raise StudyError("Go event must be an object")
            action, package, test = event.get("Action"), event.get("Package"), event.get("Test")
            if action in ("build-start", "build-output"):
                continue
            if action == "build-fail":
                raise StudyError("Go build failed")
            if not isinstance(package, str) or not package:
                raise StudyError("Go event lacks package identity")
            if test is not None:
                if not isinstance(test, str) or not test or packages.get(package) != "start":
                    raise StudyError("invalid test identity or missing package start")
                key = (package, test)
                if action == "run" and key not in tests:
                    tests[key] = "run"
                elif action in ("pause", "cont", "output") and tests.get(key) in ("run", "pause", "cont"):
                    if action != "output":
                        tests[key] = action
                elif action in ("pass", "fail", "skip") and tests.get(key) in ("run", "pause", "cont"):
                    tests[key] = action
                else:
                    raise StudyError("incomplete, duplicate, or contradictory test events")
            elif action == "start" and package not in packages:
                packages[package] = "start"
            elif action == "output" and packages.get(package) == "start":
                continue
            elif action in ("pass", "fail", "skip") and packages.get(package) == "start":
                packages[package] = action
            else:
                raise StudyError("incomplete, duplicate, or contradictory package events")
        if not packages or any(state not in ("pass", "fail") for state in packages.values()):
            raise StudyError("missing package completion or skipped package")
        if not tests or any(state not in ("pass", "fail") for state in tests.values()):
            raise StudyError("missing test completion, no tests, or skipped tests")
        result.passing = {key for key, state in tests.items() if state == "pass"}
        result.failing = {key for key, state in tests.items() if state == "fail"}
        if {p for p, state in packages.items() if state == "fail"} != {p for p, _ in result.failing}:
            raise StudyError("package failure without a corresponding test failure")
        expected_exit = 1 if result.failing else 0
        if process.returncode != expected_exit:
            raise StudyError("process exit disagrees with completed tests")
        result.valid = True
    except (StudyError, ValueError, TypeError) as exc:
        result.errors.append(str(exc))
    return result


def test_command(seconds: int) -> list[str]:
    """Use uncached tests with both inner and outer time limits."""
    if type(seconds) is not int or not 1 <= seconds <= MAX_SECONDS:
        raise StudyError("timeout must be an integer in 1..120 seconds")
    return ["go", "test", "-json", "-count=1", f"-timeout={seconds}s", "./..."]


def observe(repo: Path, revision: str, module: str, private: Path, env: dict,
            seconds: int, mutation: dict | None = None) -> GoResult:
    """Run one observation in a fresh clone and reject source changes by tests."""
    relative_path(module)
    with tempfile.TemporaryDirectory(prefix="trial-", dir=private) as name:
        snapshot = Path(name) / "repo"
        clone_snapshot(repo, revision, snapshot, env)
        tree_digest(snapshot)
        mod = snapshot / module
        if not (mod / "go.mod").is_file():
            raise StudyError("module has no go.mod")
        if mutation is not None:
            target = mod / relative_path(mutation["file"])
            if target.is_symlink() or not target.is_file() or target.stat().st_nlink != 1:
                raise StudyError("mutation target is not an independent regular file")
            source = target.read_text()
            if source.count(mutation["before"]) != 1:
                raise StudyError("mutation site must occur exactly once")
            target.write_text(source.replace(mutation["before"], mutation["after"], 1))
        before = tree_digest(snapshot)
        process = run_command(test_command(seconds), mod, env, seconds)
        if tree_digest(snapshot) != before or git(snapshot, env, "rev-parse", "HEAD").strip() != revision:
            raise StudyError("test execution changed its source snapshot")
        return parse_go_result(process)
