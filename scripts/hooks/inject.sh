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
# ОХВАТ СУДИТСЯ, А НЕ ТОЛЬКО КОД. В фикстуре хука три пакета, два вложенных.
# У сборки и vet дефект кладётся по очереди в КАЖДЫЙ пакет фикстуры, у gofmt —
# в КАЖДЫЙ файл Go индекса: проверка, суженная до любой строгой части обхода
# (корень, поддерево, дерево без корня, все пакеты, кроме одного), пропустит
# хотя бы один дефект, и утверждение о его пакете или файле разойдётся. Одного
# дефекта здесь мало: он держит лишь сужения, выбрасывающие его пакет, а чисел
# сборка и vet не печатают, число же файлов gofmt хук считает по своему
# перечню, а не по прогону gofmt. У go test и линтера охват сверяется у близнеца
# числами их СОБСТВЕННОГО прогона (пакеты и пробы — из потока JSON прогонщика,
# пакеты линтера — из журнала подставного), и дефект у каждого один, во
# вложенном пакете. -count=1 судится журналом исполнений: проба фикстуры пишет
# строку в файл мимо учёта входов go test, и прогон, взятый из кеша, строки не
# добавляет. Подставной линтер исполняет ту часть контракта настоящего, от
# которой зависит вердикт, — какой конфиг прочитан, где кэш, какие пакеты
# осмотрены — и пишет её в журнал; проба сверяет журнал с деревом фикстуры и с
# ci.yml. «Тот же, что в ci.yml» — пин линтера и конфиг — берётся из
# исполняемых строк `run:` ci.yml, а не выписывается здесь второй раз.
#
# ИСХОДЫ: 0 — все утверждения сошлись; 1 — хоть одно разошлось;
#         2 — не выполнилось (нет git/go/gofmt/make/python3, нет временного
#             каталога, пин не прочитан, шаг линтера в ci.yml не прочитан
#             однозначно, нет прогонщика вердикта, текст v1 не тот, фикстура
#             не собрана или имя её пакета не прочитано, не проверено ни одного
#             утверждения).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tree="$(cd "$here/../.." && pwd -P)"
INSTALL="$here/install.sh"
HOOK="$here/pre-push"
MAKEFILE="$tree/Makefile"
VERDICT="$tree/.github/scripts/go-test-verdict.py"
CI="$tree/.github/workflows/ci.yml"

void() { echo "inject: НЕ ВЫПОЛНИЛОСЬ — $*" >&2; exit 2; }
for f in "$INSTALL" "$HOOK" "$VERDICT" "$CI"; do [ -f "$f" ] || void "нет $f"; done
for t in git go gofmt make python3; do command -v "$t" >/dev/null 2>&1 || void "нет $t в PATH"; done
pin="$(sed -n 's/^GOLANGCI_LINT_VERSION="v\([0-9.]*\)"$/\1/p' "$HOOK")"
[ -n "$pin" ] || void "пин линтера в $HOOK не прочитан — сверять версию не с чем"

# Что линтер конвейера ставит и чем зовётся — из исполняемых однострочных `run:`
# ci.yml: строка комментария YAML начинается с `#` и сюда не попадает. Прочитано
# не ровно одно значение — сверять не с чем, и это не зелёное.
ci_runs="$(sed -nE 's/^[[:space:]]*(-[[:space:]]+)?run:[[:space:]]+//p' "$CI")"
ci_pin="$(printf '%s\n' "$ci_runs" |
    grep -oE 'golangci-lint/v2/cmd/golangci-lint@v[0-9]+\.[0-9]+\.[0-9]+' | sed 's/.*@v//' | sort -u)"
[ "$(printf '%s' "$ci_pin" | grep -c .)" -eq 1 ] ||
    void "пин установки golangci-lint в $CI прочитан не однозначно: «${ci_pin//$'\n'/ }»"
ci_lint="$(printf '%s\n' "$ci_runs" | grep -E '^golangci-lint run( |$)')"
[ "$(printf '%s' "$ci_lint" | grep -c .)" -eq 1 ] ||
    void "шаг «golangci-lint run» в $CI найден не ровно один раз — конфиг конвейера не прочитан"
ci_cfg="$(printf '%s\n' "$ci_lint" | sed -nE 's/.*(--config[= ]|-c )([^ ]+).*/\2/p')"
ci_cfg="${ci_cfg#./}"
[ -n "$ci_cfg" ] || void "шаг линтера в $CI не называет конфиг флагом --config — сверять не с чем"

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
not() { ! "$@"; }
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
# Засчитывается цель, чей рецепт зовёт scripts/hooks: цель, объявленная одним
# .PHONY без рецепта, у make «исполняется» кодом 0, не делая ничего, — и код
# `make -n` её от настоящей не отличает.
delegates() {
    local o
    o="$(make -C "$1" --no-print-directory -n "$2" 2>&1)" &&
        printf '%s\n' "$o" | grep -q 'bash scripts/hooks/'
}
nnamed=0
for t in $named; do
    nnamed=$((nnamed + 1))
    if delegates "$A" "$t"; then ok "названная цель make $t в Makefile есть и зовёт scripts/hooks"
    else bad "тексты называют «make $t», а в Makefile нет цели с таким рецептом"; fi
done
if [ "$nnamed" -gt 0 ]; then ok "тексты хука и провязки называют целей make: $nnamed"
else bad "тексты хука и провязки не называют ни одной цели make — отказ советует команду, которой нет в Makefile"; fi
G="$work/ghost"
mkdir -p "$G"
printf '.PHONY: ghost\n' > "$G/Makefile"
fact "близнец сверки имён: цель лишь в .PHONY, без рецепта — не засчитана" not delegates "$G" ghost
fact "близнец сверки имён: несуществующая цель — не засчитана" not delegates "$A" corelib-probe-no-such-target

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
probe_test_long='
func TestLongIsSkippedUnderShort(t *testing.T) {
	if testing.Short() {
		t.Skip("длинная проба: хук гонит -short")
	}
	t.Fatal("хук гонит пробы БЕЗ -short: длинная проба исполнилась")
}'
probe_test_one='
func TestProbe(t *testing.T) { Probe() }'
printf 'package probe\n\nimport "testing"\n%s\n%s\n' "$probe_test_one" "$probe_test_long" > "$B/probe_test.go"
# Вложенные пакеты — предмет охвата: `./...` и `.` различимы только там, где
# пакетов больше одного. inner несёт исполняемую пробу, inner/deep — ни одной:
# пакет без проб законен, пока пробы исполнены где-то ещё.
mkdir -p "$B/inner/deep"
printf 'package inner\n\n// Inner — вложенный пакет фикстуры.\nfunc Inner() {}\n' > "$B/inner/inner.go"
# TestInner ведёт журнал исполнений (PROBE_RUN_LOG): пишет через syscall, а не
# через os, — учёт входов go test (testlog) такой записи не видит, и прогон,
# взятый из кеша, строки не добавит. Без переменной журнал не ведётся.
cat > "$B/inner/inner_test.go" <<'GO'
package inner

import (
	"syscall"
	"testing"
)

func TestInner(t *testing.T) {
	Inner()
	p, ok := syscall.Getenv("PROBE_RUN_LOG")
	if !ok {
		return
	}
	fd, err := syscall.Open(p, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT, 0o644)
	if err != nil {
		t.Fatalf("журнал исполнений: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if _, err := syscall.Write(fd, []byte("TestInner\n")); err != nil {
		t.Fatalf("журнал исполнений: %v", err)
	}
}
GO
deep_go='package deep

// Deep — пакет без проб, второй уровень вложенности.
const Deep = 1
'
printf '%s' "$deep_go" > "$B/inner/deep/deep.go"
# Конфиг линтера — там, где его называет ci.yml: проба судит, что хук читает ТОТ.
mkdir -p "$B/$(dirname "$ci_cfg")"
printf 'version: "2"\n' > "$B/$ci_cfg"
git -C "$B" init -q && git -C "$B" add -A && git -C "$B" commit -qm fixture || void "фикстура хука не собрана"
git init -q --bare "$work/b.git" && git -C "$B" remote add origin "$work/b.git" || void "удалённый фикстуры хука не заведён"
bgit="$(git -C "$B" rev-parse --absolute-git-dir)"
# Что обязан осмотреть охват `./...`: файлы Go индекса и их каталоги (пакеты).
want_files="$(git -C "$B" ls-files '*.go' | wc -l)"
want_pkgs="$(git -C "$B" ls-files '*.go' | sed -E '/\//!s|.*|.|; s|/[^/]*$||' | sort -u)"
[ "$want_files" -gt 0 ] && [ "$(printf '%s\n' "$want_pkgs" | grep -c .)" -gt 1 ] ||
    void "фикстура хука без вложенных пакетов — охват судить не на чем"

# Линтер — подставной. Исход задаёт проба, но то, от чего зависит вердикт
# настоящего, он исполняет так же, как настоящий, и пишет в журнал:
#   config= — какой конфиг прочитан: --config/-c (нет файла — код 3); без флага —
#             поиск .golangci.{yml,yaml,toml,json} от каталога запуска вверх,
#             затем в $HOME; не найден — «умолчание», то есть не конфиг конвейера;
#   cache=  — где кэш: GOLANGCI_LINT_CACHE, без неё — ${XDG_CACHE_HOME:-$HOME/.cache};
#   pkg=    — каждый осмотренный пакет по шаблонам, как у go (без шаблона —
#             ./...; каталоги на «.» и «_», testdata, vendor не обходятся);
#             каталога шаблона нет — код 3; не осмотрено ни одного — код 5.
# Находка — строка `// probe:lint-finding` в файле осмотренного пакета: код 1.
# Только встроенные bash: подставной работает и в урезанном PATH пробы.
shim="$work/shim"
mkdir -p "$shim"
cat > "$shim/golangci-lint" <<'LINT'
#!/usr/bin/env bash
set -u
log="${PROBE_LINT_LOG:-/dev/null}"
case "${1:-}" in
version) echo "golangci-lint has version ${PROBE_LINT_VERSION:-} built with go"; exit 0 ;;
run) shift ;;
*) echo "подставной линтер: команда «${1:-}» не поддержана" >&2; exit 3 ;;
esac
abspath() {
    local p="$1"
    case "$p" in /*) ;; *) p="$PWD/$p" ;; esac
    (cd "${p%/*}" 2>/dev/null && printf '%s/%s\n' "$(pwd -P)" "${p##*/}")
}
config="" mode=auto pats=()
while [ "$#" -gt 0 ]; do
    case "$1" in
    --config=*) config="${1#--config=}"; mode=explicit ;;
    --config|-c) config="${2:-}"; mode=explicit; shift ;;
    --no-config) mode=none ;;
    --timeout|--concurrency|-j|--build-tags|--path-prefix) shift ;;
    -*) ;;
    *) pats+=("$1") ;;
    esac
    shift
done
used=""
case "$mode" in
explicit)
    if [ ! -f "$config" ]; then
        printf 'config=ОТКАЗ %s\n' "$config" >> "$log"
        echo "can't read config file $config: no such file" >&2
        exit 3
    fi
    used="$(abspath "$config")" ;;
auto)
    d="$PWD"
    while [ -z "$used" ]; do
        for n in .golangci.yml .golangci.yaml .golangci.toml .golangci.json; do
            [ -f "$d/$n" ] && { used="$(abspath "$d/$n")"; break; }
        done
        [ -n "$d" ] && [ "$d" != / ] || break
        d="${d%/*}"
    done
    if [ -z "$used" ] && [ -n "${HOME:-}" ]; then
        for n in .golangci.yml .golangci.yaml .golangci.toml .golangci.json; do
            [ -f "$HOME/$n" ] && { used="$(abspath "$HOME/$n")"; break; }
        done
    fi ;;
esac
printf 'config=%s\n' "${used:-умолчание}" >> "$log"
cache="${GOLANGCI_LINT_CACHE:-}"
[ -n "$cache" ] || cache="${XDG_CACHE_HOME:-${HOME:-}/.cache}/golangci-lint"
printf 'cache=%s\n' "$cache" >> "$log"
shopt -s globstar nullglob
[ "${#pats[@]}" -gt 0 ] || pats=(./...)
declare -A seen=()
pkgs=()
add() {
    local d="${1%/}" f
    d="${d#./}"; [ -n "$d" ] || d=.
    [ -z "${seen[$d]:-}" ] || return 0
    for f in "$d"/*.go; do seen[$d]=1; pkgs+=("$d"); printf 'pkg=%s\n' "$d" >> "$log"; return 0; done
}
for p in "${pats[@]}"; do
    case "$p" in
    ...|*/...) base="${p%...}"; base="${base%/}"; [ -n "$base" ] || base=.; rec=1 ;;
    *) base="$p"; rec=0 ;;
    esac
    if [ ! -d "$base" ]; then
        printf 'pattern=ОТКАЗ %s\n' "$p" >> "$log"
        echo "pattern $p: directory not found" >&2
        exit 3
    fi
    add "$base"
    [ "$rec" = 1 ] || continue
    for d in "$base"/**/; do
        case "/${d#./}" in */_*|*/testdata/*|*/vendor/*) continue ;; esac
        add "$d"
    done
done
if [ "${#pkgs[@]}" -eq 0 ]; then
    echo "no go files to analyze" >&2
    exit 5
fi
rc=0
for d in "${pkgs[@]}"; do
    for f in "$d"/*.go; do
        n=0
        while IFS= read -r l || [ -n "$l" ]; do
            n=$((n + 1))
            [ "$l" != "// probe:lint-finding" ] || { echo "$f:$n:1: находка подставного линтера (probe)"; rc=1; }
        done < "$f"
    done
done
rc="${PROBE_LINT_RC:-$rc}"
echo "подставной линтер: пакетов ${#pkgs[@]}, код $rc"
exit "$rc"
LINT
chmod +x "$shim/golangci-lint"
export PROBE_LINT_VERSION="$pin" PROBE_LINT_LOG="$work/lint.log"
: > "$PROBE_LINT_LOG"
# lint_field <ключ> — значения ключа из журнала подставного линтера.
lint_field() { sed -n "s/^$1=//p" "$PROBE_LINT_LOG"; }

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
# nogrep — bare без одного инструмента самого хука: близнец bare ровно в один факт.
nogrep="$work/nogrep"
no_grep=()
for t in "${basic[@]}"; do [ "$t" = grep ] || no_grep+=("$t"); done
pathdir "$nogrep" "${no_grep[@]}"
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

: > "$PROBE_LINT_LOG"
runin "$(line 21 "$h0")" hk
expect "близнец: провязанный клон, чистая копия на отправляемой ревизии — 0" 0 \
    "судится ревизия $h0" "исполнено 5 из 5, красных 0"
# Охват близнеца — числами дерева фикстуры, а не «зелёным»: проверка, суженная
# до корня или до части обхода, на этом же дереве тоже зелёная.
expect "охват gofmt: прочитаны все файлы Go индекса ($want_files)" 0 "прочитано файлов Go: $want_files"
expect "охват go test: пакетов 3, исполнены пробы корня и вложенного (2), длинная пропущена" 0 \
    "пакетов осмотрено : 3" "проб исполнено    : 2" "ПРОПУЩЕНО         : 1"
fact "пин линтера хука ($pin) равен пину установки в ci.yml ($ci_pin)" test "$pin" = "$ci_pin"
got="$(lint_field config)"
if [ "$got" = "$B/$ci_cfg" ]; then ok "линтер прочитал конфиг, который называет ci.yml: $ci_cfg"
else bad "линтер прочитал конфиг «${got:-—}», а ci.yml называет $ci_cfg — вердикт был бы о других правилах"; fi
got="$(lint_field cache)"
case "$got" in
"$bgit"/*) ok "кэш линтера — в каталоге git этой копии: ${got#"$bgit"/}" ;;
*) bad "кэш линтера «${got:-—}» вне каталога git копии ($bgit) — общий на машину отдал бы чужой вердикт" ;;
esac
got="$(lint_field pkg | sort -u)"
if [ "$got" = "$want_pkgs" ]; then ok "охват линтера: осмотрены все пакеты дерева ($(printf '%s\n' "$got" | grep -c .))"
else bad "охват линтера: осмотрено «${got//$'\n'/ }», в дереве пакеты «${want_pkgs//$'\n'/ }»"; fi

# defect <имя> <файл> <содержимое> <образец…> — коммит с дефектом, отправка, откат.
defect() {
    local name="$1" file="$2" body="$3"
    shift 3
    printf '%s' "$body" > "$B/$file"
    git -C "$B" add -- "$file" && git -C "$B" commit -qm "defect: $name" || { bad "фикстура дефекта «$name» не собрана"; return; }
    runin "$(line 21 "$(git -C "$B" rev-parse HEAD)")" hk
    expect "$name" 1 "$@"
    git -C "$B" reset -q --hard "$h0"
}
# Охват сборки и vet — дефектом в КАЖДОМ пакете фикстуры по очереди, охват
# gofmt — в КАЖДОМ файле Go индекса: сужение до любой строгой части обхода
# пропускает хотя бы один, и утверждение о нём разойдётся. Перечни — те же
# want_pkgs и ls-files, по которым судится близнец; имя пакета спрашивается у
# go list. Дефект сборки и vet — новый файл пакета; gofmt — строка с лишним
# пробелом в конце файла: сборка и vet на ней зелёные, красен один gofmt.
npk=0
for d in $want_pkgs; do
    pname="$(cd "$B" && go list -f '{{.Name}}' "./$d" 2>/dev/null)"
    [ -n "$pname" ] || void "имя пакета фикстуры «$d» не прочитано (go list) — дефект положить некуда"
    if [ "$d" = . ]; then at=""; else at="$d/"; fi
    defect "охват сборки: ошибка типа в пакете $d — отказ с именем файла" "${at}zz_probe_build.go" \
        "package $pname"$'\n\nvar _ int = "s"\n' \
        "КРАСНОЕ: сборка" "${at}zz_probe_build.go"
    defect "охват vet: битый тег структуры в пакете $d — отказ, и красен один vet" "${at}zz_probe_vet.go" \
        "package $pname"$'\n\n// probeVetT — тег без закрывающей кавычки: сборка проходит, vet — нет.\ntype probeVetT struct {\n\tA int `json:"a`\n}\n' \
        "КРАСНОЕ: vet" "красные — vet. " "${at}zz_probe_vet.go"
    npk=$((npk + 1))
done
nfile=0
for f in $(git -C "$B" ls-files '*.go'); do
    defect "охват gofmt: неформатированная строка в $f — отказ с его именем, и красен один gofmt" "$f" \
        "$(cat "$B/$f")"$'\n\nvar  _ = 0\n' \
        "КРАСНОЕ: gofmt" "требуется gofmt" "красные — gofmt. " "$f"
    nfile=$((nfile + 1))
done
echo "   дефекты охвата разложены: сборка и vet — по $npk пакетам, gofmt — по $nfile файлам Go индекса"
defect "go test: красная проба во вложенном пакете — отказ с её именем" inner/deep/red_test.go \
    $'package deep\n\nimport "testing"\n\nfunc TestRed(t *testing.T) { t.Fatal("красная проба фикстуры") }\n' \
    "КРАСНОЕ: go test -short" "TestRed"
defect "линтер: находка во вложенном пакете — отказ с именем проверки" inner/deep/deep.go \
    "$deep_go"$'\n// probe:lint-finding\n' \
    "КРАСНОЕ: golangci-lint" "inner/deep/deep.go"
runin "$(line 21 "$h0")" with PROBE_LINT_RC=3 hk
expect "код 3 линтера — красное, а не «без условия»" 1 "красные — golangci-lint"
runin "$(line 21 "$h0")" with PROBE_LINT_VERSION=0.0.1 hk
expect "версия линтера не равна пину — без условия, названо, в «исполнено» не входит" 0 \
    "без условия 1" "исполнено 4 из 5"
runin "$(line 21 "$h0")" env PATH="$bare" "$bare/bash" -c "cd '$B' && bash scripts/hooks/pre-push"
expect "не исполнено ни одной проверки — отказ, а не зелёное" 2 "исполнено НОЛЬ проверок из 5"
# Инструмент самого хука (не проверки) снят — отказ называет его, а не выводит
# из пустого вывода ложную причину («правки в копии» при чистой копии).
runin "$(line 21 "$h0")" env PATH="$nogrep" "$nogrep/bash" -c "cd '$B' && bash scripts/hooks/pre-push"
expect "нет grep — отказ до проверок, назван инструмент хука" 2 "нет grep в PATH" "исполнено проверок: 0"
fact "нет grep — ложной причины «правки в копии» нет" not grep -qF "неотслеживаемых файлов" <<<"$out"
runin "$(line 21 "$h0")" env PATH="$nopy" "$nopy/bash" -c "cd '$B' && bash scripts/hooks/pre-push"
expect "нет python3 — go test без условия, назван, в «исполнено» не входит" 0 \
    "без условия 1" "python3 не найден" "исполнено 4 из 5"
runin "$(line 21 "$h0")" with CORELIB_SKIP_PREPUSH=1 hk
expect "обход объявлен и печатает непроверенное" 0 "НЕ выполнялись" "здесь НЕ гонялось"
runin "" hk
expect "пустой вход: судится рабочая копия, и это сказано" 0 "входа отправки нет"
runin "(delete) $zero refs/heads/old $h0" hk
expect "одни снятия ссылок — проверять нечего, и это сказано" 0 "все — снятия" "исполнено проверок: 0"

# go test -short: дефект хука — пробы без -short. Инъекция меняет РОВНО один
# флаг в исполняемой строке `go test` копии хука (строки комментария начинаются
# с `#` и не трогаются); не изменила ничего — сама инъекция не состоялась.
sed -E '/^[[:space:]]*go test /s/ -short( |$)/ /' "$HOOK" > "$work/pre-push.noshort"
fact "инъекция «без -short» изменила копию хука" not_same "$HOOK" "$work/pre-push.noshort"
runin "$(line 21 "$h0")" hk_as "$work/pre-push.noshort"
expect "дефект: хук гонит пробы без -short — длинная проба исполнилась, отказ с её именем" 1 \
    "красные — go test -short" "TestLongIsSkippedUnderShort"

# go test -count=1: без него второй прогон того же дерева go test берёт из
# кеша, и вердикт становится функцией того, КОГДА его считали. Судится журналом
# исполнений TestInner: два прогона хука на одной ревизии — две строки.
# Контроль — копия хука без -count=1 (та же инъекция одного флага, что выше):
# строк меньше двух. Без контроля журнал, дающий две строки при любом хуке,
# держал бы -count=1 только на словах.
# runs_twice <команда…> — два прогона с журналом исполнений; строк — в $nruns, коды — в $rcs.
runs_twice() {
    : > "$work/runs.log"
    rcs=""
    runin "$(line 21 "$h0")" with PROBE_RUN_LOG="$work/runs.log" "$@"; rcs="$rcs $rc"
    runin "$(line 21 "$h0")" with PROBE_RUN_LOG="$work/runs.log" "$@"; rcs="$rcs $rc"
    nruns="$(grep -c . "$work/runs.log")"
}
runs_twice hk
if [ "$rcs" = " 0 0" ] && [ "$nruns" -eq 2 ]; then ok "-count=1: два прогона хука — TestInner исполнена дважды (коды$rcs)"
elif [ "$nruns" -lt 2 ]; then bad "-count=1: два прогона хука (коды$rcs), а TestInner исполнена $nruns раз из 2 — вердикт взят из кеша"
else bad "-count=1: два прогона хука (коды$rcs), TestInner исполнена $nruns раз из 2"; fi
sed -E '/^[[:space:]]*go test /s/ -count=1( |$)/ /' "$HOOK" > "$work/pre-push.nocount"
fact "инъекция «без -count=1» изменила копию хука" not_same "$HOOK" "$work/pre-push.nocount"
runs_twice hk_as "$work/pre-push.nocount"
if [ "$nruns" -lt 2 ]; then ok "контроль: хук без -count=1 — TestInner исполнена $nruns раз из 2, прогон взят из кеша"
else bad "контроль: хук без -count=1, а TestInner исполнена $nruns раз из 2 — журнал не отличает кешированный прогон от свежего"; fi

git -C "$B" rm -q .github/scripts/go-test-verdict.py && git -C "$B" commit -qm no-verdict
runin "$(line 21 "$(git -C "$B" rev-parse HEAD)")" hk
expect "прогонщика вердикта нет в дереве — красное, а не «без условия»" 1 \
    "красные — go test -short" "прогонщика вердикта нет"
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

# Пустой обход проб — не зелёное: «отказов 0» при нуле исполненных проб значит
# «спросить было не у кого». Два дефекта и близнец, каждый сквозь git push;
# «все пропущены» и близнец различаются ровно одной исполняемой пробой.
# push_tree <ссылка> — отправка текущей вершины сквозь переходник.
push_tree() { runc env PATH="$shim:$PATH" git -C "$B" push -q origin "HEAD:refs/heads/$1"; }
git -C "$B" rm -q probe_test.go inner/inner_test.go && git -C "$B" commit -qm no-probes
push_tree no-probes
expect "пустой обход: в дереве ни одной пробы — отказ, а не зелёное" nz \
    "проб исполнено    : 0" "КРАСНОЕ: go test -short"
fact "пустой обход: ссылка НЕ доехала" no_ref "$B" no-probes
printf 'package probe\n\nimport "testing"\n%s\n' "$probe_test_long" > "$B/probe_test.go"
git -C "$B" add probe_test.go && git -C "$B" commit -qm all-skipped
push_tree all-skipped
expect "пустой обход: все пробы пропущены под -short — отказ, а не зелёное" nz \
    "проб исполнено    : 0" "ПРОПУЩЕНО         : 1" "КРАСНОЕ: go test -short"
fact "пустой обход: при всех пропущенных ссылка НЕ доехала" no_ref "$B" all-skipped
printf 'package probe\n\nimport "testing"\n%s\n%s\n' "$probe_test_one" "$probe_test_long" > "$B/probe_test.go"
git -C "$B" commit -qam one-probe
push_tree one-probe
expect "близнец пустого обхода: одна исполненная проба — отправка идёт" 0 \
    "проб исполнено    : 1" "исполнено 5 из 5, красных 0"
fact "близнец пустого обхода: ссылка доехала" has_ref "$B" one-probe
git -C "$B" reset -q --hard "$h0"

echo ""
total=$((pass + fail))
echo "== inject: утверждений $total · сошлось $pass · разошлось $fail"
[ "$total" -gt 0 ] || void "не проверено ни одного утверждения"
[ "$fail" -eq 0 ] || exit 1
exit 0
