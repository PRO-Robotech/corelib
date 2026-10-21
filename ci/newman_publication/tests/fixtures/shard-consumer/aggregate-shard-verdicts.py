#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1
"""aggregate-shard-verdicts.py — один ответ про весь прогон после разнесения по раннерам.

ПРЕДМЕТ. До разнесения весь e2e судился в одном job'е: «зелено» означало «этот job
зелёный». После — восемь суит живут в пяти job'ах на пяти кластерах, и «зелено»
становится утверждением, которое НИКТО не делает, если его не сделать здесь.

ЗЕЛЁНЫМ СЧИТАЕТСЯ ТОЛЬКО ТО, ЧТО СОШЛОСЬ ПО ВСЕМ ЧЕТЫРЁМ ПУНКТАМ:
  1. описи пришли от КАЖДОГО шарда манифеста — отсутствие описи это «не
     выполнилось», а «не выполнилось» никогда не вычитается из вердикта и никогда
     не засчитывается за успех (testing.md);
  2. сумма отчитавшихся коллекций равна числу коллекций в дереве — ни одна не
     потеряна и ни одна не задвоена (это и есть проверка, что шардирование не
     стало тихим способом не гонять кейс);
  3. сумма упавших утверждений, неотвеченных запросов и упавших скриптов — ноль;
  4. пустых отчётов (отчёт есть, утверждений ноль) — ноль: «ничего не проверено»
     обязано быть отличимо от «ничего не упало».

ПОЧЕМУ ПУНКТ 2 ОТДЕЛЬНО ОТ ПУНКТА 1. Шард может отчитаться, а внутри него
коллекция — нет. Тогда его собственный гейт краснеет, но если краснота почему-то
не доехала (job снят по таймауту, артефакт не загрузился), сумма по дереву это
всё равно поймает: 81 из 82 — это находка, а не округление.

ИСХОД НАЗЫВАЕТСЯ КАТЕГОРИЕЙ, А НЕ ТОЛЬКО ЧИСЛОМ (2026-08-17). Категорий три —
зелёный, красный, «не выполнилось», — и раньше они печатались неразличимо: и
«упало 11 утверждений», и «стенды не поднимались» приходили одним красным, из
которого нельзя было понять, случилось ли вообще что-нибудь. Первая строка сводки
теперь отвечает на этот вопрос, а «часть дерева не судилась» отделено от «всё
судилось и упало»: это разные вопросы к разным людям.

КАСКАД НАЗЫВАЕТСЯ ОДИН РАЗ. Если стенды не поднимались (план красный ⇒ шарды
`skipped`), пять «шард не отчитался» — СЛЕДСТВИЕ одной находки плана, а не пять
находок. Прогоны 31995227651 и 31995487127 напечатали по восемь находок, из
которых предмет был у одной; разбор начинался с чтения пяти пустых шардов.
Исход при этом остаётся КРАСНЫМ — меняется только то, что названо причиной.
Установить это своими силами свод не может (следов у «не запускался» и у
«исполнялся и не доехал» одинаково ноль), поэтому исходы работ приходят
параметрами `--plan-result` / `--shard-result` из `needs.<job>.result`.

НИЧЕГО НЕ ВЫЧИТАЕТСЯ. Здесь нет ни списка известного красного, ни исключений, ни
маски. Если такой механизм когда-нибудь появится, он обязан истекать сам.

Самопроверка (`--self-test`) синтезирует набор описей и вносит по одному дефекту,
требуя красного на каждом, плюс держит рядом законный полный набор ⇒ зелёный.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]
MANIFEST = REPO / "deploy" / "e2e-shards.json"
SUFFIX = ".postman_collection.json"
GLOBS = ("services/*/tests/newman/collections/*" + SUFFIX,
         "gateway/tests/newman/collections/*" + SUFFIX)


def tree_collection_count(repo: pathlib.Path = REPO) -> int:
    r = subprocess.run(["git", "-C", str(repo), "ls-files", "--", *GLOBS],
                       capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"FATAL: git ls-files не отработал: {r.stderr.strip()}")
    return len([l for l in r.stdout.splitlines() if l.strip()])


# Категории исхода. Их ТРИ, и четвёртой нет: зелёный, красный, «не выполнилось».
# Красный делится ещё на две формы, потому что «упало» и «часть дерева никто не
# судил» — разные вопросы к разным людям, а раньше они печатались одним числом.
OUTCOME_GREEN = "ЗЕЛЁНЫЙ"
OUTCOME_RED = "КРАСНЫЙ"
OUTCOME_PARTIAL = "КРАСНЫЙ · ЧАСТЬ ДЕРЕВА НЕ СУДИЛАСЬ"
OUTCOME_DIDNOTRUN = "НЕ ВЫПОЛНИЛОСЬ"
OUTCOME_PRECOND = "НЕ ВЫПОЛНИЛОСЬ · УСЛОВИЕ НЕ СОЗДАНО"

_OUTCOME_GLOSS = {
    OUTCOME_GREEN: "всё дерево судилось, падений нет",
    OUTCOME_RED: "всё дерево судилось, есть падения — читать «Находки»",
    OUTCOME_PARTIAL: ("часть коллекций НЕ судилась вовсе; их зелёного нет и красного тоже — "
                      "числа ниже относятся к меньшему, чем всё дерево"),
    OUTCOME_DIDNOTRUN: ("вердикта нет НИ У ОДНОЙ ревизии: прогон повторяется, "
                        "а не читается как красный e2e"),
    # ФОРМУ НЕ НАЗЫВАЕМ ЗДЕСЬ, И ЭТО НЕ ОБТЕКАЕМОСТЬ. Здесь стояло «источник чартов
    # не ответил» — единственная форма, известная на день записи. Форм ДВЕ: к ней
    # добавилась «стенд поднялся, но исполняет другое дерево» (отметку ставит
    # вердикт провенанса), и на ней эта строка стала бы ложной, ничем этого не
    # показав. Причину и форму несёт ОПИСЬ ШАРДА, и они печатаются ниже, в находке
    # по каждому шарду: так третья форма приедет названной, а не переименованной.
    OUTCOME_PRECOND: ("вердикт о пробах не выносил НИКТО: условие прогона не создано, "
                      "и причина названа в находках по шардам. Дефекта продукта здесь НЕ "
                      "показано; прогон повторяется, а не разбирается как упавшая проба"),
}


def outcome(tot: dict, findings: list[str]) -> tuple[str, str]:
    """Категория исхода прогона. Ничего не решает — называет то, что уже посчитано.

    Существует потому, что три разных состояния печатались неразличимо: «упало N
    утверждений», «шарды не отчитались» и «стенды не поднимались» приходили одним
    красным, и первым действием при разборе было выяснение, случилось ли вообще
    что-нибудь. Категория отвечает на этот вопрос первой строкой.

    «УСЛОВИЕ НЕ СОЗДАНО» — подвид «не выполнилось», а не поблажка (kacho#655):
    исход остаётся ОТКАЗОМ и зелёным не становится ни при каких числах. Названо
    отдельно потому, что отвечает другому человеку: красное зовёт разбирать
    продукт, а это — повторить прогон либо снять зависимость от чужого хоста.
    Даётся ТОЛЬКО когда нехватку целиком объясняют шарды с отметкой И нет ни
    одного упавшего утверждения: иначе чужая недоступность стала бы способом
    не показывать свои падения.
    """
    if tot.get("precondition") and not tot.get("unexplained") \
            and not (tot["failed"] or tot["unanswered"] or tot["script_failed"] or tot["empty"]):
        return OUTCOME_PRECOND, _OUTCOME_GLOSS[OUTCOME_PRECOND]
    if tot["reported"] == 0 or tot["assertions"] == 0:
        return OUTCOME_DIDNOTRUN, _OUTCOME_GLOSS[OUTCOME_DIDNOTRUN]
    if tot["reported"] < tot["tree"]:
        return OUTCOME_PARTIAL, _OUTCOME_GLOSS[OUTCOME_PARTIAL]
    if findings:
        return OUTCOME_RED, _OUTCOME_GLOSS[OUTCOME_RED]
    return OUTCOME_GREEN, _OUTCOME_GLOSS[OUTCOME_GREEN]


def _precondition_shards(verdicts: dict[str, dict]) -> dict[str, str]:
    """shard-id → «форма + причина», по описям, несущим отметку несозданного условия.

    ФОРМЫ ЗДЕСЬ НЕ ПЕРЕЧИСЛЕНЫ НАМЕРЕННО. Свод печатает то, что назвала опись, и
    ничего не сопоставляет со списком: список из двух известных форм обратил бы
    третью в молчание — ровно тот класс, из-за которого форма «стенд исполняет
    другое дерево» не доезжала до свода вовсе. Форма, не названная описью,
    называется отсутствующей, а не подставляется привычной.
    """
    out: dict[str, str] = {}
    for sid, v in verdicts.items():
        p = v.get("precondition") or {}
        if p.get("unmet"):
            kind = str(p.get("kind") or "").strip() or "ФОРМА НЕ НАЗВАНА описью"
            out[sid] = f"форма «{kind}»; {p.get('detail') or 'причина не названа'}"
    return out


def _stands_never_started(stages: dict[str, str] | None) -> bool:
    """Поднимались ли стенды вообще — по исходу работ, а не по следам их отсутствия.

    Единственный источник — `needs.<job>.result` конвейера. Своими силами свод
    этого установить не может: «описи нет» выглядит одинаково и когда шард
    исполнялся и не доехал, и когда он не запускался вовсе. Первое — находка,
    второе — следствие чужой находки, и путать их дорого.

    Без переданных исходов (локальный вызов, старый вызывающий) возвращает False:
    неизвестность не даёт права объявить каскад.
    """
    if not stages:
        return False
    if (stages.get("shard") or "").strip() == "skipped":
        return True
    plan = (stages.get("plan") or "").strip()
    return bool(plan) and plan != "success"


def decide(manifest: dict, verdicts: dict[str, dict], tree_total: int,
           stages: dict[str, str] | None = None) -> tuple[list[str], dict]:
    """verdicts: shard-id → опись. Возвращает (находки, числа). Ничего не вычитает."""
    findings: list[str] = []
    want = [s["id"] for s in manifest["shards"]]

    # Стенды не поднимались и не пришло НИ ОДНОЙ описи — это один каскад, а не
    # пять находок. Красным исход остаётся (ниже он и остаётся), но причина
    # называется та, что действительно случилась.
    cascade = _stands_never_started(stages) and not verdicts
    if cascade:
        plan = (stages or {}).get("plan") or "?"
        shard = (stages or {}).get("shard") or "?"
        findings.append(
            f"ПРОГОН НЕ СОСТОЯЛСЯ: стенды не поднимались (план: {plan}, шарды: {shard}). "
            f"Ни один из {len(want)} шардов дерева не судил — это «не выполнилось», "
            f"третья категория: ни зелёное, ни красное, и в вердикт засчитывается как ОТКАЗ. "
            f"Причина названа в работе «план шардов + гейты покрытия»; пустая таблица "
            f"ниже — её следствие, а не отдельные находки")

    # УСЛОВИЕ НЕ СОЗДАНО — называется РАНЬШЕ производных чисел и один раз.
    # Шард, чей стенд не поднялся из-за чужого хоста, отчитается нулём коллекций;
    # без этой строки его ноль читался бы как «пробы упали», то есть как дефект
    # продукта, — ровно то, ради чего kacho#655 и заведён. Ничего не вычитается:
    # исход остаётся ОТКАЗОМ, меняется то, ЧТО названо причиной и кому адресовано.
    precond = _precondition_shards(verdicts)
    for sid in sorted(precond):
        findings.append(
            f"УСЛОВИЕ НЕ СОЗДАНО на шарде '{sid}': {precond[sid]}. Пробы не выполнялись "
            f"вовсе, и их ноль ниже НЕ является вердиктом о продукте. Это третья "
            f"категория: ни зелёное, ни красное, в вердикт засчитывается как ОТКАЗ, "
            f"и прогон повторяется. Сама работа шарда при этом КРАСНАЯ — её опись "
            f"выходит кодом 75 (прежде она уходила зелёной, и свод оставался "
            f"единственным, кто об этом говорил)")

    for sid in want:
        if sid not in verdicts and not cascade:
            findings.append(
                f"шард '{sid}' НЕ ОТЧИТАЛСЯ — описи нет. Это «не выполнилось»: "
                f"ни зелёное, ни красное, и в вердикт засчитывается как ОТКАЗ")
    for sid in sorted(set(verdicts) - set(want)):
        findings.append(f"пришла опись шарда '{sid}', которого нет в манифесте — "
                        f"свод считает не то дерево")

    tot = {k: 0 for k in ("expected", "reported", "requests", "unanswered",
                          "assertions", "failed", "script_failed")}
    empty: list[str] = []
    missing: list[str] = []
    for sid in want:
        v = verdicts.get(sid)
        if not v:
            continue
        for k in tot:
            tot[k] += int(v.get(k) or 0)
        empty += list(v.get("empty") or [])
        missing += list(v.get("missing") or [])

    if tot["expected"] != tree_total and not cascade:
        findings.append(f"шарды в сумме ОЖИДАЛИ {tot['expected']} коллекций, "
                        f"а в дереве их {tree_total} — покрытие неполно либо задвоено")
    if tot["reported"] != tree_total and not cascade:
        findings.append(f"ОТЧИТАЛОСЬ {tot['reported']} коллекций из {tree_total} в дереве"
                        + (f"; без отчёта: {', '.join(sorted(missing))}" if missing else ""))
    if tot["failed"]:
        findings.append(f"упавших утверждений: {tot['failed']}")
    if tot["unanswered"]:
        findings.append(f"запросов без ответа: {tot['unanswered']} "
                        f"(«не дозвонились» — не «прошло»)")
    if tot["script_failed"]:
        findings.append(f"упавших скриптов кейсов: {tot['script_failed']} "
                        f"(проверки этих кейсов не исполнялись вовсе)")
    if empty:
        findings.append(f"пустых отчётов (0 утверждений): {len(empty)} — {', '.join(sorted(empty))}")
    if tot["assertions"] == 0 and tree_total and not cascade:
        findings.append("НОЛЬ УТВЕРЖДЕНИЙ ЗА ВЕСЬ ПРОГОН — прочитано ничего; "
                        "это отказ, а не «ничего не упало»")

    tot["shards_expected"] = len(want)
    tot["shards_reported"] = len([s for s in want if s in verdicts])
    tot["tree"] = tree_total
    tot["empty"] = len(empty)
    tot["precondition"] = len(precond)

    # ЧЕМ ОБЪЯСНЕНА НЕХВАТКА. Категория «условие не создано» выдаётся только
    # тогда, когда КАЖДЫЙ недосчитавшийся шард несёт отметку. Один шард с чужой
    # недоступностью не вправе объяснять молчание соседнего — иначе отметка стала
    # бы способом не показывать чужой незапуск.
    tot["unexplained"] = len([
        sid for sid in want
        if sid not in precond and (sid not in verdicts
                                   or int((verdicts[sid] or {}).get("reported") or 0)
                                   < int((verdicts[sid] or {}).get("expected") or 0))
    ])
    return findings, tot


def load(dirpath: pathlib.Path) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for p in sorted(dirpath.rglob("shard-verdict-*.json")):
        try:
            v = json.loads(p.read_text(encoding="utf-8"))
        except Exception as exc:
            print(f"ВНИМАНИЕ: опись {p} нечитаема ({exc.__class__.__name__}) — "
                  f"считается непришедшей", file=sys.stderr)
            continue
        sid = v.get("shard")
        if sid:
            out[sid] = v
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True, help="каталог со скачанными описями шардов")
    ap.add_argument("--summary", default=None, help="файл GITHUB_STEP_SUMMARY (необязательно)")
    # Исходы работ конвейера. Своими силами свод не отличает «шард исполнялся и не
    # доехал» от «шард не запускался вовсе»: следов у обоих одинаково ноль.
    ap.add_argument("--plan-result", default="", help="needs.plan.result")
    ap.add_argument("--shard-result", default="", help="needs.shard.result")
    args = ap.parse_args()

    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    verdicts = load(pathlib.Path(args.dir))
    tree_total = tree_collection_count()
    stages = {"plan": args.plan_result, "shard": args.shard_result}
    findings, tot = decide(manifest, verdicts, tree_total, stages)
    code, gloss = outcome(tot, findings)

    lines = ["## Сводный вердикт e2e (по шардам)", "",
             f"**ИСХОД: {code}** — {gloss}", "",
             f"шардов отчиталось **{tot['shards_reported']} из {tot['shards_expected']}**; "
             f"коллекций **{tot['reported']} из {tot['tree']}** в дереве", "",
             "| шард | суиты | коллекций | запросов | утверждений | УПАЛО | без ответа | скрипт упал |",
             "|---|---|---:|---:|---:|---:|---:|---:|"]
    precond_rows = _precondition_shards(verdicts)
    for s in manifest["shards"]:
        v = verdicts.get(s["id"])
        if not v:
            lines.append(f"| `{s['id']}` | {' '.join(s['suites'])} | **НЕ ОТЧИТАЛСЯ** | — | — | — | — | — |")
            continue
        # Ноль коллекций из-за чужого хоста и ноль из-за упавшего прогона — разные
        # состояния, и строка таблицы обязана их различать: иначе разбор начинается
        # с чтения пустого шарда, у которого нечего читать.
        if s["id"] in precond_rows:
            lines.append(f"| `{s['id']}` | {' '.join(s['suites'])} | "
                         f"**УСЛОВИЕ НЕ СОЗДАНО** ({v['reported']}/{v['expected']}) | "
                         f"— | — | — | — | — |")
            continue
        lines.append(f"| `{s['id']}` | {' '.join(s['suites'])} | {v['reported']}/{v['expected']} | "
                     f"{v['requests']} | {v['assertions']} | **{v['failed']}** | "
                     f"{v['unanswered']} | {v['script_failed']} |")
    lines += ["", f"**ИТОГО**: запросов {tot['requests']}, утверждений {tot['assertions']}, "
                  f"упавших {tot['failed']}, без ответа {tot['unanswered']}, "
                  f"упавших скриптов {tot['script_failed']}, пустых отчётов {tot['empty']}."]
    if findings:
        lines += ["", "### Находки", ""] + [f"- {f}" for f in findings]
    text = "\n".join(lines)
    print(text)
    if args.summary:
        with open(args.summary, "a", encoding="utf-8") as fh:
            fh.write(text + "\n")

    if findings:
        print(f"\nFAIL: исход «{code}» — находок "
              f"{len(findings)}. Ничего не вычитается и не объясняется.", file=sys.stderr)
        return 1
    print(f"\nOK: исход «{code}» — все {tot['shards_expected']} шардов отчитались, "
          f"{tot['reported']} коллекций из {tot['tree']}, упавших 0.")
    return 0


def _self_test() -> int:
    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    ids = [s["id"] for s in manifest["shards"]]
    # Число берётся ИЗ ДЕРЕВА, тем же способом, что и раскладка `full()` ниже.
    #
    # Здесь стояла константа, а `full()` рядом считала коллекции по `git ls-files`.
    # Две половины одной фикстуры из разных источников: пока числа совпадали,
    # самопроверка проходила, а первая же добавленная коллекция сделала «законный
    # близнец» красным — то есть гейт ронял конвейер от НОРМАЛЬНОЙ работы, и чинить
    # тянуло бы не его, а свод.
    #
    # Синтетическим здесь остаётся то, ради чего самопроверка и существует: описи
    # шардов и внесённые в них дефекты. Размер дерева синтезировать незачем — он и
    # есть тот вход, по которому свод судит.
    tree = tree_collection_count()

    def full() -> dict[str, dict]:
        per = {sid: {"shard": sid, "expected": 0, "reported": 0, "missing": [], "empty": [],
                     "requests": 0, "unanswered": 0, "assertions": 0,
                     "failed": 0, "script_failed": 0} for sid in ids}
        # раскладываем 82 коллекции по шардам ровно так, как их делит манифест
        import subprocess as sp
        for s in manifest["shards"]:
            n = 0
            for svc in s["suites"]:
                d = "gateway/tests/newman" if svc == "api-gateway" else f"services/{svc}/tests/newman"
                r = sp.run(["git", "-C", str(REPO), "ls-files", "--",
                            f"{d}/collections/*{SUFFIX}"], capture_output=True, text=True)
                n += len([l for l in r.stdout.splitlines() if l.strip()])
            per[s["id"]]["expected"] = n
            per[s["id"]]["reported"] = n
            per[s["id"]]["assertions"] = 10 * n
        return per

    ok = True

    def run(v: dict, label: str, want_red: bool, want_word: str | None = None,
            stages: dict[str, str] | None = None, want_outcome: str | None = None,
            forbid_word: str | None = None) -> None:
        nonlocal ok
        findings, tot = decide(manifest, v, tree, stages)
        red = bool(findings)
        code, _ = outcome(tot, findings)
        good = (red == want_red) and (not want_word or any(want_word in f for f in findings))
        if forbid_word:
            good = good and not any(forbid_word in f for f in findings)
        if want_outcome:
            good = good and code == want_outcome
        ok = ok and good
        print(f"  [{'ok ' if good else 'FAIL'}] {label}: "
              f"{'КРАСНЫЙ' if red else 'зелёный'} / исход «{code}»"
              f" (ждали {'красного' if want_red else 'зелёного'}"
              + (f", исход «{want_outcome}»" if want_outcome else "") + ")"
              + (f" — {findings[0]}" if red else ""))

    print("=== самопроверка свода (инъекция в обе стороны) ===")
    run(full(), "законный близнец: все шарды отчитались, 82 из 82", want_red=False)

    v = full(); v.pop(ids[-1])
    run(v, f"(а) шард '{ids[-1]}' не отчитался", want_red=True, want_word="НЕ ОТЧИТАЛСЯ")

    v = full(); v[ids[0]]["reported"] -= 1; v[ids[0]]["missing"] = ["vpc/subnet"]
    run(v, "(б) одна коллекция шарда без отчёта", want_red=True, want_word="ОТЧИТАЛОСЬ")

    v = full(); v[ids[0]]["expected"] -= 1; v[ids[0]]["reported"] -= 1
    run(v, "(в) коллекцию убрали из назначения шарда", want_red=True, want_word="ОЖИДАЛИ")

    v = full(); v[ids[1]]["failed"] = 3
    run(v, "(г) три упавших утверждения", want_red=True, want_word="упавших утверждений")

    v = full(); v[ids[1]]["unanswered"] = 2
    run(v, "(д) два запроса без ответа", want_red=True, want_word="без ответа")

    v = full(); v[ids[1]]["script_failed"] = 1
    run(v, "(е) упавший скрипт кейса", want_red=True, want_word="упавших скриптов")

    v = full(); v[ids[1]]["empty"] = ["iam/iam-role"]
    run(v, "(ж) отчёт без единого утверждения", want_red=True, want_word="пустых отчётов")

    v = full(); v["dns"] = {"shard": "dns", "expected": 0, "reported": 0}
    run(v, "(з) опись шарда вне манифеста", want_red=True, want_word="нет в манифесте")

    # ─── КАТЕГОРИЯ ИСХОДА. Исходов три, и вердикт обязан назвать свой ────────
    #
    # Инъекция в обе стороны: у каждой категории свой вход И свой законный
    # близнец, иначе «не выполнилось» осталось бы неотличимо от красного —
    # ровно та подмена, из-за которой разбор прогона начинался не с той
    # стороны.
    run(full(), "исход: всё судилось, падений нет", want_red=False, want_outcome=OUTCOME_GREEN)

    v = full(); v[ids[1]]["failed"] = 3
    run(v, "исход: всё судилось, есть падения", want_red=True, want_outcome=OUTCOME_RED)

    v = full(); v[ids[1]]["reported"] = 0; v[ids[1]]["assertions"] = 0
    v[ids[1]]["missing"] = ["iam/iam-role"]
    run(v, "исход: один шард не отчитал НИ ОДНОЙ коллекции — часть дерева не судилась",
        want_red=True, want_outcome=OUTCOME_PARTIAL)

    run({}, "исход: описей нет вовсе", want_red=True, want_outcome=OUTCOME_DIDNOTRUN)

    # ─── ПЛАН КРАСНЫЙ ⇒ СТЕНДЫ НЕ ПОДНИМАЛИСЬ ───────────────────────────────
    #
    # Пять «шард не отчитался» — СЛЕДСТВИЕ одной находки плана, а не пять
    # находок. Перечисляя их, свод описывал каскад как коллапс прогона: на
    # 31995227651 и 31995487127 он напечатал восемь находок, из которых предмет
    # был у одной, и разбор начинался с чтения пяти пустых шардов.
    #
    # Ничего не вычитается: исход остаётся КРАСНЫМ, меняется то, ЧТО названо
    # причиной. Законный близнец ниже держит границу — при зелёном плане
    # неотчитавшийся шард по-прежнему находка сам по себе.
    run({}, "(и) план красный, стенды не поднимались", want_red=True,
        want_word="ПРОГОН НЕ СОСТОЯЛСЯ", forbid_word="НЕ ОТЧИТАЛСЯ",
        stages={"plan": "failure", "shard": "skipped"}, want_outcome=OUTCOME_DIDNOTRUN)

    v = full(); v.pop(ids[-1])
    run(v, f"(и-контроль) план ЗЕЛЁНЫЙ, шард '{ids[-1]}' не отчитался — это находка",
        want_red=True, want_word="НЕ ОТЧИТАЛСЯ", forbid_word="ПРОГОН НЕ СОСТОЯЛСЯ",
        stages={"plan": "success", "shard": "failure"})

    run({}, "(и-контроль-2) план зелёный, описей нет — молчать нельзя", want_red=True,
        want_word="НЕ ОТЧИТАЛСЯ", stages={"plan": "success", "shard": "failure"})

    # ─── УСЛОВИЕ НЕ СОЗДАНО (kacho#655) ─────────────────────────────────────
    #
    # Инъекция в обе стороны, и вторая половина здесь важнее первой: без неё
    # отметка стала бы МАСКОЙ — способом подать любой незапуск и любое падение
    # как «не наша вина». Поэтому рядом с каждым послаблением стоит вход, на
    # котором оно НЕ выдаётся.
    def with_precond(sid: str, detail: str = "источник чартов не ответил: https://charts.example/x",
                     kind: str | None = "external-chart-source"):
        v = full()
        v[sid]["reported"] = 0
        v[sid]["assertions"] = 0
        v[sid]["missing"] = [f"{sid}/all"]
        v[sid]["precondition"] = {"unmet": True, "detail": detail}
        if kind is not None:
            v[sid]["precondition"]["kind"] = kind
        return v

    run(with_precond(ids[1]),
        "(к) стенд шарда не поднялся: чужой источник чартов не ответил",
        want_red=True, want_word="УСЛОВИЕ НЕ СОЗДАНО", want_outcome=OUTCOME_PRECOND)

    # ВТОРАЯ ФОРМА — та, на которой свод МОЛЧАЛ. Её отметку ставит вердикт
    # провенанса: стенд поднялся, но исполняет другое дерево. Файла он не
    # оставляет, поэтому до 2026-09-12 опись её не несла, и прогон 34694955309
    # приехал сюда как «НЕ ВЫПОЛНИЛОСЬ … 0 коллекций из 57» — без единого слова о
    # том, что условие не создано. Ось проверяет ровно это: форма читается и
    # НАЗЫВАЕТСЯ, а не сводится к привычной.
    run(with_precond(ids[1], detail="стенд исполняет ДРУГОЕ дерево",
                     kind="stand-revision-divergence"),
        "(к2) стенд поднялся, но исполняет другое дерево",
        want_red=True, want_word="stand-revision-divergence", want_outcome=OUTCOME_PRECOND)

    # Форма, не названная описью, обязана быть названа ОТСУТСТВУЮЩЕЙ, а не
    # подставлена привычной: иначе третья форма приедет переименованной во вторую.
    run(with_precond(ids[1], kind=None),
        "(к3) опись не назвала форму — свод говорит «форма не названа»",
        want_red=True, want_word="ФОРМА НЕ НАЗВАНА", want_outcome=OUTCOME_PRECOND)

    # Законный близнец той же формы: тот же ноль коллекций, отметки НЕТ ⇒
    # по-прежнему «часть дерева не судилась», то есть разбирают продукт.
    v = full(); v[ids[1]]["reported"] = 0; v[ids[1]]["assertions"] = 0
    v[ids[1]]["missing"] = [f"{ids[1]}/all"]
    run(v, "(к-контроль) тот же ноль коллекций БЕЗ отметки — послабления нет",
        want_red=True, want_outcome=OUTCOME_PARTIAL, forbid_word="УСЛОВИЕ НЕ СОЗДАНО")

    # Отметка НЕ прикрывает упавшие утверждения: они есть — значит пробы шли и
    # что-то сказали, и это вердикт о продукте.
    v = with_precond(ids[1]); v[ids[0]]["failed"] = 3
    run(v, "(к-контроль-2) отметка ЕСТЬ, но 3 утверждения упали — не послабление",
        want_red=True, want_word="упавших утверждений", want_outcome=OUTCOME_PARTIAL)

    # Отметка одного шарда не объясняет молчание другого.
    v = with_precond(ids[1]); v.pop(ids[-1])
    run(v, "(к-контроль-3) отметка у одного, ДРУГОЙ не отчитался — не послабление",
        want_red=True, want_word="НЕ ОТЧИТАЛСЯ", want_outcome=OUTCOME_PARTIAL)

    # И главное: «условие не создано» НИКОГДА не зелёное. Оно остаётся отказом.
    findings_k, tot_k = decide(manifest, with_precond(ids[1]), tree, None)
    if not findings_k:
        print("  [FAIL] «условие не создано» прошло как ЗЕЛЁНОЕ — третья категория "
              "превратилась в успех"); ok = False
    else:
        print("  [ok ] «условие не создано» остаётся ОТКАЗОМ (находок "
              f"{len(findings_k)}), зелёным не становится")

    print("самопроверка:", "OK" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(_self_test() if "--self-test" in sys.argv[1:] else main())
