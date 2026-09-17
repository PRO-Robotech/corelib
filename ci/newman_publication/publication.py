"""Производимый ZIP и локальная проверка закрытой проекции.

Это не сканер исторических архивов и не разрешение на внешний upload.
"""
from __future__ import annotations

import io
import os
from pathlib import Path
import zipfile

from .catalogue import Catalogue
from .common import (decode, encode, field_count, integer, open_directory,
                     read_file, require, sha256, shape)
from .projection import check_log, log_projection, report_projection


def member_name(section, index):
    singular = {"reports": "report", "logs": "log"}[section]
    return f"{section}/{singular}-{index + 1:06d}.json"


def public_manifest(catalogue):
    return {"schema_version": 1, "run": dict(catalogue.manifest["run"]),
            "reports": [], "logs": [], "verdicts": []}


def entry_metadata(section, index, data, catalogue):
    result = {"index": index, "member": member_name(section, index),
              "bytes": len(data), "sha256": sha256(data)}
    if section == "reports":
        result["collection_sha256"] = catalogue.manifest[section][index]["collection_sha256"]
    return result


def project_members(catalogue, result):
    members = {}
    public = public_manifest(catalogue)
    for section in ("reports", "logs"):
        for entry in catalogue.manifest[section]:
            index = entry["index"]
            if section == "reports":
                data = catalogue.input_bytes(entry["report"])
                document = report_projection(decode(data), catalogue.collections[index])
            else:
                data = catalogue.input_bytes(entry["path"])
                document = log_projection(data, index)
            projected = encode(document)
            require(len(projected) <= catalogue.limits["document_bytes"], "LIMIT_EXCEEDED")
            public[section].append(entry_metadata(section, index, projected, catalogue))
            members[member_name(section, index)] = projected
            result["files_checked"] += 1
            result["fields_checked"] += field_count(document)
    members["manifest.json"] = encode(public)
    result["fields_checked"] += field_count(public)
    return members


def zip_bytes(members, limits):
    require(len(members) <= limits["zip_entries"], "LIMIT_EXCEEDED")
    require(sum(map(len, members.values())) <= limits["expanded_bytes"], "LIMIT_EXCEEDED")
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name, data in members.items():
            # Даже timestamp и permissions производятся здесь, не берутся из raw ZIP.
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100600 << 16
            archive.writestr(info, data)
    data = stream.getvalue()
    require(len(data) <= limits["archive_bytes"], "LIMIT_EXCEEDED")
    return data


def bind_bytes(result, data):
    result["archive_bytes"] = len(data)
    result["archive_sha256"] = sha256(data)


def write_output(output, data, result):
    path = Path(output).absolute()
    parent = open_directory(path.parent)
    owned = False
    directory = None
    try:
        try:
            os.mkdir(path.name, mode=0o700, dir_fd=parent)
            owned = True
        except FileExistsError:
            require(False, "OUTPUT_EXISTS")
        directory = os.open(path.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        for name, content in (("publication.zip", data), ("verdict.json", encode(result))):
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                         0o600, dir_fd=directory)
            with os.fdopen(fd, "wb") as stream:
                stream.write(content)
        owned = False
    finally:
        if owned and directory is not None:
            for name in ("publication.zip", "verdict.json"):
                try:
                    os.unlink(name, dir_fd=directory)
                except FileNotFoundError:
                    pass
        if directory is not None:
            os.close(directory)
        if owned:
            os.rmdir(path.name, dir_fd=parent)
        os.close(parent)


def project(manifest, output, limits, result):
    # lexists учитывает dangling symlink; чужой прежний результат не удаляется.
    require(not os.path.lexists(output), "OUTPUT_EXISTS")
    catalogue = Catalogue(manifest, limits, result)
    data = zip_bytes(project_members(catalogue, result), limits)
    bind_bytes(result, data)
    result.update(status="CLEAN", code="COMPLETE")
    write_output(output, data, result)


def check(manifest, archive_path, stdin, limits, result):
    if archive_path == "-":
        data = stdin.read(limits["archive_bytes"] + 1)
        require(len(data) <= limits["archive_bytes"], "LIMIT_EXCEEDED")
    else:
        data = read_file(archive_path, limits["archive_bytes"])
    bind_bytes(result, data)
    catalogue = Catalogue(manifest, limits, result)
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        infos = archive.infolist()
        require(len(infos) <= limits["zip_entries"], "LIMIT_EXCEEDED")
        require(not archive.comment, "UNSUPPORTED_INPUT")
        require(len({info.filename for info in infos}) == len(infos), "UNSAFE_PATH")
        expected = {"manifest.json"}
        for section in ("reports", "logs"):
            expected.update(member_name(section, index) for index in range(len(catalogue.manifest[section])))
        require({info.filename for info in infos} == expected, "UNSUPPORTED_INPUT")
        require(sum(info.file_size for info in infos) <= limits["expanded_bytes"], "LIMIT_EXCEEDED")
        for info in infos:
            require(not info.extra and not info.comment and not info.flag_bits & 1
                    and info.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED), "UNSUPPORTED_INPUT")
            require(info.file_size <= limits["document_bytes"], "LIMIT_EXCEEDED")
        public = decode(archive.read("manifest.json"))
        shape(public, ("schema_version", "run", "reports", "logs", "verdicts"))
        require(type(public["schema_version"]) is int and public["schema_version"] == 1)
        require(public["run"] == catalogue.manifest["run"] and public["verdicts"] == [], "SOURCE_MISMATCH")
        for section in ("reports", "logs"):
            require(type(public[section]) is list
                    and len(public[section]) == len(catalogue.manifest[section]))
            for index, entry in enumerate(public[section]):
                content = archive.read(member_name(section, index))
                require(entry == entry_metadata(section, index, content, catalogue), "SOURCE_MISMATCH")
                document = decode(content)
                if section == "reports":
                    # Повторная проекция обязана быть тождественной: opaque поля,
                    # даже не похожие на секрет, не входят в эту схему.
                    require(document == report_projection(document, catalogue.collections[index]),
                            "UNSUPPORTED_INPUT")
                else:
                    check_log(document, index)
                result["files_checked"] += 1
                result["fields_checked"] += field_count(document)
        result["fields_checked"] += field_count(public)
    result.update(status="CLEAN", code="COMPLETE")
