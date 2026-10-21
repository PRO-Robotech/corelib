"""Prepare real Newman/project/check twins before asking JS capability; no SUT copy."""
import json
from pathlib import Path
import sys
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from support import Fixture, fixture_hash_check, invoke, digest, EVIDENCE

root = Path(sys.argv[1])
root.mkdir(parents=True, exist_ok=True)
fixture_hash_check()
rows = []
for scene, suite_rc in [("green", 0), ("finding", 1), ("precondition", 3), ("script-error", 1)]:
    f = Fixture(root / scene, scene)
    before = f.reader_verdict()
    assert before["suite_rc"] == suite_rc and before["coverage_rc"] == 0
    projected = f.project()
    assert projected.returncode == 0 and not projected.stderr
    result = json.loads(projected.stdout)
    assert result["status"] == "CLEAN" and result["code"] == "COMPLETE"
    target = f.output / "publication.zip"
    data = target.read_bytes()
    assert result["archive_bytes"] == len(data) and result["archive_sha256"] == digest(data)
    for source, incoming in [(str(target), None), ("-", data)]:
        checked = invoke([sys.executable, "-m", "ci.newman_publication", "check", "--archive", source,
                          "--manifest", f.manifest], input_bytes=incoming, label="upload-prerequisite-check")
        assert checked.returncode == 0 and not checked.stderr
        verdict = json.loads(checked.stdout)
        assert verdict["operation"] == "check" and verdict["status"] == "CLEAN"
        assert verdict["archive_sha256"] == digest(data) and verdict["archive_bytes"] == len(data)
        assert verdict["files_declared"] == verdict["files_checked"] > 0 and verdict["findings"] == 0
    with zipfile.ZipFile(target) as z:
        after = f.reader_verdict(z.read("reports/report-000001.json"), "upload-projected")
    assert before == after
    rows.append({"scene": scene, "archivePath": str(target), "manifestPath": str(f.manifest),
                 "rawPath": str(f.report), "size": len(data), "sha256": digest(data),
                 "checkerResult": verdict, "readers": before})
# Genuine ZIP changes: one member added, removed or changed; not malformed harness bytes.
f = rows[0]
with zipfile.ZipFile(f["archivePath"]) as z:
    original = {name: z.read(name) for name in z.namelist()}
mutations = []
for kind in ["add", "remove", "change"]:
    members = dict(original)
    name = "reports/report-000001.json"
    if kind == "add":
        name = "reports/report-999999.json"
        members[name] = b"{}"
    elif kind == "remove":
        del members[name]
    else:
        members[name] += b" "
    changed = [p for p in original.keys() | members.keys() if original.get(p) != members.get(p)]
    assert changed == [name]
    target = root / (kind + ".zip")
    with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED) as z:
        for path, content in members.items():
            z.writestr(path, content)
    c = invoke([sys.executable, "-m", "ci.newman_publication", "check", "--archive", target,
                "--manifest", f["manifestPath"]], label="upload-mutated-check")
    assert c.returncode == 3 and not c.stderr
    answer = json.loads(c.stdout)
    assert answer["status"] == "NOT_EXECUTED"
    mutations.append({"kind": kind, "path": str(target), "changed_members": changed,
                      "sha256": digest(target.read_bytes()), "checkerResult": answer})
print(json.dumps({"fixtures": rows, "mutations": mutations, "captures": str(EVIDENCE)}))
