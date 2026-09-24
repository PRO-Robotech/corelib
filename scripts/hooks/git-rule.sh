#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# ПРАВИЛО GIT — единственный источник предиката (решения владельца 2026-09-22,
# PRO-Robotech/kacho-workspace#770). Сам не исполняется: его читает хук коммита
# scripts/hooks/commit-msg, и своей копии предиката у потребителя нет. Экземпляр
# свой, не копия (ban20): форма взята у стража kacho, байты не перенесены.
#
# Правило:
#   · ветка — номер задачи ЭТОГО репозитория, `^[0-9]+$`; исключение одно —
#     `main`; ветка, открытая до правила, не переименовывается;
#   · первая строка — `#<N> …`, не длиннее 72 СИМВОЛОВ (не байт), одно
#     утверждение; слияние — `#<N> merge #<M>: …` либо `#<N> merge main: …`;
#   · тело — не длиннее 12 строк;
#   · подпись — корневая учётная запись (`~/.gitconfig`); -c user.*,
#     --local/--worktree user.*, GIT_COMMITTER_*, GIT_CONFIG_GLOBAL её не
#     переопределяют — отказ при любом значении;
#   · атрибуции нет: трейлер Co-Authored-By с Claude/anthropic, строка
#     Claude-Session:, «Generated with [Claude Code]», ссылка claude.ai/code.
#
# НОМЕР ОБЫЧНОГО КОММИТА С ИМЕНЕМ ВЕТКИ НЕ СВЕРЯЕТСЯ (решение corelib#25,
# 2026-09-24). Форма пачки владельца: ветка пачки — номер её первой задачи,
# коммит на задачу — `#<N> …` своей задачи, то есть `#58 …` на ветке `25`
# законен. Сверка «N = ветка» отвергла бы пачку на втором же коммите. Что N —
# задача этого репозитория, хук не знает: сети у него нет; это предмет проверки
# запроса. Номер ветки сверяется у СЛИЯНИЯ: слияние — акт самой ветки.
#
# T0 — граница истории: время автора коммита, заведшего scripts/hooks/commit-msg
# в историю ревизии. Выводится, а не выписывается: литерал пришлось бы вписать
# до коммита, момент которого он называет. Коммит с датой автора до T0 по форме
# и автору не судится; атрибуция и коммиттер судятся у любого. Коммита правила в
# истории нет (он сам сейчас и создаётся) либо он на границе мелкого клона — T0
# не выведен, судится всё, и отказ говорит это вслух.
GIT_RULE_T0_PATH=scripts/hooks/commit-msg
GIT_RULE_T0=0
GIT_RULE_T0_KNOWN=0
# shellcheck disable=SC2034  # пределы читает потребитель (commit-msg)
GIT_RULE_SUBJECT_MAX=72
# shellcheck disable=SC2034  # то же
GIT_RULE_BODY_MAX=12

# git_rule_t0 [ревизия] — печатает T0 эпохой; 1 — не выведен.
# `tail`: git log пишет новое первым, а правило вступило самым старым добавлением.
# Граница мелкого клона показывает ВСЕ свои файлы добавленными: найденный на ней
# «коммит правила» — время границы, а не правила.
git_rule_t0() {
    local rec h shallow
    rec="$(git log --no-color --diff-filter=A --format='%H %at' "${1:-HEAD}" -- "$GIT_RULE_T0_PATH" 2>/dev/null | tail -1)"
    [ -n "$rec" ] || return 1
    h="${rec%% *}"
    shallow="$(git rev-parse --git-path shallow 2>/dev/null)"
    if [ -n "$shallow" ] && [ -f "$shallow" ] && grep -qx "$h" "$shallow"; then
        return 1
    fi
    printf '%s' "${rec##* }"
}

# git_rule_load_t0 [ревизия] — выставляет GIT_RULE_T0 и GIT_RULE_T0_KNOWN.
git_rule_load_t0() {
    if GIT_RULE_T0="$(git_rule_t0 "${1:-HEAD}")"; then
        GIT_RULE_T0_KNOWN=1
    else
        GIT_RULE_T0=0
        GIT_RULE_T0_KNOWN=0
    fi
}

git_rule_t0_text() {
    if [ "$GIT_RULE_T0_KNOWN" = 1 ]; then
        date -u -d "@$GIT_RULE_T0" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf '@%s' "$GIT_RULE_T0"
    else
        printf 'не выведен — коммита, заведшего %s, в истории нет либо он на границе мелкого клона: судится всё' "$GIT_RULE_T0_PATH"
    fi
}

git_rule_is_number() { [[ "$1" =~ ^[0-9]+$ ]]; }

# git_rule_subject_task <первая строка> — печатает N из `#<N> …`; 1 — формы нет.
git_rule_subject_task() {
    [[ "$1" =~ ^#([0-9]+)\ [^[:space:]] ]] || return 1
    printf '%s' "${BASH_REMATCH[1]}"
}

# git_rule_merge_form <первая строка> — `#<N> merge #<M>: …` либо `#<N> merge main: …`.
git_rule_merge_form() { [[ "$1" =~ ^#[0-9]+\ merge\ (#[0-9]+|main):\ [^[:space:]] ]]; }

# git_rule_chars <строка> — число СИМВОЛОВ UTF-8 при любой локали: считаются
# байты, кроме байтов продолжения (10xxxxxx). ${#…} в bash под LC_ALL=C считает
# байты, и кириллица в 72 символа (141 байт) была бы отвергнута.
git_rule_chars() { printf '%s' "$1" | LC_ALL=C tr -d '\200-\277' | wc -c | tr -d ' '; }

# git_rule_second_statement <первая строка> — печатает признак второго
# утверждения; 1 — признака нет. Разбором различимы три: точка с запятой, точка
# в конце, точка с пробелом перед заглавной буквой (второе предложение). Тире
# не судится: «ветка — номер задачи» — одно утверждение, и разбором его от двух
# не отличить; это держится вниманием.
#
# Заглавная — латинская A-Z либо кириллическая А-Я, Ё, сверенная по БАЙТАМ UTF-8
# (D0 81, D0 90…D0 AF) при LC_ALL=C: класс [[:upper:]] зависит от локали, и под
# LC_ALL=C кириллицы в нём нет.
git_rule_second_statement() {
    local s="$1" LC_ALL=C re
    case "$s" in
        *\;*) printf 'точка с запятой'; return 0 ;;
        *.) printf 'точка в конце'; return 0 ;;
    esac
    re=$'\\. ([A-Z]|\xd0[\x81\x90-\xaf])'
    if [[ "$s" =~ $re ]]; then
        printf 'второе предложение после «. »'
        return 0
    fi
    return 1
}

# git_rule_attribution <текст> — печатает первую строку атрибуции; 1 — её нет.
# Регистр не различается. Co-Authored-By без Claude/anthropic — законный соавтор;
# имя трейлера в середине строки — упоминание, а не трейлер.
git_rule_attribution() {
    local line found=1 restore
    restore="$(shopt -p nocasematch)"
    shopt -s nocasematch
    while IFS= read -r line; do
        if [[ "$line" =~ ^[[:space:]]*co-authored-by:.*(claude|anthropic) ]] ||
            [[ "$line" =~ ^[[:space:]]*claude-session: ]] ||
            [[ "$line" =~ generated[[:space:]]+with[[:space:]]+\[?claude[[:space:]]+code ]] ||
            [[ "$line" =~ claude\.ai/code ]]; then
            printf '%s' "$line"
            found=0
            break
        fi
    done <<<"$1"
    eval "$restore"
    return "$found"
}

# git_rule_root_ident — «имя <адрес>» корневой учётной записи; 1 — не задана.
git_rule_root_ident() {
    local n e
    n="$(git config --global --includes --get user.name 2>/dev/null)" || return 1
    e="$(git config --global --includes --get user.email 2>/dev/null)" || return 1
    [ -n "$n" ] && [ -n "$e" ] || return 1
    printf '%s <%s>' "$n" "$e"
}

# git_rule_ident_overrides — печатает уровни, на которых user.* задан ПОВЕРХ
# корня (local, worktree, command — это -c и GIT_CONFIG_COUNT); пусто — нет.
# Уровень system ниже корня и его не переопределяет.
git_rule_ident_overrides() {
    git config --show-scope --get-regexp '^user\.(name|email)$' 2>/dev/null |
        while read -r scope key _; do
            case "$scope" in global|system) ;; *) printf '%s:%s\n' "$scope" "$key" ;; esac
        done | sort -u
}

# git_rule_before_rule <ревизия> — ветка открыта до правила: среди её
# СОБСТВЕННЫХ коммитов (не достижимых ни с `main`, ни с веток-номеров —
# локальных и удалённых) есть коммит с датой автора до T0.
git_rule_before_rule() {
    local ref short excl=() d
    while IFS= read -r ref; do
        case "$ref" in
            refs/heads/*) short="${ref#refs/heads/}" ;;
            refs/remotes/*/HEAD) continue ;;
            refs/remotes/*) short="${ref#refs/remotes/}"; short="${short#*/}" ;;
            *) continue ;;
        esac
        if [ "$short" = main ] || git_rule_is_number "$short"; then excl+=("^$ref"); fi
    done < <(git for-each-ref --format='%(refname)' refs/heads refs/remotes)
    while IFS= read -r d; do
        [ -n "$d" ] || continue
        [ "$d" -lt "$GIT_RULE_T0" ] && return 0
    done < <(git log --no-color --format=%at "$1" "${excl[@]}" 2>/dev/null)
    return 1
}
