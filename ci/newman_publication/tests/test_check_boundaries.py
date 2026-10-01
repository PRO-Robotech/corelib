"""Negative final-ZIP checks, paired with the actual current project/check CLI."""
import copy
import io
import json
from pathlib import Path
import struct
import sys
import tempfile
import unittest
import warnings
import zipfile

import test_publication
from support import EVIDENCE, Fixture, digest, invoke

CANARY = b"synthetic apricot private material"


class FinalArchiveBoundary(unittest.TestCase):
    verdict = test_publication.PublicationProjection.verdict
    project_clean = test_publication.PublicationProjection.project_clean

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="np-zip-boundary-")
        self.addCleanup(self.temp.cleanup)
        self.fixture = Fixture(self.temp.name, "finding")
        (self.fixture.raw / "runner.log").write_text("ordinary fixture log\n")
        self.fixture.data["logs"] = [{"index": 0, "path": "runner.log"}]
        self.fixture.save_manifest()
        self.project_clean(self.fixture)
        self.original = (self.fixture.output / "publication.zip").read_bytes()
        with zipfile.ZipFile(io.BytesIO(self.original)) as archive:
            self.infos = [copy.copy(info) for info in archive.infolist()]
            self.members = {info.filename: archive.read(info) for info in self.infos}

    def repack(self, members=None, infos=None, comment=b""):
        data = io.BytesIO()
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)  # duplicate-member fixture is intentional
            with zipfile.ZipFile(data, "w") as archive:
                archive.comment = comment
                for info in self.infos if infos is None else infos:
                    archive.writestr(info, (self.members if members is None else members)[info.filename])
        return data.getvalue()

    def refuse(self, data, name):
        path = Path(self.temp.name) / (name + ".zip")
        path.write_bytes(data)
        # Archive construction is a prerequisite. Malformed ZIP probes opt out
        # explicitly; all metadata/member mutants remain readable ordinary ZIPs.
        if name not in {"truncated", "invalid-format", "empty-file"}:
            with zipfile.ZipFile(path) as archive:
                self.assertGreaterEqual(len(archive.infolist()), 0)
        process = invoke([sys.executable, "-m", "ci.newman_publication", "check",
                          "--archive", path, "--manifest", self.fixture.manifest], label="check-" + name)
        (process.capture_dir / "lawful.zip").write_bytes(self.original)
        (process.capture_dir / "mutated.zip").write_bytes(data)
        (process.capture_dir / "mutation.json").write_text(json.dumps({
            "kind": name, "lawful_sha256": digest(self.original), "mutated_sha256": digest(data),
            "lawful_bytes": len(self.original), "mutated_bytes": len(data),
            "trusted_collection_sha256": digest(self.fixture.collection.read_bytes()),
        }, indent=2) + "\n")
        self.assertTrue(process.returncode in (1, 3), name + ": mutated final ZIP was authorized")
        self.assertFalse(process.stderr, "refusal leaked raw diagnostic")
        result = json.loads(process.stdout)
        status = {1: "FINDING", 3: "NOT_EXECUTED"}[process.returncode]
        self.assertEqual(result["status"], status)
        self.assertNotEqual(result["code"], "COMPLETE")
        self.verdict(process, "check", status, result["code"])
        self.assertTrue(CANARY not in process.stdout + process.stderr)
        if result["archive_sha256"] is not None:
            self.assertEqual(result["archive_sha256"], digest(data))
            self.assertEqual(result["archive_bytes"], len(data))

    def test_np04_bytes_before_or_after_zip_are_not_closed_projection(self):
        for name, data in [("prefix", CANARY + b"\n" + self.original),
                           ("suffix", self.original + b"\n" + CANARY)]:
            with self.subTest(position=name):
                self.refuse(data, name)

    def test_np04_internal_zip_gaps_and_shadow_directory_are_refused(self):
        # The lawful writer emits a single ordinary EOCD and no ZIP64 envelope.
        eocd = len(self.original) - 22
        self.assertEqual(self.original[eocd:eocd + 4], b"PK\x05\x06")
        central = struct.unpack_from("<I", self.original, eocd + 16)[0]

        def insert_before_central(position, payload, local_extra=False):
            data = bytearray(self.original[:position] + payload + self.original[position:])
            shift = len(payload)
            if local_extra:
                self.assertEqual(struct.unpack_from("<H", data, 28)[0], 0)
                struct.pack_into("<H", data, 28, shift)
            cursor = central + shift
            while cursor < eocd + shift:
                self.assertEqual(data[cursor:cursor + 4], b"PK\x01\x02")
                offset = struct.unpack_from("<I", data, cursor + 42)[0]
                if offset >= position:
                    struct.pack_into("<I", data, cursor + 42, offset + shift)
                name, extra, comment = struct.unpack_from("<HHH", data, cursor + 28)
                cursor += 46 + name + extra + comment
            self.assertEqual(cursor, eocd + shift)
            struct.pack_into("<I", data, eocd + shift + 16, central + shift)
            return bytes(data)

        local_name_size = struct.unpack_from("<H", self.original, 26)[0]
        local_extra = insert_before_central(30 + local_name_size,
            struct.pack("<HH", 0xCAFE, len(CANARY)) + CANARY, local_extra=True)
        duplicate_tail = bytearray(self.original[central:])
        struct.pack_into("<I", duplicate_tail, len(duplicate_tail) - 6, len(self.original))
        variants = [
            ("local-extra-only", local_extra),
            ("gap-before-central", insert_before_central(central, CANARY)),
            ("shadow-eocd-before-central", insert_before_central(central, self.original[eocd:])),
            ("repeated-central-eocd-tail", self.original + bytes(duplicate_tail)),
        ]
        for name, data in variants:
            with self.subTest(kind=name):
                # Every mutation retains readable identical member payloads and
                # an empty central extra. The added bytes are the only carrier.
                with zipfile.ZipFile(io.BytesIO(data)) as archive:
                    self.assertEqual({i.filename: archive.read(i) for i in archive.infolist()}, self.members)
                    self.assertTrue(all(not i.extra for i in archive.infolist()))
                self.refuse(data, name)

    def test_np03_symlink_or_directory_member_metadata_is_refused(self):
        for name, mode in [("symlink", 0o120777), ("directory", 0o040700)]:
            with self.subTest(kind=name):
                infos = copy.deepcopy(self.infos)
                target = next(info for info in infos if info.filename.startswith("reports/"))
                target.create_system = 3
                target.external_attr = mode << 16
                self.refuse(self.repack(infos=infos), name)

    def test_np03_metadata_carriers_are_not_ignored(self):
        self.refuse(self.repack(comment=CANARY), "archive-comment")
        for name, field, value in [("member-comment", "comment", CANARY),
                                    ("member-extra", "extra", struct.pack("<HH", 0xCAFE, len(CANARY)) + CANARY)]:
            with self.subTest(kind=name):
                infos = copy.deepcopy(self.infos)
                setattr(infos[0], field, value)
                self.refuse(self.repack(infos=infos), name)

    def test_np03_duplicate_unknown_missing_and_unsafe_member(self):
        for name in ["duplicate", "unknown", "missing", "traversal", "absolute"]:
            with self.subTest(kind=name):
                infos, members = copy.deepcopy(self.infos), dict(self.members)
                if name == "duplicate":
                    infos.append(copy.copy(infos[-1]))
                elif name == "missing":
                    infos = [info for info in infos if not info.filename.startswith("reports/")]
                else:
                    filename = {"unknown": "extra.bin", "traversal": "../escape.json", "absolute": "/escape.json"}[name]
                    info = zipfile.ZipInfo(filename)
                    infos.append(info)
                    members[filename] = CANARY
                self.refuse(self.repack(members, infos), name)

    def test_np03_empty_truncated_and_nonzip_are_not_clean(self):
        for name, data in [("empty-file", b""), ("invalid-format", CANARY), ("truncated", self.original[:-16])]:
            with self.subTest(kind=name):
                self.refuse(data, name)
        data = io.BytesIO()
        with zipfile.ZipFile(data, "w"):
            pass
        self.refuse(data.getvalue(), "empty-archive")

    def test_np02_rebound_member_hash_does_not_authorize_opaque_key(self):
        members = dict(self.members)
        name = "reports/report-000001.json"
        report = json.loads(members[name])
        report[CANARY.decode()] = "ordinary value"
        members[name] = json.dumps(report).encode()
        public = json.loads(members["manifest.json"])
        entry = public["reports"][0]
        entry.update(bytes=len(members[name]), sha256=digest(members[name]))
        members["manifest.json"] = json.dumps(public).encode()
        self.refuse(self.repack(members), "rebound-opaque-key")

    def test_np03_changed_public_binding_and_bad_member_hash_refused(self):
        for mode in ["run", "digest", "unknown-key"]:
            with self.subTest(kind=mode):
                members = dict(self.members)
                public = json.loads(members["manifest.json"])
                if mode == "run":
                    public["run"]["attempt"] += 1
                elif mode == "digest":
                    public["reports"][0]["sha256"] = "0" * 64
                else:
                    public[CANARY.decode()] = "ordinary value"
                members["manifest.json"] = json.dumps(public).encode()
                self.refuse(self.repack(members), "manifest-" + mode)


if __name__ == "__main__":
    print("evidence=" + str(EVIDENCE), flush=True)
    unittest.main(verbosity=2)
