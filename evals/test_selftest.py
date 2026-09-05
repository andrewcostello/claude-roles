import json
from pathlib import Path
import tempfile
import unittest

from selftest import checked_result


class ResultTests(unittest.TestCase):
    def fixture(self, root: Path, *, passed=True, reward=1, exception=None):
        trial = root / "trial"
        (trial / "verifier").mkdir(parents=True)
        (trial / "result.json").write_text(json.dumps({
            "exception_info": exception, "verifier_result": {"rewards": {"reward": reward}},
        }))
        report = {"status": "passed" if passed else "failed", "cases": [{"name": "check", "passed": passed}]}
        path = trial / "verifier/report.json"
        path.write_text(json.dumps(report))
        return path, report

    def test_positive_and_negative_results(self):
        for passed in (True, False):
            with self.subTest(passed=passed), tempfile.TemporaryDirectory() as root:
                path = Path(root)
                self.fixture(path, passed=passed, reward=int(passed))
                self.assertEqual(checked_result(path, int(passed))["checks"], 1)

    def test_infrastructure_failure_is_not_a_successful_negative_control(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            self.fixture(path, passed=False, reward=0, exception={"type": "TimeoutError"})
            with self.assertRaises(ValueError):
                checked_result(path, 0)

    def test_missing_and_duplicate_trials_are_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            with self.assertRaises(ValueError):
                checked_result(path, 0)
            self.fixture(path)
            (path / "second").mkdir()
            (path / "second/result.json").write_text("{}")
            with self.assertRaises(ValueError):
                checked_result(path, 1)

    def test_incomplete_malformed_and_inconsistent_reports_are_rejected(self):
        for kind in ("empty", "error", "inconsistent", "duplicate", "string boolean", "wrong reward"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as root:
                path = Path(root)
                report_path, report = self.fixture(path)
                if kind == "empty":
                    report["cases"] = []
                elif kind == "error":
                    report["status"] = "error"
                elif kind == "inconsistent":
                    report["cases"][0]["passed"] = False
                elif kind == "duplicate":
                    report["cases"] *= 2
                elif kind == "string boolean":
                    report["cases"][0]["passed"] = "false"
                report_path.write_text(json.dumps(report))
                with self.assertRaises(ValueError):
                    checked_result(path, 0 if kind == "wrong reward" else 1)


if __name__ == "__main__":
    unittest.main()
