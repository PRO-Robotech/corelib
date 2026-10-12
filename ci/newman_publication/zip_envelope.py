"""Полное покрытие байтов ZIP, который производит локальный projector.

Final check принимает обычный однодисковый ZIP без ZIP64, descriptors,
extras и comments. Historical scan дополнительно читает измеренный streaming
профиль: DEFLATE, нулевые local sizes/CRC и подписанный descriptor 16 bytes.
Разметка: PKWARE APPNOTE 6.3.10, §4.3.7/8/12/16.
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
DESCRIPTOR = struct.Struct("<4s3I")


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


def _extra_fields(data):
    """TLV framing проверяется целиком; payload каждого поля остаётся carrier."""
    fields = []
    position = 0
    while position < len(data):
        require(position + 4 <= len(data), "MALFORMED_INPUT")
        identifier, size = struct.unpack_from("<HH", data, position)
        # ZIP64/encryption требуют иной интерпретации размеров/байтов и здесь
        # не поддержаны; generic UTF-8 metadata не меняет framing архива.
        require(identifier not in (0x0001, 0x0017, 0x9901), "UNSUPPORTED_INPUT")
        position += 4
        require(position + size <= len(data), "MALFORMED_INPUT")
        fields.append(data[position:position + size])
        position += size
    return fields


def check_envelope(data, infos, *, historical_comment=None):
    historical = historical_comment is not None
    comment_bytes = historical_comment if historical else b""
    end_offset = len(data) - END.size - len(comment_bytes)
    signature, disk, directory_disk, disk_count, count, directory_size, directory_offset, comment = (
        _record(END, data, end_offset, len(data)))
    require(signature == b"PK\x05\x06" and comment == len(comment_bytes)
            and data[end_offset + END.size:] == comment_bytes, "UNSUPPORTED_INPUT")
    require(disk == directory_disk == 0 and disk_count == count == len(infos)
            and 0 < count < 0xFFFF, "UNSUPPORTED_INPUT")
    require(directory_offset + directory_size == end_offset, "MALFORMED_INPUT")

    central_position = directory_offset
    spans = []
    metadata = [comment_bytes] if historical else []
    for info in infos:
        header = _record(CENTRAL, data, central_position, end_offset)
        (signature, made_by, needed, flags, method, modified_time, modified_date,
         crc, compressed, size, name_size, extra_size, comment_size, start_disk,
         internal_attributes, external_attributes, local_offset) = header
        require(signature == b"PK\x01\x02", "MALFORMED_INPUT")
        require(start_disk == internal_attributes == 0
                and flags in ((0, 0x800, 8) if historical else (0,)) and needed <= 20
                and method in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED), "UNSUPPORTED_INPUT")
        descriptor = historical and flags == 8
        if descriptor:
            require(method == zipfile.ZIP_DEFLATED, "UNSUPPORTED_INPUT")
        if historical:
            # Старые ZIP writers могли не проставить Unix file-type bits.
            require(made_by >> 8 in (0, 3) and not external_attributes & 0x10
                    and stat.S_IFMT(external_attributes >> 16) in (0, stat.S_IFREG), "UNSAFE_PATH")
        else:
            require(extra_size == comment_size == 0, "UNSUPPORTED_INPUT")
            require(made_by >> 8 == 3 and stat.S_ISREG(external_attributes >> 16)
                    and external_attributes & 0xFFFF == 0, "UNSAFE_PATH")
        name_start = central_position + CENTRAL.size
        name_end = name_start + name_size
        extra_end = name_end + extra_size
        central_position = extra_end + comment_size
        require(central_position <= end_offset, "MALFORMED_INPUT")
        name = data[name_start:name_end]
        encoding = "utf-8" if flags & 0x800 else "cp437"
        require(name == info.filename.encode(encoding if historical else "ascii")
                and local_offset == info.header_offset
                and crc == info.CRC and compressed == info.compress_size
                and size == info.file_size, "MALFORMED_INPUT")

        local = _record(LOCAL, data, local_offset, directory_offset)
        require(local[0] == b"PK\x03\x04", "MALFORMED_INPUT")
        # Общие поля от version-needed до extra-size совпадают побайтно по смыслу:
        # local extra нельзя спрятать за пустым central extra.
        if descriptor:
            require(local[1:6] == header[2:7] and local[6:9] == (0, 0, 0)
                    and local[9] == name_size, "MALFORMED_INPUT")
        else:
            require(local[1:10] == header[2:11], "MALFORMED_INPUT")
        if not historical:
            require(local[10] == extra_size, "MALFORMED_INPUT")
        local_name_start = local_offset + LOCAL.size
        local_name_end = local_name_start + name_size
        payload_start = local_name_end + local[10]
        payload_end = payload_start + compressed
        require(payload_end <= directory_offset
                and data[local_name_start:local_name_end] == name, "MALFORMED_INPUT")
        member_end = payload_end
        if descriptor:
            # Central задаёт compressed span; конец DEFLATE проверяется отдельно.
            # Descriptor занимает ровно следующие 16 bytes без поиска signature.
            require(_record(DESCRIPTOR, data, payload_end, directory_offset)
                    == (b"PK\x07\x08", crc, compressed, size), "MALFORMED_INPUT")
            member_end += DESCRIPTOR.size
        if historical:
            metadata.extend(_extra_fields(data[name_end:extra_end]))
            metadata.append(data[extra_end:central_position])
            metadata.extend(_extra_fields(data[local_name_end:payload_start]))
        spans.append((local_offset, payload_start, payload_end, member_end, method, size))

    require(central_position == end_offset, "MALFORMED_INPUT")
    position = 0
    for start, payload_start, payload_end, member_end, method, size in sorted(spans):
        # Равенство, а не <=: ни наложений, ни префикса, ни дыр между members.
        require(start == position, "MALFORMED_INPUT")
        _payload(data, payload_start, payload_end, method, size)
        # CRC фактических распакованных bytes проверяет zipfile при чтении member.
        position = member_end
    require(position == directory_offset, "MALFORMED_INPUT")
    return metadata
