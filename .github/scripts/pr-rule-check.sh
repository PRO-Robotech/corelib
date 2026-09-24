#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Проверка ЗАПРОСА по правилу git. Предикат — scripts/hooks/git-rule.sh, общий с
# хуком коммита и стражем отправки. Хуки клиентские: их минуют --no-verify,
# непровязанный клон и правка через веб. Здесь — держатель, которого клиентом
# не обойти; шаг стоит в задании «сборка · vet · gofmt» ci.yml, чей контекст
# защита `main` требует поимённо (отдельный процесс не блокировал бы слияния,
# пока его имени нет в настройке репозитория, то есть вне дерева).
#
# Судится:
#   · заголовок — `#<N> `, у головы-номера N = голова (заголовок — акт ветки);
#     атрибуции нет;
#   · тело — без атрибуции;
#   · голова — номер задачи; исключения — `main` и ветка, открытая до правила
#     (в диапазоне запроса есть коммит с датой автора до T0);
#   · коммиты base..head — git_rule_judge_range: атрибуция у любого, форма у
#     коммитов после T0, номер ветки у слияний первой цепочки головы-номера;
#   · номера — каждый `#<N>` заголовка и первых строк коммитов после T0 (и `#<M>`
#     слияния) — задача ЭТОГО репозитория: GET $GITHUB_API_URL/repos/
#     $GITHUB_REPOSITORY/issues/<N> → 200, без поля `pull_request`, с
#     `repository_url` этого репозитория. 404, 410 — задачи нет; 301 — задача
#     перенесена; `pull_request` — это запрос, а не задача. Прочий ответ или
#     сбой — исход 2: номер не сверен, и это не зелёное. Замер контракта API —
#     в шапке пробы scripts/hooks/git-rule-inject.sh.
# Подпись здесь НЕ судится: корневой учётной записи в конвейере нет, её держат
# хуки. Сообщение серверного слияния задаётся после этой проверки и ею не
# судится.
#
# Вход — окружение: GITHUB_EVENT_NAME; у запроса ещё PR_TITLE, PR_BODY,
# HEAD_REF, BASE_SHA, HEAD_SHA (переменными, а не подстановкой в текст шага:
# это ввод автора запроса), GITHUB_REPOSITORY, GITHUB_API_URL (по умолчанию
# https://api.github.com), GH_TOKEN (необязателен).
#
# Исходы: 0 — нарушений нет либо событие не запрос (судить нечего, и это
# печатается); 1 — нарушения, названы поимённо; 2 — судить не смог (нет входа,
# мелкий клон, коммита нет в клоне, T0 не выведен, API не ответило).
set -uo pipefail

say() { printf '%s\n' "$@"; }
cant() { say "pr-rule: НЕ СУДИЛОСЬ — $*" >&2; exit 2; }

event="${GITHUB_EVENT_NAME:-}"
[ -n "$event" ] || cant "не задан GITHUB_EVENT_NAME — какое событие судить, неизвестно"
case "$event" in
    pull_request|pull_request_target) ;;
    *)
        say "== правило запроса: событие «$event» — запроса нет: заголовка, тела, головы и диапазона нет, судить нечего"
        exit 0
        ;;
esac
for v in PR_TITLE PR_BODY HEAD_REF BASE_SHA HEAD_SHA GITHUB_REPOSITORY; do
    [ -n "${!v+x}" ] || cant "не задан $v — судить нечего"
done
for t in git curl python3; do
    command -v "$t" >/dev/null 2>&1 || cant "нет $t в PATH — это инструмент самой проверки"
done

root="$(git rev-parse --show-toplevel 2>/dev/null)" || cant "это не рабочая копия git — диапазона запроса нет"
lib="$root/scripts/hooks/git-rule.sh"
[ -f "$lib" ] || cant "нет $lib — предиката правила нет"
# shellcheck source=../../scripts/hooks/git-rule.sh
. "$lib"

[ "$(git rev-parse --is-shallow-repository)" != true ] ||
    cant "клон мелкий — диапазон base..head неполон (нужен fetch-depth: 0)"
for s in "$BASE_SHA" "$HEAD_SHA"; do
    git cat-file -e "$s^{commit}" 2>/dev/null || cant "коммита «$s» в клоне нет — диапазон не построить"
done
git_rule_load_t0 "$HEAD_SHA" "$BASE_SHA"
[ "$GIT_RULE_T0_KNOWN" = 1 ] ||
    cant "T0 не выведен — коммита, заведшего $GIT_RULE_T0_PATH, в истории базы и головы нет: границы истории нет, судить коммиты до правила как новые было бы ложным красным"

head="$HEAD_REF"
range=("$HEAD_SHA" --not "$BASE_SHA")

if attr="$(git_rule_attribution "$PR_TITLE")"; then
    GIT_RULE_FINDINGS+=("заголовок: атрибуция «$attr»")
fi
if ! n="$(git_rule_subject_task "$PR_TITLE")"; then
    GIT_RULE_FINDINGS+=("заголовок не начинается с «#<N> »: «$PR_TITLE»")
else
    GIT_RULE_NUMBERS+=("$n")
    if git_rule_is_number "$head" && [ "$n" != "$head" ]; then
        GIT_RULE_FINDINGS+=("заголовок «#$n» у головы «$head»: заголовок — акт ветки, и номер у него её")
    fi
fi
if attr="$(git_rule_attribution "$PR_BODY")"; then
    GIT_RULE_FINDINGS+=("тело: атрибуция «$attr»")
fi
if [ "$head" != main ] && ! git_rule_is_number "$head"; then
    pre=0
    while IFS= read -r d; do
        [ -n "$d" ] && [ "$d" -lt "$GIT_RULE_T0" ] && { pre=1; break; }
    done < <(git log --no-color --format=%at "${range[@]}")
    [ "$pre" = 1 ] ||
        GIT_RULE_FINDINGS+=("голова «$head»: ветка называется номером задачи (^[0-9]+\$), исключения — main и ветка, открытая до правила")
fi
git_rule_judge_range "$head" - "${range[@]}" || cant "диапазон запроса не читается git"

# ── НОМЕРА — задачи этого репозитория ─────────────────────────────────────────
api="${GITHUB_API_URL:-https://api.github.com}"
api="${api%/}"
repo_url="/repos/$GITHUB_REPOSITORY"
tmp="$(mktemp)" || cant "нет временного файла для ответа API"
trap 'rm -f "$tmp"' EXIT
auth=()
[ -z "${GH_TOKEN:-}" ] || auth=(-H "Authorization: Bearer $GH_TOKEN")
declare -A asked=()
unasked=()
checked=0
for n in "${GIT_RULE_NUMBERS[@]}"; do
    [ -z "${asked[$n]:-}" ] || continue
    asked[$n]=1
    checked=$((checked + 1))
    code="$(curl -sS --max-time 20 --retry 2 --retry-delay 1 -o "$tmp" -w '%{http_code}' \
        -H 'Accept: application/vnd.github+json' "${auth[@]}" "$api$repo_url/issues/$n" 2>/dev/null)" || code="сбой curl"
    case "$code" in
        200)
            kind="$(python3 -c '
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print("разбор"); sys.exit(0)
if not isinstance(d, dict) or d.get("number") != int(sys.argv[2]):
    print("разбор")
elif "pull_request" in d:
    print("запрос")
elif not str(d.get("repository_url", "")).endswith(sys.argv[3]):
    print("чужой")
else:
    print("задача")
' "$tmp" "$n" "$repo_url")"
            case "$kind" in
                задача) ;;
                запрос) GIT_RULE_FINDINGS+=("#$n — запрос, а не задача: номер первой строки — задача этого репозитория") ;;
                чужой) GIT_RULE_FINDINGS+=("#$n — задача другого репозитория, а не $GITHUB_REPOSITORY") ;;
                *) unasked+=("#$n (ответ 200 не разобран)") ;;
            esac
            ;;
        404|410) GIT_RULE_FINDINGS+=("нет задачи #$n в $GITHUB_REPOSITORY (ответ $code)") ;;
        301) GIT_RULE_FINDINGS+=("задача #$n перенесена из $GITHUB_REPOSITORY (ответ 301)") ;;
        *) unasked+=("#$n (ответ $code)") ;;
    esac
done

say "== правило запроса (T0 $(git_rule_t0_text)): голова «$head», коммитов в диапазоне $GIT_RULE_SEEN, из них после T0 $GIT_RULE_AFTER_T0, номеров сверено с трекером $((checked - ${#unasked[@]})) из $checked"
say "   судилось: заголовок, тело, имя головы, сообщения коммитов, номера задач; подпись — нет (её держат хуки)"
if [ "${#GIT_RULE_FINDINGS[@]}" -gt 0 ]; then
    say "   нарушений: ${#GIT_RULE_FINDINGS[@]}"
    printf '     %s\n' "${GIT_RULE_FINDINGS[@]}"
    [ "${#unasked[@]}" -eq 0 ] || say "   и не сверено с трекером: ${unasked[*]}"
    exit 1
fi
if [ "${#unasked[@]}" -gt 0 ]; then
    say "pr-rule: НЕ СУДИЛОСЬ — номера не сверены с трекером ($api): ${unasked[*]}" >&2
    exit 2
fi
say "   нарушений нет"
exit 0
