"""Обход поддержанных носителей, общая credential-семантика и бюджеты.

Узлы считаются для каждой типизированной representation. Ключи и ZIP metadata
сканируются как координаты без базового JSON node; их decoded representations
расходуют тот же общий бюджет. Вложенность dict/list не является unwrap.
"""
from __future__ import annotations

import base64
import binascii
import json
import re
from urllib.parse import unquote_to_bytes, urlsplit

from .common import Refusal, decode, require


SECRET_NAMES = frozenset((
    "password", "passwd", "pwd", "clientsecret", "accesstoken", "refreshtoken",
    "sessiontoken", "idtoken", "authtoken", "bearertoken", "clientassertion",
    "privatekey", "signingkey", "apikey", "secretkey", "authorization",
    "proxyauthorization", "cookie", "setcookie",
))
ASSIGNMENT = re.compile(
    r'''(?=(?<![\w-])([A-Za-z][A-Za-z0-9_-]*)["']?\s*([:=])\s*'''
    r'''("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}&\]"']+))''')
BEARER = re.compile(r"\bBearer[ \t]+([^\s,;\"'<>]+)", re.IGNORECASE)
BASIC = re.compile(r"\bBasic[ \t]+([^\s]+)", re.IGNORECASE)
AUTH_NAMES = frozenset(("authorization", "proxyauthorization"))
HEADER = re.compile(r"(?=(?<![\w-])([A-Za-z][A-Za-z0-9_-]*)[ \t]*:[ \t]*([^\r\n]*))")
BASIC_SCHEME = re.compile(r"^Basic(?:[ \t]|$)", re.IGNORECASE)
BASIC_VALUE = re.compile(r"Basic[ \t]+([^\s]+)[ \t]*\Z", re.IGNORECASE)
PRIVATE_KEY = re.compile(r"-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----")
URL = re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://[^\s\"'<>]+")
PERCENT = re.compile(r"%[0-9A-Fa-f]{2}")
BASE64 = re.compile(r"[A-Za-z0-9+/_-]+={0,2}\Z")
BASE64_TOKEN = re.compile(r"(?<![A-Za-z0-9+/_-])[A-Za-z0-9+/_-]{4,}={0,2}(?![A-Za-z0-9+/_=-])")
JSON_START = re.compile(r'^\s*(?:\{\s*(?:"|})|\[\s*(?:\]|[\[\{"\d-]|true\b|false\b|null\b)|")')
JSON_OBJECT_START = re.compile(r'^\{[ \t\r\n]*"')


def normalized(name):
    return re.sub(r"[^a-z0-9]", "", name.lower())


def present(value):
    # Ни placeholders, ни redaction markers не являются отсутствием значения.
    return value is not None and value != ""


def text_bytes(data):
    try:
        return data.decode("utf-8")
    except UnicodeError:
        raise Refusal("UNSUPPORTED_INPUT") from None


def _base64(text, urlsafe=False, *, canonical=True):
    require(type(text) is str and bool(BASE64.fullmatch(text)), "MALFORMED_INPUT")
    require(not (set(text) & (set("+/") if urlsafe else set("-_"))), "MALFORMED_INPUT")
    require(len(text.rstrip("=")) % 4 != 1, "MALFORMED_INPUT")
    if "=" in text:
        require(len(text) % 4 == 0, "MALFORMED_INPUT")
    try:
        data = base64.b64decode(text + "=" * (-len(text) % 4),
                               altchars=b"-_" if urlsafe else None, validate=True)
    except (ValueError, binascii.Error):
        raise Refusal("MALFORMED_INPUT") from None
    if canonical:
        encoded = (base64.urlsafe_b64encode if urlsafe else base64.b64encode)(data).decode("ascii")
        require(text.rstrip("=") == encoded.rstrip("="), "MALFORMED_INPUT")
    return data


def _implicit_base64(text):
    """Отделяет распознанный carrier от случайного слова без codec declaration.

    Канонический UTF-8 результат, padding или декодируемая структура — признаки
    carrier. Ненулевые pad bits распознанного carrier дают отказ. Например,
    обычный ключ cycles не каноничен и раскрывается лишь в непрозрачный фрагмент;
    JSON, assignment, URL или следующий base64 слой скрыть так нельзя.
    """
    if not BASE64.fullmatch(text) or len(text) < 4:
        return None
    urlsafe = bool(set(text) & set("-_"))
    try:
        raw = _base64(text, urlsafe, canonical=False)
    except Refusal:
        return None  # Лексическая форма не объявляет неявный base64 carrier.
    try:
        decoded = text_bytes(raw)
    except Refusal:
        require(not text.endswith("="), "UNSUPPORTED_INPUT")
        return None
    encoded = (base64.urlsafe_b64encode if urlsafe else base64.b64encode)(raw).decode("ascii")
    canonical = text.rstrip("=") == encoded.rstrip("=")
    plain = decoded.strip()
    readable = all(char.isprintable() or char in "\r\n\t" for char in decoded)
    structured = (JSON_START.match(decoded) or ASSIGNMENT.search(decoded)
                  or URL.search(decoded) or PERCENT.search(decoded)
                  or PRIVATE_KEY.search(decoded) or BEARER.search(decoded) or BASIC.search(decoded)
                  or (len(plain) >= 4 and BASE64.fullmatch(plain)))
    if not (canonical and readable) and not text.endswith("=") and not structured:
        return None
    require(canonical, "MALFORMED_INPUT")
    return raw


def _url_bytes(text):
    require(type(text) is str and not re.search(r"%(?![0-9a-fA-F]{2})", text), "MALFORMED_INPUT")
    return unquote_to_bytes(text)


def _typed(data):
    """Parse object/array/string JSON внутри того же unwrap, без промежуточного node."""
    text = text_bytes(data)
    stripped = text.strip(" \t\r\n")
    if stripped.startswith(("{", "[", '"')):
        try:
            value, end = json.JSONDecoder().raw_decode(stripped)
        except (ValueError, RecursionError):
            require(not JSON_OBJECT_START.match(stripped), "MALFORMED_INPUT")
        else:
            if end == len(stripped) and type(value) in (dict, list, str):
                return decode(stripped)
    return text


def _basic_candidate(token):
    """Discovery may locate credential bytes, but never validates a token prefix."""
    prefix = re.match(r"[A-Za-z0-9+/]+={0,2}", token)
    if prefix is None:
        return None
    try:
        raw = _base64(prefix.group(), canonical=False)
        material = text_bytes(raw)
    except Refusal:
        return None
    if ":" not in material:
        return None
    # The *original whole token*, including any junk suffix, must be canonical.
    return _base64(token)


class CarrierWalk:
    def __init__(self, limits, result, *, detect=True):
        self.limits, self.result, self.detect = limits, result, detect
        self.nodes = 0

    def finding(self):
        if self.detect:
            self.result["findings"] += 1

    def add_node(self):
        self.nodes += 1
        require(self.nodes <= self.limits["decoded_nodes"], "LIMIT_EXCEEDED")

    def next_depth(self, depth):
        require(depth < self.limits["decode_depth"], "LIMIT_EXCEEDED")
        return depth + 1

    def representation(self, data, depth, context):
        depth = self.next_depth(depth)
        require(len(data) <= self.limits["document_bytes"], "LIMIT_EXCEEDED")
        self.walk(_typed(data), depth, context)

    def walk(self, value, depth=0, context="", *, node=True, unwrap=True, path=()):
        if node:
            self.add_node()
        self.result["fields_checked"] += 1
        if type(value) is dict:
            self.object(value, depth, context, path)
        elif type(value) is list:
            for index, child in enumerate(value):
                self.walk(child, depth, context, unwrap=unwrap, path=path + (index,))
        elif type(value) is str:
            self.text(value, depth, context, unwrap=unwrap)

    def coordinate(self, value):
        self.walk(value, node=False)

    def object(self, value, depth, context, path):
        is_buffer = value.get("type") == "Buffer"
        is_encoded = "encoding" in value
        if is_buffer:
            require(type(value.get("data")) is list and all(
                type(byte) is int and 0 <= byte <= 255 for byte in value["data"]), "MALFORMED_INPUT")
        if is_encoded:
            require(value["encoding"] in ("url", "base64", "base64url"), "UNSUPPORTED_ENCODING")
            require(type(value.get("data")) is str, "MALFORMED_INPUT")

        # Поддержаны обычные JSON fields и Postman key/value либо name/value arrays.
        field = value.get("key", value.get("name"))
        if type(field) is str and "value" in value and present(value["value"]):
            if normalized(field) in SECRET_NAMES or (context == "bearer" and normalized(field) == "token"):
                self.finding()
        for key, child in value.items():
            name = normalized(key)
            # Newman response.cookie — типизированный jar, а не scalar Cookie
            # header. Пустой jar содержит ноль credentials; в другом контексте
            # [] не становится маркером отсутствия чувствительного значения.
            cookie_jar = (key == "cookie" and len(path) == 4
                          and path[:2] == ("run", "executions")
                          and type(path[2]) is int and path[3] == "response")
            if cookie_jar:
                require(type(child) is list, "MALFORMED_INPUT")
                for cookie in child:
                    require(type(cookie) is dict and type(cookie.get("name")) is str
                            and "value" in cookie, "MALFORMED_INPUT")
                    if present(cookie["value"]):
                        self.finding()
            elif name in SECRET_NAMES and present(child):
                self.finding()
            if context == "bearer" and name == "token" and present(child):
                self.finding()
            self.walk(key, depth, context, node=False)
            child_context = context
            if name in AUTH_NAMES or (key == "value" and type(field) is str
                                      and normalized(field) in AUTH_NAMES):
                child_context = "authorization"
            if value.get("type") in ("bearer", "basic") and key == value["type"]:
                child_context = value["type"]
            # Явный envelope декодируется ровно один раз после базового обхода.
            self.walk(child, depth, child_context,
                      unwrap=not ((is_buffer or is_encoded) and key in ("data", "type", "encoding")),
                      path=path + (key,))
        if is_buffer:
            self.representation(bytes(value["data"]), depth, context)
        if is_encoded:
            encoding, payload = value["encoding"], value["data"]
            data = _url_bytes(payload) if encoding == "url" else _base64(payload, encoding == "base64url")
            self.representation(data, depth, context)

    def text(self, text, depth, context, *, unwrap=True):
        # Locate complete JSON spans before inspecting their lexical surroundings.
        # Their original bytes are strictly decoded below; permissive discovery
        # cannot erase duplicate keys, non-finite values, or the remaining text.
        json_spans = []
        introduced_error = False
        if unwrap:
            decoder = json.JSONDecoder()
            position = 0
            while position < len(text):
                if text[position] not in '{["':
                    position += 1
                    continue
                try:
                    value, end = decoder.raw_decode(text, position)
                except (ValueError, RecursionError):
                    introduced_error |= bool(JSON_OBJECT_START.match(text[position:]))
                    position += 1
                    continue
                if type(value) in (dict, list, str):
                    json_spans.append((position, end))
                position = end
        decoded_spans = list(json_spans)

        def inside(position, spans=json_spans):
            return any(start <= position < end for start, end in spans)

        for recognizer in (PRIVATE_KEY, BEARER):
            if any(not inside(match.start()) for match in recognizer.finditer(text)):
                self.finding()

        def basic(token):
            raw = _base64(token)
            material = text_bytes(raw)
            require(":" in material, "MALFORMED_INPUT")
            if unwrap:
                self.representation(raw, depth, context)
            if present(material.split(":", 1)[1]):
                self.finding()

        declared_spans = []
        header_starts = set()
        for match in HEADER.finditer(text):
            if normalized(match.group(1)) not in AUTH_NAMES or inside(match.start(1)):
                continue
            header_starts.add(match.start(1))
            value = match.group(2).strip(" \t")
            if present(value):
                self.finding()
            if BASIC_SCHEME.match(value):
                parsed = BASIC_VALUE.fullmatch(value)
                require(parsed is not None, "MALFORMED_INPUT")
                basic(parsed.group(1))
                declared_spans.append(match.span(2))
                decoded_spans.append(match.span(2))

        # Structured header declarations apply only to the actual value subtree.
        # A raw header ends at CR/LF; its generic assignment recognizer cannot
        # cross that boundary and claim the following line as a header value.
        value = text.strip(" \t")
        if context == "authorization" and BASIC_SCHEME.match(value):
            parsed = BASIC_VALUE.fullmatch(value)
            require(parsed is not None, "MALFORMED_INPUT")
            basic(parsed.group(1))
            declared_spans.append((0, len(text)))
            decoded_spans.append((0, len(text)))

        for match in ASSIGNMENT.finditer(text):
            if (inside(match.start(1)) or match.start(1) in header_starts
                    or normalized(match.group(1)) not in SECRET_NAMES):
                continue
            token = match.group(3)
            if token.startswith('"'):
                try:
                    material = json.loads(token)
                except ValueError:
                    material = token
            elif token.startswith("'"):
                material = token[1:-1]
            else:
                material = None if token == "null" and match.group(2) == ":" else token
            if present(material):
                self.finding()
        for match in BASIC.finditer(text):
            if inside(match.start()) or inside(match.start(), declared_spans):
                continue
            token = match.group(1)
            raw = _base64(token) if context == "basic" else _basic_candidate(token)
            if raw is None:
                continue
            basic(token)
            decoded_spans.append(match.span(1))
        for match in URL.finditer(text):
            if inside(match.start()):
                continue
            try:
                url = urlsplit(match.group())
            except ValueError:
                raise Refusal("MALFORMED_INPUT") from None
            if present(url.password):
                self.finding()
        if not unwrap or not text:
            return

        require(not introduced_error, "MALFORMED_INPUT")
        if len(json_spans) == 1:
            start, end = json_spans[0]
            if not text[:start].strip(" \t\r\n") and not text[end:].strip(" \t\r\n"):
                self.walk(decode(text[start:end]), self.next_depth(depth), context)
                return
        if PERCENT.search(text):
            # A recognized URL carrier still cannot hide malformed/binary bytes.
            data = _url_bytes(text)
            decoded_text = text_bytes(data)
            if decoded_text != text:
                self.walk(_typed(data), self.next_depth(depth), context)
                return
        raw = _implicit_base64(text.strip())
        if raw is not None:
            self.representation(raw, depth, context)
            return
        for start, end in json_spans:
            self.walk(decode(text[start:end]), self.next_depth(depth), context)
        # Full original-text traversal continues after every JSON/Basic span.
        for token in BASE64_TOKEN.finditer(text):
            if any(start <= token.start() and token.end() <= end for start, end in decoded_spans):
                continue
            raw = _implicit_base64(token.group())
            if raw is not None:
                self.representation(raw, depth, context)
