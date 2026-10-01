"""Actual Kacho shard producer/reader fixtures; no publication implementation."""
from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import shutil
import sys
import uuid

from support import EVIDENCE, ENV, FIXTURES, Fixture, digest, fixture_hash_check, invoke


SHARD_FIXTURES = FIXTURES / "shard-consumer"
SHARD = "probe-shard"
SUITE = "probe"
CATALOGUE_PATH = "deploy/e2e-shards.json"
COLLECTION_PATH = "services/probe/tests/newman/collections/fixture.postman_collection.json"


def producer_hashes():
    fixture_hash_check()
    provenance = json.loads((SHARD_FIXTURES / "provenance.json").read_bytes())
    for name, record in provenance["files"].items():
        assert digest((SHARD_FIXTURES / name).read_bytes()) == record["sha256"]
    return provenance


class ShardFixture:
    def __init__(self, base, scene="green", mark=None):
        self.fixture = f = Fixture(base, scene)
        self.base, self.source, self.raw = f.base, f.source, f.raw
        self.catalogue = {"shards": [{"id": SHARD, "suites": [SUITE]}]}
        scripts = self.source / ".github/scripts"
        scripts.mkdir(parents=True)
        for name in ("shard-verdict.py", "aggregate-shard-verdicts.py"):
            shutil.copyfile(SHARD_FIXTURES / name, scripts / name)
        (self.source / "deploy").mkdir()
        (self.source / CATALOGUE_PATH).write_text(json.dumps(self.catalogue) + "\n")
        collection = self.source / COLLECTION_PATH
        collection.parent.mkdir(parents=True)
        shutil.copyfile(f.collection, collection)
        f.data["reports"][0]["collection"] = COLLECTION_PATH
        f.data["reports"][0]["collection_sha256"] = digest(collection.read_bytes())
        for args in (["add", ".github/scripts", CATALOGUE_PATH, str(collection.relative_to(self.source))],
                     ["-c", "user.name=NP shard fixture", "-c", "user.email=np@example.invalid",
                      "commit", "-qm", "bind actual shard producer and tracked suite catalogue"]):
            result = invoke(["git", *args], cwd=self.source, label="shard-git")
            assert result.returncode == 0
        f.data["source_commit"] = invoke(["git", "rev-parse", "HEAD"], cwd=self.source,
                                           label="shard-head").stdout.decode().strip()
        f.save_manifest()
        self.producer = scripts / "shard-verdict.py"
        self.reader = scripts / "aggregate-shard-verdicts.py"
        self.shard_report = self.source / "services/probe/tests/newman/out/fixture.json"
        self.shard_report.parent.mkdir()
        shutil.copyfile(f.report, self.shard_report)
        self.mark = mark
        if mark == "file":
            marker = self.source / "deploy/helm/umbrella/.kacho-deps-precondition-unmet"
            marker.parent.mkdir(parents=True)
            marker.write_text("synthetic chart prerequisite unavailable\n")
        self.verdict_path = self.raw / "private-shard-verdict.json"
        self.raw_verdict = None

    def commit_catalogue(self):
        (self.source / CATALOGUE_PATH).write_text(json.dumps(self.catalogue) + "\n")
        collections = [str(p.relative_to(self.source)) for p in
                       sorted((self.source / "services").glob("*/tests/newman/collections/*.postman_collection.json"))]
        result = invoke(["git", "add", "deploy/e2e-shards.json", *collections],
                        cwd=self.source, label="shard-catalogue-add")
        assert result.returncode == 0
        result = invoke(["git", "-c", "user.name=NP shard fixture", "-c",
                         "user.email=np@example.invalid", "commit", "-qm", "bind test catalogue"],
                        cwd=self.source, label="shard-catalogue-commit")
        assert result.returncode == 0
        self.fixture.data["source_commit"] = invoke(["git", "rev-parse", "HEAD"],
            cwd=self.source, label="shard-catalogue-head").stdout.decode().strip()
        self.fixture.save_manifest()

    def add_suite(self, suite="other", shard="other-shard", declared=True):
        f = self.fixture
        collection = self.source / f"services/{suite}/tests/newman/collections/fixture.postman_collection.json"
        collection.parent.mkdir(parents=True)
        shutil.copyfile(f.collection, collection)
        output = collection.parent.parent / "out/fixture.json"
        output.parent.mkdir()
        shutil.copyfile(f.report, output)
        self.catalogue["shards"].append({"id": shard, "suites": [suite]})
        if declared:
            private = self.raw / f"{suite}-report.json"
            shutil.copyfile(f.report, private)
            f.data["reports"].append({"index": len(f.data["reports"]),
                "collection": str(collection.relative_to(self.source)),
                "collection_sha256": digest(collection.read_bytes()), "report": private.name})
        self.commit_catalogue()

    def produce(self, shard=SHARD, destination=None):
        destination = self.verdict_path if destination is None else destination
        old = ENV.pop("STAND_PRECONDITION_UNMET", None)
        if self.mark == "env":
            ENV["STAND_PRECONDITION_UNMET"] = "1"
        try:
            process = invoke([sys.executable, self.producer, "--shard", shard,
                              "--out", destination], cwd=self.source, label="actual-shard-producer")
        finally:
            ENV.pop("STAND_PRECONDITION_UNMET", None)
            if old is not None:
                ENV["STAND_PRECONDITION_UNMET"] = old
        self.raw_verdict = json.loads(destination.read_bytes())
        self.producer_capture = str(process.capture_dir)
        return process

    def aggregate(self, document, label):
        directory = self.base / ("aggregate-" + label + "-" + uuid.uuid4().hex[:8])
        directory.mkdir()
        documents = document if isinstance(document, list) else [document]
        for index, item in enumerate(documents):
            (directory / f"shard-verdict-{index + 1:06d}.json").write_text(json.dumps(item) + "\n")
        process = invoke([sys.executable, self.reader, "--dir", directory,
                          "--plan-result", "success", "--shard-result", "success"],
                         cwd=self.source, label="actual-aggregate-" + label)
        spec = importlib.util.spec_from_file_location("actual_aggregate_" + uuid.uuid4().hex, self.reader)
        reader = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(reader)
        verdicts = reader.load(directory)
        count = reader.tree_collection_count(self.source)
        findings, totals = reader.decide(self.catalogue, verdicts, count,
                                         {"plan": "success", "shard": "success"})
        category, _ = reader.outcome(totals, findings)
        assert count > 0 and category.encode() in process.stdout
        observation = {"rc": process.returncode, "category": category,
                       "totals": totals, "finding_count": len(findings)}
        (process.capture_dir / "observed-reader.json").write_text(json.dumps(observation, ensure_ascii=False, indent=2) + "\n")
        return observation, str(process.capture_dir)

    def enable_verdict(self):
        self.fixture.data["verdicts"] = [{"index": 0, "kind": "kacho-shard-v1",
                                           "path": self.verdict_path.name}]
        self.fixture.save_manifest()

    def set_verdict(self, document):
        self.verdict_path.write_text(json.dumps(document) + "\n")

    def project(self, name, limits=None):
        f = self.fixture
        f.output = self.base / name
        args = []
        if limits is not None:
            config = self.base / (name + "-limits.json")
            config.write_text(json.dumps(limits))
            args = ["--limits", config]
        return f.project(*args)

    def checker(self, archive, label, limits=None):
        args = [sys.executable, "-m", "ci.newman_publication", "check", "--archive", archive,
                "--manifest", self.fixture.manifest]
        if limits is not None:
            config = self.base / (label + "-limits.json")
            config.write_text(json.dumps(limits))
            args += ["--limits", config]
        return invoke(args, label="verdict-check-" + label)
