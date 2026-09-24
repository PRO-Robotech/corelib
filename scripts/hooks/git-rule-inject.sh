#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# git-rule-inject.sh — проба правила git: его предиката (scripts/hooks/git-rule.sh)
# и трёх потребителей — хука коммита (scripts/hooks/commit-msg), стража
# отправки (scripts/hooks/git-rule-push.sh, его зовёт scripts/hooks/pre-push) и
# проверки запроса (.github/scripts/pr-rule-check.sh). Каждое свойство — парой:
# дефект → коммит не записан / ссылка не доехала / код не 0, причина названа;
# законный близнец, отличный в один факт, → записано, доехало, код 0. Судятся
# файлы ЭТОЙ рабочей копии, скопированные в синтетический клон во временном
# каталоге; коммиты — настоящие `git commit` и `git merge`, отправки — настоящий
# `git push`, всё сквозь переходники v2, которые кладёт install.sh (текст — у
# производителя, режим `install.sh stub`, а не своей копией).
#
# КОРНЕВАЯ УЧЁТНАЯ ЗАПИСЬ ПРОБЫ ЗАДАЁТСЯ HOME, а не GIT_CONFIG_GLOBAL: корень —
# это `~/.gitconfig`, а GIT_CONFIG_GLOBAL его перенаправляет, и такой коммит и
# такую отправку правило отвергает (это утверждения ниже).
#
# ОТПРАВКИ ПРОБЫ ИДУТ С CORELIB_SKIP_PREPUSH=1: обход снимает ПРОВЕРКИ дерева
# (сборку и пробы Go, которых в фикстуре нет), а страж правила идёт до него и им
# не снимается — это тоже утверждение ниже, и одна отправка идёт без обхода.
#
# API ТРЕКЕРА У ПРОВЕРКИ ЗАПРОСА — ПОДСТАВНОЙ сервер на 127.0.0.1. Его контракт —
# замер настоящего (2026-09-24, api.github.com, repos/PRO-Robotech/corelib/issues/<N>):
# задача → 200 с `repository_url` репозитория и без `pull_request`; номер
# запроса → 200 С полем `pull_request`; нет номера → 404 `{"message":"Not Found"}`.
# Сверх замера подставной отдаёт 410, 301 и 500 — ответы, которые API называет
# в документации (задача удалена, перенесена, сбой). Сеть проба не трогает.
#
# КОНТРОЛЬ — дерево без хука коммита (как ветка волны до corelib#25): тот же
# дефектный коммит там записывается. Без этого прогона нечем показать, что
# дефект воспроизводим, а не выдуман.
#
# ГРАНИЦЫ ИЗМЕРЕНЫ, А НЕ ОБЪЯВЛЕНЫ. Пути, которыми коммит минует commit-msg
# (--no-verify, cherry-pick, rebase, am, revert, commit-tree, --cleanup=strip в
# командной строке, новый коммит с автором и датой вершины до T0), проба
# исполняет и утверждает, что дефектный коммит записан: шапка хука называет их
# слепыми зонами, и это утверждение держится замером git этой машины.
#
# ИСХОДЫ: 0 — все утверждения сошлись; 1 — хоть одно разошлось;
#         2 — не выполнилось (нет файла под пробой, нет git/make/python3/curl,
#             фикстура не собрана, подставной API не встал, не проверено ни
#             одного утверждения).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tree="$(cd "$here/../.." && pwd -P)"
HOOK="$here/commit-msg"
RULE="$here/git-rule.sh"
INSTALL="$here/install.sh"
PREPUSH="$here/pre-push"
PUSHRULE="$here/git-rule-push.sh"
PRCHECK="$tree/.github/scripts/pr-rule-check.sh"
MAKEFILE="$tree/Makefile"
CI="$tree/.github/workflows/ci.yml"

void() { echo "git-rule-inject: НЕ ВЫПОЛНИЛОСЬ — $*" >&2; exit 2; }
for f in "$HOOK" "$RULE" "$INSTALL" "$PREPUSH" "$PUSHRULE" "$PRCHECK" "$MAKEFILE" "$CI"; do
    [ -f "$f" ] || void "нет $f"
done
for t in git make sed grep cmp seq mktemp python3 curl; do command -v "$t" >/dev/null 2>&1 || void "нет $t в PATH"; done

work="$(mktemp -d)" || void "нет временного каталога"
api_pid=""
trap '[ -z "$api_pid" ] || kill "$api_pid" 2>/dev/null; rm -rf "$work"' EXIT
work="$(cd "$work" && pwd -P)"

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR \
      GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_PREFIX GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM \
      GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL \
      GIT_AUTHOR_DATE GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL GIT_COMMITTER_DATE \
      GIT_EDITOR GIT_SEQUENCE_EDITOR EDITOR VISUAL MAKEFLAGS MAKELEVEL MFLAGS \
      CORELIB_SKIP_PREPUSH GITHUB_EVENT_NAME GITHUB_REPOSITORY GITHUB_API_URL GH_TOKEN \
      PR_TITLE PR_BODY HEAD_REF BASE_SHA HEAD_SHA
export HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" GIT_CONFIG_NOSYSTEM=1 \
       GIT_MERGE_AUTOEDIT=no GIT_TERMINAL_PROMPT=0
mkdir -p "$HOME" "$XDG_CONFIG_HOME"
root_name="probe-root"
root_mail="root@example.invalid"
cat > "$HOME/.gitconfig" <<CFG
[user]
	name = $root_name
	email = $root_mail
[init]
	defaultBranch = main
[commit]
	gpgsign = false
[advice]
	detachedHead = false
CFG
cp "$HOME/.gitconfig" "$work/root.gitconfig"

pass=0
fail=0
ok()  { pass=$((pass + 1)); echo "  сошлось    $1"; }
bad() { fail=$((fail + 1)); echo "  РАЗОШЛОСЬ  $1"; }
fact() { local name="$1"; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }
# shellcheck disable=SC2329  # зовётся через fact
not() { ! "$@"; }

# ── ФИКСТУРА ─────────────────────────────────────────────────────────────────
# h0 — история до правила: дата автора 2020 года, первая строка без номера,
#      подпись не корневая, в дереве нет scripts/ — это ревизия СТАРШЕ хука.
# h1 — коммит правила: заводит scripts/hooks; его время автора — T0.
F="$work/f"
mkdir -p "$F/scripts/hooks"
cp "$HOOK" "$RULE" "$INSTALL" "$PREPUSH" "$PUSHRULE" "$F/scripts/hooks/" || void "файлы под пробой не скопированы"
chmod +x "$F/scripts/hooks/commit-msg" "$F/scripts/hooks/pre-push"
echo 'фикстура пробы правила git' > "$F/README"
t0_iso="2026-01-01T00:00:00Z"
git -C "$F" init -q || void "фикстура не заведена"
if ! { git -C "$F" add README &&
    GIT_AUTHOR_NAME=old GIT_AUTHOR_EMAIL=old@example.invalid GIT_AUTHOR_DATE=2020-01-01T00:00:00Z \
    GIT_COMMITTER_NAME=old GIT_COMMITTER_EMAIL=old@example.invalid \
    GIT_COMMITTER_DATE=2020-01-01T00:00:00Z git -C "$F" commit -qm "fixture: история до правила"; }; then
    void "коммит истории фикстуры не собран"
fi
h0="$(git -C "$F" rev-parse HEAD)"
if ! { git -C "$F" add scripts &&
    GIT_AUTHOR_DATE="$t0_iso" GIT_COMMITTER_DATE="$t0_iso" git -C "$F" commit -qm "#1 правило git в хуках"; }; then
    void "коммит правила фикстуры не собран"
fi
h1="$(git -C "$F" rev-parse HEAD)"
git init -q --bare "$work/f.git" && git -C "$F" remote add origin "$work/f.git" || void "удалённый фикстуры не заведён"

echo "== провязка хуков (install.sh)"
runc() { out="$("$@" 2>&1 </dev/null)"; rc=$?; }
runc bash -c "cd '$F' && bash scripts/hooks/install.sh install"
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -qF "переходник провязан 2"; then
    ok "install провязывает commit-msg и pre-push переходниками v2 (код 0)"
else
    bad "install в фикстуре: код $rc"; printf '%s\n' "$out" | sed 's/^/      | /'
fi
FH="$F/.git/hooks"
[ -x "$FH/commit-msg" ] || void "переходник commit-msg не провязан — судить хук нечем"
[ -x "$FH/pre-push" ] || void "переходник pre-push не провязан — судить отправку нечем"
for hk in commit-msg pre-push; do
    fact "переходник $hk побайтно равен тексту производителя (install.sh stub)" \
        cmp -s "$FH/$hk" <(bash "$INSTALL" stub "$hk")
    fact "переходник $hk не советует --no-verify — обход проверок запрещён правилом" \
        not grep -qF -- "--no-verify" "$FH/$hk"
    fact "переходник $hk называет выход без обхода: слияние ревизии, где хук есть" \
        grep -qF "git merge <ветка с scripts/hooks/$hk>" "$FH/$hk"
done

# ── ПОПЫТКА КОММИТА ──────────────────────────────────────────────────────────
# attempt <команда…> — исполняет команду в фикстуре с окружением из массива E;
# код в $rc, вывод в $out, записан ли коммит — в $made (сменилась ли вершина).
E=()
attempt() {
    local before after
    before="$(git -C "$F" rev-parse -q --verify HEAD)"
    out="$(cd "$F" && env "${E[@]}" "$@" 2>&1 </dev/null)"
    rc=$?
    after="$(git -C "$F" rev-parse -q --verify HEAD)"
    made=0
    [ "$before" = "$after" ] || made=1
}
# has_all <образец…> — каждый образец есть в $out; недостающие — в $miss.
has_all() {
    local p
    miss=""
    for p in "$@"; do printf '%s' "$out" | grep -qF -- "$p" || miss="$miss «$p»"; done
    [ -z "$miss" ]
}
# refused <имя> <образец…> — коммит НЕ записан, код не 0, каждый образец в тексте.
refused() {
    local name="$1"
    shift
    has_all "$@"
    if [ "$rc" -ne 0 ] && [ "$made" = 0 ] && [ -z "$miss" ]; then ok "$name (отказ, код $rc)"
    else bad "$name: код $rc, записан $made; нет образцов:${miss:- —}"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
}
# accepted <имя> <первая строка> — коммит записан, код 0, первая строка записанного — эта.
accepted() {
    local name="$1" want="$2" got
    got="$(git -C "$F" log -1 --format=%s)"
    if [ "$rc" -eq 0 ] && [ "$made" = 1 ] && [ "$got" = "$want" ]; then ok "$name (записан, код 0)"
    else bad "$name: код $rc, записан $made, первая строка «$got», ждали «$want»"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
}
# on <ветка> [<вершина>] — фикстура на ветке, от названной вершины (по умолчанию h1).
on() {
    git -C "$F" merge --abort >/dev/null 2>&1
    git -C "$F" checkout -q -f -B "$1" "${2:-$h1}" || void "ветка $1 фикстуры не встала"
    git -C "$F" clean -qfdx -e scripts >/dev/null 2>&1
    E=()
}
commit() { attempt git commit -q --allow-empty "$@"; }
# tree_without <вершина> <путь> — дерево вершины без пути (собрано на своём индексе).
tree_without() {
    (cd "$F" && GIT_INDEX_FILE="$work/tw.idx" git read-tree "$1" &&
        GIT_INDEX_FILE="$work/tw.idx" git update-index --force-remove "$2" &&
        GIT_INDEX_FILE="$work/tw.idx" git write-tree)
}

# ── ПЕРВАЯ СТРОКА: #<N> ──────────────────────────────────────────────────────
echo "== первая строка «#<N> »"
on 7
commit -m "hooks: x"
refused "C1: «hooks: x» на ветке 7" "не начинается с «#<N> »" "«hooks: x»"
on 7; commit -m "#7 x"
accepted "близнец C1: «#7 x» на ветке 7" "#7 x"
for bad_subj in "#7x" "# 7 x" " #7 x" "#7"; do
    on 7; commit -m "$bad_subj"
    refused "форма «$bad_subj» — не «#<N> »" "не начинается с «#<N> »"
done
# T1 — форма пачки: ветка пачки — номер её первой задачи, коммит на задачу
# «#<N> …». Номер обычного коммита с именем ветки не сверяется; что N — задача
# этого репозитория, судит проверка запроса (ниже), а не хук.
on 7; commit -m "#8 x"
accepted "T1: «#8 x» на ветке 7 — форма пачки, законно" "#8 x"
on 25; commit -m "#58 гейт целевых веток"
accepted "T1: «#58 …» на ветке пачки 25" "#58 гейт целевых веток"
on 25; commit -m "#59 цепочка needs"
accepted "T1: «#59 …» на ветке пачки 25" "#59 цепочка needs"
on main; commit -m "#7 x"
accepted "ветка main — исключение, «#7 x» законно" "#7 x"
git -C "$F" checkout -q --detach "$h1"; E=()
commit -m "#7 x"
accepted "отсоединённая вершина — имени ветки нет, судится форма" "#7 x"

# ── ДЛИНА ПЕРВОЙ СТРОКИ: ≤72 СИМВОЛА, НЕ БАЙТА ───────────────────────────────
echo "== длина первой строки (символы, а не байты)"
ru() { local n="$1" s=""; for _ in $(seq "$n"); do s="${s}ж"; done; printf '%s' "$s"; }
s72="#7 $(ru 69)"
s73="#7 $(ru 70)"
on 7; commit -m "$s73"
refused "C3: 73 символа кириллицей — отказ, названы 73 и 72" "73" "72"
on 7; commit -m "$s72"
accepted "близнец C3: 72 символа кириллицей" "$s72"
on 7; E=(LC_ALL=C LANG=C); commit -m "$s72"
accepted "C4: 72 символа кириллицей под LC_ALL=C — счёт в символах (байтов 141)" "$s72"
on 7; E=(LC_ALL=C LANG=C); commit -m "$s73"
refused "C4: 73 символа под LC_ALL=C — отказ и там" "73"
s72l="#7 $(printf 'a%.0s' $(seq 69))"
on 7; commit -m "${s72l}a"
refused "73 символа латиницей — отказ" "73"
on 7; commit -m "$s72l"
accepted "72 символа латиницей" "$s72l"

# ── ОДНО УТВЕРЖДЕНИЕ; ПЕРВАЯ СТРОКА ОТДЕЛЕНА ОТ ТЕЛА ─────────────────────────
echo "== одно утверждение и отделённая первая строка"
on 7; commit -m "#7 снят x; заведён y"
refused "точка с запятой — второе утверждение" "точка с запятой"
on 7; commit -m "#7 снят x."
refused "точка в конце первой строки" "точка в конце"
on 7; commit -m "#7 снят x. Заведён y"
refused "второе предложение после точки" "второе предложение"
on 7; commit -m "#7 снят go.mod: версия 1.2 закреплена"
accepted "близнец: точка внутри имени и числа — не второе предложение" "#7 снят go.mod: версия 1.2 закреплена"
on 7; commit -m "#7 ветка — номер задачи"
accepted "граница: тире не судится (одно утверждение через тире от двух не отличить разбором)" "#7 ветка — номер задачи"
printf '#7 первая строка\nвторая строка без пустой\n' > "$work/glued.msg"
on 7; commit -F "$work/glued.msg"
refused "первая строка без пустой строки после неё — git склеит их в заголовок" "пустой строки"

# ── ТЕЛО: ≤12 СТРОК ПОСЛЕ git stripspace ─────────────────────────────────────
# Единица счёта — строка тела после `git stripspace`, СЧИТАЯ пустые строки между
# абзацами: так тело видит читатель `git log`. Первая строка и пустая после неё
# в счёт не входят; хвостовые пустые строки stripspace снимает.
echo "== тело (строки после git stripspace, пустые между абзацами — в счёте)"
on 7; commit -m "#7 x" -m "$(seq 13)"
refused "C5: тело из 13 строк — отказ" "13" "12"
on 7; commit -m "#7 x" -m "$(seq 12)"
accepted "близнец C5: тело из 12 строк" "#7 x"
on 7; commit -m "#7 x" -m "$(seq 6)" -m "$(seq 6)"
refused "единица счёта: 12 непустых + 1 пустая между абзацами = 13 — отказ" "13"
on 7; commit -m "#7 x" -m "$(seq 6)" -m "$(seq 5)"
accepted "единица счёта: 11 непустых + 1 пустая = 12" "#7 x"
{ printf '#7 x\n\n'; seq 12; printf '\n\n\n\n'; } > "$work/tail.msg"
on 7; commit -F "$work/tail.msg"
accepted "хвостовые пустые строки снимает stripspace — 12" "#7 x"

# ── АТРИБУЦИИ НЕТ: каждая форма — своим прогоном ─────────────────────────────
# Строки дефекта — ОДНИ И ТЕ ЖЕ для отказа и для близнеца «упоминание»: близнец
# кладёт ту же строку в середину строки тела, и отличие ровно одно — место.
echo "== атрибуция"
while IFS='|' read -r label trailer; do
    [ -n "$label" ] || continue
    on 7; commit -m "#7 x" -m "тело" -m "$trailer"
    refused "C6: $label" "атрибуция"
done <<'FORMS'
Co-Authored-By с Claude|Co-Authored-By: Claude Opus <noreply@example.invalid>
Co-authored-by в нижнем регистре (34bc810, 73c4a29)|Co-authored-by: Claude <noreply@example.invalid>
соавтор с адресом anthropic|Co-Authored-By: Someone <noreply@anthropic.com>
Claude-Session:|Claude-Session: session_01probe
«Generated with [Claude Code]»|Generated with [Claude Code](https://example.invalid)
«Generated with Claude Code» без скобок|generated with claude code
ссылка claude.ai/code|https://claude.ai/code/session_01probe
FORMS
on 7; commit -m "#7 x" -m "тело" -m "Co-authored-by: Иван Петров <ivan@example.org>"
accepted "близнец C6: соавтор-человек законен" "#7 x"
while IFS='|' read -r label trailer; do
    [ -n "$label" ] || continue
    on 7; commit -m "#7 x" -m "Снята строка шаблона $trailer — подставлялась по умолчанию"
    accepted "близнец C6: «$label» в середине строки — упоминание, а не трейлер" "#7 x"
done <<'MENTIONS'
Co-Authored-By с Claude|Co-Authored-By: Claude Opus <noreply@example.invalid>
Claude-Session:|Claude-Session: session_01probe
MENTIONS

# ── ПОДПИСЬ — КОРНЕВАЯ УЧЁТНАЯ ЗАПИСЬ ────────────────────────────────────────
echo "== подпись (корень — ~/.gitconfig пробы)"
on 7; commit -m "#7 x"
fact "близнец: автор и коммиттер — корневая учётная запись" \
    test "$(git -C "$F" log -1 --format='%an <%ae>|%cn <%ce>')" = "$root_name <$root_mail>|$root_name <$root_mail>"
on 7; commit --author="Other <other@example.invalid>" -m "#7 x"
refused "--author чужой — отказ" "автор «Other <other@example.invalid>»"
on 7; E=(GIT_AUTHOR_NAME=bot GIT_AUTHOR_EMAIL=bot@example.invalid); commit -m "#7 x"
refused "GIT_AUTHOR_* чужие — отказ" "автор «bot <bot@example.invalid>»"
# Коммиттер, отличный от корня, при КОРНЕВОМ user.*: сам корень несёт
# committer.email. Переопределения уровня нет (уровень — global), и ловит его
# ровно сравнение коммиттера с корнем.
printf '[committer]\n\temail = bot@example.invalid\n' >> "$HOME/.gitconfig"
on 7; commit -m "#7 x"
refused "коммиттер из committer.email корня — отказ сравнением" "коммиттер «$root_name <bot@example.invalid>»"
cp "$work/root.gitconfig" "$HOME/.gitconfig"
on 7; attempt git -c user.name="$root_name" -c user.email="$root_mail" commit -q --allow-empty -m "#7 x"
refused "-c user.* с КОРНЕВЫМИ значениями — отказ при любом значении" "command:user.email"
on 7; attempt git -c committer.email="$root_mail" commit -q --allow-empty -m "#7 x"
refused "-c committer.email с КОРНЕВЫМ значением — отказ при любом значении" "command:committer.email"
on 7; attempt git -c author.email="$root_mail" commit -q --allow-empty -m "#7 x"
refused "-c author.email с КОРНЕВЫМ значением — отказ при любом значении" "command:author.email"
on 7; git -C "$F" config --local user.email "$root_mail"; commit -m "#7 x"
refused "--local user.email — отказ при любом значении" "local:user.email"
git -C "$F" config --local --unset user.email
on 7; git -C "$F" config --local committer.name bot; commit -m "#7 x"
refused "--local committer.name — отказ: назван уровень и коммиттер" "local:committer.name" "коммиттер «bot <$root_mail>»"
git -C "$F" config --local --unset committer.name
printf '[committer]\n\temail = %s\n' "$root_mail" > "$work/sys-committer"
on 7; E=(-u GIT_CONFIG_NOSYSTEM GIT_CONFIG_SYSTEM="$work/sys-committer"); commit -m "#7 x"
refused "committer.email уровня system — старше корневого user.*: отказ" "system:committer.email"
printf '[user]\n\temail = other@example.invalid\n' > "$work/sys-user"
on 7; E=(-u GIT_CONFIG_NOSYSTEM GIT_CONFIG_SYSTEM="$work/sys-user"); commit -m "#7 x"
accepted "близнец: user.email уровня system ниже корня — не переопределение" "#7 x"
on 7; E=(GIT_COMMITTER_NAME="$root_name"); commit -m "#7 x"
refused "GIT_COMMITTER_NAME — отказ при любом значении" "GIT_COMMITTER_NAME"
on 7; E=(GIT_CONFIG_GLOBAL="$work/root.gitconfig"); commit -m "#7 x"
refused "GIT_CONFIG_GLOBAL перенаправляет корень — отказ" "GIT_CONFIG_GLOBAL"
mkdir -p "$work/nohome"
on 7; E=(HOME="$work/nohome" XDG_CONFIG_HOME="$work/nohome/.config" GIT_AUTHOR_NAME=x GIT_AUTHOR_EMAIL=x@example.invalid GIT_COMMITTER_NAME=x GIT_COMMITTER_EMAIL=x@example.invalid); commit -m "#7 x"
refused "корневая учётная запись не задана — отказ" "корневая учётная запись не задана"

# ── РЕДАКТОР: строка «#…» вырезается как комментарий ─────────────────────────
echo "== коммит через редактор (знак комментария — тот, что возьмёт git)"
# Редактор пишет первую строку и тело: без хука git вырежет «#7 x» и запишет
# коммит с первой строкой «тело» — дефект, а не пустое сообщение.
# shellcheck disable=SC2016  # $1 — аргумент редактора, раскрывается им, а не здесь
printf '#!/bin/sh\nprintf "#7 x\\n\\nтело\\n" > "$1"\n' > "$work/editor.sh"
chmod +x "$work/editor.sh"
edit() { E=(GIT_EDITOR="$work/editor.sh"); attempt git "$@" commit -q --allow-empty; }
on 7; edit
refused "коммит через редактор — отказ до того, как git вырежет «#7 x»" "через редактор"
on 7; git -C "$F" config core.commentChar ';'; edit
accepted "близнец: редактор при core.commentChar=; — «#» не комментарий" "#7 x"
git -C "$F" config --unset core.commentChar
on 7; git -C "$F" config core.commentChar auto; edit
refused "core.commentChar=auto и редактор — знак выбирает git, «#7 x» он вырезает: отказ" "при core.commentChar=auto"
git -C "$F" config --unset core.commentChar
on 7; edit -c core.commentChar=auto
refused "-c core.commentChar=auto и редактор — отказ" "при core.commentChar=auto"
# Оба ключа задают один знак, и git берёт ПОСЛЕДНИЙ прочитанный (замер: ';' затем
# '#' — вырезается «#»). Хук, читающий ключи в своём порядке, взял бы не тот.
on 7; git -C "$F" config core.commentString ';'; git -C "$F" config core.commentChar '#'; edit
refused "commentString=; затем commentChar=# — git берёт «#»: отказ" "через редактор"
git -C "$F" config --unset core.commentString; git -C "$F" config --unset core.commentChar
on 7; git -C "$F" config core.commentChar '#'; git -C "$F" config core.commentString ';'; edit
accepted "близнец: commentChar=# затем commentString=; — git берёт «;», «#7 x» цел" "#7 x"
git -C "$F" config --unset core.commentString; git -C "$F" config --unset core.commentChar
on 7; git -C "$F" config commit.cleanup strip; commit -m "#7 x" -m "тело"
refused "commit.cleanup=strip вырежет «#7 x» и при -m — отказ" "commit.cleanup"
on 7; git -C "$F" config commit.cleanup whitespace; commit -m "#7 x" -m "тело"
accepted "близнец: commit.cleanup=whitespace" "#7 x"
git -C "$F" config --unset commit.cleanup

# ── СЛИЯНИЕ: настоящий git merge ─────────────────────────────────────────────
echo "== слияние (git merge --no-ff)"
on 8; echo 8 > "$F/eight.txt"; git -C "$F" add eight.txt; commit -m "#8 предмет восьмой"
accepted "ветка 8: свой коммит" "#8 предмет восьмой"
h8="$(git -C "$F" rev-parse HEAD)"
merge() { attempt git merge -q --no-ff "$@"; }
on 7; merge 8 -m "#7 merge #8: предмет восьмой"
accepted "слияние по форме «#7 merge #8: …» на ветке 7" "#7 merge #8: предмет восьмой"
fact "записанное слияние — коммит с двумя родителями" \
    test "$(git -C "$F" log -1 --format=%P | wc -w)" -eq 2
on 7; merge 8 -m "#7 merge main: сверка со стволом"
accepted "слияние по форме «#7 merge main: …»" "#7 merge main: сверка со стволом"
on 7; merge 8 -m "#7 предмет восьмой"
refused "слияние без «merge #<M>: …» — отказ" "слияние"
on 7; merge 8 -m "#9 merge #8: предмет восьмой"
refused "слияние на ветке 7 с номером 9 — отказ: слияние — акт ветки, номер её" "«#9» на ветке «7»"
on 7; merge 8 -m "#7 merge #8:"
refused "слияние без текста после двоеточия — отказ" "слияние"
on 7; merge 8
refused "слияние с сообщением git по умолчанию («Merge branch …») — отказ" "Merge branch"
on 7; merge 8 -m "#7 merge #8: предмет" -m "Co-Authored-By: Claude <noreply@example.invalid>"
refused "слияние с атрибуцией — отказ" "атрибуция"

# ── ИМЯ ВЕТКИ: номер задачи; исключение одно — main ──────────────────────────
echo "== имя ветки"
on issue-7; commit -m "#7 x"
refused "ветка «issue-7», открытая после правила, — отказ" "ветка «issue-7»" "номером задачи"
on lane/oauth2-engine; commit -m "#7 x"
refused "ветка «lane/oauth2-engine» после правила — отказ" "ветка «lane/oauth2-engine»"
# Ветка до правила: её собственный коммит (не достижимый ни со ствола, ни с
# веток-номеров) датирован до T0. Собран commit-tree — хук на нём не исполняется.
old_c="$(GIT_AUTHOR_DATE=2025-06-01T00:00:00Z GIT_COMMITTER_DATE=2025-06-01T00:00:00Z \
    git -C "$F" commit-tree -p "$h1" -m "lane: работа до правила" "$h1^{tree}")" || void "коммит ветки до правила не собран"
on batch-quota-fate "$old_c"; commit -m "#7 x"
accepted "близнец: ветка до правила (свой коммит датирован до T0) — не переименовывается" "#7 x"
on 7 "$old_c"; on issue-8 "$old_c"; commit -m "#8 x"
refused "ветка «issue-8» от вершины, лежащей на ветке-номере, — отказ" "ветка «issue-8»"
git -C "$F" branch -q -D 7 batch-quota-fate issue-7 issue-8 lane/oauth2-engine 2>/dev/null

# ── РЕВИЗИЯ СТАРШЕ ПРАВИЛА (R8): отказ без обхода, выход — слияние ───────────
# Переходник провязан в общем каталоге клона и видит ВСЕ рабочие копии, а в
# дереве, открытом до правила, хука нет. Отказ остаётся (#24: ненайденный
# адресат зелёным не бывает), но выход, который он называет, законен: внести
# правило слиянием. Слияние судит сам внесённый хук, и граница истории T0
# берётся у обоих родителей — иначе ветка до правила на этом слиянии читалась бы
# веткой после правила.
echo "== ревизия старше правила"
pre_c="$(GIT_AUTHOR_DATE=2025-06-01T00:00:00Z GIT_COMMITTER_DATE=2025-06-01T00:00:00Z \
    git -C "$F" commit-tree -p "$h0" -m "lane: работа до правила" "$h0^{tree}")" || void "коммит до правила не собран"
on batch-quota-fate "$pre_c"
fact "фикстура: в рабочей копии ревизии до правила хука нет" test ! -e "$F/scripts/hooks/commit-msg"
commit -m "#57 x"
refused "R8: коммит в ревизии старше хука — отказ, проверок не было" "ОТКАЗ — проверок НЕ БЫЛО" "git merge <ветка с scripts/hooks/commit-msg>"
fact "R8: отказ не называет --no-verify" not grep -qF -- "--no-verify" <<<"$out"
merge "$h1" -m "#57 merge main: правило git в ветке до правила"
accepted "R8: слияние, вносящее правило, судит внесённый хук и записано" "#57 merge main: правило git в ветке до правила"
commit -m "#57 x"
accepted "R8: после слияния коммит ветки до правила записан" "#57 x"
on batch-quota-fate "$pre_c"; merge "$h1" -m "hooks: merge"
refused "R8: слияние с дефектной первой строкой судит внесённый хук — отказ" "не начинается с «#<N> »"
on 41 "$pre_c"; merge "$h1" -m "#41 merge main: правило git в ветке"
accepted "R8: ветка-номер до правила — слияние по форме записано" "#41 merge main: правило git в ветке"
git -C "$F" branch -q -D batch-quota-fate 41 2>/dev/null

# ── T0: граница истории ──────────────────────────────────────────────────────
echo "== T0 (наименьшее время автора коммита, заводившего scripts/hooks/commit-msg)"
on 7; E=(GIT_AUTHOR_DATE=2020-06-01T00:00:00Z); commit -m "#7 x"
refused "новый коммит с датой автора до T0 — отказ: дата в прошлом правило не обходит" "раньше T0"
# Перепись сообщения коммита до T0: вершина — коммит с датой автора до T0 поверх
# коммита правила (так приходит перенесённая старая работа); --amend сохраняет
# автора и дату, форма и автор не судятся, атрибуция и коммиттер — судятся.
hist_c="$(GIT_AUTHOR_NAME=old GIT_AUTHOR_EMAIL=old@example.invalid GIT_AUTHOR_DATE=2025-01-01T00:00:00Z \
    git -C "$F" commit-tree -p "$h1" -m "lane: старая работа" "$h1^{tree}")" || void "коммит истории не собран"
on 7 "$hist_c"; commit --amend -m "lane: старая работа, сообщение переписано"
accepted "близнец: перепись сообщения коммита до T0 — форма и автор не судятся" "lane: старая работа, сообщение переписано"
on 7 "$hist_c"; commit --amend -m "lane: старая работа" -m "Co-Authored-By: Claude <noreply@example.invalid>"
refused "перепись до T0 с атрибуцией — отказ: атрибуция судится у любого" "атрибуция"
on 7 "$hist_c"; E=(GIT_COMMITTER_NAME=bot GIT_COMMITTER_EMAIL=bot@example.invalid); commit --amend -m "lane: старая работа"
refused "перепись до T0 чужим коммиттером — отказ" "GIT_COMMITTER_NAME"
# Два добавления: хук правила снят и заведён снова. T0 — первое добавление:
# коммит с датой между ними — после правила, судится целиком и записан.
rm_tree="$(tree_without "$h1" scripts/hooks/commit-msg)" || void "дерево без хука не собрано"
rm_c="$(GIT_AUTHOR_DATE=2026-02-01T00:00:00Z GIT_COMMITTER_DATE=2026-02-01T00:00:00Z \
    git -C "$F" commit-tree -p "$h1" -m "#1 хук снят" "$rm_tree")" || void "коммит снятия хука не собран"
re_c="$(GIT_AUTHOR_DATE=2026-03-01T00:00:00Z GIT_COMMITTER_DATE=2026-03-01T00:00:00Z \
    git -C "$F" commit-tree -p "$rm_c" -m "#1 хук заведён снова" "$h1^{tree}")" || void "коммит повторного добавления не собран"
fact "фикстура: у ревизии два добавления хука" \
    test "$(git -C "$F" log --diff-filter=A --format=%H "$re_c" -- scripts/hooks/commit-msg | wc -l)" -eq 2
on 7 "$re_c"; E=(GIT_AUTHOR_DATE=2026-02-15T00:00:00Z); commit -m "#7 между добавлениями"
accepted "T0 — первое добавление: коммит между двумя добавлениями — после правила" "#7 между добавлениями"
on 7 "$re_c"; E=(GIT_AUTHOR_DATE=2025-12-15T00:00:00Z); commit -m "#7 до первого добавления"
refused "близнец: коммит до первого добавления — раньше T0" "раньше T0"
# Граница мелкого клона показывает все свои файлы добавленными: T0 с неё не
# берётся. Клон глубины 1 на коммите ПОСЛЕ правила: новый коммит с датой между
# настоящим T0 и границей — не «дата раньше T0», а коммит, судимый целиком.
on 7; E=(GIT_AUTHOR_DATE=2026-03-01T00:00:00Z GIT_COMMITTER_DATE=2026-03-01T00:00:00Z); commit -m "#7 после правила"
accepted "коммит после правила — вершина мелкого клона" "#7 после правила"
git -C "$F" branch -q -f shallow-tip HEAD
S="$work/shallow"
git clone -q --depth 1 --branch shallow-tip "file://$F" "$S" 2>/dev/null || void "мелкий клон не собран"
git -C "$S" checkout -q -b 7
(cd "$S" && bash scripts/hooks/install.sh install) >/dev/null 2>&1 || void "мелкий клон не провязан"
before="$(git -C "$S" rev-parse HEAD)"
out="$(cd "$S" && GIT_AUTHOR_DATE=2026-02-01T00:00:00Z git commit -q --allow-empty -m "#7 x" 2>&1 </dev/null)"; rc=$?
made=0; [ "$(git -C "$S" rev-parse HEAD)" = "$before" ] || made=1
if [ "$rc" -eq 0 ] && [ "$made" = 1 ]; then ok "мелкий клон: T0 с границы не берётся — коммит судится целиком и записан"
else bad "мелкий клон: T0 взят с границы — код $rc"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
out="$(cd "$S" && git commit -q --allow-empty -m "hooks: x" 2>&1 </dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF "T0 не выведен"; then ok "мелкий клон: отказ называет, что T0 не выведен"
else bad "мелкий клон: код $rc, «T0 не выведен» не сказано"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
# Мелкий клон, чья граница файла правила НЕ несёт: хук снят до границы и заведён
# после. Добавление в видимой истории одно — повторное, позже настоящего T0;
# взятое за T0, оно сделало бы коммит между добавлениями «датой раньше T0».
git -C "$F" branch -q -f readd-tip "$re_c"
S2="$work/shallow2"
git clone -q --depth 2 --branch readd-tip "file://$F" "$S2" 2>/dev/null || void "второй мелкий клон не собран"
git -C "$S2" checkout -q -b 7
(cd "$S2" && bash scripts/hooks/install.sh install) >/dev/null 2>&1 || void "второй мелкий клон не провязан"
fact "фикстура: граница мелкого клона — коммит снятия, файла правила на ней нет" \
    bash -c "[ \"\$(git -C '$S2' rev-parse HEAD~1)\" = '$rm_c' ] && ! git -C '$S2' cat-file -e '$rm_c:scripts/hooks/commit-msg' 2>/dev/null"
before="$(git -C "$S2" rev-parse HEAD)"
out="$(cd "$S2" && GIT_AUTHOR_DATE=2026-02-15T00:00:00Z git commit -q --allow-empty -m "#7 между добавлениями" 2>&1 </dev/null)"; rc=$?
made=0; [ "$(git -C "$S2" rev-parse HEAD)" = "$before" ] || made=1
if [ "$rc" -eq 0 ] && [ "$made" = 1 ]; then ok "мелкий клон без файла на границе: T0 не выведен, коммит между добавлениями судится целиком и записан"
else bad "мелкий клон без файла на границе: T0 взят с повторного добавления — код $rc"; printf '%s\n' "$out" | sed 's/^/      | /'; fi

# ── ПЕРЕХОДНИК И АДРЕСАТ ─────────────────────────────────────────────────────
echo "== переходник: адресата нет — отказ"
on 7
mv "$F/scripts/hooks/commit-msg" "$work/cm.aside"
commit -m "#7 x"
refused "адресат снят с диска — коммит остановлен, проверок не было, выход назван" \
    "ОТКАЗ — проверок НЕ БЫЛО" "git checkout -- scripts/hooks/commit-msg"
mv "$work/cm.aside" "$F/scripts/hooks/commit-msg"
mv "$F/scripts/hooks/git-rule.sh" "$work/rule.aside"
commit -m "#7 x"
refused "предиката нет рядом с хуком — отказ, а не молчаливый пропуск" "git-rule.sh"
mv "$work/rule.aside" "$F/scripts/hooks/git-rule.sh"

# ── ОТПРАВКА: страж правила в pre-push, настоящий git push ───────────────────
# Дефектные коммиты собираются мимо хука коммита (--no-verify) — так они и
# приходят к отправке. Каждый дефект — на своей ветке-номере от коммита правила:
# отказанная ссылка не доезжает, и следующий случай судится на чистом месте.
echo "== отправка (scripts/hooks/git-rule-push.sh сквозь pre-push)"
P=()
push() { attempt env CORELIB_SKIP_PREPUSH=1 "${P[@]}" git push -q origin "$@"; }
remote_at() { git -C "$F" ls-remote origin "$1" | cut -f1; }
# delivered <имя> <ссылка> <образец…> — код 0, ссылка на удалённом = вершина HEAD.
delivered() {
    local name="$1" ref="$2" want
    shift 2
    want="$(git -C "$F" rev-parse HEAD)"
    has_all "$@"
    if [ "$rc" -eq 0 ] && [ "$(remote_at "$ref")" = "$want" ] && [ -z "$miss" ]; then ok "$name (доехала, код 0)"
    else bad "$name: код $rc, на удалённом «$(remote_at "$ref")», ждали $want; нет образцов:${miss:- —}"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
}
# stopped <имя> <ссылка> <образец…> — код не 0, ссылки на удалённом нет.
stopped() {
    local name="$1" ref="$2"
    shift 2
    has_all "$@"
    if [ "$rc" -ne 0 ] && [ -z "$(remote_at "$ref")" ] && [ -z "$miss" ]; then ok "$name (остановлена, код $rc)"
    else bad "$name: код $rc, на удалённом «$(remote_at "$ref")»; нет образцов:${miss:- —}"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
}
nv() { attempt git commit -q --allow-empty --no-verify "$@"; }
on main "$h1"; P=(); push main
delivered "законная отправка main: история до T0 и коммит правила" refs/heads/main "правило git" "нарушений нет"
on 101; commit -m "#101 x"; push 101
delivered "законная отправка ветки-номера" refs/heads/101 "новых коммитов 1"
on 102; nv -m "hooks: x"; push 102
stopped "первая строка без «#<N> » в отправке — отказ до проверок и до обхода" refs/heads/102 \
    "не начинается с «#<N> »" "правило git нарушено"
fact "обход CORELIB_SKIP_PREPUSH стража правила не снимает" not grep -qF "пропущен по CORELIB_SKIP_PREPUSH" <<<"$out"
attempt git push -q origin 102
stopped "та же отправка без обхода — отказ до проверок дерева" refs/heads/102 "правило git нарушено" "исполнено проверок: 0"
on 103; nv -m "#103 x" -m "Co-authored-by: Claude <noreply@example.invalid>"; push 103
stopped "атрибуция в отправляемом коммите — отказ" refs/heads/103 "атрибуция"
on 104; nv --author="Other <other@example.invalid>" -m "#104 x"; push 104
stopped "чужой автор в отправляемом коммите — отказ" refs/heads/104 "автор «Other <other@example.invalid>»"
on 105; E=(GIT_COMMITTER_NAME=bot GIT_COMMITTER_EMAIL=bot@example.invalid); nv -m "#105 x"; E=(); push 105
stopped "чужой коммиттер в отправляемом коммите — отказ" refs/heads/105 "коммиттер «bot <bot@example.invalid>»"
on issue-106; nv -m "#106 x"; push issue-106
stopped "новая ветка «issue-106» — отказ по имени" refs/heads/issue-106 "ветка «issue-106»"
on 25; commit -m "#58 гейт целевых веток"; push 25
delivered "T1: «#58 …» на ветке пачки 25 — доехала" refs/heads/25
on 107; attempt git merge -q --no-ff --no-verify "$h8" -m "#9 merge #8: предмет восьмой"; push 107
stopped "слияние «#9» на ветке 107 — отказ: номер слияния — номер ветки" refs/heads/107 "«#9» на ветке «107»"
on 108; merge "$h8" -m "#108 merge #8: предмет восьмой"; push 108
delivered "близнец: слияние «#108 merge #8» на ветке 108 — влитый «#8 …» судится формой" refs/heads/108 "новых коммитов 2"
on batch-quota-fate "$old_c"; commit -m "#57 x"; push batch-quota-fate
delivered "новая ветка до правила (свой коммит до T0) — имя законно" refs/heads/batch-quota-fate
on 109; commit -m "#109 x"; P=(GIT_CONFIG_GLOBAL="$work/root.gitconfig"); push 109
stopped "GIT_CONFIG_GLOBAL при отправке — корень перенаправлен: отказ" refs/heads/109 "GIT_CONFIG_GLOBAL"
P=(HOME="$work/nohome" XDG_CONFIG_HOME="$work/nohome/.config"); push 109
stopped "корень не задан при отправке — судить подпись не с чем: отказ" refs/heads/109 "корневая учётная запись не задана"
P=()
on 110; nv -m "hooks: опубликован мимо стража"
attempt git push -q --no-verify origin 110
fact "фикстура: дефектный коммит опубликован мимо стража (--no-verify)" test -n "$(remote_at refs/heads/110)"
on 111 110; commit -m "#111 поверх опубликованного"; push 111
delivered "опубликованный коммит не судится повторно — судится новый" refs/heads/111 "новых коммитов 1"
push :110
fact "снятие ссылки не судится — снято" test -z "$(remote_at refs/heads/110)"
on 112; nv -m "hooks: метка"; push "HEAD:refs/tags/t112"
stopped "метка на неопубликованном дефектном коммите — отказ: коммит уехал бы" refs/tags/t112 "не начинается с «#<N> »"
on 113; commit -m "#113 x"
mv "$F/scripts/hooks/git-rule-push.sh" "$work/push.aside"
push 113
stopped "стража правила нет рядом с pre-push — отказ, а не молчаливый пропуск" refs/heads/113 "git-rule-push.sh: правило git судить нечем"
mv "$work/push.aside" "$F/scripts/hooks/git-rule-push.sh"
out="$(cd "$S" && printf 'refs/heads/7 %s refs/heads/7 %s\n' "$(git rev-parse HEAD)" 0000000000000000000000000000000000000000 |
    bash scripts/hooks/git-rule-push.sh origin 2>&1)"; rc=$?
if printf '%s' "$out" | grep -qF "T0 не выведен"; then ok "мелкий клон: страж отправки называет, что T0 не выведен (код $rc)"
else bad "мелкий клон: страж отправки не сказал «T0 не выведен» (код $rc)"; printf '%s\n' "$out" | sed 's/^/      | /'; fi

# ── ЗАПРОС: .github/scripts/pr-rule-check.sh ─────────────────────────────────
echo "== проверка запроса (подставной API трекера)"
cat > "$work/api.py" <<'PY'
import http.server, json, sys
port_file, log_file = sys.argv[1], sys.argv[2]
REPO = "/repos/probe/corelib/issues/"
ISSUES = {1, 7, 8, 25, 26, 32, 41, 57, 58, 59}
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(log_file, "a") as f:
            f.write("%s %s\n" % (self.path, self.headers.get("Authorization") or "-"))
        code, body = 404, {"message": "Not Found"}
        if self.path.startswith(REPO) and self.path[len(REPO):].isdigit():
            n = int(self.path[len(REPO):])
            url = "https://api.example.invalid/repos/probe/corelib"
            if n in ISSUES:
                code, body = 200, {"number": n, "repository_url": url}
            elif n == 46:
                code, body = 200, {"number": n, "repository_url": url, "pull_request": {"url": url + "/pulls/46"}}
            elif n == 4100:
                code, body = 410, {"message": "This issue was deleted"}
            elif n == 3010:
                code, body = 301, {"message": "Moved Permanently"}
            elif n == 5000:
                code, body = 500, {"message": "Server Error"}
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def log_message(self, *a):
        pass
srv = http.server.HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
srv.serve_forever()
PY
python3 "$work/api.py" "$work/api.port" "$work/api.log" >/dev/null 2>&1 &
api_pid=$!
for _ in $(seq 100); do [ -s "$work/api.port" ] && break; sleep 0.05; done
[ -s "$work/api.port" ] || void "подставной API не встал"
api_port="$(cat "$work/api.port")"
: > "$work/api.log"
# pr <событие> <заголовок> <тело> <голова> <база> <вершина> [<клон>] — проверка запроса.
pr() {
    out="$(cd "${7:-$F}" && env -i PATH="$PATH" HOME="$HOME" XDG_CONFIG_HOME="$XDG_CONFIG_HOME" \
        GIT_CONFIG_NOSYSTEM=1 GITHUB_EVENT_NAME="$1" PR_TITLE="$2" PR_BODY="$3" HEAD_REF="$4" \
        BASE_SHA="$5" HEAD_SHA="$6" GITHUB_REPOSITORY=probe/corelib \
        GITHUB_API_URL="http://127.0.0.1:$api_port" GH_TOKEN=probe-token bash "$PRCHECK" 2>&1)"
    rc=$?
}
verdict() {
    local name="$1" want="$2"
    shift 2
    has_all "$@"
    if [ "$rc" -eq "$want" ] && [ -z "$miss" ]; then ok "$name (код $rc)"
    else bad "$name: код $rc, ждали $want; нет образцов:${miss:- —}"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
}
# Ветки запроса: 25 — законная пачка (#25, #58, слияние #25 merge #8). У 26
# база — вершина пачки, и диапазон несёт ровно один коммит — дефект случая;
# прочие — по одному факту.
on 25; commit -m "#25 хук коммита"; commit -m "#58 гейт целевых веток"; merge "$h8" -m "#25 merge #8: предмет восьмой"
pr25="$(git -C "$F" rev-parse HEAD)"
pr pull_request "#25 хуки и гейты" "Тело запроса. Снята строка шаблона Claude-Session: session_01probe — подставлялась." 25 "$h1" "$pr25"
verdict "законный запрос пачки 25: #25, #58, слияние #25 merge #8, упоминание в теле" 0 "нарушений нет"
fact "проверка запроса спрашивала API трекера о задачах 25, 58, 8 с ключом" \
    bash -c "grep -qx '/repos/probe/corelib/issues/25 Bearer probe-token' '$work/api.log' &&
             grep -qx '/repos/probe/corelib/issues/58 Bearer probe-token' '$work/api.log' &&
             grep -qx '/repos/probe/corelib/issues/8 Bearer probe-token' '$work/api.log'"
pr pull_request "хуки и гейты" "" 25 "$h1" "$pr25"
verdict "заголовок без «#<N> » — отказ" 1 "заголовок не начинается с «#<N> »"
pr pull_request "#26 хуки и гейты" "" 25 "$h1" "$pr25"
verdict "заголовок «#26» у головы 25 — отказ: заголовок — акт ветки" 1 "заголовок «#26» у головы «25»"
pr pull_request "#25 хуки и гейты, Generated with Claude Code" "" 25 "$h1" "$pr25"
verdict "заголовок с атрибуцией — отказ" 1 "заголовок: атрибуция"
pr pull_request "#25 хуки и гейты" "Тело."$'\n\n'"Co-Authored-By: Claude <noreply@example.invalid>" 25 "$h1" "$pr25"
verdict "тело с трейлером атрибуции — отказ" 1 "тело: атрибуция"
pr pull_request "#25 хуки и гейты" "" issue-25 "$h1" "$pr25"
verdict "голова «issue-25» без коммита до T0 — отказ по имени" 1 "голова «issue-25»"
on batch-quota-fate "$old_c"; commit -m "#57 x"
bq="$(git -C "$F" rev-parse HEAD)"
pr pull_request "#57 работа до правила" "" batch-quota-fate "$h1" "$bq"
verdict "голова до правила (коммит до T0 в запросе) — имя законно, форма старого не судится" 0 "нарушений нет"
on 26 "$pr25"; nv -m "hooks: x"
pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
verdict "коммит после T0 без «#<N> » в запросе — отказ с его sha" 1 "не начинается с «#<N> »" "$(git -C "$F" rev-parse --short=10 HEAD)"
on 26 "$pr25"; nv -m "#26 x" -m "Co-authored-by: Claude <noreply@example.invalid>"
pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
verdict "коммит с атрибуцией в запросе — отказ" 1 "атрибуция в сообщении"
on 27; echo 8b > "$F/eight-b.txt"; git -C "$F" add eight-b.txt; commit -m "#8 предмет второй"
h8b="$(git -C "$F" rev-parse HEAD)"
on 26 "$pr25"; attempt git merge -q --no-ff --no-verify "$h8b" -m "#9 merge #8: предмет второй"
fact "фикстура: на ветке 26 записано слияние (два родителя)" \
    test "$(git -C "$F" log -1 --format=%P | wc -w)" -eq 2
pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
verdict "слияние «#9» на первой цепочке головы 26 — отказ" 1 "«#9» на ветке «26»"
on 26 "$pr25"; merge "$h8b" -m "#26 merge #8: предмет второй"
pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
verdict "близнец: слияние «#26 merge #8» на голове 26 — законно" 0 "нарушений нет"
# Номер влитой ветки M называет ТОЛЬКО первая строка слияния: у влитого коммита
# свой номер (#8), и сверка M держится лишь сбором номеров слияния.
on 26 "$pr25"; merge "$h8b" -m "#26 merge #99999: предмет второй"
pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
verdict "«#26 merge #99999: …» — M не задача этого репозитория: отказ" 1 "нет задачи #99999"
for case in "99999|нет задачи #99999" "46|#46 — запрос, а не задача" "4100|нет задачи #4100" "3010|задача #3010 перенесена"; do
    n="${case%%|*}"; why="${case#*|}"
    on 26 "$pr25"; nv -m "#$n x"
    pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
    verdict "«#$n …» — не задача этого репозитория: отказ" 1 "$why"
done
pr pull_request "#99999 x" "" 99999 "$h1" "$pr25"
verdict "номер заголовка и головы — тоже задача этого репозитория: отказ" 1 "нет задачи #99999"
on 26 "$pr25"; nv -m "#5000 x"
pr pull_request "#26 x" "" 26 "$pr25" "$(git -C "$F" rev-parse HEAD)"
verdict "API трекера ответил 500 — судить не смог: исход 2, а не зелёное" 2 "500"
pr push "" "" "" "" ""
verdict "событие не запрос (push) — судить нечего, и это сказано" 0 "запроса нет"
pr pull_request "#1 x" "" 1 "$h0" "$h0"
verdict "T0 не выведен (в истории запроса правила нет) — исход 2" 2 "T0 не выведен"
pr pull_request "#7 x" "" 7 "$h1" "$(git -C "$S" rev-parse HEAD)" "$S"
verdict "мелкий клон — диапазон неполон: исход 2" 2 "мелкий"
out="$(cd "$F" && env -i PATH="$PATH" HOME="$HOME" GITHUB_EVENT_NAME=pull_request bash "$PRCHECK" 2>&1)"; rc=$?
verdict "вход не задан (нет PR_TITLE) — исход 2" 2 "не задан"

# ── КОНТРОЛЬ: дерево без хука коммита (как ветка волны до corelib#25) ────────
echo "== контроль: без scripts/hooks/commit-msg дефект записывается"
G="$work/g"
mkdir -p "$G/scripts/hooks"
cp "$INSTALL" "$G/scripts/hooks/install.sh"
printf '#!/usr/bin/env bash\nexit 0\n' > "$G/scripts/hooks/pre-push"
chmod +x "$G/scripts/hooks/pre-push"
if ! { git -C "$G" init -q && git -C "$G" add -A && git -C "$G" commit -qm "fixture: без хука коммита"; }; then
    void "контрольная фикстура не собрана"
fi
(cd "$G" && bash scripts/hooks/install.sh install) >/dev/null 2>&1 || void "контрольная фикстура не провязана"
git -C "$G" checkout -q -b 7
out="$(git -C "$G" commit -q --allow-empty -m "hooks: x" -m "Co-Authored-By: Claude <noreply@example.invalid>" 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && [ "$(git -C "$G" log -1 --format=%s)" = "hooks: x" ]; then
    ok "контроль: без хука коммита «hooks: x» с атрибуцией записан — дефект воспроизводим"
else
    bad "контроль: без хука коммита дефектный коммит не записан (код $rc) — контроль ничего не показывает"
fi

# ── ГРАНИЦЫ: пути, которыми коммит минует commit-msg ─────────────────────────
echo "== границы (слепые зоны хука, измерены)"
bypassed() {
    local name="$1" want="$2" got
    got="$(git -C "$F" log -1 --format=%s)"
    if [ "$rc" -eq 0 ] && [ "$got" = "$want" ]; then ok "граница: $name — записано «$got»"
    else bad "граница: $name — код $rc, первая строка «$got»: слепая зона шапки хука не подтверждена"; printf '%s\n' "$out" | sed 's/^/      | /'; fi
}
on 7; commit --no-verify -m "hooks: x"
bypassed "git commit --no-verify" "hooks: x"
# src_c — коммит с дефектной первой строкой поверх коммита правила, собран без
# хука (commit-tree на своём индексе): его переносят команды ниже.
blob="$(printf 'src\n' | git -C "$F" hash-object -w --stdin)"
src_tree="$(cd "$F" && GIT_INDEX_FILE="$work/src.idx" git read-tree "$h1" &&
    GIT_INDEX_FILE="$work/src.idx" git update-index --add --cacheinfo "100644,$blob,src.txt" &&
    GIT_INDEX_FILE="$work/src.idx" git write-tree)" || void "дерево переносимого коммита не собрано"
src_c="$(git -C "$F" commit-tree -p "$h1" -m "hooks: src" "$src_tree")" || void "переносимый коммит не собран"
on 7; attempt git cherry-pick "$src_c"
bypassed "git cherry-pick" "hooks: src"
on 7 "$src_c"; attempt git revert --no-edit HEAD
bypassed "git revert --no-edit" "Revert \"hooks: src\""
on 7; echo base > "$F/base.txt"; git -C "$F" add base.txt; commit -m "#7 база"
h7b="$(git -C "$F" rev-parse HEAD)"
on 9 "$src_c"; attempt git rebase -q "$h7b"
bypassed "git rebase (без -i)" "hooks: src"
on 7; git -C "$F" format-patch -q -1 "$src_c" -o "$work/patches" >/dev/null
attempt git am -q "$work/patches"/*.patch
bypassed "git am" "hooks: src"
on 7
# shellcheck disable=SC2016  # раскрывает дочерний bash в фикстуре
attempt bash -c 'git reset -q --hard "$(git commit-tree -p HEAD -m "hooks: tree" "HEAD^{tree}")"'
bypassed "git commit-tree" "hooks: tree"
on 7; commit --cleanup=strip -m "#7 x" -m "тело стало первой строкой"
bypassed "--cleanup=strip в командной строке (хук видит «#7 x», git записывает тело)" "тело стало первой строкой"
on 7; merge 8 --no-verify -m "hooks: merge"
bypassed "git merge --no-verify" "hooks: merge"
# O1: новый коммит, скопировавший автора и дату вершины до T0, хук от переписи
# этой вершины не отличает — окружение хука у `--amend` и у `-C HEAD` одно
# (замер: GIT_AUTHOR_*, GIT_EDITOR, GIT_INDEX_FILE совпадают). Записан без
# суждения формы и автора; дата на день позже — уже отказ (выше, «раньше T0»).
on 7 "$hist_c"; commit -C HEAD
bypassed "новый коммит -C HEAD поверх вершины до T0 (O1)" "lane: старая работа"
fact "граница O1: записан НОВЫЙ коммит (родитель — вершина до T0), а не перепись" \
    test "$(git -C "$F" log -1 --format=%P)" = "$hist_c"
on 7 "$hist_c"; commit --author="old <old@example.invalid>" --date=2025-01-01T00:00:00Z -m "anything goes. Here"
bypassed "новый коммит с автором и датой вершины до T0 (O1)" "anything goes. Here"

# ── ПРОВЯЗКА В МЕХАНИЗМЫ: Makefile и ci.yml зовут пробу и проверку запроса ───
echo "== кто зовёт пробу и проверку запроса"
mk_out="$(make -C "$tree" --no-print-directory -n probe-hooks 2>&1)"
fact "make probe-hooks зовёт bash scripts/hooks/git-rule-inject.sh" \
    grep -qF "bash scripts/hooks/git-rule-inject.sh" <<<"$mk_out"
ci_runs="$(sed -nE 's/^[[:space:]]*(-[[:space:]]+)?run:[[:space:]]+//p' "$CI")"
fact "ci.yml исполняет bash scripts/hooks/git-rule-inject.sh строкой run:" \
    grep -qxF "bash scripts/hooks/git-rule-inject.sh" <<<"$ci_runs"
fact "ci.yml исполняет bash .github/scripts/pr-rule-check.sh строкой run:" \
    grep -qxF "bash .github/scripts/pr-rule-check.sh" <<<"$ci_runs"
# Заголовок и тело правятся без отправки: без `edited` вердикт о них остаётся
# от прежнего текста. Строка `types:` блока pull_request — код, а не комментарий.
pr_types="$(sed -n '/^  pull_request:/,/^  [a-z_]*:/p' "$CI" | grep -E '^    types:')"
fact "ci.yml: запрос перепроверяется на правке заголовка и тела (types содержит edited)" \
    grep -qE '(\[|[ ,])edited([],]| |$)' <<<"$pr_types"

echo ""
total=$((pass + fail))
echo "== git-rule-inject: утверждений $total · сошлось $pass · разошлось $fail"
[ "$total" -gt 0 ] || void "не проверено ни одного утверждения"
[ "$fail" -eq 0 ] || exit 1
exit 0
