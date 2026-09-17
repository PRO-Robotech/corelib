"""Полное покрытие байтов ZIP, который производит локальный projector.

Поддержан обычный однодисковый ZIP без ZIP64, descriptors, extras и comments.
Эти расширения writer не производит; чтение произвольного исторического ZIP
не входит в final check. Разметка: PKWARE APPNOTE 6.3.10, §4.3.7/12/16.
https://pkware.cachefly.net/webdocs/casestudies/APPNOTE.TXT
"""
from __future__ import annotations

import stat
import struct
import zipfile
import zlib

from .common import require


END = struct.Struct("<4s4H2IH")
CENTRAL = struct.Struct("<4s6H3I5H2I")
LOCAL = struct.Struct("<4s5H3I2H")


def _record(layout, data, offset, stop):
    require(0 <= offset <= stop - layout.size, "MALFORMED_INPUT")
    return layout.unpack_from(data, offset)


def _payload(data, start, stop, method, size):
    """zipfile допускает хвост после DEFLATE end; такой хвост не проверен reader."""
    payload = data[start:stop]
    if method == zipfile.ZIP_STORED:
        require(len(payload) == size, "MALFORMED_INPUT")
    else:
        inflater = zlib.decompressobj(-zlib.MAX_WBITS)
        # Размер уже ограничен caller. +1 различает точный размер и ложную длину,
        # не позволяя inflater молча остановиться на заявленной границе.
        content = inflater.decompress(payload, size + 1)
        require(inflater.eof and not inflater.unused_data and not inflater.unconsumed_tail
                and len(content) == size, "MALFORMED_INPUT")


def check_envelope(data, infos):
    end_offset = len(data) - END.size
    signature, disk, directory_disk, disk_count, count, directory_size, directory_offset, comment = (
        _record(END, data, end_offset, len(data)))
    require(signature == b"PK\x05\x06" and comment == 0, "UNSUPPORTED_INPUT")
    require(disk == directory_disk == 0 and disk_count == count == len(infos)
            and 0 < count < 0xFFFF, "UNSUPPORTED_INPUT")
    require(directory_offset + directory_size == end_offset, "MALFORMED_INPUT")

    central_position = directory_offset
    spans = []
    for info in infos:
        header = _record(CENTRAL, data, central_position, end_offset)
        (signature, made_by, needed, flags, method, modified_time, modified_date,
         crc, compressed, size, name_size, extra_size, comment_size, start_disk,
         internal_attributes, external_attributes, local_offset) = header
        require(signature == b"PK\x01\x02", "MALFORMED_INPUT")
        require(extra_size == comment_size == start_disk == internal_attributes == 0
                and flags == 0 and needed <= 20
                and method in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED), "UNSUPPORTED_INPUT")
        require(made_by >> 8 == 3 and stat.S_ISREG(external_attributes >> 16)
                and external_attributes & 0xFFFF == 0, "UNSAFE_PATH")
        name_start = central_position + CENTRAL.size
        central_position = name_start + name_size
        require(central_position <= end_offset, "MALFORMED_INPUT")
        name = data[name_start:central_position]
        require(name == info.filename.encode("ascii") and local_offset == info.header_offset
                and crc == info.CRC and compressed == info.compress_size
                and size == info.file_size, "MALFORMED_INPUT")

        local = _record(LOCAL, data, local_offset, directory_offset)
        require(local[0] == b"PK\x03\x04", "MALFORMED_INPUT")
        # Общие поля от version-needed до extra-size совпадают побайтно по смыслу:
        # local extra нельзя спрятать за пустым central extra.
        require(local[1:] == header[2:12], "MALFORMED_INPUT")
        local_name_start = local_offset + LOCAL.size
        payload_start = local_name_start + name_size
        payload_end = payload_start + compressed
        require(payload_end <= directory_offset
                and data[local_name_start:payload_start] == name, "MALFORMED_INPUT")
        spans.append((local_offset, payload_start, payload_end, method, size))

    require(central_position == end_offset, "MALFORMED_INPUT")
    position = 0
    for start, payload_start, stop, method, size in sorted(spans):
        # Равенство, а не <=: ни наложений, ни префикса, ни дыр между members.
        require(start == position, "MALFORMED_INPUT")
        _payload(data, payload_start, stop, method, size)
        position = stop
    require(position == directory_offset, "MALFORMED_INPUT")
