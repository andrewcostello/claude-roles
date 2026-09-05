"""Offline controls using real Go binaries, Git clones, and owned fixtures."""
import copy
import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch

import scorecard
import study_common as common
import study_execution as execution


class ExecutionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temporary.name)
        cls.private = cls.root / "private"
        cls.private.mkdir()
        cls.env = execution.execution_env(cls.private)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def setUp(self):
        self.case = self.root / self._testMethodName
        self.case.mkdir()
        self.repo = self.case / "worktree-ARM"
        self.module = "cmd/probe"
        self.mod = self.repo / self.module
        self.mod.mkdir(parents=True)
        (self.mod / "go.mod").write_text("module example.invalid/probe\n\ngo 1.21\n")
        (self.mod / "value.go").write_text("package probe\n\nfunc Value() int { return 1 }\n")
        (self.mod / "value_test.go").write_text(
            'package probe\nimport "testing"\n'
            'func TestValue(t *testing.T) { if Value() != 1 { t.Fatal("wrong value") } }\n')
        execution.git(self.repo, self.env, "init", "--quiet")
        self.commit()
        self.mutation = {"id": "wrong-value", "file": "value.go", "before": "return 1", "after": "return 2"}

    def commit(self):
        execution.git(self.repo, self.env, "add", ".")
        execution.git(self.repo, self.env, "-c", "user.name=Study Fixture", "-c",
                      "user.email=study@example.invalid", "commit", "--quiet", "-m", "fixture")
        self.revision = execution.git(self.repo, self.env, "rev-parse", "HEAD").strip()

    def observe(self, mutation=None, seconds=90):
        return execution.observe(self.repo, self.revision, self.module, self.private, self.env, seconds, mutation)

    def score(self, mutations=None):
        inventory = {"module": self.module, "mutations": mutations or [self.mutation]}
        with patch.object(scorecard, "execution_env", return_value=self.env):
            return scorecard.score_arm(self.repo, self.module, inventory, revision=self.revision,
                                       seconds=90, scratch_parent=self.private, trusted_local_code=True)

    def test_real_go_pass_and_assertion_failure(self):
        self.assertTrue(self.observe().green)
        failure = self.observe(self.mutation)
        self.assertTrue(failure.valid)
        self.assertEqual(failure.failing, {("example.invalid/probe", "TestValue")})

    def test_real_testmain_nonzero_is_not_green(self):
        (self.mod / "value_test.go").write_text(
            'package probe\nimport ("testing"; "os")\n'
            'func TestValue(t *testing.T) {}\n'
            'func TestMain(m *testing.M) { m.Run(); os.Exit(1) }\n')
        self.commit()
        result = self.observe()
        self.assertFalse(result.green)
        self.assertFalse(result.valid)

    def test_mixed_package_build_failure_is_not_green(self):
        broken = self.mod / "broken"
        broken.mkdir()
        (broken / "bad.go").write_text("package broken\nvar X = notDeclared\n")
        self.commit()
        self.assertFalse(self.observe().valid)

    def test_compile_breaking_mutant_is_invalid_with_assertion_free_tests(self):
        (self.mod / "value_test.go").write_text('package probe\nimport "testing"\nfunc TestNothing(t *testing.T) {}\n')
        self.commit()
        mutation = {**self.mutation, "after": "return notDeclared"}
        result = self.score([mutation])
        self.assertTrue(result["gate_green"])
        self.assertFalse(result["complete"])
        self.assertIsNone(result["kill_rate"])
        self.assertEqual(result["mutations"][0]["status"], "invalid")

    def test_complete_inventory_has_distinct_killed_and_survived_results(self):
        source_before = (self.mod / "value.go").read_bytes()
        mutation = {**self.mutation, "id": "equivalent", "after": "return 1 + 0"}
        result = self.score([self.mutation, mutation])
        self.assertTrue(result["complete"], result)
        self.assertEqual(result["kill_rate"], 0.5)
        self.assertEqual([m["status"] for m in result["mutations"]], ["killed", "survived"])
        self.assertEqual((self.mod / "value.go").read_bytes(), source_before)
        execution.check_source(self.repo, self.revision, self.env)

    def test_missing_ambiguous_and_broken_sites_do_not_get_a_rate(self):
        mutations = [{**self.mutation, "id": "missing", "before": "absent text"},
                     {**self.mutation, "id": "ambiguous", "before": "r"},
                     {**self.mutation, "id": "broken", "after": "return notDeclared"}]
        result = self.score(mutations)
        self.assertFalse(result["complete"])
        self.assertIsNone(result["kill_rate"])
        self.assertEqual([m["status"] for m in result["mutations"]], ["invalid"] * 3)

    def test_concurrent_edit_to_submission_survives_and_invalidates_score(self):
        real_observe = scorecard.observe
        changed = (self.mod / "value.go").read_text() + "// concurrent editor\n"
        calls = []

        def edit_during_observation(*args, **kwargs):
            result = real_observe(*args, **kwargs)
            if not calls:
                (self.mod / "value.go").write_text(changed)
            calls.append(True)
            return result

        with patch.object(scorecard, "observe", side_effect=edit_during_observation):
            result = self.score()
        self.assertFalse(result["complete"])
        self.assertIsNone(result["kill_rate"])
        self.assertEqual((self.mod / "value.go").read_text(), changed)

    def test_dirty_short_revision_and_symlink_sources_are_refused(self):
        with self.assertRaises(common.StudyError):
            execution.check_source(self.repo, self.revision[:7], self.env)
        (self.mod / "value.go").write_text("uncommitted edit")
        self.assertFalse(self.score()["complete"])
        (self.repo / "link").symlink_to(self.mod / "value.go")
        self.commit()
        with self.assertRaises(common.StudyError):
            execution.check_source(self.repo, self.revision, self.env)

    def test_real_concurrent_writer_is_preserved_during_go_execution(self):
        started = self.case / "go-started"
        (self.mod / "value_test.go").write_text(
            'package probe\nimport ("testing"; "os"; "time")\n'
            f'func TestValue(t *testing.T) {{ os.WriteFile({json.dumps(str(started))}, []byte("started"), 0600); '
            'time.Sleep(300 * time.Millisecond); if Value() != 1 { t.Fatal("wrong value") } }\n')
        self.commit()
        changed = (self.mod / "value.go").read_text() + "// independent writer\n"
        stop = threading.Event()
        written = threading.Event()

        def writer():
            while not stop.wait(0.01):
                if started.exists():
                    (self.mod / "value.go").write_text(changed)
                    written.set()
                    return

        thread = threading.Thread(target=writer)
        thread.start()
        try:
            result = self.score()
        finally:
            stop.set()
            thread.join(timeout=2)
        self.assertFalse(thread.is_alive())
        self.assertTrue(written.is_set())
        self.assertFalse(result["complete"])
        self.assertEqual((self.mod / "value.go").read_text(), changed)

    def test_tests_that_rewrite_source_cannot_supply_evidence(self):
        (self.mod / "value_test.go").write_text(
            'package probe\nimport ("testing"; "os")\n'
            'func TestValue(t *testing.T) { os.WriteFile("value.go", []byte("changed"), 0600) }\n')
        self.commit()
        with self.assertRaisesRegex(common.StudyError, "changed its source"):
            self.observe()
        self.assertIn("return 1", (self.mod / "value.go").read_text())
        execution.check_source(self.repo, self.revision, self.env)

    def test_untrusted_execution_is_not_enabled_by_default(self):
        with patch.object(scorecard, "observe") as runner:
            result = scorecard.score_arm(self.repo, self.module, {"module": self.module, "mutations": [self.mutation]},
                                         revision=self.revision)
        runner.assert_not_called()
        self.assertFalse(result["complete"])

    def test_changed_harness_identity_invalidates_measurement(self):
        real_protocol = scorecard.measurement_protocol
        calls = []

        def changing_protocol(*args):
            protocol = real_protocol(*args)
            if calls:
                protocol["harness_sha256"] = "f" * 64
            calls.append(True)
            return protocol

        with patch.object(scorecard, "measurement_protocol", side_effect=changing_protocol):
            result = self.score()
        self.assertFalse(result["complete"])
        self.assertIsNone(result["kill_rate"])
        self.assertIn("harness or runtime changed", result["errors"][-1])

    def test_environment_does_not_inherit_credentials_or_go_overrides(self):
        with patch.dict(os.environ, {"SECRET_TEST_KEY": "secret", "GOFLAGS": "-broken",
                                     "GIT_CONFIG_COUNT": "1", "GOWORK": "/bad/workspace"}):
            env = execution.execution_env(self.private)
        self.assertNotIn("SECRET_TEST_KEY", env)
        self.assertNotIn("GIT_CONFIG_COUNT", env)
        self.assertEqual(env["GOFLAGS"], "")
        self.assertEqual(env["GOWORK"], "off")

    def test_timeout_bounds_parent_and_child_process_group(self):
        marker = self.case / "late-marker"
        child = f"import time; from pathlib import Path; time.sleep(2); Path({str(marker)!r}).touch()"
        parent = f"import subprocess,sys,time; subprocess.Popen([sys.executable,'-c',{child!r}]); time.sleep(10)"
        started = time.monotonic()
        with self.assertRaisesRegex(common.StudyError, "exceeded"):
            execution.run_command([sys.executable, "-c", parent], self.case, self.env, 1)
        self.assertLess(time.monotonic() - started, 3)
        time.sleep(1.2)
        self.assertFalse(marker.exists())

    def test_timeout_does_not_wait_forever_for_detached_child_pipes(self):
        pid_file = self.case / "child.pid"
        child = f"import os,time; from pathlib import Path; Path({str(pid_file)!r}).write_text(str(os.getpid())); time.sleep(10)"
        parent = f"import subprocess,sys,time; subprocess.Popen([sys.executable,'-c',{child!r}],start_new_session=True); time.sleep(10)"
        started = time.monotonic()
        try:
            with self.assertRaisesRegex(common.StudyError, "exceeded"):
                execution.run_command([sys.executable, "-c", parent], self.case, self.env, 1)
            self.assertLess(time.monotonic() - started, 3)
        finally:
            if pid_file.exists():
                try:
                    os.kill(int(pid_file.read_text()), signal.SIGKILL)
                except ProcessLookupError:
                    pass

    def test_cli_end_to_end_binds_completed_arm_and_bakeoff_uses_same_contract(self):
        mutation_file = self.case / "mutations.json"
        inventory = {"module": self.module, "mutations": [self.mutation]}
        mutation_file.write_text(json.dumps(inventory))
        protocol = scorecard.measurement_protocol(inventory, 90, self.private, self.env)
        task = {"key": "ARM", "role": "seals", "agent": "fixture-agent", "model": "fixture-model",
                "agent_runtime": "fixture-cli/1",
                "effort": "high", "description": "Assert value.", "status": "To Do"}
        spec = {"tasks": [task], "base_branch": self.revision, "measurement": protocol}
        run = copy.deepcopy(spec)
        run["tasks"][0].update(status="Done", study_evidence={"base_revision": self.revision,
            "subject_revision": self.revision, "invocation_id": "fixture/1", "agent_runtime": "fixture-cli/1"})
        spec_path, run_path, output_path = [self.case / n for n in ("spec.json", "run.json", "score.json")]
        spec_path.write_text(json.dumps(spec))
        run_path.write_text(json.dumps(run))
        command = [sys.executable, str(scorecard.HERE.parent / "bakeoff-seals/score.py"),
                   "--spec", str(spec_path), "--run", str(run_path), "--mutations", str(mutation_file),
                   "--worktree-base", str(self.case), "--json-out", str(output_path),
                   "--scratch-parent", str(self.private), "--timeout-seconds", "90", "--trusted-local-code"]
        result = subprocess.run(command, capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        rows = common.rows_from(run_path, spec_path)
        common.merge_scores(rows, output_path)
        self.assertTrue(rows[0]["score_verified"], rows)
        command = [sys.executable, str(scorecard.HERE / "analyse.py"), "--study",
                   f"fixture:{run_path}:{spec_path}:{output_path}"]
        analysis = subprocess.run(command, capture_output=True, text=True, timeout=30)
        self.assertEqual(analysis.returncode, 0, analysis.stdout + analysis.stderr)
        self.assertIn("1/1 verified measurements", analysis.stdout)


if __name__ == "__main__":
    unittest.main()
