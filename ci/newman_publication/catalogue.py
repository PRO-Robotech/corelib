"""Приватное объявление входов и каталог, связанный с настоящим Git commit."""
from __future__ import annotations

import os
import re
import subprocess

from .common import (Refusal, absolute_path, decode, integer, member_path,
                     read_file, require, sha256, shape)


def git(root, *args):
    env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    env["GIT_NO_REPLACE_OBJECTS"] = "1"
    try:
        result = subprocess.run(["git", "--no-pager", "-C", str(root), *args],
                                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                timeout=20, check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise Refusal("SOURCE_MISMATCH") from None
    require(result.returncode == 0, "SOURCE_MISMATCH")
    return result.stdout


class Catalogue:
    def __init__(self, path, limits, result):
        self.limits = limits
        self.manifest = doc = decode(read_file(path, limits["document_bytes"]))
        shape(doc, ("schema_version", "source_root", "source_commit", "input_root",
                    "run", "reports", "logs", "verdicts"))
        require(type(doc["schema_version"]) is int and doc["schema_version"] == 1)
        shape(doc["run"], ("id", "attempt", "shard_index"))
        integer(doc["run"]["id"], 1)
        integer(doc["run"]["attempt"], 1)
        integer(doc["run"]["shard_index"])
        self.source = absolute_path(doc["source_root"])
        self.input = absolute_path(doc["input_root"])
        commit = doc["source_commit"]
        require(type(commit) is str and re.fullmatch(r"[a-f0-9]{40}", commit))
        seen = set()
        for section, keys, path_key in (
            ("reports", ("index", "collection", "collection_sha256", "report"), "report"),
            ("logs", ("index", "path"), "path"),
            ("verdicts", ("index", "kind", "path"), "path"),
        ):
            require(type(doc[section]) is list)
            for index, entry in enumerate(doc[section]):
                shape(entry, keys)
                require(integer(entry["index"]) == index)
                path_value = str(member_path(entry[path_key]))
                require(path_value not in seen, "UNSAFE_PATH")
                seen.add(path_value)
        result["files_declared"] = sum(len(doc[key]) for key in ("reports", "logs", "verdicts"))
        require(doc["reports"], "EMPTY_INPUT")
        for entry in doc["verdicts"]:
            require(entry["kind"] == "kacho-shard-v1", "UNSUPPORTED_INPUT")
        require(git(self.source, "rev-parse", "--show-toplevel").rstrip(b"\n")
                == os.fsencode(self.source), "SOURCE_MISMATCH")
        require(git(self.source, "rev-parse", "HEAD").strip() == commit.encode(), "SOURCE_MISMATCH")
        self.collections = []
        seen_collections = set()
        for entry in doc["reports"]:
            name = str(member_path(entry["collection"]))
            require(name not in seen_collections, "SOURCE_MISMATCH")
            seen_collections.add(name)
            digest = entry["collection_sha256"]
            require(type(digest) is str and re.fullmatch(r"[a-f0-9]{64}", digest))
            blob = self.source_bytes(name)
            require(sha256(blob) == digest, "SOURCE_MISMATCH")
            self.collections.append(decode(blob))

    def source_bytes(self, name):
        """Коллекции и shard catalogue имеют одну границу доверия к Git blob."""
        name = str(member_path(name))
        commit = self.manifest["source_commit"]
        tracked = git(self.source, "ls-tree", "-z", commit, "--", name)
        require(tracked.startswith((b"100644 blob ", b"100755 blob "))
                and tracked.count(b"\x00") == 1, "SOURCE_MISMATCH")
        blob = git(self.source, "show", commit + ":" + name)
        checkout = read_file(self.source / name, self.limits["document_bytes"])
        require(len(blob) <= self.limits["document_bytes"], "LIMIT_EXCEEDED")
        require(sha256(blob) == sha256(checkout), "SOURCE_MISMATCH")
        return blob

    def tracked_paths(self, prefix):
        raw = git(self.source, "ls-tree", "-r", "--name-only", "-z",
                  self.manifest["source_commit"], "--", prefix)
        return [name.decode("utf-8") for name in raw.split(b"\x00") if name]

    def input_bytes(self, relative):
        return read_file(self.input / member_path(relative), self.limits["document_bytes"])
