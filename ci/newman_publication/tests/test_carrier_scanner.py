"""Independent CI-NP-02/03 CLI holders. No product imports or source inspection.

verifies: https://github.com/PRO-Robotech/kacho/issues/1810

Every declared carrier twin executes project/check and the real reader trio
before the first scan. An absent scan dispatch is a scoped capability RED;
its attempts are counted separately from semantic scanner decisions.
"""
from __future__ import annotations

import base64
import copy
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile

from carrier_corpus import (CANARY, CARRIERS, CEILINGS, REPRESENTATIONS, carrier_pairs,
                            encode, field_report, json_bytes, one_fact, render_carrier,
                            rendered_delta, representation, secret_field_pairs, text_value, zip_bytes)
from support import EVIDENCE, FIXTURES, Fixture, digest, fixture_hash_check, invoke

KEYS = {"schema_version", "operation", "status", "code", "files_declared", "files_checked",
        "fields_checked", "findings", "archive_bytes", "archive_sha256"}
RC = {"CLEAN": 0, "FINDING": 1, "NOT_EXECUTED": 3}
CODES = {"COMPLETE", "SECRET_MATERIAL", "EMPTY_INPUT", "MISSING_INPUT", "UNREADABLE_INPUT",
         "MALFORMED_INPUT", "UNSUPPORTED_INPUT", "UNSUPPORTED_ENCODING", "LIMIT_EXCEEDED",
         "SOURCE_MISMATCH", "UNSAFE_PATH", "OUTPUT_EXISTS", "INTERRUPTED", "CHECKER_UNAVAILABLE", "INTERNAL_ERROR"}
LAWFUL_SECRET_VALUES = (None, "")


def carrier_needles():
    needles = {CANARY}
    for payload in (CANARY, "access_token=" + CANARY, "public_label=" + CANARY):
        for form in REPRESENTATIONS:
            value = representation(payload, form)
            needles.add(text_value(value))
            if isinstance(value, dict) and isinstance(value.get("data"), str):
                needles.add(value["data"])
    return needles


NEEDLES = carrier_needles()


def has_public_carrier(value):
    if isinstance(value, str):
        return any(needle in value for needle in NEEDLES)
    if isinstance(value, dict):
        return any(has_public_carrier(key) or has_public_carrier(child) for key, child in value.items())
    if isinstance(value, list):
        token = list(CANARY.encode())
        if any(value[i:i + len(token)] == token for i in range(len(value) - len(token) + 1)):
            return True
        return any(has_public_carrier(child) for child in value)
    return False


def result_errors(process, operation, status, code, archive=None):
    """Only the observable closed CLI contract is the oracle."""
    errors = []
    if process.returncode != RC[status]:
        errors.append("return_code")
    if process.stderr:
        errors.append("stderr_not_closed")
    try:
        result = json.loads(process.stdout)
    except (ValueError, UnicodeError):
        return errors + ["result_not_json"], None
    if type(result) is not dict or set(result) != KEYS:
        return errors + ["result_schema"], result
    if (type(result["schema_version"]) is not int or result["schema_version"] != 1
            or (result["operation"], result["status"], result["code"]) != (operation, status, code)):
        errors.append("classified_outcome")
    for key in ("files_declared", "files_checked", "fields_checked", "findings", "archive_bytes"):
        if type(result[key]) is not int or result[key] < 0:
            errors.append("counter_type")
    if "counter_type" not in errors:
        if result["files_checked"] > result["files_declared"]:
            errors.append("checked_exceeds_declared")
        if status == "CLEAN" and not (result["files_declared"] == result["files_checked"] > 0
                                      and result["fields_checked"] > 0 and result["findings"] == 0):
            errors.append("empty_or_incomplete_clean")
        if status == "FINDING" and result["findings"] < 1:
            errors.append("finding_without_findings")
    sha = result["archive_sha256"]
    if sha is not None and (not isinstance(sha, str) or len(sha) != 64
                            or any(c not in "0123456789abcdef" for c in sha)):
        errors.append("digest_shape")
    if archive is not None and (status in ("CLEAN", "FINDING") or sha is not None):
        if (result["archive_bytes"], sha) != (len(archive), digest(archive)):
            errors.append("full_archive_binding")
    if archive is not None and operation == "scan" and status in ("CLEAN", "FINDING"):
        try:
            with zipfile.ZipFile(io.BytesIO(archive)) as bundle:
                if result["files_declared"] != len(bundle.infolist()):
                    errors.append("declared_member_census")
        except zipfile.BadZipFile:
            errors.append("authorized_invalid_archive")
    diagnostic = process.stdout + process.stderr
    forbidden = (CANARY.encode(), base64.b64encode(CANARY.encode()),
                 base64.urlsafe_b64encode(CANARY.encode()),
                 "".join("%%%02X" % b for b in CANARY.encode()).encode(),
                 "".join("\\u%04x" % ord(c) for c in CANARY).encode(),
                 encode(list(CANARY.encode())))
    if any(value in diagnostic for value in forbidden):
        errors.append("canary_in_diagnostic")
    return sorted(set(errors)), result


class CarrierScanner(unittest.TestCase):
    def test_ci_np_02_03_carriers_and_capacity(self):
        with tempfile.TemporaryDirectory(prefix="np-carrier-", dir="/var/tmp") as tmp:
            run = CarrierRun(self, Path(tmp))
            try:
                run.prepare_all()
            except Exception as error:
                (EVIDENCE / "carrier-summary.json").write_bytes(encode({
                    "holder_outcome": "NOT_EXECUTED", "phase": "prerequisite",
                    "prerequisites_executed": len(run.prerequisites), "scan_attempts": 0,
                    "scan_semantic_decisions": 0, "cause_type": type(error).__name__}))
                raise
            run.execute_all()


class CarrierRun:
    def __init__(self, test, base):
        self.test, self.base = test, base
        self.rows, self.prerequisites, self.scans, self.configs = [], [], [], []
        self.known = {}
        self.inputs = EVIDENCE / "carrier-inputs"
        self.inputs.mkdir()

    def require(self, predicate, message):
        self.test.assertTrue(predicate, message)

    def record_scan(self, name, data, status="CLEAN", code="COMPLETE", limits=None, **kw):
        self.require(not any(row["id"] == name for row in self.rows), "duplicate case ID")
        self.rows.append({"id": name, "data": data, "status": status, "code": code,
                          "limits": limits, **kw})

    def clone(self, name, scene, report, filename="fixture.json", log=None):
        original = self.known[scene][0]
        fixture = copy.copy(original)
        fixture.base = self.base / name
        fixture.raw = fixture.base / "raw"
        fixture.raw.mkdir(parents=True)
        fixture.output = fixture.base / "published"
        fixture.report = fixture.raw / filename
        fixture.report.write_bytes(report)
        fixture.data = copy.deepcopy(original.data)
        fixture.data["input_root"] = str(fixture.raw)
        fixture.data["reports"][0]["report"] = filename
        if log is not None:
            (fixture.raw / "run.log").write_bytes(log)
            fixture.data["logs"] = [{"index": 0, "path": "run.log"}]
        fixture.manifest = fixture.base / "private-manifest.json"
        fixture.save_manifest()
        return fixture

    def clean_project(self, fixture, name, expected_readers, read_before):
        before = fixture.reader_verdict() if read_before else expected_readers
        self.require(before == expected_readers, name + ": raw carrier broke reader prerequisite")
        process = fixture.project()
        errors, result = result_errors(process, "project", "CLEAN", "COMPLETE")
        self.require(not errors, name + ": project prerequisite: " + ",".join(errors))
        self.require({p.name for p in fixture.output.iterdir()} == {"publication.zip", "verdict.json"},
                     name + ": unexpected published files")
        self.require(json.loads((fixture.output / "verdict.json").read_bytes()) == result,
                     name + ": verdict differs from stdout")
        package = (fixture.output / "publication.zip").read_bytes()
        (process.capture_dir / "publication.zip").write_bytes(package)
        (process.capture_dir / "raw-report.json").write_bytes(fixture.report.read_bytes())
        self.require((result["archive_bytes"], result["archive_sha256"]) == (len(package), digest(package)),
                     name + ": project digest mismatch")
        check = invoke([sys.executable, "-m", "ci.newman_publication", "check", "--archive",
                        fixture.output / "publication.zip", "--manifest", fixture.manifest], label="carrier-check")
        errors, _ = result_errors(check, "check", "CLEAN", "COMPLETE", package)
        self.require(not errors, name + ": check prerequisite: " + ",".join(errors))
        with zipfile.ZipFile(io.BytesIO(package)) as bundle:
            self.require(not bundle.comment, name + ": output archive metadata")
            for info in bundle.infolist():
                self.require(not info.comment and not info.extra, name + ": output member metadata")
                # Every public string, including keys, comes from the trusted
                # catalogue or a fixed enum. Input sentinels are never permitted.
                content = bundle.read(info)
                self.require(not has_public_carrier(json.loads(content)) and not has_public_carrier(info.filename),
                             name + ": public raw/encoded carrier")
            projected = bundle.read("reports/report-000001.json")
            if fixture.data["logs"]:
                raw_log = (fixture.raw / "run.log").read_bytes()
                public_log = json.loads(bundle.read("logs/log-000001.json"))
                self.require(public_log == {"schema_version": 1, "kind": "log", "index": 0,
                                            "bytes": len(raw_log), "lines": len(raw_log.splitlines())},
                             name + ": log summary lost exact byte/line census")
        raw_run, public_run = json.loads(fixture.report.read_bytes())["run"], json.loads(projected)["run"]
        self.require(raw_run["stats"] == public_run["stats"], name + ": stats changed")
        self.require(len(raw_run["failures"]) == len(public_run["failures"]), name + ": failures changed")
        self.require(len(raw_run["executions"]) == len(public_run["executions"]), name + ": execution count changed")
        for raw, public in zip(raw_run["executions"], public_run["executions"]):
            self.require((raw["cursor"]["position"], raw["cursor"]["iteration"], raw["response"]["code"])
                         == (public["cursor"]["position"], public["cursor"]["iteration"], public["response"]["code"]),
                         name + ": execution facts changed")
        if read_before:
            after = fixture.reader_verdict(projected, "public")
            self.require(before == after, name + ": actual readers changed after projection")
        self.prerequisites.append({"id": name, "project_capture": str(process.capture_dir),
                                   "check_capture": str(check.capture_dir), "reader_pair_executed": read_before,
                                   "raw_sha256": digest(fixture.report.read_bytes()),
                                   "projected_sha256": digest(package)})
        return package

    def prepare_all(self):
        # No scanner probe may precede this entire prerequisite phase.
        hashes = fixture_hash_check()
        for scene, expected in (("green", 0), ("finding", 1), ("precondition", 3), ("script-error", 1)):
            fixture = Fixture(self.base / ("baseline-" + scene), scene)
            before = fixture.reader_verdict(suffix="baseline")
            self.require(before["coverage_rc"] == 0 and before["suite_rc"] == expected,
                         scene + ": genuine fixture readers did not execute")
            self.known[scene] = fixture, before
            self.clean_project(fixture, "baseline-" + scene, before, True)
        green = json.loads((FIXTURES / "green/out/fixture.json").read_bytes())
        finding = json.loads((FIXTURES / "finding/out/fixture.json").read_bytes())
        pairs = list(carrier_pairs())
        self.require(len(pairs) == len(CARRIERS) * len(REPRESENTATIONS) > 0, "empty or incomplete carrier walk")
        pairs_manifest = []
        for name, lawful, negative in pairs:
            fact = one_fact(lawful, negative)
            scene = "finding" if lawful["carrier"] == "error" else "green"
            source = finding if scene == "finding" else green
            rendered_pair = [render_carrier(source, spec) for spec in (lawful, negative)]
            actual_delta = rendered_delta(*rendered_pair)
            packages = []
            for (side, spec), rendered in zip((("lawful", lawful), ("negative", negative)), rendered_pair):
                case_id = name + "--" + side
                # Rendering itself must not alter the catalogue relation.
                self.require(json.loads(rendered["report"])["collection"]["item"] == source["collection"]["item"],
                             case_id + ": fixture changed trusted catalogue")
                fixture = self.clone(case_id, scene, rendered["report"], rendered["name"], rendered["log"])
                packages.append(self.clean_project(fixture, case_id, self.known[scene][1], True))
                self.record_scan(case_id, rendered["archive"], "CLEAN" if side == "lawful" else "FINDING",
                                 "COMPLETE" if side == "lawful" else "SECRET_MATERIAL",
                                 group="carriers", pair=name, side=side, changed_fact=fact,
                                 carrier=spec["carrier"], representation=spec["representation"])
            if lawful["carrier"] == "log":
                # Decimal Buffer serialization may change the raw log byte count
                # despite one payload substitution. That exact count is public
                # by contract and was checked above; Newman report bytes stay equal.
                with zipfile.ZipFile(io.BytesIO(packages[0])) as a, zipfile.ZipFile(io.BytesIO(packages[1])) as b:
                    self.require(a.read("reports/report-000001.json") == b.read("reports/report-000001.json"),
                                 name + ": log altered the Newman report")
            else:
                self.require(packages[0] == packages[1], name + ": untrusted carrier changed public bytes")
            pairs_manifest.append({"id": name, "lawful": lawful, "negative": negative,
                                   "changed_fact": fact, "rendered_delta": actual_delta})
        for name, lawful, negative in secret_field_pairs():
            fact = one_fact(lawful, negative)
            reports = [field_report(green, spec) for spec in (lawful, negative)]
            actual_delta = rendered_delta(*[{"report": report, "name": "report.json", "log": None,
                                            "archive": zip_bytes([("report.json", report)])} for report in reports])
            packages = []
            for (side, spec), report in zip((("lawful", lawful), ("negative", negative)), reports):
                case_id = "field--" + name + "--" + side
                fixture = self.clone(case_id, "green", report)
                packages.append(self.clean_project(fixture, case_id, self.known["green"][1], True))
                self.record_scan(case_id, zip_bytes([("report.json", report)]),
                                 "CLEAN" if side == "lawful" else "FINDING",
                                 "COMPLETE" if side == "lawful" else "SECRET_MATERIAL",
                                 group="field-families", pair=name, side=side, changed_fact=fact)
            self.require(packages[0] == packages[1], name + ": secret field changed public bytes")
            pairs_manifest.append({"id": "field--" + name, "lawful": lawful, "negative": negative,
                                   "changed_fact": fact, "rendered_delta": actual_delta})
        self.prepare_edge_cases(green)
        (EVIDENCE / "carrier-pairs.json").write_bytes(encode(pairs_manifest))
        (EVIDENCE / "carrier-prerequisites.json").write_bytes(encode({"fixture_hashes": hashes,
                                                                    "rows": self.prerequisites}))
        print("carrier prerequisites executed=" + str(len(self.prerequisites)), flush=True)

    def boundary(self, name, data, field, equal):
        self.require(1 < equal <= CEILINGS[field], name + ": invalid capacity fixture")
        # Same bytes; only the one declared lower-only budget changes by one.
        good, bad = {field: equal}, {field: equal - 1}
        one_fact(good, bad)
        for side, limits, status, code in (("lawful", good, "CLEAN", "COMPLETE"),
                                          ("negative", bad, "NOT_EXECUTED", "LIMIT_EXCEEDED")):
            self.record_scan(name + "--" + side, data, status, code, limits,
                             group="capacity", pair=name, side=side, changed_fact=field)

    def prepare_edge_cases(self, green):
        for field in ("access_token", "password", "client_secret", "refresh_token", "session_token",
                      "client_assertion", "private_key", "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"):
            for index, value in enumerate(LAWFUL_SECRET_VALUES):
                header = field in ("Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie")
                spec = {"carrier": "header" if header else "field", "field": field, "value": value,
                        "target": "response" if field == "Set-Cookie" else "request"}
                report = field_report(green, spec)
                case_id = "empty-secret--" + field + "--" + str(index)
                fixture = self.clone(case_id, "green", report)
                self.clean_project(fixture, case_id, self.known["green"][1], True)
                self.record_scan(case_id, zip_bytes([("report.json", report)]), group="lawful-empty")
        for carrier in ("bearer-auth", "basic-auth", "json-request-body", "json-response-body", "url-userinfo", "sensitive-query"):
            rendered, packages = [], []
            for side, value in (("lawful", ""), ("negative", CANARY)):
                doc = copy.deepcopy(green)
                request = doc["run"]["executions"][0]["request"]
                if carrier == "bearer-auth":
                    request["auth"] = {"type": "bearer", "bearer": [{"key": "token", "value": value, "type": "string"}]}
                elif carrier == "basic-auth":
                    request["auth"] = {"type": "basic", "basic": [
                        {"key": "username", "value": "public-user", "type": "string"},
                        {"key": "password", "value": value, "type": "string"}]}
                elif carrier == "json-request-body":
                    request["body"] = {"mode": "raw", "raw": encode({"password": value}).decode()}
                elif carrier == "json-response-body":
                    doc["run"]["executions"][0]["response"]["stream"] = {
                        "type": "Buffer", "data": list(encode({"password": value}))}
                elif carrier == "url-userinfo":
                    request["url"] = "https://public-user:" + value + "@example.invalid/"
                elif carrier == "sensitive-query":
                    request["url"]["query"].append({"key": "access_token", "value": value})
                report = encode(doc)
                data = zip_bytes([("report.json", report)])
                rendered.append({"report": report, "name": "report.json", "log": None, "archive": data})
                case_id = "context--" + carrier + "--" + side
                fixture = self.clone(case_id, "green", report)
                packages.append(self.clean_project(fixture, case_id, self.known["green"][1], True))
                self.record_scan(case_id, data, "CLEAN" if side == "lawful" else "FINDING",
                                 "COMPLETE" if side == "lawful" else "SECRET_MATERIAL",
                                 group="contextual", pair=carrier, side=side, changed_fact="credential-value")
            rendered_delta(*rendered)
            self.require(packages[0] == packages[1], carrier + ": contextual value changed public bytes")
        for name, value in (("public-key", "-----BEGIN PUBLIC KEY-----\nYWJj\n-----END PUBLIC KEY-----"),
                            ("public-id", "nonsecret-account-17")):
            data = zip_bytes([("public.json", encode({name: value}))])
            self.record_scan(name, data, group="lawful-public")
        # Body-walk boundaries: plain ZIP names/metadata are carrier coordinates,
        # like object keys, and do not add baseline JSON nodes. Each decoded
        # representation, if present, is counted separately by the convention.
        # Simple, independently enumerable nodes: object + scalar = two.
        simple = encode({"public_label": "ordinary value"})
        two = zip_bytes([("a.json", simple), ("b.json", simple)])
        self.boundary("archive-size", two, "archive_bytes", len(two))
        self.boundary("expanded-size", two, "expanded_bytes", len(simple) * 2)
        self.boundary("document-size", two, "document_bytes", len(simple))
        self.boundary("entry-count", two, "zip_entries", 2)
        self.boundary("nodes-across-members", two, "decoded_nodes", 4)
        many_keys = encode({"one": 1, "two": 2, "three": 3})
        self.boundary("keys-not-nodes", zip_bytes([("keys.json", many_keys)]), "decoded_nodes", 4)
        # Base parsed object + encoding scalar + data scalar + decoded scalar.
        self.boundary("decoded-representation-nodes", zip_bytes([("encoded.json", encode(
            {"encoding": "base64", "data": "YWJj"}))]), "decoded_nodes", 4)
        # Buffer: object, type scalar, data list, three byte scalars, decoded scalar.
        self.boundary("buffer-representation-nodes", zip_bytes([("buffer.json", encode(
            {"type": "Buffer", "data": [97, 98, 99]}))]), "decoded_nodes", 7)
        # Exact typed-result examples independently approved in #1810 comment
        # 5714676827. The intermediate parser string is not another walk node.
        typed_examples = (
            ("json-string-object", '{"ok":true}', 3),
            ("buffer-object", {"type": "Buffer", "data": list(b'{"ok":true}')}, 16),
            ("base64-object", {"encoding": "base64", "data": "eyJvayI6dHJ1ZX0="}, 5),
            ("base64-json-string", {"encoding": "base64", "data": "IntcIm9rXCI6dHJ1ZX0i"}, 6),
        )
        for name, value, nodes in typed_examples:
            self.boundary("typed-nodes--" + name, zip_bytes([("typed.json", encode(value))]), "decoded_nodes", nodes)
        self.boundary("typed-base64-string-depth", zip_bytes([("typed.json", encode(
            {"encoding": "base64", "data": "IntcIm9rXCI6dHJ1ZX0i"}))]), "decode_depth", 2)
        nested = {"public_label": "ordinary"}
        for _ in range(3):
            nested = json.dumps(nested, separators=(",", ":"))
        self.boundary("nested-json-depth", zip_bytes([("nested.json", encode(nested))]), "decode_depth", 3)
        # Ordinary nesting must consume zero decoding depth; the actual captured
        # report has structural depth ten and Buffer bodies at unwrap depth one.
        self.record_scan("real-newman-structural-depth", zip_bytes([("report.json", encode(green))]),
                         limits={"decode_depth": 1}, group="decoding")
        for form in ("url", "base64", "base64url", "buffer"):
            value = representation("public label", form)
            self.record_scan("single-unwrap--" + form, zip_bytes([("a.json", encode(value))]),
                             limits={"decode_depth": 1}, group="decoding")
        # Eight exact JSON string unwrappings are legal; the ninth is refused.
        leaf = {"public_label": "ordinary"}
        for _ in range(8):
            leaf = json.dumps(leaf, separators=(",", ":"))
        self.record_scan("default-depth-eight", zip_bytes([("a.json", encode(leaf))]), group="decoding")
        leaf = json.dumps(leaf, separators=(",", ":"))
        self.record_scan("default-depth-nine", zip_bytes([("a.json", encode(leaf))]),
                         "NOT_EXECUTED", "LIMIT_EXCEEDED", group="decoding")
        good = {"encoding": "base64", "data": "YWJj"}
        bad = dict(good, encoding="rot13")
        one_fact(good, bad)
        for side, value, status, code in (("lawful", good, "CLEAN", "COMPLETE"),
                                         ("negative", bad, "NOT_EXECUTED", "UNSUPPORTED_ENCODING")):
            self.record_scan("explicit-encoding--" + side, zip_bytes([("a.json", encode(value))]),
                             status, code, group="incomplete", pair="explicit-encoding", side=side)
        lawful = zip_bytes([("report.json", encode(green))])
        for name, data, code in (("empty-file", b"", "EMPTY_INPUT"),
                                 ("empty-archive", zip_bytes([]), "EMPTY_INPUT"),
                                 ("malformed-json", zip_bytes([("a.json", b'{"note":')]), "MALFORMED_INPUT"),
                                 ("unknown-file", zip_bytes([("a.bin", b'ordinary')]), "UNSUPPORTED_INPUT"),
                                 ("non-zip", b'ordinary', "MALFORMED_INPUT"),
                                 ("missing-file", None, "MISSING_INPUT"),
                                 ("unreadable-file", lawful, "UNREADABLE_INPUT")):
            self.record_scan(name + "--lawful", lawful, group="incomplete", pair=name, side="lawful")
            self.record_scan(name + "--negative", data, "NOT_EXECUTED", code,
                             group="incomplete", pair=name, side="negative",
                             unreadable=name == "unreadable-file")
        # Every configured ceiling, bad integer shape, and unknown option is
        # tested through all three public commands; defaults are not imported.
        for field, ceiling in CEILINGS.items():
            self.configs.append((field + "--above-ceiling", {field: ceiling + 1}))
            self.configs.append((field + "--zero", {field: 0}))
            self.configs.append((field + "--negative", {field: -1}))
            self.configs.append((field + "--boolean", {field: True}))
            self.configs.append((field + "--fraction", {field: 1.5}))
            self.configs.append((field + "--string", {field: "1"}))
        self.configs.append(("unknown-field", {"unknown_" + CANARY: 1}))
        self.configs.append(("not-object", []))

    def execute_all(self):
        self.require(bool(self.rows) and bool(self.prerequisites), "empty holder cannot pass")
        declarations = [{k: v for k, v in row.items() if k != "data"} for row in self.rows]
        (EVIDENCE / "carrier-declared.json").write_bytes(encode(declarations))
        failures, missing_dispatch = [], 0
        for row in self.rows:
            path = self.inputs / (row["id"] + ".zip")
            if row["data"] is not None:
                path.write_bytes(row["data"])
            argv = [sys.executable, "-m", "ci.newman_publication", "scan", "--archive", path]
            if row["limits"] is not None:
                limits = self.inputs / (row["id"] + ".limits.json")
                limits.write_bytes(encode(row["limits"]))
                argv.extend(["--limits", limits])
            if row.get("unreadable"):
                self.require(os.geteuid() != 0, "unreadable fixture requires non-root caller")
                path.chmod(0)
                try:
                    path.read_bytes()
                except PermissionError:
                    pass
                else:
                    self.test.fail("unreadable fixture remained readable")
            try:
                process = invoke(argv, label="carrier-scan")
            finally:
                if row.get("unreadable"):
                    path.chmod(0o600)
            errors, result = result_errors(process, "scan", row["status"], row["code"], row["data"])
            absent = (isinstance(result, dict) and result.get("operation") != "scan")
            semantic = False
            if (isinstance(result, dict) and set(result) == KEYS and result.get("operation") == "scan"
                    and isinstance(result.get("status"), str) and result["status"] in RC
                    and isinstance(result.get("code"), str) and result["code"] in CODES):
                validation, _ = result_errors(process, "scan", result["status"], result["code"], row["data"])
                semantic = not validation
            missing_dispatch += int(absent)
            self.scans.append({**{k: v for k, v in row.items() if k != "data"},
                               "input_sha256": digest(row["data"]) if row["data"] is not None else None,
                               "capture": str(process.capture_dir), "rc": process.returncode,
                               "observed": result, "errors": errors,
                               "semantic_decision_executed": semantic,
                               "capability_boundary": "no_scan_dispatch" if absent else None})
            if errors:
                failures.append(row["id"])
        fixture = self.known["green"][0]
        config_results = []
        for name, config in [("lawful-default", {}), ("lawful-ceilings", CEILINGS), *self.configs]:
            limits = self.inputs / ("config-" + name + ".json")
            limits.write_bytes(encode(config))
            expected_status = "CLEAN" if name.startswith("lawful-") else "NOT_EXECUTED"
            code = "COMPLETE" if expected_status == "CLEAN" else "MALFORMED_INPUT"
            for operation in ("project", "check", "scan"):
                argv = [sys.executable, "-m", "ci.newman_publication", operation, "--limits", limits]
                if operation in ("project", "check"):
                    argv += ["--manifest", fixture.manifest]
                if operation == "project":
                    argv += ["--output-dir", self.base / ("config-output-" + name)]
                else:
                    argv += ["--archive", fixture.output / "publication.zip"]
                process = invoke(argv, label="limits-" + operation)
                errors, result = result_errors(process, operation, expected_status, code)
                config_results.append({"id": name, "operation": operation, "errors": errors,
                                       "capture": str(process.capture_dir), "rc": process.returncode,
                                       "observed": result})
                if errors:
                    failures.append("limits/" + name + "/" + operation)
        groups = sorted({r["group"] for r in self.rows})
        summary = {"scope": "CI-NP-02/03 carrier, decoding and lower-only capacity CLI; no SDK/workflow/retention",
                   "prerequisites_executed": len(self.prerequisites), "scan_declared": len(self.rows),
                   "scan_attempts": len(self.scans), "scan_semantic_decisions": sum(r["semantic_decision_executed"] for r in self.scans),
                   "missing_scan_dispatch": missing_dispatch, "limits_declared": (len(self.configs) + 2) * 3,
                   "invalid_or_missing_scan_verdict": sum(not r["semantic_decision_executed"] for r in self.scans),
                   "limits_attempts": len(config_results), "failed_expectations": len(failures),
                   "groups": {g: {"declared": sum(r["group"] == g for r in self.rows),
                                   "attempted": sum(r["group"] == g for r in self.scans),
                                   "lawful": sum(r["group"] == g and r["status"] == "CLEAN" for r in self.rows),
                                   "negative": sum(r["group"] == g and r["status"] != "CLEAN" for r in self.rows)}
                              for g in groups}, "failures": failures}
        (EVIDENCE / "carrier-results.json").write_bytes(encode(self.scans))
        (EVIDENCE / "limits-results.json").write_bytes(encode(config_results))
        (EVIDENCE / "carrier-summary.json").write_text(json.dumps(summary, indent=2) + "\n")
        print(json.dumps({k: v for k, v in summary.items() if k != "failures"}, sort_keys=True), flush=True)
        self.require(not failures, "carrier CLI expectations failed; see carrier-summary.json; "
                     "missing scan dispatch is a capability boundary, never semantic FINDING")


if __name__ == "__main__":
    print("evidence=" + str(EVIDENCE), flush=True)
    unittest.main(verbosity=2)
