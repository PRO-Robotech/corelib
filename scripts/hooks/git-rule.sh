#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# ПРАВИЛО GIT — единственный источник предиката (решения владельца 2026-09-22,
# PRO-Robotech/kacho-workspace#770). Сам не исполняется: его читают три
# потребителя, и своей копии предиката нет ни у одного:
#
#   scripts/hooks/commit-msg           сообщение и подпись в МОМЕНТ коммита;
#   scripts/hooks/git-rule-push.sh     имя ветки и ЗАПИСАННЫЕ коммиты отправки (зовёт pre-push);
#   .github/scripts/pr-rule-check.sh   заголовок, голова, тело и коммиты ЗАПРОСА.
#
# Экземпляр свой, не копия (ban20): форма взята у стража kacho, байты не перенесены.
#
# Правило:
#   · ветка — номер задачи ЭТОГО репозитория, `^[0-9]+$`; исключение одно —
#     `main`; ветка, открытая до правила, не переименовывается;
#   · первая строка — `#<N> …`, не длиннее 72 СИМВОЛОВ (не байт), одно
#     утверждение; слияние — `#<N> merge #<M>: …` либо `#<N> merge main: …`;
#   · тело — не длиннее 12 строк;
#   · подпись — корневая учётная запись (`~/.gitconfig`) у автора и коммиттера.
#     Переопределение — отказ при любом значении: user.* уровней local,
#     worktree, command (-c); author.* и committer.* любого уровня, кроме
#     корня (у git они старше user.* и на уровне system); GIT_COMMITTER_*;
#     GIT_CONFIG_GLOBAL;
#   · атрибуции нет: трейлер Co-Authored-By с Claude/anthropic, трейлер
#     Claude-Session:, «Generated with [Claude Code]», ссылка claude.ai/code.
#
# НОМЕР ОБЫЧНОГО КОММИТА С ИМЕНЕМ ВЕТКИ НЕ СВЕРЯЕТСЯ — решение T1, записано в
# PRO-Robotech/corelib#25 (комментарий «Решение T1», 2026-09-24). Форма пачки
# владельца: ветка пачки — номер её первой задачи, коммит на задачу — `#<N> …`
# своей задачи, то есть `#58 …` на ветке `25` законен; сверка «N = ветка»
# отвергла бы пачку на втором коммите. Номер ветки сверяется у актов САМОЙ
# ветки: у слияния на её первой родительской цепочке и у заголовка запроса.
# Что N — задача этого репозитория, хукам не узнать (сети у них нет): это
# сверяет проверка запроса по API трекера.
#
# T0 — граница истории: НАИМЕНЬШЕЕ время автора среди коммитов, заводивших
# scripts/hooks/commit-msg в историю названных ревизий (у слияния — обоих
# родителей). Наименьшее, а не последнее выведенное: снятый и заведённый
# снова хук правила не отменяет, и порядок вывода `git log` (он по дате
# коммиттера) здесь ничего не решает. Выводится, а не выписывается: литерал
# пришлось бы вписать до коммита, момент которого он называет. Коммита правила
# в истории нет (он сам сейчас и создаётся) либо клон мелкий — T0 не выведен,
# судится всё, и потребитель говорит это вслух. Мелкий клон не выводит T0
# НИКОГДА: история за границей не видна, и первое добавление может лежать за
# ней, даже когда граница файла не несёт (хук снят до границы и заведён после —
# T0 вышел бы повторным добавлением, позже настоящего).
#
# КОММИТ ДО ПРАВИЛА — тот, что ЗАПИСАН до правила (дата коммиттера до T0, и в
# его истории нет добавления правила: ни один коммит, заводивший
# scripts/hooks/commit-msg, ему не предок и не он сам), с датой автора до T0.
# Его форма и автор не судятся, коммиттер не судится у записанного до правила;
# атрибуция судится у любого. Даты задаёт клиент (GIT_AUTHOR_DATE,
# GIT_COMMITTER_DATE, commit-tree), историю — нет: коммит, чья история несёт
# добавление правила, записан после него, какие бы даты он ни нёс. Дата
# коммиттера нужна сверх истории: коммит, записанный после T0, — новый коммит,
# даже когда автор и дата автора у него старые (перенос старой работы; rebase
# правило владельца запрещает).
#
# ГРАНИЦА (измерена пробой, утверждения «граница:»): коммит, записанный после
# правила МИМО хука коммита на основании СТАРШЕ правила с обеими датами до T0,
# от работы до правила не отличим ничем из данных git — основание и обе даты
# выбирает клиент. Его форму, автора и имя его ветки не судят ни страж
# отправки, ни проверка запроса. Через хук коммита такой коммит не записать: в
# дереве с хуком история несёт добавление правила, и дата автора до T0 — отказ.
GIT_RULE_T0_PATH=scripts/hooks/commit-msg
GIT_RULE_T0=0
GIT_RULE_T0_KNOWN=0
GIT_RULE_T0_ADDS=()
GIT_RULE_SUBJECT_MAX=72
GIT_RULE_BODY_MAX=12

# git_rule_load_t0 [ревизия…] — выставляет GIT_RULE_T0 (эпохой),
# GIT_RULE_T0_KNOWN и GIT_RULE_T0_ADDS — коммиты, заводившие путь правила в
# историю ревизий. Не выведен — T0 0, KNOWN 0, добавлений нет: судится всё.
git_rule_load_t0() {
    local recs h at min="" adds=()
    GIT_RULE_T0=0
    GIT_RULE_T0_KNOWN=0
    GIT_RULE_T0_ADDS=()
    [ "$#" -gt 0 ] || set -- HEAD
    [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" != true ] || return 0
    recs="$(git log --no-color --diff-filter=A --format='%H %at' "$@" -- "$GIT_RULE_T0_PATH" 2>/dev/null)" || return 0
    while read -r h at; do
        [ -n "$h" ] || continue
        adds+=("$h")
        if [ -z "$min" ] || [ "$at" -lt "$min" ]; then min="$at"; fi
    done <<<"$recs"
    [ -n "$min" ] || return 0
    GIT_RULE_T0="$min"
    GIT_RULE_T0_KNOWN=1
    GIT_RULE_T0_ADDS=("${adds[@]}")
}

# git_rule_after_add <коммит> — в истории коммита есть добавление правила (оно
# ему предок либо он сам). Ответ git, отличный от «да» и «нет», читается «да»:
# недоказанное «до правила» не освобождает от суждения.
git_rule_after_add() {
    local a rc
    for a in "${GIT_RULE_T0_ADDS[@]}"; do
        git merge-base --is-ancestor "$a" "$1" 2>/dev/null
        rc=$?
        [ "$rc" = 1 ] || return 0
    done
    return 1
}

# git_rule_recorded_before <коммит> <дата коммиттера> — коммит записан до
# правила: дата коммиттера до T0 и в истории нет добавления правила.
git_rule_recorded_before() {
    [ "$2" -lt "$GIT_RULE_T0" ] && ! git_rule_after_add "$1"
}

# git_rule_pre_rule <коммит> <дата автора> <дата коммиттера> — коммит до
# правила: записан до правила и дата автора до T0. T0 не выведен — ни один.
git_rule_pre_rule() {
    [ "$2" -lt "$GIT_RULE_T0" ] && git_rule_recorded_before "$1" "$3"
}

# git_rule_range_pre_rule <аргументы rev-list…> — в диапазоне есть коммит до правила.
git_rule_range_pre_rule() {
    local h at ct
    while read -r h at ct; do
        [ -n "$h" ] || continue
        git_rule_pre_rule "$h" "$at" "$ct" && return 0
    done < <(git log --no-color --format='%H %at %ct' "$@" 2>/dev/null)
    return 1
}

git_rule_t0_text() {
    if [ "$GIT_RULE_T0_KNOWN" = 1 ]; then
        date -u -d "@$GIT_RULE_T0" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf '@%s' "$GIT_RULE_T0"
    else
        printf 'не выведен — коммита, заведшего %s, в истории нет либо клон мелкий: судится всё' "$GIT_RULE_T0_PATH"
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

# git_rule_subject_numbers <первая строка> — номера задач, которые первая строка
# называет: N из `#<N> …` и M из `… merge #<M>: …`, по строке на номер.
git_rule_subject_numbers() {
    local n
    n="$(git_rule_subject_task "$1")" || return 0
    printf '%s\n' "$n"
    if [[ "$1" =~ ^#[0-9]+\ merge\ #([0-9]+):\  ]]; then printf '%s\n' "${BASH_REMATCH[1]}"; fi
}

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

# git_rule_form <сообщение> <слияние: 0|1> <ветка> — нарушения ФОРМЫ сообщения
# (уже после git stripspace), по строке на каждое; пусто — форма соблюдена.
# Номер слияния сверяется с <веткой>, только когда она — номер; пусто — нет.
# Единица счёта тела — строка после git stripspace, СЧИТАЯ пустые между
# абзацами: так тело видит читатель `git log`.
git_rule_form() {
    local msg="$1" merge="$2" branch="$3" subj rest n len why lines
    subj="${msg%%$'\n'*}"
    rest=""
    [ "$subj" = "$msg" ] || rest="${msg#*$'\n'}"
    if ! n="$(git_rule_subject_task "$subj")"; then
        printf '%s\n' "первая строка не начинается с «#<N> »: «$subj»"
    elif [ "$merge" = 1 ]; then
        git_rule_merge_form "$subj" ||
            printf '%s\n' "слияние — «#<N> merge #<M>: …» либо «#<N> merge main: …», а не «$subj»"
        if git_rule_is_number "$branch" && [ "$n" != "$branch" ]; then
            printf '%s\n' "слияние «#$n» на ветке «$branch»: слияние — акт ветки, и номер у него её"
        fi
    fi
    len="$(git_rule_chars "$subj")"
    [ "$len" -le "$GIT_RULE_SUBJECT_MAX" ] ||
        printf '%s\n' "первая строка — $len символов, предел $GIT_RULE_SUBJECT_MAX"
    if why="$(git_rule_second_statement "$subj")"; then
        printf '%s\n' "первая строка — одно утверждение, а здесь $why: «$subj»"
    fi
    if [ -n "$rest" ]; then
        if [ -n "${rest%%$'\n'*}" ]; then
            printf '%s\n' "после первой строки нет пустой строки — git склеит следующую строку с первой в заголовок"
        else
            lines="$(printf '%s\n' "${rest#*$'\n'}" | wc -l | tr -d ' ')"
            [ "$lines" -le "$GIT_RULE_BODY_MAX" ] ||
                printf '%s\n' "тело — $lines строк после git stripspace (пустые между абзацами в счёте), предел $GIT_RULE_BODY_MAX"
        fi
    fi
}

# git_rule_howto <класс> <ветка> [<строка атрибуции>] — «как правильно» для
# класса нарушения, строкой; отказ печатает строки только своих классов, чтобы
# памятка не заслоняла причину. Классы: form (первая строка и тело), merge
# (форма слияния; номер — <ветки>, когда она номер), attribution, ident
# (подпись), branch, editor, date (дата автора до T0).
git_rule_howto() {
    local class="$1" branch="$2" attr="${3:-}" b="<N>"
    git_rule_is_number "$branch" && b="$branch"
    case "$class" in
        form) printf '%s\n' "сообщение: git commit -m \"#<N> <одно утверждение>\" -m \"<тело>\" — первая строка начинается с «#<N> » (<N> — номер задачи этого репозитория), не длиннее $GIT_RULE_SUBJECT_MAX символов, без «;» и без точки в конце; тело — после пустой строки, не длиннее $GIT_RULE_BODY_MAX строк" ;;
        merge) printf '%s\n' "слияние: git merge --no-ff <ветка> -m \"#$b merge #<M>: <что влито>\" либо -m \"#$b merge main: <что влито>\" — номер слияния — номер этой ветки; первая строка не длиннее $GIT_RULE_SUBJECT_MAX символов, тело — не длиннее $GIT_RULE_BODY_MAX строк" ;;
        attribution) printf '%s\n' "атрибуция: удалите строку «$attr» — трейлеры Co-Authored-By с Claude/anthropic, Claude-Session:, «Generated with Claude Code» и ссылки claude.ai/code в сообщение не пишутся; соавтор-человек законен" ;;
        ident) printf '%s\n' "подпись: коммит без --author, -c user.*/author.*/committer.*, GIT_COMMITTER_* и GIT_CONFIG_GLOBAL; настройку подписи уровня local/worktree снимите (git config --local --unset <ключ>); подпись задаётся один раз — git config --global user.name / user.email" ;;
        branch) printf '%s\n' "ветка: git branch -m <N> — ветка называется номером задачи этого репозитория (^[0-9]+\$), исключение одно — main" ;;
        editor) printf '%s\n' "сообщение — через -m или -F, без редактора: git commit -m \"#<N> …\" — строку «#…» git вырезает как комментарий" ;;
        date) printf '%s\n' "дата автора — текущая: коммит без --date и GIT_AUTHOR_DATE в прошлом; у --amend и -C вершины с датой до T0 — с --reset-author" ;;
    esac
}

# git_rule_attribution <текст> — печатает первую строку атрибуции; 1 — её нет.
# Регистр не различается. Трейлер — строка, НАЧАТАЯ его именем: то же имя в
# середине строки — упоминание (так пишут, снимая шаблон), а не трейлер.
# Co-Authored-By без Claude/anthropic — законный соавтор.
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

# git_rule_ident_overrides — печатает уровни, на которых подпись задана ПОВЕРХ
# корня, `уровень:ключ`; пусто — нет. user.* уровня system ниже корня и его не
# переопределяет; author.* и committer.* у git старше user.* на ЛЮБОМ уровне
# (замер: committer.email уровня system даёт коммиттера при корневом
# user.email), поэтому они — переопределение везде, кроме самого корня.
git_rule_ident_overrides() {
    git config --show-scope --get-regexp '^(user|author|committer)\.(name|email)$' 2>/dev/null |
        while read -r scope key _; do
            case "$scope:$key" in
                global:*) ;;
                system:user.*) ;;
                *) printf '%s:%s\n' "$scope" "$key" ;;
            esac
        done | sort -u
}

# git_rule_before_rule <ревизия> — ветка открыта до правила: среди её
# СОБСТВЕННЫХ коммитов (не достижимых ни с `main`, ни с веток-номеров —
# локальных и удалённых) есть коммит до правила (git_rule_pre_rule).
git_rule_before_rule() {
    local ref short excl=()
    while IFS= read -r ref; do
        case "$ref" in
            refs/heads/*) short="${ref#refs/heads/}" ;;
            refs/remotes/*/HEAD) continue ;;
            refs/remotes/*) short="${ref#refs/remotes/}"; short="${short#*/}" ;;
            *) continue ;;
        esac
        if [ "$short" = main ] || git_rule_is_number "$short"; then excl+=("^$ref"); fi
    done < <(git for-each-ref --format='%(refname)' refs/heads refs/remotes)
    git_rule_range_pre_rule "$1" "${excl[@]}"
}

# git_rule_judge_range <ветка> <подпись> <аргументы rev-list…>
#
# Судит каждый ЗАПИСАННЫЙ коммит диапазона — у отправки и у запроса. Сообщение
# чистится git stripspace, как у хука коммита: единица счёта тела одна.
#   · атрибуция — у любого коммита;
#   · коммит не до правила (git_rule_pre_rule) — форма (git_rule_form): слияние
#     — по числу родителей, номер ветки — у слияний ПЕРВОЙ РОДИТЕЛЬСКОЙ цепочки
#     <ветки>; номера первой строки копятся в GIT_RULE_NUMBERS;
#   · <подпись> «имя <адрес>» — автор у коммита не до правила, коммиттер у
#     коммита, не ЗАПИСАННОГО до правила (git_rule_recorded_before): перепись
#     старого коммита записывает нового коммиттера; «-» — подпись не судится.
# Находки — в GIT_RULE_FINDINGS с коротким sha; счёт — GIT_RULE_SEEN и
# GIT_RULE_AFTER_RULE (судимых формой). Код 1 — диапазон не читается git.
GIT_RULE_FINDINGS=()
GIT_RULE_NUMBERS=()
GIT_RULE_SEEN=0
GIT_RULE_AFTER_RULE=0
git_rule_judge_range() {
    local branch="$1" ident="$2" owned log rec h at ct parents author committer raw msg merge on f attr before
    shift 2
    owned="$(git rev-list --first-parent "$@" 2>/dev/null)" || {
        GIT_RULE_FINDINGS+=("диапазон «$*» не читается git rev-list — судить нечем")
        return 1
    }
    log="$(git -c log.showSignature=false log --no-color --encoding=UTF-8 \
        --format='%H%x1f%at%x1f%ct%x1f%P%x1f%an <%ae>%x1f%cn <%ce>%x1f%B%x1e' "$@" 2>/dev/null)" || {
        GIT_RULE_FINDINGS+=("диапазон «$*» не читается git log — судить нечем")
        return 1
    }
    # Запись завершается \x1e, git дописывает перевод строки после каждой.
    while IFS= read -r -d $'\x1e' rec; do
        rec="${rec#$'\n'}"
        [ -n "$rec" ] || continue
        IFS=$'\x1f' read -r -d '' h at ct parents author committer raw <<<"$rec"
        [ -n "${h:-}" ] || continue
        GIT_RULE_SEEN=$((GIT_RULE_SEEN + 1))
        msg="$(printf '%s' "$raw" | git stripspace)"
        if attr="$(git_rule_attribution "$msg")"; then
            GIT_RULE_FINDINGS+=("${h:0:10} атрибуция в сообщении: «$attr»")
        fi
        # before — записан до правила; коммит до правила (git_rule_pre_rule) —
        # записан до правила и с датой автора до T0: before уже посчитан.
        before=0
        git_rule_recorded_before "$h" "$ct" && before=1
        if [ "$ident" != - ] && [ "$before" = 0 ] && [ "$committer" != "$ident" ]; then
            GIT_RULE_FINDINGS+=("${h:0:10} коммиттер «$committer» — не корневая учётная запись «$ident»")
        fi
        [ "$before" = 0 ] || [ "$at" -ge "$GIT_RULE_T0" ] || continue
        GIT_RULE_AFTER_RULE=$((GIT_RULE_AFTER_RULE + 1))
        merge=0
        [ "$(wc -w <<<"$parents")" -lt 2 ] || merge=1
        on=""
        [[ $'\n'"$owned"$'\n' != *$'\n'"$h"$'\n'* ]] || on="$branch"
        while IFS= read -r f; do
            [ -z "$f" ] || GIT_RULE_FINDINGS+=("${h:0:10} $f")
        done < <(git_rule_form "$msg" "$merge" "$on")
        while IFS= read -r f; do
            [ -z "$f" ] || GIT_RULE_NUMBERS+=("$f")
        done < <(git_rule_subject_numbers "${msg%%$'\n'*}")
        if [ "$ident" != - ] && [ "$author" != "$ident" ]; then
            GIT_RULE_FINDINGS+=("${h:0:10} автор «$author» — не корневая учётная запись «$ident»")
        fi
    done <<<"$log"
    return 0
}
