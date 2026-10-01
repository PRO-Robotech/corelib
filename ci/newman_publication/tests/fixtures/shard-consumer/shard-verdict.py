#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1
"""shard-verdict.py — что ЭТОТ шард реально исполнил, числом, в машинном виде.

ПРЕДМЕТ. После разнесения по раннерам вердикт больше не выносится в одном месте:
каждый шард судит свои суиты сам (assert-suites-green.sh), а свести это в один
ответ можно только по следам. Файл, который пишет этот скрипт, и есть след:
сколько коллекций шард ОБЯЗАН был отчитать (по дереву), сколько отчитал, и что
именно в этих отчётах.

ОПИСЬ — И ИСХОД РАБОТЫ ТАМ, ГДЕ БОЛЬШЕ ЕГО НЕ ВЫНОСИТ НИКТО (решение владельца
2026-09-12: «сделай так, чтобы можно было гарантировать зелёный; если что-то не так
— в красный»). О ПРОБАХ скрипт по-прежнему не судит: их вердикт выносят гейты суит,
и второй ответ рядом с первым дал бы два разных ответа на один вопрос. Поэтому
ветвление ровно такое:

  условие не создано      → код 75, метка «УСЛОВИЕ НЕ СОЗДАНО». Все гейты суит при
                            этом ПРОПУЩЕНЫ, то есть вердикта не выносит НИ ОДИН
                            шаг работы, и прежде работа уходила ЗЕЛЁНОЙ;
  коллекций в дереве ноль → код 1, метка «ОПИСЬ ПУСТА»: «0 из 0» — не вердикт;
  всё прочее              → код 0 и метка «вердикт выносят гейты суит».

Метки взаимно исключают друг друга и печатается ровно одна: два красных — «условие
не создано» и «пробы упали» — обязаны быть различимы ТЕКСТОМ, иначе отладка гадает.
Уникальность метки в дереве держит `--self-test`.

Здесь стояло «всегда выходит нулём, если сумел записать файл». Утверждение было
верно ровно до тех пор, пока работа краснела сама: прогон 34694955309 дал ЧЕТЫРЕ
шарда `success` при исполненных нуле коллекций из 57 — отметку поставил вердикт
провенанса, десять вердиктных шагов погасли, и сказал об этом ТОЛЬКО свод.

ЧЕТЫРЕ ИСХОДА ЗАПРОСА считаются раздельно — теми же полями, что читает
assert-suites-green.sh, чтобы шард и свод не разошлись в арифметике:
  assertions.failed          — проверка отработала и сказала «нет»;
  requests.failed            — ответа не пришло вовсе (UNANSWERED);
  testScripts/prerequestScripts.failed — скрипт упал ДО проверок;
  assertions.total == 0      — отчёт есть, а не проверено ничего.
Ни одно из трёх последних никогда не сворачивается в первое и не вычитается.

ОТСУТСТВИЕ ОТЧЁТА — ОТДЕЛЬНОЕ СОСТОЯНИЕ, а не ноль падений: коллекция дерева, по
которой нет файла отчёта, попадает в `missing`, и агрегатор трактует это как «не
выполнилось».

ПОЧЕМУ отчётов нет — тоже часть описи (kacho#655). «Ни одна проба не судила»
получается ТРЕМЯ разными способами, и следов у всех трёх одинаково ноль: прогон
шёл и упал (находка о дереве); стенд не поднялся, потому что посторонний хост не
отдал чарты; стенд поднялся, но исполняет ДРУГОЕ дерево. Второе и третье —
«условие не создано» (`e2e-flow.md` §6), и они приходят ОТМЕТКОЙ от того, кто их
наблюдал: наблюдателей ДВА, и опись называет обоих (см. FLAG_PRODUCERS ниже).
Прежде здесь стоял «единственный владелец материализации зависимостей» — второй
наблюдатель существовал уже тогда, и его форма молчала ровно потому, что этот
текст о ней не знал. Своими силами свод причины не установит, и молчание здесь
означало бы, что чужая причина и дальше читается как красный шард.
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[2]
MANIFEST = REPO / "deploy" / "e2e-shards.json"
SUFFIX = ".postman_collection.json"

# ФОРМ НЕСОЗДАННОГО УСЛОВИЯ ДВЕ, И ВТОРАЯ БЫЛА НЕВИДИМА.
#
#   форма A — отметка-ФАЙЛ. Её кладёт единственный владелец материализации
#             зависимостей умбреллы (deploy/scripts/helm-umbrella-deps.sh) и
#             снимает в начале каждого прогона, поэтому она описывает ЭТОТ
#             прогон, а не позапрошлый. Причина лежит в файле;
#   форма B — отметка в ОКРУЖЕНИИ и больше нигде. Её ставит вердикт провенанса
#             (.github/scripts/stand-revision-verdict.sh), когда стенд поднялся,
#             но исполняет ДРУГОЕ дерево. Файла он не оставляет — и распознаватель,
#             искавший только файл, на этой форме МОЛЧАЛ: в описи не было ни
#             `precondition`, ни причины, и свод честно печатал «НЕ ВЫПОЛНИЛОСЬ …
#             0 коллекций из 57», ни разу не сказав «условие не создано».
#
# Перечень производителей ВЫВОДИТСЯ ИЗ ДЕРЕВА самопроверкой и сверяется с
# FLAG_PRODUCERS в обе стороны: третий производитель обязан дать находку, а не
# молчание, а запись без производителя — не переживать свой предмет.
MARK_NAME = ".kacho-deps-precondition-unmet"
PRECOND_MARK = REPO / "deploy" / "helm" / "umbrella" / MARK_NAME
FLAG = "STAND_PRECONDITION_UNMET"

# Код 75 — машинный признак ТРЕТЬЕЙ КАТЕГОРИИ внутри прогона, тот же, что у
# классификатора посадки (deploy/scripts/classify-integration-outcome.sh, читает
# его ci.yaml). Работа при нём краснеет: исход РАБОТЫ меняется, различение
# остаётся в тексте и в коде.
EXIT_UNMET = 75
EXIT_EMPTY = 1

# Метки исхода. ОДНА печатается всегда, и они взаимно исключают друг друга:
# отладка не обязана гадать, какое из двух красных перед ней.
VERDICT_UNMET = "ВЕРДИКТ РАБОТЫ ШАРДА: КРАСНОЕ — УСЛОВИЕ НЕ СОЗДАНО"
VERDICT_EMPTY = "ВЕРДИКТ РАБОТЫ ШАРДА: КРАСНОЕ — ОПИСЬ ПУСТА"
VERDICT_GATES = "ВЕРДИКТ РАБОТЫ ШАРДА: вердикт выносят гейты суит"



def precondition() -> dict | None:
    """«Условие не создано» — в ЛЮБОЙ из двух форм, с названной формой и причиной.

    Отметка-ФАЙЛ идёт первой не по старшинству, а по содержательности: в ней
    лежит причина, названная тем, кто её наблюдал. Отметка в окружении причины
    не несёт — её производитель печатает обе ревизии в журнал и в сводку, и
    подставлять здесь свою догадку значило бы выдать её за наблюдение.

    Пустой файл — тоже отметка: причина могла не извлечься, но САМ ФАКТ чужой
    недоступности наблюдал тот, кто её положил. Считать отметку без текста
    отсутствующей значило бы терять третью категорию из-за пустой строки.
    """
    if PRECOND_MARK.is_file():
        try:
            detail = PRECOND_MARK.read_text(encoding="utf-8").strip()
        except OSError as exc:
            # Нечитаемая отметка — НЕ «отметки нет»: факт наблюдён, потеряна
            # только его формулировка, и молчать об этом нельзя.
            detail = f"отметка есть, но не прочиталась ({exc.__class__.__name__})"
        return {"unmet": True, "kind": "external-chart-source",
                "producer": ".github/scripts/stand-up.sh",
                "detail": detail or "причина не извлеклась (отметка пуста)"}
    if os.environ.get(FLAG) == "1":
        return {"unmet": True, "kind": "stand-revision-divergence",
                "producer": ".github/scripts/stand-revision-verdict.sh",
                "detail": "стенд поднялся, но исполняет ДРУГОЕ дерево: ревизия контейнеров "
                          "не сошлась с ожидаемой. Обе величины назвал шаг «провенанс "
                          "стенда» выше — в журнале работы и в сводке прогона"}
    return None


def suite_dir(svc: str) -> pathlib.Path:
    return REPO / ("gateway/tests/newman" if svc == "api-gateway"
                   else f"services/{svc}/tests/newman")


def tracked_stems(svc: str) -> list[str]:
    """Коллекции суиты по ДЕРЕВУ (git), а не по диску: gen.py кладёт файлы рядом."""
    d = suite_dir(svc).relative_to(REPO)
    r = subprocess.run(["git", "-C", str(REPO), "ls-files", "--", f"{d}/collections/*{SUFFIX}"],
                       capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"FATAL: git ls-files не отработал: {r.stderr.strip()}")
    return sorted(pathlib.PurePath(l).name[: -len(SUFFIX)] for l in r.stdout.split() if l.strip())


def read_report(path: pathlib.Path) -> dict:
    stats = json.loads(path.read_text(encoding="utf-8")).get("run", {}).get("stats", {})

    def n(section: str, field: str) -> int:
        return int((stats.get(section) or {}).get(field) or 0)

    return {
        "requests": n("requests", "total"),
        "unanswered": n("requests", "failed"),
        "assertions": n("assertions", "total"),
        "failed": n("assertions", "failed"),
        "script_failed": n("testScripts", "failed") + n("prerequestScripts", "failed"),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--shard", required=True)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    shard = next((s for s in manifest["shards"] if s["id"] == args.shard), None)
    if shard is None:
        raise SystemExit(f"FATAL: шарда '{args.shard}' нет в {MANIFEST}")

    v = {
        "shard": args.shard,
        "suites": shard["suites"],
        "expected": 0, "reported": 0, "missing": [], "empty": [],
        "requests": 0, "unanswered": 0, "assertions": 0,
        "failed": 0, "script_failed": 0,
        "per_collection": {},
    }

    pre = precondition()
    if pre:
        v["precondition"] = pre

    for svc in shard["suites"]:
        for stem in tracked_stems(svc):
            key = f"{svc}/{stem}"
            v["expected"] += 1
            rp = suite_dir(svc) / "out" / f"{stem}.json"
            if not rp.is_file():
                v["missing"].append(key)
                continue
            try:
                st = read_report(rp)
            except Exception as exc:  # нечитаемый отчёт — это НЕ «ноль падений»
                v["missing"].append(f"{key} (отчёт нечитаем: {exc.__class__.__name__})")
                continue
            v["reported"] += 1
            v["per_collection"][key] = st
            for f in ("requests", "unanswered", "assertions", "failed", "script_failed"):
                v[f] += st[f]
            if st["assertions"] == 0:
                v["empty"].append(key)

    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(v, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    print(f"[shard {args.shard}] суиты: {' '.join(shard['suites'])}")
    if pre:
        print(f"[shard {args.shard}] УСЛОВИЕ НЕ СОЗДАНО: {pre['detail']} — "
              f"пробы не выполнялись вовсе; числа ниже это не вердикт о них")
    print(f"[shard {args.shard}] коллекций отчиталось {v['reported']} из {v['expected']}; "
          f"запросов {v['requests']}, утверждений {v['assertions']}, "
          f"УПАВШИХ {v['failed']}, БЕЗ ОТВЕТА {v['unanswered']}, "
          f"СКРИПТ УПАЛ {v['script_failed']}, ПУСТЫХ ОТЧЁТОВ {len(v['empty'])}")
    if v["missing"]:
        print(f"[shard {args.shard}] БЕЗ ОТЧЁТА ({len(v['missing'])}): {', '.join(v['missing'])}")
    print(f"[shard {args.shard}] опись записана: {out}")

    # ─── ИСХОД РАБОТЫ. Ровно одна метка, и порядок ветвей несущий ───────────
    #
    # ПУСТОЙ ОБХОД РАНЬШЕ ВСЕГО. «Ожидали 0, отчиталось 0» сходится с любым
    # утверждением и потому не утверждает ничего; поставив эту ветвь после
    # несозданного условия, мы получили бы объяснение вместо отказа — более
    # утешительную историю на месте слепого прибора.
    if v["expected"] == 0:
        print(f"{VERDICT_EMPTY} — коллекций суит {' '.join(shard['suites'])} в дереве "
              f"не нашлось ни одной. Судить не по чему: «0 из 0» сходится с любым "
              f"утверждением, поэтому это ОТКАЗ, а не зелёное. Чинится там, где "
              f"порождаются коллекции, а не здесь.")
        return EXIT_EMPTY

    # УСЛОВИЕ НЕ СОЗДАНО ⇒ КРАСНОЕ (решение владельца 2026-09-12). Гейты суит
    # при отметке ПРОПУЩЕНЫ все до одного — значит вердикта о пробах не выносит
    # ни один шаг работы, и «ничего не упало» здесь означало «ничего не
    # спрашивали». Прежде работа при этом уходила ЗЕЛЁНОЙ.
    if pre:
        print(f"{VERDICT_UNMET} · форма «{pre['kind']}» · наблюдатель "
              f"{pre['producer']}")
        print(f"    причина: {pre['detail']}")
        print(f"    вердикта о пробах нет НИ У ОДНОГО шага этой работы: гейты суит "
              f"пропущены отметкой {FLAG}, коллекций исполнено "
              f"{v['reported']} из {v['expected']}. Дефекта продукта здесь НЕ "
              f"показано — это ТРЕТЬЯ КАТЕГОРИЯ, и работа краснеет именно поэтому: "
              f"зелёное означало бы «проверено и чисто».")
        print(f"    что делать: снять названную причину и ПОВТОРИТЬ прогон. Разбирать "
              f"как упавшую пробу нечего — ни одна не исполнялась.")
        return EXIT_UNMET

    # ПРОХОД ТРЕБУЕТ ПРОЧИТАННОГО, а не отсутствия падений: `expected > 0`
    # проверено ветвью выше, и снять её значит вернуть зелёное на пустом обходе.
    print(f"{VERDICT_GATES}: условие создано, коллекций прочитано "
          f"{v['reported']} из {v['expected']}. Опись вердикта о пробах не выносит — "
          f"он у гейтов суит выше, и второй ответ здесь дал бы два разных ответа "
          f"на один вопрос.")
    return 0


# ─────────────────────────────────────────────────────────────────────────────
# САМОПРОВЕРКА: РАСПОЗНАВАТЕЛЬ ЗНАЕТ ВСЕ ФОРМЫ, А КРАСНОЕ ДОЕЗЖАЕТ ДО РАБОТЫ
#
# Предмет самопроверки — не «написано ли», а ТРИ вещи, каждая из которых
# ломается молча:
#
#   1. форм несозданного условия в дереве ДВЕ, и вторая была невидима. Отметку
#      `<отметка>=1` кладут ДВА производителя: владелец подъёма
#      (при отметке-файле постороннего источника чартов) и вердикт провенанса
#      (когда стенд исполняет ДРУГОЕ дерево). Второй файла не оставляет — и
#      распознаватель, ищущий только файл, на нём МОЛЧАЛ. Наблюдалось на прогоне
#      34694955309: свод сказал «НЕ ВЫПОЛНИЛОСЬ … 0 коллекций из 57» и НЕ сказал
#      «условие не создано», потому что ни одна опись этого не несла;
#   2. перечень производителей ВЫВОДИТСЯ ИЗ ДЕРЕВА, а не выписан. Третий
#      производитель, о котором распознаватель не рассуждал, обязан давать
#      находку, а не молчание;
#   3. краснота скрипта становится краснотой РАБОТЫ только если шаг её не глотает.
#      `continue-on-error`, `|| true`, снятое условие — и ложное зелёное
#      возвращается, ничем этого не показав.
#
# Прогонов на каждую ось три, а не два: контроль · инъекция нового · инъекция
# существующего. Без третьего молчание прежней формы неотличимо от её смерти.
WORKFLOW = REPO / ".github" / "workflows" / "e2e-newman.yml"

# Производители отметки, О КОТОРЫХ РАСПОЗНАВАТЕЛЬ РАССУЖДАЛ, и форма каждого.
# Набор сверяется с деревом В ОБЕ СТОРОНЫ: запись без производителя переживает
# свой предмет, производитель без записи — слепая зона.
FLAG_PRODUCERS = {
    ".github/scripts/stand-up.sh": "external-chart-source",
    ".github/scripts/stand-revision-verdict.sh": "stand-revision-divergence",
}

# ПРОИЗВОДИТЕЛЬ СУДИТСЯ ПО СУЩЕСТВУ, А НЕ ПО ТЕКСТУ, и это не вкус: перечень,
# выведенный поиском по слову, ловил ЧЕТЫРЕ вида законной записи — фикстуру
# соседней полосы, синтетику в дереве проб, вызов самопроверки и собственную
# прозу. Замер на подставном дереве: перечень 6 при производителях 2.
#
# Раковина — то, через что отметка попадает в ОКРУЖЕНИЕ ЗАДАНИЯ, и таких мест у
# платформы два: `$GITHUB_ENV` (переменная следующих шагов) и `$GITHUB_OUTPUT`
# (выход шага, объявляемый в `outputs`). Запись в раковину отличается от
# упоминания раковины оператором ДОПИСЫВАНИЯ: `>>` в оболочке, режим `"a"` в
# питоне. Слово `GITHUB_ENV` без него стоит в дереве в условиях шагов, в
# объяснениях и в читателях отметки.
SINK = re.compile(r"GITHUB_(?:ENV|OUTPUT)")
APPEND = re.compile(r">>|['\"]a['\"]")
# Вызов файла В ПОЗИЦИИ КОМАНДЫ: имя ИЗВЛЕКАЕТСЯ из строки, а не ищется по
# каждому пути дерева. Обе формы записи одним выражением, и ИНТЕРПРЕТАТОР ПЕРЕД
# ПУТЁМ ОБЯЗАТЕЛЕН — цена его отсутствия измерена на этом дереве: вызовов 4
# вместо 7, производителей 1 вместо 2, и терялся ровно
# `bash "$GITHUB_WORKSPACE/…/stand-revision-verdict.sh"`, то есть второй
# производитель целиком.
#
# Извлечение, а не перебор — тоже замер, а не вкус: перебор 6692 путей по 1206
# строкам тела шагов не уложился в 120 с и был снят как непригодный, а не как
# некрасивый.
CALL = re.compile(r"(?:^|[;&|(]|\$\()[ \t]*"
                  r"(?:(?:bash|sh|python3|python)[ \t]+)?"
                  r"([^\s;&|()]+)")


def _code_lines(text: str) -> list[str]:
    """Строки БЕЗ комментариев: `#` в оболочке, YAML и питоне, `//` в Go и TS.

    Комментарий отброшен ДО поиска, а не после: собственное объяснение предмета
    иначе становится находкой о предмете. В дереве это уже наблюдалось —
    `.github/scripts/stand-up.sh` попадал в перечень строкой шапки, где оба слова
    стоят рядом, и промах был невидим лишь потому, что файл и без неё производитель.
    """
    out: list[str] = []
    for line in text.splitlines():
        bare = line.lstrip()
        if bare.startswith("#") or bare.startswith("//"):
            continue
        out.append(line)
    return out


def _sets_flag(text: str) -> bool:
    """Файл САМ дописывает отметку в раковину окружения задания."""
    return any(f"{FLAG}=1" in l and SINK.search(l) and APPEND.search(l)
               for l in _code_lines(text))


def _declares_flag(doc: dict) -> bool:
    """Объявление конвейера ставит отметку КАРТОЙ `env:` — на любом из трёх уровней."""
    def has(node: object) -> bool:
        env = node.get("env") if isinstance(node, dict) else None
        return isinstance(env, dict) and FLAG in env

    if has(doc):
        return True
    for job in ((doc.get("jobs") or {}) if isinstance(doc, dict) else {}).values():
        if has(job):
            return True
        for step in (job.get("steps") or []) if isinstance(job, dict) else []:
            if has(step):
                return True
    return False


def _tracked(root: pathlib.Path) -> list[str]:
    r = subprocess.run(["git", "-C", str(root), "ls-files", "-z"],
                       capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"FATAL: git ls-files не отработал: {r.stderr.strip()}")
    return [f for f in r.stdout.split("\0") if f]


def _read(root: pathlib.Path, rel: str) -> str | None:
    # Отказ чтения НЕ глушится в ответ о ФОРМЕ (см. ниже), но здесь он означает
    # «не текст»: двоичный файл отметки не дописывает, и `None` отличимо от «нет».
    try:
        return (root / rel).read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError):
        return None


def _workflow_docs(root: pathlib.Path) -> list[tuple[str, dict]]:
    """Объявления конвейера. Каталог фиксирован ПЛАТФОРМОЙ, а не моим перечнем:
    GitHub Actions читает только `.github/workflows/`, и `jobs:` обязателен."""
    import yaml  # локально: обычный проход описи разбора YAML не требует
    out: list[tuple[str, dict]] = []
    for rel in _tracked(root):
        if not rel.startswith(".github/workflows/") or not rel.endswith((".yml", ".yaml")):
            continue
        text = _read(root, rel)
        if text is None:
            continue
        try:
            doc = yaml.safe_load(text) or {}
        except yaml.YAMLError:
            # Неразбираемое объявление — НЕ «объявления нет»: пропустить его молча
            # значило бы ответить «производителей нет» там, где их не прочитали.
            raise SystemExit(f"FATAL: объявление конвейера не разобралось: {rel}")
        if isinstance(doc, dict) and isinstance(doc.get("jobs"), dict):
            out.append((rel, doc))
    return out


def _step_run_lines(docs: list[tuple[str, dict]]) -> list[str]:
    """Строки, которые конвейер ИСПОЛНЯЕТ. САМОПРОВЕРКА — НЕ ПРОИЗВОДСТВО.

    Единица счёта — строка вызова, и цена оговорки измерена: без неё на этом
    дереве вызовов 9 вместо 7, и два лишних — `stand-up.sh --self-test`, который
    работает в своём временном дереве и в `$GITHUB_ENV` работы не пишет ничего.
    Оговорка судит СТРОКУ: перенос `--self-test` на продолжение она не увидит.

    Судится РАЗОБРАННОЕ тело шага, а не текст файла, и это тоже замер: тот же
    предикат по тексту `.github/workflows/*` дал 2 вместо 7 — в файле перед
    вызовом стоит `run:`, и позиция команды у строки другая.
    """
    out: list[str] = []
    for _, doc in docs:
        for job in (doc.get("jobs") or {}).values():
            for step in (job.get("steps") or []) if isinstance(job, dict) else []:
                if not isinstance(step, dict):
                    continue
                for line in str(step.get("run") or "").splitlines():
                    if "--self-test" in line:
                        continue
                    out.append(line)
    return out


def _runs_of(lines: list[str], names: dict[str, list[str]]) -> set[str]:
    """Пути, ЗАПУСКАЕМЫЕ этими строками. Отбор по имени файла в позиции команды.

    Отбор по ИМЕНИ, а не по полному пути: в теле шага путь приходит с
    `$GITHUB_WORKSPACE`, с `./` и без, и сверка полных путей молча теряла бы
    ровно те формы, ради которых предикат и заведён. Цена сказана прямо: два
    файла с одинаковым именем в разных каталогах считаются запускаемыми оба.
    """
    hit: set[str] = set()
    for line in lines:
        for token in CALL.findall(line):
            name = token.strip('"\'`').rsplit("/", 1)[-1]
            hit.update(names.get(name, ()))
    return hit


def flag_producers_in_tree(root: pathlib.Path | None = None) -> dict[str, bool]:
    """путь → оставляет ли он ещё и отметку-ФАЙЛ. ВЫВЕДЕНО из дерева ПО СУЩЕСТВУ.

    Производитель — тот, кто ВЫСТАВЛЯЕТ отметку в окружение задания, и обе
    половины признака обязательны:

      пишет      — непрокомментированная строка дописывает `<отметка>=1` в
                   `$GITHUB_ENV`/`$GITHUB_OUTPUT`, либо объявление несёт её
                   картой `env:`;
      исполняется — конвейер файл ЗАПУСКАЕТ: тело шага зовёт его прямо, или
                   зовёт того, кто зовёт его (один переход), или файл САМ есть
                   объявление конвейера — тогда запускать его некому, он и есть
                   запуск.

    ФИКСТУРА ОТСЕКАЕТСЯ ВТОРОЙ ПОЛОВИНОЙ, А НЕ ПЕРЕЧНЕМ ПУТЕЙ. Перечень
    исключений разошёлся бы с деревом молча — тот же класс, что и текстовый
    поиск. Свойство же проверяет сам конвейер: синтетика, доказывающая падение
    ЧУЖОЙ проверки, законно кладёт отметку и законно не запускается ни одним
    шагом, поэтому на ЭТОМ прогоне выставить её не может.

    ОДИН ПЕРЕХОД, А НЕ НОЛЬ: производитель, которого шаг зовёт через обёртку,
    иначе выпал бы из перечня — то есть починка текстового поиска завела бы
    молчание в другую сторону. Глубже одного перехода — названный остаток:
    на этом дереве оба производителя зовутся ПРЯМО (вызовов 3 и 4).

    ЧЕГО ПРИЗНАК НЕ РАЗЛИЧАЕТ, сказано прямо. Файл, НЕСУЩИЙ производящую
    команду как ДАННЫЕ — фикстура в строковой константе, — от файла, который её
    ИСПОЛНЯЕТ, статически неотличим: у обоих это текст. Наблюдалось на себе:
    выписав фикстуры буквально, я сделал производителем ЭТУ опись — и её же
    назвал производителем гейт соседней полосы, потому что правило у него то же.
    Поэтому фикстуры здесь СОСТАВЛЯЮТСЯ из имени отметки, а не выписываются, и
    буквальных написаний `<отметка>=1` в этом файле ноль. Держит это не обещание:
    носитель, попавший в перечень, даёт ПРОВАЛ двусторонней сверки — громко, в
    первом же прогоне, как и случилось со мной.
    """
    root = REPO if root is None else root
    tracked = _tracked(root)
    docs = _workflow_docs(root)

    # ── половина «пишет» ────────────────────────────────────────────────────
    cand: set[str] = set()
    for rel in tracked:
        text = _read(root, rel)
        if text is not None and _sets_flag(text):
            cand.add(rel)
    decl = {rel for rel, doc in docs if _declares_flag(doc)}
    cand |= decl

    # ── половина «исполняется» ──────────────────────────────────────────────
    names: dict[str, list[str]] = {}
    for rel in tracked:
        names.setdefault(rel.rsplit("/", 1)[-1], []).append(rel)
    direct = _runs_of(_step_run_lines(docs), names)
    hop: list[str] = []
    for rel in sorted(direct):
        text = _read(root, rel)
        if text is not None:
            hop.extend(l for l in _code_lines(text) if "--self-test" not in l)
    runnable = direct | _runs_of(hop, names)

    out: dict[str, bool] = {}
    for rel in sorted(cand):
        if rel not in runnable and rel not in {r for r, _ in docs}:
            continue
        # Отказ чтения НЕ глушится: `except` здесь превратил бы неизвестность в
        # «отметки-файла не кладёт», то есть в тихий ответ о форме. Проверено
        # собой: первая редакция этой строки маскировала NameError и отвечала
        # «нет» — ось формы покраснела, и только двусторонняя сверка это назвала.
        out[rel] = MARK_NAME in (root / rel).read_text(encoding="utf-8")
    return out


def roster_findings(tree: dict[str, bool], declared: dict[str, str]) -> list[str]:
    """Расхождения перечня с объявленным набором, В ОБЕ СТОРОНЫ.

    Вынесено из тела самопроверки в отдельное выражение не для порядка, а чтобы
    двусторонняя сверка была ДОКАЗУЕМА ИНЪЕКЦИЕЙ: в дереве расхождений ноль, и
    пока сверка жила внутри печати, её собственное молчание было неотличимо от
    её смерти.
    """
    out: list[str] = []
    for path, with_mark in sorted(tree.items()):
        want = declared.get(path)
        if want is None:
            out.append(f"производителя '{path}' нет в FLAG_PRODUCERS — его форма даст МОЛЧАНИЕ")
            continue
        expect = "external-chart-source" if with_mark else "stand-revision-divergence"
        if want != expect:
            out.append(f"'{path}': запись говорит «{want}», дерево — «{expect}»")
    for path in declared:
        if path not in tree:
            out.append(f"запись '{path}' производителя в дереве НЕ имеет — снимите её")
    return out


# Подставное дерево со ВСЕМИ законными формами предмета — и с четырьмя видами
# законной записи, которые производителями НЕ являются. Форм записи отметки
# четыре, форм вызова файла четыре; в настоящем дереве живут по две, поэтому
# остальные держатся ИНЪЕКЦИЕЙ, а не обещанием.
ROSTER_PROBE_FILES: dict[str, str] = {
    # ── форма записи A: дописывание в `$GITHUB_ENV` (живёт в дереве) ─────────
    ".github/scripts/env-producer.sh":
        f'#!/usr/bin/env bash\necho "{FLAG}=1" >> "$GITHUB_ENV"\n',
    # ── форма записи B: выход шага, объявляемый в `outputs` ──────────────────
    ".github/scripts/out-producer.sh":
        f'#!/usr/bin/env bash\necho "{FLAG}=1" >> "$GITHUB_OUTPUT"\n',
    # ── форма записи D: дописывание из питона режимом «a» ────────────────────
    ".github/scripts/py-producer.py":
        f'import os\nopen(os.environ["GITHUB_ENV"], "a").write("{FLAG}=1\\n")\n',
    # обёртка: её зовёт шаг, она зовёт производителя — ФОРМА ВЫЗОВА «один переход»
    ".github/scripts/wrapper.sh":
        '#!/usr/bin/env bash\npython3 .github/scripts/py-producer.py\n',
    # ── НЕ производитель: единственный вызов в конвейере — самопроверка ──────
    ".github/scripts/selftest-only.sh":
        f'#!/usr/bin/env bash\necho "{FLAG}=1" >> "$GITHUB_ENV"\n',
    # ── НЕ производитель: собственная проза, оба слова в одной строке ────────
    ".github/scripts/prose.sh":
        f'#!/usr/bin/env bash\n# $GITHUB_ENV  {FLAG}=1 >> — шаги ниже гасятся\ntrue\n',
    # ── НЕ производитель: законная фикстура соседней полосы. Конвейер её не
    #    запускает НИ ОДНИМ вызовом, поэтому на ЭТОМ прогоне выставить отметку
    #    она не может — при том что кладёт её совершенно законно.
    "deploy/scripts/fixture-inject.sh":
        f'#!/usr/bin/env bash\necho "{FLAG}=1" >> "$GITHUB_ENV"\n',
    # ── НЕ производитель: синтетика в дереве проб ────────────────────────────
    "internal/repohygiene/fixture.go":
        f'package repohygiene\n\nconst inj = "echo {FLAG}=1 >> \\"$GITHUB_ENV\\""\n',
    # ── форма вызова: объявление конвейера кладёт отметку ТЕЛОМ ШАГА ─────────
    ".github/workflows/inline.yml":
        'name: inline\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n'
        f'      - name: inline\n        run: echo "{FLAG}=1" >> "$GITHUB_ENV"\n',
    # ── форма записи C: объявление несёт отметку КАРТОЙ `env:` ───────────────
    ".github/workflows/envmap.yml":
        'name: envmap\njobs:\n  j:\n    runs-on: ubuntu-latest\n'
        '    env:\n      STAND_PRECONDITION_UNMET: 1\n    steps:\n'
        '      - name: noop\n        run: "true"\n',
    # ── объявление, которое всех и запускает. Само отметки не кладёт ─────────
    ".github/workflows/run.yml":
        'name: run\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n'
        '      - name: bare\n        run: .github/scripts/env-producer.sh\n'
        '      - name: interp\n        run: bash'
        ' "$GITHUB_WORKSPACE/.github/scripts/out-producer.sh"\n'
        '      - name: hop\n        run: bash .github/scripts/wrapper.sh\n'
        '      - name: selftest\n        run: bash .github/scripts/selftest-only.sh --self-test\n'
        '      - name: prose\n        run: bash .github/scripts/prose.sh\n',
}
# Кого перечень ОБЯЗАН назвать. Остальные файлы подставного дерева — законная
# запись, производителями не являющаяся; их отсутствие проверяется отдельно.
ROSTER_PROBE_WANT = {
    ".github/scripts/env-producer.sh": "запись в $GITHUB_ENV · вызов голым путём",
    ".github/scripts/out-producer.sh": "запись в $GITHUB_OUTPUT · вызов через интерпретатор",
    ".github/scripts/py-producer.py": "запись из питона режимом «a» · вызов через один переход",
    ".github/workflows/inline.yml": "запись телом шага · файл САМ есть объявление",
    ".github/workflows/envmap.yml": "запись картой env: · файл САМ есть объявление",
}
ROSTER_PROBE_DENY = {
    ".github/scripts/selftest-only.sh": "зовётся только как `--self-test`",
    ".github/scripts/prose.sh": "проза: оба слова в строке, записи нет",
    "deploy/scripts/fixture-inject.sh": "законная фикстура соседней полосы: конвейер её не зовёт",
    "internal/repohygiene/fixture.go": "синтетика в дереве проб",
}


def _roster_tree(root: pathlib.Path, files: dict[str, str]) -> None:
    for rel, text in files.items():
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")
    subprocess.run(["git", "init", "-q"], cwd=root, check=True, capture_output=True)
    # Пути перечислены ЯВНО: `-A` в общей рабочей копии запрещён, и привычка
    # сохраняется здесь, чтобы не переехала обратно копированием.
    subprocess.run(["git", "add", ".github", "deploy", "internal"],
                   cwd=root, check=True, capture_output=True)


def verdict_steps() -> list[dict]:
    """Шаги объявления работы `shard`, запускающие ЭТУ опись. Выведены из дерева."""
    import yaml  # локально: обычный проход описи разбора YAML не требует
    doc = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8")) or {}
    steps = list(((doc.get("jobs") or {}).get("shard") or {}).get("steps") or [])
    return [s for s in steps if "shard-verdict.py" in str(s.get("run") or "")]


def step_swallows_red(step: dict) -> list[str]:
    """Чем шаг мог бы проглотить ненулевой код. Пусто — не проглотит."""
    why: list[str] = []
    if str(step.get("continue-on-error") or "").lower() in ("true", "yes", "on"):
        why.append("continue-on-error")
    run = str(step.get("run") or "")
    for token in ("|| true", "|| exit 0", "; exit 0", "set +e", "|| :"):
        if token in run:
            why.append(f"«{token}» в теле")
    if not str(step.get("if") or "").strip():
        why.append("условия нет вовсе — шаг не дойдёт до упавшей работы")
    return why


def _probe_tree(tmp: pathlib.Path, *, collections: int = 2) -> pathlib.Path:
    """Подставное дерево: настоящий скрипт, синтетический манифест и суита.

    Настоящее дерево трогать нельзя — опись читает отчёты по путям суит, и
    проба, кладущая их туда, оставляла бы следы в рабочей копии. Скрипт при этом
    КОПИРУЕТСЯ, а не переписывается: судится то, что поедет в конвейер.
    """
    (tmp / ".github" / "scripts").mkdir(parents=True)
    (tmp / "deploy" / "helm" / "umbrella").mkdir(parents=True)
    suite = tmp / "services" / "geo" / "tests" / "newman"
    (suite / "collections").mkdir(parents=True)
    (suite / "out").mkdir(parents=True)
    shutil.copy2(pathlib.Path(__file__).resolve(), tmp / ".github" / "scripts" / "shard-verdict.py")
    (tmp / "deploy" / "e2e-shards.json").write_text(
        json.dumps({"shards": [{"id": "probe", "suites": ["geo"]}]}), encoding="utf-8")
    for i in range(collections):
        (suite / "collections" / f"c{i}{SUFFIX}").write_text("{}", encoding="utf-8")
    subprocess.run(["git", "init", "-q"], cwd=tmp, check=True, capture_output=True)
    # Пути перечислены ЯВНО: `-A` в общей рабочей копии запрещён, и привычка
    # сохраняется здесь, чтобы не переехала обратно копированием.
    subprocess.run(["git", "add", "services", "deploy", ".github"],
                   cwd=tmp, check=True, capture_output=True)
    return suite


def _report(failed: int = 0, assertions: int = 4, requests: int = 4,
            unanswered: int = 0, scripts: int = 0) -> str:
    return json.dumps({"run": {"stats": {
        "requests": {"total": requests, "failed": unanswered},
        "assertions": {"total": assertions, "failed": failed},
        "testScripts": {"total": requests, "failed": scripts},
        "prerequestScripts": {"total": requests, "failed": 0},
    }}})


def _run_probe(tmp: pathlib.Path, env_flag: bool) -> tuple[int, str, dict]:
    env = dict(os.environ)
    env.pop(FLAG, None)
    if env_flag:
        env[FLAG] = "1"
    out = tmp / "shard-verdicts" / "shard-verdict-probe.json"
    p = subprocess.run([sys.executable, str(tmp / ".github" / "scripts" / "shard-verdict.py"),
                        "--shard", "probe", "--out", str(out)],
                       cwd=tmp, env=env, capture_output=True, text=True)
    written: dict = {}
    if out.is_file():
        written = json.loads(out.read_text(encoding="utf-8"))
    return p.returncode, p.stdout + p.stderr, written


def _self_test() -> int:
    ok = True
    seen = {"производителей": 0, "шагов-вызовов": 0, "прогонов описи": 0}

    def say(good: bool, label: str, detail: str = "") -> None:
        nonlocal ok
        ok = ok and good
        print(f"  {'ОК    ' if good else 'ПРОВАЛ'} {label}"
              + (f" — {detail}" if detail and not good else ""))

    print("=== shard-verdict.py --self-test ===")

    # ── ось 1: КОНТРОЛЬ — перечень выведен из дерева ПО СУЩЕСТВУ ─────────────
    print("  --- формы отметки: перечень выведен из дерева, сверен в обе стороны")
    tree = flag_producers_in_tree()
    seen["производителей"] = len(tree)
    say(bool(tree), f"производителей отметки {FLAG} в дереве: {len(tree)}",
        "обход пуст — «все формы известны» значило бы «ни одна не прочитана»")
    for path, with_mark in sorted(tree.items()):
        print(f"         '{path}' — отметка-файл: {'да' if with_mark else 'нет'}")
    findings = roster_findings(tree, FLAG_PRODUCERS)
    say(not findings,
        f"перечень сошёлся с FLAG_PRODUCERS в обе стороны "
        f"(дерево {len(tree)}, запись {len(FLAG_PRODUCERS)})",
        "; ".join(findings))

    # ── ось 1б: ИНЪЕКЦИЯ распознавателя — все формы, и ни одна лишняя ────────
    #
    # ПРЕДМЕТ. Перечень выводился ПОИСКОМ ПО ТЕКСТУ, и текст не отличает того,
    # кто отметку СТАВИТ, от того, кто её лишь называет. Замер на этом же
    # подставном дереве: прежний предикат давал перечень из ШЕСТИ при
    # производителях ДВУХ — лишними были законная фикстура соседней полосы,
    # синтетика дерева проб, вызов самопроверки и собственный комментарий.
    #
    # Инъекция обязательна в обе стороны: без неё «фикстура не попала» неотличимо
    # от «не попал никто», а «производитель попал» — от «попадают все».
    print("  --- инъекция распознавателя: каждая форма доказана, лишних нет")
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        _roster_tree(tmp, ROSTER_PROBE_FILES)
        got = flag_producers_in_tree(tmp)
        seen["форм записи"] = 4
        seen["форм вызова"] = 4
        for path, form in sorted(ROSTER_PROBE_WANT.items()):
            say(path in got, f"производитель попал в перечень: {form}",
                f"'{path}' не найден — эта форма даёт МОЛЧАНИЕ; в перечне: {sorted(got)}")
        for path, why in sorted(ROSTER_PROBE_DENY.items()):
            say(path not in got, f"в перечень НЕ попал: {why}",
                f"'{path}' назван производителем — отладку пошлёт не туда")
        say(set(got) == set(ROSTER_PROBE_WANT),
            f"перечень подставного дерева — РОВНО производители ({len(got)} из "
            f"{len(ROSTER_PROBE_FILES)} файлов)", f"в перечне: {sorted(got)}")
        # Двусторонняя сверка ДОЛЖНА находить, а не молчать: производители
        # подставного дерева в FLAG_PRODUCERS не объявлены ни один.
        inj = roster_findings(got, FLAG_PRODUCERS)
        say(len(inj) == len(got) + len(FLAG_PRODUCERS),
            f"сверка назвала {len(inj)} расхождений: {len(got)} незаявленных "
            f"производителя и {len(FLAG_PRODUCERS)} записи без предмета",
            f"нашла: {inj}")

    # ── ось 1в: ИНЪЕКЦИЯ СУЩЕСТВУЮЩЕГО — прежняя форма ещё жива ──────────────
    #
    # Фикстура соседа остаётся на месте, а у НАСТОЯЩЕГО производителя снимается
    # строка записи. Без этого прогона молчание живой формы неотличимо от её
    # смерти: «фикстура не попала» верно и у предиката, который не находит НИЧЕГО.
    print("  --- инъекция существующего: снят производитель ⇒ перечень краснеет")
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        files = dict(ROSTER_PROBE_FILES)
        files[".github/scripts/env-producer.sh"] = "#!/usr/bin/env bash\ntrue\n"
        _roster_tree(tmp, files)
        got = flag_producers_in_tree(tmp)
        say(".github/scripts/env-producer.sh" not in got,
            "снятый производитель из перечня ушёл — предикат не всегда-истинен",
            f"в перечне: {sorted(got)}")
        say("deploy/scripts/fixture-inject.sh" not in got,
            "фикстура соседа не попала и теперь — при ЖИВОМ остальном перечне",
            f"в перечне: {sorted(got)}")
        say(len(got) == len(ROSTER_PROBE_WANT) - 1,
            f"перечень стал {len(got)} вместо {len(ROSTER_PROBE_WANT)} — остальные формы живы",
            f"в перечне: {sorted(got)}")
        was = {".github/scripts/env-producer.sh": "external-chart-source"}
        gone = [f for f in roster_findings(got, was) if "НЕ имеет" in f]
        say(len(gone) == 1,
            "сверка назвала запись, потерявшую производителя — это находка, не тишина",
            f"нашла: {gone}")

    # ── ось 2: контроль ──────────────────────────────────────────────────────
    print("  --- контроль: отметки нет, отчёты на местах, пробы зелёные")
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        suite = _probe_tree(tmp)
        (suite / "out" / "c0.json").write_text(_report(), encoding="utf-8")
        (suite / "out" / "c1.json").write_text(_report(), encoding="utf-8")
        rc, out, v = _run_probe(tmp, env_flag=False)
        seen["прогонов описи"] += 1
        say(rc == 0, f"опись вышла нулём (код {rc})", out[-500:])
        say(int(v.get("expected") or 0) > 0 and v.get("reported") == v.get("expected"),
            f"прочитано коллекций {v.get('reported')} из {v.get('expected')} — обход не пуст")
        say("precondition" not in v, "отметки в описи нет — условие создано")
        say(VERDICT_GATES in out and VERDICT_UNMET not in out,
            "напечатана метка «вердикт выносят гейты суит», и только она")

    # ── ось 3: инъекция НОВОГО — форма без файла (расхождение ревизий) ───────
    print("  --- инъекция нового: отметка в окружении, файла нет ⇒ КРАСНОЕ")
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        suite = _probe_tree(tmp)
        rc, out, v = _run_probe(tmp, env_flag=True)
        seen["прогонов описи"] += 1
        say(rc != 0, f"опись покраснела (код {rc})", "исход работы остался бы зелёным")
        say(rc == 75, f"код {rc} — машинный признак третьей категории",
            "ждали 75: тот же признак, что у классификатора посадки")
        say(VERDICT_UNMET in out, "текст назвал несозданное условие", out[-500:])
        say("вердикта о пробах нет" in out,
            "текст сказал, что вердикта о пробах НЕТ", out[-500:])
        say((v.get("precondition") or {}).get("kind") == "stand-revision-divergence",
            "опись несёт форму «stand-revision-divergence» — свод её прочтёт",
            f"в описи: {v.get('precondition')}")
        # ТЕКСТ ОТКАЗА НАЗЫВАЕТ НАБЛЮДАТЕЛЯ ЛИТЕРАЛОМ, а перечень выведен из
        # дерева. Связь между ними держится здесь: литерал, разошедшийся с
        # деревом, посылает отладку не туда — и это ровно то, чем красное
        # отличается от красного. Молча разойтись они больше не могут.
        who = (v.get("precondition") or {}).get("producer")
        say(who in tree, f"наблюдатель «{who}» из текста отказа есть в выведенном перечне",
            f"в перечне: {sorted(tree)}")
        say(bool(who) and who in out, "текст отказа назвал наблюдателя ИМЕНЕМ файла",
            out[-500:])

    # ── ось 4: инъекция СУЩЕСТВУЮЩЕГО — отметка-файл постороннего источника ──
    print("  --- инъекция существующего: отметка-ФАЙЛ ⇒ КРАСНОЕ, причина из файла")
    for with_env in (True, False):
        with tempfile.TemporaryDirectory() as td:
            tmp = pathlib.Path(td)
            suite = _probe_tree(tmp)
            (tmp / "deploy" / "helm" / "umbrella" / MARK_NAME).write_text(
                "источник чартов не ответил: https://charts.example.invalid/x\n", encoding="utf-8")
            rc, out, v = _run_probe(tmp, env_flag=with_env)
            seen["прогонов описи"] += 1
            tail = f"(отметка в окружении: {'есть' if with_env else 'нет'})"
            say(rc == 75, f"код 75 {tail}", f"получено {rc}")
            say((v.get("precondition") or {}).get("kind") == "external-chart-source",
                f"форма «external-chart-source» {tail}", f"в описи: {v.get('precondition')}")
            say("charts.example.invalid" in out,
                f"причина взята ИЗ ФАЙЛА, а не подставлена {tail}", out[-500:])
            who = (v.get("precondition") or {}).get("producer")
            say(who in tree, f"наблюдатель «{who}» есть в выведенном перечне {tail}",
                f"в перечне: {sorted(tree)}")
            say(bool(who) and who in out, f"текст отказа назвал наблюдателя {tail}",
                out[-500:])

    # ── ось 5: законный близнец — пробы УПАЛИ, условие создано ───────────────
    print("  --- законный близнец: пробы упали ⇒ опись НЕ присваивает себе вердикт")
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        suite = _probe_tree(tmp)
        (suite / "out" / "c0.json").write_text(_report(failed=3), encoding="utf-8")
        (suite / "out" / "c1.json").write_text(_report(unanswered=2), encoding="utf-8")
        rc, out, v = _run_probe(tmp, env_flag=False)
        seen["прогонов описи"] += 1
        say(rc == 0, f"опись вышла нулём (код {rc}) — краснеет гейт суиты, а не она",
            "второй вердикт об одном предмете")
        say(VERDICT_UNMET not in out,
            "метки «условие не создано» в тексте НЕТ — два красных различимы", out[-500:])
        say(v.get("failed") == 3 and v.get("unanswered") == 2,
            f"числа доехали до свода: упавших {v.get('failed')}, без ответа {v.get('unanswered')}")

    # ── ось 6: пустой обход роняет ───────────────────────────────────────────
    print("  --- пустой обход: коллекций в дереве ноль ⇒ КРАСНОЕ, а не «0 из 0»")
    with tempfile.TemporaryDirectory() as td:
        tmp = pathlib.Path(td)
        _probe_tree(tmp, collections=0)
        rc, out, v = _run_probe(tmp, env_flag=False)
        seen["прогонов описи"] += 1
        say(rc != 0, f"опись покраснела (код {rc})",
            "«ожидали 0, отчиталось 0» уехало бы зелёным")
        say(VERDICT_EMPTY in out, "метка «опись пуста» — отдельная от двух остальных",
            out[-500:])

    # ── ось 7: метка уникальна в дереве ──────────────────────────────────────
    # ПЕЧАТАЮЩИХ ровно один, и это несущее число. «Два красных различимы по
    # тексту» держится не обещанием, а тем, что метку печатает единственный
    # скрипт: второй печатающий вернул бы догадку. Замерено ИСПОЛНЕНИЕМ, почему
    # одной общей фразы было недостаточно: гейт суиты на упавших пробах печатает
    # `TOTAL(категории): … УСЛОВИЕ НЕ СОЗДАНО 0` — то есть отбор по короткой фразе
    # выбрал бы ОБА красных. Отбор по метке выбирает ровно одно.
    #
    # Документация из счёта исключена намеренно: предмет — печать в журнал
    # прогона, а страница, цитирующая метку, вторым печатающим не становится.
    print("  --- различимость: метку печатает РОВНО один исполняемый файл дерева")
    for mark in (VERDICT_UNMET, VERDICT_EMPTY):
        r = subprocess.run(["git", "-C", str(REPO), "grep", "-l", "--fixed-strings", mark],
                           capture_output=True, text=True)
        found = [l for l in r.stdout.splitlines() if l.strip()]
        say(bool(found), f"метка «{mark[:44]}…» в дереве вообще есть",
            "не нашлось ни одного вхождения — предикат слеп")
        printers = [f for f in found if not f.endswith(".md")]
        say(len(printers) == 1, f"печатающих её файлов {len(printers)} "
            f"(всего вхождений в файлах: {len(found)})",
            f"печатают: {printers or 'никто'}")

    # ── ось 8: краснота доезжает до работы ───────────────────────────────────
    print("  --- распространение: шаг работы `shard` ненулевой код НЕ глотает")
    steps = verdict_steps()
    seen["шагов-вызовов"] = len(steps)
    say(bool(steps), f"шагов, зовущих опись в работе `shard`: {len(steps)}",
        "ни одного — судить распространение не по чему")
    for s in steps:
        why = step_swallows_red(s)
        say(not why, f"шаг «{s.get('name')}» проглотить красное не может",
            "; ".join(why))
    hurt = [dict(s, **{"continue-on-error": True}) for s in steps]
    say(bool(steps) and all(step_swallows_red(h) for h in hurt),
        "инъекция: `continue-on-error: true` → находка")
    hurt2 = [dict(s, run=str(s.get("run") or "") + " || true") for s in steps]
    say(bool(steps) and all(step_swallows_red(h) for h in hurt2),
        "инъекция: «|| true» в теле → находка")

    print("осмотрено: " + ", ".join(f"{k} {n}" for k, n in seen.items()))
    print("самопроверка:", "PASS" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(_self_test() if "--self-test" in sys.argv[1:] else main())
