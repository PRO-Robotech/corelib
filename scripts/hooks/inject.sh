#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# inject.sh — проба хука отправки, его провязки и целей Makefile. Каждое
# свойство — парой: дефект → ненулевой код и названная причина; законный
# близнец → код 0. Судятся scripts/hooks/install.sh, scripts/hooks/pre-push и
# корневой Makefile ЭТОЙ рабочей копии, скопированные в синтетические клоны во
# временном каталоге; файлов самого клона проба не трогает. Текст переходника
# берётся у производителя (`install.sh stub`), вердикт проб — у того же
# прогонщика, что в ci.yml (.github/scripts/go-test-verdict.py), а не своей
# копией.
#
# Третий прогон — старая редакция переходника v1 (побайтно та, что лежит в
# клонах, провязанных до corelib#24; sha256 8a2feff8…): на ненайденном адресате
# она выходит НУЛЁМ. Без этого прогона нечем показать, что дефект, от которого
# проба защищает, воспроизводим, а не выдуман. Тот же v1 предъявляется и хуку:
# клон с v1, отправляющий дерево С хуком, получает отказ — иначе v1 остался бы в
# клоне и пропустил бы молча следующую отправку дерева без хука.
#
# Подпись фикстурных коммитов и прочие настройки git — из собственного конфига
# пробы (GIT_CONFIG_GLOBAL во временном каталоге): клоны одноразовые, это не
# история, а конфиг машины (в том числе core.hooksPath) на них не влияет.
#
# ИСХОДЫ: 0 — все утверждения сошлись; 1 — хоть одно разошлось;
#         2 — не выполнилось (нет git/go/gofmt/make/python3, нет временного
#             каталога, пин не прочитан, нет прогонщика вердикта).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tree="$(cd "$here/../.." && pwd -P)"
INSTALL="$here/install.sh"
HOOK="$here/pre-push"
MAKEFILE="$tree/Makefile"
VERDICT="$tree/.github/scripts/go-test-verdict.py"

void() { echo "inject: НЕ ВЫПОЛНИЛОСЬ — $*" >&2; exit 2; }
for f in "$INSTALL" "$HOOK" "$VERDICT"; do [ -f "$f" ] || void "нет $f"; done
for t in git go gofmt make python3; do command -v "$t" >/dev/null 2>&1 || void "нет $t в PATH"; done
pin="$(sed -n 's/^GOLANGCI_LINT_VERSION="v\([0-9.]*\)"$/\1/p' "$HOOK")"
[ -n "$pin" ] || void "пин линтера в $HOOK не прочитан — сверять версию не с чем"

work="$(mktemp -d)" || void "нет временного каталога"
trap 'rm -rf "$work"' EXIT
work="$(cd "$work" && pwd -P)"

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
      GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_PREFIX CORELIB_SKIP_PREPUSH \
      MAKEFLAGS MAKELEVEL MFLAGS
cat > "$work/gitconfig" <<'CFG'
[user]
	name = probe
	email = probe@example.invalid
[init]
	defaultBranch = main
[commit]
	gpgsign = false
[advice]
	detachedHead = false
CFG
export GIT_CONFIG_GLOBAL="$work/gitconfig" GIT_CONFIG_NOSYSTEM=1
export GOWORK=off GOFLAGS=

pass=0
fail=0
ok()  { pass=$((pass + 1)); echo "  сошлось    $1"; }
bad() { fail=$((fail + 1)); echo "  РАЗОШЛОСЬ  $1"; }

# runc <команда…> — вывод в $out, код в $rc. runin <вход> <команда…> — то же со входом.
runc()  { out="$("$@" 2>&1)"; rc=$?; }
runin() { local input="$1"; shift; out="$("$@" 2>&1 <<<"$input")"; rc=$?; }
# with <ИМЯ=значение> <команда…> — команда (в том числе функция) с одной переменной окружения.
# shellcheck disable=SC2163  # экспортируется ИМЯ=значение из аргумента, так и задумано
with()  { local kv="$1"; shift; ( export "$kv"; "$@" ); }

# expect <имя> <код: число | nz> <образец…> — код и КАЖДЫЙ образец текста.
expect() {
    local name="$1" want="$2" p good=1 miss=""
    shift 2
    case "$want" in
        nz) [ "$rc" -ne 0 ] || good=0 ;;
        *)  [ "$rc" -eq "$want" ] || good=0 ;;
    esac
    for p in "$@"; do
        printf '%s' "$out" | grep -qF -- "$p" || { good=0; miss="$miss «$p»"; }
    done
    if [ "$good" = 1 ]; then
        ok "$name (код $rc)"
    else
        bad "$name: код $rc, ждали $want; нет образцов:${miss:- —}"
        printf '%s\n' "$out" | sed 's/^/      | /'
    fi
}
fact() { local name="$1"; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }
not_same() { ! cmp -s "$1" "$2"; }
no_ref() { [ -z "$(git -C "$1" ls-remote origin "refs/heads/$2")" ]; }
has_ref() { [ -n "$(git -C "$1" ls-remote origin "refs/heads/$2")" ]; }

zero=0000000000000000000000000000000000000000

# Старая редакция переходника v1 — побайтно (sha256 8a2feff8…): одна на обе
# фикстуры, чтобы провязка и хук судили ОДИН и тот же дефект.
cat > "$work/stub-v1" <<'V1'
#!/usr/bin/env bash
# СГЕНЕРИРОВАН `bash scripts/hooks/install.sh install` — правится НЕ здесь, а в scripts/hooks/pre-push.
# corelib-hook-stub v1
set -uo pipefail
top="$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
[ -n "$top" ] || top="$PWD"
real="$top/scripts/hooks/pre-push"
if [ ! -x "$real" ]; then
    echo "pre-push: в этой рабочей копии нет $real — проверок НЕ БЫЛО" >&2
    exit 0
fi
exec "$real" "$@"
V1
chmod +x "$work/stub-v1"
v1sum="$(sha256sum "$work/stub-v1" | cut -c1-8)"
[ "$v1sum" = 8a2feff8 ] || void "текст v1 в пробе не равен тому, что лежит в клонах (sha256 $v1sum…, ждали 8a2feff8…)"

# ════ ПРОВЯЗКА: переходник, install.sh и цели Makefile ═══════════════════════
echo "== провязка (scripts/hooks/install.sh, Makefile)"
A="$work/a"
mkdir -p "$A/scripts/hooks"
cp "$INSTALL" "$A/scripts/hooks/install.sh"
[ ! -f "$MAKEFILE" ] || cp "$MAKEFILE" "$A/Makefile"
printf '#!/usr/bin/env bash\necho "адресат исполнен: pre-push"\n' > "$A/scripts/hooks/pre-push"
printf '#!/usr/bin/env bash\nexit 0\n' > "$A/scripts/hooks/pre-rebase"
echo 'в имени точка — не хук' > "$A/scripts/hooks/notes.md"
chmod +x "$A/scripts/hooks/pre-push" "$A/scripts/hooks/pre-rebase"
git -C "$A" init -q && git -C "$A" add -A && git -C "$A" commit -qm fixture || void "фикстура провязки не собрана"
printf '#!/usr/bin/env bash\nexit 0\n' > "$A/scripts/hooks/untracked"
chmod +x "$A/scripts/hooks/untracked"
git init -q --bare "$work/a.git" && git -C "$A" remote add origin "$work/a.git" || void "удалённый фикстуры не заведён"
inst() { (cd "$A" && bash scripts/hooks/install.sh "$@"); }
mk()   { make -C "$A" --no-print-directory "$@"; }
AH="$A/.git/hooks"

fact "в дереве есть корневой Makefile с целями провязки" test -f "$MAKEFILE"
runc mk check-hooks
expect "make check-hooks: непровязанный клон — отказ, число хуков выведено обходом индекса" nz \
    "не провязаны: pre-push pre-rebase" "отслеживаемых 2 "
runc mk install-hooks
expect "make install-hooks провязывает и сам выносит вердикт check" 0 \
    "переходник провязан 2" "адресаты: исполнимых 2 из 2"
# Цель не провязала — дальше судится провязка, а не Makefile: без этого одна
# пропавшая цель роняла бы все утверждения ниже, и красное пришло бы от соседа.
[ -x "$AH/pre-push" ] || inst install >/dev/null 2>&1
runc mk check-hooks
expect "make check-hooks: провязанный клон — 0" 0 "переходник провязан 2"
runc mk -n probe-hooks
expect "make probe-hooks зовёт эту пробу" 0 "bash scripts/hooks/inject.sh"
runc mk
expect "make без цели перечисляет цели провязки" 0 "install-hooks" "check-hooks" "probe-hooks"

# Каждая цель, которую тексты хука, провязки и переходника называют командой
# `make <цель>`, в Makefile есть: отказ, советующий несуществующую цель, чинить
# нечем. Перечень выводится из текстов, а не выписывается; пустой — находка.
named="$( { cat "$INSTALL" "$HOOK"; bash "$INSTALL" stub pre-push; } |
    grep -oE '(^|[^[:alnum:]_-])make [a-z][a-z0-9-]*' | sed -E 's/^.*make //' | sort -u)"
nnamed=0
for t in $named; do
    nnamed=$((nnamed + 1))
    if mk -n "$t" >/dev/null 2>&1; then ok "названная цель make $t в Makefile есть"
    else bad "тексты называют «make $t», а в Makefile такой цели нет"; fi
done
[ "$nnamed" -gt 0 ] && ok "тексты хука и провязки называют целей make: $nnamed" ||
    bad "тексты хука и провязки не называют ни одной цели make — отказ советует команду, которой нет в Makefile"
runc mk -n corelib-probe-no-such-target
expect "близнец сверки имён: несуществующая цель make — отказ" nz

fact "переходник побайтно равен тексту производителя (install.sh stub)" \
    cmp -s "$AH/pre-push" <(bash "$INSTALL" stub pre-push)

runc git -C "$A" push -q origin HEAD:refs/heads/twin
expect "близнец: адресат на месте — отправка идёт, адресат исполнен" 0 "адресат исполнен: pre-push"
fact "близнец: ссылка доехала до удалённого" has_ref "$A" twin

mv "$A/scripts/hooks/pre-push" "$work/pp.aside"
runc git -C "$A" push -q origin HEAD:refs/heads/absent
expect "дефект: адресат снят — отправка остановлена с причиной" nz \
    "ОТКАЗ — проверок НЕ БЫЛО" "адресат: $A/scripts/hooks/pre-push"
fact "дефект: ссылка до удалённого НЕ доехала" no_ref "$A" absent
runc inst check
expect "check различает «переходник провязан» и «адресат исполним»" nz \
    "переходник провязан 2" "исполнимых 1 из 2" "адресат не исполним: pre-push"

mv "$work/pp.aside" "$A/scripts/hooks/pre-push"
chmod -x "$A/scripts/hooks/pre-push"
runc git -C "$A" push -q origin HEAD:refs/heads/noexec
expect "дефект: адресат не исполняемый — отказ" nz "ОТКАЗ — проверок НЕ БЫЛО"
fact "дефект: неисполняемый адресат — ссылка НЕ доехала" no_ref "$A" noexec
chmod +x "$A/scripts/hooks/pre-push"

cp "$work/stub-v1" "$AH/pre-push"
mv "$A/scripts/hooks/pre-push" "$work/pp.aside"
runc git -C "$A" push -q origin HEAD:refs/heads/old-v1
expect "контроль: v1 на снятом адресате выходит НУЛЁМ — дефект воспроизводим" 0 "проверок НЕ БЫЛО"
fact "контроль: v1 пропустил ссылку непроверенной" has_ref "$A" old-v1
mv "$work/pp.aside" "$A/scripts/hooks/pre-push"
runc inst check
expect "переходник v1 — «прежней редакции», а не «провязано»" nz \
    "прежней редакции: pre-push(v1)" "переходник провязан 1"
runc inst install
expect "install перезаписывает v1" 0 "переходник провязан 2" "прежней редакции 0"
fact "после install в клоне маркер v2" grep -qx '# corelib-hook-stub v2' "$AH/pre-push"

printf '#!/bin/sh\necho чужой\n' > "$AH/pre-rebase"
cp "$AH/pre-rebase" "$work/foreign.copy"
runc inst check
expect "чужой файл под именем хука — отказ check" nz "ЧУЖОЙ файл: pre-rebase"
runc inst install
expect "чужой файл под именем хука — install отказывает" nz "НЕ перезаписывается"
fact "чужой файл не тронут" cmp -s "$AH/pre-rebase" "$work/foreign.copy"
rm -f "$AH/pre-rebase"
printf '#!/bin/sh\nexit 0\n' > "$AH/post-merge"
cp "$AH/post-merge" "$work/other.copy"
runc inst install
expect "близнец: посторонний хук под другим именем оставлен и назван" 0 \
    "посторонних хуков оставлено нетронутыми: post-merge"
fact "посторонний хук не тронут" cmp -s "$AH/post-merge" "$work/other.copy"

git -C "$A" config core.hooksPath scripts/hooks
runc inst check
expect "core.hooksPath на scripts/hooks — отказ (без файла там git молчит)" nz "выставлен core.hooksPath"
runc inst install
expect "core.hooksPath — install отказывает" nz "выставлен core.hooksPath"
git -C "$A" config --unset core.hooksPath
runc inst check
expect "близнец: core.hooksPath снят — check 0" 0 "переходник провязан 2"

E="$work/empty"
mkdir -p "$E/scripts/hooks"
cp "$INSTALL" "$E/scripts/hooks/install.sh"
echo 'не хук' > "$E/scripts/hooks/readme.txt"
git -C "$E" init -q && git -C "$E" add -A && git -C "$E" commit -qm fixture || void "пустая фикстура не собрана"
runc bash -c "cd '$E' && bash scripts/hooks/install.sh install"
expect "пустой обход хуков — отказ, а не «нечего делать»" nz "НИ ОДНОГО отслеживаемого хука"

# ════ ХУК ОТПРАВКИ: scripts/hooks/pre-push ════════════════════════════════════
echo "== хук отправки (scripts/hooks/pre-push), пин линтера $pin"
B="$work/b"
mkdir -p "$B/scripts/hooks" "$B/.github/scripts"
cp "$HOOK" "$B/scripts/hooks/pre-push"
cp "$INSTALL" "$B/scripts/hooks/install.sh"
cp "$VERDICT" "$B/.github/scripts/go-test-verdict.py"
chmod +x "$B/scripts/hooks/pre-push"
printf 'module example.invalid/probe\n\ngo 1.21\n' > "$B/go.mod"
# Библиотека, как сам фундамент: `go build ./...` исполнимого файла в копию не пишет.
printf 'package probe\n\nfunc Probe() {}\n' > "$B/probe.go"
# Две пробы: исполняемая и длинная. Длинная ПАДАЕТ, если её исполнили: зелёный
# близнец тем самым доказывает, что хук гонит `-short`, а не только «гонит».
cat > "$B/probe_test.go" <<'GO'
package probe

import "testing"

func TestProbe(t *testing.T) { Probe() }

func TestLongIsSkippedUnderShort(t *testing.T) {
	if testing.Short() {
		t.Skip("длинная проба: хук гонит -short")
	}
	t.Fatal("хук гонит пробы БЕЗ -short: длинная проба исполнилась")
}
GO
printf 'version: "2"\n' > "$B/.github/golangci.yml"
git -C "$B" init -q && git -C "$B" add -A && git -C "$B" commit -qm fixture || void "фикстура хука не собрана"
git init -q --bare "$work/b.git" && git -C "$B" remote add origin "$work/b.git" || void "удалённый фикстуры хука не заведён"

# Линтер — подставной: исход задаётся пробой, версия по умолчанию — пин хука.
shim="$work/shim"
mkdir -p "$shim"
cat > "$shim/golangci-lint" <<'LINT'
#!/usr/bin/env bash
case "${1:-}" in
version) echo "golangci-lint has version ${PROBE_LINT_VERSION} built with go" ;;
run)     echo "подставной линтер: код ${PROBE_LINT_RC:-0}"; exit "${PROBE_LINT_RC:-0}" ;;
esac
LINT
chmod +x "$shim/golangci-lint"
export PROBE_LINT_VERSION="$pin"

# pathdir <каталог> <инструмент…> — PATH ровно из названных инструментов.
pathdir() {
    local d="$1" t p
    shift
    mkdir -p "$d"
    for t in "$@"; do
        p="$(command -v "$t")" || void "нет $t для пути $d"
        ln -s "$p" "$d/$t"
    done
}
# bare — PATH без go, gofmt, python3 и линтера: только то, что нужно самому хуку
# и провязке, которую он спрашивает.
basic=(bash git grep head wc sort mkdir cat sed)
bare="$work/bare"
pathdir "$bare" "${basic[@]}"
# nopy — всё, кроме интерпретатора прогонщика вердикта.
nopy="$work/nopy"
pathdir "$nopy" "${basic[@]}" go gofmt
ln -s "$shim/golangci-lint" "$nopy/golangci-lint"

hk() { (cd "$B" && PATH="$shim:$PATH" bash scripts/hooks/pre-push origin "$work/b.git"); }
# hk_as <скрипт> — хук ИЗМЕНЁННОЙ редакции, исполненный на том же чистом дереве:
# копия лежит вне дерева, поэтому «грязной» копию не делает.
hk_as() { local script="$1"; (cd "$B" && PATH="$shim:$PATH" bash "$script" origin "$work/b.git"); }
line() { printf 'refs/heads/%s %s refs/heads/%s %s' "$1" "$2" "$1" "$zero"; }
h0="$(git -C "$B" rev-parse HEAD)"

# Провязка клона — предпосылка вердикта о следующей отправке: клон без
# переходника либо с переходником v1 выпустит дерево без хука молча.
runin "$(line 21 "$h0")" hk
expect "клон не провязан — отказ до проверок" 1 "ОТКАЗ — провязка клона" "не провязаны: pre-push"
(cd "$B" && bash scripts/hooks/install.sh install) >/dev/null 2>&1 || bad "install в фикстуре хука"

runin "$(line 21 "$h0")" hk
expect "близнец: провязанный клон, чистая копия на отправляемой ревизии — 0" 0 \
    "судится ревизия $h0" "исполнено 5 из 5, красных 0" "проб исполнено    : 1" "ПРОПУЩЕНО         : 1"
runin "$(line 21 "$h0")" with PROBE_LINT_RC=1 hk
expect "линтер красный — отказ с именем проверки" 1 "красные — golangci-lint"
runin "$(line 21 "$h0")" with PROBE_LINT_RC=3 hk
expect "код 3 линтера — красное, а не «без условия»" 1 "красные — golangci-lint"
runin "$(line 21 "$h0")" with PROBE_LINT_VERSION=0.0.1 hk
expect "версия линтера не равна пину — без условия, названо, в «исполнено» не входит" 0 \
    "без условия 1" "исполнено 4 из 5"
runin "$(line 21 "$h0")" env PATH="$bare" "$bare/bash" -c "cd '$B' && bash scripts/hooks/pre-push"
expect "не исполнено ни одной проверки — отказ, а не зелёное" 2 "исполнено НОЛЬ проверок из 5"
runin "$(line 21 "$h0")" env PATH="$nopy" "$nopy/bash" -c "cd '$B' && bash scripts/hooks/pre-push"
expect "нет python3 — go test без условия, назван, в «исполнено» не входит" 0 \
    "без условия 1" "python3 не найден" "исполнено 4 из 5"
runin "$(line 21 "$h0")" with CORELIB_SKIP_PREPUSH=1 hk
expect "обход объявлен и печатает непроверенное" 0 "НЕ выполнялись" "здесь НЕ гонялось"
runin "" hk
expect "пустой вход: судится рабочая копия, и это сказано" 0 "входа отправки нет"
runin "(delete) $zero refs/heads/old $h0" hk
expect "одни снятия ссылок — проверять нечего, и это сказано" 0 "все — снятия" "исполнено проверок: 0"

# go test -short: дефект хука — пробы без -short. Инъекция меняет РОВНО одно
# слово в копии хука; не изменила ничего — сама инъекция не состоялась.
sed 's|go test ./... -short |go test ./... |' "$HOOK" > "$work/pre-push.noshort"
fact "инъекция «без -short» изменила копию хука" not_same "$HOOK" "$work/pre-push.noshort"
runin "$(line 21 "$h0")" hk_as "$work/pre-push.noshort"
expect "дефект: хук гонит пробы без -short — длинная проба исполнилась, отказ с её именем" 1 \
    "красные — go test -short" "TestLongIsSkippedUnderShort"

printf 'package probe\n\nimport "testing"\n\nfunc TestRed(t *testing.T) { t.Fatal("красная проба фикстуры") }\n' > "$B/red_test.go"
git -C "$B" add red_test.go && git -C "$B" commit -qm red-test
runin "$(line 21 "$(git -C "$B" rev-parse HEAD)")" hk
expect "проба красная — отказ с именем пробы" 1 "красные — go test -short" "TestRed"
git -C "$B" reset -q --hard "$h0"

git -C "$B" rm -q .github/scripts/go-test-verdict.py && git -C "$B" commit -qm no-verdict
runin "$(line 21 "$(git -C "$B" rev-parse HEAD)")" hk
expect "прогонщика вердикта нет в дереве — красное, а не «без условия»" 1 \
    "красные — go test -short" "прогонщика вердикта нет"
git -C "$B" reset -q --hard "$h0"

printf 'package probe\n\nfunc Probe() {\n\tvar x int = "s"\n\t_ = x\n}\n' > "$B/probe.go"
git -C "$B" commit -qam broken
h1="$(git -C "$B" rev-parse HEAD)"
runin "$(line 21 "$h1")" hk
expect "сборка красная — отказ" 1 "красные — сборка vet"
git -C "$B" reset -q --hard "$h0"

printf 'package probe\nfunc Probe(){}\n' > "$B/probe.go"
git -C "$B" commit -qam unformatted
runin "$(line 21 "$(git -C "$B" rev-parse HEAD)")" hk
expect "gofmt красный — отказ с перечнем" 1 "красные — gofmt" "требуется gofmt"
git -C "$B" reset -q --hard "$h0"

printf 'package probe\n\nconst second = 1\n' > "$B/doc.go"
git -C "$B" add doc.go && git -C "$B" commit -qm second
h2="$(git -C "$B" rev-parse HEAD)"
runin "$(line 21 "$h0")" hk
expect "уезжает не HEAD — отказ: вердикт был бы о другом дереве" 1 "проверки судили бы другое дерево"
runin "$(line 21 "$h2")"$'\n'"$(line 22 "$h0")" hk
expect "две разные вершины в одной отправке — отказ" 1 "2 разных вершин"
runin "$(line 21 "$h2")"$'\n'"$(line 22 "$h2")" hk
expect "близнец: две ссылки на одну вершину — 0" 0 "исполнено 5 из 5"
echo x > "$B/stray.txt"
runin "$(line 21 "$h2")" hk
expect "неотслеживаемый файл в копии — отказ" 1 "неотслеживаемых файлов: 1"
rm -f "$B/stray.txt"
echo '// правка' >> "$B/doc.go"
runin "$(line 21 "$h2")" hk
expect "неснятая правка отслеживаемого — отказ" 1 "неотслеживаемых файлов: 1"
git -C "$B" checkout -q -- doc.go

git -C "$B" checkout -q -b wip/probe
printf 'package probe\n\nfunc Probe() {\n\tvar x int = "s"\n\t_ = x\n}\n' > "$B/probe.go"
git -C "$B" commit -qam draft
runin "$(line wip/probe "$(git -C "$B" rev-parse HEAD)")" hk
expect "имя ветки wip/* проверок не снимает" 1 "красные — сборка"
git -C "$B" checkout -q main

# Сквозь git push: переходник → хук → код отправки.
runc env PATH="$shim:$PATH" git -C "$B" push -q origin HEAD:refs/heads/21
expect "сквозь git push: зелёное — отправка идёт" 0 "исполнено 5 из 5"
fact "сквозь git push: зелёная ссылка доехала" has_ref "$B" 21
git -C "$B" checkout -q wip/probe
runc env PATH="$shim:$PATH" git -C "$B" push -q origin HEAD:refs/heads/22
expect "сквозь git push: красное — отправка остановлена" nz "красные — сборка"
fact "сквозь git push: красная ссылка НЕ доехала" no_ref "$B" 22
git -C "$B" checkout -q main

# Переходник v1 в клоне зовёт хук, раз адресат есть, — и хук отказывает: иначе v1
# пережил бы эту отправку и следующую, дерева без хука, выпустил бы молча.
cp "$work/stub-v1" "$B/.git/hooks/pre-push"
runc env PATH="$shim:$PATH" git -C "$B" push -q origin HEAD:refs/heads/stale
expect "сквозь git push: клон с переходником v1 — отказ с причиной и командой" nz \
    "ОТКАЗ — провязка клона" "прежней редакции: pre-push(v1)" "make install-hooks"
fact "сквозь git push: через переходник v1 ссылка НЕ доехала" no_ref "$B" stale
(cd "$B" && bash scripts/hooks/install.sh install) >/dev/null 2>&1 || bad "повторный install в фикстуре хука"
runc env PATH="$shim:$PATH" git -C "$B" push -q origin HEAD:refs/heads/fresh
expect "близнец: переходник перепровязан — отправка идёт" 0 "исполнено 5 из 5"
fact "близнец: после перепровязки ссылка доехала" has_ref "$B" fresh

echo ""
total=$((pass + fail))
echo "== inject: утверждений $total · сошлось $pass · разошлось $fail"
[ "$total" -gt 0 ] || void "не проверено ни одного утверждения"
[ "$fail" -eq 0 ] || exit 1
exit 0
