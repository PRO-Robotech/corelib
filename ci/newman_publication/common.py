"""Безопасные отказы, чтение и сериализация без исходных диагностик."""
from __future__ import annotations

import errno
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import stat


class Refusal(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


LIMITS = {
    "archive_bytes": 67108864,
    "expanded_bytes": 268435456,
    "document_bytes": 67108864,
    "zip_entries": 1024,
    "decode_depth": 8,
    "decoded_nodes": 100000,
}


def require(condition, code="MALFORMED_INPUT"):
    if not condition:
        raise Refusal(code)


def integer(value, minimum=0):
    require(type(value) is int and value >= minimum)
    return value


def shape(value, keys):
    require(type(value) is dict and set(value) == set(keys))
    return value


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def encode(value):
    return (json.dumps(value, ensure_ascii=True, separators=(",", ":"),
                       sort_keys=True, allow_nan=False) + "\n").encode()


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result)
        result[key] = value
    return result


def decode(data):
    try:
        return json.loads(data, object_pairs_hook=_pairs,
                          parse_constant=lambda _: (_ for _ in ()).throw(Refusal("MALFORMED_INPUT")))
    except (ValueError, UnicodeError, RecursionError):
        raise Refusal("MALFORMED_INPUT") from None


def field_count(value):
    if type(value) is dict:
        return sum(1 + field_count(v) for v in value.values())
    if type(value) is list:
        return sum(field_count(v) for v in value)
    return 1


def member_path(value):
    require(type(value) is str and value and "\x00" not in value, "UNSAFE_PATH")
    path = PurePosixPath(value)
    require(not path.is_absolute() and ".." not in path.parts
            and value == path.as_posix() and "\\" not in value
            and value != ".", "UNSAFE_PATH")
    return path


def absolute_path(value):
    require(type(value) is str and value and "\x00" not in value, "UNSAFE_PATH")
    path = Path(value)
    require(path.is_absolute() and ".." not in path.parts, "UNSAFE_PATH")
    return path


def open_directory(path):
    """openat удерживает каждый каталог; symlink не проходит даже при замене пути."""
    path = absolute_path(str(path))
    fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in path.parts[1:]:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = nxt
        return fd
    except BaseException:
        os.close(fd)
        raise


def read_file(path, limit):
    path = Path(path).absolute()
    directory = open_directory(path.parent)
    try:
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        with os.fdopen(fd, "rb") as stream:
            require(stat.S_ISREG(os.fstat(stream.fileno()).st_mode), "UNSAFE_PATH")
            data = stream.read(limit + 1)
            require(len(data) <= limit, "LIMIT_EXCEEDED")
            return data
    finally:
        os.close(directory)


def os_code(error):
    if error.errno in (errno.ELOOP, errno.ENOTDIR):
        return "UNSAFE_PATH"
    if error.errno == errno.ENOENT:
        return "MISSING_INPUT"
    return "UNREADABLE_INPUT"


def load_limits(path):
    limits = dict(LIMITS)
    if path is not None:
        lowered = decode(read_file(path, LIMITS["document_bytes"]))
        require(type(lowered) is dict and set(lowered) <= set(limits))
        for key, value in lowered.items():
            require(1 <= integer(value, 1) <= limits[key])
            limits[key] = value
    return limits
