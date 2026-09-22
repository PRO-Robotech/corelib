#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# oauth2-provenance.sh — МАШИННАЯ проверка происхождения внесённого чужого кода
# движка OAuth2: поддерева `internal/oauth2/` и его зависимостей в
# `internal/oauth2deps/`. Утверждение «это апстрим такой-то версии плюс вот эта
# правка» здесь не слово, а воспроизводимый опыт.
#
# Исходов ТРИ:
#   0 — зелёный: каждая область ВОССТАНОВЛЕНА из апстрима и совпала ПОБАЙТОВО;
#   1 — КРАСНЫЙ: совпадения нет, либо нарушено одно из утверждений ниже —
#       расхождение напечатано целиком;
#   2 — ПРОВЕРКА НЕ СОСТОЯЛАСЬ: апстрим недоступен, нет инструмента, записи
#       происхождения не разобрались.
#
# ТРЕТИЙ ИСХОД НЕСУЩИЙ. Проверка, не сумевшая скачать апстрим, без него
# печатала бы «расхождений нет» — то есть отказ выглядел бы как чистота.
#
# ПИНЫ ЗДЕСЬ НЕ ЗАПИСАНЫ. Их единственное место — блок «```upstream-pins»
# файла `internal/oauth2/PROVENANCE.md`; разбирает его единственный разборщик
# `.github/scripts/upstream-pins.sh`. Запись — модуль, версия, коммит, сумма,
# подкаталог апстрима
# (`.` — модуль целиком), лицензия, каталог здесь, файл правки (`none` — правки
# нет).
#
# ЧТО ИМЕННО УТВЕРЖДАЕТСЯ, по шагам:
#   0. Каждый внесённый каталог назван записью, и каждая запись называет
#      существующий каталог. Внесённые каталоги — сам `internal/oauth2` и
#      каждый прямой подкаталог `internal/oauth2deps/` (состав — из индекса
#      git). Каталог без записи не сверялся бы и не наблюдался бы — молча.
#   1. Ни один внесённый апстрим НЕ стоит в графе модулей фундамента
#      (`go list -m all`). Поддерево, а не зависимость: второй экземпляр того же
#      кода модулем — два места об одном предмете, и сканер уязвимостей видел
#      бы только одно из них.
#   2. Байты апстрима те самые: контрольная сумма из `go mod download` равна
#      пину. Модуля нет в go.sum фундамента (см. п. 1), поэтому скачанное
#      сверяет с базой контрольных сумм сам `go` (GOSUMDB).
#   3. Байты соответствуют тегу и коммиту VCS: `.Origin.Hash` равен пину.
#   4. Лицензия апстрима совместима с Apache-2.0 фундамента: объявленный
#      идентификатор — из закрытого перечня, и файл LICENSE апстрима несёт
#      признак именно этой лицензии.
#   5. Дерево получается из апстрима ОБЪЯВЛЕННЫМ преобразованием: взять модуль
#      целиком за вычетом обвязки либо один подкаталог с LICENSE модуля,
#      переписать пути импорта ВСЕХ внесённых апстримов на пути фундамента,
#      поставить явное имя корневому пакету, если оно не совпадает с последним
#      элементом пути, прогнать gofmt.
#   6. Остаток расхождения — РОВНО файл правки записи, и после его наложения
#      расхождений НЕТ НИ ОДНОГО.
#
# То есть «наша правка» — не список в документе, а файл, который обязан
# наложиться и обязан исчерпать разницу. Разъехалось одно с другим — красный.
set -uo pipefail

PROVENANCE_FILE="internal/oauth2/PROVENANCE.md"
LOCAL_MODULE="github.com/PRO-Robotech/corelib"
# Корень поддерева движка — сам внесённый каталог; корень зависимостей движка —
# каталог, КАЖДЫЙ прямой подкаталог которого внесён.
ENGINE_ROOT="internal/oauth2"
DEPS_ROOT="internal/oauth2deps"

# Обвязка апстрима, НЕ вносимая в фундамент (для записей «модуль целиком»).
# Список — часть утверждения: появится в апстриме новый файл этого класса, и
# проверка покраснеет, а не промолчит.
STRIP_DIRS=(.github scripts docs)
STRIP_FILES=(
  CHANGELOG.md HISTORY.md README.md CODE_OF_CONDUCT.md CONTRIBUTING.md
  SECURITY.md MAINTAINERS Makefile .travis.yml .gitignore .golangci.yml
  .nancy-ignore .prettierignore .reference-ignore package.json
  package-lock.json fosite.png generate-mocks.sh go.mod go.sum
  tools.go go_mod_indirect_pins.go generate.go
)

die_unavailable() { echo "происхождение: $* — ПРОВЕРКА НЕ СОСТОЯЛАСЬ." >&2; exit 2; }

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || die_unavailable "не git-дерево"
cd "$repo_root" || die_unavailable "не войти в корень дерева"

for tool in go jq diff git patch gofmt; do
    command -v "$tool" >/dev/null 2>&1 || die_unavailable "нет $tool"
done

# shellcheck source-path=SCRIPTDIR source=upstream-pins.sh
. "$repo_root/.github/scripts/upstream-pins.sh" || die_unavailable "нет разборщика записей"
pins_read "$PROVENANCE_FILE" || die_unavailable "записи происхождения не разобраны"

echo "=== ПРОИСХОЖДЕНИЕ внесённого кода движка OAuth2 ==="
echo "записей происхождения: $PIN_COUNT (из $PROVENANCE_FILE)"

rc=0
red() { echo "КРАСНЫЙ: $*" >&2; rc=1; }

# --- 0. каждый внесённый каталог назван записью, и наоборот ------------------
declare -A pinned=()
for ((i = 0; i < PIN_COUNT; i++)); do pinned["${PIN_LOCAL[$i]}"]=1; done

areas=("$ENGINE_ROOT")
while IFS= read -r child; do
    [ -n "$child" ] && areas+=("$DEPS_ROOT/$child")
done < <(git ls-files -- "$DEPS_ROOT" | awk -F/ 'NF > 3 { print $3 }' | sort -u)
echo "внесённых каталогов в индексе git: ${#areas[@]}"

for area in "${areas[@]}"; do
    if [ -z "${pinned[$area]+set}" ]; then
        red "каталог $area внесён, но записи происхождения у него нет — не сверяется и не наблюдается."
    fi
done
declare -A present=()
for area in "${areas[@]}"; do present["$area"]=1; done
for ((i = 0; i < PIN_COUNT; i++)); do
    local_path="${PIN_LOCAL[$i]}"
    if [ -z "$(git ls-files -- "$local_path" | head -1)" ]; then
        red "запись ${PIN_MODULE[$i]}/${PIN_SUBDIR[$i]} называет каталог $local_path, которого в индексе git нет."
    elif [ -z "${present[$local_path]+set}" ]; then
        red "каталог $local_path записи ${PIN_MODULE[$i]} лежит вне $ENGINE_ROOT и прямых подкаталогов $DEPS_ROOT."
    fi
done

# --- 1. ни один внесённый апстрим не стоит в графе модулей --------------------
graph=$(go list -m -f '{{.Path}}' all 2>/dev/null) || die_unavailable "граф модулей не построился (go list -m all)"
[ -n "$graph" ] || die_unavailable "граф модулей пуст"
echo "модулей в графе фундамента: $(printf '%s\n' "$graph" | wc -l)"
declare -A in_graph_reported=()
for ((i = 0; i < PIN_COUNT; i++)); do
    m="${PIN_MODULE[$i]}"
    [ -n "${in_graph_reported[$m]+set}" ] && continue
    if printf '%s\n' "$graph" | grep -qxF -- "$m"; then
        in_graph_reported["$m"]=1
        red "апстрим $m внесён поддеревом И стоит в графе модулей фундамента: второй экземпляр того же кода. Цепочка: $(go mod why -m "$m" 2>/dev/null | sed -n '2,$p' | tr '\n' ' ')"
    fi
done

# --- правила переписи путей импорта: из ВСЕХ записей сразу ---------------------
# Кавычка в образце несущая: переписываются СТРОКОВЫЕ ЛИТЕРАЛЫ, а упоминания в
# комментариях остаются свидетельством происхождения.
#
# Правило записи «модуль целиком» — открывающая кавычка и путь модуля, БЕЗ
# границы после него. Это не небрежность, а преобразование, которым поддерево
# внесено: так переписаны не только пути импорта, но и константа
# `defaultJWKSFetcherStrategyCachePrefix` в `client_authentication_jwks_strategy.go`
# (`"github.com/ory/fosite.DefaultJWKSFetcherStrategy:"`). Правило с границей
# (`"модуль"` и `"модуль/`) её не трогало бы и дало бы КРАСНЫЙ на неизменном
# дереве — замерено контрольным прогоном при сведении пинов в один блок.
#
# Правило записи «подкаталог» — ровно путь этого пакета и его вложенных: из
# чужого модуля внесён один пакет, и префикс модуля переписал бы импорты
# НЕвнесённых пакетов на несуществующие пути.
rewrite=()
for ((i = 0; i < PIN_COUNT; i++)); do
    dst="$LOCAL_MODULE/${PIN_LOCAL[$i]}"
    if [ "${PIN_SUBDIR[$i]}" = "." ]; then
        rewrite+=(-e "s|\"${PIN_MODULE[$i]}|\"$dst|g")
    else
        src="${PIN_MODULE[$i]}/${PIN_SUBDIR[$i]}"
        rewrite+=(-e "s|\"$src\"|\"$dst\"|g" -e "s|\"$src/|\"$dst/|g")
    fi
done

work=$(mktemp -d) || die_unavailable "не создать временный каталог"
trap 'rm -rf "$work"' EXIT

verified=0
for ((i = 0; i < PIN_COUNT; i++)); do
    m="${PIN_MODULE[$i]}"; v="${PIN_VERSION[$i]}"; sub="${PIN_SUBDIR[$i]}"
    local_path="${PIN_LOCAL[$i]}"; local_import="$LOCAL_MODULE/$local_path"
    echo
    echo "--- $local_path ← $m@$v (подкаталог апстрима: $sub)"
    [ -d "$local_path" ] || { echo "   каталога нет — сверять нечего (названо выше)"; continue; }

    meta=$(go mod download -json "$m@$v" 2>/dev/null) || die_unavailable "апстрим $m@$v не скачался"
    echo "$meta" | jq -e . >/dev/null 2>&1 || die_unavailable "вывод go mod download для $m не разбирается"
    got_sum=$(echo "$meta"    | jq -r '.Sum // empty')
    got_commit=$(echo "$meta" | jq -r '.Origin.Hash // empty')
    src_dir=$(echo "$meta"    | jq -r '.Dir // empty')
    [ -n "$got_sum" ] || die_unavailable "в выводе для $m нет контрольной суммы"
    { [ -n "$src_dir" ] && [ -d "$src_dir" ]; } || die_unavailable "каталог апстрима $m не найден"

    if [ "$got_sum" != "${PIN_SUM[$i]}" ]; then
        red "контрольная сумма $m@$v разошлась: пин ${PIN_SUM[$i]}, получено $got_sum"
    else
        echo "   сумма    : совпала ($got_sum)"
    fi
    if [ -z "$got_commit" ]; then
        # Прокси отдаёт `.Origin` не для всех версий: для старых публикаций
        # сведений о VCS в нём нет (так у `github.com/ory/go-convenience@v0.1.0`).
        # Пин коммита, который прокси не подтверждает, без этой ветки не
        # сверялся бы НИКОГДА — ни одним прогоном. Поэтому модуль скачивается
        # второй раз, прямо из VCS, в отдельный кэш: `go` сам вычисляет сумму
        # из репозитория и сверяет её с базой контрольных сумм, а `.Origin.Hash`
        # называет коммит тега. Не удалось — привязка не проверена, и это
        # «проверка не состоялась», а не зелёный.
        direct=$(GOMODCACHE="$work/modcache-direct" GOPROXY=direct GOFLAGS=-modcacherw \
                 go mod download -json "$m@$v" 2>/dev/null) \
            || die_unavailable "прокси не назвал коммит $m@$v, а прямое скачивание из VCS не удалось"
        got_commit=$(echo "$direct" | jq -r '.Origin.Hash // empty')
        direct_sum=$(echo "$direct" | jq -r '.Sum // empty')
        [ -n "$got_commit" ] || die_unavailable "прямое скачивание $m@$v не назвало коммит"
        if [ "$direct_sum" != "${PIN_SUM[$i]}" ]; then
            red "сумма $m@$v, вычисленная из VCS, разошлась с пином: пин ${PIN_SUM[$i]}, получено $direct_sum"
        fi
        echo "   коммит   : прокси сведений о VCS не несёт — взят из прямого скачивания"
    fi
    if [ "$got_commit" != "${PIN_COMMIT[$i]}" ]; then
        red "коммит $m@$v разошёлся: пин ${PIN_COMMIT[$i]}, получено $got_commit"
    else
        echo "   коммит   : совпал"
    fi

    lic="$src_dir/LICENSE"
    case "${PIN_LICENSE[$i]}" in
        Apache-2.0) lic_ok() { grep -q 'Apache License' "$lic" && grep -q 'Version 2.0' "$lic"; } ;;
        MIT)        lic_ok() { grep -q 'Permission is hereby granted, free of charge' "$lic"; } ;;
        *)          lic_ok() { return 1; }
                    red "лицензия ${PIN_LICENSE[$i]} записи $m не из перечня совместимых с Apache-2.0 (Apache-2.0, MIT)" ;;
    esac
    if [ ! -f "$lic" ]; then
        red "у апстрима $m@$v нет файла LICENSE"
    elif ! lic_ok; then
        red "LICENSE апстрима $m@$v не несёт признака объявленной лицензии ${PIN_LICENSE[$i]}"
    else
        echo "   лицензия : ${PIN_LICENSE[$i]}, признак в LICENSE апстрима найден"
    fi

    rebuilt="$work/rebuilt-$i"
    mkdir -p "$rebuilt" || die_unavailable "не создать рабочий каталог"
    if [ "$sub" = "." ]; then
        cp -r "$src_dir/." "$rebuilt/" 2>/dev/null || die_unavailable "не скопировать апстрим $m"
    else
        [ -d "$src_dir/$sub" ] || { red "в апстриме $m@$v нет подкаталога $sub"; continue; }
        cp -r "$src_dir/$sub/." "$rebuilt/" 2>/dev/null || die_unavailable "не скопировать $m/$sub"
        cp "$lic" "$rebuilt/LICENSE" 2>/dev/null || die_unavailable "не скопировать LICENSE $m"
    fi
    chmod -R u+w "$rebuilt"

    # Здесь и ниже `find` обходит ВРЕМЕННУЮ копию кэша модулей, а не дерево
    # фундамента: индекса git у неё нет, её состав и есть байты апстрима.
    ( cd "$rebuilt" || exit 2
      if [ "$sub" = "." ]; then
          for d in "${STRIP_DIRS[@]}";  do rm -rf -- "$d"; done
          for f in "${STRIP_FILES[@]}"; do rm -f  -- "$f"; done
      fi
      # Отбор файлов — через find, а НЕ через `grep -rl '^\t…'`. Табуляция в
      # образце значима: GNU grep в базовых регулярных выражениях читает `\t`
      # как букву `t`, и такой образец не совпал бы НИ С ЧЕМ — молча, с кодом 1,
      # неотличимо от «правка уже применена». Наблюдалось при заведении
      # сценария. GNU sed `\t` понимает как табуляцию и в образце, и в замене.
      find . -name '*.go' -type f -print0 | xargs -0 -r sed -i "${rewrite[@]}"
      # Корневой пакет, чьё имя не совпадает с последним элементом пути
      # (fosite лежит в `…/oauth2`): явное имя везде, где импорт корня безымянный.
      root_pkg=$(find . -maxdepth 1 -name '*.go' ! -name '*_test.go' -print0 \
                   | xargs -0 -r sed -n 's/^package \([a-z0-9_]*\)$/\1/p' | sort -u)
      if [ -n "$root_pkg" ] && [ "$(printf '%s\n' "$root_pkg" | wc -l)" -ne 1 ]; then
          echo "корневой пакет $m не единственный: $root_pkg" >&2; exit 2
      fi
      if [ -n "$root_pkg" ] && [ "$root_pkg" != "$(basename "$local_path")" ]; then
          find . -name '*.go' -type f -print0 | xargs -0 -r sed -i \
            -e "s|^\t\"$local_import\"$|\t$root_pkg \"$local_import\"|" \
            -e "s|^import \"$local_import\"$|import $root_pkg \"$local_import\"|" \
            -e "s|^\t\"$local_import\" //|\t$root_pkg \"$local_import\" //|"
      fi
      gofmt -l -w . >/dev/null 2>&1
    ) || die_unavailable "преобразование апстрима $m не выполнилось"

    patch_file="${PIN_PATCH[$i]}"
    if [ "$patch_file" != "none" ]; then
        [ -f "$patch_file" ] || die_unavailable "нет файла правки $patch_file"
        # Сперва ПРОБНОЕ наложение, потом настоящее. В обратном порядке
        # несостоявшееся наложение успевает применить удавшиеся куски, и
        # диагностика после него печатает «Reversed (or previously applied)
        # patch detected» про куски, которые на самом деле в порядке, — то есть
        # называет не ту причину.
        if ! dry=$(patch --batch --forward --dry-run -p1 -d "$rebuilt" < "$patch_file" 2>&1); then
            red "$patch_file не накладывается на восстановленный апстрим $m: правка в дереве и правка в файле разошлись."
            echo "$dry" >&2
            continue
        fi
        patch --batch --forward -s -p1 -d "$rebuilt" < "$patch_file" >/dev/null 2>&1 \
            || die_unavailable "$patch_file прошёл пробное наложение, но не наложился"
        echo "   правка   : $patch_file наложилась"
    else
        echo "   правка   : нет (none)"
    fi

    # Собственные файлы происхождения сравнению не подлежат: их в апстриме нет.
    #
    # Восстановленный апстрим выше перечисляется С ДИСКА намеренно: это копия
    # кэша модулей во временном каталоге, индекса git у неё нет и быть не
    # должно. Сторона фундамента сравнивается рабочим деревом — в конвейере это
    # и есть коммит; лишний неотслеживаемый файл здесь даёт КРАСНЫЙ, то есть
    # диск ошибается только в сторону отказа. Объём осмотренного — из индекса.
    delta=$(diff -ruN -x PROVENANCE.md -x PROVENANCE.patch "$rebuilt" "$local_path" 2>&1)
    if [ -n "$delta" ]; then
        red "восстановленный апстрим $m не совпал с $local_path:"
        echo "$delta" >&2
        continue
    fi
    echo "   дерево   : восстановлено из апстрима и совпало ПОБАЙТОВО ($(git ls-files -- "$local_path" | wc -l) файлов в индексе)"
    verified=$((verified + 1))
done

echo
echo "итог: записей $PIN_COUNT · совпало побайтово $verified · внесённых каталогов ${#areas[@]}"
if [ "$rc" -eq 0 ] && [ "$verified" -ne "$PIN_COUNT" ]; then
    red "сверено $verified из $PIN_COUNT записей"
fi
if [ "$rc" -eq 0 ]; then echo "ЗЕЛЁНЫЙ: происхождение подтверждено."; fi
exit "$rc"
