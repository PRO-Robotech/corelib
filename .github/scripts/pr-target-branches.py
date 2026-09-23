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
     не начавшийся контекст остаётся «ожидается» и блокирует слияние навсегда.

ФИЛЬТР ВЫЧИСЛЯЕТСЯ, А НЕ ИЩЕТСЯ ОБРАЗЦОМ. Разбирается YAML, шаблоны ветки
читаются по правилам хостинга: `*` — любые символы, кроме `/`; `**` — любые;
`?` и `+` — ноль-или-один и один-или-больше ПРЕДЫДУЩЕГО символа; `[…]` — один
символ из перечня или диапазона (только a-z, A-Z, 0-9); `\\` экранирует; `!`
в начале — отрицание; решает последний совпавший шаблон. Форма, которой
разборщик не знает, — исход 2, а не «не совпало»: молчание на незнакомой
записи было бы зелёным без осмотра.

ПРЕДПОСЫЛКА ЗАЯВЛЕНА И ПРОВЕРЯЕТСЯ. Условия `if:` заданий и шагов не
вычисляются. Условие по ветке (`base_ref`, `github.ref`, `pull_request.base`)
гасит задание или шаг на запросе, который событие пустило, — и зелёное пришло
бы без исполнения. Таких условий в дереве ноль; появится — исход 2
«предпосылка не держится»: гейт дорастить, а не обойти.

ИСХОДОВ ТРИ: 0 — свойство держится у всех; 1 — нарушение, названо файлом и
веткой; 2 — проверка не состоялась (нет разборщика YAML, пустой обход,
незнакомая форма, предпосылка).

ЧЕГО ГЕЙТ НЕ ДОКАЗЫВАЕТ: что хостинг читает шаблон так же. Это доказывает
только живой запрос — `gh pr view <N> --json statusCheckRollup` на запросе в
ветку-номер. Разборщик сверен с примерами шпаргалки фильтров (самопроверка).

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
BRANCH_CONDITION = re.compile(
    r"\b(?:base_ref|head_ref|ref_name)\b|\bgithub\.ref\b|pull_request\.base")
CLASS_BODY = re.compile(r"(?:[A-Za-z0-9](?:-[A-Za-z0-9])?)+")


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


def _branch_filter(rel, event, cfg, findings):
    """→ (вид фильтра, шаблоны) либо None, если судить нечего."""
    if cfg is None:
        return None, []
    if not isinstance(cfg, dict):
        raise Unknown("%s: on.%s — %r, разбору не известно" % (rel, event, cfg))
    for key in ("paths", "paths-ignore"):
        if key in cfg:
            findings.append("%s: on.%s.%s — событие сужено по путям: контекст, "
                            "который не начался, блокирует слияние навсегда"
                            % (rel, event, key))
    if "branches" in cfg and "branches-ignore" in cfg:
        findings.append("%s: on.%s несёт и branches, и branches-ignore — хостинг "
                        "такой процесс отвергает" % (rel, event))
        return "refused", []
    key = next((k for k in ("branches", "branches-ignore") if k in cfg), None)
    if key is None:
        return None, []
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
    return key, compiled


def _premise(rel, doc, census):
    jobs = doc.get("jobs") or {}
    if not isinstance(jobs, dict):
        raise Unknown("%s: `jobs` — не отображение" % rel)
    for name, job in jobs.items():
        census["jobs"] += 1
        if not isinstance(job, dict):
            continue
        conds = [("задание %s" % name, job.get("if"))]
        for n, step in enumerate(job.get("steps") or [], 1):
            if isinstance(step, dict):
                conds.append(("задание %s, шаг %d" % (name, n), step.get("if")))
        for where, cond in conds:
            if cond is None:
                continue
            census["conditions"] += 1
            if BRANCH_CONDITION.search(str(cond)):
                raise Unknown(
                    "ПРЕДПОСЫЛКА НЕ ДЕРЖИТСЯ: %s: %s — условие по ветке «%s». "
                    "Условия гейт не вычисляет, и зелёное здесь пришло бы без "
                    "исполнения; дорастить разбор, а не обойти." % (rel, where, cond))


def audit(files, out):
    """files: {путь: текст} отслеживаемых процессов. Возвращает код исхода."""
    try:
        import yaml
    except ImportError as e:
        print("ПРОВЕРКА НЕ СОСТОЯЛАСЬ: нет PyYAML, разобрать процессы нечем: %s" % e,
              file=out)
        return 2
    census = {"files": len(files), "jobs": 0, "conditions": 0,
              "pull_request": 0, "pull_request_target": 0, "without": 0}
    findings = []
    try:
        if not files:
            raise Unknown("обход пуст: в индексе нет ни одного процесса .github/workflows/*.y(a)ml")
        for rel in sorted(files):
            try:
                doc = yaml.safe_load(files[rel])
            except yaml.YAMLError as e:
                raise Unknown("%s: не разбирается как YAML: %s" % (rel, e)) from e
            events = _events(rel, doc)
            _premise(rel, doc, census)
            present = [e for e in EVENTS if e in events]
            if not present:
                census["without"] += 1
            for event in present:
                census[event] += 1
                kind, compiled = _branch_filter(rel, event, events[event], findings)
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
    print("заданий осмотрено   : %d  (условий if: %d, по ветке 0)"
          % (census["jobs"], census["conditions"]), file=out)
    print("образцы             : %s · номер %s · не-номер %s"
          % (TRUNK, ", ".join(NUMBER_BRANCHES), ", ".join(NON_NUMBER_BRANCHES)), file=out)
    if findings:
        print("", file=out)
        for f in findings:
            print("  НАРУШЕНИЕ %s" % f, file=out)
        print("КРАСНЫЙ: нарушений %d." % len(findings), file=out)
        return 1
    print("ЗЕЛЁНЫЙ: запрос в main и в ветку-номер гонит конвейер, в ветку не-номер — нет.",
          file=out)
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

    def run(want, title, files, must=()):
        buf = io.StringIO()
        got = audit(files, buf)
        text = buf.getvalue()
        missing = [m for m in must if m not in text]
        check("%s (ждали %d, получили %d%s)" % (
            title, want, got, "; в тексте нет: " + ", ".join(missing) if missing else ""),
            got == want and not missing)

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
