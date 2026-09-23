#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# oauth2-upstream-advisories.sh [<файл записей>] — НАБЛЮДЕНИЕ за уведомлениями
# об уязвимостях апстримов, внесённых в фундамент поддеревом.
#
# ЗАЧЕМ ОТДЕЛЬНО ОТ govulncheck. Сканер уязвимостей Go сверяет с базой ПУТИ
# МОДУЛЕЙ из графа сборки. Внесённый код живёт под путём фундамента
# (`github.com/PRO-Robotech/corelib/internal/oauth2/...`), и в графе модулей
# апстрима нет вовсе — это держит `oauth2-provenance.sh`, шаг 1. Значит
# `govulncheck ./...` об уязвимости `github.com/ory/fosite@v0.49.0` не узнает
# НИКОГДА: для него этого кода не существует. Без этого гейта объявленная
# уязвимость внесённого движка не была бы замечена ни одной проверкой.
#
# ЧТО ДЕЛАЕТ. Берёт пины из ЕДИНСТВЕННОГО их места — блока «```upstream-pins»
# файла `internal/oauth2/PROVENANCE.md` (разборщик `upstream-pins.sh`, общий с
# гейтом происхождения) — и по каждой записи спрашивает базу OSV
# (`POST https://api.osv.dev/v1/query`, экосистема Go): какие уведомления
# затрагивают ЭТОТ модуль в ЭТОЙ версии. В OSV сведены и база уязвимостей Go
# (`GO-…`), и уведомления GitHub (`GHSA-…`).
#
# КАСАЕТСЯ ЛИ УВЕДОМЛЕНИЕ ВНЕСЁННОГО. Из `github.com/ory/x` внесён ОДИН пакет
# (`errorsx`); уведомление о другом пакете того же модуля фундамент не
# затрагивает, и краснеть на нём значило бы приучить читателя к ложному
# красному. Решение принимается по группе уведомлений, связанных псевдонимом
# (`GHSA-…` и его `GO-…` — одна уязвимость):
#   * группа называет пакеты этого модуля (`ecosystem_specific.imports`) —
#     касается, если среди них внесённый пакет или вложенный в него; запись
#     «модуль целиком» касается всегда;
#   * группа пакетов НЕ называет — касается: уведомление уровня модуля, и
#     доказать, что внесённый пакет не затронут, нечем.
# Отозванное уведомление (`withdrawn`) не касается ничего и считается отдельно.
#
# Исходов ТРИ:
#   0 — зелёный: спрошены ВСЕ записи, уведомлений, касающихся внесённого, нет;
#   1 — КРАСНЫЙ: есть уведомление, касающееся внесённого, — названо поимённо;
#   2 — НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: база не ответила, ответ не разобрался, база
#       ответила отказом, записи не разобрались.
#
# ТРЕТИЙ ИСХОД НЕСУЩИЙ: гейт, не дозвавшийся до базы, без него печатал бы
# «уведомлений нет» — отказ выглядел бы ровно как чистота.
#
# КОГДА ИСПОЛНЯЕТСЯ — см. задание `upstream-advisories` в
# `.github/workflows/ci.yml`: на каждом запросе на слияние и отправке в ствол И
# ПО РАСПИСАНИЮ, раз в сутки. Расписание несущее: уведомление появляется без
# единой правки дерева, и гейт, исполняемый только на правке, узнал бы о нём
# лишь при следующей.
set -uo pipefail

OSV_QUERY_URL="${OSV_QUERY_URL:-https://api.osv.dev/v1/query}"

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd) || { echo "не найти каталог сценария" >&2; exit 2; }
self="$here/$(basename "${BASH_SOURCE[0]}")"
# shellcheck source-path=SCRIPTDIR source=upstream-pins.sh
. "$here/upstream-pins.sh" || { echo "нет разборщика записей $here/upstream-pins.sh" >&2; exit 2; }

# Решение по одному ответу. Вход — массив уведомлений (все страницы), $m —
# модуль, $s — внесённый подкаталог (`.` — модуль целиком). Выход — объект с
# группами и их вердиктом.
# shellcheck disable=SC2016
read -r -d '' VERDICT_JQ <<'JQ'
def gokey: if (.id | startswith("GO-")) then .id
           else (((.aliases // []) | map(select(startswith("GO-"))) | .[0]) // .id) end;
def imports($m): [ .affected[]? | select(.package.name == $m and (.package.ecosystem // "") == "Go")
                   | .ecosystem_specific.imports[]?.path ];
def touches($m; $s): if $s == "." then true
                     else any(.[]; . == ($m + "/" + $s) or startswith($m + "/" + $s + "/")) end;
. as $all
| ($all | map(select(.withdrawn != null)) | length) as $withdrawn
| [ $all | map(select(.withdrawn == null)) | group_by(gokey)[]
    | { key: (.[0] | gokey),
        ids: (map([.id] + (.aliases // [])) | add | unique),
        imports: (map(imports($m)) | add | unique) }
    | . + { verdict: (if (.imports | length) == 0 then "module"
                      elif (.imports | touches($m; $s)) then "package"
                      else "other" end) } ] as $groups
| { total: ($all | length), withdrawn: $withdrawn, groups: $groups }
JQ

# osv_query <модуль> <версия без v> <жетон страницы|""> — печатает тело ответа.
# Код: 0 — ответ получен; 2 — нет ответа.
osv_query() {
    local m="$1" ver="$2" token="$3"
    if [ -n "${OSV_FIXTURE_DIR:-}" ]; then
        # Самопроверка: ответы базы — ЗАХВАЧЕННЫЕ файлы, а не сеть (см. --self-test).
        local f="$OSV_FIXTURE_DIR/${m//\//_}@${ver}${token:+.$token}.json"
        [ -f "$f" ] || { echo "нет захваченного ответа $f" >&2; return 2; }
        cat "$f"
        return 0
    fi
    local body
    body=$(jq -cn --arg m "$m" --arg v "$ver" --arg t "$token" \
        '{package: {name: $m, ecosystem: "Go"}, version: $v} + (if $t == "" then {} else {page_token: $t} end)')
    curl -sS --fail-with-body --max-time 30 --retry 2 \
         -H 'Content-Type: application/json' -X POST --data "$body" "$OSV_QUERY_URL"
}

gate() {
    local file="$1"
    pins_read "$file" || { echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: записи происхождения не разобраны." >&2; return 2; }

    echo "=== УВЕДОМЛЕНИЯ ОБ УЯЗВИМОСТЯХ ВНЕСЁННЫХ АПСТРИМОВ ==="
    echo "база     : $OSV_QUERY_URL${OSV_FIXTURE_DIR:+ (захваченные ответы: $OSV_FIXTURE_DIR)}"
    echo "записей  : $PIN_COUNT (из $file)"

    local i queried=0 pages=0 total=0 withdrawn=0 groups=0 touching=0 other=0
    for ((i = 0; i < PIN_COUNT; i++)); do
        local m="${PIN_MODULE[$i]}" v="${PIN_VERSION[$i]}" s="${PIN_SUBDIR[$i]}"
        local ver="${v#v}" token="" all='[]' resp
        while :; do
            if ! resp=$(osv_query "$m" "$ver" "$token"); then
                echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: база не ответила на запрос о $m@$v${token:+ (страница $token)}." >&2
                [ -n "$resp" ] && echo "  ответ: $(printf '%s' "$resp" | head -c 300)" >&2
                return 2
            fi
            if ! printf '%s' "$resp" | jq -e 'type == "object"' >/dev/null 2>&1; then
                echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: ответ о $m@$v не разбирается как объект JSON: $(printf '%s' "$resp" | head -c 200)" >&2
                return 2
            fi
            # Отказ базы приходит объектом {code, message} — это не «уведомлений нет».
            if printf '%s' "$resp" | jq -e 'has("code") or has("message")' >/dev/null 2>&1; then
                echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: база отказала на запрос о $m@$v: $(printf '%s' "$resp" | jq -c .)" >&2
                return 2
            fi
            if ! printf '%s' "$resp" | jq -e '(.vulns // []) | type == "array"' >/dev/null 2>&1; then
                echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: в ответе о $m@$v поле vulns не массив" >&2
                return 2
            fi
            pages=$((pages + 1))
            all=$(jq -cn --argjson a "$all" --argjson r "$resp" '$a + ($r.vulns // [])')
            token=$(printf '%s' "$resp" | jq -r '.next_page_token // empty')
            [ -z "$token" ] && break
        done
        queried=$((queried + 1))

        local verdict
        verdict=$(printf '%s' "$all" | jq -c --arg m "$m" --arg s "$s" "$VERDICT_JQ") \
            || { echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: решение по ответу о $m@$v не вычислилось" >&2; return 2; }

        local n_total n_wd n_groups n_touch n_other
        n_total=$(jq -r '.total' <<<"$verdict")
        n_wd=$(jq -r '.withdrawn' <<<"$verdict")
        n_groups=$(jq -r '.groups | length' <<<"$verdict")
        n_touch=$(jq -r '[.groups[] | select(.verdict != "other")] | length' <<<"$verdict")
        n_other=$(jq -r '[.groups[] | select(.verdict == "other")] | length' <<<"$verdict")
        total=$((total + n_total)); withdrawn=$((withdrawn + n_wd)); groups=$((groups + n_groups))
        touching=$((touching + n_touch)); other=$((other + n_other))

        echo
        echo "--- ${PIN_LOCAL[$i]} ← $m@$v (внесено: $([ "$s" = "." ] && echo "модуль целиком" || echo "пакет $m/$s"))"
        echo "   уведомлений $n_total · отозвано $n_wd · уязвимостей (групп по псевдонимам) $n_groups · касаются внесённого $n_touch · не касаются $n_other"
        jq -r '.groups[] | select(.verdict == "package")
               | "   КАСАЕТСЯ  \(.key) [\(.ids | join(", "))] — затронут внесённый пакет: \(.imports | join(", "))"' <<<"$verdict"
        jq -r '.groups[] | select(.verdict == "module")
               | "   КАСАЕТСЯ  \(.key) [\(.ids | join(", "))] — уровень модуля, пакеты не названы"' <<<"$verdict"
        jq -r '.groups[] | select(.verdict == "other")
               | "   мимо      \(.key) — затронуты только невнесённые пакеты: \(.imports | join(", "))"' <<<"$verdict"
    done

    echo
    echo "итог: записей $PIN_COUNT · спрошено $queried · страниц ответа $pages · уведомлений $total · отозвано $withdrawn · уязвимостей $groups · касаются внесённого $touching · не касаются $other"
    if [ "$touching" -gt 0 ]; then
        echo "КРАСНЫЙ: уведомлений об уязвимостях, касающихся внесённого кода, — $touching. Поднять апстрим до исправленной версии (процедура — internal/oauth2/PROVENANCE.md, «Как обновлять апстрим») либо разобрать уведомление и снять внесённое." >&2
        return 1
    fi
    echo "ЗЕЛЁНЫЙ: об этих версиях внесённых апстримов уведомлений об уязвимостях нет."
    return 0
}

# --- самопроверка: доказательство инъекцией в обе стороны ---------------------
#
# Ответы базы — ЗАХВАЧЕННЫЕ, а не сочинённые: `.github/scripts/testdata/osv/`,
# снято 2026-09-22 запросом
#   curl -X POST https://api.osv.dev/v1/query \
#        -d '{"package":{"name":"<модуль>","ecosystem":"Go"},"version":"<версия>"}'
# и сокращено до полей, которые читает гейт (id, aliases, withdrawn,
# affected[].package, affected[].ecosystem_specific.imports[].path) фильтром
# `jq -S`; остальное гейт не читает, а 107 КиБ ответа о golang.org/x/crypto
# в дереве ничего не доказывали бы сверх 16 КиБ. Сочинены только ФОРМЫ
# ТРАНСПОРТА, которых в захваченных ответах нет: вторая страница, отозванное
# уведомление, отказ базы, не-JSON (файлы `example.test_*`).
#
# Детерминизм — свойство самопроверки: база меняется каждый день, а
# доказательство способности упасть не имеет права меняться вместе с ней.
# Живой ответ читает сам гейт.
self_test() {
    local fixtures="$here/testdata/osv"
    [ -d "$fixtures" ] || { echo "нет захваченных ответов $fixtures" >&2; return 2; }
    local tmp; tmp=$(mktemp -d) || return 2
    # EXIT, а не RETURN: ловушка RETURN в bash глобальна и сработала бы на
    # возврате первой же вложенной функции, стерев записи до первой пробы.
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp'" EXIT
    local probes=0 failed=0

    record() { # record <модуль> <версия> <подкаталог> <каталог>
        printf 'upstream_module: %s\nupstream_version: %s\nupstream_commit: %s\nupstream_sum: %s\nupstream_subdir: %s\nupstream_license: Apache-2.0\nlocal_path: %s\nlocal_patch: none\n' \
            "$1" "$2" 0123456789abcdef0123456789abcdef01234567 \
            "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" "$3" "$4"
    }
    pins() { # pins <файл> <запись>... — блок из записей через пустую строку
        # Подстановка команды срезает у записи конечный перевод строки, поэтому
        # он возвращается здесь: иначе закрывающая ограда легла бы в строку
        # последнего ключа, и блок остался бы незакрытым.
        local out="$1"; shift
        { echo '```upstream-pins'; local first=1 r
          for r in "$@"; do [ "$first" -eq 1 ] || echo; first=0; printf '%s\n' "$r"; done
          echo '```'; } > "$out"
    }
    run() { # run <ожидаемый код> <имя> <образец в выводе|""> <файл записей> [переменные среды...]
        local want="$1" name="$2" needle="$3" file="$4" got=0 out; shift 4
        probes=$((probes + 1))
        # `bash <путь>`, а не `$0`: вызванный без каталога (`bash имя.sh`)
        # сценарий по `$0` не нашёл бы сам себя — код 127 на КАЖДОЙ пробе.
        out=$(env "$@" bash "$self" "$file" 2>&1) || got=$?
        if [ "$got" -ne "$want" ]; then
            echo "  ПРОВАЛ $name — ждали код $want, получили $got" >&2
            printf '%s\n' "$out" | sed 's/^/      /' >&2
            failed=$((failed + 1)); return
        fi
        if [ -n "$needle" ] && ! grep -qF -- "$needle" <<<"$out"; then
            echo "  ПРОВАЛ $name — код $got верный, но в выводе нет «$needle»: находка не названа" >&2
            printf '%s\n' "$out" | sed 's/^/      /' >&2
            failed=$((failed + 1)); return
        fi
        echo "  ok   $name (код $got)"
    }

    local fx="OSV_FIXTURE_DIR=$fixtures"
    pins "$tmp/clean.md"    "$(record github.com/ory/fosite v0.49.0 . internal/oauth2)"
    pins "$tmp/vuln.md"     "$(record github.com/ory/fosite v0.30.0 . internal/oauth2)"
    pins "$tmp/both.md"     "$(record golang.org/x/crypto v0.1.0 bcrypt internal/oauth2deps/bcrypt)" \
                            "$(record github.com/ory/fosite v0.30.0 . internal/oauth2)"
    pins "$tmp/bcrypt.md"   "$(record golang.org/x/crypto v0.1.0 bcrypt internal/oauth2deps/bcrypt)"
    pins "$tmp/ssh.md"      "$(record golang.org/x/crypto v0.1.0 ssh internal/oauth2deps/ssh)"
    pins "$tmp/sshagent.md" "$(record golang.org/x/crypto v0.1.0 ssh/agent internal/oauth2deps/sshagent)"
    pins "$tmp/ss.md"       "$(record golang.org/x/crypto v0.1.0 ss internal/oauth2deps/ss)"
    pins "$tmp/paged.md"    "$(record example.test/paged v1.0.0 . internal/paged)"
    pins "$tmp/withdrawn.md" "$(record example.test/withdrawn v1.0.0 . internal/withdrawn)"
    pins "$tmp/refused.md"  "$(record example.test/refused v1.0.0 . internal/refused)"
    pins "$tmp/garbage.md"  "$(record example.test/garbage v1.0.0 . internal/garbage)"
    pins "$tmp/missing.md"  "$(record example.test/missing v1.0.0 . internal/missing)"
    # shellcheck disable=SC2016  # обратные кавычки — ограда блока, а не подстановка
    printf '```upstream-pins\n```\n' > "$tmp/empty.md"

    echo "=== гейт уведомлений об уязвимостях: доказательство инъекцией ==="
    # (−) ПОЛОЖИТЕЛЬНЫЙ БЛИЗНЕЦ: без него всё нижеследующее зеленело бы на гейте,
    # который краснеет всегда.
    run 0 "(−) fosite v0.49.0: база уведомлений не знает — зелёное" "касаются внесённого 0" "$tmp/clean.md" "$fx"
    # (+) один факт против близнеца — версия. Настоящие уведомления о fosite
    # v0.30.0; GHSA-grfp-q2mm-hfp6 псевдонима GO-… не имеет — уровень модуля.
    run 1 "(+) fosite v0.30.0: уязвимость названа поимённо" "GO-2021-0110" "$tmp/vuln.md" "$fx"
    run 1 "(+) fosite v0.30.0: уведомление без пакетов — уровень модуля, касается" "GHSA-grfp-q2mm-hfp6 [CVE-2020-15234, GHSA-grfp-q2mm-hfp6] — уровень модуля" "$tmp/vuln.md" "$fx"
    run 1 "(+) группы по псевдонимам: 5 уведомлений — 3 уязвимости" "уведомлений 5 · отозвано 0 · уязвимостей (групп по псевдонимам) 3" "$tmp/vuln.md" "$fx"
    run 1 "(+) чистая запись не гасит соседнюю уязвимую" "спрошено 2 · страниц ответа 2 · уведомлений 46 · отозвано 0 · уязвимостей 26 · касаются внесённого 3 · не касаются 23" "$tmp/both.md" "$fx"
    # Подкаталог: те же 41 настоящее уведомление о golang.org/x/crypto v0.1.0 —
    # все о ssh, ssh/agent, ssh/knownhosts, openpgp.
    run 0 "(−) внесён bcrypt: уведомления о ssh/openpgp его не касаются" "касаются внесённого 0 · не касаются 23" "$tmp/bcrypt.md" "$fx"
    run 1 "(+) внесён ssh: те же уведомления касаются (вложенные ssh/agent и ssh/knownhosts — тоже)" "касаются внесённого 22 · не касаются 1" "$tmp/ssh.md" "$fx"
    run 1 "(+) внесён ssh/agent: касается своё, уведомления о родителе ssh — мимо" "касаются внесённого 5 · не касаются 18" "$tmp/sshagent.md" "$fx"
    run 0 "(−) внесён «ss»: строковый префикс пути ssh пакетом не считается" "касаются внесённого 0 · не касаются 23" "$tmp/ss.md" "$fx"
    # Формы транспорта.
    run 1 "(+) вторая страница прочитана: уязвимость лежит только на ней" "TEST-0002" "$tmp/paged.md" "$fx"
    run 1 "(+) отозванное уведомление считается отдельно и не касается" "страниц ответа 2 · уведомлений 2 · отозвано 1 · уязвимостей 1 · касаются внесённого 1" "$tmp/paged.md" "$fx"
    run 0 "(−) только отозванное уведомление — зелёное" "уведомлений 1 · отозвано 1 · уязвимостей 0 · касаются внесённого 0" "$tmp/withdrawn.md" "$fx"
    # (+) главный класс: уведомлений «нет», потому что спросить НЕ УДАЛОСЬ.
    run 2 "(+) база ответила отказом {code,message} — НЕ СОСТОЯЛОСЬ, а не чисто" "база отказала" "$tmp/refused.md" "$fx"
    run 2 "(+) ответ не JSON — НЕ СОСТОЯЛОСЬ" "не разбирается как объект JSON" "$tmp/garbage.md" "$fx"
    run 2 "(+) ответа нет — НЕ СОСТОЯЛОСЬ" "база не ответила" "$tmp/missing.md" "$fx"
    run 2 "(+) база недоступна по сети — НЕ СОСТОЯЛОСЬ" "база не ответила" "$tmp/clean.md" "OSV_QUERY_URL=http://127.0.0.1:9/v1/query"
    run 2 "(+) записей 0 — НЕ СОСТОЯЛОСЬ, а не «уязвимостей нет»" "записей 0" "$tmp/empty.md" "$fx"

    echo
    echo "oauth2-upstream-advisories --self-test: проб исполнено $probes, провалов $failed"
    [ "$probes" -eq 0 ] && { echo "ПРОВАЛ: ни одной пробы не исполнено" >&2; return 2; }
    [ "$failed" -gt 0 ] && return 1
    return 0
}

command -v jq   >/dev/null 2>&1 || { echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: нет jq" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: нет curl" >&2; exit 2; }

if [ "${1:-}" = "--self-test" ]; then
    self_test
    exit $?
fi
if [ "$#" -gt 1 ]; then
    echo "довод один: [<файл записей>]" >&2
    exit 2
fi
if [ "$#" -eq 1 ]; then
    gate "$1"
else
    root=$(git -C "$here" rev-parse --show-toplevel 2>/dev/null) || { echo "НАБЛЮДЕНИЕ НЕ СОСТОЯЛОСЬ: не git-дерево" >&2; exit 2; }
    gate "$root/internal/oauth2/PROVENANCE.md"
fi
exit $?
