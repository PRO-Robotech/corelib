"""Полный scoped scan поддержанных historical JSON/log ZIP, без публикации."""
from __future__ import annotations

import io
from pathlib import PurePosixPath
import zipfile

from .carriers import CarrierWalk, text_bytes
from .common import decode, member_path, read_file, require, sha256
from .zip_envelope import check_envelope


def scan(path, limits, result):
    data = read_file(path, limits["archive_bytes"])
    result.update(archive_bytes=len(data), archive_sha256=sha256(data))
    require(data, "EMPTY_INPUT")
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        infos = archive.infolist()
        result["files_declared"] = len(infos)
        require(infos, "EMPTY_INPUT")
        require(len(infos) <= limits["zip_entries"], "LIMIT_EXCEEDED")
        require(len({info.filename for info in infos}) == len(infos), "UNSAFE_PATH")
        require(sum(info.file_size for info in infos) <= limits["expanded_bytes"], "LIMIT_EXCEEDED")
        for info in infos:
            member_path(info.filename)
            require(not info.is_dir() and info.orig_filename == info.filename, "UNSAFE_PATH")
            require(info.file_size <= limits["document_bytes"], "LIMIT_EXCEEDED")
            require(PurePosixPath(info.filename).suffix.lower() in (".json", ".log", ".txt"), "UNSUPPORTED_INPUT")
        metadata = check_envelope(data, infos, historical_comment=archive.comment)
        walker = CarrierWalk(limits, result)
        for value in metadata:
            if value:
                walker.coordinate(text_bytes(value))
        for info in infos:
            walker.coordinate(info.filename)
            with archive.open(info) as stream:
                content = stream.read(limits["document_bytes"] + 1)
                require(len(content) <= limits["document_bytes"], "LIMIT_EXCEEDED")
                require(len(content) == info.file_size, "MALFORMED_INPUT")
            if PurePosixPath(info.filename).suffix.lower() == ".json":
                walker.walk(decode(content))
            else:
                walker.walk(text_bytes(content))
            result["files_checked"] += 1
    result.update(status="FINDING" if result["findings"] else "CLEAN",
                  code="SECRET_MATERIAL" if result["findings"] else "COMPLETE")
