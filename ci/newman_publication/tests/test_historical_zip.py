"""Independent CI-NP-08 historical SDK ZIP profile holder; synthetic bytes only.

verifies: https://github.com/PRO-Robotech/kacho/issues/1810

Approved routine scope: signed 16-byte descriptors (flags 8, deflate 8,
zero local CRC/sizes), strict full framing and UTF-8 .cli/.rc text. The
project/check profile and all carrier/capacity semantics remain unchanged.
No product parser is imported. Stdlib writer/readers establish prerequisites
before the first scan, and every mutation keeps its lawful original evidence.
"""
from __future__ import annotations

import binascii
import io
import json
from pathlib import Path
import struct
import sys
import tempfile
import unittest
import zipfile
import zlib

from carrier_corpus import CANARY
from support import EVIDENCE, Fixture, digest, fixture_hash_check, invoke
from test_carrier_scanner import result_errors


END = struct.Struct("<4s4H2IH")
CENTRAL = struct.Struct("<4s6H3I5H2I")
LOCAL = struct.Struct("<4s5H3I2H")
DESCRIPTOR = struct.Struct("<4s3I")


class Unseekable:
    """Exercise zipfile's real streaming writer, not a handcrafted ZIP twin."""
    def __init__(self):
        self.data = bytearray()

    def write(self, value):
        self.data.extend(value)
        return len(value)

    def tell(self):
        raise OSError("synthetic streaming sink has no position API")

    def seek(self, *args):
        raise OSError("synthetic streaming sink cannot seek")

    def flush(self):
        pass


def bundle(members, streaming=True):
    sink = Unseekable() if streaming else io.BytesIO()
    with zipfile.ZipFile(sink, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name, value in members.items():
            info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 3
            info.external_attr = 0o100644 << 16
            archive.writestr(info, value)
    return bytes(sink.data) if streaming else sink.getvalue()


def readable(data):
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        return {info.filename: archive.read(info) for info in archive.infolist()}


def profile(data, members, streaming=True):
    """Independent full fixture census: framing, metadata, decoded bytes/CRC."""
    assert readable(data) == members
    end_at = len(data) - END.size
    end = END.unpack_from(data, end_at)
    assert end[0] == b"PK\x05\x06" and end[1:3] == (0, 0) and end[-1] == 0
    assert end[3] == end[4] == len(members)
    assert end[5] + end[6] == end_at
    position, local_end, rows = end[6], 0, []
    for name, content in members.items():
        central = CENTRAL.unpack_from(data, position)
        assert central[0] == b"PK\x01\x02"
        assert central[3] == (8 if streaming else 0) and central[4] == 8
        assert central[11:15] == (0, 0, 0, 0)
        assert central[15] == 0o100644 << 16 and central[16] == local_end
        assert central[7] == binascii.crc32(content) & 0xFFFFFFFF
        assert central[9] == len(content)
        local = LOCAL.unpack_from(data, local_end)
        assert local[0] == b"PK\x03\x04" and local[1:6] == central[2:7]
        assert local[6:9] == ((0, 0, 0) if streaming else central[7:10])
        assert local[9:] == (len(name.encode("ascii")), 0)
        payload_at = local_end + LOCAL.size + local[9]
        assert data[local_end + LOCAL.size:payload_at] == name.encode("ascii")
        assert data[position + CENTRAL.size:position + CENTRAL.size + central[10]] == name.encode("ascii")
        descriptor_at = payload_at + central[8]
        inflater = zlib.decompressobj(-zlib.MAX_WBITS)
        decoded = inflater.decompress(data[payload_at:descriptor_at])
        assert decoded == content and inflater.eof and not inflater.unused_data and not inflater.unconsumed_tail
        if streaming:
            assert DESCRIPTOR.unpack_from(data, descriptor_at) == (b"PK\x07\x08", *central[7:10])
        rows.append({"name": name, "local": local_end, "payload": payload_at,
                     "descriptor": descriptor_at, "central": position,
                     "compressed": central[8], "expanded": central[9], "crc": central[7]})
        local_end = descriptor_at + (DESCRIPTOR.size if streaming else 0)
        position += CENTRAL.size + central[10]
    assert local_end == end[6] and position == end_at
    return {"central": end[6], "end": end_at, "members": rows}


def replace_field(data, offset, width, value):
    changed = bytearray(data)
    changed[offset:offset + width] = value.to_bytes(width, "little")
    changed = bytes(changed)
    offsets = [i for i, (old, new) in enumerate(zip(data, changed)) if old != new]
    assert offsets and all(offset <= i < offset + width for i in offsets)
    return changed


def splice_before_central(data, layout, position, removed, added):
    """One framing edit; repair only the directory location bookkeeping."""
    assert 0 <= position <= position + removed <= layout["central"]
    result = bytearray(data[:position] + added + data[position + removed:])
    shift = len(added) - removed
    new_central, new_end = layout["central"] + shift, layout["end"] + shift
    struct.pack_into("<I", result, new_end + 16, new_central)
    for row in layout["members"]:
        if row["local"] >= position:
            struct.pack_into("<I", result, row["central"] + shift + 42, row["local"] + shift)
    return bytes(result)


class HistoricalZip(unittest.TestCase):
    def test_ci_np_08_actual_historical_format(self):
        with tempfile.TemporaryDirectory(prefix="np-historical-") as temp:
            run = HistoricalRun(self, Path(temp))
            try:
                run.prepare()
            except Exception as error:
                run.write("historical-summary.json", {
                    "holder_outcome": "NOT_EXECUTED", "phase": "prerequisite",
                    "cause_type": type(error).__name__, "scan_attempts": 0,
                    "prerequisites_completed": run.prerequisites})
                raise
            run.execute()


class HistoricalRun:
    def __init__(self, test, base):
        self.test, self.base = test, base
        self.rows, self.prerequisites = [], []
        self.inputs = EVIDENCE / "historical-inputs"
        self.inputs.mkdir()

    def write(self, name, value):
        (EVIDENCE / name).write_text(json.dumps(value, indent=2) + "\n")

    def add(self, name, lawful, mutant=None, status="CLEAN", axis="lawful exact profile",
            operation="scan", manifest=None, prerequisite=None):
        data = lawful if mutant is None else mutant
        assert not any(row["id"] == name for row in self.rows)
        if mutant is not None:
            assert data != lawful
        directory = self.inputs / name
        directory.mkdir()
        (directory / "lawful.zip").write_bytes(lawful)
        (directory / "candidate.zip").write_bytes(data)
        pair = {"id": name, "axis": axis, "lawful_sha256": digest(lawful),
                "candidate_sha256": digest(data), "lawful_bytes": len(lawful),
                "candidate_bytes": len(data), "prerequisite": prerequisite,
                "changed_offsets_if_same_size": [i for i, pair in enumerate(zip(lawful, data))
                    if pair[0] != pair[1]] if len(lawful) == len(data) else None}
        (directory / "pair.json").write_text(json.dumps(pair, indent=2) + "\n")
        self.rows.append({"id": name, "data": data, "status": status, "axis": axis,
                          "path": directory / "candidate.zip", "pair": pair,
                          "operation": operation, "manifest": manifest})

    def prepare(self):
        fixture_hash_check()
        # Keep real project/check and all four reader outcome forms as prerequisites.
        for scene, expected in (("green", 0), ("finding", 1), ("precondition", 3), ("script-error", 1)):
            fixture = Fixture(self.base / scene, scene)
            before = fixture.reader_verdict(suffix="raw")
            self.test.assertEqual((before["coverage_rc"], before["suite_rc"]), (0, expected))
            projected = fixture.project()
            self.test.assertEqual(result_errors(projected, "project", "CLEAN", "COMPLETE")[0], [])
            data = (fixture.output / "publication.zip").read_bytes()
            checked = invoke([sys.executable, "-m", "ci.newman_publication", "check",
                              "--archive", fixture.output / "publication.zip",
                              "--manifest", fixture.manifest], label="historical-prereq-check")
            self.test.assertEqual(result_errors(checked, "check", "CLEAN", "COMPLETE", data)[0], [])
            members = readable(data)
            self.test.assertEqual(before, fixture.reader_verdict(members["reports/report-000001.json"], "public"))
            self.prerequisites.append({"scene": scene, "project": str(projected.capture_dir),
                                       "check": str(checked.capture_dir), "reader_pair": True})
            if scene == "green":
                self.projected, self.manifest = data, fixture.manifest
                self.projected_descriptor = bundle(members)
                profile(self.projected_descriptor, members)

        good_json = json.dumps({"public_label": CANARY}).encode()
        bad_json = json.dumps({"access_token": CANARY}).encode()
        source = {"fixture.json": good_json}
        ordinary = bundle(source, streaming=False)
        profile(ordinary, source, streaming=False)
        self.add("existing-seekable-json", ordinary, prerequisite="stdlib read + exact seekable profile")
        for suffix in ("json", "cli", "rc"):
            good = good_json if suffix == "json" else ("public_label=" + CANARY + "\n").encode()
            bad = bad_json if suffix == "json" else ("access_token=" + CANARY + "\n").encode()
            members = {"fixture." + suffix: good}
            lawful = bundle(members)
            negative = bundle({"fixture." + suffix: bad})
            good_layout = profile(lawful, members)
            profile(negative, {"fixture." + suffix: bad})
            self.add("descriptor-" + suffix, lawful, prerequisite=good_layout)
            self.add("secret-" + suffix, lawful, negative, "FINDING", "one assignment label changes public_label to access_token",
                     prerequisite="both exact profiles; same member path and value; one credential label")
            if suffix in ("cli", "rc"):
                invalid = bundle({"fixture." + suffix: good + b"\xff"})
                profile(invalid, {"fixture." + suffix: good + b"\xff"})
                self.add("invalid-utf8-" + suffix, lawful, invalid, "NOT_EXECUTED", "one invalid UTF-8 byte appended to text",
                         prerequisite="valid ZIP/CRC; bytes fail strict UTF-8 decode")
                with self.test.assertRaises(UnicodeDecodeError):
                    (good + b"\xff").decode("utf-8", errors="strict")

        lawful = bundle(source)
        layout = profile(lawful, source)
        row = layout["members"][0]
        d = row["descriptor"]
        for name, offset, width, value in (
                ("descriptor-signature", d, 4, int.from_bytes(b"PK\x07\x08", "little") ^ 1),
                ("descriptor-crc", d + 4, 4, row["crc"] ^ 1),
                ("descriptor-compressed-size", d + 8, 4, row["compressed"] + 1),
                ("descriptor-expanded-size", d + 12, 4, row["expanded"] + 1),
                ("local-placeholder-crc", row["local"] + 14, 4, 1),
                ("local-placeholder-compressed", row["local"] + 18, 4, 1),
                ("local-placeholder-expanded", row["local"] + 22, 4, 1)):
            mutant = replace_field(lawful, offset, width, value)
            self.test.assertEqual(readable(mutant), source)
            self.add(name, lawful, mutant, "NOT_EXECUTED", "one fixed-width header field",
                     prerequisite={"changed_field_offset": offset, "width": width,
                                   "stdlib_ignores_invalid_field_and_reads_original": True})

        # Descriptor truncation, a framing gap, and archive trailer all remain
        # ordinary-reader accessible; the strict scanner must cover these bytes.
        mutations = [
            ("descriptor-truncated", splice_before_central(lawful, layout, d + 15, 1, b""), "one descriptor byte removed; central offset repaired"),
            ("descriptor-gap", splice_before_central(lawful, layout, d + 16, 0, b"G"), "one gap byte inserted; central offset repaired"),
            ("archive-trailer", lawful + b"T", "one byte after EOCD"),
        ]
        for name, mutant, axis in mutations:
            self.test.assertEqual(readable(mutant), source)
            self.add(name, lawful, mutant, "NOT_EXECUTED", axis,
                     prerequisite="stdlib reads identical payload; only declared framing defect")

        # The claimed compressed span includes one byte after DEFLATE EOF.
        # Repair both size declarations, leaving only exact-consumption violated.
        tail = bytearray(splice_before_central(lawful, layout, d, 0, b"X"))
        struct.pack_into("<I", tail, d + 1 + 8, row["compressed"] + 1)
        struct.pack_into("<I", tail, row["central"] + 1 + 20, row["compressed"] + 1)
        self.test.assertEqual(readable(tail), source)
        inflater = zlib.decompressobj(-zlib.MAX_WBITS)
        self.test.assertEqual(inflater.decompress(tail[row["payload"]:d + 1]), good_json)
        self.test.assertEqual(inflater.unused_data, b"X")
        self.add("deflate-tail", lawful, bytes(tail), "NOT_EXECUTED", "one byte after DEFLATE EOF inside declared compressed span",
                 prerequisite="same decoded content; descriptor/central sizes repaired; unused_data exactly X")

        # An internally consistent CRC claim still must match the actual bytes.
        crc = replace_field(lawful, d + 4, 4, row["crc"] ^ 1)
        crc = replace_field(crc, row["central"] + 16, 4, row["crc"] ^ 1)
        with self.test.assertRaises(zipfile.BadZipFile):
            readable(crc)
        self.add("actual-crc", lawful, crc, "NOT_EXECUTED", "one false CRC claim in central and descriptor",
                 prerequisite="descriptor equals central; actual decoded CRC differs; stdlib raises BadZipFile")

        # A late member must be scanned, and a descriptor must end exactly at
        # the next local record as well as at the central directory boundary.
        mixed = {"one.json": good_json, "two.log": b"ordinary log\n", "three.txt": b"ordinary text\n",
                 "four.cli": b"ordinary command\n", "five.rc": ("public_label=" + CANARY + "\n").encode()}
        mixed_good = bundle(mixed)
        mixed_layout = profile(mixed_good, mixed)
        mixed_bad = dict(mixed, **{"five.rc": ("access_token=" + CANARY + "\n").encode()})
        mixed_secret = bundle(mixed_bad)
        profile(mixed_secret, mixed_bad)
        self.test.assertEqual([name for name in mixed if mixed[name] != mixed_bad[name]], ["five.rc"])
        self.add("descriptor-multiple-members", mixed_good, prerequisite=mixed_layout)
        self.add("secret-final-member", mixed_good, mixed_secret, "FINDING", "one assignment label in final member",
                 prerequisite="five exact profiles; only final member credential label changes")
        between = mixed_layout["members"][1]["local"]
        gap = splice_before_central(mixed_good, mixed_layout, between, 0, b"G")
        self.test.assertEqual(readable(gap), mixed)
        self.add("gap-between-members", mixed_good, gap, "NOT_EXECUTED", "one byte before second local record",
                 prerequisite="identical five payloads; subsequent central local offsets repaired")

        # Unknown binary types and empty archives cannot produce a vacuous CLEAN.
        binary = bundle({"fixture.bin": good_json})
        profile(binary, {"fixture.bin": good_json})
        self.add("unknown-member-type", lawful, binary, "NOT_EXECUTED", "one member suffix changes json to bin",
                 prerequisite="valid exact profile; identical member bytes")
        empty = bundle({})
        profile(empty, {})
        self.add("empty-archive", lawful, empty, "NOT_EXECUTED", "single member removed",
                 prerequisite="valid empty stdlib ZIP; census zero")

        self.add("strict-check-project-lawful", self.projected, operation="check", manifest=self.manifest,
                 prerequisite="actual project/check and reader pair already passed")
        self.add("strict-check-descriptor-refused", self.projected, self.projected_descriptor, "NOT_EXECUTED",
                 "same exact projected member bytes repacked with streaming descriptor", operation="check", manifest=self.manifest,
                 prerequisite="both ZIPs readable with identical complete member map")
        self.test.assertEqual(readable(self.projected), readable(self.projected_descriptor))
        self.write("historical-prerequisites.json", {"status": "PASS", "real_scenes": self.prerequisites,
                    "cases_prepared": len(self.rows), "all_pair_inputs_saved": True})

    def execute(self):
        results, failures = [], []
        for row in self.rows:
            args = [sys.executable, "-m", "ci.newman_publication", row["operation"], "--archive", row["path"]]
            if row["manifest"]:
                args += ["--manifest", row["manifest"]]
            process = invoke(args, label="historical-" + row["id"])
            try:
                observed = json.loads(process.stdout)
            except (ValueError, UnicodeError):
                observed = None
            # Refusal code is intentionally not narrowed beyond the accepted
            # malformed/unsupported/empty code family for these input defects.
            code = {"CLEAN": "COMPLETE", "FINDING": "SECRET_MATERIAL"}.get(row["status"])
            if code is None:
                code = observed.get("code") if isinstance(observed, dict) else "MALFORMED_INPUT"
            errors, observed = result_errors(process, row["operation"], row["status"], code, row["data"])
            if row["status"] == "NOT_EXECUTED" and code not in {
                    "MALFORMED_INPUT", "UNSUPPORTED_INPUT", "UNSUPPORTED_ENCODING", "EMPTY_INPUT"}:
                errors.append("refusal_code_outside_input_family")
            is_valid = isinstance(observed, dict) and observed.get("operation") == row["operation"]
            results.append({"id": row["id"], "operation": row["operation"], "expected": row["status"],
                            "observed": observed, "rc": process.returncode, "errors": errors,
                            "capture": str(process.capture_dir),
                            "content_decision": is_valid and observed.get("status") in ("CLEAN", "FINDING"),
                            "scope_refusal": is_valid and observed.get("status") == "NOT_EXECUTED"})
            if errors:
                failures.append(row["id"])
        summary = {"scope": "historical signed descriptor ZIP + UTF-8 cli/rc; no live retention or SDK delivery",
                   "prerequisites": "PASS", "declared": len(self.rows), "attempted": len(results),
                   "scan_attempts": sum(row["operation"] == "scan" for row in results),
                   "content_decisions": sum(row["content_decision"] for row in results),
                   "scope_refusals": sum(row["scope_refusal"] for row in results),
                   "expected_findings": sum(row["expected"] == "FINDING" for row in results),
                   "actual_findings": sum(isinstance(row["observed"], dict) and row["observed"].get("status") == "FINDING" for row in results),
                   "failed_expectations": len(failures), "failures": failures}
        self.write("historical-results.json", results)
        self.write("historical-summary.json", summary)
        print(json.dumps(summary, sort_keys=True), flush=True)
        self.test.assertFalse(failures, "historical profile expectations failed; see evidence; NOT_EXECUTED is not a secret finding")


if __name__ == "__main__":
    print("evidence=" + str(EVIDENCE), flush=True)
    unittest.main(verbosity=2)
