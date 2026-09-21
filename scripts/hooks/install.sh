#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# install.sh — провязать хук отправки этого клона и сказать, провязан ли он.
# Своей цели `make` здесь нет: у фундамента (corelib) нет Makefile вовсе —
# ci.yml зовёт go/golangci-lint/gosec напрямую (см. его шапку: «форма взята у
# конвейера службы, но НЕ скопирована»). Тот же принцип держит и этот файл:
# способ провязки — команда, а не цель:
#
#   bash scripts/hooks/install.sh install
#   bash scripts/hooks/install.sh check
#
# ЭКЗЕМПЛЯР ЭТОГО МЕХАНИЗМА — СВОЙ, А НЕ СКОПИРОВАННЫЙ. Идея переходника
# (стаб в `.git/hooks`, исполняющий отслеживаемый скрипт из рабочей копии) та
# же, что у kacho и у kaname — но байты не перенесены: копия одного
# отслеживаемого пути в двух стволах запрещена (ban20-copy-ban). У фундамента
# нет ни Makefile, ни группировки прогона по составу диффа — здесь один
# Go-модуль без соседей, и хук отправки зовёт `go`/`golangci-lint` напрямую.
#
# ПОЧЕМУ НЕ `core.hooksPath` — тот же довод везде: git начинает искать хуки
# ТОЛЬКО по указанному пути, и всё, что уже лежит в `.git/hooks`, перестаёт
# исполняться молча. Переходник этого не делает: посторонний хук под другим
# именем не трогается никогда, переходник берёт скрипт из ТЕКУЩЕЙ рабочей
# копии, путей машины в нём нет.
#
# ХУКОМ СЧИТАЕТСЯ отслеживаемый файл в scripts/hooks без точки в имени.
set -uo pipefail

mode="${1:-install}"

die() { printf '%s\n' "$@" >&2; exit 1; }

root="$(git rev-parse --show-toplevel 2>/dev/null)" ||
    die "install-hooks: это не рабочая копия git — провязывать не во что."
common="$(git rev-parse --git-common-dir 2>/dev/null)" ||
    die "install-hooks: git не назвал общий каталог репозитория."
case "$common" in /*) ;; *) common="$root/$common" ;; esac

src="$root/scripts/hooks"
dst="$common/hooks"

hooks=()
while IFS= read -r rel; do
    base="${rel##*/}"
    case "$base" in *.*) continue ;; esac
    hooks+=("$base")
done < <(git -C "$root" ls-files scripts/hooks)

[ "${#hooks[@]}" -gt 0 ] ||
    die "install-hooks: в scripts/hooks нет НИ ОДНОГО отслеживаемого хука." \
        "Пустой обход здесь означал бы зелёный вывод при непровязанном клоне."

marker="corelib-hook-stub v1"

configured="$(git config --get core.hooksPath 2>/dev/null || true)"
if [ -n "$configured" ]; then
    abs="$configured"
    case "$abs" in /*) ;; *) abs="$root/$abs" ;; esac
    if [ ! -d "$abs" ]; then
        die "ОТКАЗ: core.hooksPath = «$configured» — каталога по этому пути НЕТ." \
            "  git config --unset core.hooksPath   # затем: bash scripts/hooks/install.sh install"
    fi
    real_cfg="$(cd "$abs" && pwd -P)"
    if [ "$real_cfg" != "$(cd "$src" && pwd -P)" ]; then
        die "ОТКАЗ: core.hooksPath = «$configured» ведёт в $real_cfg." \
            "git будет искать хуки ТАМ, провязка в $dst не исполнится ни разу." \
            "  git config --unset core.hooksPath   # затем: bash scripts/hooks/install.sh install"
    fi
    echo "core.hooksPath = «$configured» — хуки исполняются НАПРЯМУЮ из $src."
    echo "отслеживаемых хуков: ${#hooks[@]}; исполняются напрямую"
    exit 0
fi

mkdir -p "$dst" || die "install-hooks: не создать $dst"

stub_for() {
    cat <<STUB
#!/usr/bin/env bash
# СГЕНЕРИРОВАН \`bash scripts/hooks/install.sh install\` — правится НЕ здесь, а в scripts/hooks/$1.
# $marker
set -uo pipefail
top="\$(git rev-parse --show-toplevel 2>/dev/null)" || top=""
[ -n "\$top" ] || top="\$PWD"
real="\$top/scripts/hooks/$1"
if [ ! -x "\$real" ]; then
    echo "$1: в этой рабочей копии нет \$real — проверок НЕ БЫЛО" >&2
    exit 0
fi
exec "\$real" "\$@"
STUB
}

wired=0
missing=()
foreign=()
for name in "${hooks[@]}"; do
    target="$dst/$name"
    if [ ! -e "$target" ]; then
        missing+=("$name")
        continue
    fi
    if grep -qF "$marker" "$target" 2>/dev/null; then
        if [ -x "$target" ]; then wired=$((wired + 1)); else missing+=("$name"); fi
        continue
    fi
    foreign+=("$name")
done

kept=()
for f in "$dst"/*; do
    [ -e "$f" ] || continue
    b="${f##*/}"
    case "$b" in *.sample) continue ;; esac
    ours=0
    for name in "${hooks[@]}"; do [ "$b" = "$name" ] && ours=1; done
    [ "$ours" = 1 ] || kept+=("$b")
done

report() {
    echo "хуки: провязано $wired из ${#hooks[@]} ($dst)"
    [ "${#kept[@]}" -eq 0 ] ||
        printf 'посторонних хуков оставлено нетронутыми: %s\n' "${kept[*]}"
}

case "$mode" in
check)
    report
    if [ "${#foreign[@]}" -gt 0 ]; then
        echo "ОТКАЗ: под именем хука лежит ЧУЖОЙ файл: ${foreign[*]}" >&2
        exit 1
    fi
    if [ "${#missing[@]}" -gt 0 ]; then
        echo "ОТКАЗ: не провязаны: ${missing[*]}" >&2
        echo "Отправка ветки НЕ проверяется локально: конвейер станет первым читателем." >&2
        echo "  bash scripts/hooks/install.sh install" >&2
        exit 1
    fi
    exit 0
    ;;
notice)
    if [ "${#missing[@]}" -gt 0 ] || [ "${#foreign[@]}" -gt 0 ]; then
        echo "ВНИМАНИЕ: хуки git не провязаны (${#missing[@]} не провязано, ${#foreign[@]} занято чужим)." >&2
        echo "  Отправка ветки НЕ будет проверена локально — «bash scripts/hooks/install.sh check» это покажет." >&2
    fi
    exit 0
    ;;
install) ;;
*) die "install-hooks: неизвестный режим «$mode» (install | check | notice)" ;;
esac

if [ "${#foreign[@]}" -gt 0 ]; then
    die "ОТКАЗ: под именем хука уже лежит ЧУЖОЙ файл: ${foreign[*]}" \
        "Он НЕ перезаписывается: уберите его сами либо слейте со scripts/hooks/<имя>."
fi

installed=()
for name in "${hooks[@]}"; do
    target="$dst/$name"
    stub_for "$name" > "$target" || die "install-hooks: не записать $target"
    chmod +x "$target" || die "install-hooks: не сделать исполняемым $target"
    installed+=("$name")
done

wired=${#installed[@]}
report
printf 'провязаны переходниками: %s\n' "${installed[*]}"
echo "проверить в любой момент: bash scripts/hooks/install.sh check"
