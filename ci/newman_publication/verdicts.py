"""Узкий kacho-shard-v1 adapter: каталог и числа, без нового вердикта о пробах."""
from __future__ import annotations

from pathlib import PurePosixPath

from .common import decode, integer, member_path, require, sha256


COUNTERS = ("requests", "unanswered", "assertions", "failed", "script_failed")
NUMBERS = ("expected", "reported", *COUNTERS)
REQUIRED = {"shard", "suites", *NUMBERS, "missing", "empty", "per_collection"}
PRECONDITIONS = ("external-chart-source", "stand-revision-divergence")
SUFFIX = ".postman_collection.json"


def report_counts(report):
    # Та же арифметика наблюдений, что у producer; категория остаётся у readers.
    stats = report["run"]["stats"]
    return {"requests": stats["requests"]["total"],
            "unanswered": stats["requests"]["failed"],
            "assertions": stats["assertions"]["total"],
            "failed": stats["assertions"]["failed"],
            "script_failed": stats["testScripts"]["failed"] + stats["prerequestScripts"]["failed"]}


class Verdicts:
    def __init__(self, catalogue):
        self.catalogue = catalogue
        self.enabled = bool(catalogue.manifest["verdicts"])
        self.selected, self.covered = set(), set()
        self.shards = {}
        self.reports = {entry["collection"]: entry["index"]
                        for entry in catalogue.manifest["reports"]}
        if not self.enabled:
            return
        data = catalogue.source_bytes("deploy/e2e-shards.json")
        self.catalogue_sha256 = sha256(data)
        document = decode(data)
        require(type(document) is dict and type(document.get("shards")) is list,
                "SOURCE_MISMATCH")
        require(document["shards"], "SOURCE_MISMATCH")
        for shard in document["shards"]:
            require(type(shard) is dict and type(shard.get("id")) is str
                    and shard["id"] and type(shard.get("suites")) is list, "SOURCE_MISMATCH")
            suites = shard["suites"]
            require(suites and all(type(suite) is str and suite for suite in suites),
                    "SOURCE_MISMATCH")
            require(len(set(suites)) == len(suites) and shard["id"] not in self.shards,
                    "SOURCE_MISMATCH")
            for suite in suites:
                require(str(member_path(suite)) == PurePosixPath(suite).name, "SOURCE_MISMATCH")
            self.shards[shard["id"]] = {"id": shard["id"], "suites": list(suites)}

    def relation(self, document):
        selector = document["shard"]
        require(type(selector) is str and selector in self.shards, "SOURCE_MISMATCH")
        shard = self.shards[selector]
        require(selector not in self.selected and document["suites"] == shard["suites"],
                "SOURCE_MISMATCH")
        relation = {}
        for suite in shard["suites"]:
            directory = "gateway/tests/newman" if suite == "api-gateway" else f"services/{suite}/tests/newman"
            prefix = directory + "/collections/"
            paths = sorted((path for path in self.catalogue.tracked_paths(prefix)
                            if path.startswith(prefix) and path.endswith(SUFFIX)),
                           key=lambda path: PurePosixPath(path).name)
            require(paths, "SOURCE_MISMATCH")
            for path in paths:
                key = suite + "/" + PurePosixPath(path).name[:-len(SUFFIX)]
                require(key not in relation and path in self.reports, "SOURCE_MISMATCH")
                index = self.reports[path]
                require(index not in self.covered, "SOURCE_MISMATCH")
                relation[key] = index
        self.selected.add(selector)
        self.covered.update(relation.values())
        return shard, relation

    def project(self, document, index, reports, *, final=False):
        require(type(document) is dict and REQUIRED <= document.keys(), "UNSUPPORTED_INPUT")
        shard, relation = self.relation(document)
        observed = {}
        projected = {}
        for raw_key, report_index in relation.items():
            member = f"reports/report-{report_index + 1:06d}.json"
            counts = report_counts(reports[report_index])
            observed[member if final else raw_key] = counts
            projected[member] = counts
        for key in NUMBERS:
            integer(document[key])
        require(document["expected"] == document["reported"] == len(relation), "SOURCE_MISMATCH")
        require(type(document["per_collection"]) is dict
                and set(document["per_collection"]) == set(observed), "SOURCE_MISMATCH")
        for key, expected in observed.items():
            counts = document["per_collection"][key]
            require(type(counts) is dict and set(COUNTERS) <= counts.keys(), "UNSUPPORTED_INPUT")
            require({field: integer(counts[field]) for field in COUNTERS} == expected,
                    "SOURCE_MISMATCH")
        totals = {field: sum(counts[field] for counts in projected.values()) for field in COUNTERS}
        require(all(document[field] == value for field, value in totals.items()), "SOURCE_MISMATCH")
        require(document["missing"] == [] and document["empty"] == [
            key for key, counts in observed.items() if counts["assertions"] == 0], "SOURCE_MISMATCH")
        output = {"schema_version": 1, "kind": "kacho-shard-v1", "index": index,
                  "shard": shard["id"], "suites": list(shard["suites"]),
                  "expected": len(relation), "reported": len(relation), **totals,
                  "missing": [], "empty": [key for key, counts in projected.items()
                                            if counts["assertions"] == 0],
                  "per_collection": projected}
        if "precondition" in document:
            precondition = document["precondition"]
            require(type(precondition) is dict and type(precondition.get("unmet")) is bool
                    and precondition.get("kind") in PRECONDITIONS, "UNSUPPORTED_INPUT")
            output["precondition"] = {"unmet": precondition["unmet"], "kind": precondition["kind"]}
        if final:
            require(type(document.get("schema_version")) is int
                    and type(document.get("index")) is int
                    and document == output, "UNSUPPORTED_INPUT")
        return output

    def finish(self):
        if self.enabled:
            require(self.covered == set(self.reports.values()), "SOURCE_MISMATCH")
