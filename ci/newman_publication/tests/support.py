"""Independent black-box fixture and capture utilities, no publication algorithm."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
import zipfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
FIXTURES = HERE / "fixtures"
ENV = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
ENV["PYTHONDONTWRITEBYTECODE"] = "1"
ENV["PYTHONPATH"] = str(ROOT)
EVIDENCE = Path(os.environ.get("CI_NP_EVIDENCE_DIR", tempfile.gettempdir())) / ("np-" + uuid.uuid4().hex[:10])
EVIDENCE.mkdir(parents=True)
SEQUENCE = 0


def digest(data):
    return hashlib.sha256(data).hexdigest()


def invoke(argv, cwd=ROOT, input_bytes=None, label="process"):
    global SEQUENCE
    SEQUENCE += 1
    dest = EVIDENCE / f"{SEQUENCE:04d}-{label}"
    dest.mkdir()
    started = time.monotonic()
    result = subprocess.run([str(x) for x in argv], cwd=cwd, input=input_bytes,
                            capture_output=True, env=ENV, timeout=40)
    (dest / "stdout").write_bytes(result.stdout)
    (dest / "stderr").write_bytes(result.stderr)
    (dest / "execution.json").write_text(json.dumps({
        "argv": [str(x) for x in argv], "cwd": str(cwd), "rc": result.returncode,
        "seconds": time.monotonic() - started,
        "stdout_sha256": digest(result.stdout), "stderr_sha256": digest(result.stderr),
    }, indent=2) + "\n")
    result.capture_dir = dest
    return result


class Fixture:
    def __init__(self, base, scene="green"):
        self.base = Path(base)
        self.source = self.base / "source"
        self.raw = self.base / "raw"
        self.output = self.base / "published"
        self.raw.mkdir(parents=True)
        shutil.copytree(FIXTURES / scene / "collections", self.source / "collections")
        shutil.copytree(FIXTURES / scene / "scripts", self.source / "scripts")
        self.report = self.raw / "fixture.json"
        shutil.copyfile(FIXTURES / scene / "out/fixture.json", self.report)
        self.collection = self.source / "collections/fixture.postman_collection.json"
        for args in [["init", "-q"], ["add", "."],
                     ["-c", "user.name=NP fixture", "-c", "user.email=np@example.invalid",
                      "commit", "-qm", "tracked Newman catalogue fixture"]]:
            result = invoke(["git", *args], self.source, label="git-fixture")
            if result.returncode:
                raise RuntimeError("fixture Git prerequisite failed")
        sha = invoke(["git", "rev-parse", "HEAD"], self.source, label="git-head").stdout.decode().strip()
        self.data = {
            "schema_version": 1, "source_root": str(self.source), "source_commit": sha,
            "input_root": str(self.raw), "run": {"id": 17, "attempt": 2, "shard_index": 0},
            "reports": [{"index": 0, "collection": "collections/fixture.postman_collection.json",
                         "collection_sha256": digest(self.collection.read_bytes()), "report": "fixture.json"}],
            "logs": [], "verdicts": [],
        }
        self.manifest = self.base / "private-manifest.json"
        self.save_manifest()

    def save_manifest(self):
        self.manifest.write_text(json.dumps(self.data))

    def change_report(self, change):
        report = json.loads(self.report.read_text())
        change(report)
        self.report.write_text(json.dumps(report))

    def project(self, *extra):
        return invoke([sys.executable, "-m", "ci.newman_publication", "project",
                       "--manifest", self.manifest, "--output-dir", self.output, *extra], label="project")

    def reader_verdict(self, report=None, suffix="raw"):
        """Run the real readers; relocation changes only filesystem coordinates."""
        suite = self.base / ("reader-" + suffix)
        shutil.copytree(self.source / "collections", suite / "collections")
        shutil.copytree(self.source / "scripts", suite / "scripts")
        (suite / "out").mkdir()
        if report is None:
            report = self.report.read_bytes()
        (suite / "out/fixture.json").write_bytes(report)
        readers = Path(os.environ.get("CI_NP_READER_DIR", FIXTURES / "readers"))
        coverage = invoke([sys.executable, readers / "exec-coverage.py", "--collections-glob",
                           "collections/*.postman_collection.json", "--out-dir", "out"], suite,
                          label="coverage-" + suffix)
        gate = invoke(["bash", readers / "assert-suites-green.sh"], suite, label="suite-" + suffix)
        # Import the actual implementation and set its existing REPO filesystem seam.
        spec = importlib.util.spec_from_file_location("np_real_newman_live", readers / "newman-live.py")
        live = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(live)
        live_root = self.base / ("live-" + suffix)
        live_suite = live_root / "services/probe/tests/newman"
        live_suite.parent.mkdir(parents=True)
        shutil.copytree(suite, live_suite)
        live.REPO = live_root
        state = live.read_suite("probe")
        failures = live.failures_of("probe")
        numeric = {key: getattr(state, key) for key in
                   ["expected", "reported", "requests", "assertions", "failed", "unanswered", "empty"]}
        kinds = [item["kind"] for item in failures]
        record = {"coverage_rc": coverage.returncode, "suite_rc": gate.returncode,
                  "live_counts": numeric, "live_kinds": kinds,
                  "reader_hashes": {n: digest((readers / n).read_bytes()) for n in
                                    ["exec-coverage.py", "assert-suites-green.sh", "newman-live.py"]}}
        (EVIDENCE / f"reader-{uuid.uuid4().hex[:8]}.json").write_text(json.dumps(record, indent=2))
        return {k: v for k, v in record.items() if k != "reader_hashes"}


def archive(path, members, comment=b""):
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as out:
        out.comment = comment
        for name, data in members.items():
            out.writestr(name, data)
    return path


def fixture_hash_check():
    declared = json.loads((FIXTURES / "provenance.json").read_text())["files"]
    actual = {name: digest((FIXTURES / name).read_bytes()) for name in declared}
    if declared != actual:
        raise RuntimeError("immutable fixture bytes mismatch")
    return actual
