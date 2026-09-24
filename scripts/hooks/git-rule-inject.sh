#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# git-rule-inject.sh — проба хука коммита (scripts/hooks/commit-msg) и его
# предиката (scripts/hooks/git-rule.sh). Каждое свойство — парой: дефект →
# коммит НЕ записан, код не 0, причина названа; законный близнец, отличный в
# один факт, → коммит записан с той первой строкой, которую подали. Судятся
# файлы ЭТОЙ рабочей копии, скопированные в синтетический клон во временном
# каталоге; коммиты — настоящие `git commit` и `git merge` сквозь переходник
# v2, который кладёт install.sh (текст переходника — у производителя, режим
# `install.sh stub`, а не своей копией).
#
# КОРНЕВАЯ УЧЁТНАЯ ЗАПИСЬ ПРОБЫ ЗАДАЁТСЯ HOME, а не GIT_CONFIG_GLOBAL: корень —
# это `~/.gitconfig`, а GIT_CONFIG_GLOBAL его перенаправляет, и такой коммит
# хук отвергает (это одно из утверждений ниже).
#
# КОНТРОЛЬ — дерево без хука коммита (как ветка волны до corelib#25): тот же
# дефектный коммит там записывается. Без этого прогона нечем показать, что
# дефект воспроизводим, а не выдуман.
#
# ГРАНИЦЫ ИЗМЕРЕНЫ, А НЕ ОБЪЯВЛЕНЫ. Пути, которыми коммит минует commit-msg
# (--no-verify, cherry-pick, rebase, am, revert, commit-tree, --cleanup=strip в
# командной строке), проба исполняет и утверждает, что дефектный коммит
# записан: шапка хука называет их слепыми зонами, и это утверждение держится
# замером git этой машины, а не памятью.
#
# ИСХОДЫ: 0 — все утверждения сошлись; 1 — хоть одно разошлось;
#         2 — не выполнилось (нет файла под пробой, нет git/make, фикстура не
#             собрана, не проверено ни одного утверждения).
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tree="$(cd "$here/../.." && pwd -P)"
HOOK="$here/commit-msg"
RULE="$here/git-rule.sh"
INSTALL="$here/install.sh"
MAKEFILE="$tree/Makefile"
CI="$tree/.github/workflows/ci.yml"

void() { echo "git-rule-inject: НЕ ВЫПОЛНИЛОСЬ — $*" >&2; exit 2; }
for f in "$HOOK" "$RULE" "$INSTALL" "$MAKEFILE" "$CI"; do [ -f "$f" ] || void "нет $f"; done
for t in git make sed grep cmp seq mktemp; do command -v "$t" >/dev/null 2>&1 || void "нет $t в PATH"; done

work="$(mktemp -d)" || void "нет временного каталога"
trap 'rm -rf "$work"' EXIT
work="$(cd "$work" && pwd -P)"

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR \
      GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_PREFIX GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM \
      GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL \
      GIT_AUTHOR_DATE GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL GIT_COMMITTER_DATE \
      GIT_EDITOR GIT_SEQUENCE_EDITOR EDITOR VISUAL MAKEFLAGS MAKELEVEL MFLAGS
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

pass=0
fail=0
ok()  { pass=$((pass + 1)); echo "  сошлось    $1"; }
bad() { fail=$((fail + 1)); echo "  РАЗОШЛОСЬ  $1"; }
fact() { local name="$1"; shift; if "$@"; then ok "$name"; else bad "$name"; fi; }
# shellcheck disable=SC2329  # зовётся через fact
not() { ! "$@"; }

# ── ФИКСТУРА ─────────────────────────────────────────────────────────────────
# Первый коммит — история до правила: дата автора 2020 года, первая строка без
#      номера, подпись не корневая; заведён ДО провязки, хука на нём нет.
# h1 — коммит правила: заводит scripts/hooks/commit-msg; его время автора — T0.
F="$work/f"
mkdir -p "$F/scripts/hooks"
cp "$HOOK" "$RULE" "$INSTALL" "$F/scripts/hooks/" || void "файлы под пробой не скопированы"
chmod +x "$F/scripts/hooks/commit-msg"
echo 'фикстура пробы правила git' > "$F/README"
t0_iso="2026-01-01T00:00:00Z"
git -C "$F" init -q || void "фикстура не заведена"
if ! { git -C "$F" add README &&
    GIT_AUTHOR_NAME=old GIT_AUTHOR_EMAIL=old@example.invalid GIT_AUTHOR_DATE=2020-01-01T00:00:00Z \
    GIT_COMMITTER_DATE=2020-01-01T00:00:00Z git -C "$F" commit -qm "fixture: история до правила"; }; then
    void "коммит истории фикстуры не собран"
fi
if ! { git -C "$F" add scripts &&
    GIT_AUTHOR_DATE="$t0_iso" GIT_COMMITTER_DATE="$t0_iso" git -C "$F" commit -qm "#1 правило git в хуках"; }; then
    void "коммит правила фикстуры не собран"
fi
h1="$(git -C "$F" rev-parse HEAD)"

echo "== провязка хука коммита (install.sh)"
runc() { out="$("$@" 2>&1 </dev/null)"; rc=$?; }
runc bash -c "cd '$F' && bash scripts/hooks/install.sh install"
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -qF "переходник провязан 1"; then
    ok "install провязывает commit-msg переходником v2 (код 0)"
else
    bad "install в фикстуре: код $rc"; printf '%s\n' "$out" | sed 's/^/      | /'
fi
FH="$F/.git/hooks"
[ -x "$FH/commit-msg" ] || void "переходник commit-msg не провязан — судить хук нечем"
fact "переходник commit-msg побайтно равен тексту производителя (install.sh stub)" \
    cmp -s "$FH/commit-msg" <(bash "$INSTALL" stub commit-msg)
fact "переходник commit-msg советует обход коммита, а не отправки" \
    grep -qF "git commit --no-verify" "$FH/commit-msg"
fact "переходник commit-msg не советует «git push --no-verify»" \
    not grep -qF "git push --no-verify" "$FH/commit-msg"

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
# refused <имя> <образец…> — коммит НЕ записан, код не 0, каждый образец в тексте.
refused() {
    local name="$1" p good=1 miss=""
    shift
    [ "$rc" -ne 0 ] || good=0
    [ "$made" = 0 ] || good=0
    for p in "$@"; do printf '%s' "$out" | grep -qF -- "$p" || { good=0; miss="$miss «$p»"; }; done
    if [ "$good" = 1 ]; then ok "$name (отказ, код $rc)"
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
# этого репозитория, судит не хук (сети у него нет), а проверка запроса.
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
on 7; commit -m "#7 x" -m "Хук отвергает трейлер Claude-Session в начале строки, упоминание — нет."
accepted "близнец C6: упоминание имени трейлера в середине строки — не трейлер" "#7 x"

# ── ПОДПИСЬ — КОРНЕВАЯ УЧЁТНАЯ ЗАПИСЬ ────────────────────────────────────────
echo "== подпись (корень — ~/.gitconfig пробы)"
on 7; commit -m "#7 x"
fact "близнец: автор и коммиттер — корневая учётная запись" \
    test "$(git -C "$F" log -1 --format='%an <%ae>|%cn <%ce>')" = "$root_name <$root_mail>|$root_name <$root_mail>"
on 7; commit --author="Other <other@example.invalid>" -m "#7 x"
refused "--author чужой — отказ" "автор «Other <other@example.invalid>»"
on 7; E=(GIT_AUTHOR_NAME=bot GIT_AUTHOR_EMAIL=bot@example.invalid); commit -m "#7 x"
refused "GIT_AUTHOR_* чужие — отказ" "автор «bot <bot@example.invalid>»"
on 7; attempt git -c user.name="$root_name" -c user.email="$root_mail" commit -q --allow-empty -m "#7 x"
refused "-c user.* с КОРНЕВЫМИ значениями — отказ при любом значении" "command"
on 7; git -C "$F" config --local user.email "$root_mail"; commit -m "#7 x"
refused "--local user.email — отказ при любом значении" "local"
git -C "$F" config --local --unset user.email
on 7; E=(GIT_COMMITTER_NAME="$root_name"); commit -m "#7 x"
refused "GIT_COMMITTER_NAME — отказ при любом значении" "GIT_COMMITTER_NAME"
cp "$HOME/.gitconfig" "$work/other.gitconfig"
on 7; E=(GIT_CONFIG_GLOBAL="$work/other.gitconfig"); commit -m "#7 x"
refused "GIT_CONFIG_GLOBAL перенаправляет корень — отказ" "GIT_CONFIG_GLOBAL"
mkdir -p "$work/nohome"
on 7; E=(HOME="$work/nohome" XDG_CONFIG_HOME="$work/nohome/.config" GIT_AUTHOR_NAME=x GIT_AUTHOR_EMAIL=x@example.invalid GIT_COMMITTER_NAME=x GIT_COMMITTER_EMAIL=x@example.invalid); commit -m "#7 x"
refused "корневая учётная запись не задана — отказ" "корневая учётная запись не задана"

# ── РЕДАКТОР: строка «#…» вырезается как комментарий ─────────────────────────
echo "== коммит через редактор"
# Редактор пишет первую строку и тело: без хука git вырежет «#7 x» и запишет
# коммит с первой строкой «тело» — дефект, а не пустое сообщение.
# shellcheck disable=SC2016  # $1 — аргумент редактора, раскрывается им, а не здесь
printf '#!/bin/sh\nprintf "#7 x\\n\\nтело\\n" > "$1"\n' > "$work/editor.sh"
chmod +x "$work/editor.sh"
on 7; E=(GIT_EDITOR="$work/editor.sh"); attempt git commit -q --allow-empty
refused "коммит через редактор — отказ до того, как git вырежет «#7 x»" "через редактор"
on 7; git -C "$F" config core.commentChar ';'; E=(GIT_EDITOR="$work/editor.sh"); attempt git commit -q --allow-empty
accepted "близнец: редактор при core.commentChar=; — «#» не комментарий" "#7 x"
git -C "$F" config --unset core.commentChar
on 7; git -C "$F" config commit.cleanup strip; commit -m "#7 x" -m "тело"
refused "commit.cleanup=strip вырежет «#7 x» и при -m — отказ" "commit.cleanup"
on 7; git -C "$F" config commit.cleanup whitespace; commit -m "#7 x" -m "тело"
accepted "близнец: commit.cleanup=whitespace" "#7 x"
git -C "$F" config --unset commit.cleanup

# ── СЛИЯНИЕ: настоящий git merge ─────────────────────────────────────────────
echo "== слияние (git merge --no-ff)"
on 8; echo 8 > "$F/eight.txt"; git -C "$F" add eight.txt; commit -m "#8 предмет восьмой"
accepted "ветка 8: свой коммит" "#8 предмет восьмой"
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

# ── T0: граница истории ──────────────────────────────────────────────────────
echo "== T0 (время автора коммита, заведшего scripts/hooks/commit-msg)"
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

# ── ПЕРЕХОДНИК И АДРЕСАТ ─────────────────────────────────────────────────────
echo "== переходник: адресата нет — отказ"
on 7
mv "$F/scripts/hooks/commit-msg" "$work/cm.aside"
commit -m "#7 x"
refused "адресат снят — коммит остановлен, проверок не было" "ОТКАЗ — проверок НЕ БЫЛО" "git commit --no-verify"
mv "$work/cm.aside" "$F/scripts/hooks/commit-msg"
mv "$F/scripts/hooks/git-rule.sh" "$work/rule.aside"
commit -m "#7 x"
refused "предиката нет рядом с хуком — отказ, а не молчаливый пропуск" "git-rule.sh"
mv "$work/rule.aside" "$F/scripts/hooks/git-rule.sh"

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

# ── ПРОВЯЗКА В МЕХАНИЗМЫ: Makefile и ci.yml зовут эту пробу ──────────────────
echo "== кто зовёт пробу"
mk_out="$(make -C "$tree" --no-print-directory -n probe-hooks 2>&1)"
fact "make probe-hooks зовёт bash scripts/hooks/git-rule-inject.sh" \
    grep -qF "bash scripts/hooks/git-rule-inject.sh" <<<"$mk_out"
ci_runs="$(sed -nE 's/^[[:space:]]*(-[[:space:]]+)?run:[[:space:]]+//p' "$CI")"
fact "ci.yml исполняет bash scripts/hooks/git-rule-inject.sh строкой run:" \
    grep -qxF "bash scripts/hooks/git-rule-inject.sh" <<<"$ci_runs"

echo ""
total=$((pass + fail))
echo "== git-rule-inject: утверждений $total · сошлось $pass · разошлось $fail"
[ "$total" -gt 0 ] || void "не проверено ни одного утверждения"
[ "$fail" -eq 0 ] || exit 1
exit 0
