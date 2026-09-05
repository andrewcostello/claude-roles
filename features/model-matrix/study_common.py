"""Validate operator-owned study inputs and bind measurements to their subject.

Consistency checks do not authenticate provider claims. Keep specifications,
run manifests, and measurement outputs outside candidate write access.
"""
from __future__ import annotations

import hashlib
import json
import math
import re
import subprocess
from pathlib import Path, PurePosixPath


class StudyError(ValueError):
    """An input or observation cannot support a verified comparison."""


def digest(value: object) -> str:
    """Hash a canonical JSON value, rejecting non-finite numbers."""
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=True,
                                    allow_nan=False, separators=(",", ":")).encode()).hexdigest()


def file_digest(path: Path) -> str:
    """Hash the exact bytes of an input file."""
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _unique(pairs: list[tuple]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise StudyError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def json_loads(text: str) -> object:
    """Parse JSON without duplicate keys or non-finite constants."""
    def invalid_constant(value: str) -> None:
        raise StudyError(f"invalid JSON constant: {value}")
    def finite_float(value: str) -> float:
        number = float(value)
        if not math.isfinite(number):
            raise StudyError("non-finite JSON number")
        return number
    return json.loads(text, object_pairs_hook=_unique, parse_constant=invalid_constant, parse_float=finite_float)


def load_document(path: Path) -> dict:
    """Read a JSON or YAML mapping without importing a dispatcher checkout."""
    text = path.read_text(encoding="utf-8")
    if path.suffix == ".json" or text.lstrip().startswith("{"):
        value = json_loads(text)
    else:
        try:
            from ruamel.yaml import YAML
        except ImportError as exc:
            raise StudyError("YAML inputs require features/model-matrix/requirements.txt") from exc
        parser = YAML(typ="safe", pure=True)
        parser.allow_duplicate_keys = False
        try:
            value = parser.load(text)
        except Exception as exc:
            raise StudyError(f"invalid YAML in {path.name}: {exc}") from exc
    if not isinstance(value, dict):
        raise StudyError(f"{path.name}: expected a mapping")
    return value


def task_rows(doc: dict) -> dict[str, dict]:
    """Index explicit non-scaffold arms, including completed and failed rows."""
    tasks = doc.get("tasks")
    if not isinstance(tasks, list) or not tasks:
        raise StudyError("tasks must be a nonempty list")
    indexed, seen = {}, set()
    for row in tasks:
        if not isinstance(row, dict):
            raise StudyError("each task must be a mapping")
        key = row.get("key")
        if not isinstance(key, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", key):
            raise StudyError("invalid task key")
        if key in seen:
            raise StudyError(f"duplicate task key: {key}")
        seen.add(key)
        if not isinstance(row.get("role"), str) or not row["role"]:
            raise StudyError(f"{key}: explicit role required")
        if row["role"] != "scaffold":
            indexed[key] = row
    if not indexed:
        raise StudyError("no experiment arms")
    return indexed


def relative_path(value: object) -> str:
    """Require a normalized, nonempty repository-relative POSIX path."""
    if not isinstance(value, str) or not value or "\\" in value:
        raise StudyError("invalid relative path")
    path = PurePosixPath(value)
    if path.is_absolute() or any(p in ("", ".", "..", ".git") for p in value.split("/")):
        raise StudyError(f"unsafe relative path: {value}")
    return value


def require_sha(value: object, label: str) -> str:
    """Require a full Git object ID, never an abbreviation or ref name."""
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", value):
        raise StudyError(f"{label}: full commit ID required")
    return value


def require_text(value: object, label: str) -> str:
    """Require an explicit nonblank string without normalization."""
    if not isinstance(value, str) or not value.strip():
        raise StudyError(f"{label}: nonblank string required")
    return value


def mutations_from(doc: dict) -> list[dict]:
    """Validate a nonempty mutation inventory before executing any code."""
    relative_path(doc.get("module"))
    mutations = doc.get("mutations")
    if not isinstance(mutations, list) or not mutations:
        raise StudyError("a nonempty mutation inventory is required")
    seen = set()
    for mutation in mutations:
        if not isinstance(mutation, dict):
            raise StudyError("mutation must be a mapping")
        key = require_text(mutation.get("id"), "mutation id")
        if key in seen:
            raise StudyError(f"duplicate mutation id: {key}")
        seen.add(key)
        path = relative_path(mutation.get("file"))
        if not path.endswith(".go") or path.endswith("_test.go"):
            raise StudyError("mutations must target production Go source")
        before = require_text(mutation.get("before"), "mutation before")
        after = mutation.get("after")
        if not isinstance(after, str) or after == before:
            raise StudyError("mutation must change source text")
    return mutations


def rows_from(run_path: Path, spec_path: Path | None) -> list[dict]:
    """Retain every intended/observed arm; absent evidence is never its own pin."""
    if spec_path is None or not spec_path.is_file():
        raise StudyError("a separate authored specification is required")
    if run_path.samefile(spec_path):
        raise StudyError("specification and run must be separate files")
    spec_hash, run_hash = file_digest(spec_path), file_digest(run_path)
    spec_doc, run_doc = load_document(spec_path), load_document(run_path)
    spec, run = task_rows(spec_doc), task_rows(run_doc)
    rows = []
    for key in sorted(spec.keys() | run.keys()):
        pin, actual = spec.get(key, {}), run.get(key, {})
        errors = []
        context = {}
        try:
            if not pin or not actual:
                raise StudyError("missing authored or actual arm")
            for field in ("agent", "model", "effort", "role", "description", "agent_runtime"):
                if require_text(pin.get(field), f"authored {field}") != actual.get(field):
                    raise StudyError(f"{field} differs from the authored specification")
            evidence = actual.get("study_evidence")
            if not isinstance(evidence, dict):
                raise StudyError("missing study_evidence; historical row is unverified")
            base = require_sha(spec_doc.get("base_branch"), "base_branch")
            if evidence.get("base_revision") != base:
                raise StudyError("base revision differs from the specification")
            if evidence.get("agent_runtime") != pin["agent_runtime"]:
                raise StudyError("agent runtime differs from the authored specification")
            context = {
                "spec_sha256": spec_hash, "run_sha256": run_hash, "key": key,
                "agent": pin["agent"], "model": pin["model"], "effort": pin["effort"],
                "brief_sha256": digest(pin["description"]), "base_revision": base,
                "subject_revision": require_sha(evidence.get("subject_revision"), "subject_revision"),
                "invocation_id": require_text(evidence.get("invocation_id"), "invocation_id"),
                "agent_runtime": require_text(evidence.get("agent_runtime"), "agent_runtime"),
            }
        except StudyError as exc:
            errors.append(str(exc))
        cost = actual.get("cost_usd")
        if cost is not None and (type(cost) not in (float, int) or not math.isfinite(cost) or cost < 0):
            errors.append("invalid cost_usd")
            cost = None
        rows.append({"key": key, "pinned_agent": pin.get("agent"),
                     "pinned_model": pin.get("model"), "effort": pin.get("effort"),
                     "agent_runtime": pin.get("agent_runtime"),
                     "status": actual.get("status", "Missing"), "cost_usd": cost,
                     "provenance_ok": not errors, "errors": errors,
                     "context": context, "measurement": spec_doc.get("measurement"),
                     "score_verified": False})
    if file_digest(spec_path) != spec_hash or file_digest(run_path) != run_hash:
        raise StudyError("study inputs changed while being read")
    invocations = [r["context"].get("invocation_id") for r in rows if r["provenance_ok"]]
    for row in rows:
        if row["provenance_ok"] and invocations.count(row["context"]["invocation_id"]) != 1:
            row["provenance_ok"] = False
            row["errors"].append("duplicate invocation identity; not an independent trial")
    return rows


def validate_protocol(expected: object) -> None:
    """Require a complete fixed measurement protocol with a bounded Go command."""
    required = {"oracle_sha256", "harness_sha256", "module", "command", "go_version", "platform", "mutation_ids"}
    if not isinstance(expected, dict) or set(expected) != required:
        raise StudyError("incomplete measurement protocol")
    for field in ("oracle_sha256", "harness_sha256"):
        if not isinstance(expected[field], str) or not re.fullmatch(r"[0-9a-f]{64}", expected[field]):
            raise StudyError(f"invalid {field}")
    relative_path(expected["module"])
    require_text(expected["go_version"], "go_version")
    require_text(expected["platform"], "platform")
    command = expected["command"]
    if (not isinstance(command, list) or len(command) != 6 or
            command[:4] != ["go", "test", "-json", "-count=1"] or command[-1] != "./..." or
            not isinstance(command[4], str) or not re.fullmatch(r"-timeout=[1-9][0-9]*s", command[4]) or
            not 1 <= int(command[4][9:-1]) <= 120):
        raise StudyError("invalid or unbounded measurement command")
    ids = expected["mutation_ids"]
    if not isinstance(ids, list) or not ids:
        raise StudyError("missing authored mutation inventory")
    for key in ids:
        require_text(key, "mutation id")
    if len(set(ids)) != len(ids):
        raise StudyError("duplicate authored mutation id")


def _recorded_go(raw: object):
    from study_execution import parse_go_result
    if (not isinstance(raw, dict) or set(raw) != {"exit_code", "stdout", "stderr"} or
            type(raw["exit_code"]) is not int or not isinstance(raw["stdout"], str) or
            not isinstance(raw["stderr"], str)):
        raise StudyError("missing complete Go execution record")
    return parse_go_result(subprocess.CompletedProcess([], raw["exit_code"], raw["stdout"], raw["stderr"]))


def merge_scores(rows: list[dict], score_path: Path | None) -> None:
    """Accept only complete scores bound to the exact authored measurement."""
    for row in rows:
        row["score_verified"] = False
        for field in ("kill", "of", "kill_rate", "mutation_ids"):
            row.pop(field, None)
    if score_path is None or not score_path.is_file():
        raise StudyError("a score file is required; old observations are unverified")
    scores = json_loads(score_path.read_text())
    if not isinstance(scores, list) or not scores:
        raise StudyError("scores must be a nonempty list")
    indexed = {}
    for score in scores:
        if not isinstance(score, dict) or not isinstance(score.get("arm"), str):
            raise StudyError("invalid score row")
        if score["arm"] in indexed:
            raise StudyError("duplicate score arm")
        indexed[score["arm"]] = score
    if set(indexed) != {r["key"] for r in rows}:
        raise StudyError("score inventory does not match the complete study")
    for row in rows:
        row["score_verified"] = False
        score = indexed[row["key"]]
        try:
            if not row["provenance_ok"] or row["status"] != "Done":
                raise StudyError("arm is incomplete or has unverified provenance")
            if type(score.get("schema_version")) is not int or score["schema_version"] != 2 or score.get("context") != row["context"]:
                raise StudyError("score is legacy, stale, or bound to different inputs")
            expected = row["measurement"]
            if not isinstance(expected, dict) or score.get("measurement") != expected:
                raise StudyError("measurement differs from the authored protocol")
            validate_protocol(expected)
            if score.get("complete") is not True or score.get("gate_green") is not True:
                raise StudyError("measurement is incomplete or baseline is not green")
            raw_baselines = score.get("baseline_runs")
            if not isinstance(raw_baselines, list) or len(raw_baselines) != 2:
                raise StudyError("two baseline execution records are required")
            baselines = [_recorded_go(raw) for raw in raw_baselines]
            if not all(b.green for b in baselines) or baselines[0].passing != baselines[1].passing:
                raise StudyError("baseline execution records do not prove a stable green suite")
            if score.get("baseline_tests") != sorted([list(t) for t in baselines[0].passing]):
                raise StudyError("baseline inventory contradicts recorded executions")
            observations = score.get("mutations")
            if not isinstance(observations, list) or not observations:
                raise StudyError("missing mutation observations")
            ids = []
            killed = 0
            for observation in observations:
                if not isinstance(observation, dict):
                    raise StudyError("invalid mutation observation")
                ids.append(require_text(observation.get("id"), "mutation id"))
                if observation.get("status") not in ("killed", "survived"):
                    raise StudyError("invalid or unexecuted mutation")
                executed = _recorded_go(observation.get("execution"))
                if not executed.valid:
                    raise StudyError("mutation execution is invalid")
                if observation["status"] == "killed" and not executed.failing & baselines[0].passing:
                    raise StudyError("kill claim has no detecting baseline test")
                if observation["status"] == "survived" and (not executed.green or executed.passing != baselines[0].passing):
                    raise StudyError("survival claim changed or failed the test inventory")
                killed += observation["status"] == "killed"
            if len(set(ids)) != len(ids):
                raise StudyError("duplicate mutation observation")
            if ids != expected["mutation_ids"]:
                raise StudyError("mutation observations differ from the authored inventory")
            if type(score.get("kill_rate")) not in (int, float) or score["kill_rate"] != killed / len(ids):
                raise StudyError("kill rate contradicts recorded executions")
            row.update(score_verified=True, kill=killed, of=len(ids),
                       kill_rate=killed / len(ids), mutation_ids=ids)
        except StudyError as exc:
            row["errors"].append(str(exc))
    comparable = [r for r in rows if r["score_verified"]]
    if len({digest([r["measurement"], r["context"]["base_revision"],
                    r["context"]["brief_sha256"], r["mutation_ids"]]) for r in comparable}) > 1:
        for row in comparable:
            row["score_verified"] = False
            row["errors"].append("study mixes bases, briefs, or measurement inventories")


def write_new_json(path: Path, value: object) -> None:
    """Create an output exclusively; never overwrite an input or prior result."""
    data = json.dumps(value, indent=2, allow_nan=False) + "\n"
    with path.open("x", encoding="utf-8") as output:
        output.write(data)
