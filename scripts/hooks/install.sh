#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# install.sh — провязать хуки этого дерева в клон и сказать, провязаны ли они.
#
#   make install-hooks                       # = bash scripts/hooks/install.sh install
#   make check-hooks                         # = bash scripts/hooks/install.sh check
#   bash scripts/hooks/install.sh stub <имя> # текст переходника (для пробы scripts/hooks/inject.sh)
#
# install кладёт переходники и выносит вердикт check; check — 0, когда
# провязано и исполнимо, 1 — когда нет. Цели корневого Makefile — адрес, под
# которым провязку ищут; логики в них нет. Экземпляр свой, не копия (ban20):
# идея переходника общая с kacho и kaname, байты не перенесены.
#
# ПЕРЕХОДНИК, А НЕ `core.hooksPath`. С `core.hooksPath` git ищет хуки только по
# указанному пути: всё, что лежит в `.git/hooks`, перестаёт исполняться молча, а
# если по тому пути хука нет (рабочая копия старше хука), git не исполняет
# НИЧЕГО и тоже молчит. Поэтому любой `core.hooksPath` — отказ, а в `.git/hooks`
# кладётся переходник, исполняющий отслеживаемый скрипт ТЕКУЩЕЙ рабочей копии;
# посторонний хук под другим именем не трогается, чужой файл под нашим именем
# не перезаписывается (отказ).
#
# НЕТ АДРЕСАТА — ОТКАЗ (corelib#24). Редакция v1 на ненайденном адресате
# печатала «проверок НЕ БЫЛО» и выходила нулём: отправка уезжала непроверенной,
# и отличить это от зелёного было нечем. v2 выходит кодом 1 с причиной. Клон с
# v1 выглядит провязанным и ведёт себя как непровязанный, поэтому редакция
# читается из маркера: v1 — «прежней редакции», check отказывает, install
# перезаписывает. `check` печатает раздельно «переходник провязан» и «адресат
# исполним»: переходник без исполнимого адресата откажет каждой отправке.
#
# ОТКАЗ НАЗЫВАЕТ ВЫХОДЫ, А НЕ ОБХОД (corelib#25, возврат R8). Переходник лежит в
# ОБЩЕМ каталоге клона и видит все его рабочие копии, а в ревизии, открытой до
# хука, адресата нет — это штатный случай, и отказ в нём остаётся (ненайденный
# адресат зелёным не бывает). Законные выходы — внести хук слиянием ревизии,
# где он есть (слияние судит уже внесённый хук), вернуть потерянный файл либо
# снять переходник, если хук снят намеренно. Обход проверок (`--no-verify`)
# отказ не предлагает: он запрещён правилом, и совет его — совет нарушить.
#
# ХУКОМ СЧИТАЕТСЯ отслеживаемый (`git ls-files`) файл в scripts/hooks без точки
# в имени; число осмотренных выводится этим обходом, пустой обход — отказ.
set -uo pipefail

mode="${1:-install}"

die() { printf '%s\n' "$@" >&2; exit 1; }

marker_family="corelib-hook-stub v"
stub_version=2

# stub_for — ЕДИНСТВЕННЫЙ производитель текста переходника: проба берёт его
# режимом `stub`, а не своей копией.
stub_for() {
    cat <<STUB
#!/usr/bin/env bash
# СГЕНЕРИРОВАН \`bash scripts/hooks/install.sh install\` — правится НЕ здесь, а в scripts/hooks/$1.
# $marker_family$stub_version
# Нет адресата — ОТКАЗ, а не успех: «ноль осмотренного» зелёным не бывает (corelib#24).
set -uo pipefail
top="\$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
[ -n "\$top" ] || top="\$PWD"
real="\$top/scripts/hooks/$1"
if [ ! -x "\$real" ]; then
    {
        echo "$1: ОТКАЗ — проверок НЕ БЫЛО: адресата нет в этой рабочей копии либо он не исполняемый."
        echo "  адресат: \$real"
        echo "  исходы: ревизия копии старше хука — внести его слиянием ревизии, где он есть"
        echo "          (git merge <ветка с scripts/hooks/$1>), либо работать из копии на такой ревизии;"
        echo "          файл потерян или не исполняемый — git checkout -- scripts/hooks/$1;"
        echo "          хук снят намеренно — снять и переходник: rm \"\$(git rev-parse --git-common-dir)/hooks/$1\"."
    } >&2
    exit 1
fi
exec "\$real" "\$@"
STUB
}

case "$mode" in
install|check) ;;
stub)
    [ "$#" -ge 2 ] || die "install-hooks: режим stub требует имя хука: $0 stub <имя>"
    stub_for "$2"
    exit 0
    ;;
*) die "install-hooks: неизвестный режим «$mode» (install | check | stub <имя>)" ;;
esac

root="$(git rev-parse --show-toplevel 2>/dev/null)" ||
    die "install-hooks: это не рабочая копия git — провязывать не во что."
common="$(git rev-parse --git-common-dir 2>/dev/null)" ||
    die "install-hooks: git не назвал общий каталог репозитория."
case "$common" in /*) ;; *) common="$root/$common" ;; esac

src="$root/scripts/hooks"
dst="$common/hooks"

hooks=()
while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    base="${rel##*/}"
    case "$base" in *.*) continue ;; esac
    hooks+=("$base")
done < <(git -C "$root" ls-files scripts/hooks)

[ "${#hooks[@]}" -gt 0 ] ||
    die "install-hooks: в scripts/hooks нет НИ ОДНОГО отслеживаемого хука." \
        "Пустой обход здесь означал бы зелёный вывод при непровязанном клоне." \
        "Осмотрено: $src"

configured="$(git -C "$root" config --get core.hooksPath 2>/dev/null || true)"
[ -z "$configured" ] ||
    die "ОТКАЗ: выставлен core.hooksPath=«$configured»." \
        "git ищет хуки только там: переходник в $dst не исполнится ни разу, а без" \
        "файла по тому пути git не исполнит НИЧЕГО и промолчит." \
        "  git config --unset core.hooksPath   # затем: make install-hooks"

mkdir -p "$dst" || die "install-hooks: не создать $dst"

# stub_version_of — редакция переходника из его маркера; пусто — файл не наш.
stub_version_of() {
    sed -n "s/^# ${marker_family}\([0-9][0-9]*\).*/\1/p" "$1" 2>/dev/null | head -1
}

survey() {
    wired=0; missing=(); stale=(); foreign=(); runnable=0; unrunnable=()
    local name t ver
    for name in "${hooks[@]}"; do
        if [ -x "$src/$name" ]; then runnable=$((runnable + 1)); else unrunnable+=("$name"); fi
        t="$dst/$name"
        if [ ! -e "$t" ]; then missing+=("$name"); continue; fi
        ver="$(stub_version_of "$t")"
        if [ -z "$ver" ]; then foreign+=("$name"); continue; fi
        if [ "$ver" -lt "$stub_version" ]; then stale+=("$name(v$ver)"); continue; fi
        if [ -x "$t" ]; then wired=$((wired + 1)); else missing+=("$name"); fi
    done
}
survey

if [ "$mode" = install ]; then
    [ "${#foreign[@]}" -eq 0 ] ||
        die "ОТКАЗ: под именем хука уже лежит ЧУЖОЙ файл: ${foreign[*]}" \
            "Он НЕ перезаписывается: уберите его сами либо слейте со scripts/hooks/<имя>."
    for name in "${hooks[@]}"; do
        stub_for "$name" > "$dst/$name" || die "install-hooks: не записать $dst/$name"
        chmod +x "$dst/$name" || die "install-hooks: не сделать исполняемым $dst/$name"
    done
    echo "провязаны переходниками v$stub_version: ${hooks[*]}"
    survey
fi

kept=()
for f in "$dst"/*; do
    [ -e "$f" ] || continue
    b="${f##*/}"
    case "$b" in *.sample) continue ;; esac
    ours=0
    for name in "${hooks[@]}"; do [ "$b" = "$name" ] && ours=1; done
    [ "$ours" = 1 ] || kept+=("$b")
done

echo "хуки: отслеживаемых ${#hooks[@]} · переходник провязан $wired · не провязано ${#missing[@]} · прежней редакции ${#stale[@]} · занято чужим ${#foreign[@]}"
echo "адресаты: исполнимых $runnable из ${#hooks[@]} ($src)"
echo "клон: $dst"
[ "${#kept[@]}" -eq 0 ] || echo "посторонних хуков оставлено нетронутыми: ${kept[*]}"

rc=0
if [ "${#foreign[@]}" -gt 0 ]; then
    echo "ОТКАЗ: под именем хука лежит ЧУЖОЙ файл: ${foreign[*]} — уберите его либо слейте со scripts/hooks/<имя>." >&2
    rc=1
fi
if [ "${#stale[@]}" -gt 0 ]; then
    echo "ОТКАЗ: переходник прежней редакции: ${stale[*]} (нужна v$stub_version) — на ненайденном адресате выходит нулём и пропускает отправку молча." >&2
    rc=1
fi
if [ "${#missing[@]}" -gt 0 ]; then
    echo "ОТКАЗ: не провязаны: ${missing[*]} — отправка не проверяется локально, конвейер станет первым читателем." >&2
    rc=1
fi
[ "$rc" -eq 0 ] || echo "  make install-hooks   # = bash scripts/hooks/install.sh install" >&2
if [ "${#unrunnable[@]}" -gt 0 ]; then
    echo "ОТКАЗ: адресат не исполним: ${unrunnable[*]} — переходник откажет каждой отправке." >&2
    echo "  git checkout -- scripts/hooks/<имя>   # либо chmod +x scripts/hooks/<имя>" >&2
    rc=1
fi
exit "$rc"
