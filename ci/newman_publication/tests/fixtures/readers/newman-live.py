#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1
"""newman-live — прогон e2e видно ПОКА ОН ИДЁТ, а не после.

ЧТО БЫЛО. Восемь suite'ов гоняются одним шагом параллельно, а их вывод уходит в
файлы (``out/suite.log``) — в интерфейсе всё это время не видно ничего, кроме
крутящегося шага. Первое число появлялось минут через сорок, вместе с гейтами.
Прогон, за которым нельзя следить, отлаживают следующим прогоном — и цена ошибки
в фикстуре равна полному циклу стенда.

КАКОЙ КАНАЛ ВЫБРАН И ПОЧЕМУ. Три предложенных способа дают разное:

* **отдельный шаг на суиту** — стадия видна в интерфейсе, но шаги идут
  последовательно, а суиты гоняются ОДНОВРЕМЕННО ради времени стенда. Разбить их
  по шагам значит вернуть последовательный прогон. Отвергнуто по стоимости;
  посуитная краснота в интерфейсе и так есть — её дают шаги-гейты после прогона.
* **``$GITHUB_STEP_SUMMARY`` по мере прохождения** — файл сводки читается, когда
  ШАГ ЗАВЕРШИЛСЯ. Внутри идущего шага он не виден никому, поэтому «в реальном
  времени» через него не получается. Оставлен как итоговая сводка (её пишут
  короткие шаги-гейты — вот они появляются по мере).
* **аннотации ``::error::`` + таблица прогресса в лог** — лог шага стримится, и
  строки видны сразу; аннотация всплывает в интерфейсе тогда же. ЭТО и есть
  канал реального времени, он здесь и реализован.

ЧТО ЭТОТ СКРИПТ НЕ ДЕЛАЕТ. Он НЕ выносит вердикт. Вердикт — у
``tests/newman/scripts/assert-suites-green.sh`` (по суите) и у
``.github/scripts/check-newman-suite-gates.py`` (связность). Второй, более мягкий
вердикт рядом с первым — способ получить два разных ответа на один вопрос;
поэтому ``watch`` и ``report`` всегда выходят с нулём и говорят об этом вслух.

Режимы:
  watch  — печатать таблицу прогресса каждые --interval секунд и выдавать
           аннотацию на КАЖДОЕ новое упавшее утверждение, пока идёт прогон.
  report — итоговая сводка в $GITHUB_STEP_SUMMARY + аннотации на всё, что не
           успело попасть в поток.

Самопроверка: --self-test — собирает отчёты с внесёнными дефектами и требует,
чтобы число и координата были названы.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import sys
import tempfile
import time

REPO = pathlib.Path(__file__).resolve().parents[2]

# Предел канала аннотаций. Интерфейс показывает ограниченное их число, поэтому
# сверх предела аннотации не выпускаются — но НИ ОДНА находка не пропадает: все
# они печатаются в лог и в сводку, а сам факт усечения объявляется отдельной
# аннотацией. Молчаливое усечение было бы ровно тем «не выполнилось в зачёт
# прошло», против которого весь этот файл.
DEFAULT_MAX_ANNOTATIONS = 40


def suite_dir(svc: str) -> pathlib.Path:
    return REPO / ("gateway/tests/newman" if svc == "api-gateway" else f"services/{svc}/tests/newman")


def services_from_runner() -> list[str]:
    """Список суит берётся из ЕДИНСТВЕННОГО источника — умолчания SERVICES прогонщика.

    Свой список здесь разъехался бы с прогоном молча: суита исполнялась бы, а
    следить за ней было бы нечем.
    """
    sh = (REPO / "deploy/scripts/newman-parallel.sh").read_text()
    m = re.search(r'^SERVICES="\$\{SERVICES:-([^}]*)\}"', sh, re.M)
    if not m:
        raise SystemExit("FAIL: не удалось прочитать умолчание SERVICES из deploy/scripts/newman-parallel.sh")
    return m.group(1).split()


# ─── чтение отчётов ──────────────────────────────────────────────────────────

class SuiteState:
    """Числа одной суиты, посчитанные ПО ОТЧЁТАМ, а не по факту запуска."""

    def __init__(self, svc: str):
        self.svc = svc
        self.expected = 0     # сколько коллекций сгенерировано
        self.reported = 0     # для скольких есть отчёт
        self.requests = 0
        self.assertions = 0
        self.failed = 0
        self.unanswered = 0
        self.empty = 0        # отчёты без единого утверждения
        self.finished = False # прогонщик дописал out/summary.txt

    @property
    def stage(self) -> str:
        if self.finished:
            return "готово"
        if self.reported == 0:
            return "старт"
        return "идёт"

    def row(self) -> str:
        return (f"{self.svc:<12} {self.stage:<7} {self.reported:>3}/{self.expected:<3} "
                f"{self.requests:>7} {self.assertions:>8} {self.failed:>8} {self.unanswered:>7} {self.empty:>6}")


def read_suite(svc: str) -> SuiteState:
    st = SuiteState(svc)
    d = suite_dir(svc)
    cols = d / "collections"
    out = d / "out"
    if cols.is_dir():
        st.expected = len(list(cols.glob("*.postman_collection.json")))
    if not out.is_dir():
        return st
    st.finished = (out / "summary.txt").exists()
    for f in sorted(out.glob("*.json")):
        try:
            doc = json.loads(f.read_text())
        except Exception:
            # Отчёт, который пишется прямо сейчас, читается наполовину. Это не
            # находка — на следующем обходе он будет целым.
            continue
        run = doc.get("run") or {}
        stats = run.get("stats") or {}
        st.reported += 1
        st.requests += int(((stats.get("requests") or {}).get("total")) or 0)
        total_asserts = int(((stats.get("assertions") or {}).get("total")) or 0)
        st.assertions += total_asserts
        st.failed += int(((stats.get("assertions") or {}).get("failed")) or 0)
        st.unanswered += int(((stats.get("requests") or {}).get("failed")) or 0)
        if total_asserts == 0:
            st.empty += 1
    return st


def failures_of(svc: str) -> list[dict]:
    """Все находки суиты с их координатами; каждая — со СВОИМ видом."""
    out = suite_dir(svc) / "out"
    found: list[dict] = []
    if not out.is_dir():
        return found
    for f in sorted(out.glob("*.json")):
        try:
            doc = json.loads(f.read_text())
        except Exception:
            continue
        stem = f.stem
        run = doc.get("run") or {}
        stats = run.get("stats") or {}
        if int(((stats.get("assertions") or {}).get("total")) or 0) == 0:
            found.append({
                "svc": svc, "collection": stem, "kind": "NOTHING-RAN",
                # Адрес такой находки — сама коллекция: упавшего кейса у неё нет,
                # и подставлять пустоту значило бы напечатать находку без имени.
                "case": stem, "request": "(вся коллекция)",
                "text": "коллекция не произвела ни одного утверждения — суита, которая ничего не утверждает, пройти не может",
            })
        for fail in run.get("failures") or []:
            err = fail.get("error") or {}
            src = fail.get("source") or {}
            parent = fail.get("parent") or {}
            kind = "FAILED" if (err.get("name") == "AssertionError") else "UNANSWERED"
            found.append({
                "svc": svc, "collection": stem, "kind": kind,
                "case": str(parent.get("name") or ""),
                "request": str(src.get("name") or ""),
                "text": str(err.get("test") or "") or str(err.get("message") or "нет ответа"),
                "message": str(err.get("message") or ""),
            })
    return found


# ─── координата находки в исходнике кейса ────────────────────────────────────

_CASE_ID_RE = re.compile(r'''id\s*=\s*["']([A-Za-z0-9][A-Za-z0-9._:-]*)["']''')


def case_index(svc: str) -> dict[str, tuple[str, int]]:
    """case-id → (путь к файлу кейсов относительно корня, номер строки).

    Аннотация без координаты — красный крестик без адреса: её нельзя починить,
    не открыв артефакт. Идентификатор кейса лежит в его исходнике, туда и
    указываем.
    """
    idx: dict[str, tuple[str, int]] = {}
    cases = suite_dir(svc) / "cases"
    if not cases.is_dir():
        return idx
    for f in sorted(cases.glob("*.py")):
        rel = str(f.relative_to(REPO))
        for n, line in enumerate(f.read_text(errors="replace").splitlines(), start=1):
            m = _CASE_ID_RE.search(line)
            if m and m.group(1) not in idx:
                idx[m.group(1)] = (rel, n)
    return idx


def case_id_of(folder: str) -> str:
    """Имя папки newman — «<ID> — <описание>»; адресуется кейс идентификатором."""
    for sep in (" — ", " :: "):
        if sep in folder:
            folder = folder.split(sep, 1)[0]
    return folder.strip()


def annotate(f: dict, idx: dict[str, tuple[str, int]]) -> str:
    cid = case_id_of(f["case"])
    where = idx.get(cid)
    title = f"{f['svc']}/{f['collection']}: {f['kind']}"
    body = f"{cid or f['case']} / {f['request']} — {f['text']}"
    body = body.replace("\r", " ").replace("\n", "%0A").replace("::", ": ")
    if where:
        return f"::error file={where[0]},line={where[1]},title={title}::{body}"
    return f"::error title={title}::{body}"


# ─── печать ──────────────────────────────────────────────────────────────────

HEADER = (f"{'SUITE':<12} {'СТАДИЯ':<7} {'ОТЧЁТЫ':<7} {'ЗАПРОС':>7} {'УТВЕРЖД':>8} "
          f"{'УПАЛО':>8} {'БЕЗОТВ':>7} {'ПУСТО':>6}")


def print_table(states: list[SuiteState], elapsed: int) -> None:
    tot = SuiteState("ИТОГО")
    for s in states:
        tot.expected += s.expected
        tot.reported += s.reported
        tot.requests += s.requests
        tot.assertions += s.assertions
        tot.failed += s.failed
        tot.unanswered += s.unanswered
        tot.empty += s.empty
    lines = [f"── e2e-newman, {elapsed // 60}м{elapsed % 60:02d}с ────────────────────────────", HEADER]
    lines += [s.row() for s in states]
    lines.append(f"{'ИТОГО':<12} {'':<7} {tot.reported:>3}/{tot.expected:<3} "
                 f"{tot.requests:>7} {tot.assertions:>8} {tot.failed:>8} {tot.unanswered:>7} {tot.empty:>6}")
    print("\n".join(lines), flush=True)


def fail_key(f: dict) -> str:
    return "|".join((f["svc"], f["collection"], f["kind"], f["case"], f["request"], f["text"]))


# ─── режимы ──────────────────────────────────────────────────────────────────

def watch(services: list[str], interval: int, stop_file: pathlib.Path, max_ann: int) -> int:
    print("[live] наблюдение за прогоном: таблица каждые "
          f"{interval}с + аннотация на каждое новое упавшее утверждение.", flush=True)
    print("[live] это ОТЧЁТ, не вердикт: вердикт выносят шаги-гейты ниже.", flush=True)
    idx = {svc: case_index(svc) for svc in services}
    seen: set[str] = set()
    emitted = 0
    started = time.time()
    while True:
        states = [read_suite(svc) for svc in services]
        print_table(states, int(time.time() - started))
        for svc in services:
            for f in failures_of(svc):
                k = fail_key(f)
                if k in seen:
                    continue
                seen.add(k)
                if emitted < max_ann:
                    print(annotate(f, idx[svc]), flush=True)
                    emitted += 1
                elif emitted == max_ann:
                    print(f"::error title=аннотации усечены::выпущено {max_ann}; "
                          f"остальные находки — в сводке и в отчётах артефакта, ни одна не потеряна", flush=True)
                    emitted += 1
        if stop_file.exists():
            break
        time.sleep(interval)
    print(f"[live] наблюдение завершено: новых находок за прогон — {len(seen)}", flush=True)
    return 0


def report(services: list[str], max_ann: int) -> int:
    idx = {svc: case_index(svc) for svc in services}
    states = [read_suite(svc) for svc in services]
    print_table(states, 0)

    rows = ["| суита | отчёты/коллекций | запросов | утверждений | упало | без ответа | пустых отчётов |",
            "|---|---|---|---|---|---|---|"]
    tot = [0, 0, 0, 0, 0, 0, 0]
    for s in states:
        rows.append(f"| `{s.svc}` | {s.reported}/{s.expected} | {s.requests} | {s.assertions} | "
                    f"{s.failed} | {s.unanswered} | {s.empty} |")
        tot = [a + b for a, b in zip(tot, [s.reported, s.expected, s.requests, s.assertions,
                                           s.failed, s.unanswered, s.empty])]
    rows.append(f"| **итого** | {tot[0]}/{tot[1]} | {tot[2]} | {tot[3]} | {tot[4]} | {tot[5]} | {tot[6]} |")

    detail: list[str] = []
    emitted = 0
    for svc in services:
        fs = failures_of(svc)
        if not fs:
            continue
        detail.append(f"\n<details><summary>{svc} — находок: {len(fs)}</summary>\n")
        for f in fs:
            cid = case_id_of(f["case"])
            where = idx[svc].get(cid)
            addr = f"{where[0]}:{where[1]}" if where else "(координата не найдена)"
            detail.append(f"- `{f['kind']}` **{cid or f['case']}** / {f['request']} "
                          f"— {f['text']} — `{svc}/{f['collection']}` — {addr}")
            if emitted < max_ann:
                print(annotate(f, idx[svc]), flush=True)
                emitted += 1
        detail.append("\n</details>")

    body = ["## e2e-newman — числа прогона", "",
            "Вердикт выносят шаги-гейты; эта таблица — то же самое в числах.", "",
            *rows]
    # «Ноль находок» обязано быть отличимо от «ноль прочитанного»: прогон, умерший
    # раньше времени, оставляет здоровыми ВСЕ счётчики, кроме первого.
    missing = tot[1] - tot[0]
    if missing > 0:
        body += ["", f"> **Коллекций без отчёта: {missing} из {tot[1]}.** Это не «ничего не упало» — "
                     "это «столько-то не выполнилось». Гейты ниже назовут их поимённо."]
    if detail:
        body += ["", "### Находки", *detail]
    elif missing == 0:
        body += ["", "Находок нет, и все коллекции отчитались."]

    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as fh:
            fh.write("\n".join(body) + "\n")
    else:
        print("\n".join(body), flush=True)
    return 0


# ─── самопроверка ────────────────────────────────────────────────────────────

def self_test() -> int:
    """Гейт видимости обязан НАЗЫВАТЬ число и координату — иначе он крестик без адреса."""
    global REPO
    rc = 0
    real_repo = REPO
    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        d = root / "services/probe/tests/newman"
        (d / "collections").mkdir(parents=True)
        (d / "out").mkdir(parents=True)
        (d / "cases").mkdir(parents=True)
        (d / "collections/probe.postman_collection.json").write_text("{}")
        (d / "collections/silent.postman_collection.json").write_text("{}")
        (d / "cases/probe.py").write_text('CASES = []\n# x\nCase(\n    id="PROBE-ONE",\n)\n')
        # (1) упавшее утверждение, (2) неотвеченный запрос
        (d / "out/probe.json").write_text(json.dumps({"run": {
            "stats": {"requests": {"total": 2, "failed": 1}, "assertions": {"total": 3, "failed": 1}},
            "failures": [
                {"error": {"name": "AssertionError", "test": "status 400"},
                 "source": {"name": "cr-bad"}, "parent": {"name": "PROBE-ONE — проба"}},
                {"error": {"name": "Error", "message": "ECONNREFUSED"},
                 "source": {"name": "cr-dead"}, "parent": {"name": "PROBE-ONE — проба"}},
            ]}}))
        # (3) отчёт без утверждений
        (d / "out/silent.json").write_text(json.dumps({"run": {
            "stats": {"requests": {"total": 0, "failed": 0}, "assertions": {"total": 0, "failed": 0}},
            "failures": []}}))
        REPO = root

        st = read_suite("probe")
        ok = (st.expected == 2 and st.reported == 2 and st.failed == 1
              and st.unanswered == 1 and st.empty == 1)
        print(f"  (1) числа по отчётам           → {'НАЗВАНЫ' if ok else 'РАЗОШЛИСЬ'}: "
              f"коллекций {st.reported}/{st.expected}, упало {st.failed}, "
              f"без ответа {st.unanswered}, пустых {st.empty}")
        rc |= 0 if ok else 1

        fs = failures_of("probe")
        kinds = sorted({f["kind"] for f in fs})
        ok = kinds == ["FAILED", "NOTHING-RAN", "UNANSWERED"]
        print(f"  (2) три исхода различены       → {'ДА' if ok else 'НЕТ'}: {kinds}")
        rc |= 0 if ok else 1

        idx = case_index("probe")
        ann = [annotate(f, idx) for f in fs if f["kind"] == "FAILED"]
        ok = bool(ann) and "cases/probe.py,line=4" in ann[0] and "status 400" in ann[0]
        print(f"  (3) координата в аннотации     → {'ЕСТЬ' if ok else 'НЕТ'}: {ann[0] if ann else '—'}")
        rc |= 0 if ok else 1

        # КОНТРОЛЬ: чистая суита не выпускает ни одной аннотации.
        (d / "out/probe.json").write_text(json.dumps({"run": {
            "stats": {"requests": {"total": 2, "failed": 0}, "assertions": {"total": 3, "failed": 0}},
            "failures": []}}))
        (d / "out/silent.json").write_text(json.dumps({"run": {
            "stats": {"requests": {"total": 1, "failed": 0}, "assertions": {"total": 1, "failed": 0}},
            "failures": []}}))
        clean = failures_of("probe")
        print(f"  (4) КОНТРОЛЬ: чистая суита     → {'МОЛЧИТ' if not clean else 'ЛОЖНОЕ: ' + str(clean)}")
        rc |= 0 if not clean else 1

    REPO = real_repo
    print("самопроверка: " + ("ПРОЙДЕНА" if rc == 0 else "ПРОВАЛЕНА"))
    return rc


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("mode", nargs="?", choices=["watch", "report"], default="report")
    ap.add_argument("--services", default="")
    ap.add_argument("--interval", type=int, default=20)
    ap.add_argument("--stop-file", default="/tmp/newman-live.stop")
    ap.add_argument("--max-annotations", type=int, default=DEFAULT_MAX_ANNOTATIONS)
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args(argv)
    if a.self_test:
        return self_test()
    services = a.services.split() if a.services.strip() else services_from_runner()
    if a.mode == "watch":
        return watch(services, a.interval, pathlib.Path(a.stop_file), a.max_annotations)
    return report(services, a.max_annotations)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
