#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# oauth2-provenance.sh — МАШИННАЯ проверка происхождения поддерева
# `internal/oauth2/`. Утверждение «это fosite@v0.49.0 плюс вот эта правка»
# здесь не слово, а воспроизводимый опыт.
#
# Исходов ТРИ:
#   0 — зелёный: поддерево ВОССТАНОВЛЕНО из апстрима и совпало ПОБАЙТОВО;
#   1 — КРАСНЫЙ: совпадения нет, расхождение напечатано целиком;
#   2 — ПРОВЕРКА НЕ СОСТОЯЛАСЬ: апстрим недоступен, нет инструмента, нет входа.
#
# ТРЕТИЙ ИСХОД НЕСУЩИЙ. Проверка, не сумевшая скачать апстрим, без него
# печатала бы «расхождений нет» — то есть отказ выглядел бы как чистота.
#
# ЧТО ИМЕННО УТВЕРЖДАЕТСЯ, по шагам:
#   1. Байты апстрима те самые: контрольная сумма модуля из `go mod download`
#      совпадает с ПИНОМ ниже (та же сумма, что и в go.sum).
#   2. Байты соответствуют тегу и коммиту VCS: `.Origin.Hash` из того же вывода
#      совпадает с ПИНОМ коммита.
#   3. Дерево получается из апстрима ОБЪЯВЛЕННЫМ преобразованием: снять
#      перечисленную обвязку, переписать пути импорта, поставить явное имя
#      корневому пакету, прогнать gofmt.
#   4. Остаток расхождения — РОВНО `internal/oauth2/PROVENANCE.patch`, и после
#      его наложения расхождений НЕТ НИ ОДНОГО.
#
# То есть «наша правка» — не список в документе, а файл, который обязан
# наложиться и обязан исчерпать разницу. Разъехалось одно с другим — красный.
set -uo pipefail

UPSTREAM_MODULE="github.com/ory/fosite"
UPSTREAM_VERSION="v0.49.0"
UPSTREAM_SUM="h1:KNqO7RVt/1X8F08/UI0Y+GRvcpscCWgjqvpLBQPRovo="
UPSTREAM_COMMIT="653c812bc40cbda049857b432ad3346f1a53d42d"
LOCAL_PATH="internal/oauth2"
LOCAL_IMPORT="github.com/PRO-Robotech/corelib/internal/oauth2"
PATCH_FILE="${LOCAL_PATH}/PROVENANCE.patch"

# Обвязка апстрима, НЕ вносимая в фундамент. Список — часть утверждения:
# появится в апстриме новый файл этого класса, и проверка покраснеет, а не
# промолчит.
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

command -v go   >/dev/null 2>&1 || die_unavailable "нет go"
command -v jq   >/dev/null 2>&1 || die_unavailable "нет jq"
command -v diff >/dev/null 2>&1 || die_unavailable "нет diff"
command -v git  >/dev/null 2>&1 || die_unavailable "нет git"
[ -d "$LOCAL_PATH" ] || die_unavailable "нет каталога $LOCAL_PATH"
[ -f "$PATCH_FILE" ] || die_unavailable "нет файла правки $PATCH_FILE"

echo "=== ПРОИСХОЖДЕНИЕ $LOCAL_PATH ==="
echo "апстрим  : $UPSTREAM_MODULE@$UPSTREAM_VERSION"
echo "коммит   : $UPSTREAM_COMMIT"

meta=$(go mod download -json "$UPSTREAM_MODULE@$UPSTREAM_VERSION" 2>/dev/null) \
  || die_unavailable "апстрим не скачался"
echo "$meta" | jq -e . >/dev/null 2>&1 || die_unavailable "вывод go mod download не разбирается"

got_sum=$(echo "$meta"    | jq -r '.Sum // empty')
got_commit=$(echo "$meta" | jq -r '.Origin.Hash // empty')
src_dir=$(echo "$meta"    | jq -r '.Dir // empty')
[ -n "$got_sum" ]    || die_unavailable "в выводе нет контрольной суммы"
[ -n "$src_dir" ] && [ -d "$src_dir" ] || die_unavailable "каталог апстрима не найден"

rc=0
if [ "$got_sum" != "$UPSTREAM_SUM" ]; then
    echo "КРАСНЫЙ: контрольная сумма апстрима разошлась." >&2
    echo "  пин     : $UPSTREAM_SUM" >&2
    echo "  получено: $got_sum" >&2
    rc=1
else
    echo "сумма    : совпала ($got_sum)"
fi
if [ -z "$got_commit" ]; then
    echo "ПРЕДУПРЕЖДЕНИЕ: кэш модуля не несёт .Origin.Hash (скачан прокси без" >&2
    echo "  сведений о VCS). Привязка к коммиту НЕ проверена этим прогоном." >&2
elif [ "$got_commit" != "$UPSTREAM_COMMIT" ]; then
    echo "КРАСНЫЙ: коммит апстрима разошёлся." >&2
    echo "  пин     : $UPSTREAM_COMMIT" >&2
    echo "  получено: $got_commit" >&2
    rc=1
else
    echo "коммит   : совпал"
fi

work=$(mktemp -d) || die_unavailable "не создать временный каталог"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/rebuilt" || die_unavailable "не создать рабочий каталог"
cp -r "$src_dir/." "$work/rebuilt/" 2>/dev/null || die_unavailable "не скопировать апстрим"
chmod -R u+w "$work/rebuilt"

( cd "$work/rebuilt" || exit 2
  for d in "${STRIP_DIRS[@]}";  do rm -rf -- "$d"; done
  for f in "${STRIP_FILES[@]}"; do rm -f  -- "$f"; done
  # Отбор файлов — через find, а НЕ через `grep -rl '^\t…'`. Табуляция в
  # образце значима: GNU grep в базовых регулярных выражениях читает `\t` как
  # букву `t`, и такой образец не совпал бы НИ С ЧЕМ — молча, с кодом 1,
  # неотличимо от «правка уже применена». Наблюдалось при заведении сценария.
  # GNU sed `\t` понимает как табуляцию и в образце, и в замене.
  #
  # 1. пути импорта
  # 2. корневой пакет зовётся fosite, а последний элемент пути — oauth2:
  #    ставим явное имя везде, где импорт корня безымянный
  find . -name '*.go' -type f -print0 | xargs -0 -r sed -i \
    -e "s|\"$UPSTREAM_MODULE|\"$LOCAL_IMPORT|g" \
    -e "s|^\t\"$LOCAL_IMPORT\"$|\tfosite \"$LOCAL_IMPORT\"|" \
    -e "s|^import \"$LOCAL_IMPORT\"$|import fosite \"$LOCAL_IMPORT\"|" \
    -e "s|^\t\"$LOCAL_IMPORT\" //|\tfosite \"$LOCAL_IMPORT\" //|"
  # 3. форматирование
  gofmt -l -w . >/dev/null 2>&1
) || die_unavailable "преобразование апстрима не выполнилось"

command -v patch >/dev/null 2>&1 || die_unavailable "нет patch"
if ! patch --batch --forward -s -p1 -d "$work/rebuilt" < "$PATCH_FILE" >/dev/null 2>&1; then
    echo "КРАСНЫЙ: $PATCH_FILE не накладывается на восстановленный апстрим." >&2
    echo "  Значит правка в дереве и правка в файле разошлись." >&2
    patch --batch --forward --dry-run -p1 -d "$work/rebuilt" < "$PATCH_FILE" >&2
    exit 1
fi

# Собственные файлы происхождения сравнению не подлежат: их в апстриме нет.
delta=$(diff -ruN -x PROVENANCE.md -x PROVENANCE.patch "$work/rebuilt" "$LOCAL_PATH" 2>&1)
if [ -n "$delta" ]; then
    echo "КРАСНЫЙ: восстановленный апстрим не совпал с деревом." >&2
    echo "$delta" >&2
    exit 1
fi

echo "дерево   : восстановлено из апстрима и совпало ПОБАЙТОВО"
if [ "$rc" -eq 0 ]; then echo "ЗЕЛЁНЫЙ: происхождение подтверждено."; fi
exit "$rc"
