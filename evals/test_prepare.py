import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from prepare import BASELINE, FILES, prepare


class PrepareTests(unittest.TestCase):
    def test_pinned_export_is_complete_and_reproducible(self):
        repo = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as root:
            first, second = Path(root) / "first", Path(root) / "second"
            a = prepare(repo, first)
            b = prepare(repo, second)
            self.assertEqual(a, b)
            self.assertEqual(a["baseline_commit"], BASELINE)
            self.assertEqual(json.loads((first / "provenance.json").read_text()), a)
            self.assertEqual(
                {p.relative_to(first / "environment/gates").as_posix() for p in (first / "environment/gates").rglob("*") if p.is_file()},
                set(FILES),
            )
            for name, digest in a["sha256"].items():
                self.assertEqual(hashlib.sha256((first / name).read_bytes()).hexdigest(), digest)

    def test_existing_output_is_never_overwritten(self):
        repo = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as root:
            output = Path(root)
            marker = output / "keep.txt"
            marker.write_text("keep")
            with self.assertRaises(FileExistsError):
                prepare(repo, output)
            self.assertEqual(marker.read_text(), "keep")
            self.assertEqual(list(output.iterdir()), [marker])


if __name__ == "__main__":
    unittest.main()
