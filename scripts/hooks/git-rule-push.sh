#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Страж правила git для ОТПРАВКИ. Предикат — scripts/hooks/git-rule.sh; зовёт
# scripts/hooks/pre-push первым, до проверок дерева и до обхода
# CORELIB_SKIP_PREPUSH: обход снимает проверки, а не правило. Проба —
# scripts/hooks/git-rule-inject.sh.
#
# Вход — строки git `<local_ref> <local_sha> <remote_ref> <remote_sha>` на stdin.
#   · снятие ссылки (одни нули) не судится — вершины у него нет;
#   · имя судится у ВЕТКИ (`refs/heads/*`), которой на удалённом ещё нет: номер
#     задачи, `main` либо ветка до правила (свой коммит до правила —
#     git_rule_pre_rule); ветка, уже лежащая на удалённом, не переименовывается;
#   · коммиты судятся у ЛЮБОЙ ссылки, в том числе метки: метка на
#     неопубликованном коммите увозит его так же, как ветка. Новые — недостижимые
#     ни с одной удалённой ссылки и с прежней вершины этой; суждение —
#     git_rule_judge_range с корневой подписью.
# Хук коммита мог не исполниться (--no-verify, cherry-pick, rebase, am, непровязанный
# клон): здесь судятся уже ЗАПИСАННЫЕ коммиты, чем бы они ни были сделаны.
#
# ГРАНИЦЫ. Страж клиентский: `git push --no-verify` и непровязанный клон его
# минуют; держатель, которого эти обходы не минуют, — проверка запроса
# (.github/scripts/pr-rule-check.sh), и граница по датам у неё та же. Даты
# коммита задаёт клиент, поэтому коммит до правила — это ещё и история без
# добавления правила (git_rule_pre_rule): коммит поверх правила судится целиком,
# какие бы даты он ни нёс. Не отличим от работы до правила и не судится по
# форме, автору и имени ветки коммит, записанный на основании СТАРШЕ правила с
# обеими датами до T0 (замер — утверждения «граница:» пробы).
#
# Исходы: 0 — нарушений нет; 1 — нарушения, названы поимённо; 2 — судить не
# смог (корень не задан либо перенаправлен GIT_CONFIG_GLOBAL).
set -uo pipefail

# Каталог — разбором пути, без dirname: страж исполняется в PATH хука отправки,
# а в его перечне инструментов dirname нет.
src="${BASH_SOURCE[0]}"
case "$src" in */*) here="${src%/*}" ;; *) here=. ;; esac
here="$(cd "$here" && pwd)"
if [ ! -f "$here/git-rule.sh" ]; then
    echo "git-rule-push: нет $here/git-rule.sh — предиката правила нет, судить нечем" >&2
    exit 2
fi
# shellcheck source=scripts/hooks/git-rule.sh
. "$here/git-rule.sh"

zero="0000000000000000000000000000000000000000"
ident=""
cannot=""
if [ -n "${GIT_CONFIG_GLOBAL+x}" ]; then
    cannot="корень перенаправлен окружением GIT_CONFIG_GLOBAL — подпись сверять не с чем"
elif ! ident="$(git_rule_root_ident)"; then
    cannot="корневая учётная запись не задана (git config --global user.name / user.email) — подпись сверять не с чем"
fi

refs=0
t0_texts=()
while read -r _lref lsha rref rsha; do
    [ -n "${lsha:-}" ] || continue
    case "$lsha" in *[!0]*) ;; *) continue ;; esac
    refs=$((refs + 1))
    if ! tip="$(git rev-parse --verify --quiet "$lsha^{commit}")"; then
        # Метка не на коммите (на дереве или блобе) коммитов не везёт.
        git cat-file -e "$lsha" 2>/dev/null ||
            GIT_RULE_FINDINGS+=("ссылка «$rref»: объект $lsha не разрешается")
        continue
    fi
    git_rule_load_t0 "$tip"
    t0_texts+=("$(git_rule_t0_text)")
    neg=(--not --remotes)
    existed=0
    if [ -n "${rsha:-}" ] && [ "$rsha" != "$zero" ]; then
        existed=1
        git cat-file -e "$rsha^{commit}" 2>/dev/null && neg+=("$rsha")
    fi
    name=""
    case "$rref" in refs/heads/*) name="${rref#refs/heads/}" ;; esac
    if [ -n "$name" ] && [ "$existed" = 0 ] && [ "$name" != main ] &&
        ! git_rule_is_number "$name" && ! git_rule_before_rule "$tip"; then
        GIT_RULE_FINDINGS+=("ветка «$name»: новая ветка называется номером задачи (^[0-9]+\$), исключение одно — main; ветка до правила несёт свой коммит, записанный до правила (обе даты до T0, в истории нет добавления правила)")
    fi
    if [ -n "$cannot" ]; then
        git_rule_judge_range "$name" - "$tip" "${neg[@]}"
    else
        git_rule_judge_range "$name" "$ident" "$tip" "${neg[@]}"
    fi
done

t0_line=""
declare -A t0_seen=()
for t in "${t0_texts[@]}"; do
    [ -z "${t0_seen[$t]:-}" ] || continue
    t0_seen[$t]=1
    t0_line="${t0_line:+$t0_line; }$t"
done
echo "== правило git (scripts/hooks/git-rule.sh; T0 ${t0_line:-—}): ссылок $refs, новых коммитов $GIT_RULE_SEEN, из них после правила (судимых формой) $GIT_RULE_AFTER_RULE"
[ "$refs" -gt 0 ] || echo "   ссылок с содержимым в отправке нет — судить нечего"
if [ "${#GIT_RULE_FINDINGS[@]}" -gt 0 ]; then
    echo "   нарушений: ${#GIT_RULE_FINDINGS[@]}"
    printf '     %s\n' "${GIT_RULE_FINDINGS[@]}"
    [ -z "$cannot" ] || echo "   и подпись не сверена: $cannot"
    exit 1
fi
if [ -n "$cannot" ] && [ "$GIT_RULE_SEEN" -gt 0 ]; then
    echo "   подпись НЕ сверена у $GIT_RULE_SEEN коммитов: $cannot"
    exit 2
fi
echo "   нарушений нет"
exit 0
