#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# ГЕЙТ ГРАНИЦЫ `oauthceremony`: ни один экспортированный элемент пакета не
# называет чужого типа.
#
# ЗАЧЕМ ОТДЕЛЬНЫЙ ГЕЙТ, ЕСЛИ ЭТО ПРОБЫ. `go test -run <имя>` возвращает НОЛЬ,
# когда ни одна проба не совпала с именем: удалённая или переименованная проба
# выглядит как пройденная. Правило границы — главное правило пакета, и держать
# его на проверке, которую снимает переименование, нельзя.
#
# Поэтому гейт не спрашивает у `go test` код возврата, а СЧИТАЕТ, сколько проб
# действительно прошло, и требует ровно ожидаемое число.
#
# Исходы: 0 — граница цела; 1 — граница нарушена либо проба пропала;
#         2 — проверка не состоялась (нет инструмента, не собрался пакет).

set -o errexit
set -o nounset
set -o pipefail

readonly PACKAGE='./oauthceremony/'
# Пробы границы, поимённо. Число записей ЗДЕСЬ и есть ожидаемое число
# пройденных проб: добавление пробы в пакет без правки этой строки гейт не
# заметит, а пропажу — заметит.
readonly PROBES=(
  'TestNoForeignTypeInExportedSurface'
  'TestExportedTypesCarryNoForeignTypeAtRuntime'
  'TestRuntimeRosterCoversEveryExportedType'
  'TestIssuedArtifactsCarryNoVendorPrefix'
)

fail_unavailable() {
  printf 'ПРОВЕРКА НЕ СОСТОЯЛАСЬ: %s\n' "$1" >&2
  exit 2
}

command -v go >/dev/null 2>&1 || fail_unavailable 'инструмент go не найден'

pattern="$(IFS='|'; printf '^(%s)$' "${PROBES[*]}")"

output=''
status=0
output="$(go test -count=1 -v -run "${pattern}" "${PACKAGE}" 2>&1)" || status=$?

if [[ "${status}" -ne 0 ]] && ! grep -q -- '--- FAIL' <<<"${output}"; then
  printf '%s\n' "${output}" >&2
  fail_unavailable "прогон проб завершился кодом ${status} без единой находки"
fi

passed="$(grep -c -- '^--- PASS: ' <<<"${output}" || true)"
failed="$(grep -c -- '^--- FAIL: ' <<<"${output}" || true)"
expected="${#PROBES[@]}"

printf 'пробы границы: ожидалось %d шт, прошло %d шт, не прошло %d шт\n' \
  "${expected}" "${passed}" "${failed}"

if [[ "${failed}" -ne 0 ]]; then
  printf '%s\n' "${output}" >&2
  printf 'ГРАНИЦА НАРУШЕНА: чужой тип виден в экспортированной поверхности пакета oauthceremony\n' >&2
  exit 1
fi

if [[ "${passed}" -ne "${expected}" ]]; then
  printf '%s\n' "${output}" >&2
  printf 'ПРОБА ПРОПАЛА: прошло %d шт вместо %d шт — правило границы больше не держится пробой\n' \
    "${passed}" "${expected}" >&2
  exit 1
fi

printf 'ГРАНИЦА ЦЕЛА: чужих типов в экспортированной поверхности oauthceremony нет\n'
exit 0
