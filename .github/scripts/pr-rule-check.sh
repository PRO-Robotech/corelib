#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Проверка ЗАПРОСА по правилу git. Предикат — scripts/hooks/git-rule.sh, общий с
# хуком коммита и стражем отправки. Хуки клиентские: их минуют --no-verify,
# непровязанный клон и правка через веб. Здесь — держатель, которого эти обходы
# не минуют; но даты коммита задаёт клиент, и граница по ним названа ниже
# («ГРАНИЦА»). Шаг стоит в задании «сборка · vet · gofmt» ci.yml, чей контекст
# защита `main` требует поимённо (отдельный процесс не блокировал бы слияния,
# пока его имени нет в настройке репозитория, то есть вне дерева).
#
# ГРАНИЦА — даты коммита, их задаёт клиент. Коммит до правила здесь — не только
# даты до T0, но и история без добавления правила (git_rule_pre_rule): коммит
# поверх правила судится целиком, какие бы даты он ни нёс. Коммит, записанный
# на основании СТАРШЕ правила с обеими датами до T0, от работы до правила не
# отличим ничем из данных git: его форма и имя головы не судятся (замер —
# утверждения «граница:» пробы scripts/hooks/git-rule-inject.sh).
#
# Судится:
#   · заголовок — `#<N> `, у головы-номера N = голова (заголовок — акт ветки);
#     атрибуции нет;
#   · тело — без атрибуции;
#   · голова — номер задачи; исключения — `main` и ветка, открытая до правила
#     (в диапазоне запроса есть коммит до правила — git_rule_pre_rule);
#   · коммиты base..head — git_rule_judge_range: атрибуция у любого, форма у
#     коммитов не до правила, номер ветки у слияний клиента первой цепочки
#     головы-номера;
#   · серверное слияние (шапка scripts/hooks/git-rule.sh, «СЕРВЕРНОЕ
#     СЛИЯНИЕ») — слияние с коммиттером площадки, которое площадка о себе
#     подтвердила: GET …/commits/<sha> (замер контракта — ниже, у
#     server_confirm). Его сообщение площадка составила ПОСЛЕ проверки его
#     запроса, и судится оно здесь — у следующего запроса;
#   · послабления поимённо — .github/scripts/pr-rule-exemptions.txt: запись
#     прощает одно нарушение одного серверного слияния, названа в выводе, и
#     запись, которой нечего исключать, — нарушение;
#   · номера — каждый `#<N>` заголовка и первых строк коммитов не до правила (и `#<M>`
#     слияния) — задача ЭТОГО репозитория: GET $GITHUB_API_URL/repos/
#     $GITHUB_REPOSITORY/issues/<N> → 200, без поля `pull_request`, с
#     `repository_url` этого репозитория. 404, 410 — задачи нет; 301 — задача
#     перенесена; `pull_request` — это запрос, а не задача. Прочий ответ или
#     сбой — исход 2: номер не сверен, и это не зелёное. Замер контракта API —
#     в шапке пробы scripts/hooks/git-rule-inject.sh.
# Подпись здесь НЕ судится: корневой учётной записи в конвейере нет, её держат
# хуки. Сообщение серверного слияния СВОЕГО запроса задаётся после этой
# проверки и ею не судится — его судит проверка следующего запроса, где оно
# уже в диапазоне.
#
# Вход — окружение: GITHUB_EVENT_NAME; у запроса ещё PR_TITLE, PR_BODY,
# HEAD_REF, BASE_SHA, HEAD_SHA (переменными, а не подстановкой в текст шага:
# это ввод автора запроса), GITHUB_REPOSITORY, GITHUB_API_URL (по умолчанию
# https://api.github.com), GH_TOKEN (необязателен).
#
# Исходы: 0 — нарушений нет либо событие не запрос (судить нечего, и это
# печатается); 1 — нарушения, названы поимённо; 2 — судить не смог (нет входа,
# мелкий клон, коммита нет в клоне, T0 не выведен, API не ответило, у записи
# послабления нет ссылки main для проверки истечения).
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
if [ "$head" != main ] && ! git_rule_is_number "$head" && ! git_rule_range_pre_rule "${range[@]}"; then
    GIT_RULE_FINDINGS+=("голова «$head»: ветка называется номером задачи (^[0-9]+\$), исключения — main и ветка, открытая до правила (её коммит записан до правила)")
fi
# ── API ПЛОЩАДКИ — трекер и коммиты ──────────────────────────────────────────
api="${GITHUB_API_URL:-https://api.github.com}"
api="${api%/}"
repo_url="/repos/$GITHUB_REPOSITORY"
tmp="$(mktemp)" || cant "нет временного файла для ответа API"
trap 'rm -f "$tmp"' EXIT
auth=()
[ -z "${GH_TOKEN:-}" ] || auth=(-H "Authorization: Bearer $GH_TOKEN")
unasked=()
# api_get <путь от репозитория> — код ответа; тело — в $tmp.
api_get() {
    local code
    code="$(curl -sS --max-time 20 --retry 2 --retry-delay 1 -o "$tmp" -w '%{http_code}' \
        -H 'Accept: application/vnd.github+json' "${auth[@]}" "$api$repo_url/$1" 2>/dev/null)" || code="сбой curl"
    printf '%s' "$code"
}

# ── СЕРВЕРНЫЕ СЛИЯНИЯ — подтверждает площадка ────────────────────────────────
# Кандидат (git_rule_server_candidates) — серверное слияние, когда площадка о
# нём отвечает: коммиттер — служебная запись web-flow, подпись проверена
# (verification.verified), адрес коммиттера — noreply@github.com, родителей
# два и больше. Замер 2026-09-24: 372daa90 — login web-flow, verified true,
# reason valid; слияние клиента c6d8b1b3 — login автора, verified false,
# reason unsigned; sha, которого на площадке нет, — 422. 404 и 422 — коммит
# площадкой не собран; прочий ответ — не сверено, и вердикта нет.
declare -A server_kind=()
# server_confirm <sha> — вид в server_kind[sha]: «площадка», «не площадка» либо
# «ответ <код>» (не сверено).
server_confirm() {
    local sha="$1" code
    [ -z "${server_kind[$sha]:-}" ] || return 0
    code="$(api_get "commits/$sha")"
    case "$code" in
        200)
            server_kind[$sha]="$(python3 -c '
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print("ответ 200 не разобран"); sys.exit(0)
def obj(v):
    return v if isinstance(v, dict) else {}
if not isinstance(d, dict) or d.get("sha") != sys.argv[2]:
    print("ответ 200 не разобран"); sys.exit(0)
commit = obj(d.get("commit"))
parents = d.get("parents") if isinstance(d.get("parents"), list) else []
server = (obj(d.get("committer")).get("login") == "web-flow"
          and obj(commit.get("verification")).get("verified") is True
          and obj(commit.get("committer")).get("email") == "noreply@github.com"
          and len(parents) >= 2)
print("площадка" if server else "не площадка")
' "$tmp" "$sha")"
            ;;
        404|422) server_kind[$sha]="не площадка" ;;
        *) server_kind[$sha]="ответ $code" ;;
    esac
}
candidates=0
servers=0
while IFS= read -r h; do
    [ -n "$h" ] || continue
    candidates=$((candidates + 1))
    server_confirm "$h"
    case "${server_kind[$h]}" in
        площадка) servers=$((servers + 1)); GIT_RULE_SERVER_MERGES+="$h"$'\n' ;;
        "не площадка") ;;
        # Не сверено: форма судится серверной, а вердикта нет — исход не 0.
        *) unasked+=("коммит ${h:0:10} (${server_kind[$h]})"); GIT_RULE_SERVER_MERGES+="$h"$'\n' ;;
    esac
done < <(git_rule_server_candidates "${range[@]}")

git_rule_judge_range "$head" - "${range[@]}" || cant "диапазон запроса не читается git"

# ── ПОСЛАБЛЕНИЯ ПОИМЁННО — .github/scripts/pr-rule-exemptions.txt ────────────
# Строка записи: `<sha полностью> <класс> <причина с предикатом снятия>`;
# пустые строки и строки с `#` в начале — не записи. Прощает запись ОДНО
# нарушение ОДНОГО коммита, и только серверного слияния: свой коммит клиент
# переписывает, а сообщение слияния площадки после слияния не переписать без
# --force. Класс — из словаря exempt_prefix (начало текста нарушения).
# Запись, которой нечего исключать, — нарушение: коммита нет в клоне либо он
# уже предок main (ни один запрос его больше не судит). Ведомости нет либо
# записей 0 — прощать нечего, и это цель, а не отказ.
declare -A exempt_prefix=([тело]="тело — ")
ledger_rel=.github/scripts/pr-rule-exemptions.txt
ledger="$root/$ledger_rel"
entries=0
forgiven=()
mains=()
while IFS= read -r ref; do mains+=("$ref"); done < <(git for-each-ref --format='%(refname)' refs/heads/main 'refs/remotes/*/main')
if [ -f "$ledger" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in '' | '#'*) continue ;; esac
        entries=$((entries + 1))
        read -r sha cls reason <<<"$line"
        if ! [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; then
            GIT_RULE_FINDINGS+=("запись послабления «$line»: первое поле — sha коммита полностью (40 знаков)")
            continue
        fi
        e="запись послабления ${sha:0:10}"
        if [ -z "${exempt_prefix[$cls]+x}" ]; then
            GIT_RULE_FINDINGS+=("$e: класс «$cls» не из словаря (${!exempt_prefix[*]})")
            continue
        fi
        if [ -z "$reason" ]; then
            GIT_RULE_FINDINGS+=("$e: без причины — причина и предикат снятия пишутся в той же строке")
            continue
        fi
        if ! git cat-file -e "$sha^{commit}" 2>/dev/null; then
            GIT_RULE_FINDINGS+=("$e — коммита нет в клоне: исключать нечего, запись снимается")
            continue
        fi
        [ "${#mains[@]}" -gt 0 ] || cant "ссылки main в клоне нет — истечение записей послабления ($ledger_rel) не проверить"
        landed=""
        for ref in "${mains[@]}"; do
            if git merge-base --is-ancestor "$sha" "$ref" 2>/dev/null; then landed="$ref"; break; fi
        done
        if [ -n "$landed" ]; then
            GIT_RULE_FINDINGS+=("$e — уже предок main ($landed): ни один запрос его больше не судит, запись снимается")
            continue
        fi
        server_confirm "$sha"
        case "${server_kind[$sha]}" in
            площадка) ;;
            "не площадка")
                GIT_RULE_FINDINGS+=("$e — не серверное слияние: свой коммит переписывается, а не прощается")
                continue
                ;;
            *)
                unasked+=("коммит ${sha:0:10} записи послабления (${server_kind[$sha]})")
                continue
                ;;
        esac
        pre="${sha:0:10} ${exempt_prefix[$cls]}"
        kept=()
        for f in "${GIT_RULE_FINDINGS[@]}"; do
            if [[ "$f" == "$pre"* ]]; then forgiven+=("${sha:0:10} $cls — $reason"); else kept+=("$f"); fi
        done
        GIT_RULE_FINDINGS=("${kept[@]}")
    done <"$ledger"
fi

# ── НОМЕРА — задачи этого репозитория ─────────────────────────────────────────
declare -A asked=()
checked=0
for n in "${GIT_RULE_NUMBERS[@]}"; do
    [ -z "${asked[$n]:-}" ] || continue
    asked[$n]=1
    checked=$((checked + 1))
    code="$(api_get "issues/$n")"
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
unasked_numbers=0
for u in "${unasked[@]}"; do [[ "$u" != '#'* ]] || unasked_numbers=$((unasked_numbers + 1)); done

say "== правило запроса (T0 $(git_rule_t0_text)): голова «$head», коммитов в диапазоне $GIT_RULE_SEEN, из них после правила (судимых формой) $GIT_RULE_AFTER_RULE, серверных слияний $servers из кандидатов $candidates, номеров сверено с трекером $((checked - unasked_numbers)) из $checked"
say "   судилось: заголовок, тело, имя головы, сообщения коммитов, номера задач; подпись — нет (её держат хуки)"
say "   записей послабления $entries ($ledger_rel), прощено нарушений ${#forgiven[@]}"
[ "${#forgiven[@]}" -eq 0 ] || printf '     послаблено: %s\n' "${forgiven[@]}"
if [ "${#GIT_RULE_FINDINGS[@]}" -gt 0 ]; then
    say "   нарушений: ${#GIT_RULE_FINDINGS[@]}"
    printf '     %s\n' "${GIT_RULE_FINDINGS[@]}"
    [ "${#unasked[@]}" -eq 0 ] || say "   и не сверено с API площадки: ${unasked[*]}"
    exit 1
fi
if [ "${#unasked[@]}" -gt 0 ]; then
    say "pr-rule: НЕ СУДИЛОСЬ — не сверено с API площадки ($api): ${unasked[*]}" >&2
    exit 2
fi
say "   нарушений нет"
exit 0
