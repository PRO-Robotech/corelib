"""Строит разрешённые поля заново; opaque строки вообще не переносятся."""
from __future__ import annotations

import ast
import json
import re

from .common import integer, require, shape


PRECONDITION = "[УСЛОВИЕ НЕ СОЗДАНО]"
STATS = {"iterations", "items", "scripts", "prerequests", "requests", "tests",
         "assertions", "testScripts", "prerequestScripts"}
LITERAL_TEST = re.compile(r'''\bpm\s*\.\s*test\s*\(\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')\s*,''')


def _script_names(item):
    """Только статическое имя из доверенного каталога, без исполнения JavaScript.

    Динамический текст стража здесь не поддержан: его нельзя безопасно заменить
    догадкой, сохранив предикат существующего coverage reader.
    """
    names = set()
    for event in item.get("event", []):
        if type(event) is not dict:
            continue
        script = event.get("script", {})
        if type(script) is not dict:
            continue
        lines = script.get("exec", [])
        if type(lines) is str:
            lines = [lines]
        if type(lines) is not list or not all(type(line) is str for line in lines):
            continue
        for match in LITERAL_TEST.finditer("\n".join(lines)):
            literal = match.group(1)
            try:
                name = json.loads(literal) if literal.startswith('"') else ast.literal_eval(literal)
            except (ValueError, SyntaxError):
                continue
            if type(name) is str and name.startswith(PRECONDITION):
                names.add(name)
    return names


class Collection:
    def __init__(self, document):
        require(type(document) is dict, "UNSUPPORTED_INPUT")
        self.leaves = []
        self.items = self._items(document.get("item"), "", _script_names(document))
        require(self.leaves, "UNSUPPORTED_INPUT")

    def _items(self, items, parent, inherited):
        require(type(items) is list, "UNSUPPORTED_INPUT")
        projected = []
        for item in items:
            require(type(item) is dict and type(item.get("name")) is str, "UNSUPPORTED_INPUT")
            name = item["name"]
            names = inherited | _script_names(item)
            if "request" in item:
                require("item" not in item, "UNSUPPORTED_INPUT")
                self.leaves.append((parent, name, names))
                projected.append({"name": name, "request": {}})
            else:
                projected.append({"name": name, "item": self._items(item.get("item"), name, names)})
        return projected

    def fingerprint(self):
        return [(parent, name) for parent, name, _ in self.leaves]

    def cursor(self, raw):
        require(type(raw) is dict, "UNSUPPORTED_INPUT")
        position = integer(raw.get("position"))
        iteration = integer(raw.get("iteration"))
        length = integer(raw.get("length"), 1)
        require(position < len(self.leaves) and length == len(self.leaves), "SOURCE_MISMATCH")
        cursor = {"position": position, "iteration": iteration, "length": length}
        if "cycles" in raw:
            cycles = integer(raw["cycles"], 1)
            require(iteration < cycles, "SOURCE_MISMATCH")
            cursor["cycles"] = cycles
        return cursor

    def failure(self, raw):
        require(type(raw) is dict, "UNSUPPORTED_INPUT")
        cursor = self.cursor(raw.get("cursor"))
        parent, name, allowed_preconditions = self.leaves[cursor["position"]]
        source, ancestor = raw.get("source"), raw.get("parent")
        require(type(source) is dict and source.get("name") == name
                and type(ancestor) is dict and ancestor.get("name") == parent, "SOURCE_MISMATCH")
        error = raw.get("error")
        require(type(error) is dict and type(error.get("name")) is str, "UNSUPPORTED_INPUT")
        projected = {"name": "Error", "message": "redacted"}
        if error["name"] == "AssertionError":
            projected["name"] = "AssertionError"
            test = error.get("test", "")
            require(type(test) is str, "UNSUPPORTED_INPUT")
            if test.startswith(PRECONDITION):
                require(test in allowed_preconditions, "UNSUPPORTED_INPUT")
                # Возвращается экземпляр имени из каталога, не свободная строка отчёта.
                projected["test"] = next(item for item in allowed_preconditions if item == test)
            else:
                projected["test"] = "assertion"
        elif error.get("code"):
            projected["code"] = "REQUEST_ERROR"
        return {"cursor": cursor, "source": {"name": name}, "parent": {"name": parent},
                "error": projected}


def report_projection(raw, catalogue):
    trusted = Collection(catalogue)
    require(type(raw) is dict and type(raw.get("collection")) is dict, "UNSUPPORTED_INPUT")
    observed = Collection(raw["collection"])
    require(observed.fingerprint() == trusted.fingerprint(), "SOURCE_MISMATCH")
    run = raw.get("run")
    require(type(run) is dict, "UNSUPPORTED_INPUT")
    stats = run.get("stats")
    require(type(stats) is dict and set(stats) == STATS, "UNSUPPORTED_INPUT")
    projected_stats = {}
    for name in sorted(STATS):
        counts = shape(stats[name], ("total", "pending", "failed"))
        projected_stats[name] = {key: integer(counts[key]) for key in ("total", "pending", "failed")}
    executions = run.get("executions")
    failures = run.get("failures")
    require(type(executions) is list and type(failures) is list, "UNSUPPORTED_INPUT")
    projected_executions = []
    for execution in executions:
        require(type(execution) is dict, "UNSUPPORTED_INPUT")
        cursor = trusted.cursor(execution.get("cursor"))
        _, name, _ = trusted.leaves[cursor["position"]]
        require(type(execution.get("item")) is dict
                and execution["item"].get("name") == name, "SOURCE_MISMATCH")
        response = execution.get("response")
        if response is not None:
            require(type(response) is dict, "UNSUPPORTED_INPUT")
            code = integer(response.get("code"), 100)
            require(code <= 599, "UNSUPPORTED_INPUT")
            response = {"code": code}
        projected = {"cursor": cursor, "item": {"name": name}, "response": response}
        if execution.get("requestError") is not None:
            projected["requestError"] = {"code": "REQUEST_ERROR"}
        projected_executions.append(projected)
    return {"collection": {"item": trusted.items}, "run": {
        "stats": projected_stats,
        "executions": projected_executions,
        "failures": [trusted.failure(failure) for failure in failures],
    }}


def log_projection(data, index):
    return {"schema_version": 1, "kind": "log", "index": index,
            "bytes": len(data), "lines": data.count(b"\n") + int(bool(data) and not data.endswith(b"\n"))}


def check_log(document, index):
    shape(document, ("schema_version", "kind", "index", "bytes", "lines"))
    require(type(document["schema_version"]) is int and document["schema_version"] == 1)
    require(document["kind"] == "log" and integer(document["index"]) == index)
    integer(document["bytes"])
    integer(document["lines"])
