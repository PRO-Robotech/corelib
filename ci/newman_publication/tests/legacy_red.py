"""First behavioral RED of the actual Kaname predecessor; no missing-module claim.

Run with --legacy /exact/checkout/.github/scripts/redact-newman-report.py.
Assertions express the approved new publication boundary, so this predecessor is
expected to fail. This executable is retained as first-RED evidence only; it is
not silently switched to a new implementation to fabricate before/after parity.
"""
import argparse
import json
import shutil
from pathlib import Path
import sys
import tempfile
import unittest

from support import EVIDENCE, Fixture, digest, fixture_hash_check, invoke

LEGACY = None
CANARY = "synthetic apricot paper"


class LegacyPublicationBoundary(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="np-legacy-")
        self.addCleanup(self.temp.cleanup)
        self.fixture = Fixture(Path(self.temp.name))

    def run_legacy(self, label):
        out = self.fixture.base / label
        process = invoke([sys.executable, LEGACY, "--in", self.fixture.raw, "--out", out],
                         label="legacy-" + label)
        shutil.copytree(self.fixture.raw, process.capture_dir / "input")
        if out.exists():
            shutil.copytree(out, process.capture_dir / "output")
        return process, out

    def legal_twin(self):
        self.assertEqual(self.fixture.reader_verdict()["suite_rc"], 0)
        proc, out = self.run_legacy("lawful")
        self.assertEqual(proc.returncode, 0, "lawful real Newman input must be executable")
        before = self.fixture.reader_verdict(suffix="before")
        after = self.fixture.reader_verdict((out / "fixture.json").read_bytes(), "after")
        self.assertEqual(before, after, "lawful twin changed real reader verdict/counts")
        return out

    def test_01_opaque_body_has_no_public_copy(self):
        self.legal_twin()
        self.fixture.change_report(lambda d: d["run"]["executions"][0]["response"].update(
            stream={"type": "Buffer", "data": list(CANARY.encode())}))
        proc, out = self.run_legacy("opaque")
        self.assertEqual(proc.returncode, 0)
        doc = json.loads((out / "fixture.json").read_text())
        body = doc["run"]["executions"][0]["response"].get("stream")
        self.assertIsNone(body, "opaque response bytes survived purported publication cleaning")

    def test_02_untrusted_metadata_key_removed(self):
        self.legal_twin()
        self.fixture.change_report(lambda d: d["collection"]["info"].update({CANARY: "ordinary value"}))
        proc, out = self.run_legacy("metadata")
        self.assertEqual(proc.returncode, 0)
        self.assertTrue(CANARY.encode() not in (out / "fixture.json").read_bytes(),
                         "untrusted metadata key survived while command declared CLEAN")

    def test_03_unknown_file_refuses_complete_publication(self):
        self.legal_twin()
        (self.fixture.raw / "unrecognized.bin").write_bytes(b"ordinary bytes")
        proc, _ = self.run_legacy("unknown")
        self.assertNotEqual(proc.returncode, 0, "unknown mandatory input was silently omitted")

    def test_04_malformed_json_has_closed_diagnostic(self):
        self.legal_twin()
        old = self.fixture.report
        old.unlink()
        (self.fixture.raw / (CANARY + ".json")).write_text("{")
        proc, _ = self.run_legacy("malformed")
        self.assertNotEqual(proc.returncode, 0)
        self.assertTrue(b"Traceback" not in proc.stderr, "parser traceback crossed public diagnostic boundary")

    def test_05_raw_log_text_has_no_public_copy(self):
        self.legal_twin()
        (self.fixture.raw / "run.log").write_text(CANARY + "\n")
        proc, out = self.run_legacy("log")
        self.assertEqual(proc.returncode, 0)
        self.assertTrue(CANARY.encode() not in (out / "run.log").read_bytes(),
                         "free log text survived purported publication cleaning")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--legacy", required=True, type=Path)
    args = parser.parse_args()
    LEGACY = args.legacy.resolve()
    fixtures = fixture_hash_check()
    (EVIDENCE / "provenance.json").write_text(json.dumps({
        "legacy": str(LEGACY), "legacy_sha256": digest(LEGACY.read_bytes()),
        "holder_sha256": digest(Path(__file__).read_bytes()), "fixture_hashes": fixtures,
        "scope": "Actual predecessor behavior only; not new corelib CLI or runtime/retention proof",
    }, indent=2) + "\n")
    print("evidence=" + str(EVIDENCE), flush=True)
    unittest.main(argv=[sys.argv[0]], verbosity=2)
