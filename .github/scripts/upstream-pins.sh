# shellcheck shell=bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# upstream-pins.sh — ЕДИНСТВЕННЫЙ разборщик записей происхождения внесённого
# чужого кода. Подключается (`source`), сам не исполняется.
#
# ЗАЧЕМ ОДИН РАЗБОРЩИК НА ДВА ГЕЙТА. Пины апстрима читают двое:
# `oauth2-provenance.sh` (байты те самые) и `oauth2-upstream-advisories.sh`
# (об этой версии не объявлено уязвимостей). Держи каждый свою копию пинов или
# свой разбор — и после первого подъёма версии один гейт судил бы новую
# версию, а другой старую, оба зелёные. Поэтому пины лежат в ОДНОМ месте —
# блоке «```upstream-pins» файла `internal/oauth2/PROVENANCE.md`, — и читаются
# ОДНОЙ функцией.
#
# ФОРМА БЛОКА ЗАКРЫТА. Запись — строки `ключ: значение` без пробелов в
# значении; записи разделены пустой строкой; ключей ровно восемь, каждый ровно
# один раз. Неизвестный ключ, пропущенный ключ, повтор, значение не той формы,
# второй блок, незакрытый блок, ноль записей — ОТКАЗ РАЗБОРА (код 2), а не
# «пропустим строку». Разборщик, молча отбрасывающий непонятную строку, — это
# распознаватель со слепой зоной: запись с опечаткой в ключе просто исчезла бы
# из обеих проверок, и обе остались бы зелёными.
#
# После успешного `pins_read <файл>` заполнены массивы одинаковой длины
# PIN_COUNT: PIN_MODULE PIN_VERSION PIN_COMMIT PIN_SUM PIN_SUBDIR PIN_LICENSE
# PIN_LOCAL PIN_PATCH.

pins_read() {
    local file="$1"
    PIN_COUNT=0
    PIN_MODULE=(); PIN_VERSION=(); PIN_COMMIT=(); PIN_SUM=()
    PIN_SUBDIR=(); PIN_LICENSE=(); PIN_LOCAL=(); PIN_PATCH=()

    [ -f "$file" ] || { echo "записи происхождения: нет файла $file" >&2; return 2; }

    local opens
    opens=$(grep -c '^```upstream-pins$' "$file")
    if [ "$opens" -ne 1 ]; then
        echo "записи происхождения: блоков upstream-pins в $file — $opens, нужен ровно один" >&2
        return 2
    fi

    local block
    if ! block=$(awk '
        /^```upstream-pins$/ { inside = 1; next }
        inside && /^```$/    { closed = 1; exit }
        inside               { print }
        END                  { if (!closed) exit 3 }
    ' "$file"); then
        echo "записи происхождения: блок upstream-pins в $file не закрыт" >&2
        return 2
    fi

    local -A rec=()
    local lineno=0 line key value

    _pins_flush() {
        [ "${#rec[@]}" -eq 0 ] && return 0
        local k
        for k in upstream_module upstream_version upstream_commit upstream_sum \
                 upstream_subdir upstream_license local_path local_patch; do
            if [ -z "${rec[$k]+set}" ]; then
                echo "записи происхождения: в записи №$((PIN_COUNT + 1)) нет ключа $k" >&2
                return 2
            fi
        done
        local i
        for ((i = 0; i < PIN_COUNT; i++)); do
            if [ "${PIN_LOCAL[$i]}" = "${rec[local_path]}" ]; then
                echo "записи происхождения: каталог ${rec[local_path]} назван дважды" >&2
                return 2
            fi
            if [ "${PIN_MODULE[$i]}" = "${rec[upstream_module]}" ] \
               && [ "${PIN_SUBDIR[$i]}" = "${rec[upstream_subdir]}" ]; then
                echo "записи происхождения: ${rec[upstream_module]}/${rec[upstream_subdir]} назван дважды" >&2
                return 2
            fi
        done
        PIN_MODULE+=("${rec[upstream_module]}")
        PIN_VERSION+=("${rec[upstream_version]}")
        PIN_COMMIT+=("${rec[upstream_commit]}")
        PIN_SUM+=("${rec[upstream_sum]}")
        PIN_SUBDIR+=("${rec[upstream_subdir]}")
        PIN_LICENSE+=("${rec[upstream_license]}")
        PIN_LOCAL+=("${rec[local_path]}")
        PIN_PATCH+=("${rec[local_patch]}")
        PIN_COUNT=$((PIN_COUNT + 1))
        rec=()
    }

    while IFS= read -r line || [ -n "$line" ]; do
        lineno=$((lineno + 1))
        if [ -z "$line" ]; then
            _pins_flush || return 2
            continue
        fi
        if [[ ! "$line" =~ ^([a-z_]+):\ ([^[:space:]]+)$ ]]; then
            echo "записи происхождения: строка $lineno блока не в форме «ключ: значение»: $line" >&2
            return 2
        fi
        key="${BASH_REMATCH[1]}"; value="${BASH_REMATCH[2]}"
        if [ -n "${rec[$key]+set}" ]; then
            echo "записи происхождения: ключ $key повторён в одной записи (строка $lineno)" >&2
            return 2
        fi
        case "$key" in
            upstream_module)
                [[ "$value" =~ ^[a-z0-9.-]+(/[A-Za-z0-9._-]+)+$ ]] || { echo "записи происхождения: модуль не в форме пути: $value" >&2; return 2; } ;;
            upstream_version)
                [[ "$value" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "записи происхождения: версия не в форме vX.Y.Z: $value" >&2; return 2; } ;;
            upstream_commit)
                [[ "$value" =~ ^[0-9a-f]{40}$ ]] || { echo "записи происхождения: коммит не в форме 40 hex: $value" >&2; return 2; } ;;
            upstream_sum)
                [[ "$value" =~ ^h1:[A-Za-z0-9+/]{43}=$ ]] || { echo "записи происхождения: сумма не в форме h1:…=: $value" >&2; return 2; } ;;
            upstream_subdir)
                [[ "$value" = "." || ( "$value" =~ ^[a-z0-9_-]+(/[a-z0-9_-]+)*$ ) ]] || { echo "записи происхождения: подкаталог апстрима не в форме: $value" >&2; return 2; } ;;
            upstream_license)
                [[ "$value" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "записи происхождения: лицензия не в форме SPDX: $value" >&2; return 2; } ;;
            local_path)
                [[ "$value" =~ ^internal(/[a-z0-9_]+)+$ ]] || { echo "записи происхождения: каталог не под internal/: $value" >&2; return 2; } ;;
            local_patch)
                [[ "$value" = "none" || ( "$value" =~ ^internal(/[A-Za-z0-9_.-]+)+$ ) ]] || { echo "записи происхождения: файл правки не в форме: $value" >&2; return 2; } ;;
            *)
                echo "записи происхождения: неизвестный ключ $key (строка $lineno)" >&2
                return 2 ;;
        esac
        rec[$key]="$value"
    done <<<"$block"
    _pins_flush || return 2

    if [ "$PIN_COUNT" -eq 0 ]; then
        echo "записи происхождения: блок upstream-pins в $file пуст — записей 0" >&2
        return 2
    fi
    return 0
}
