"""Independent nonempty kacho-shard-v1 holder for CI-NP-01/02/03.

verifies: https://github.com/PRO-Robotech/kacho/issues/1810

Routine binding v2: 19a9d04459011d47801d760db8d2d6fdecb84a01a5aca393973e3dc1fd3f0ad0.
Only fixtures, real producer/readers and the public project/check CLI are used.
An authored lawful final ZIP is a fixture, never evidence of a successful SUT
project. Negatives count as established only when their lawful pair passes.
"""
from __future__ import annotations

import base64
import copy
import io
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
import zipfile

from shard_support import CATALOGUE_PATH, ShardFixture, producer_hashes
from support import EVIDENCE, ENV, digest, invoke
from test_carrier_scanner import CODES, result_errors


CANARY = "synthetic violet private note"
COUNTERS = ("requests", "unanswered", "assertions", "failed", "script_failed")
NUMBERS = ("expected", "reported", *COUNTERS)


def encoded(value):
    return (json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")) + "\n").encode()


def changes(left, right, path="$"):
    if type(left) is not type(right):
        return [path]
    if isinstance(left, dict):
        return [p for key in sorted(set(left) | set(right)) for p in
                ([path + "." + key] if key not in left or key not in right else
                 changes(left[key], right[key], path + "." + key))]
    if isinstance(left, list):
        if len(left) != len(right):
            return [path]
        return [p for i, (a, b) in enumerate(zip(left, right))
                for p in changes(a, b, path + f"[{i}]")]
    return [] if left == right else [path]


def repack(members):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w") as archive:
        for name, content in members.items():
            info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100600 << 16
            archive.writestr(info, content)
    data = stream.getvalue()
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        assert {i.filename: archive.read(i) for i in archive.infolist()} == members
        assert all(not i.flag_bits & 8 and not i.extra and not i.comment for i in archive.infolist())
    return data


def expected_verdict(raw, index, relation):
    """Fixture construction from observed producer facts, not a reader oracle."""
    assert set(raw["per_collection"]) == set(relation)
    assert raw["missing"] == []
    doc = {"schema_version": 1, "kind": "kacho-shard-v1", "index": index,
           "shard": raw["shard"], "suites": list(raw["suites"]),
           **{k: raw[k] for k in NUMBERS}, "missing": [],
           "empty": [relation[key] for key in raw["empty"]],
           "per_collection": {relation[key]: {k: counts[k] for k in COUNTERS}
                              for key, counts in raw["per_collection"].items()}}
    if "precondition" in raw:
        doc["precondition"] = {k: raw["precondition"][k] for k in ("unmet", "kind")}
    return doc


class VerdictHolder(unittest.TestCase):
    def test_nonempty_verdict_contract(self):
        self.rows, self.errors, self.prerequisites = [], [], []
        # Synthetic inputs only; no runner/API credentials enter fixture children.
        for key in list(ENV):
            if any(word in key.upper() for word in ("TOKEN", "PASSWORD", "SECRET", "CREDENTIAL")):
                ENV.pop(key)
        ENV.update(GOWORK="off", PYTHONDONTWRITEBYTECODE="1", GIT_NO_REPLACE_OBJECTS="1")
        self.provenance = producer_hashes()
        with tempfile.TemporaryDirectory(prefix="np-verdict-holder-") as temporary:
            self.base = Path(temporary)
            cases = [("green", None), ("finding", None), ("precondition", None),
                     ("script-error", None), ("green", "file"), ("green", "env"),
                     ("empty-assertions", None), ("multi", None)]
            self.fixtures = {}
            # Complete all independent prerequisites before judging the gap.
            for scene, mark in cases:
                key = scene + ("-" + mark if mark else "")
                self.fixtures[key] = self.prepare(key, scene, mark)
            for key, fixture in self.fixtures.items():
                self.lawful(key, fixture)
            self.project_mutants()
            self.source_mutants()
            self.final_mutants()
            self.limits()
        summary = {
            "holder": "nonempty-kacho-shard-v1", "provenance": self.provenance,
            "routine_sha256": "19a9d04459011d47801d760db8d2d6fdecb84a01a5aca393973e3dc1fd3f0ad0",
            "prerequisites": self.prerequisites, "cases": self.rows,
            "attempts": len(self.rows), "failures": len(self.errors),
            "established_negative_pairs": sum(r.get("established_negative", False) for r in self.rows),
            "unestablished_negative_pairs": sum(r.get("unestablished_negative", False) for r in self.rows),
            "errors": self.errors,
        }
        (EVIDENCE / "verdict-holder.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({"evidence": str(EVIDENCE), "prerequisites": len(self.prerequisites),
                          "attempts": len(self.rows), "failures": len(self.errors),
                          "established_negative_pairs": summary["established_negative_pairs"],
                          "unestablished_negative_pairs": summary["unestablished_negative_pairs"]}), flush=True)
        self.assertFalse(self.errors, f"{len(self.errors)} scoped contract failures; see verdict-holder.json")

    def prepare(self, key, scene="green", mark=None):
        s = ShardFixture(self.base / key, "green" if scene in ("multi", "empty-assertions") else scene, mark)
        if scene == "empty-assertions":
            original = json.loads(s.fixture.report.read_bytes())
            zero = copy.deepcopy(original)
            zero["run"]["stats"]["assertions"].update(total=0, pending=0, failed=0)
            for execution in zero["run"]["executions"]:
                execution["assertions"] = []
            zero["run"]["failures"] = []
            s.fixture.report.write_bytes(encoded(zero))
            shutil.copyfile(s.fixture.report, s.shard_report)
            # This one-axis fixture removes assertions, not a claimed new Newman execution.
            (EVIDENCE / "empty-assertions-derived.json").write_text(json.dumps({
                "axis": "remove observed assertions", "original_sha256": digest(encoded(original)),
                "derived_sha256": digest(encoded(zero)), "changed": changes(original, zero)}, indent=2))
        if scene == "multi":
            s.add_suite()
        p = s.produce()
        self.assertEqual(p.returncode, 75 if mark else 0)
        raws, paths = [copy.deepcopy(s.raw_verdict)], [s.verdict_path]
        if scene == "multi":
            other = s.raw / "private-other-verdict.json"
            p = s.produce("other-shard", other)
            self.assertEqual(p.returncode, 0)
            raws.append(copy.deepcopy(s.raw_verdict))
            paths.append(other)
        s.raws, s.paths = raws, paths
        s.before, aggregate_capture = s.aggregate(raws, "raw")
        before = s.fixture.reader_verdict(suffix="before")
        baseline = s.project("no-verdict-control")
        errors, _ = result_errors(baseline, "project", "CLEAN", "COMPLETE")
        self.assertFalse(errors, "broken no-verdict project prerequisite")
        data = (s.fixture.output / "publication.zip").read_bytes()
        checked = s.checker(s.fixture.output / "publication.zip", "no-verdict-control")
        errors, _ = result_errors(checked, "check", "CLEAN", "COMPLETE", data)
        self.assertFalse(errors, "broken no-verdict checker prerequisite")
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            members = {i.filename: archive.read(i) for i in archive.infolist()}
        after = s.fixture.reader_verdict(members["reports/report-000001.json"], "control-after")
        self.assertEqual(before, after)
        for entry in s.fixture.data["reports"][1:]:
            index = entry["index"]
            raw_observed = s.fixture.reader_verdict((s.raw / entry["report"]).read_bytes(), f"before-{index}")
            projected_observed = s.fixture.reader_verdict(members[f"reports/report-{index + 1:06d}.json"],
                                                         f"control-after-{index}")
            self.assertEqual(raw_observed, projected_observed)
            self.assertEqual(raw_observed, before)
        s.report_observation = before
        if scene == "precondition":
            self.assertEqual(before["suite_rc"], 3)
            self.assertEqual(s.before["category"], "КРАСНЫЙ")
        if scene == "empty-assertions":
            self.assertGreater(s.before["totals"]["empty"], 0)
            self.assertNotEqual(s.before["rc"], 0)
        relations = [{"probe/fixture": "reports/report-000001.json"}]
        if scene == "multi":
            relations.append({"other/fixture": "reports/report-000002.json"})
        s.expected = [expected_verdict(raw, i, relation)
                      for i, (raw, relation) in enumerate(zip(raws, relations))]
        projected_observation, fixture_reader_capture = s.aggregate(s.expected, "authored-fixture")
        self.assertEqual(s.before, projected_observation)
        public = json.loads(members["manifest.json"])
        for i, doc in enumerate(s.expected):
            member = f"verdicts/verdict-{i + 1:06d}.json"
            content = encoded(doc)
            members[member] = content
            public["verdicts"].append({"index": i, "member": member,
                                       "bytes": len(content), "sha256": digest(content)})
        members["manifest.json"] = encoded(public)
        s.members = members
        s.authored_bytes = repack(members)
        s.authored = s.base / "authored-lawful.zip"
        s.authored.write_bytes(s.authored_bytes)
        s.fixture.data["verdicts"] = [{"index": i, "kind": "kacho-shard-v1", "path": path.name}
                                     for i, path in enumerate(paths)]
        s.fixture.save_manifest()
        s.manifest_original = copy.deepcopy(s.fixture.data)
        record = {"id": key, "source_commit": s.fixture.data["source_commit"],
                  "catalogue_sha256": digest((s.source / CATALOGUE_PATH).read_bytes()),
                  "producer_rc": 75 if mark else 0, "aggregate": s.before,
                  "reader_before": before, "reader_after_control": after,
                  "aggregate_capture": aggregate_capture, "authored_reader_capture": fixture_reader_capture,
                  "baseline_project_capture": str(baseline.capture_dir),
                  "baseline_check_capture": str(checked.capture_dir),
                  "authored_fixture_sha256": digest(s.authored_bytes),
                  "raw_verdicts": raws, "projected_fixture_verdicts": s.expected}
        self.prerequisites.append(record)
        return s

    def observe(self, case, process, operation, *, archive=None, refusal=False, code=None, pair=None, delta=None):
        parsed = None
        try:
            parsed = json.loads(process.stdout)
        except (ValueError, UnicodeError):
            pass
        status = "NOT_EXECUTED" if refusal else "CLEAN"
        expected_code = code or (parsed.get("code") if refusal and isinstance(parsed, dict) else "COMPLETE")
        errors, result = result_errors(process, operation, status, expected_code, archive)
        if refusal and (expected_code not in CODES or expected_code in ("COMPLETE", "INTERNAL_ERROR", "INTERRUPTED")):
            errors.append("not_a_supported_refusal")
        if CANARY.encode() in process.stdout + process.stderr:
            errors.append("private_canary_in_public_diagnostic")
        established = refusal and pair is True and not errors
        row = {"id": case, "operation": operation, "expected_status": status,
               "expected_code": code, "actual": result, "capture": str(process.capture_dir),
               "errors": errors, "lawful_pair_passed": pair, "single_fact_delta": delta,
               "established_negative": established,
               "unestablished_negative": refusal and pair is not True}
        self.rows.append(row)
        if errors:
            self.errors.append({"id": case, "errors": errors})
        return not errors

    def lawful(self, key, s):
        projected = s.project("nonempty")
        s.project_ok = self.observe(key + "/project", projected, "project")
        checked = s.checker(s.authored, "authored-lawful")
        s.check_ok = self.observe(key + "/authored-check", checked,
                                 "check", archive=s.authored_bytes)
        count = len(s.fixture.data["reports"]) + len(s.fixture.data["verdicts"])
        if s.check_ok:
            result = json.loads(checked.stdout)
            self.assertEqual((result["files_declared"], result["files_checked"]), (count, count))
        # Authored fixtures and actual SUT production remain separate evidence.
        if not s.project_ok:
            self.assertFalse((s.fixture.output / "publication.zip").exists())
            return
        self.assertEqual({p.name for p in s.fixture.output.iterdir()}, {"publication.zip", "verdict.json"})
        actual_path = s.fixture.output / "publication.zip"
        data = actual_path.read_bytes()
        proof = json.loads((s.fixture.output / "verdict.json").read_bytes())
        self.assertEqual(proof, json.loads(projected.stdout))
        self.assertEqual((proof["files_declared"], proof["files_checked"]), (count, count))
        self.assertEqual((proof["archive_bytes"], proof["archive_sha256"]), (len(data), digest(data)))
        self.observe(key + "/actual-check", s.checker(actual_path, "actual-lawful"), "check", archive=data)
        stdin = invoke([sys.executable, "-m", "ci.newman_publication", "check", "--archive", "-",
                        "--manifest", s.fixture.manifest], input_bytes=data, label="verdict-stdin-check")
        self.observe(key + "/stdin-check", stdin, "check", archive=data)
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            members = {i.filename: archive.read(i) for i in archive.infolist()}
        self.assertEqual(set(members), set(s.members))
        actual = [json.loads(members[f"verdicts/verdict-{i + 1:06d}.json"])
                  for i in range(len(s.expected))]
        self.assertEqual(actual, s.expected)
        public = json.loads(members["manifest.json"])
        self.assertEqual(public["verdicts"], [
            {"index": i, "member": f"verdicts/verdict-{i + 1:06d}.json",
             "bytes": len(members[f"verdicts/verdict-{i + 1:06d}.json"]),
             "sha256": digest(members[f"verdicts/verdict-{i + 1:06d}.json"])}
            for i in range(len(s.expected))])
        after, _ = s.aggregate(actual, "actual-projected")
        self.assertEqual(after, s.before)
        for i in range(len(s.fixture.data["reports"])):
            member = f"reports/report-{i + 1:06d}.json"
            self.assertEqual(json.loads(members[member]), json.loads(s.members[member]))
            self.assertEqual(s.fixture.reader_verdict(members[member], f"actual-after-{i}"),
                             s.report_observation)
        for content in members.values():
            self.assertNotIn(CANARY.encode(), content)
            self.assertNotIn(str(s.base).encode(), content)

    def project_mutants(self):
        s = self.fixtures["finding"]
        original = copy.deepcopy(s.raws[0])
        mutations = {
            "unknown-shard": lambda d: d.update(shard=CANARY),
            "wrong-suites": lambda d: d.update(suites=[CANARY]),
            "counter-bool": lambda d: d.update(expected=True),
            "counter-negative": lambda d: d.update(requests=-1),
            "counter-string": lambda d: d.update(assertions="1"),
            "expected-census": lambda d: d.update(expected=d["expected"] + 1),
            "reported-census": lambda d: d.update(reported=d["reported"] + 1),
            "requests-drift": lambda d: d.update(requests=d["requests"] + 1),
            "unanswered-drift": lambda d: d.update(unanswered=d["unanswered"] + 1),
            "failed-hidden": lambda d: d.update(failed=0),
            "script-counter-drift": lambda d: d.update(script_failed=d["script_failed"] + 1),
            "missing-required-key": lambda d: d.pop("per_collection"),
            "orphan-per-collection": lambda d: d["per_collection"].update({CANARY: d["per_collection"]["probe/fixture"]}),
            "missing-per-collection": lambda d: d.update(per_collection={}),
            "per-collection-count-drift": lambda d: d["per_collection"]["probe/fixture"].update(requests=999),
            "invented-missing": lambda d: d.update(missing=["probe/fixture"]),
            "invented-empty": lambda d: d.update(empty=["probe/fixture"]),
            "unknown-precondition": lambda d: d.update(precondition={"unmet": True, "kind": CANARY}),
            "precondition-bool-type": lambda d: d.update(precondition={"unmet": 1, "kind": "external-chart-source"}),
        }
        for name, change in mutations.items():
            mutated = copy.deepcopy(original)
            change(mutated)
            delta = changes(original, mutated)
            self.assertEqual(len(delta), 1)
            s.set_verdict(mutated)
            self.project_refusal(s, "raw/" + name, delta=delta)
        s.set_verdict(original)
        for name, content, code in (("empty", b"", None), ("malformed", b"{", None),
                                    ("unknown-shape", b"[]", None),
                                    ("duplicate-json-key", encoded(original).replace(b'"expected":1', b'"expected":1,"expected":1'), None)):
            self.assertNotEqual(content, encoded(original))
            s.verdict_path.write_bytes(content)
            self.project_refusal(s, "raw/" + name, code=code, delta=["raw document encoding"])
        s.set_verdict(original)
        for name, change in {
            "unknown-kind": lambda d: d["verdicts"][0].update(kind="unknown"),
            "index-gap": lambda d: d["verdicts"][0].update(index=1),
            "index-bool": lambda d: d["verdicts"][0].update(index=False),
            "path-traversal": lambda d: d["verdicts"][0].update(path="../private-shard-verdict.json"),
            "extra-manifest-key": lambda d: d["verdicts"][0].update(note=CANARY),
            "empty-reports": lambda d: d.update(reports=[]),
        }.items():
            data = copy.deepcopy(s.manifest_original)
            change(data)
            delta = changes(s.manifest_original, data)
            self.assertEqual(len(delta), 1)
            s.fixture.data = data
            s.fixture.save_manifest()
            self.project_refusal(s, "manifest/" + name, delta=delta)
        s.fixture.data = copy.deepcopy(s.manifest_original)
        s.fixture.save_manifest()
        for name, path in (("missing-verdict", s.verdict_path), ("missing-report", s.fixture.report)):
            content = path.read_bytes()
            path.unlink()
            self.project_refusal(s, "raw/" + name, code="MISSING_INPUT", delta=["declared file presence"])
            path.write_bytes(content)
        content = s.verdict_path.read_bytes()
        s.verdict_path.unlink()
        target = s.raw / "private-verdict-target.json"
        target.write_bytes(content)
        s.verdict_path.symlink_to(target)
        self.project_refusal(s, "raw/symlink-verdict", code="UNSAFE_PATH", delta=["verdict filesystem type"])
        s.verdict_path.unlink()
        s.verdict_path.write_bytes(content)
        for name, change in {
            "opaque-key": lambda d: d.update({CANARY: "ordinary"}),
            "opaque-value": lambda d: d.update(note=CANARY),
            "secret-carrier": lambda d: d.update(note={"access_token": CANARY}),
        }.items():
            doc = copy.deepcopy(original)
            change(doc)
            s.set_verdict(doc)
            self.privacy_project(s, name)
        s.set_verdict(original)
        marked = self.fixtures["green-file"]
        doc = copy.deepcopy(marked.raws[0])
        doc["precondition"].update(detail=CANARY, producer=CANARY)
        marked.set_verdict(doc)
        self.privacy_project(marked, "precondition-free-fields")
        marked.set_verdict(marked.raws[0])

        multi = self.fixtures["multi"]
        data = copy.deepcopy(multi.manifest_original)
        data["verdicts"].pop()
        multi.fixture.data = data
        multi.fixture.save_manifest()
        self.project_refusal(multi, "census/proper-subset", delta=["$.verdicts"])
        multi.fixture.data = copy.deepcopy(multi.manifest_original)
        multi.fixture.save_manifest()
        wrong = copy.deepcopy(multi.raws[0])
        wrong["shard"] = "other-shard"
        multi.set_verdict(wrong)
        self.project_refusal(multi, "census/other-valid-shard", delta=["$.shard"])
        # Complete selector/suite/per-key substitution still cannot authorize a
        # duplicate shard covering one declared report twice and orphaning another.
        multi.set_verdict(multi.raws[1])
        self.project_refusal(multi, "census/duplicate-shard-and-orphan", delta=["first verdict identity"])
        multi.set_verdict(multi.raws[0])

    def project_refusal(self, s, name, code=None, delta=None):
        process = s.project("refusal-" + name.replace("/", "-"))
        self.observe(name, process, "project", refusal=True, code=code, pair=s.project_ok, delta=delta)
        self.assertFalse((s.fixture.output / "publication.zip").exists(), "refusal left publication ZIP")

    def privacy_project(self, s, name):
        process = s.project("privacy-" + name)
        if self.observe("privacy/" + name, process, "project"):
            data = (s.fixture.output / "publication.zip").read_bytes()
            with zipfile.ZipFile(io.BytesIO(data)) as archive:
                for info in archive.infolist():
                    self.assertNotIn(CANARY.encode(), archive.read(info))
                projected = json.loads(archive.read("verdicts/verdict-000001.json"))
            self.assertEqual(projected, s.expected[0])
            self.observe("privacy/" + name + "/check", s.checker(s.fixture.output / "publication.zip", name),
                         "check", archive=data)

    def source_mutants(self):
        s = self.fixtures["green"]
        path = s.source / CATALOGUE_PATH
        original = path.read_bytes()
        path.write_bytes(original + b" \n")
        self.project_refusal(s, "source/checkout-drift", code="SOURCE_MISMATCH", delta=["catalogue checkout bytes"])
        self.observe("source/check-checkout-drift", s.checker(s.authored, "dirty-catalogue"), "check",
                     archive=s.authored_bytes, refusal=True, code="SOURCE_MISMATCH", pair=s.check_ok,
                     delta=["catalogue checkout bytes"])
        path.write_bytes(original)
        path.unlink()
        self.project_refusal(s, "source/missing-catalogue", delta=["catalogue checkout presence"])
        path.write_bytes(original)
        path.unlink()
        path.symlink_to(s.base / "catalogue-target.json")
        (s.base / "catalogue-target.json").write_bytes(original)
        self.project_refusal(s, "source/symlink-catalogue", delta=["catalogue filesystem type"])
        path.unlink()
        path.write_bytes(original)
        data = copy.deepcopy(s.manifest_original)
        data["source_commit"] = "0" * 40
        s.fixture.data = data
        s.fixture.save_manifest()
        self.project_refusal(s, "source/commit-mismatch", code="SOURCE_MISMATCH", delta=["$.source_commit"])
        s.fixture.data = copy.deepcopy(s.manifest_original)
        s.fixture.save_manifest()
        for name, change in {
            "duplicate-id": lambda c: c["shards"].append(copy.deepcopy(c["shards"][0])),
            "unknown-shard": lambda c: c["shards"][0].update(id="different-shard"),
            "repeated-suite": lambda c: c["shards"][0]["suites"].append("probe"),
            "missing-catalogue-entry": lambda c: c.update(shards=[]),
        }.items():
            # Recommit each candidate: a parser/source relation defect cannot be
            # masked by an unrelated dirty-checkout rejection.
            original_catalogue = copy.deepcopy(s.catalogue)
            change(s.catalogue)
            s.commit_catalogue()
            self.project_refusal(s, "source/" + name, delta=changes(original_catalogue, s.catalogue))
            self.observe("source/check-" + name, s.checker(s.authored, name), "check",
                         archive=s.authored_bytes, refusal=True, pair=s.check_ok,
                         delta=changes(original_catalogue, s.catalogue))
            s.catalogue = original_catalogue
            s.commit_catalogue()
        # Restore a consistent current source binding for later final checks.
        s.manifest_original = copy.deepcopy(s.fixture.data)

    def final_mutants(self):
        s = self.fixtures["finding"]
        variants = {
            "opaque-key": lambda d: d.update({CANARY: "ordinary"}),
            "unknown-schema": lambda d: d.update(schema_version=2),
            "index-drift": lambda d: d.update(index=1),
            "counter-bool": lambda d: d.update(expected=True),
            "expected-drift": lambda d: d.update(expected=d["expected"] + 1),
            "reported-drift": lambda d: d.update(reported=d["reported"] + 1),
            "failed-hidden": lambda d: d.update(failed=0),
            "requests-drift": lambda d: d.update(requests=d["requests"] + 1),
            "unanswered-drift": lambda d: d.update(unanswered=d["unanswered"] + 1),
            "script-counter-drift": lambda d: d.update(script_failed=d["script_failed"] + 1),
            "unknown-shard": lambda d: d.update(shard=CANARY),
            "wrong-suites": lambda d: d.update(suites=[CANARY]),
            "orphan-report-index": lambda d: d["per_collection"].update({"reports/report-000002.json": d["per_collection"]["reports/report-000001.json"]}),
            "missing-per-collection": lambda d: d.update(per_collection={}),
            "invented-missing": lambda d: d.update(missing=["reports/report-000001.json"]),
            "invented-empty": lambda d: d.update(empty=["reports/report-000001.json"]),
            "per-collection-drift": lambda d: d["per_collection"]["reports/report-000001.json"].update(failed=0),
            "unknown-precondition": lambda d: d.update(precondition={"unmet": True, "kind": CANARY}),
            "free-precondition": lambda d: d.update(precondition={"unmet": True, "kind": "external-chart-source", "detail": CANARY}),
        }
        for name, change in variants.items():
            doc = copy.deepcopy(s.expected[0])
            change(doc)
            delta = changes(s.expected[0], doc)
            self.assertEqual(len(delta), 1)
            self.check_mutant(s, name, doc, delta)
        multi = self.fixtures["multi"]
        doc = copy.deepcopy(multi.expected[0])
        doc["shard"] = "other-shard"
        self.check_mutant(multi, "other-valid-shard", doc, ["$.shard"])
        doc = copy.deepcopy(multi.expected[1])
        doc["index"] = 0
        self.check_mutant(multi, "duplicate-shard-and-orphan", doc, ["first verdict identity"])
        # Permitted precondition mark does not purport to authenticate raw mark history.
        s = self.fixtures["green"]
        self.assertEqual(s.raws[0].get("precondition"), None)
        doc = copy.deepcopy(s.expected[0])
        doc["precondition"] = {"unmet": False, "kind": "external-chart-source"}
        members = self.rebound(s, doc)
        data = repack(members)
        path = s.base / "closed-precondition-form.zip"
        path.write_bytes(data)
        self.observe("final/closed-precondition-form", s.checker(path, "closed-precondition-form"), "check", archive=data)
        # Final bytes are sufficient; raw files are not a second authority.
        originals = {path: path.read_bytes() for path in [*s.paths, s.fixture.report]}
        for path in originals:
            path.unlink()
        self.observe("final/no-raw-reread", s.checker(s.authored, "no-raw-reread"), "check", archive=s.authored_bytes)
        for path, content in originals.items():
            path.write_bytes(content)

    def rebound(self, s, doc):
        members = dict(s.members)
        member = "verdicts/verdict-000001.json"
        members[member] = encoded(doc)
        public = json.loads(members["manifest.json"])
        public["verdicts"][0].update(bytes=len(members[member]), sha256=digest(members[member]))
        members["manifest.json"] = encoded(public)
        return members

    def check_mutant(self, s, name, doc, delta):
        data = repack(self.rebound(s, doc))
        path = s.base / ("final-" + name + ".zip")
        path.write_bytes(data)
        process = s.checker(path, name)
        (process.capture_dir / "lawful.zip").write_bytes(s.authored_bytes)
        (process.capture_dir / "mutated.zip").write_bytes(data)
        (process.capture_dir / "mutation.json").write_text(json.dumps({
            "single_subject_fact": delta, "auxiliary": "rebind changed member size and digest",
            "lawful_sha256": digest(s.authored_bytes), "mutated_sha256": digest(data)}, indent=2))
        self.observe("final/" + name, process, "check", archive=data, refusal=True,
                     pair=s.check_ok, delta=delta)

    def limits(self):
        s = self.fixtures["green"]
        # Exact input bytes from authored lawful ZIP; equality and one below use
        # the same complete artifact and manifest, including nonempty verdict.
        bounds = {"archive_bytes": len(s.authored_bytes),
                  "expanded_bytes": sum(map(len, s.members.values())), "zip_entries": len(s.members)}
        for key, bound in bounds.items():
            good = s.checker(s.authored, key + "-equal", {key: bound})
            passed = self.observe("limit/" + key + "/equal", good, "check", archive=s.authored_bytes)
            bad = s.checker(s.authored, key + "-below", {key: bound - 1})
            self.observe("limit/" + key + "/below", bad, "check", archive=s.authored_bytes,
                         refusal=True, code="LIMIT_EXCEEDED", pair=passed, delta=["limit " + key])
        # Raw verdict is deliberately the largest input document. Its discarded
        # opaque padding must still consume input/document/expanded budgets.
        original = copy.deepcopy(s.raws[0])
        padded = copy.deepcopy(original)
        padded["note"] = "ordinary padding " * 2000
        s.set_verdict(padded)
        raw_size = s.verdict_path.stat().st_size
        self.assertGreater(raw_size, max(s.fixture.report.stat().st_size, s.fixture.manifest.stat().st_size))
        for key, bound in (("document_bytes", raw_size),
                           ("expanded_bytes", raw_size + s.fixture.report.stat().st_size)):
            good = s.project("input-" + key + "-equal", {key: bound})
            passed = self.observe("limit/input-" + key + "/equal", good, "project")
            bad = s.project("input-" + key + "-below", {key: bound - 1})
            self.observe("limit/input-" + key + "/below", bad, "project", refusal=True,
                         code="LIMIT_EXCEEDED", pair=passed, delta=["limit " + key])
        for depth in (8, 9):
            doc = copy.deepcopy(original)
            value = "ordinary final text"
            for _ in range(depth):
                value = {"encoding": "base64", "data": base64.b64encode(encoded(value)).decode()}
            doc["note"] = value
            s.set_verdict(doc)
            process = s.project("depth-" + str(depth))
            if depth == 8:
                depth_pair = self.observe("limit/decode-depth/equal", process, "project")
            else:
                self.observe("limit/decode-depth/above", process, "project", refusal=True,
                             code="LIMIT_EXCEEDED", pair=depth_pair, delta=["carrier unwrap depth"])
        for count in (1000, 100001):
            doc = copy.deepcopy(original)
            doc["note"] = [None] * count
            s.set_verdict(doc)
            process = s.project("nodes-" + str(count))
            if count == 1000:
                node_pair = self.observe("limit/decoded-nodes/lawful", process, "project")
            else:
                self.observe("limit/decoded-nodes/overflow", process, "project", refusal=True,
                             code="LIMIT_EXCEEDED", pair=node_pair, delta=["opaque carrier node count"])
        s.set_verdict(original)


if __name__ == "__main__":
    print("evidence=" + str(EVIDENCE), flush=True)
    unittest.main(verbosity=2)
