#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# gosec-gate.sh <файл-SARIF> <файл-текстовой-сводки>
#
# Выносит вердикт по отчёту gosec. Исходов ТРИ:
#
#   0 — зелёный: отчёт разобран, находок level=error ноль;
#   1 — КРАСНЫЙ: находки level=error есть, каждая названа координатой;
#   2 — СКАНИРОВАНИЕ НЕ СОСТОЯЛОСЬ: отчёт не разбирается, либо дерево не
#       собралось, либо перепись пуста.
#
# ТРЕТИЙ ИСХОД — НЕСУЩИЙ, И ВОТ ПОЧЕМУ. Сканер, не сумевший собрать дерево,
# пишет отчёт, ПОБАЙТОВО такой же, как у чистого дерева: `results: []`, валидный
# JSON, тот же код возврата. То есть отказ анализа выглядит ровно как чистота.
# Различает их только ПЕРЕПИСЬ: строка `Files : N` из текстовой сводки говорит,
# сколько файлов прочитано, а `Golang errors in file:` — что дерево не
# загрузилось. Гейт по одному лишь числу находок был бы зелёным на несостоявшемся
# сканировании — то есть формой без содержания.
#
# КОД ВОЗВРАТА САМОГО gosec ВЕРДИКТОМ НЕ ЯВЛЯЕТСЯ: он ненулевой и при находках
# ниже порога, то есть отвечает на другой вопрос.
set -uo pipefail

gate() {
    local sarif="$1" summary="$2"

    if [ ! -s "$sarif" ]; then
        echo "gosec: отчёт не создан или пуст — сканирование НЕ СОСТОЯЛОСЬ." >&2
        echo "Это не «находок нет»: пустой отчёт неотличим от чистого дерева." >&2
        return 2
    fi
    if ! jq -e . "$sarif" >/dev/null 2>&1; then
        echo "gosec: SARIF не разбирается — сканирование НЕ СОСТОЯЛОСЬ." >&2
        return 2
    fi
    if [ -s "$summary" ] && grep -q 'Golang errors in file' "$summary"; then
        echo "gosec: дерево не загрузилось (Golang errors in file) — сканирование НЕ СОСТОЯЛОСЬ." >&2
        grep -m5 -A2 'Golang errors in file' "$summary" >&2
        return 2
    fi

    # Перепись — ОТДЕЛЬНОЕ утверждение, а не украшение вывода.
    local files lines
    files=$(sed -n 's/^[[:space:]]*Files[[:space:]]*:[[:space:]]*\([0-9]\+\).*/\1/p' "$summary" | tail -1)
    lines=$(sed -n 's/^[[:space:]]*Lines[[:space:]]*:[[:space:]]*\([0-9]\+\).*/\1/p' "$summary" | tail -1)
    files=${files:-0}; lines=${lines:-0}
    if [ "$files" -eq 0 ]; then
        echo "gosec: перепись пуста — прочитано 0 файлов, сканирование НЕ СОСТОЯЛОСЬ." >&2
        return 2
    fi

    local errs total
    errs=$(jq '[.runs[].results[]|select(.level=="error")]|length' "$sarif")
    total=$(jq '[.runs[].results[]]|length' "$sarif")

    echo "=== ВЕРДИКТ gosec ==="
    echo "прочитано файлов  : $files (строк $lines)"
    echo "результатов всего : $total"
    echo "из них level=error: $errs"

    if [ "$errs" -eq 0 ]; then
        echo "ЗЕЛЁНЫЙ: находок уровня error нет."
        return 0
    fi
    echo ""
    echo "=== находки level=error ==="
    jq -r '.runs[].results[]|select(.level=="error")
           |"   \(.ruleId)  \(.locations[0].physicalLocation.artifactLocation.uri):\(.locations[0].physicalLocation.region.startLine)"' \
        "$sarif"
    echo ""
    echo "КРАСНЫЙ: находок level=error $errs." >&2
    return 1
}

# --- самопроверка: доказательство инъекцией в обе стороны ---------------------
if [ "${1:-}" = "--self-test" ]; then
    TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
    probes=0; failed=0
    clean_sarif='{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gosec","rules":[]}},"results":[]}]}'
    dirty_sarif='{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gosec","rules":[]}},"results":[{"ruleId":"G304","level":"error","locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/x/y.go"},"region":{"startLine":42}}}]}]}]}'
    note_sarif='{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gosec","rules":[]}},"results":[{"ruleId":"G104","level":"note","locations":[{"physicalLocation":{"artifactLocation":{"uri":"internal/x/y.go"},"region":{"startLine":7}}}]}]}]}'
    good_summary=$'Summary:\n  Files  : 617\n  Lines  : 130015\n  Issues : 0'
    run() { # run <ожидаемый> <имя> <sarif> <summary>
        local want="$1" name="$2" got=0
        probes=$((probes + 1))
        printf '%s' "$3" > "$TMP/s.sarif"; printf '%s\n' "$4" > "$TMP/sum.txt"
        gate "$TMP/s.sarif" "$TMP/sum.txt" >/dev/null 2>&1 || got=$?
        if [ "$got" -ne "$want" ]; then
            echo "  ПРОВАЛ $name — ждали $want, получили $got" >&2
            failed=$((failed + 1)); return
        fi
        echo "  ok   $name (код $got)"
    }

    echo "=== гейт gosec: доказательство инъекцией ==="
    # (−) ПОЛОЖИТЕЛЬНЫЙ БЛИЗНЕЦ: без него всё нижеследующее зеленело бы на гейте,
    # который краснеет всегда.
    run 0 "(−) чистое дерево, перепись непуста — зелёное" "$clean_sarif" "$good_summary"
    # (+) один факт против близнеца: появилась находка уровня error.
    run 1 "(+) находка level=error — красное" "$dirty_sarif" "$good_summary"
    # (−) находка НИЖЕ порога красной не делает: иначе порог был бы не тот, что объявлен.
    run 0 "(−) находка level=note порога не переходит" "$note_sarif" "$good_summary"
    # (+) главный класс: отчёт чист, но сканирования НЕ БЫЛО.
    run 2 "(+) перепись пуста (0 файлов) — НЕ СОСТОЯЛОСЬ, а не чисто" "$clean_sarif" \
        $'Summary:\n  Files  : 0\n  Lines  : 0\n  Issues : 0'
    run 2 "(+) дерево не загрузилось — НЕ СОСТОЯЛОСЬ, а не чисто" "$clean_sarif" \
        $'Golang errors in file: [internal/x/y.go]: y.go:1:1: expected declaration\nSummary:\n  Files  : 617\n  Lines  : 1'
    run 2 "(+) SARIF не разбирается — НЕ СОСТОЯЛОСЬ" "{ обрезано" "$good_summary"
    run 2 "(+) SARIF пуст — НЕ СОСТОЯЛОСЬ" "" "$good_summary"

    echo
    echo "gosec-gate --self-test: проб исполнено $probes, провалов $failed"
    [ "$probes" -eq 0 ] && { echo "ПРОВАЛ: ни одной пробы не исполнено" >&2; exit 2; }
    [ "$failed" -gt 0 ] && exit 1
    exit 0
fi

if [ "$#" -ne 2 ]; then
    echo "нужно два довода: <файл-SARIF> <файл-текстовой-сводки>" >&2
    exit 2
fi
gate "$1" "$2"
