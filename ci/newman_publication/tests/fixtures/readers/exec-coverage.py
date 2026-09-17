#!/usr/bin/env python3

# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""exec-coverage.py — EXECUTION coverage gate: did every request in the collection
actually run?

WHY (the blindness this closes)
-------------------------------
newman reports failures only for requests it EXECUTED. A request that never ran
contributes zero assertions and zero failures, so a suite that silently stopped
after 6 of its 46 requests is indistinguishable — to `.run.stats.assertions.failed`
— from a suite that ran all 46 cleanly. Both read "0 failed" and both were gated
GREEN.

That is exactly what happened: a poll pre-request guard used

    if (!pm.environment.get('<opId>')) { pm.execution.setNextRequest(null); }

intending "skip this poll when the create was rejected and no operation exists".
But `setNextRequest(null)` does not skip a request — it ENDS THE RUN. The guard
fires deterministically on negative cases (create rejected 403/400 → no op id), so
the whole remainder of the collection was dropped. The iam suite executed 1177 of
1502 requests (78.4%) while reporting every collection GREEN; the 325 dropped
requests were the authz-deny leak matrix, the ROLE/INV/USR/ESC deny matrices and
the fail-closed OpenFGA-outage case — i.e. the reason the suite exists.

`pm.execution.skipRequest()` is the primitive that skips exactly one request.

КАКИЕ ОБЛАСТИ СКРИПТОВ ЧИТАЮТСЯ (и почему это отдельный абзац)
--------------------------------------------------------------
Postman исполняет перед каждым запросом ТРИ пре-скрипта: корневой
(`collection.event`), скрипты всех объемлющих папок и собственный скрипт шага.
Эта перепись читала ТОЛЬКО третий, и `collection.event` не читался нигде.

Цена измерена, а не предположена. Замер по дереву на `dfd2c027` (предикат — в
теле задачи #661): коллекций 89, корневого стража несут 57 — vpc 18/18, nlb 9/9,
storage 9/9, compute 7/7, geo 7/7, registry 5/5, gateway 2/2 и **iam 0/32**. Гейт
писался в iam, где этой формы нет ни в одной коллекции, поэтому слепота пережила
и обзор, и инъекцию: доказательство существовало и не касалось той области, в
которой страж применяется чаще всего.

Следствий было два, и второе тише первого:

  * ЛОЖНЫЕ СРАБАТЫВАНИЯ. Шаг, пропущенный корневым стражем по предписанной этим
    же гейтом форме, метился находкой `ASSERTED-NOT-EXECUTED`. На прогоне
    32077150454 (шард nlb) — 157 находок, при том что в той же сводке стояло
    `SKIPS: 0 RECORDED-SKIP`: о записанных пропусках гейт отчитался нулём, имея
    их все 157. Перечень такой длины перестают читать, а перестав читать,
    возвращаются к вердикту по коду возврата;
  * СЛЕПАЯ ЗОНА ЗАПРЕТОВ. Статические запреты читали те же элементные скрипты,
    поэтому для 57 коллекций из 89 не проверялись ВООБЩЕ. Маска, внесённая в
    корневой скрипт, гейтом не ловилась — при том что маски запрещены директивой
    владельца.

Теперь: запреты судят КАЖДУЮ область отдельно (утверждение из корня не делает
«слышимым» немой пропуск элемента); классификатор записанного пропуска читает
ЭФФЕКТИВНЫЙ пре-скрипт (корень + папки + элемент); объём прочитанного печатается
строкой `SCOPES:` — по областям и всегда, включая нули, иначе «ноль находок»
снова станет неотличимо от «ноль прочитанного».

ЧТО СЮДА НАМЕРЕННО НЕ СЛОЖЕНО. Корневой `skipRequest()` НЕ засчитывается в
«объяснённые пропуски» (`_explained_skippable`). Он исполняется перед каждым
запросом, поэтому такой зачёт объявил бы объяснённым ЛЮБОЕ усечение — то есть
убил бы класс, ради которого перепись заведена, разом в 57 коллекциях. Корень
читается там, где вывод делается ПО ОТЧЁТУ: он даёт шаблон имени, а извиняет шаг
только фактически упавшее утверждение стража.

ИМЯ УТВЕРЖДЕНИЯ СОБИРАЕТСЯ КОНКАТЕНАЦИЕЙ, и сверка это выдерживает. Стражи дерева
пишут `pm.test('предусловие: {{' + _n + '}} …')` и `pm.test('FIXTURE REQUIRED: ' +
__missing.join(', '))`. Прежний разбор вынимал первый ЛИТЕРАЛ и сравнивал его с
именем из отчёта целиком, поэтому не сошёлся бы ни с одним из них даже после того,
как страж прочитан. Теперь из выражения строится шаблон: литеральные члены —
дословно, вычисляемые — подстановкой; выражение БЕЗ единого непустого литерала
шаблона не даёт вовсе (иначе он совпал бы с чем угодно — амнистия).

СКРИПТ ЧИТАЕТСЯ РАЗБОРОМ, А НЕ КАК ТЕКСТ. Комментарий — не код: без этого запрет
краснел бы на объяснении, которое он сам предписывает держать рядом с собой.
Упоминание запрета внутри строкового литерала — тоже не вызов (наши генераторы
пишут в имена утверждений длинную прозу). Последовательность `//` внутри
регулярного выражения — код, а не начало комментария.

WHAT THIS CHECKS
----------------
Per collection, two independent things:

1. STATIC BANS
   a. no script may contain `setNextRequest(null)`. It is never the right
      primitive inside a per-request guard, and its damage is invisible to the
      assertion-based verdict, so it is banned outright rather than merely
      detected through its effects.
   b. no ENVIRONMENT guard may skip SILENTLY. A guard that skips because a
      harness variable is missing must carry an assertion, so the missing
      variable is reported instead of quietly deleting checks. See below.

   THE SECOND-ORDER BLIND SPOT (why 1b exists). Skipping is the sanctioned fix
   for 1a — but a skip leaves NO trace either: no execution record, no assertion,
   no failure. So a guard that fires for the WRONG REASON still passes, and this
   gate cannot see it, because "explained by a skipRequest() guard" is exactly
   what it was taught to accept. Two guards, three identical lines, opposite
   meanings:

     if (!opId)            skipRequest()  → LEGAL. The create under test was
                                            refused on purpose; there is no
                                            operation to poll and nothing to
                                            assert. Nothing is lost.
     if (!internalBaseUrl) skipRequest()  → BROKEN HARNESS. The check is still
                                            meaningful and still expected to run;
                                            the runner merely failed to inject
                                            the variable (--env-var).

   Losing one variable would silently remove 31 authorization checks from a
   single collection (label-revoke-vpc) and the suite would still report GREEN —
   the same blindness as the truncation above, one level down. Hence: an
   environment guard must FAIL, not merely skip. The sanctioned shape is
   gen.py::require_env_url — assert (naming the variable), then skip.

   THE MIRROR HALF LIVES ELSEWHERE, AND ON PURPOSE. 1b catches a guard that skips
   MUTELY. The opposite deviation — a guard that asserts and SENDS ANYWAY — is
   invisible to this gate by construction: the step executed and answered, so
   coverage is satisfied while every OTHER assertion of that step was scored
   against whatever the step became without its missing input (without a bearer:
   an ANONYMOUS caller, i.e. a different principal). That half is held statically
   by `internal/repohygiene/artifactgates/newmanprerequestguard_test.go` — a pre-request
   assertion must be paired with `pm.execution.skipRequest()` in the same branch.

2. EXECUTION COVERAGE — every leaf request must either have EXECUTED or be
   statically EXPLAINED as skippable. A request is explained when:
     a. its pre-request script calls `pm.execution.skipRequest()` (an explicit,
        reviewable opt-out — the conditional-poll guards), or
     b. some earlier request can jump over it via a literal
        `pm.execution.setNextRequest('<name>')` targeting a later request (the
        pre-clean / branch jumps in rbac-visibility-set, iam-flat-authz-vbc, ...).

   Anything else that did not run is UNEXPLAINED and fails the suite. Truncation
   (the whole tail vanishing) can never be explained, so this catches the entire
   class, not just the one idiom that caused it.

3. ANSWERED — a request that newman attempted but that got NO RESPONSE did not
   execute, whatever its cursor says.

   THE THIRD STATE (why 2 alone was not enough). A request ends one of three
   ways: it is ANSWERED (a response arrived, any status), an assertion FAILED,
   or it is UNANSWERED — nothing came back. Only the first two had a home.
   newman records an execution for a transport failure too — same cursor
   position, but `response: null` and a `requestError` (DNS, refused connection,
   TLS, timeout) — so check 2 above counted it as executed. Meanwhile its test
   script still runs, against an empty response, where `pm.response.code` is
   `undefined` and an assertion phrased "if the code is undefined, return" PASSES.
   And the last honest signal, `.run.stats.requests.failed`, was being SUBTRACTED
   by the suite gate as DNS noise.

   All three layers agreed that nothing was wrong. Eight ban-#6 negatives — the
   ones asserting that Internal* RPCs are not reachable on the advertised
   external endpoint — never ran, and the verdict read "0 failed requests".

   So: an ANSWER is the evidence of execution, not a cursor position. A skip
   guard explains a request that did not run; it does not excuse one that ran
   and got nothing back.

Matching is POSITIONAL: generated collections carry no item `id` (newman mints one
per run), but every execution records `cursor.position` — the index of the item in
the flattened collection order. `cursor.length` is cross-checked against the leaf
count so a report can never be silently paired with a different collection.

Self-loop retries (`setNextRequest(pm.info.requestName)`) inflate
`.run.stats.requests.total` far beyond the item count, which is why raw request
totals are NOT a usable signal here — distinct executed positions are.

WHOSE REPORTS THESE ARE (the third state of the GATE, not of a request)
----------------------------------------------------------------------
The coverage half above is computed from `out/<name>.json` — a run artifact this
gate does NOT produce and git does not track (`services/iam/tests/newman/.gitignore`
names `out/` and records why: a leftover `authz-deny-rerun.json` from a local run
once resurfaced as a 511-failure phantom suite in CI). So the input is whatever
happens to be lying in that directory, and it can be from another run, another
branch, or no run at all.

Both of those used to reach the verdict as if they were evidence:

  * `out/` empty  → every collection returned before the coverage checks and the
    run ended on "OK: every request either executed or is an explained skip." —
    an affirmative about 1658 requests of which zero had been looked at. The
    per-collection "NO REPORT" line was printed and then dropped on the floor:
    the TOTAL line is scraped from "executed N/M" text, which "NO REPORT" does
    not match, so it did not print either.
  * `out/` holding another tree's reports → the coverage checks ran against them
    and named collections RED. A red verdict about a tree the reports do not
    describe is worse than no verdict: it sends someone to debug a suite that
    was never in question.

Measured on this gate, tree fixed, only `--out-dir` swapped: exit 0 with an empty
directory, exit 1 with a five-day-old directory from another commit. The verdict
was a function of the artifact directory alone.

A run either happened or it did not, and "it did not" is NEITHER green NOR red —
it is a third category that must not be spent on either side. Hence:

  * a collection with no report is ABSTAINED, not passed;
  * a report whose collection does not match the one on disk — compared by the
    fingerprint of the flattened leaf (folder, name) sequence, not merely by leaf
    COUNT, which the old cross-check used and which two different revisions of the
    same suite routinely share — is ABSTAINED, not failed;
  * the census (how many judged, how many abstained, and why) prints on every run,
    so "nothing found" cannot read the same as "nothing examined";
  * the affirmative sentence is emitted only when something was actually judged.

The static bans keep applying in every case: they read the tracked collection,
which IS this tree, so they need no run to be true.

Usage:
    python3 exec-coverage.py [--collections-glob GLOB] [--out-dir DIR]
Exit: 0 = every collection judged and fully accounted for;
      1 = ban hit or unexplained gaps;
      2 = no collections matched (usage);
      3 = DID NOT RUN — no usable report for at least one collection, so the
          coverage question was not answered. Not a pass and not a failure.
"""
from __future__ import annotations

import argparse
import glob
import hashlib
import json
import os
import re
import sys

# `setNextRequest(null)` / `setNextRequest( null )`, on pm.execution or the legacy
# `postman.` alias. Banned: it ends the run instead of skipping a request.
_BAN_RE = re.compile(r"setNextRequest\(\s*null\s*\)")

# Explicit single-request skip — the sanctioned opt-out.
_SKIP_RE = re.compile(r"skipRequest\s*\(")

# A read of a harness-supplied endpoint variable: pm.environment.get('internalBaseUrl'),
# pm.variables.get('externalBaseUrl'), ... A guard keyed on one of these is an
# ENVIRONMENT guard (misconfigured harness), not an operation guard (legal skip).
_ENV_URL_VAR_RE = re.compile(r"""(?:environment|variables|globals)\.get\(\s*['"](\w*[Bb]aseUrl)['"]\s*\)""")

# Any assertion at all in the same script — that is what makes a skip audible.
_ASSERT_RE = re.compile(r"pm\.test\s*\(")

# Literal forward jump: setNextRequest('name') / setNextRequest("name").
# The dynamic self-loop form setNextRequest(pm.info.requestName) has no quotes and
# is deliberately not matched (it re-runs the SAME request, skipping nothing).
_JUMP_RE = re.compile(r"setNextRequest\(\s*(['\"])(.*?)\1\s*\)")

_MAX_REPORTED = 12  # cap the per-collection offender list in the output


def _iter_leaves(items):
    """Postman items nest (folders). Yield leaf items (those with `.request`) in
    the same depth-first order newman executes them."""
    for it in items:
        if "request" in it:
            yield it
        if "item" in it:
            yield from _iter_leaves(it["item"])


def _iter_leaf_parents(items, parent=""):
    """The enclosing folder name of each leaf, in the SAME order as `_iter_leaves`.

    newman's failure records identify a step by the pair (`parent.name`,
    `source.name`) — folder and item — because item names repeat across folders.
    Positional matching needs the same pair, so the folder is tracked separately
    rather than changing what `_iter_leaves` yields.
    """
    for it in items:
        if "request" in it:
            yield parent
        if "item" in it:
            yield from _iter_leaf_parents(it["item"], it.get("name", ""))


def _scripts(item, listen=None):
    """Concatenated script source of an item, optionally only for one event type
    ('prerequest' | 'test')."""
    out = []
    for ev in item.get("event", []) or []:
        if listen is not None and ev.get("listen") != listen:
            continue
        exec_ = (ev.get("script") or {}).get("exec") or []
        if isinstance(exec_, str):
            out.append(exec_)
        else:
            out.extend(str(x) for x in exec_)
    return "\n".join(out)


def _leaf_fingerprint(items):
    """Identity of a collection as a RUN would see it: the flattened leaf sequence.

    Returns (fingerprint, leaf_count). The fingerprint is over the (folder, name)
    pair of every leaf in execution order, which is what makes it able to tell two
    revisions of the same suite apart — renaming, reordering or replacing a step
    all move it, while the leaf COUNT (the only thing the previous cross-check
    compared) survives all three. That count-only check let 18 of 29 stale reports
    through in a live measurement, and the 11 it did catch it filed as RED.

    Deliberately NOT hashed: request URLs, script bodies, variables. A report is
    "from this collection" if it ran these steps in this order; an edit to a script
    body is a different question and belongs to the static bans, which read the
    tracked file directly.
    """
    h = hashlib.sha256()
    n = 0
    for leaf, parent in zip(_iter_leaves(items), _iter_leaf_parents(items)):
        h.update(parent.encode("utf-8"))
        h.update(b"\x00")
        h.update((leaf.get("name") or "").encode("utf-8"))
        h.update(b"\x00")
        n += 1
    return h.hexdigest()[:16], n


def _transport_error(ex):
    """Return a short description of why this execution got no response, or None
    if a response arrived.

    newman's JSON reporter writes, for a request that died before an HTTP
    exchange completed, `response: null` plus a `requestError` object carrying the
    Node-level cause, e.g.

        {"errno": -3008, "code": "ENOTFOUND", "syscall": "getaddrinfo",
         "hostname": "api.kacho.local"}

    Both signals are checked: `requestError` is the explicit one, and a null
    response is the invariant it implies. Either is enough — an execution without
    a response did not answer, whatever else the report says about it.
    """
    err = ex.get("requestError")
    if err is None and ex.get("response") is not None:
        return None
    if isinstance(err, dict):
        code = err.get("code") or err.get("errno") or "request error"
        host = err.get("hostname") or err.get("address") or ""
        syscall = err.get("syscall") or ""
        detail = " ".join(x for x in (str(code), syscall, host) if x)
        return detail or "request error"
    if isinstance(err, str) and err:
        return err
    return "no response"


# Вызовы, которыми страж ОБЪЯВЛЯЕТ пропуск:
#   pm.test('<имя>', () => { pm.expect.fail('<текст>'); })
# Ими различается «пропуск, ЗАПИСАННЫЙ утверждением стража» и «шаг, чьи проверки
# исполнились, а записи об исполнении нет».
#
# Здесь стоял разбор ЛИТЕРАЛА: `pm\.test\(\s*(['"])(.*?)\1`. Он вынимал первую
# строку и сравнивал её с именем из отчёта целиком — то есть работал ровно на тех
# стражах, чьё имя литералом и является. Все корневые стражи дерева собирают имя
# КОНКАТЕНАЦИЕЙ, поэтому с ними он не сошёлся бы никогда, даже будучи прочитан.
# Теперь берётся аргумент вызова целиком (`_argument_source`) и из него строится
# шаблон (`_expression_pattern`).
_TEST_CALL_RE = re.compile(r"pm\.test\s*\(")
_FAIL_CALL_RE = re.compile(r"pm\.expect\.fail\s*\(")
# ─────────────────────────────────────────────────────────────────────────────
# ЧТЕНИЕ СКРИПТА: код, строковый литерал, регулярное выражение, комментарий
# ─────────────────────────────────────────────────────────────────────────────
#
# Гейт читает ИСПОЛНЯЕМУЮ часть, а не текст. Пока областью чтения были только
# элементные скрипты, разница была почти незаметна: их пишет генератор и прозы в
# них мало. С корневыми скриптами она становится решающей — там живут объяснения
# рядом с самим запретом, и запрет по сырому тексту краснел бы на комментарии,
# который его же и предписывает. Такой запрет снимают первым.
#
# Отсюда ОДИН разборщик и ДВЕ проекции, потому что вопросы разные:
#   `_code`  — комментарии убраны, СТРОКИ НА МЕСТЕ. Отвечает «какой литерал сюда
#              передали» (имя утверждения, цель перехода, имя переменной);
#   `_calls` — комментарии убраны и тела строк выпотрошены. Отвечает «вызывается
#              ли здесь X». Упоминание запрета внутри прозы — не вызов: наши
#              генераторы пишут в имена утверждений и в тексты провалов длинные
#              объяснения, и по сырому тексту запрет краснел бы на тексте о себе.
#
# Регулярное выражение распознаётся отдельно: последовательность `//` внутри него
# — это код, а не начало комментария. Без этого строка с регуляркой съедала бы
# собственный остаток, и вызов за ней стал бы невидим.
_REGEX_KEYWORDS = frozenset(
    "return typeof case in of new delete void do else yield await instanceof".split()
)


def _skip_string(src, i):
    """Индекс сразу за закрывающей кавычкой строки, начинающейся в `i`."""
    q = src[i]
    j = i + 1
    n = len(src)
    while j < n:
        if src[j] == "\\":
            j += 2
            continue
        if src[j] == q:
            return j + 1
        j += 1
    return n


def _regex_here(last_sig, last_word):
    """Может ли `/` в этой позиции начинать регулярное выражение (а не деление)."""
    if not last_sig:
        return True
    if last_word:
        return last_word in _REGEX_KEYWORDS
    return last_sig in "(,=:[!&|?{};+-*%<>~^"


def _tokenize(src):
    """[(вид, текст)] — вид ∈ {code, string, regex, comment}."""
    out = []
    buf = []
    i, n = 0, len(src)
    last_sig = ""
    last_word = ""

    def flush():
        if buf:
            out.append(("code", "".join(buf)))
            del buf[:]

    while i < n:
        c = src[i]
        if c == "/" and i + 1 < n and src[i + 1] == "/":
            flush()
            j = src.find("\n", i)
            j = n if j < 0 else j
            out.append(("comment", src[i:j]))
            i = j
            continue
        if c == "/" and i + 1 < n and src[i + 1] == "*":
            flush()
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append(("comment", src[i:j]))
            i = j
            continue
        if c in "'\"`":
            flush()
            j = _skip_string(src, i)
            out.append(("string", src[i:j]))
            last_sig, last_word = c, ""
            i = j
            continue
        if c == "/" and _regex_here(last_sig, last_word):
            j, in_class, closed = i + 1, False, False
            while j < n:
                d = src[j]
                if d == "\\":
                    j += 2
                    continue
                if d == "\n":
                    break
                if d == "[":
                    in_class = True
                elif d == "]":
                    in_class = False
                elif d == "/" and not in_class:
                    j += 1
                    closed = True
                    break
                j += 1
            if closed:
                flush()
                out.append(("regex", src[i:j]))
                last_sig, last_word = "/", ""
                i = j
                continue
        buf.append(c)
        if not c.isspace():
            last_sig = c
            last_word = (last_word + c) if (c.isalnum() or c in "_$") else ""
        i += 1
    flush()
    return out


def _code(src):
    """Скрипт без комментариев, строковые литералы сохранены + маска строк.

    Маска нужна, чтобы поиск ВЫЗОВА не срабатывал внутри литерала: строка
    `'зови pm.test(…)'` содержит текст вызова и вызовом не является.
    """
    parts = []
    mask = bytearray()
    for kind, text in _tokenize(src):
        if kind == "comment":
            continue
        parts.append(text)
        mask.extend(b"\x01" * len(text) if kind == "string" else b"\x00" * len(text))
    return "".join(parts), mask


def _calls(src):
    """Скрипт без комментариев, тела строковых литералов выпотрошены."""
    parts = []
    for kind, text in _tokenize(src):
        if kind == "comment":
            continue
        parts.append(text[0] + text[-1] if kind == "string" and len(text) >= 2 else text)
    return "".join(parts)


# ─────────────────────────────────────────────────────────────────────────────
# ИМЯ УТВЕРЖДЕНИЯ, СОБРАННОЕ КОНКАТЕНАЦИЕЙ
# ─────────────────────────────────────────────────────────────────────────────
#
# Стражи дерева называют утверждение не литералом, а склейкой:
#   pm.test('предусловие: {{' + _n + '}} не было захвачено — запрос не отправлен', …)
#   pm.test('FIXTURE REQUIRED: ' + __missing.join(', '), …)
# Сверка «литерал == имя из отчёта» не сошлась бы ни с одним из них даже после
# того, как страж ПРОЧИТАН: она вынимает первый литерал и сравнивает с целым.
#
# Поэтому из выражения строится ШАБЛОН: литеральные члены — дословно, остальные —
# подстановкой. Это не ослабление: `.*` появляется ровно там, где исходник
# действительно склеивает вычисляемое, и нигде больше. Выражение БЕЗ единого
# непустого литерала шаблона не даёт вовсе — иначе он совпал бы с чем угодно, то
# есть страж превратился бы в амнистию (fail-closed).
_ESCAPES = {"n": "\n", "t": "\t", "r": "\r", "b": "\b", "f": "\f", "v": "\v",
            "0": "\0", "\\": "\\", "'": "'", '"': '"', "`": "`", "/": "/"}


def _unescape(s):
    out = []
    i, n = 0, len(s)
    while i < n:
        if s[i] == "\\" and i + 1 < n:
            nxt = s[i + 1]
            if nxt == "u" and i + 5 < n + 1:
                try:
                    out.append(chr(int(s[i + 2:i + 6], 16)))
                    i += 6
                    continue
                except ValueError:
                    pass
            if nxt == "x" and i + 3 < n + 1:
                try:
                    out.append(chr(int(s[i + 2:i + 4], 16)))
                    i += 4
                    continue
                except ValueError:
                    pass
            out.append(_ESCAPES.get(nxt, nxt))
            i += 2
            continue
        out.append(s[i])
        i += 1
    return "".join(out)


def _argument_source(code, open_paren):
    """Текст ПЕРВОГО аргумента вызова, чья `(` стоит в позиции `open_paren`."""
    depth = 0
    j, n = open_paren, len(code)
    start = open_paren + 1
    while j < n:
        c = code[j]
        if c in "'\"`":
            j = _skip_string(code, j)
            continue
        if c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
            if depth == 0:
                return code[start:j]
        elif c == "," and depth == 1:
            return code[start:j]
        j += 1
    return None


def _split_concatenation(expr):
    """Члены выражения по верхнеуровневому `+` (строки не разрезаются)."""
    terms, buf = [], []
    depth = 0
    i, n = 0, len(expr)
    while i < n:
        c = expr[i]
        if c in "'\"`":
            j = _skip_string(expr, i)
            buf.append(expr[i:j])
            i = j
            continue
        if c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
        elif c == "+" and depth == 0:
            terms.append("".join(buf))
            buf = []
            i += 1
            continue
        buf.append(c)
        i += 1
    terms.append("".join(buf))
    return terms


def _term_pattern(term):
    """(кусок регулярного выражения, это_литерал) для одного члена склейки."""
    t = term.strip()
    if len(t) >= 2 and t[0] in "'\"" and _skip_string(t, 0) == len(t) and t[-1] == t[0]:
        lit = _unescape(t[1:-1])
        return re.escape(lit), lit
    if len(t) >= 2 and t[0] == "`" and _skip_string(t, 0) == len(t) and t[-1] == "`":
        body = t[1:-1]
        out, lit_seen = [], ""
        i, n = 0, len(body)
        chunk = []
        while i < n:
            if body[i] == "\\" and i + 1 < n:
                chunk.append(body[i:i + 2])
                i += 2
                continue
            if body[i] == "$" and i + 1 < n and body[i + 1] == "{":
                lit = _unescape("".join(chunk))
                chunk = []
                if lit:
                    out.append(re.escape(lit))
                    lit_seen = lit_seen or lit
                depth = 1
                i += 2
                while i < n and depth:
                    if body[i] == "{":
                        depth += 1
                    elif body[i] == "}":
                        depth -= 1
                    i += 1
                out.append(".*")
                continue
            chunk.append(body[i])
            i += 1
        lit = _unescape("".join(chunk))
        if lit:
            out.append(re.escape(lit))
            lit_seen = lit_seen or lit
        return "".join(out), lit_seen
    return ".*", ""


def _expression_pattern(expr, anchored_end):
    """Скомпилированный шаблон имени/текста, либо None (нечего сверять)."""
    parts, first_literal = [], ""
    for term in _split_concatenation(expr):
        piece, literal = _term_pattern(term)
        if literal and not first_literal:
            first_literal = literal
        parts.append(piece)
    if not first_literal:
        return None  # ни одного непустого литерала — шаблон был бы «что угодно»
    body = re.sub(r"(?:\.\*){2,}", ".*", "".join(parts))
    return re.compile("^" + body + ("$" if anchored_end else ""), re.S), first_literal


def _guard_patterns(script_src):
    """(шаблоны имён pm.test, шаблоны текстов pm.expect.fail) этого скрипта."""
    code, mask = _code(script_src)
    names, fails = [], []
    for m in _TEST_CALL_RE.finditer(code):
        if mask[m.start()]:
            continue
        expr = _argument_source(code, m.end() - 1)
        if expr is None:
            continue
        got = _expression_pattern(expr, True)
        if got:
            names.append(got[0])
    for m in _FAIL_CALL_RE.finditer(code):
        if mask[m.start()]:
            continue
        expr = _argument_source(code, m.end() - 1)
        if expr is None:
            continue
        got = _expression_pattern(expr, False)
        if got:
            fails.append(got)
    return names, fails


# ─────────────────────────────────────────────────────────────────────────────
# ОБЛАСТИ СКРИПТОВ: КОРЕНЬ КОЛЛЕКЦИИ И ПАПКИ, А НЕ ТОЛЬКО ЭЛЕМЕНТ
# ─────────────────────────────────────────────────────────────────────────────
#
# Postman исполняет перед каждым запросом ТРИ пре-скрипта: корневой
# (`collection.event`), скрипты всех объемлющих папок и собственный скрипт шага.
# Перепись читала только третий. Замер по дереву (`dfd2c027`): коллекций 89, с
# корневым стражем — 57 (vpc 18/18, nlb 9/9, storage 9/9, compute 7/7, geo 7/7,
# registry 5/5, gateway 2/2, iam 0/32). Гейт писался в iam, где этой формы нет
# вовсе, поэтому слепота пережила и обзор, и инъекцию.
def _iter_script_scopes(col):
    """[(метка, узел)] для КАЖДОЙ области скриптов: корень + каждая папка."""
    scopes = [("корень коллекции", col)]

    def walk(items, path):
        for it in items:
            if "item" in it:
                nm = it.get("name", "?")
                here = f"{path}/{nm}" if path else nm
                scopes.append((f"папка {here!r}", it))
                walk(it["item"], here)

    walk(col.get("item", []), "")
    return scopes


def _iter_leaf_inherited(col, listen="prerequest"):
    """Унаследованный скрипт каждого листа (корень + объемлющие папки).

    Порядок — тот же, что у `_iter_leaves`, потому что обход тот же.
    """
    out = []

    def walk(items, acc):
        for it in items:
            if "request" in it:
                out.append(acc)
            if "item" in it:
                walk(it["item"], acc + "\n" + _scripts(it, listen))

    walk(col.get("item", []), _scripts(col, listen))
    return out


def _recorded_skip_guards(leaves, inherited_pre=None):
    """Индексы шагов, чей ЭФФЕКТИВНЫЙ пре-скрипт несёт САНКЦИОНИРОВАННУЮ форму
    стража: «утвердить, назвав переменную, и только потом пропустить».

    Эффективный пре-скрипт = корень коллекции + объемлющие папки + собственный
    скрипт шага, в том порядке, в котором их исполняет newman. Раньше читался
    только третий — а в 57 коллекциях дерева из 89 страж живёт в первом.

    Возвращает {index: (шаблоны имён, шаблоны текстов провала)} — по ним ниже
    сверяется, что упавшее утверждение шага принадлежит ИМЕННО стражу.

    ЗАЧЕМ ОТДЕЛЬНАЯ КАТЕГОРИЯ. Форма «утвердить → skipRequest()» объявлена
    правильной этим же гейтом (шапка, п. 2б) и уже применяется дважды —
    `gen.py::require_env_url` и `gen.py::_op_id_guard(required=True)`. Но
    классификатор относил КАЖДЫЙ такой шаг к ASSERTED-NOT-EXECUTED, то есть к
    находке: страж утвердил (шаг «asserted») и не отправился (записи об
    исполнении нет). Гейт наказывал ровно ту форму, которую сам предписывает, а
    его текст находки — «a skip guard cannot explain a step that asserted» — для
    этого приёма был неверен. Долг записан замером 2026-07-31: 135 таких шагов в
    75 различимых местах, и он назван открытым в приёмке IAM-INT-1 (Р10).

    ЧЕМ ЭТО НЕ АМНИСТИЯ. Извиняется не «есть страж», а «упало ИМЕННО утверждение
    стража». Шаг, у которого провалилось утверждение тест-скрипта, ИСПОЛНЯЛСЯ —
    тест-скрипт не выполняется без ответа, — и он остаётся находкой, даже если
    страж в пре-скрипте у него есть. Именно эту половину класса гейт и заводился
    ловить (пропавшие записи об исполнении в executions). Корневой страж эту
    границу не сдвигает: он даёт шаблон имени, а решает по-прежнему отчёт.
    """
    guards = {}
    for i, it in enumerate(leaves):
        own = _scripts(it, "prerequest")
        pre = (inherited_pre[i] + "\n" + own) if inherited_pre else own
        calls = _calls(pre)
        if not (_SKIP_RE.search(calls) and _ASSERT_RE.search(calls)):
            continue
        names, fails = _guard_patterns(pre)
        if not names and not fails:
            continue  # объявить нечем — сверять не с чем (fail-closed)
        guards[i] = (names, fails)
    return guards


def _failure_belongs_to_guard(failure, names, fails):
    """Принадлежит ли упавшее утверждение самому стражу.

    `names` — шаблоны имён, привязанные с обоих концов: имя из отчёта обязано
    отвечать шаблону ЦЕЛИКОМ, а не начинаться с него. `fails` — пары (шаблон без
    правой привязки, первый литерал): текст провала chai дописывает справа,
    поэтому там сверка префиксная.

    FAIL-CLOSED: если отчёт не позволяет установить, ЧТО именно упало, шаг НЕ
    извиняется. Неустановимое остаётся находкой — иначе «объяснённый пропуск»
    стал бы корзиной «всё остальное», которой не бывает.
    """
    err = failure.get("error") or {}
    test = (err.get("test") or "").strip()
    if test:
        return any(p.match(test) for p in names)
    msg = (err.get("message") or "").strip()
    if msg:
        for pat, literal in fails:
            if pat.match(msg):
                return True
            if literal and literal.startswith(msg):
                return True  # отчёт обрезал сообщение стража
    return False


def _explained_skippable(leaves):
    """Indices that are allowed to not execute, with the reason.

    (a) explicit `skipRequest()` in the item's OWN pre-request script;
    (b) jumped over by a literal forward `setNextRequest('<later item>')`.
    Both are static over-approximations of newman's runtime control flow — they
    never mask a truncation, because a truncated tail has no jump pointing past
    it and no skip guard of its own.

    КОРНЕВОЙ И ПАПОЧНЫЙ ПРОПУСК СЮДА НЕ СКЛАДЫВАЕТСЯ — НАМЕРЕННО, и это не
    недосмотр расширения. Корневой пре-скрипт исполняется перед КАЖДЫМ запросом,
    поэтому засчитать его `skipRequest()` в «объяснённые пропуски» значило бы
    объявить объяснённым ЛЮБОЕ усечение: у пропавшего хвоста появился бы страж,
    которого у него нет. Это ровно тот класс, ради которого перепись заведена, и
    он умер бы разом в 57 коллекциях из 89.
    Корень читается там, где вывод делается ПО ОТЧЁТУ, а не по форме кода:
    `_recorded_skip_guards` даёт шаблон имени, а извиняет шаг только фактически
    упавшее утверждение стража. Немой корневой пропуск остаётся UNEXPLAINED.
    """
    by_name = {}
    for i, it in enumerate(leaves):
        by_name.setdefault(it.get("name", ""), i)  # newman resolves to the FIRST match

    reasons = {}
    for i, it in enumerate(leaves):
        if _SKIP_RE.search(_calls(_scripts(it, "prerequest"))):
            reasons[i] = "skipRequest() guard"

    for i, it in enumerate(leaves):
        code, mask = _code(_scripts(it))
        for m in _JUMP_RE.finditer(code):
            if mask[m.start()]:
                continue  # текст перехода внутри литерала — не переход
            target = m.group(2)
            j = by_name.get(target)
            if j is None or j <= i + 1:
                continue  # unknown target, self, or adjacent → nothing skipped
            for k in range(i + 1, j):
                reasons.setdefault(k, f"jumped over by {it.get('name','?')!r} → {target!r}")
    return reasons


def check_collection(col_path, out_dir, census=None):
    """Returns (ok, judged, coverage_line, [problem lines]).

    `census` — необязательный словарь-счётчик ОБЪЁМА ОСМОТРЕННОГО: сколько
    областей скриптов прочитано (корней, папок, элементов) и сколько из них
    несут стража. Без этой величины «ноль находок» неотличимо от «ноль
    прочитанного» — а именно так и выглядела слепота к корневым скриптам: гейт
    молчал не потому, что нарушений нет, а потому, что 57 коллекций из 89 он в
    этой части не читал вовсе.
    """
    name = os.path.basename(col_path).replace(".postman_collection.json", "")
    problems = []
    with open(col_path, encoding="utf-8") as fh:
        col = json.load(fh)
    leaves = list(_iter_leaves(col.get("item", [])))
    parents = list(_iter_leaf_parents(col.get("item", [])))
    inherited = _iter_leaf_inherited(col)
    total = len(leaves)

    # ── ОБЛАСТИ СКРИПТОВ: корень + папки + элементы ──────────────────────────
    # Статические запреты судят КАЖДУЮ область отдельно, а не их склейку: страж
    # объявляется утверждением В ТОМ ЖЕ скрипте, где пропускает, поэтому
    # утверждение из корня не вправе делать «слышимым» немой пропуск элемента.
    scopes = [(lbl, _scripts(node), _scripts(node, "prerequest"))
              for lbl, node in _iter_script_scopes(col)]
    scopes += [(repr(it.get("name", "?")), _scripts(it), _scripts(it, "prerequest"))
               for it in leaves]

    if census is not None:
        census["collections"] += 1
        folders = len(_iter_script_scopes(col)) - 1
        census["folders"] += folders
        census["leaves"] += total
        if _scripts(col).strip():
            census["roots_with_script"] += 1
        for lbl, allsrc, _pre in scopes[1:1 + folders]:
            if allsrc.strip():
                census["folders_with_script"] += 1
        for it in leaves:
            if _scripts(it).strip():
                census["leaves_with_script"] += 1
        # «Унаследованный страж» считается по ПУТИ до листа (корень + его папки),
        # а не по объединению всех папок сразу: иначе пропуск из одной ветки и
        # утверждение из другой сложились бы в стража, которого нет ни на одном
        # пути, и число говорило бы не то, что называет.
        for inh in inherited:
            calls = _calls(inh)
            if _SKIP_RE.search(calls) and _ASSERT_RE.search(calls):
                census["inherited_guard_collections"] += 1
                break

    # (1a) static ban — setNextRequest(null) ends the run
    for where, allsrc, _pre in scopes:
        if _BAN_RE.search(_calls(allsrc)):
            problems.append(
                f"  BANNED setNextRequest(null) in {where} — it ENDS THE RUN, "
                f"it does not skip a request. Use pm.execution.skipRequest()."
            )

    # (1b) static ban — an environment guard that skips without saying so
    silent = []
    for where, _allsrc, src in scopes:
        calls = _calls(src)
        if not _SKIP_RE.search(calls):
            continue
        code, _mask = _code(src)
        env_vars = sorted(set(_ENV_URL_VAR_RE.findall(code)))
        if env_vars and not _ASSERT_RE.search(calls):
            silent.append((where, "/".join(env_vars)))
    if silent:
        problems.append(
            f"  {len(silent)} SILENT environment guard(s) — skip when a harness variable is "
            f"missing and assert NOTHING. A missing variable is a misconfigured runner, not a "
            f"legal mode: newman leaves no trace of a skipped request, so the check would "
            f"vanish and the suite would still read GREEN. Use gen.py::require_env_url — fail "
            f"naming the variable, then skip. (Operation guards `if (!opId)` are unaffected: a "
            f"create refused on purpose genuinely has nothing to poll.)"
        )
        for nm, vs in silent[:_MAX_REPORTED]:
            problems.append(f"    {nm}  [{vs}]")
        if len(silent) > _MAX_REPORTED:
            problems.append(f"    ... and {len(silent) - _MAX_REPORTED} more")

    report = os.path.join(out_dir, f"{name}.json")
    if not os.path.isfile(report):
        # No run happened for this collection, so the coverage question has no
        # answer here. ABSTAIN — never fold an unasked question into the pass
        # side. The static bans above already stand on their own.
        return (not problems, False, f"{name}: {total} items, NO REPORT (not judged)", problems)

    with open(report, encoding="utf-8") as fh:
        doc = json.load(fh)
    run = doc.get("run", {})

    # ── PROVENANCE: is this report about the collection sitting on disk? ──────
    #
    # newman's JSON reporter embeds the collection it executed. Comparing leaf
    # identity (not leaf count) answers the question the count could not: a report
    # left over from another branch of the same suite has the same number of steps
    # far more often than it has the same steps.
    want_fp, _ = _leaf_fingerprint(col.get("item", []))
    rep_col = doc.get("collection")
    if isinstance(rep_col, dict) and rep_col.get("item") is not None:
        got_fp, got_n = _leaf_fingerprint(rep_col.get("item", []))
        if got_fp != want_fp:
            return (not problems, False,
                    f"{name}: {total} items, REPORT IS NOT OF THIS COLLECTION "
                    f"(disk {want_fp}/{total} leaves vs report {got_fp}/{got_n}) — "
                    f"not judged; re-run the suite", problems)
    else:
        # Older reporter or a synthesised report: fall back to the leaf count. A
        # mismatch here is still a provenance answer, not a product answer, so it
        # abstains rather than reddening — that misfiling is what sent someone to
        # debug another tree's suite.
        lens = {ex.get("cursor", {}).get("length")
                for ex in (run.get("executions") or [])
                if isinstance(ex.get("cursor", {}).get("length"), int)}
        if lens and total not in lens:
            return (not problems, False,
                    f"{name}: {total} items, REPORT IS NOT OF THIS COLLECTION "
                    f"(report cursors say {sorted(lens)}; no embedded collection to "
                    f"compare) — not judged; re-run the suite", problems)
    executions = run.get("executions", []) or []
    stats = run.get("stats", {}) or {}
    failures = run.get("failures", []) or []

    executed = set()
    lengths = set()
    # position -> transport error, for requests newman attempted but that never
    # produced a response. These carry a cursor position like any other execution,
    # which is exactly why "position seen" cannot stand for "request executed".
    unanswered = {}
    for ex in executions:
        cur = ex.get("cursor") or {}
        pos = cur.get("position")
        if isinstance(cur.get("length"), int):
            lengths.add(cur["length"])
        if not isinstance(pos, int):
            continue
        err = _transport_error(ex)
        if err is None:
            executed.add(pos)
        else:
            # A later retry of the same position CAN succeed (self-loop polls);
            # one answered attempt is enough to call the position executed.
            unanswered.setdefault(pos, err)
    for pos in list(unanswered):
        if pos in executed:
            del unanswered[pos]

    # The count cross-check that used to live here changed SIDES rather than
    # disappearing: a report whose cursors do not describe this collection is a
    # statement about the INPUT, and filing it as a `problems` entry made it a
    # statement about the PRODUCT — which is how a five-day-old artifact directory
    # came to name twelve collections as failing in a tree it knew nothing about.
    # Positions that disagree with the collection cannot be read as coverage, so
    # the honest answer is "not judged", not "failed".
    if lengths and total not in lengths:
        return (not problems, False,
                f"{name}: {total} items, REPORT DOES NOT DESCRIBE THIS COLLECTION "
                f"(report cursors say {sorted(lengths)} leaves) — not judged; "
                f"re-run the suite", problems)

    # ── THE FOURTH STATE: asserted, but never executed ───────────────────────
    #
    # `run.executions` is NOT the complete record of what ran, and this gate walked
    # only that array. Measured on the production-posture run of 2026-07-30: an item
    # that skips its own request from the pre-request script gets NO execution record
    # at all, while its assertions still count in `run.stats` and still appear in
    # `run.failures`. In `iam-user.json` the array accounted for 121 failed assertions
    # against a summary of 137; the missing sixteen belonged to items with zero
    # execution entries. Four iam collections carried the same shape.
    #
    # For THIS gate the wrong number is the smaller half of the problem. The items
    # that disappear are precisely the guarded ones — and a guard is what this gate
    # accepts as the explanation "nothing was lost here". So a guard that fired,
    # asserted and FAILED was being filed as a legal silent skip: the gate reported
    # the opposite of what happened, in the one place it was least able to notice.
    #
    # Hence the failure list is consulted as evidence of execution. A step named there
    # RAN — whatever the array says — and it is a finding, not a skip.
    asserted_names = set()
    for f in failures:
        src = ((f.get("source") or {}).get("name") or "").strip()
        par = ((f.get("parent") or {}).get("name") or "").strip()
        if src:
            asserted_names.add((par, src))
            asserted_names.add(("", src))
    asserted = set()
    per_index_failures = {}
    if asserted_names:
        for i, (it, par) in enumerate(zip(leaves, parents)):
            nm = (it.get("name") or "").strip()
            if (par, nm) in asserted_names or ("", nm) in asserted_names:
                asserted.add(i)
                per_index_failures[i] = [
                    f for f in failures
                    if ((f.get("source") or {}).get("name") or "").strip() == nm
                ]

    reasons = _explained_skippable(leaves)
    # An unanswered request is NOT explained by a skip guard: the guard says "this
    # request did not run", the transport error says "it ran and got nothing back".
    # Folding the second into the first is how the class hid in the first place.
    guards = _recorded_skip_guards(leaves, inherited)

    # ЗАПИСАННЫЙ ПРОПУСК vs НЕМОЙ РАЗРЫВ — две разные вещи, и до сих пор они
    # считались одной. Шаг «утвердил и не исполнился» может быть:
    #   * САНКЦИОНИРОВАННЫМ стражем — пре-скрипт назвал переменную падающим
    #     утверждением и пропустил запрос. Пропуск ЗАПИСАН, счётен и виден в
    #     вердикте как упавшее утверждение. Это не находка;
    #   * НЕМЫМ разрывом — запись об исполнении пропала, а утверждения шага
    #     исполнились (тест-скрипт не выполняется без ответа). Находка, как и была.
    # Разделяет их не наличие стража, а ПРИНАДЛЕЖНОСТЬ упавшего утверждения
    # стражу; неустановимое остаётся находкой (fail-closed).
    recorded_skip, asserted_gap = [], []
    for i in range(total):
        if i in executed or i in unanswered or i not in asserted:
            continue
        g = guards.get(i)
        fl = per_index_failures.get(i) or []
        if g and fl and all(_failure_belongs_to_guard(f, g[0], g[1]) for f in fl):
            recorded_skip.append(i)
        else:
            asserted_gap.append(i)
    recorded_skip.sort()
    asserted_gap.sort()

    unexplained = [i for i in range(total)
                   if i not in executed and i not in reasons and i not in unanswered
                   and i not in asserted]
    skipped_ok = [i for i in range(total)
                  if i not in executed and i in reasons and i not in unanswered
                  and i not in asserted]

    pct = (100.0 * len(executed) / total) if total else 100.0
    # Where the numbers come from is part of the report: the left-hand ones are walked
    # from the executions array (which is incomplete by construction), the right-hand
    # ones are the run summary, which is authoritative. Printing both makes "zero
    # findings" distinguishable from "zero read", and makes a future divergence visible
    # instead of silently halving a count.
    s_assert = (stats.get("assertions") or {})
    walked_assertions = sum(len(ex.get("assertions") or []) for ex in executions)
    line = (
        f"{name}: executed {len(executed)}/{total} ({pct:.0f}%)"
        f"{f', {len(skipped_ok)} explained-skip' if skipped_ok else ''}"
        f"{f', {len(recorded_skip)} RECORDED-SKIP' if recorded_skip else ''}"
        f"{f', {len(asserted_gap)} ASSERTED-NOT-EXECUTED' if asserted_gap else ''}"
        f"{f', {len(unanswered)} UNANSWERED' if unanswered else ''}"
        f"{f', {len(unexplained)} UNEXPLAINED' if unexplained else ''}"
        f"  [summary: {s_assert.get('total', 0)} assertions,"
        f" {s_assert.get('failed', 0)} failed, {len(failures)} listed;"
        f" array walked {walked_assertions}]"
    )

    if asserted_gap:
        problems.append(
            f"  {len(asserted_gap)} step(s) ASSERTED but have no execution record — their "
            f"script ran and the run summary counts their assertions, while the executions "
            f"array does not contain them. The failure booked against each of these does NOT "
            f"belong to a precondition guard in its own pre-request script (or the report does "
            f"not say what failed, which is not an excuse either) — so this is a step whose "
            f"checks ran while its execution record vanished, NOT a recorded skip. Read the "
            f"verdict from run.stats / run.failures, and fix the step so its check either runs "
            f"or is not written:"
        )
        for i in asserted_gap[:_MAX_REPORTED]:
            problems.append(f"    [{i}] {leaves[i].get('name','?')}")
        if len(asserted_gap) > _MAX_REPORTED:
            problems.append(f"    ... and {len(asserted_gap) - _MAX_REPORTED} more")

    if unanswered:
        problems.append(
            f"  {len(unanswered)} request(s) got NO RESPONSE — attempted, but nothing came "
            f"back (DNS / refused connection / TLS / timeout). This is not a passing check "
            f"and not a skip: the check did not happen. Fix the harness so it can reach the "
            f"endpoint, or delete the check — do not let it read as green:"
        )
        for i in sorted(unanswered)[:_MAX_REPORTED]:
            problems.append(f"    [{i}] {leaves[i].get('name','?')}  <- {unanswered[i]}")
        if len(unanswered) > _MAX_REPORTED:
            problems.append(f"    ... and {len(unanswered) - _MAX_REPORTED} more")

    if unexplained:
        problems.append(
            f"  {len(unexplained)} request(s) never executed and not explained by a "
            f"skipRequest() guard or a forward setNextRequest() jump:"
        )
        for i in unexplained[:_MAX_REPORTED]:
            problems.append(f"    [{i}] {leaves[i].get('name','?')}")
        if len(unexplained) > _MAX_REPORTED:
            problems.append(f"    ... and {len(unexplained) - _MAX_REPORTED} more")

    return (not problems, True, line, problems)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--collections-glob", default="collections/*.postman_collection.json")
    ap.add_argument("--out-dir", default="out")
    # Доказательство инъекцией живёт РЯДОМ и запускается ОТСЮДА: гейт над гейтами
    # (deploy/scripts/run-gate-self-tests.sh) находит самопроверки по РАЗБОРУ
    # этого аргумента, а не по слову в тексте. Самопроверка, которую никто не
    # исполняет, — доказательство, существующее только как проза.
    ap.add_argument("--self-test", action="store_true",
                    help="доказать инъекцией: записанный пропуск отличается от немого разрыва")
    args = ap.parse_args(argv)

    if args.self_test:
        import importlib.util as _ilu
        _p = os.path.join(os.path.dirname(os.path.abspath(__file__)), "exec-coverage-selftest.py")
        if not os.path.exists(_p):
            print(f"ОТКАЗ: нет {_p} — доказывать нечем, и это не пропуск.", file=sys.stderr)
            return 2
        _s = _ilu.spec_from_file_location("exec_coverage_selftest", _p)
        _m = _ilu.module_from_spec(_s)
        _s.loader.exec_module(_m)
        return _m.main()

    cols = sorted(glob.glob(args.collections_glob))
    if not cols:
        print(f"exec-coverage: no collections matched {args.collections_glob}", file=sys.stderr)
        return 2

    bad = []
    unjudged = []
    tot_items = tot_exec = 0
    tot_recorded = tot_explained = 0
    census = {"collections": 0, "roots_with_script": 0, "folders": 0,
              "folders_with_script": 0, "leaves": 0, "leaves_with_script": 0,
              "inherited_guard_collections": 0}
    print("===== execution coverage (did every request actually run?) =====")
    for col in cols:
        ok, judged, line, problems = check_collection(col, args.out_dir, census)
        print(line)
        for p in problems:
            print(p)
        stem = os.path.basename(col).replace(".postman_collection.json", "")
        if not ok:
            bad.append(stem)
        if not judged:
            unjudged.append(stem)
        # totals for the headline number
        m = re.search(r"executed (\d+)/(\d+)", line)
        if m:
            tot_exec += int(m.group(1))
            tot_items += int(m.group(2))
        m = re.search(r"(\d+) RECORDED-SKIP", line)
        if m:
            tot_recorded += int(m.group(1))
        m = re.search(r"(\d+) explained-skip", line)
        if m:
            tot_explained += int(m.group(1))

    # The census prints on EVERY path, including the ones that used to end on a
    # bare affirmative. "Nothing found" and "nothing examined" must not read the
    # same, and the number that separates them is how many collections were judged.
    print(f"CENSUS: {len(cols)} collection(s) matched; judged {len(cols) - len(unjudged)}; "
          f"not judged {len(unjudged)} (no usable report)")
    # ОБЪЁМ ОСМОТРЕННОГО по областям скриптов. Печатается ВСЕГДА, включая нули:
    # запрет, который «ничего не нашёл», обязан быть отличим от запрета, который
    # целый вид области не читал. Строка заведена потому, что второе и было
    # правдой: корневые скрипты 57 коллекций из 89 не читались, и по выводу это
    # было неотличимо от чистого дерева.
    print(f"SCOPES: прочитано областей скриптов — корней {census['roots_with_script']}/"
          f"{census['collections']}, папок {census['folders_with_script']}/{census['folders']}, "
          f"элементов {census['leaves_with_script']}/{census['leaves']}; "
          f"коллекций с УНАСЛЕДОВАННЫМ стражем (корень/папка) — "
          f"{census['inherited_guard_collections']}")
    if tot_items:
        print(f"TOTAL executed {tot_exec}/{tot_items} ({100.0 * tot_exec / tot_items:.1f}%)")
    # ПОПУЛЯЦИЯ ОБЪЯСНЁННЫХ ПРОПУСКОВ — счётна и РАЗДЕЛЕНА на два вида, потому что
    # объясняют они разное:
    #   RECORDED-SKIP  — страж предусловия назвал переменную ПАДАЮЩИМ утверждением
    #                    и пропустил запрос. Пропуск записан: он виден в вердикте
    #                    как упавшее утверждение и потому не может пройти незаметно;
    #   explained-skip — пропуск НЕМОЙ: `skipRequest()` без утверждения либо
    #                    перепрыгнутый литеральным `setNextRequest('<имя>')`. Он
    #                    ничего не оставляет в вердикте, и это его свойство, а не
    #                    оценка.
    # Печатается ВСЕГДА, в том числе нулями: без этой строки «объяснённых пропусков
    # ноль» неотличимо от «их не считали».
    print(f"SKIPS: {tot_recorded} RECORDED-SKIP (пропуск записан падающим утверждением стража), "
          f"{tot_explained} explained-skip (немой пропуск: skipRequest без утверждения / "
          f"перепрыгнут литеральным setNextRequest)")

    if bad:
        print(f"FAIL: execution-coverage gaps in: {' '.join(bad)}", file=sys.stderr)
        return 1
    if unjudged:
        # DID NOT RUN. Not a pass: nothing here says the requests executed. Not a
        # failure either: nothing here says they did not. Spending this on either
        # side is how an empty out/ came to print an affirmative about 1658
        # requests that were never looked at.
        print(
            f"DID NOT RUN: no usable report for {len(unjudged)} of {len(cols)} collection(s): "
            f"{' '.join(unjudged)}. The coverage question was not answered — run the suite "
            f"(scripts/run.sh) so out/<name>.json belongs to THIS tree, then re-run this gate.",
            file=sys.stderr,
        )
        return 3
    print(f"OK: every request in all {len(cols)} collection(s) either executed or is an "
          f"explained skip.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
