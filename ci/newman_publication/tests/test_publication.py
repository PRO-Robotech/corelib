"""CI-NP-01/02/03/06 scoped projection holder, independent of implementation.

Reader fixtures are real Newman executions. Legacy behavior RED is captured by
legacy_red.py before a missing future CLI can be classified as a capability gap.
SDK, upload byte binding, consumer workflows, full runtime and retention remain
separate holders; this file makes no claim about those obligations.
"""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile

from support import EVIDENCE, Fixture, digest, fixture_hash_check, invoke

KEYS = {"schema_version", "operation", "status", "code", "files_declared",
        "files_checked", "fields_checked", "findings", "archive_bytes", "archive_sha256"}
CANARY = "synthetic apricot paper"


class FixturePrerequisites(unittest.TestCase):
    def test_real_newman_fixtures_and_three_real_readers(self):
        fixture_hash_check()
        for scene, expected in [("green", 0), ("finding", 1), ("precondition", 3), ("script-error", 1)]:
            with self.subTest(scene=scene), tempfile.TemporaryDirectory(prefix="np-given-") as tmp:
                fixture = Fixture(tmp, scene)
                outcome = fixture.reader_verdict()
                self.assertEqual(outcome["coverage_rc"], 0)
                self.assertEqual(outcome["suite_rc"], expected)
                raw = json.loads(fixture.report.read_text())["run"]
                self.assertEqual(outcome["live_counts"]["requests"], raw["stats"]["requests"]["total"])
                self.assertEqual(outcome["live_counts"]["assertions"], raw["stats"]["assertions"]["total"])
                self.assertEqual(len(outcome["live_kinds"]), len(raw["failures"]))


class PublicationProjection(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="np-contract-")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)

    def verdict(self, process, operation, status, code):
        self.assertEqual(process.returncode, {"CLEAN": 0, "FINDING": 1, "NOT_EXECUTED": 3}[status],
                         "canonical command did not return the required classified outcome")
        self.assertFalse(process.stderr, "public diagnostic must use only the closed result")
        try:
            result = json.loads(process.stdout)
        except (ValueError, UnicodeError):
            self.fail("canonical command did not emit exactly one JSON result")
        self.assertEqual(set(result), KEYS)
        self.assertEqual((result["schema_version"], result["operation"], result["status"], result["code"]),
                         (1, operation, status, code))
        for key in ["files_declared", "files_checked", "fields_checked", "findings", "archive_bytes"]:
            self.assertIs(type(result[key]), int)
            self.assertGreaterEqual(result[key], 0)
        self.assertLessEqual(result["files_checked"], result["files_declared"])
        if result["archive_sha256"] is not None:
            self.assertRegex(result["archive_sha256"], r"^[a-f0-9]{64}$")
        if status == "CLEAN":
            self.assertGreater(result["files_declared"], 0, "empty walk cannot authorize publication")
            self.assertEqual(result["files_declared"], result["files_checked"])
            self.assertGreater(result["fields_checked"], 0)
            self.assertEqual(result["findings"], 0)
        self.assertTrue(CANARY.encode() not in process.stdout + process.stderr,
                        "raw material entered public diagnostic")
        return result

    def project_clean(self, fixture):
        result = self.verdict(fixture.project(), "project", "CLEAN", "COMPLETE")
        self.assertEqual({p.name for p in fixture.output.iterdir()}, {"publication.zip", "verdict.json"})
        self.assertEqual(json.loads((fixture.output / "verdict.json").read_text()), result)
        output = fixture.output / "publication.zip"
        data = output.read_bytes()
        self.assertEqual(result["archive_bytes"], len(data))
        self.assertEqual(result["archive_sha256"], digest(data))
        checked = invoke([sys.executable, "-m", "ci.newman_publication", "check", "--archive",
                          output, "--manifest", fixture.manifest], label="final-check")
        proof = self.verdict(checked, "check", "CLEAN", "COMPLETE")
        self.assertEqual((proof["archive_bytes"], proof["archive_sha256"]), (len(data), digest(data)))
        stdin_check = invoke([sys.executable, "-m", "ci.newman_publication", "check", "--archive", "-",
                             "--manifest", fixture.manifest], input_bytes=data, label="stdin-check")
        self.assertEqual(self.verdict(stdin_check, "check", "CLEAN", "COMPLETE"), proof)
        with zipfile.ZipFile(output) as bundle:
            names = bundle.namelist()
            self.assertEqual(len(names), len(set(names)))
            self.assertFalse(bundle.comment)
            self.assertIn("manifest.json", names)
            self.assertIn("reports/report-000001.json", names)
            for info in bundle.infolist():
                self.assertFalse(info.extra)
                self.assertFalse(info.comment)
                self.assertRegex(info.filename, r"^(manifest\.json|(reports/report|logs/log|verdicts/verdict)-[0-9]{6}\.json)$")
                content = bundle.read(info)
                self.assertTrue(CANARY.encode() not in content, "opaque material survived projection")
                self.assertTrue(str(fixture.base).encode() not in content, "private path survived projection")
            return json.loads(bundle.read("reports/report-000001.json")), names

    def test_np01_real_readers_preserve_all_four_outcomes(self):
        for scene in ["green", "finding", "precondition", "script-error"]:
            with self.subTest(scene=scene):
                fixture = Fixture(self.base / scene, scene)
                before = fixture.reader_verdict()
                original = json.loads(fixture.report.read_text())
                projected, _ = self.project_clean(fixture)
                self.assertEqual(projected["run"]["stats"], original["run"]["stats"])
                self.assertEqual(len(projected["run"]["failures"]), len(original["run"]["failures"]))
                old_exec = original["run"]["executions"]
                new_exec = projected["run"]["executions"]
                self.assertEqual(len(new_exec), len(old_exec))
                for old, new in zip(old_exec, new_exec):
                    self.assertEqual(new["cursor"]["position"], old["cursor"]["position"])
                    self.assertEqual(new["cursor"]["iteration"], old["cursor"]["iteration"])
                    self.assertEqual((new.get("response") or {}).get("code"), (old.get("response") or {}).get("code"))
                after = fixture.reader_verdict(json.dumps(projected).encode(), "projected")
                self.assertEqual(before, after, "actual downstream reader semantics changed")

    def test_np02_opaque_carriers_not_just_recognizable_credentials(self):
        mutations = {
            "body-buffer": lambda d: d["run"]["executions"][0]["response"].update(
                stream={"type": "Buffer", "data": list(CANARY.encode())}),
            "metadata-key": lambda d: d["collection"]["info"].update({CANARY: "ordinary value"}),
            "metadata-name": lambda d: d["collection"]["info"].update(name=CANARY),
            "arbitrary-key": lambda d: d.update({CANARY: "ordinary value"}),
            "url": lambda d: d["run"]["executions"][0]["request"].update(url=CANARY),
            "request-body": lambda d: d["run"]["executions"][0]["request"].update(body={"mode": "raw", "raw": CANARY}),
            "opaque-header": lambda d: d["run"]["executions"][0]["request"]["header"].append({"key": "X-Ordinary", "value": CANARY}),
            "environment": lambda d: d["environment"]["values"].append({"key": "ordinary", "value": CANARY}),
        }
        for name, mutation in mutations.items():
            with self.subTest(carrier=name):
                fixture = Fixture(self.base / name)
                mutation_doc = json.loads(fixture.report.read_text())
                trusted_fingerprint = copy.deepcopy(mutation_doc["collection"]["item"])
                mutation(mutation_doc)
                self.assertEqual(mutation_doc["collection"]["item"], trusted_fingerprint,
                                 "carrier injection accidentally changed catalogue relation")
                fixture.report.write_text(json.dumps(mutation_doc))
                self.assertEqual(fixture.reader_verdict()["suite_rc"], 0)
                self.project_clean(fixture)

    def test_np02_raw_log_and_private_filename_become_generated_summary(self):
        fixture = Fixture(self.base)
        renamed = fixture.raw / (CANARY + ".json")
        fixture.report.rename(renamed)
        fixture.report = renamed
        fixture.data["reports"][0]["report"] = renamed.name
        (fixture.raw / "run.log").write_text(CANARY + "\nordinary second line\n")
        fixture.data["logs"] = [{"index": 0, "path": "run.log"}]
        fixture.save_manifest()
        _, names = self.project_clean(fixture)
        self.assertEqual(set(names), {"manifest.json", "reports/report-000001.json", "logs/log-000001.json"})

    def test_np02_free_error_text_is_removed_without_changing_finding(self):
        fixture = Fixture(self.base, "finding")
        fixture.change_report(lambda d: d["run"]["failures"][0]["error"].update(message=CANARY, test=CANARY))
        before = fixture.reader_verdict()
        self.assertEqual(before["suite_rc"], 1)
        projected, _ = self.project_clean(fixture)
        self.assertEqual(before, fixture.reader_verdict(json.dumps(projected).encode(), "projected"))

    def test_np06_same_size_stale_fingerprint_refused_before_relabeling(self):
        fixture = Fixture(self.base)
        fixture.change_report(lambda d: d["collection"]["item"][0]["item"][0].update(name="different tracked relation"))
        before = fixture.reader_verdict()
        self.assertEqual(before["coverage_rc"], 3, "stale-source negative must be real to current reader")
        self.verdict(fixture.project(), "project", "NOT_EXECUTED", "SOURCE_MISMATCH")
        self.assertFalse((fixture.output / "publication.zip").exists())

    def test_np03_missing_and_malformed_report_never_publish(self):
        for mode, code in [("missing", "MISSING_INPUT"), ("malformed", "MALFORMED_INPUT")]:
            with self.subTest(mode=mode):
                fixture = Fixture(self.base / mode)
                if mode == "missing":
                    fixture.report.unlink()
                else:
                    fixture.report.write_text("{" + CANARY)
                result = self.verdict(fixture.project(), "project", "NOT_EXECUTED", code)
                self.assertGreaterEqual(result["files_declared"], 1)
                self.assertFalse((fixture.output / "publication.zip").exists())

    def test_np03_dirty_catalogue_and_wrong_commit_refused(self):
        for mode in ["dirty", "foreign-commit"]:
            with self.subTest(mode=mode):
                fixture = Fixture(self.base / mode)
                if mode == "dirty":
                    fixture.collection.write_bytes(fixture.collection.read_bytes() + b"\n")
                else:
                    fixture.data["source_commit"] = "0" * 40
                    fixture.save_manifest()
                self.verdict(fixture.project(), "project", "NOT_EXECUTED", "SOURCE_MISMATCH")
                self.assertFalse((fixture.output / "publication.zip").exists())

    def test_np03_symlink_input_refused(self):
        fixture = Fixture(self.base)
        external = self.base / "external.json"
        fixture.report.rename(external)
        fixture.report.symlink_to(external)
        self.verdict(fixture.project(), "project", "NOT_EXECUTED", "UNSAFE_PATH")
        self.assertFalse((fixture.output / "publication.zip").exists())

    def test_np03_existing_output_never_reused(self):
        fixture = Fixture(self.base)
        self.project_clean(fixture)
        old = (fixture.output / "publication.zip").read_bytes()
        self.verdict(fixture.project(), "project", "NOT_EXECUTED", "OUTPUT_EXISTS")
        self.assertEqual((fixture.output / "publication.zip").read_bytes(), old,
                         "refusal must not destroy an unrelated pre-existing output")


if __name__ == "__main__":
    print("evidence=" + str(EVIDENCE), flush=True)
    unittest.main(verbosity=2)
