"""Regression and invariant tests for study ingestion and reporting."""
import contextlib
import copy
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import analyse
import report
import study_common as common
import study_execution as execution

HERE = Path(__file__).resolve().parent


class StudyTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.spec_path = self.root / "spec.json"
        self.run_path = self.root / "run.json"
        self.score_path = self.root / "score.json"
        arm = {"key": "ARM", "agent": "fixture-agent", "model": "fixture-model", "effort": "high",
               "agent_runtime": "fixture-cli-1/high/scoped",
               "description": "Assert the public contract.", "role": "seals", "status": "To Do"}
        self.protocol = {"oracle_sha256": "a" * 64, "harness_sha256": "b" * 64,
                         "module": "cmd/probe", "command": execution.test_command(30),
                         "go_version": "go version fixture", "platform": "fixture-platform",
                         "mutation_ids": ["value-changed"]}
        self.spec = {"tasks": [arm], "base_branch": "a" * 40, "measurement": self.protocol}
        self.run = copy.deepcopy(self.spec)
        self.run["tasks"][0].update(status="Done", cost_usd=0, study_evidence={
            "base_revision": "a" * 40, "subject_revision": "b" * 40,
            "invocation_id": "run-1/ARM/attempt-1", "agent_runtime": "fixture-cli-1/high/scoped"})
        self.save_inputs()

    def save_inputs(self):
        self.spec_path.write_text(json.dumps(self.spec))
        self.run_path.write_text(json.dumps(self.run))

    def rows(self):
        return common.rows_from(self.run_path, self.spec_path)

    def score(self, row):
        def raw(failure=False):
            terminal = "fail" if failure else "pass"
            events = [{"Package": "fixture", "Action": "start"},
                      {"Package": "fixture", "Action": "run", "Test": "TestValue"},
                      {"Package": "fixture", "Action": terminal, "Test": "TestValue"},
                      {"Package": "fixture", "Action": terminal}]
            return {"exit_code": 1 if failure else 0, "stdout": "\n".join(json.dumps(e) for e in events), "stderr": ""}
        return {"schema_version": 2, "arm": row["key"], "context": copy.deepcopy(row["context"]),
                "measurement": copy.deepcopy(row["measurement"]), "complete": True, "gate_green": True,
                "baseline_runs": [raw(), raw()],
                "baseline_tests": [["fixture", "TestValue"]], "kill_rate": 1.0,
                "mutations": [{"id": "value-changed", "status": "killed", "execution": raw(True)}]}

    def merge(self, rows, scores):
        self.score_path.write_text(json.dumps(scores))
        common.merge_scores(rows, self.score_path)

    def test_completed_pinned_arm_is_included_and_verified(self):
        rows = self.rows()
        self.assertEqual([r["key"] for r in rows], ["ARM"])
        self.assertTrue(rows[0]["provenance_ok"])
        self.merge(rows, [self.score(rows[0])])
        self.assertTrue(rows[0]["score_verified"])
        self.assertEqual(rows[0]["kill_rate"], 1.0)

    def test_absent_missing_same_file_and_hardlinked_specs_are_refused(self):
        alias = self.root / "alias.json"
        os.link(self.run_path, alias)
        for path in (None, self.root / "missing.json", self.run_path, alias):
            with self.subTest(path=path), self.assertRaises(common.StudyError):
                common.rows_from(self.run_path, path)

    def test_each_pin_and_identity_field_is_required(self):
        for field in ("agent", "model", "effort", "role", "description", "agent_runtime"):
            original = self.run["tasks"][0][field]
            for wrong in (None, "different"):
                with self.subTest(field=field, wrong=wrong):
                    self.run["tasks"][0][field] = wrong
                    self.save_inputs()
                    if field == "role" and wrong is None:
                        with self.assertRaises(common.StudyError):
                            self.rows()
                    else:
                        self.assertFalse(self.rows()[0]["provenance_ok"])
            self.run["tasks"][0][field] = original

    def test_each_execution_identity_field_is_required(self):
        evidence = self.run["tasks"][0]["study_evidence"]
        for field in list(evidence):
            original = evidence.pop(field)
            with self.subTest(field=field):
                self.save_inputs()
                self.assertFalse(self.rows()[0]["provenance_ok"])
            evidence[field] = original

    def test_short_or_moved_revisions_are_not_provenance(self):
        for field, value in (("subject_revision", "b" * 7), ("base_revision", "c" * 40)):
            evidence = self.run["tasks"][0]["study_evidence"]
            original = evidence[field]
            evidence[field] = value
            self.save_inputs()
            self.assertFalse(self.rows()[0]["provenance_ok"])
            evidence[field] = original

    def test_missing_and_extra_arms_remain_visible(self):
        self.run["tasks"][0]["key"] = "UNEXPECTED"
        self.save_inputs()
        rows = self.rows()
        self.assertEqual([r["key"] for r in rows], ["ARM", "UNEXPECTED"])
        self.assertFalse(any(r["provenance_ok"] for r in rows))

    def test_scaffold_selection_uses_role_not_name_or_completion(self):
        self.spec["tasks"][0]["key"] = "ARM-SCAFFOLD"
        self.run["tasks"][0]["key"] = "ARM-SCAFFOLD"
        self.spec["tasks"].append({"key": "PARENT", "role": "scaffold", "status": "Done"})
        self.run["tasks"].append({"key": "PARENT", "role": "scaffold", "status": "Done"})
        self.save_inputs()
        self.assertEqual([r["key"] for r in self.rows()], ["ARM-SCAFFOLD"])

    def test_duplicate_rows_and_json_keys_are_refused(self):
        for doc in (self.spec, self.run):
            doc["tasks"].append(copy.deepcopy(doc["tasks"][0]))
            self.save_inputs()
            with self.assertRaises(common.StudyError):
                self.rows()
            doc["tasks"].pop()
        for value in ('{"x":1,"x":2}', '{"x":NaN}', '{"x":Infinity}', '{"x":1e999}'):
            with self.subTest(value=value), self.assertRaises(common.StudyError):
                common.json_loads(value)

    def test_yaml_is_portable_and_duplicate_keys_fail(self):
        path = self.root / "input.yaml"
        path.write_text("tasks:\n  - key: A\n    role: seals\n")
        self.assertEqual(list(common.task_rows(common.load_document(path))), ["A"])
        path.write_text("tasks: []\ntasks: []\n")
        with self.assertRaises(common.StudyError):
            common.load_document(path)

    def test_malformed_task_containers_and_empty_studies_fail(self):
        for tasks in (None, {}, "ARM", [], [None], [{"key": "../ARM", "role": "seals"}]):
            with self.subTest(tasks=tasks), self.assertRaises(common.StudyError):
                common.task_rows({"tasks": tasks})

    def test_each_score_binding_field_is_checked(self):
        row = self.rows()[0]
        for field in row["context"]:
            with self.subTest(field=field):
                rows = self.rows()
                score = self.score(rows[0])
                score["context"][field] = "wrong"
                self.merge(rows, [score])
                self.assertFalse(rows[0]["score_verified"])

    def test_each_measurement_field_is_checked(self):
        for field in self.protocol:
            with self.subTest(field=field):
                rows = self.rows()
                score = self.score(rows[0])
                score["measurement"][field] = "wrong"
                self.merge(rows, [score])
                self.assertFalse(rows[0]["score_verified"])

    def test_stale_inputs_do_not_accept_previous_scores(self):
        score = self.score(self.rows()[0])
        self.run["tasks"][0]["cost_usd"] = 1
        self.save_inputs()
        rows = self.rows()
        self.merge(rows, [score])
        self.assertFalse(rows[0]["score_verified"])

    def test_legacy_red_incomplete_and_invalid_scores_fail(self):
        changes = [("schema_version", 1), ("complete", False), ("gate_green", False),
                   ("complete", 1), ("gate_green", "true"), ("mutations", []),
                   ("mutations", [{"id": "value-changed", "status": "invalid"}]),
                   ("mutations", [{"id": "wrong-id", "status": "killed"}])]
        for field, value in changes:
            with self.subTest(field=field, value=value):
                rows = self.rows()
                score = self.score(rows[0])
                score[field] = value
                self.merge(rows, [score])
                self.assertFalse(rows[0]["score_verified"])

    def test_missing_extra_and_duplicate_score_rows_fail(self):
        rows = self.rows()
        score = self.score(rows[0])
        for scores in ([], [score, score], [{**score, "arm": "unexpected"}]):
            with self.subTest(scores=scores), self.assertRaises(common.StudyError):
                self.merge(rows, scores)
        with self.assertRaises(common.StudyError):
            common.merge_scores(rows, self.root / "absent.json")

    def test_incomplete_trials_remain_visible_and_zero_cost_is_preserved(self):
        self.run["tasks"][0]["status"] = "Blocked"
        self.save_inputs()
        rows = self.rows()
        self.merge(rows, [self.score(rows[0])])
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            analyse.report(rows, "probe")
        self.assertIn("UNVERIFIED ARM (Blocked)", output.getvalue())
        self.assertIn("recorded cost=0", output.getvalue())
        self.assertIn("n=0/1", output.getvalue())

    def test_comment_only_matches_are_never_called_coverage(self):
        source = self.root / "comments.go"
        source.write_text("// MainDelegat os.Exit Stderr ArtifactWritten SeamErrors Subcommand\n"
                          "// Chdir MainForwards Registrar raw FlagError SetFlags\n")
        total, matches = report.clause_mentions([source])
        self.assertEqual((len(matches), total), (12, 12))
        self.assertFalse(hasattr(report, "clause_coverage"))
        with self.assertRaises(FileNotFoundError):
            report.clause_mentions([self.root / "absent.go"])

    def test_output_never_overwrites_an_existing_file(self):
        before = self.spec_path.read_bytes()
        with self.assertRaises(FileExistsError):
            common.write_new_json(self.spec_path, {"changed": True})
        self.assertEqual(self.spec_path.read_bytes(), before)

    def test_duplicate_invocations_are_not_independent_replicates(self):
        for doc in (self.spec, self.run):
            row = copy.deepcopy(doc["tasks"][0])
            row["key"] = "ARM-2"
            doc["tasks"].append(row)
        self.save_inputs()
        rows = self.rows()
        self.assertFalse(any(r["provenance_ok"] for r in rows))
        self.assertTrue(all("duplicate invocation" in r["errors"][-1] for r in rows))

    def test_score_claims_must_agree_with_raw_execution_records(self):
        def missing_baseline(score):
            score.pop("baseline_runs")
        def wrong_baseline_exit(score):
            score["baseline_runs"][0]["exit_code"] = 1
        def missing_test_inventory(score):
            score["baseline_tests"] = []
        def missing_mutant_log(score):
            score["mutations"][0].pop("execution")
        def false_kill(score):
            score["mutations"][0]["execution"] = score["baseline_runs"][0]
        def false_survival(score):
            score["mutations"][0]["status"] = "survived"
        def wrong_rate(score):
            score["kill_rate"] = 0.5
        def wrong_schema_type(score):
            score["schema_version"] = 2.0
        for change in (missing_baseline, wrong_baseline_exit, missing_test_inventory, missing_mutant_log,
                       false_kill, false_survival, wrong_rate, wrong_schema_type):
            with self.subTest(change=change.__name__):
                rows = self.rows()
                score = self.score(rows[0])
                change(score)
                self.merge(rows, [score])
                self.assertFalse(rows[0]["score_verified"])

    def test_rejected_reload_cannot_retain_a_previous_verified_score(self):
        rows = self.rows()
        self.merge(rows, [self.score(rows[0])])
        self.assertTrue(rows[0]["score_verified"])
        with self.assertRaises(common.StudyError):
            common.merge_scores(rows, None)
        self.assertFalse(rows[0]["score_verified"])
        self.assertNotIn("kill_rate", rows[0])

    def test_malformed_protocols_fail_even_when_score_copies_them(self):
        changes = [("module", "../escape"), ("mutation_ids", ["x", "x"]),
                   ("mutation_ids", []), ("oracle_sha256", "bad"), ("platform", None),
                   ("go_version", ""), ("command", ["go", "test", "./..."]),
                   ("command", ["go", "test", "-json", "-count=1", "-timeout=121s", "./..."])]
        for field, value in changes:
            with self.subTest(field=field):
                rows = self.rows()
                rows[0]["measurement"][field] = value
                self.merge(rows, [self.score(rows[0])])
                self.assertFalse(rows[0]["score_verified"])

    def test_different_briefs_are_not_pooled_as_the_same_task(self):
        for doc in (self.spec, self.run):
            second = copy.deepcopy(doc["tasks"][0])
            second.update(key="ARM-2", description="Different public contract.")
            if "study_evidence" in second:
                second["study_evidence"]["invocation_id"] = "independent-attempt"
            doc["tasks"].append(second)
        self.save_inputs()
        rows = self.rows()
        self.merge(rows, [self.score(r) for r in rows])
        self.assertFalse(any(r["score_verified"] for r in rows))

    def test_legacy_or_missing_cli_inputs_return_nonzero(self):
        commands = [
            [str(HERE / "analyse.py"), "--study", f"old:{self.run_path}"],
            [str(HERE / "report.py"), "--run-yaml", str(self.run_path), "--spec", str(self.spec_path),
             "--score-json", str(self.root / "missing.json")],
            [str(HERE.parent / "bakeoff-seals/score.py"), "--spec", str(self.spec_path),
             "--run", str(self.run_path)],
        ]
        for command in commands:
            with self.subTest(command=command):
                result = subprocess.run([sys.executable, *command], capture_output=True, text=True, timeout=30)
                self.assertNotEqual(result.returncode, 0)


class GoParserTests(unittest.TestCase):
    def stream(self, *, package="example", test="TestValue", failure=False):
        terminal = "fail" if failure else "pass"
        return [{"Action": "start", "Package": package},
                {"Action": "run", "Package": package, "Test": test},
                {"Action": terminal, "Package": package, "Test": test},
                {"Action": terminal, "Package": package}]

    def parse(self, events, code=0, stderr=""):
        return execution.parse_go_result(subprocess.CompletedProcess(
            [], code, "\n".join(json.dumps(e) for e in events), stderr))

    def test_success_and_assertion_failure_are_distinct_valid_results(self):
        self.assertTrue(self.parse(self.stream()).green)
        failed = self.parse(self.stream(failure=True), code=1)
        self.assertTrue(failed.valid)
        self.assertFalse(failed.green)
        self.assertEqual(failed.failing, {("example", "TestValue")})

    def test_passing_tests_do_not_hide_package_or_process_failure(self):
        events = self.stream()
        events[-1]["Action"] = "fail"
        for variant in (events, self.stream()):
            self.assertFalse(self.parse(variant, code=1).valid)

    def test_package_qualified_identity_preserves_colliding_test_names(self):
        events = self.stream(package="one") + self.stream(package="two", failure=True)
        result = self.parse(events, code=1)
        self.assertTrue(result.valid)
        self.assertEqual(result.passing, {("one", "TestValue")})
        self.assertEqual(result.failing, {("two", "TestValue")})

    def test_every_missing_event_or_duplicate_terminal_is_refused(self):
        complete = self.stream()
        for index in range(len(complete)):
            with self.subTest(index=index):
                self.assertFalse(self.parse(complete[:index] + complete[index + 1:]).valid)
        self.assertFalse(self.parse(complete + [complete[-1]]).valid)
        reordered = [complete[i] for i in (0, 3, 1, 2)]
        self.assertFalse(self.parse(reordered).valid)

    def test_empty_skipped_malformed_and_build_results_are_invalid(self):
        skipped = self.stream()
        skipped[2]["Action"] = "skip"
        for events, code, stderr in (([], 0, ""), (skipped, 0, ""), ([None], 0, ""),
                                     (self.stream(), 0, "build diagnostics"),
                                     ([{"Action": "build-fail", "ImportPath": "example"}], 1, "")):
            with self.subTest(events=events):
                self.assertFalse(self.parse(events, code, stderr).valid)
        self.assertFalse(execution.parse_go_result(subprocess.CompletedProcess([], 0, "not JSON", "")).valid)

    def test_child_test_failure_and_parallel_pause_continue_are_supported(self):
        events = self.stream()
        events[2:2] = [{"Action": action, "Package": "example", "Test": "TestValue"}
                       for action in ("pause", "cont")]
        self.assertTrue(self.parse(events).green)

    def test_unsafe_mutations_and_time_limits_are_rejected(self):
        for path in ("../value.go", "/value.go", "nested/../../value.go", "value_test.go", "a\\value.go", ".git/config"):
            with self.subTest(path=path), self.assertRaises(common.StudyError):
                common.mutations_from({"module": "cmd/probe", "mutations": [
                    {"id": "bad", "file": path, "before": "1", "after": "2"}]})
        for value in (0, -1, 121, True, 1.5):
            with self.subTest(value=value), self.assertRaises(common.StudyError):
                execution.test_command(value)


if __name__ == "__main__":
    unittest.main()
