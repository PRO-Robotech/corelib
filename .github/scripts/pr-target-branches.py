#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""pr-target-branches — процесс конвейера гонится на запрос в `main` И в ветку-номер.

ПРЕДМЕТ. Правило ветвления (решение владельца 2026-09-22): задача вливается в
ветку волны, волна — в ветку эпика, эпик — в `main`; ветки волны и эпика
называются номером задачи. Процесс, чей `pull_request` сужен до `[main]`, на
запросе в ветку-номер не начинается ВОВСЕ: у запроса ноль контекстов, и о
волне нет вердикта до запроса эпика. Так и было (#31): запрос волны #46
(`27` → `26`) влит с нулём проверок.

СВОЙСТВО — по каждому отслеживаемому процессу с событием запроса:
  1. запрос в `main` его запускает;
  2. запрос в ветку-номер его запускает (образцы NUMBER_BRANCHES);
  3. запрос в ветку НЕ-номер его НЕ запускает (образцы NON_NUMBER_BRANCHES):
     сужение шапки ci.yml намеренное, и держится оно этим пунктом;
  4. событие не сужено по путям: защита ствола требует контексты поимённо, а
     не начавшийся контекст остаётся «ожидается» и блокирует слияние навсегда;
  5. событие пускает запрос на открытии, движении и переоткрытии: `types`,
     если задан, содержит opened, synchronize и reopened (умолчание хостинга —
     ровно они). Без `opened` у запроса нет ни одного контекста, без
     `synchronize` вердикт остаётся от прежнего дерева;
  6. ни одно задание процесса не гаснет на запросе, который событие пустило, —
     ни собственным условием (см. «Условия»), ни пропуском вышестоящего по
     цепочке `needs:` (см. «Цепочка needs»).
Ключи события — закрытый словарь PR_KEYS; ключ вне него — исход 2.

ФИЛЬТР ВЫЧИСЛЯЕТСЯ, А НЕ ИЩЕТСЯ ОБРАЗЦОМ. Разбирается YAML, шаблоны ветки
читаются по правилам хостинга: `*` — любые символы, кроме `/`; `**` — любые;
`?` и `+` — ноль-или-один и один-или-больше ПРЕДЫДУЩЕГО символа; `[…]` — один
символ из перечня или диапазона (только a-z, A-Z, 0-9); `\\` экранирует; `!`
в начале — отрицание; решает последний совпавший шаблон. Форма, которой
разборщик не знает, — исход 2, а не «не совпало»: молчание на незнакомой
записи было бы зелёным без осмотра.

УСЛОВИЯ ТОЖЕ ВЫЧИСЛЯЮТСЯ — В ЗАКРЫТОМ СЛОВАРЕ. `if:` задания и шага процесса
с событием запроса разбирается грамматикой, где есть только функции статуса
(`success()`, `failure()`, `cancelled()`, `always()`), литералы `true` и
`false`, `!`, `&&`, `||` и скобки, в обёртке `${{ }}` или без. Такое условие
не зависит ни от события, ни от ветки, и гейт его ВЫЧИСЛЯЕТ на трёх путях —
зелёном, провале, отмене:
  - истинно на зелёном — исполняется на каждом запросе, который событие пустило;
  - ложно на зелёном, истинно на провале или отмене — отчётное (выгрузка на
    провале); это позволено шагу не первому и заданию с `needs:`: без
    вышестоящих другого пути, кроме зелёного, нет;
  - иначе — нарушение: пропущенное задание защита ствола засчитывает успехом,
    и зелёное пришло бы без исполнения.
Любая другая запись — обращение к контексту (`github.*`, `env.*`, `needs.*`,
индексная форма `github.event['…']`), сравнение, иная функция — исход 2
«предпосылка не держится»: такое условие гейт не вычисляет и образцом не
угадывает. Задание, зовущее процесс (`uses:`), — тоже исход 2: его заданий гейт
не видит. Условия процесса БЕЗ события запроса не судятся — на запросе его
задания не исполняются; перепись называет их числом.

Условие без функции статуса хостинг читает как `success() && …`, а задание
без `if:` — как `success()`; гейт вычисляет именно это, а не запись.

ЦЕПОЧКА needs ТОЖЕ ВЫЧИСЛЯЕТСЯ. `success()` задания истинно, только если ВСЕ
его вышестоящие по цепочке `needs:` исполнились успешно, а пропуск
вышестоящего распространяется «на все задания цепочки от точки пропуска»
(документация хостинга, jobs.<id>.needs). Отчётное задание на зелёном пути
пропущено — и гасит на зелёном пути каждое задание ниже себя, чьё условие при
пропуске вышестоящего ложно: без `if:`, `success()`, `true`,
`success() || failure()`; ниже `always()`-звена — тоже. Гейт строит граф
`needs:` (имя вне процесса, кольцо, запись не именем — исход 2), вычисляет
зелёный путь в порядке зависимостей и называет такое задание нарушением
вместе с пропущенным вышестоящим. Условие, истинное при пропуске (`always()`,
`!cancelled()`, `!failure()`), — законный близнец. Задание ниже отчётного,
чьё собственное условие ложно на зелёном и истинно на провале (`failure()`),
само отчётное и считается отчётным.

ПОВТОР КЛЮЧА — ИСХОД 2, А НЕ ВЫБОР ЗНАЧЕНИЯ. Разборщик YAML при повторе ключа
в одном отображении молча берёт последнее значение, и гейт судил бы одно из
двух (#58: второй `needs` у задания давал зелёный). До разбора обходятся узлы
документа, и повтор — в любом отображении, блочном и потоковом, в том числе
`on` рядом с `"on"` и `on` рядом с `On` — называется ключом, путём и строками.
Ключ слияния `<<` — так же: явный ключ молча перекрывает влитый. Хостинг все
эти процессы отвергает целиком (замер corelib#58: повтор `needs`, имени
задания, ключа шага, ключа потокового отображения, `on` и `"on"`, ключ `<<`);
якорь и ссылку без `<<` принимает, и гейт их читает.

ИСХОДОВ ТРИ: 0 — свойство держится у всех; 1 — нарушение, названо файлом и
веткой, типом события или заданием; 2 — проверка не состоялась (нет
разборщика YAML, пустой обход, незнакомая форма, повтор ключа, предпосылка).

ЧЕГО ГЕЙТ НЕ ДОКАЗЫВАЕТ: что хостинг читает шаблон так же — это доказывает
только живой запрос (`gh pr view <N> --json statusCheckRollup` на запросе в
ветку-номер); разборщик сверен с примерами шпаргалки фильтров (самопроверка).
Что отчётное условие стоит у отчёта, а не у проверки: различие не
синтаксическое, и такие условия перепись называет числом. Условия внутри
действий, которые зовёт шаг (`uses:`). Что `success()` задания хостинг судит
по всей цепочке, а не по прямым вышестоящим: так читается документация,
живым запуском не измерено. Прочтение выбрано строгое: при прямом прочтении
гейт краснел бы на задании ниже `always()`-звена, которое хостинг исполнил
бы, — а зелёного без исполнения не дал бы.

Запуск:
  python3 .github/scripts/pr-target-branches.py --self-test
  python3 .github/scripts/pr-target-branches.py [--root <каталог>]
"""

import io
import re
import subprocess
import sys
import tempfile
from pathlib import Path

EVENTS = ("pull_request", "pull_request_target")
TRUNK = "main"
# Образцы — ФОРМЫ имени, а не живые ветки: одна, две и четыре цифры.
NUMBER_BRANCHES = ("7", "26", "2564")
# Близнецы: у каждого своя причина быть здесь. `26a` ловит `[0-9]*` (звезда —
# любые символы, а не повтор цифры); `release/26` ловит `**`; `issue-31` —
# снятая форма имени ветки задачи; прочие — имена веток, живших в этом
# репозитории до правила.
NON_NUMBER_BRANCHES = ("lane/oauth2-engine-intake", "batch-quota-fate",
                       "26a", "release/26", "issue-31")
CLASS_BODY = re.compile(r"(?:[A-Za-z0-9](?:-[A-Za-z0-9])?)+")
# Ключи события запроса — все, какие хостинг знает. Прочий ключ хостинг
# отвергает вместе с процессом, а гейт, молча его пропустивший, зеленел бы.
PR_KEYS = ("types", "branches", "branches-ignore", "paths", "paths-ignore")
# Умолчание хостинга для `types` — и минимум, без которого запрос остаётся без
# вердикта (opened) или с вердиктом прежнего дерева (synchronize, reopened).
REQUIRED_TYPES = ("opened", "synchronize", "reopened")
KNOWN_TYPES = REQUIRED_TYPES + (
    "assigned", "unassigned", "labeled", "unlabeled", "edited", "closed",
    "converted_to_draft", "ready_for_review", "locked", "unlocked",
    "review_requested", "review_request_removed", "auto_merge_enabled",
    "auto_merge_disabled", "milestoned", "demilestoned", "enqueued", "dequeued")
# Пути исполнения, на которых вычисляется условие: значения функций статуса.
PATHS = {
    "зелёный": {"success": True, "failure": False, "cancelled": False, "always": True},
    "провал": {"success": False, "failure": True, "cancelled": False, "always": True},
    "отмена": {"success": False, "failure": False, "cancelled": True, "always": True},
}
COND_TOKEN = re.compile(
    r"\s*(?:(?P<op>&&|\|\||!(?!=)|\(|\))"
    r"|(?P<fn>[A-Za-z_][A-Za-z0-9_]*)\(\s*\)"
    r"|(?P<lit>true|false)(?![A-Za-z0-9_.\-\[(]))")
WRAPPED = re.compile(r"\s*\$\{\{(?P<body>.*)\}\}\s*", re.S)


class Unknown(Exception):
    """Проверка не состоялась: форма незнакома, предпосылка не держится, разбирать нечем."""


# ── ШАБЛОН ВЕТКИ ─────────────────────────────────────────────────────────────

def _char_class(raw, body):
    if not CLASS_BODY.fullmatch(body):
        raise Unknown("шаблон ветки %r: перечень [%s] — не буквы, цифры и диапазоны"
                      % (raw, body))
    for lo, hi in re.findall(r"([A-Za-z0-9])-([A-Za-z0-9])", body):
        same_kind = ((lo.isdigit() and hi.isdigit())
                     or (lo.islower() and hi.islower())
                     or (lo.isupper() and hi.isupper()))
        if not same_kind or lo > hi:
            raise Unknown("шаблон ветки %r: диапазон %s-%s" % (raw, lo, hi))
    return "[" + body + "]"


def compile_pattern(raw):
    """Шаблон фильтра → (отрицание, регулярное выражение на всё имя)."""
    if not isinstance(raw, str) or raw in ("", "!"):
        raise Unknown("шаблон ветки %r: не строка либо пуст" % (raw,))
    negated = raw.startswith("!")
    body = raw[1:] if negated else raw
    atoms = []  # (выражение, можно ли повторить квантором)
    i = 0
    while i < len(body):
        c = body[i]
        if c == "*":
            width = 2 if body.startswith("**", i) else 1
            atoms.append((".*" if width == 2 else "[^/]*", False))
            i += width
        elif c in "?+":
            if not atoms or not atoms[-1][1]:
                raise Unknown("шаблон ветки %r: «%s» не относится ни к одному символу"
                              % (raw, c))
            atoms[-1] = (atoms[-1][0] + c, False)
            i += 1
        elif c == "[":
            j = body.find("]", i + 1)
            if j < 0:
                raise Unknown("шаблон ветки %r: незакрытая «[»" % raw)
            atoms.append((_char_class(raw, body[i + 1:j]), True))
            i = j + 1
        elif c == "]":
            raise Unknown("шаблон ветки %r: «]» без пары" % raw)
        elif c == "\\":
            if i + 1 >= len(body):
                raise Unknown("шаблон ветки %r: «\\» в конце" % raw)
            atoms.append((re.escape(body[i + 1]), True))
            i += 2
        else:
            atoms.append((re.escape(c), True))
            i += 1
    return negated, re.compile("".join(a for a, _ in atoms))


def fires(kind, compiled, base):
    """Пускает ли фильтр события запрос в ветку `base`."""
    if kind is None:
        return True
    if kind == "branches":
        decision = False
        for negated, rx in compiled:
            if rx.fullmatch(base):
                decision = not negated
        return decision
    return not any(rx.fullmatch(base) for _, rx in compiled)


# ── ПРОЦЕСС ──────────────────────────────────────────────────────────────────

def _events(rel, doc):
    if not isinstance(doc, dict):
        raise Unknown("%s: корень — не отображение" % rel)
    # `on:` без кавычек YAML 1.1 читает как булево True.
    on = doc["on"] if "on" in doc else doc.get(True)
    if isinstance(on, str):
        return {on: None}
    if isinstance(on, list):
        return {e: None for e in on}
    if isinstance(on, dict):
        return on
    raise Unknown("%s: `on` — %r, разбору не известно" % (rel, on))


def _types(rel, event, cfg, findings):
    """→ типы действия запроса, на которых событие пускает процесс."""
    raw = cfg.get("types", list(REQUIRED_TYPES))
    if isinstance(raw, str):
        raw = [raw]
    if not isinstance(raw, list) or not raw:
        raise Unknown("%s: on.%s.types — %r, а не непустой список" % (rel, event, raw))
    alien = [t for t in raw if t not in KNOWN_TYPES]
    if alien:
        raise Unknown("%s: on.%s.types: %s — типа действия запроса хостинг не знает, "
                      "разбору не известно" % (rel, event, ", ".join(map(repr, alien))))
    for t in REQUIRED_TYPES:
        if t not in raw:
            findings.append("%s: on.%s.types [%s] — запрос на «%s» процесс НЕ запускает"
                            % (rel, event, ", ".join(raw), t))
    return raw


def _branch_filter(rel, event, cfg, findings):
    """→ (вид фильтра, шаблоны, типы действия); вид None — фильтра по ветке нет."""
    if cfg is None:
        return None, [], list(REQUIRED_TYPES)
    if not isinstance(cfg, dict):
        raise Unknown("%s: on.%s — %r, разбору не известно" % (rel, event, cfg))
    alien = [k for k in cfg if k not in PR_KEYS]
    if alien:
        raise Unknown("%s: on.%s: ключ %s разбору не известен (знакомы: %s)"
                      % (rel, event, ", ".join(map(repr, alien)), ", ".join(PR_KEYS)))
    types = _types(rel, event, cfg, findings)
    for key in ("paths", "paths-ignore"):
        if key in cfg:
            findings.append("%s: on.%s.%s — событие сужено по путям: контекст, "
                            "который не начался, блокирует слияние навсегда"
                            % (rel, event, key))
    if "branches" in cfg and "branches-ignore" in cfg:
        findings.append("%s: on.%s несёт и branches, и branches-ignore — хостинг "
                        "такой процесс отвергает" % (rel, event))
        return "refused", [], types
    key = next((k for k in ("branches", "branches-ignore") if k in cfg), None)
    if key is None:
        return None, [], types
    raw = cfg[key]
    if isinstance(raw, str):
        raw = [raw]
    if not isinstance(raw, list) or not raw:
        raise Unknown("%s: on.%s.%s — %r, а не непустой список" % (rel, event, key, raw))
    try:
        compiled = [compile_pattern(p) for p in raw]
    except Unknown as e:
        raise Unknown("%s: on.%s.%s: %s" % (rel, event, key, e)) from e
    if key == "branches-ignore" and any(neg for neg, _ in compiled):
        raise Unknown("%s: on.%s.branches-ignore с «!» — как хостинг это читает, "
                      "разбору не известно" % (rel, event))
    return key, compiled, types


# ── УСЛОВИЕ ЗАДАНИЯ И ШАГА ───────────────────────────────────────────────────

def parse_condition(cond):
    """`if:` → дерево в словаре функций статуса; всё прочее — Unknown.

    Разбор, а не поиск образцом: запись, не выводимая грамматикой целиком,
    отвергается, какой бы безобидной ни выглядела.
    """
    if isinstance(cond, bool):
        text = "true" if cond else "false"
    elif isinstance(cond, str):
        text = cond
    else:
        raise Unknown("условие %r — не строка и не булево" % (cond,))
    wrapped = WRAPPED.fullmatch(text)
    if wrapped:
        text = wrapped.group("body")
    if "${{" in text or "}}" in text:
        raise Unknown("условие «%s» — обёртка ${{ }} не на всю запись" % cond)
    tokens, pos = [], 0
    while pos < len(text):
        if not text[pos:].strip():
            break
        m = COND_TOKEN.match(text, pos)
        if not m:
            raise Unknown("условие «%s»: «%s» вне словаря функций статуса"
                          % (cond, text[pos:].strip()))
        if m.group("fn") is not None:
            if m.group("fn") not in PATHS["зелёный"]:
                raise Unknown("условие «%s»: функция %s() вне словаря функций статуса"
                              % (cond, m.group("fn")))
            tokens.append(("fn", m.group("fn")))
        elif m.group("lit") is not None:
            tokens.append(("lit", m.group("lit") == "true"))
        else:
            tokens.append(("op", m.group("op")))
        pos = m.end()
    if not tokens:
        raise Unknown("условие «%s» пусто" % cond)

    def expect(i, op):
        if i >= len(tokens) or tokens[i] != ("op", op):
            raise Unknown("условие «%s»: грамматика не сходится у лексемы %d" % (cond, i + 1))

    def disj(i):
        node, i = conj(i)
        while i < len(tokens) and tokens[i] == ("op", "||"):
            rhs, i = conj(i + 1)
            node = ("or", node, rhs)
        return node, i

    def conj(i):
        node, i = unary(i)
        while i < len(tokens) and tokens[i] == ("op", "&&"):
            rhs, i = unary(i + 1)
            node = ("and", node, rhs)
        return node, i

    def unary(i):
        if i < len(tokens) and tokens[i] == ("op", "!"):
            node, i = unary(i + 1)
            return ("not", node), i
        if i < len(tokens) and tokens[i] == ("op", "("):
            node, i = disj(i + 1)
            expect(i, ")")
            return node, i + 1
        if i < len(tokens) and tokens[i][0] in ("fn", "lit"):
            return tokens[i], i + 1
        raise Unknown("условие «%s»: грамматика не сходится у лексемы %d" % (cond, i + 1))

    tree, end = disj(0)
    if end != len(tokens):
        raise Unknown("условие «%s»: грамматика не сходится у лексемы %d" % (cond, end + 1))
    return tree


def evaluate(tree, values):
    """Условие на значениях функций статуса `values` (путь PATHS либо контекст задания)."""
    kind = tree[0]
    if kind == "fn":
        return values[tree[1]]
    if kind == "lit":
        return tree[1]
    if kind == "not":
        return not evaluate(tree[1], values)
    if kind == "and":
        return evaluate(tree[1], values) and evaluate(tree[2], values)
    return evaluate(tree[1], values) or evaluate(tree[2], values)


def _has_status_function(tree):
    if tree[0] == "fn":
        return True
    if tree[0] == "lit":
        return False
    return any(_has_status_function(t) for t in tree[1:])


# Что хостинг вычисляет на месте записи: без `if:` — `success()`; условие без
# функции статуса — `success() && <условие>` («умолчание success() действует,
# пока в записи нет ни одной функции статуса»).
IMPLICIT = ("fn", "success")


def effective(tree):
    if tree is None:
        return IMPLICIT
    if _has_status_function(tree):
        return tree
    return ("and", IMPLICIT, tree)


def _as_written(cond):
    return ("true" if cond else "false") if isinstance(cond, bool) else cond


def _parse(rel, where, cond):
    try:
        return parse_condition(_as_written(cond))
    except Unknown as e:
        raise Unknown(
            "ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ: %s: %s — %s. Условие, зависящее от события, "
            "ветки или окружения, гейт не вычисляет, и зелёное здесь пришло бы без "
            "исполнения; дорастить разбор, а не обойти." % (rel, where, e)) from e


def _classify(rel, where, cond, tree, has_upstream, census, findings):
    """Условие, ложное на зелёном пути: отчётное, гасящее или не исполняемое никогда."""
    true_on = [p for p in PATHS if evaluate(tree, PATHS[p])]
    if has_upstream and true_on:
        census["reporting"] += 1
        return
    census["conditions_bad"] += 1
    if true_on:
        findings.append("%s: %s — условие «%s» ложно на зелёном пути, а вышестоящих "
                        "нет: на запросе НЕ исполняется, и пропущенное задание защита "
                        "ствола засчитывает успехом" % (rel, where, cond))
    else:
        findings.append("%s: %s — условие «%s» не истинно ни на одном пути: НЕ "
                        "исполняется никогда, и зелёное приходит без исполнения"
                        % (rel, where, cond))


def _needs(rel, name, job, jobs):
    """→ прямые вышестоящие задания; запись, которую хостинг отвергнет, — Unknown."""
    if "needs" not in job:
        return ()
    raw = job["needs"]
    if isinstance(raw, str):
        raw = [raw]
    if not isinstance(raw, list) or not all(isinstance(n, str) for n in raw):
        raise Unknown("%s: задание %s: `needs` — %r, а не имя задания либо список имён"
                      % (rel, name, job["needs"]))
    alien = [n for n in raw if n not in jobs]
    if alien:
        raise Unknown("%s: задание %s: needs %s — такого задания в процессе нет, "
                      "хостинг такой процесс отвергает" % (rel, name, ", ".join(alien)))
    return tuple(raw)


def _chain(rel, needs):
    """→ (порядок зависимостей, {задание: ВСЕ вышестоящие по цепочке}); кольцо — Unknown."""
    order, ancestors, state = [], {}, {}

    def visit(name, trail):
        if state.get(name) == "done":
            return
        if state.get(name) == "open":
            ring = trail[trail.index(name):] + [name]
            raise Unknown("%s: needs замкнуты в кольцо %s — хостинг такой процесс "
                          "отвергает" % (rel, " → ".join(ring)))
        state[name] = "open"
        up = set()
        for n in needs[name]:
            visit(n, trail + [name])
            up |= {n} | ancestors[n]
        ancestors[name] = up
        state[name] = "done"
        order.append(name)

    for name in needs:
        visit(name, [])
    return order, ancestors


def _conditions(rel, doc, census, findings):
    """Условия процесса С событием запроса: ни одно не гасит задание на запросе."""
    jobs = doc.get("jobs")
    if not isinstance(jobs, dict) or not jobs:
        raise Unknown("%s: `jobs` — %r, а не непустое отображение: процесс, который "
                      "гонится на запрос, без заданий хостинг отвергает" % (rel, jobs))
    needs, trees = {}, {}
    for name, job in jobs.items():
        census["jobs"] += 1
        if not isinstance(job, dict):
            raise Unknown("%s: задание %s — %r, а не отображение" % (rel, name, job))
        if "uses" in job:
            raise Unknown("%s: задание %s зовёт процесс %r — его заданий и условий гейт "
                          "не осматривает" % (rel, name, job["uses"]))
        needs[name] = _needs(rel, name, job, jobs)
        census["with_needs"] += bool(needs[name])
        trees[name] = _parse(rel, "задание %s" % name, job["if"]) if "if" in job else None
        steps = job.get("steps")
        if not isinstance(steps, list) or not steps:
            raise Unknown("%s: задание %s: `steps` — %r, а не непустой список"
                          % (rel, name, steps))
        for n, step in enumerate(steps, 1):
            if not isinstance(step, dict):
                raise Unknown("%s: задание %s, шаг %d — %r, а не отображение"
                              % (rel, name, n, step))
            if "if" not in step:
                continue
            where = "задание %s, шаг %d" % (name, n)
            tree = effective(_parse(rel, where, step["if"]))
            census["conditions"] += 1
            if not evaluate(tree, PATHS["зелёный"]):
                # Вышестоящие шага — шаги до него; у первого их нет.
                _classify(rel, where, _as_written(step["if"]), tree, n > 1,
                          census, findings)

    # ЗЕЛЁНЫЙ ПУТЬ ПО ГРАФУ: ни одно исполнившееся задание не упало, отмены нет.
    # Задание исполняется, если его условие истинно в контексте вышестоящих:
    # success() — все вышестоящие по цепочке исполнились, failure() и
    # cancelled() — ложны, always() — истинно.
    order, ancestors = _chain(rel, needs)
    ran = {}
    for name in order:
        context = {"success": all(ran[a] for a in ancestors[name]),
                   "failure": False, "cancelled": False, "always": True}
        ran[name] = evaluate(effective(trees[name]), context)
    for name in jobs:
        tree = effective(trees[name])
        census["conditions"] += trees[name] is not None
        if ran[name]:
            continue
        if evaluate(tree, PATHS["зелёный"]):
            # Своё условие истинно на зелёном — задание гасит пропуск вышестоящего.
            skipped = [a for a in order if a in ancestors[name] and not ran[a]]
            census["chain_skipped"] += 1
            if trees[name] is None:
                own = "неявное условие success() (`if:` нет)"
            else:
                own = "условие «%s»%s" % (
                    _as_written(jobs[name]["if"]),
                    "" if _has_status_function(trees[name])
                    else " (без функции статуса — неявное success() && …)")
            findings.append(
                "%s: задание %s — на зелёном пути НЕ исполняется: %s %s на зелёном "
                "пути %s, а %s при пропуске вышестоящего ложно (пропуск идёт по "
                "цепочке needs); пропущенное задание защита ствола засчитывает "
                "успехом — нужно условие, истинное при пропуске: always(), "
                "!cancelled() или !failure()"
                % (rel, name, *(("вышестоящее", ", ".join(skipped), "пропущено")
                                 if len(skipped) == 1 else
                                 ("вышестоящие", ", ".join(skipped), "пропущены")),
                   own))
            continue
        _classify(rel, "задание %s" % name, _as_written(jobs[name]["if"]), tree,
                  bool(needs[name]), census, findings)


def _other_conditions(doc, census):
    """Процесс без события запроса: условия не судятся, но счёт им ведётся."""
    jobs = doc.get("jobs")
    if not isinstance(jobs, dict):
        return
    for job in jobs.values():
        if not isinstance(job, dict):
            continue
        census["unjudged"] += "if" in job
        for step in job.get("steps") or []:
            census["unjudged"] += isinstance(step, dict) and "if" in step


def _duplicate_keys(rel, text, yaml):
    """Повтор ключа в одном отображении — Unknown с ключом, путём и строками.

    Разборщик при повторе молча берёт ПОСЛЕДНЕЕ значение, и гейт судил бы одно
    из двух, не сказав об этом; хостинг такой процесс отвергает целиком (замер
    corelib#58). Ключи сравниваются в двух прочтениях, и повтор в любом — исход 2:
      - значением разборщика (YAML 1.1): `on` и `On` — оба True, и одно из двух
        значений гейт потерял бы молча;
      - записью скаляра (так читает хостинг): `on` и `"on"` — один ключ, а гейт,
        предпочитающий строку `"on"`, судил бы не тот.
    Ключ слияния `<<` — тот же исход: разборщик сливает отображения, и явный
    ключ молча перекрывает влитый, а хостинг такой процесс отвергает (замер
    corelib#58; якорь и ссылка без `<<` хостингом приняты). Обход — по узлам
    разбора, то есть и потоковые `{a: 1, a: 2}`, и отображения внутри списков;
    ссылка на якорь проходится один раз.
    """
    try:
        root = yaml.compose(text, Loader=yaml.SafeLoader)
    except yaml.YAMLError as e:
        raise Unknown("%s: не разбирается как YAML: %s" % (rel, e)) from e
    constructor = yaml.constructor.SafeConstructor()
    walked = set()

    def label(node):
        return node.value if isinstance(node, yaml.ScalarNode) else "<%s>" % node.tag

    def walk(node, path):
        if node is None or id(node) in walked:
            return
        walked.add(id(node))
        if isinstance(node, yaml.SequenceNode):
            for i, item in enumerate(node.value, 1):
                walk(item, "%s[%d]" % (path, i))
            return
        if not isinstance(node, yaml.MappingNode):
            return
        by_value, by_text = {}, {}
        for key, value in node.value:
            if key.tag == "tag:yaml.org,2002:merge":
                raise Unknown(
                    "%s: %s: ключ слияния «%s» (строка %d) — разборщик YAML сливает "
                    "отображения и молча отдаёт перекрытое значение; хостинг такой "
                    "процесс отвергает" % (rel, path or "корень", key.value,
                                           key.start_mark.line + 1))
            try:
                ident = constructor.construct_object(key, deep=True)
                hash(ident)
            except (yaml.YAMLError, TypeError):
                ident = None  # несравнимый ключ отвергнет сам разбор ниже
            text_ident = key.value if isinstance(key, yaml.ScalarNode) else None
            for table, k in ((by_value, ident), (by_text, text_ident)):
                if k is None:
                    continue
                if k in table:
                    raise Unknown(
                        "%s: %s: ключ «%s» повторён (строки %d и %d) — разборщик YAML "
                        "молча берёт последнее значение, и гейт судил бы одно из двух; "
                        "хостинг такой процесс отвергает"
                        % (rel, path or "корень", label(key),
                           table[k].start_mark.line + 1, key.start_mark.line + 1))
                table[k] = key
            walk(value, "%s.%s" % (path, label(key)) if path else label(key))

    walk(root, "")


def audit(files, out):
    """files: {путь: текст} отслеживаемых процессов. Возвращает код исхода."""
    try:
        import yaml
    except ImportError as e:
        print("ПРОВЕРКА НЕ СОСТОЯЛАСЬ: нет PyYAML, разобрать процессы нечем: %s" % e,
              file=out)
        return 2
    census = {"files": len(files), "jobs": 0, "conditions": 0, "reporting": 0,
              "conditions_bad": 0, "unjudged": 0, "with_needs": 0, "chain_skipped": 0,
              "pull_request": 0, "pull_request_target": 0, "without": 0}
    findings, filters = [], []
    try:
        if not files:
            raise Unknown("обход пуст: в индексе нет ни одного процесса .github/workflows/*.y(a)ml")
        for rel in sorted(files):
            _duplicate_keys(rel, files[rel], yaml)
            try:
                doc = yaml.safe_load(files[rel])
            except yaml.YAMLError as e:
                raise Unknown("%s: не разбирается как YAML: %s" % (rel, e)) from e
            events = _events(rel, doc)
            present = [e for e in EVENTS if e in events]
            if not present:
                census["without"] += 1
                _other_conditions(doc, census)
                continue
            _conditions(rel, doc, census, findings)
            for event in present:
                census[event] += 1
                cfg = events[event]
                kind, compiled, types = _branch_filter(rel, event, cfg, findings)
                branches = {"refused": "ветки отвергнуты хостингом",
                            None: "ветки любые"}.get(kind) or "%s %s" % (kind, cfg[kind])
                defaulted = not (isinstance(cfg, dict) and "types" in cfg)
                filters.append("%s · %s: %s · типы %s%s" % (
                    rel, event, branches, ", ".join(types),
                    " (умолчание)" if defaulted else ""))
                if kind == "refused":
                    continue
                for base in (TRUNK,) + NUMBER_BRANCHES:
                    if not fires(kind, compiled, base):
                        findings.append("%s: on.%s — запрос в «%s» процесс НЕ запускает"
                                        % (rel, event, base))
                for base in NON_NUMBER_BRANCHES:
                    if fires(kind, compiled, base):
                        findings.append("%s: on.%s — запрос в ветку не-номер «%s» процесс "
                                        "запускает: сужение снято" % (rel, event, base))
        if census["pull_request"] + census["pull_request_target"] == 0:
            findings.append("ни один процесс не гонится на запрос: вердикта у запросов нет")
    except Unknown as e:
        print("ПРОВЕРКА НЕ СОСТОЯЛАСЬ: %s" % e, file=out)
        return 2

    print("=== триггеры запроса: перепись ===", file=out)
    print("процессов в индексе : %d  (pull_request %d · pull_request_target %d · "
          "без события запроса %d)" % (census["files"], census["pull_request"],
                                       census["pull_request_target"], census["without"]),
          file=out)
    for line in filters:
        print("событие             : %s" % line, file=out)
    print("заданий осмотрено   : %d  (условий if вычислено: %d — только вне зелёного "
          "пути %d, гасящих %d; в процессах без события запроса не судится %d)"
          % (census["jobs"], census["conditions"], census["reporting"],
             census["conditions_bad"], census["unjudged"]), file=out)
    print("цепочка needs       : заданий с needs %d, на зелёном пути гаснут вслед за "
          "вышестоящим %d" % (census["with_needs"], census["chain_skipped"]), file=out)
    print("образцы             : %s · номер %s · не-номер %s"
          % (TRUNK, ", ".join(NUMBER_BRANCHES), ", ".join(NON_NUMBER_BRANCHES)), file=out)
    if findings:
        print("", file=out)
        for f in findings:
            print("  НАРУШЕНИЕ %s" % f, file=out)
        print("КРАСНЫЙ: нарушений %d." % len(findings), file=out)
        return 1
    print("ЗЕЛЁНЫЙ: запрос в main и в ветку-номер гонит конвейер на открытии, движении и "
          "переоткрытии, и ни условия заданий, ни цепочка needs его не гасят; в ветку "
          "не-номер — нет.", file=out)
    return 0


def tracked_workflows(root):
    """Процессы из ИНДЕКСА git: игнорируемый или неотслеживаемый файл хостинг не видит."""
    try:
        listed = subprocess.run(
            ["git", "-C", str(root), "ls-files", "-z", "--",
             ".github/workflows/*.yml", ".github/workflows/*.yaml"],
            check=True, capture_output=True).stdout.decode()
    except (OSError, subprocess.CalledProcessError) as e:
        raise Unknown("индекс git не прочитан: %s" % e) from e
    files = {}
    for rel in filter(None, listed.split("\0")):
        if rel.count("/") != 2:  # хостинг читает только верхний уровень каталога
            continue
        try:
            files[rel] = (Path(root) / rel).read_text(encoding="utf-8")
        except OSError as e:
            raise Unknown("%s: не прочитан: %s" % (rel, e)) from e
    return files


def main(argv):
    root = Path(__file__).resolve().parents[2]
    if "--root" in argv:
        root = Path(argv[argv.index("--root") + 1])
    try:
        files = tracked_workflows(root)
    except Unknown as e:
        print("ПРОВЕРКА НЕ СОСТОЯЛАСЬ: %s" % e)
        return 2
    return audit(files, sys.stdout)


# ── САМОПРОВЕРКА: доказательство инъекцией в обе стороны ─────────────────────
#
# Живёт флагом этого же файла: отдельный файл в перечень шагов конвейера не
# попал бы сам. Каждый мир меняет РОВНО ОДИН факт против положительного
# близнеца — иначе неизвестно, что дало красное.
GOOD = """name: ci
on:
  push:
    branches: [main]
  pull_request:
    branches: [main, '[0-9]+']
  schedule:
    - cron: '23 4 * * *'
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: go build ./...
      - if: always()
        run: echo done
"""
F = ".github/workflows/ci.yml"


def self_test():
    probes = failed = 0

    def check(title, ok):
        nonlocal probes, failed
        probes += 1
        if ok:
            print("  ok   %s" % title)
        else:
            print("  ПРОВАЛ %s" % title, file=sys.stderr)
            failed += 1

    def run(want, title, files, must=(), must_not=()):
        buf = io.StringIO()
        got = audit(files, buf)
        text = buf.getvalue()
        missing = [m for m in must if m not in text]
        extra = [m for m in must_not if m in text]
        check("%s (ждали %d, получили %d%s%s)" % (
            title, want, got, "; в тексте нет: " + ", ".join(missing) if missing else "",
            "; в тексте лишнее: " + ", ".join(extra) if extra else ""),
            got == want and not missing and not extra)

    def matches(pattern, name):
        negated, rx = compile_pattern(pattern)
        return (not negated) and bool(rx.fullmatch(name))

    print("=== pr-target-branches: доказательство инъекцией ===")

    # Разборщик шаблона — против примеров шпаргалки фильтров хостинга.
    check("шпаргалка: v[12].[0-9]+.[0-9]+ ↔ v1.10.1, не v3.0.0",
          matches("v[12].[0-9]+.[0-9]+", "v1.10.1") and not matches("v[12].[0-9]+.[0-9]+", "v3.0.0"))
    check("шпаргалка: feature/* ↔ feature/my-branch, не feature/your/branch",
          matches("feature/*", "feature/my-branch") and not matches("feature/*", "feature/your/branch"))
    check("шпаргалка: feature/** ↔ feature/your/branch",
          matches("feature/**", "feature/your/branch"))
    check("определение «?»: main? — ноль или один «n»: mai и main, не mainn",
          matches("main?", "mai") and matches("main?", "main") and not matches("main?", "mainn"))
    check("[0-9]+ ↔ 7, 26, 2564; не 26a, не пусто",
          all(matches("[0-9]+", b) for b in NUMBER_BRANCHES)
          and not matches("[0-9]+", "26a") and not matches("[0-9]+", ""))
    check("[0-9]* ↔ 26a: звезда — любые символы, а не повтор цифры",
          matches("[0-9]*", "26a"))
    check("«!» решает последним совпавшим: [main, '[0-9]+', '!26'] не пускает 26",
          not fires("branches", [compile_pattern(p) for p in ("main", "[0-9]+", "!26")], "26"))
    for bad in ("+1", "[0-9", "[z-a]", "[.]", "**+", "\\"):
        try:
            compile_pattern(bad)
            check("незнакомая форма %r — отказ разбора" % bad, False)
        except Unknown:
            check("незнакомая форма %r — отказ разбора" % bad, True)

    # (−) положительный близнец. Без него всё ниже зеленело бы на гейте,
    # который краснеет всегда.
    run(0, "(−) [main, '[0-9]+'] — зелёный", {F: GOOD}, must=("ЗЕЛЁНЫЙ", "процессов в индексе : 1"))
    # (+) НАСТОЯЩИЙ прежний дефект: фильтр `[main]` (ci.yml до #31).
    run(1, "(+) [main] — номер не гонит", {F: GOOD.replace("[main, '[0-9]+']", "[main]")},
        must=(F, "«26» процесс НЕ запускает"))
    run(1, "(+) без main — ствол не гонит",
        {F: GOOD.replace("[main, '[0-9]+']", "['[0-9]+']")}, must=("«main» процесс НЕ запускает",))
    run(1, "(+) [main, '[0-9]*'] — гонит 26a",
        {F: GOOD.replace("'[0-9]+'", "'[0-9]*'")}, must=("не-номер «26a»",))
    run(1, "(+) фильтр снят — гонит любую ветку",
        {F: GOOD.replace("  pull_request:\n    branches: [main, '[0-9]+']\n", "  pull_request:\n")},
        must=("не-номер «lane/oauth2-engine-intake»",))
    run(1, "(+) `on` списком — тот же несуженный запрос",
        {F: re.sub(r"(?s)^on:.*?(?=^jobs:)", "on: [push, pull_request]\n", GOOD, flags=re.M)},
        must=("сужение снято",))
    run(1, "(+) сужение по путям",
        {F: GOOD.replace("[main, '[0-9]+']\n", "[main, '[0-9]+']\n    paths: ['**.go']\n")},
        must=("on.pull_request.paths",))
    run(1, "(+) branches и branches-ignore вместе",
        {F: GOOD.replace("[main, '[0-9]+']\n", "[main, '[0-9]+']\n    branches-ignore: [x]\n")},
        must=("и branches, и branches-ignore",))
    run(1, "(+) ни одного события запроса",
        {F: GOOD.replace("  pull_request:\n    branches: [main, '[0-9]+']\n", "")},
        must=("ни один процесс не гонится на запрос",))
    # (−) законные близнецы другой формы записи того же свойства.
    run(0, "(−) `\"on\"` в кавычках — та же запись",
        {F: GOOD.replace("\non:\n", '\n"on":\n')})
    run(0, "(−) branches-ignore, пускающий main и номер",
        {F: GOOD.replace("branches: [main, '[0-9]+']",
                         "branches-ignore: ['**/**', '*-*', '[0-9]*[a-z]*']")})
    run(0, "(−) процесс без события запроса рядом с гонимым — не нарушение",
        {F: GOOD, ".github/workflows/nightly.yml": "on:\n  schedule:\n    - cron: '1 1 * * *'\njobs: {}\n"},
        must=("без события запроса 1",))

    # ── ТИПЫ ДЕЙСТВИЯ ЗАПРОСА (on.<событие>.types) ─────────────────────────────
    # Прежде ключ не судился вовсе: `types: [closed]` давал ЗЕЛЁНЫЙ, хотя запрос
    # на открытии и движении процесс уже не запускал (приёмка #31, B1).
    def pr_types(value, text=GOOD):
        return text.replace("    branches: [main, '[0-9]+']\n",
                            "    branches: [main, '[0-9]+']\n    types: %s\n" % value)
    run(1, "(+) types: [closed] — открытие, движение, переоткрытие не гонят",
        {F: pr_types("[closed]")},
        must=("on.pull_request.types [closed] — запрос на «opened» процесс НЕ запускает",
              "«synchronize»", "«reopened»", "КРАСНЫЙ: нарушений 3."))
    run(1, "(+) types без synchronize — движение ветки не перепрогоняет",
        {F: pr_types("[opened, reopened]")},
        must=("на «synchronize» процесс НЕ запускает", "КРАСНЫЙ: нарушений 1."))
    run(1, "(+) types строкой `opened` — та же нехватка",
        {F: pr_types("opened")}, must=("«synchronize»", "«reopened»"))
    run(0, "(−) types: [opened, synchronize, reopened] — умолчание, записанное явно",
        {F: pr_types("[opened, synchronize, reopened]")}, must=("ЗЕЛЁНЫЙ",))
    run(0, "(−) types шире умолчания — ready_for_review сверх трёх",
        {F: pr_types("[opened, synchronize, reopened, ready_for_review]")})
    run(2, "(+) тип, которого хостинг не знает, — не состоялось",
        {F: pr_types("[opened, synchronize, reopened, synchronise]")},
        must=("'synchronise' — типа действия запроса хостинг не знает",))
    run(2, "(+) types: [] — не состоялось", {F: pr_types("[]")}, must=("непустой список",))
    run(2, "(+) незнакомый ключ события — не состоялось",
        {F: GOOD.replace("[main, '[0-9]+']\n", "[main, '[0-9]+']\n    branch: [x]\n")},
        must=("ключ 'branch' разбору не известен",))
    target = GOOD.replace("  pull_request:\n", "  pull_request_target:\n")
    run(0, "(−) pull_request_target той же записью", {F: target},
        must=("pull_request_target 1",))
    run(1, "(+) pull_request_target с types: [closed]", {F: pr_types("[closed]", target)},
        must=("on.pull_request_target.types [closed]",))

    # ── УСЛОВИЯ ЗАДАНИЯ И ШАГА: закрытый словарь, вычисление по путям ──────────
    # Прежде условие искалось образцом по тексту: индексная запись того же
    # условия по ветке давала ЗЕЛЁНЫЙ (приёмка #31, B4), условие по событию и
    # `if: false` — тоже.
    def job_if(cond):
        return GOOD.replace("    runs-on: ubuntu-latest\n",
                            "    if: %s\n    runs-on: ubuntu-latest\n" % cond, 1)
    run(2, "(+) условие по ветке в индексной записи — не состоялось",
        {F: job_if("github.event['pull_request']['base']['ref'] == 'main'")},
        must=("ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ", "задание build", "вне словаря функций статуса"))
    run(2, "(+) условие по событию — не состоялось",
        {F: job_if("github.event_name == 'push'")}, must=("ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ",))
    run(2, "(+) условие через окружение — не состоялось",
        {F: job_if("${{ env.GATE == 'on' }}")}, must=("ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ",))
    run(2, "(+) функция статуса в паре с контекстом — не состоялось",
        {F: job_if("success() && github.actor != 'bot'")}, must=("ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ",))
    run(2, "(+) обёртка ${{ }} не на всю запись — не состоялось",
        {F: job_if("${{ always() }} && github.ref == 'x'")},
        must=("обёртка ${{ }} не на всю запись",))
    run(1, "(+) if: false у задания — не исполняется никогда",
        {F: job_if("false")}, must=("задание build — условие «false» не истинно ни на "
                                    "одном пути",))
    run(1, "(+) ${{ !always() }} у задания — не исполняется никогда",
        {F: job_if("${{ !always() }}")}, must=("не истинно ни на одном пути",))
    run(1, "(+) failure() у задания без needs — на зелёном не исполняется",
        {F: job_if("failure()")}, must=("ложно на зелёном пути, а вышестоящих нет",))
    run(0, "(−) always() у задания — законный близнец индексной записи",
        {F: job_if("always()")}, must=("условий if вычислено: 2",))
    run(0, "(−) ${{ !cancelled() }} у задания", {F: job_if("${{ !cancelled() }}")})
    run(0, "(−) if: true (булево YAML) у задания", {F: job_if("true")})
    run(0, "(−) (success() || failure()) && !cancelled() у задания",
        {F: job_if("${{ (success() || failure()) && !cancelled() }}")})
    report = "  report:\n    %sif: failure()\n    runs-on: ubuntu-latest\n" \
             "    steps:\n      - run: echo report\n"
    run(0, "(−) отчётное задание: needs + failure()",
        {F: GOOD + report % "needs: build\n    "},
        must=("только вне зелёного пути 1",))
    run(1, "(+) то же задание без needs — не исполняется",
        {F: GOOD + report % ""}, must=("задание report — условие «failure()»",))
    # ── ЦЕПОЧКА needs: пропуск вышестоящего гасит нижестоящее ──────────────────
    # Прежде `needs:` не судился вовсе: задание без условия ниже отчётного давало
    # ЗЕЛЁНЫЙ, хотя на зелёном пути хостинг пропускает его вслед за отчётным, а
    # пропуск защита засчитывает успехом (приёмка #31, круг 2: N1 — без `if:`,
    # N1b — `success()`, N1c — needs [build, zz-report], и N1 рядом с близнецом).
    def job(name, needs, cond=None):
        return ("  %s:\n    needs: %s\n%s    runs-on: ubuntu-latest\n"
                "    steps:\n      - run: echo %s\n"
                % (name, needs, "    if: %s\n" % cond if cond else "", name))
    chain = GOOD + job("zz-report", "build", "failure()")
    run(1, "(+) N1: задание без if: ниже отчётного — на зелёном пути гаснет",
        {F: chain + job("zz-after", "zz-report")},
        must=("задание zz-after — на зелёном пути НЕ исполняется: вышестоящее zz-report "
              "на зелёном пути пропущено, а неявное условие success() (`if:` нет)",
              "гаснут вслед за вышестоящим 1", "КРАСНЫЙ: нарушений 1."))
    run(0, "(−) близнец N1: то же задание с if: always()",
        {F: chain + job("zz-after", "zz-report", "always()")},
        must=("ЗЕЛЁНЫЙ", "заданий с needs 2, на зелёном пути гаснут вслед за вышестоящим 0",
              "только вне зелёного пути 1"))
    run(1, "(+) N1b: if: success() ниже отчётного",
        {F: chain + job("zz-after", "zz-report", "success()")},
        must=("задание zz-after — на зелёном пути НЕ исполняется",
              "условие «success()» при пропуске вышестоящего ложно"))
    run(1, "(+) N1c: needs [build, zz-report] без if:",
        {F: chain + job("zz-after", "[build, zz-report]")},
        must=("задание zz-after — на зелёном пути НЕ исполняется: вышестоящее zz-report",
              "КРАСНЫЙ: нарушений 1."))
    run(1, "(+) N1 рядом с близнецом — краснеет ровно N1",
        {F: chain + job("zz-after", "zz-report") + job("zz-twin", "zz-report", "always()")},
        must=("задание zz-after — на зелёном пути НЕ исполняется", "КРАСНЫЙ: нарушений 1.",
              "гаснут вслед за вышестоящим 1"),
        must_not=("задание zz-twin",))
    run(1, "(+) if: true ниже отчётного — неявное success() && true",
        {F: chain + job("zz-after", "zz-report", "true")},
        must=("условие «true» (без функции статуса — неявное success() && …)",))
    run(1, "(+) success() || failure() ниже отчётного — на зелёном ложно",
        {F: chain + job("zz-after", "zz-report", "${{ success() || failure() }}")},
        must=("задание zz-after — на зелёном пути НЕ исполняется",))
    run(1, "(+) транзитивно: без if: ниже always()-звена под отчётным",
        {F: chain + job("zz-mid", "zz-report", "always()") + job("zz-after", "zz-mid")},
        must=("задание zz-after — на зелёном пути НЕ исполняется: вышестоящее zz-report",
              "КРАСНЫЙ: нарушений 1."),
        must_not=("задание zz-mid —",))
    run(1, "(+) два пропущенных вышестоящих названы оба",
        {F: chain + job("zz-report2", "build", "cancelled()")
            + job("zz-after", "[zz-report, zz-report2]")},
        must=("вышестоящие zz-report, zz-report2 на зелёном пути пропущены",))
    run(0, "(−) транзитивный близнец: !cancelled() ниже always()-звена",
        {F: chain + job("zz-mid", "zz-report", "always()")
            + job("zz-after", "zz-mid", "${{ !cancelled() }}")}, must=("ЗЕЛЁНЫЙ",))
    run(0, "(−) !failure() ниже отчётного — истинно при пропуске",
        {F: chain + job("zz-after", "zz-report", "'!failure()'")}, must=("ЗЕЛЁНЫЙ",))
    run(0, "(−) отчётное ниже отчётного — само отчётное",
        {F: chain + job("zz-after", "zz-report", "failure()")},
        must=("только вне зелёного пути 2", "гаснут вслед за вышестоящим 0"))
    run(0, "(−) needs строкой и списком ниже исполняемого — исполняется без if:",
        {F: GOOD + job("after", "build") + job("after2", "[build, after]")},
        must=("ЗЕЛЁНЫЙ", "заданий с needs 2, на зелёном пути гаснут вслед за вышестоящим 0"))
    run(2, "(+) needs на задание, которого в процессе нет",
        {F: GOOD + job("after", "biuld")}, must=("needs biuld — такого задания в процессе нет",))
    run(2, "(+) needs замкнуты в кольцо", {F: GOOD + job("a", "b") + job("b", "a")},
        must=("кольцо a → b → a",))
    run(2, "(+) задание в своих needs", {F: GOOD + job("a", "a")}, must=("кольцо a → a",))
    run(2, "(+) needs — не имя и не список имён", {F: GOOD + job("a", "{x: 1}")},
        must=("задание a: `needs` —",))

    run(1, "(+) if: false у шага — не исполняется никогда",
        {F: GOOD.replace("if: always()", "if: false")},
        must=("задание build, шаг 2 — условие «false»",))
    run(0, "(−) отчётный шаг: failure() после вышестоящего шага",
        {F: GOOD.replace("if: always()", "if: failure()")},
        must=("только вне зелёного пути 1",))
    run(1, "(+) failure() у первого шага — вышестоящих нет",
        {F: GOOD.replace("      - run: go build ./...\n",
                         "      - if: failure()\n        run: go build ./...\n")},
        must=("задание build, шаг 1 — условие «failure()» ложно на зелёном пути",))
    run(2, "(+) задание зовёт процесс — его заданий гейт не видит",
        {F: GOOD + "  called:\n    uses: ./.github/workflows/x.yml\n"},
        must=("задание called зовёт процесс",))
    run(2, "(+) задание без шагов — не состоялось",
        {F: GOOD + "  empty:\n    runs-on: ubuntu-latest\n"},
        must=("задание empty: `steps`",))
    run(2, "(+) задание — не отображение", {F: GOOD + "  broken: 7\n"},
        must=("задание broken — 7, а не отображение",))
    run(0, "(−) условие по событию в процессе БЕЗ события запроса — не судится, но счёт",
        {F: GOOD, ".github/workflows/nightly.yml":
            "on:\n  schedule:\n    - cron: '1 1 * * *'\njobs:\n  n:\n"
            "    if: github.event_name == 'schedule'\n    runs-on: ubuntu-latest\n"
            "    steps:\n      - run: 'true'\n"},
        must=("не судится 1",))
    for bad in ("(always()", "always() always()", "Always()", "always(1)", "!", "&& always()"):
        try:
            parse_condition(bad)
            check("незнакомое условие %r — отказ разбора" % bad, False)
        except Unknown:
            check("незнакомое условие %r — отказ разбора" % bad, True)

    # ── ПОВТОР КЛЮЧА: исход 2, а не выбор одного из значений ───────────────────
    # Прежде разбор при повторе молча брал ПОСЛЕДНЕЕ значение, и гейт судил одно
    # из двух: форма X9 (второй `needs` у задания) давала ЗЕЛЁНЫЙ (приёмка #31,
    # круг 3; corelib#58). Каждая форма — своим миром; у каждой близнец без
    # повтора — в мире рядом либо в GOOD.
    after = "  after:\n    needs: build\n%s    runs-on: ubuntu-latest\n" \
            "    steps:\n      - run: echo after\n"
    run(2, "(+) X9: needs повторён у задания — не состоялось, названы ключ и задание",
        {F: GOOD + after % "    needs: build\n"},
        must=("jobs.after: ключ «needs» повторён (строки 17 и 18)", "ПРОВЕРКА НЕ СОСТОЯЛАСЬ"))
    run(0, "(−) близнец X9: needs один", {F: GOOD + after % ""}, must=("ЗЕЛЁНЫЙ",))
    run(2, "(+) повтор, СКРЫВАЮЩИЙ нарушение: if: false, затем if: always()",
        {F: GOOD + after % "    if: false\n    if: always()\n"},
        must=("jobs.after: ключ «if» повторён",))
    run(2, "(+) повтор имени задания",
        {F: GOOD + GOOD[GOOD.index("  build:"):]}, must=("jobs: ключ «build» повторён",))
    run(2, "(+) повтор события запроса: второй pull_request сужен до [main]",
        {F: GOOD.replace("  schedule:\n", "  pull_request:\n    branches: [main]\n  schedule:\n")},
        must=("on: ключ «pull_request» повторён",))
    run(2, "(+) повтор в потоковом отображении: {branches: [main], branches: […]}",
        {F: GOOD.replace("  pull_request:\n    branches: [main, '[0-9]+']\n",
                         "  pull_request: {branches: [main], branches: [main, '[0-9]+']}\n")},
        must=("on.pull_request: ключ «branches» повторён",))
    run(2, "(+) повтор ключа шага",
        {F: GOOD.replace("      - if: always()\n", "      - if: always()\n        if: false\n")},
        must=("jobs.build.steps[2]: ключ «if» повторён",))
    run(2, "(+) `on` и `\"on\"` — один ключ для хостинга, два для разборщика",
        {F: GOOD.replace("\njobs:\n", "\n\"on\":\n  push:\n    branches: [x]\njobs:\n")},
        must=("корень: ключ «on» повторён",))
    run(2, "(+) `on` и `On` — два ключа для хостинга, один (True) для разборщика",
        {F: GOOD.replace("\njobs:\n", "\nOn:\n  push:\n    branches: [x]\njobs:\n")},
        must=("корень: ключ «On» повторён",))
    run(0, "(−) якорь и ссылка без повтора — законная запись",
        {F: GOOD.replace("    steps:\n", "    steps: &st\n", 1)
            + "  other:\n    runs-on: ubuntu-latest\n    steps: *st\n"},
        must=("ЗЕЛЁНЫЙ", "заданий осмотрено   : 2"))
    run(2, "(+) ключ слияния `<<`: разборщик сливает, хостинг процесс отвергает",
        {F: GOOD.replace("  build:\n", "  build: &b\n", 1)
            + "  other:\n    <<: *b\n    needs: build\n"},
        must=("jobs.other: ключ слияния «<<» (строка 17)",))
    # Настоящий вход: ci.yml ЭТОГО дерева, X9 — во втором его задании. Инъекция
    # не состоялась (задания или ci.yml нет) — провал, а не пропуск.
    real_path = Path(__file__).resolve().parents[2] / F
    real = real_path.read_text(encoding="utf-8") if real_path.is_file() else ""
    heads = re.findall(r"(?m)^  ([a-z][a-z0-9-]*):\n(?=    )", real.split("\njobs:\n", 1)[-1])
    check("настоящий ci.yml прочитан и несёт хотя бы два задания (%s)" % F, len(heads) >= 2)
    if len(heads) >= 2:
        head = "\n  %s:\n" % heads[1]
        once = real.replace(head, head + "    needs: %s\n" % heads[0], 1)
        twice = real.replace(head, head + "    needs: %s\n    needs: %s\n" % (heads[0], heads[0]), 1)
        check("инъекция в настоящий ci.yml состоялась", once != real and twice != once)
        run(0, "(−) настоящий ci.yml: needs один у задания %s" % heads[1], {F: once},
            must=("ЗЕЛЁНЫЙ",))
        run(2, "(+) настоящий ci.yml: X9 — needs повторён у задания %s" % heads[1], {F: twice},
            must=("jobs.%s: ключ «needs» повторён" % heads[1],))

    # (+) исход 2: проверка не состоялась, и это не зелёное.
    run(2, "(+) пустой обход — не состоялось", {}, must=("обход пуст",))
    run(2, "(+) условие шага по ветке — предпосылка не держится",
        {F: GOOD.replace("if: always()", "if: github.base_ref == 'main'")},
        must=("ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ", "шаг 2"))
    run(2, "(+) условие задания по ветке — предпосылка не держится",
        {F: GOOD.replace("    runs-on: ubuntu-latest\n",
                         "    if: github.ref == 'refs/heads/main'\n    runs-on: ubuntu-latest\n")},
        must=("задание build",))
    run(2, "(+) незнакомый шаблон — не состоялось",
        {F: GOOD.replace("'[0-9]+'", "'+[0-9]'")}, must=("не относится ни к одному символу",))
    run(2, "(+) незнакомый YAML — не состоялось", {F: "on: [\n"}, must=("не разбирается как YAML",))

    # Обход — по ИНДЕКСУ: файл на диске без `git add` хостинг не видит.
    with tempfile.TemporaryDirectory() as tmp:
        wf = Path(tmp) / ".github" / "workflows"
        wf.mkdir(parents=True)
        (wf / "ci.yml").write_text(GOOD, encoding="utf-8")
        subprocess.run(["git", "-C", tmp, "init", "-q"], check=True)
        check("неотслеживаемый процесс в перепись не входит",
              tracked_workflows(tmp) == {})
        subprocess.run(["git", "-C", tmp, "add", "."], check=True)
        check("отслеживаемый процесс — в переписи", list(tracked_workflows(tmp)) == [F])

    print("")
    print("pr-target-branches --self-test: проб исполнено %d, провалов %d" % (probes, failed))
    if probes == 0:
        return 2
    return 1 if failed else 0


if __name__ == "__main__":
    if "--self-test" in sys.argv[1:]:
        sys.exit(self_test())
    sys.exit(main(sys.argv[1:]))
