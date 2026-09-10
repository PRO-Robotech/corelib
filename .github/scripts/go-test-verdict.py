#!/usr/bin/env python3
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
"""go-test-verdict — читает `go test -json` со стдина и выносит ЧИСЛОВОЙ вердикт.

ИСХОДОВ ТРИ, и они не смешиваются:

  0 — ЗЕЛЁНЫЙ: обход непуст, отказов нет;
  1 — КРАСНЫЙ: упала проба либо пакет не собрался;
  2 — БЕСПРЕДМЕТНО: обход пуст — не осмотрено НИ ОДНОГО пакета. Это не успех:
      «ноль отказов» здесь означало бы «ноль прочитанного», а такой вердикт
      нельзя ни подтвердить, ни опровергнуть.

ЗАЧЕМ ПРОГОНЩИК, А НЕ ГОЛЫЙ `go test`. Голый печатает `ok` и на пакете, где
пробы ПРОШЛИ, и на пакете, где они все ПРОПУЩЕНЫ, и на пакете, где проб нет
вовсе. Три разных состояния под одним словом — и ни одного числа. Перепись
отвечает на вопрос, на который `ok` не отвечает: сколько прочитано.

ПРОПУСК — САМОСТОЯТЕЛЬНАЯ ВЕЛИЧИНА. Он не прибавляется к пройденным и не
вычитается из отказов: он печатается своим числом, чтобы рост пропусков был
виден раньше, чем кто-нибудь заметит, что гейт перестал что-либо утверждать.

ЕДИНИЦА СЧЁТА НАЗВАНА, потому что без неё числа несравнимы: проба — это
СОБЫТИЕ пробы, подпробы считаются наравне с родителями. В перечне упавших
печатаются только верхнеуровневые имена (родитель падает вслед за ребёнком,
поэтому перечень с подпробами был бы длиннее, но не содержательнее).
"""

import collections
import json
import sys


def verdict(stream, out=sys.stdout):
    """Считает события `go test -json` и печатает перепись. Возвращает код исхода."""
    pkg = collections.Counter()
    test = collections.Counter()
    failed_top = []
    # Пакеты, у которых НЕ БЫЛО ни одного события пробы: либо пусты, либо не собрались.
    saw_test_event = set()
    seen_pkgs = set()
    unparsable = 0

    for line in stream:
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except (ValueError, TypeError):
            # Сборка роняет НЕ-JSON в тот же поток. Строку считаем, но вердикта
            # по ней не выносим: её предмет назовёт событие `fail` пакета.
            unparsable += 1
            continue
        if not isinstance(event, dict):
            unparsable += 1
            continue

        action = event.get("Action")
        package = event.get("Package")
        name = event.get("Test")

        if package:
            seen_pkgs.add(package)

        if name is None:
            if action in ("pass", "fail", "skip"):
                pkg[action] += 1
            continue

        saw_test_event.add(package)
        if action in ("pass", "fail", "skip"):
            test[action] += 1
            if action == "fail" and "/" not in name:
                failed_top.append("%s :: %s" % (package, name))

    silent_pkgs = sorted(p for p in seen_pkgs if p not in saw_test_event)
    executed = test["pass"] + test["fail"]

    print("", file=out)
    print("=== ВЕРДИКТ ПРОГОНА (единица: событие пробы; подпробы считаются) ===", file=out)
    print("пакетов осмотрено : %d  (прошло %d · упало %d · без проб %d)"
          % (len(seen_pkgs), pkg["pass"], pkg["fail"], pkg["skip"]), file=out)
    print("проб исполнено    : %d" % executed, file=out)
    print("отказов           : %d" % test["fail"], file=out)
    print("ПРОПУЩЕНО         : %d  (в зачёт прохода НЕ идёт)" % test["skip"], file=out)
    if unparsable:
        print("не-JSON строк     : %d  (диагностика сборки — см. вывод выше)"
              % unparsable, file=out)
    if silent_pkgs:
        print("пакетов без событий проб: %d" % len(silent_pkgs), file=out)

    if not seen_pkgs:
        print("", file=out)
        print("БЕСПРЕДМЕТНО: обход пуст — ни одного пакета не осмотрено.", file=out)
        print("Это не зелёный вердикт: спросить было не у кого.", file=out)
        return 2

    if test["fail"] or pkg["fail"]:
        print("", file=out)
        print("=== упавшие пробы верхнего уровня (%d) ===" % len(failed_top), file=out)
        for item in sorted(failed_top):
            print("   %s" % item, file=out)
        if pkg["fail"] and not test["fail"]:
            print("", file=out)
            print("Упали ПАКЕТЫ при нуле упавших проб — то есть они не собрались.", file=out)
            print("Диагностика сборки в выводе выше; это отказ, а не пропуск.", file=out)
        print("", file=out)
        print("КРАСНЫЙ: отказов %d." % (test["fail"] or pkg["fail"]), file=out)
        return 1

    print("", file=out)
    print("ЗЕЛЁНЫЙ: отказов нет.", file=out)
    return 0


# --- самопроверка: доказательство инъекцией в обе стороны ---------------------
#
# Живёт ФЛАГОМ этого же файла, а не соседним: отдельный файл в перечень шагов
# конвейера не попал бы сам, то есть не исполнялся бы никогда.
#
# Инъекция меняет РОВНО ОДИН факт против положительного близнеца — иначе
# неизвестно, какой из двух дал красное.
def self_test():
    green = ('{"Action":"run","Package":"p","Test":"TestA"}\n'
             '{"Action":"pass","Package":"p","Test":"TestA"}\n'
             '{"Action":"pass","Package":"p"}\n')
    probes = 0
    failed = 0

    def run(want, title, payload):
        nonlocal probes, failed
        probes += 1
        buf = []

        class Sink:
            def write(self, s):
                buf.append(s)

            def flush(self):
                pass

        got = verdict(iter(payload.splitlines()), out=Sink())
        text = "".join(buf)
        if got != want:
            print("  ПРОВАЛ %s — ждали код %d, получили %d" % (title, want, got),
                  file=sys.stderr)
            failed += 1
            return ""
        print("  ok   %s (код %d)" % (title, got))
        return text

    print("=== прогонщик вердикта: доказательство инъекцией ===")

    # (−) ПОЛОЖИТЕЛЬНЫЙ БЛИЗНЕЦ. Без него всё нижеследующее зеленело бы на
    # прогонщике, который краснеет всегда.
    run(0, "(−) зелёный прогон — зелёный", green)

    # (+) один факт против близнеца: проба упала.
    text = run(1, "(+) упавшая проба — красное", green.replace(
        '{"Action":"pass","Package":"p","Test":"TestA"}',
        '{"Action":"fail","Package":"p","Test":"TestA"}').replace(
        '{"Action":"pass","Package":"p"}', '{"Action":"fail","Package":"p"}'))
    # Диагностика — ЧАСТЬ свойства: находка, не назвавшая имени, посылает
    # читателя искать не там.
    if text and "TestA" not in text:
        print("  ПРОВАЛ (+) красное не назвало имени упавшей пробы", file=sys.stderr)
        failed += 1

    # (+) пакет не собрался: событие `fail` пакета БЕЗ единого события пробы.
    run(1, "(+) пакет не собрался — красное, хотя упавших проб ноль",
        '{"Action":"output","Package":"p","Output":"build failed\\n"}\n'
        '{"Action":"fail","Package":"p"}\n')

    # (+) обход пуст — БЕСПРЕДМЕТНО, а не «прошло».
    run(2, "(+) пустой обход — беспредметно, не зелёное", "")

    # (−) законный близнец пустого обхода: пакет есть, проб в нём нет.
    # Это НЕ беспредметность — пакет прочитан.
    run(0, "(−) пакет без проб — зелёное, обход не пуст",
        '{"Action":"pass","Package":"p"}\n')

    # (+) пропуск НЕ засчитывается проходом и печатается своим числом.
    text = run(0, "(−) пропущенная проба — не красное, но названа числом",
               '{"Action":"skip","Package":"p","Test":"TestA"}\n'
               '{"Action":"pass","Package":"p"}\n')
    if text and "ПРОПУЩЕНО         : 1" not in text:
        print("  ПРОВАЛ пропуск не попал в перепись отдельной величиной", file=sys.stderr)
        failed += 1
    if text and "проб исполнено    : 0" not in text:
        print("  ПРОВАЛ пропуск зачтён в исполненные", file=sys.stderr)
        failed += 1

    # (−) не-JSON строка сама по себе вердикта не меняет: её предмет назовёт
    # событие пакета. Иначе любая строка сборки красила бы зелёный прогон.
    run(0, "(−) не-JSON строка при зелёных событиях зелёного не меняет",
        "# github.com/x/y\n" + green)

    print("")
    print("go-test-verdict --self-test: проб исполнено %d, провалов %d" % (probes, failed))
    if probes == 0:
        print("ПРОВАЛ: ни одной пробы не исполнено", file=sys.stderr)
        return 2
    return 1 if failed else 0


if __name__ == "__main__":
    if "--self-test" in sys.argv[1:]:
        sys.exit(self_test())
    sys.exit(verdict(sys.stdin))
