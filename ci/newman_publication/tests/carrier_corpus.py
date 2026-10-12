"""CI-NP-02/03 inputs from the accepted carrier and accounting contracts.

This module only constructs inputs. It neither recognizes secrets nor imports
the subject's decoding, projection, ZIP reader, or limit implementation.
"""
from __future__ import annotations

import base64
import copy
import io
import json
import struct
import urllib.parse
import zipfile

CANARY = "np47c9e1"
REPRESENTATIONS = ("raw", "json-escaped", "url", "base64", "base64url", "buffer", "nested-json")
CARRIERS = (
    "request-header", "response-header", "environment", "globals", "collection-variable",
    "auth", "url", "query", "request-body", "response-body", "script", "collection-name",
    "test-name", "error", "json-key", "metadata", "filename", "manifest", "log",
    "archive-comment", "member-comment", "member-extra",
)
CEILINGS = {"archive_bytes": 67108864, "expanded_bytes": 268435456,
            "document_bytes": 67108864, "zip_entries": 1024,
            "decode_depth": 8, "decoded_nodes": 100000}


def encode(value):
    return json.dumps(value, ensure_ascii=True, separators=(",", ":")).encode()


def representation(text, name):
    if name == "raw":
        return text
    if name == "json-escaped":
        return '"' + "".join("\\u%04x" % ord(c) for c in text) + '"'
    if name == "url":
        return {"encoding": "url", "data": "".join("%%%02X" % b for b in text.encode())}
    if name in ("base64", "base64url"):
        encoder = base64.b64encode if name == "base64" else base64.urlsafe_b64encode
        # The suffix forces both nonstandard alphabet characters in base64url;
        # ordinary ASCII samples can accidentally test standard base64 twice.
        payload = text + ("\uffff" if name == "base64url" else "")
        return {"encoding": name, "data": encoder(payload.encode()).decode()}
    if name == "buffer":
        return {"type": "Buffer", "data": list(text.encode())}
    if name == "nested-json":
        return json.dumps({"note": text}, separators=(",", ":"))
    raise AssertionError("undeclared representation")


def text_value(value):
    return value if isinstance(value, str) else encode(value).decode()


def json_bytes(value, escaped=False):
    data = encode(value)
    if escaped:
        # JSON lexical escaping is consumed by the base parse, at depth zero.
        # Escaping both the key and value proves this is not a raw-token match.
        for text in ("access_token", "public_label", CANARY):
            data = data.replace(text.encode(), "".join("\\u%04x" % ord(c) for c in text).encode())
    return data


def zip_bytes(members, *, archive_comment=b"", member_comment=b"", extra=b""):
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as bundle:
        bundle.comment = archive_comment
        for index, (name, data) in enumerate(members):
            info = zipfile.ZipInfo(name, (2020, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            if index == 0:
                info.comment = member_comment
                info.extra = extra
            bundle.writestr(info, data)
    return out.getvalue()


def one_fact(left, right):
    """Compare the complete declarative world, before representation is rendered."""
    keys = set(left) | set(right)
    changed = [key for key in sorted(keys) if left.get(key) != right.get(key)]
    if len(changed) != 1:
        raise AssertionError("paired fixture must differ in exactly one declared input fact")
    return changed[0]


def carrier_pairs():
    for carrier in CARRIERS:
        for form in REPRESENTATIONS:
            good = {"carrier": carrier, "representation": form, "payload": "public_label=" + CANARY}
            bad = dict(good, payload="access_token=" + CANARY)
            yield carrier + "--" + form, good, bad


def render_carrier(base, spec):
    """Return raw report, input filename, optional log and legacy ZIP metadata.

    All modifications are outside the tracked catalogue fingerprint and numerical
    execution facts. The manifest case uses the allowed private report coordinate;
    it does not add a forbidden key to the closed private manifest schema.
    """
    doc = copy.deepcopy(base)
    carrier, form = spec["carrier"], spec["representation"]
    value = representation(spec["payload"], form)
    text = text_value(value)
    ex = doc["run"]["executions"][0]
    name, log, metadata = "fixture.json", None, {}
    if carrier == "request-header":
        ex["request"]["header"].append({"key": "X-Public-Note", "value": value})
    elif carrier == "response-header":
        ex["response"]["header"].append({"key": "X-Public-Note", "value": value})
    elif carrier in ("environment", "globals"):
        doc[carrier]["values"].append({"key": "note", "value": value})
    elif carrier == "collection-variable":
        doc["collection"].setdefault("variable", []).append({"key": "note", "value": value})
    elif carrier == "auth":
        ex["request"]["auth"] = {"type": "bearer", "bearer": [{"key": "note", "value": value}]}
    elif carrier == "url":
        ex["request"]["url"] = "https://example.invalid/?note=" + urllib.parse.quote(text, safe="")
    elif carrier == "query":
        ex["request"]["url"]["query"].append({"key": "note", "value": value})
    elif carrier == "request-body":
        ex["request"]["body"] = {"mode": "raw", "raw": value}
    elif carrier == "response-body":
        ex["response"]["stream"] = value
    elif carrier == "script":
        doc["collection"]["event"].append({"listen": "test", "script": {"type": "text/javascript", "exec": [text]}})
    elif carrier == "collection-name":
        doc["collection"]["info"]["name"] = text
    elif carrier == "test-name":
        ex["assertions"][0]["assertion"] = text
    elif carrier == "error":
        doc["run"]["failures"][0]["error"]["message"] = text
    elif carrier == "json-key":
        doc[text] = "public value"
    elif carrier == "metadata":
        doc["collection"]["info"]["description"] = value
    elif carrier in ("filename", "manifest"):
        # Percent escaping is a path representation, and avoids slash/path syntax
        # in JSON/base64 envelopes. This is an additional supported URL unwrap.
        name = urllib.parse.quote(text, safe="") + ".json"
    elif carrier == "log":
        log = text.encode() + b"\n"
    elif carrier == "archive-comment":
        metadata["archive_comment"] = text.encode()
    elif carrier == "member-comment":
        metadata["member_comment"] = text.encode()
    elif carrier == "member-extra":
        raw = text.encode()
        metadata["extra"] = struct.pack("<HH", 0xCAFE, len(raw)) + raw
    else:
        raise AssertionError("undeclared carrier")
    report = json_bytes(doc, form == "json-escaped")
    members = [(name, report)]
    if log is not None:
        members.append(("run.log", log))
    if carrier == "manifest":
        members.append(("manifest.json", json_bytes({"reports": [{"report": name}]}, form == "json-escaped")))
    archive = zip_bytes(members, **metadata)
    return {"report": report, "name": name, "log": log, "archive": archive}


def secret_field_pairs():
    # These are independent semantic families, not synonyms harvested from SUT.
    fields = ("password", "client_secret", "access_token", "refresh_token", "session_token",
              "client_assertion", "private_key")
    for name in fields:
        pieces = name.split("_")
        spellings = (name.upper(), pieces[0] + "".join(p.title() for p in pieces[1:]), name.replace("_", "-"))
        for index, spelling in enumerate(spellings):
            good = {"carrier": "field", "field": "public_label", "value": CANARY}
            bad = dict(good, field=spelling)
            yield name + "--spelling-" + str(index), good, bad
    for name in ("Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"):
        for index, spelling in enumerate((name.lower(), name.upper(), name.swapcase())):
            good = {"carrier": "header", "field": "X-Public-Label", "value": CANARY,
                    "target": "response" if name == "Set-Cookie" else "request"}
            bad = dict(good, field=spelling)
            yield name.lower() + "--spelling-" + str(index), good, bad


def field_report(base, spec):
    doc = copy.deepcopy(base)
    entry = {"key": spec["field"], "value": spec["value"]}
    if spec["carrier"] == "header":
        target = spec["target"]
        doc["run"]["executions"][0][target]["header"].append(entry)
    else:
        doc["environment"]["values"].append(entry)
    return encode(doc)


def rendered_delta(left, right):
    """Compute changed carrier slots from actual rendered inputs, not declarations.

    A representation's envelope/byte vector is one encoded value. The independent
    input spec comparison additionally pins its codec, carrier and source text.
    ZIP compression/CRC/directory changes are derivatives, not separate facts.
    """
    changes = []

    def walk(a, b, path):
        if a == b:
            return
        if isinstance(a, dict) and isinstance(b, dict):
            if a.get("type") == b.get("type") == "Buffer" or (
                    "encoding" in a and "encoding" in b):
                changes.append(path)
                return
            removed, added = set(a) - set(b), set(b) - set(a)
            if len(removed) == len(added) == 1 and a[next(iter(removed))] == b[next(iter(added))]:
                changes.append(path + "/<key>")
            elif removed or added:
                raise AssertionError("rendered pair changed object shape")
            for key in set(a) & set(b):
                walk(a[key], b[key], path + "/" + key)
        elif isinstance(a, list) and isinstance(b, list):
            if len(a) != len(b):
                raise AssertionError("rendered pair changed list shape")
            for index, (x, y) in enumerate(zip(a, b)):
                walk(x, y, path + "/" + str(index))
        else:
            changes.append(path)

    walk(json.loads(left["report"]), json.loads(right["report"]), "report")
    for key in ("name", "log"):
        if left[key] != right[key]:
            changes.append(key)
    with zipfile.ZipFile(io.BytesIO(left["archive"])) as a, zipfile.ZipFile(io.BytesIO(right["archive"])) as b:
        for label, x, y in (("archive-comment", a.comment, b.comment),
                            ("member-comment", a.infolist()[0].comment, b.infolist()[0].comment),
                            ("member-extra", a.infolist()[0].extra, b.infolist()[0].extra)):
            if x != y:
                changes.append(label)
        for bundle, spec in ((a, left), (b, right)):
            if "manifest.json" in bundle.namelist():
                if json.loads(bundle.read("manifest.json")) != {"reports": [{"report": spec["name"]}]}:
                    raise AssertionError("legacy manifest no longer names the exact report")
    if len(changes) != 1:
        raise AssertionError("actual rendered carrier pair changed more or fewer than one slot")
    return changes
