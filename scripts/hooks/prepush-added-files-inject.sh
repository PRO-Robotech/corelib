#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Проба стража НОВЫХ BLOB'ОВ отправки (#117; образец — kacho-workspace#1189):
# хук отправки отвергает БИНАРЬ и файл БОЛЬШЕ 1 МБ среди blob'ов, которые
# отправка вносит в публичную историю, и называет файл.
#
# Признак: в kacho#3284 коммит ветки нёс собранный ELF края на 39 МБ; поймало его
# ревью, а не хук. Держатель — scripts/hooks/prepush-added-files.sh, который
# scripts/hooks/pre-push зовёт вторым, после стража правила git, до обхода
# CORELIB_SKIP_PREPUSH. Без поведенческой пробы детектор бинаря, порог или перепись
# истории ослабляются молча, и отправка продолжает проходить.
#
# ПОВЕДЕНЧЕСКАЯ: песочница — настоящий репозиторий с голым удалённым, на который
# опубликован `main`; в её дереве — копии хука, стража правила и стража новых
# blob'ов. Подпись песочницы — своя (HOME песочницы), не машины. Перечень
# исключений — СВОЙ у песочницы (строка коллекций newman с доводом): перечень
# дерева судит дерево, а не песочницу.
#
# ЧТО ТРЕБУЕТСЯ (страж — напрямую, вход git на stdin):
#   отказ (код 1, файл назван): настоящий исполняемый файл (копия `true`) и вид
#     «исполняемый бинарь»; бинарь без режима исполнения; текст больше 1 МБ вне
#     перечня; бинарь, добавленный и снятый следующим коммитом; бинарь, внесённый
#     руками в слияние; бинарь под «*.bin diff» в `.gitattributes`, под «* diff»
#     в `.git/info/attributes` и в `core.attributesFile`; законная ветка рядом с
#     бинарём (антимаска); строка перечня без предмета и без довода; ELF и
#     крупный текст, переименованные с правкой (и при `diff.renames=copies`);
#     опубликованный текст, перезаписанный ELF либо раздутый за порог; ELF в
#     первом коммите отправки при тексте во втором; текст мимо шаблона перечня;
#     ELF в корневом коммите при `log.showRoot=false` без ссылок удалённого;
#   молчание (код 0): коллекция newman больше 1 МБ по строке перечня (законный
#     близнец); исполняемый текстовый скрипт; текст ровно 1 МБ; бинарь, уже
#     опубликованный на удалённом; снятие ветки; `git mv` крупного текста без
#     правки и его копия (при любой `diff.renames`); правка текста меньше
#     порога; запись строки перечня с предметом; 3000 новых blob'ов (страж не
#     встаёт насмерть на трубе);
#   не судимо: битая ссылка удалённого — страж выходит 2.
# ЧТО ТРЕБУЕТСЯ ОТ ХУКА (настоящий pre-push песочницы):
#   ELF при CORELIB_SKIP_PREPUSH=1 — отказ кодом 1 с именем файла (страж стоит до обхода);
#   страж вышел 2 (не судимо) — отказ кодом 1; стража нет рядом с хуком — отказ
#   кодом 1 с причиной.
#
# ПРЕДПОСЫЛКА (VOID, код 2): законная отправка текста через хук при CORELIB_SKIP_PREPUSH=1
# проходит. Хук, красный на ней не по стражу новых blob'ов, к остальным пробам
# непригоден: отказ на бинаре прошёл бы тождественно и не доказал бы ничего.
# Отказал на ней сам страж — это находка (код 1), не VOID. Настоящего ELF нет — 2.
#
# --mutants: та же проба на семи воссозданных дефектах стража (детектор бинаря
# снят; порог снят; слияние не судится; атрибуты дерева перекрашивают бинарь;
# опубликованное не вычитается; строка перечня без предмета принята; строка без
# довода принята) — каждый обязан дать код 1, исходный страж — код 0.
#
# ИСХОДЫ: 0 — все пробы сошлись; 1 — проба с находкой (названа); 2 — не судимо.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKIP_VAR=CORELIB_SKIP_PREPUSH
NUM=117
REF_BRANCH=117-probe
KIT=(pre-push git-rule-push.sh git-rule.sh git-rule-bare-branches.txt)
GUARD="${PREPUSH_ADDED_GUARD_UNDER_TEST:-$here/prepush-added-files.sh}"

if [ "${1:-}" = "--mutants" ]; then
    [ -f "$here/prepush-added-files.sh" ] || { echo "mutants: стража нет — мутировать нечего" >&2; exit 2; }
    mt="$(mktemp -d)"
    trap 'rm -rf "$mt"' EXIT
    rc=0
    code=0; bash "$0" > "$mt/base.out" 2>&1 || code=$?
    if [ "$code" -ne 0 ]; then
        echo "mutants: исходный страж дал $code вместо 0 — мутанты недоказательны: $(tail -5 "$mt/base.out" | tr '\n' ' ')" >&2
        exit 2
    fi
    while IFS='|' read -r name from to; do
        [ -n "$name" ] || continue
        if ! python3 - "$here/prepush-added-files.sh" "$mt/m.sh" "$from" "$to" <<'PY'
import sys
s, d, a, b = sys.argv[1:5]
t = open(s, encoding='utf-8').read()
if t.count(a) != 1:
    sys.exit(3)
open(d, 'w', encoding='utf-8').write(t.replace(a, b))
PY
        then
            echo "mutants: «$name» — образец мутации в страже не найден ровно один раз: проба мутанта не строится" >&2
            rc=2; continue
        fi
        code=0; PREPUSH_ADDED_GUARD_UNDER_TEST="$mt/m.sh" bash "$0" > "$mt/out" 2>&1 || code=$?
        if [ "$code" -eq 1 ]; then
            echo "mutants: «$name» — проба красная (код 1), как обязана: $(grep -m1 'НАХОДКА' "$mt/out" | head -c 200)"
        else
            echo "mutants: «$name» — проба вышла $code вместо 1: дефект прошёл бы молча" >&2
            [ "$rc" -eq 2 ] || rc=1
        fi
    done <<'EOF'
детектор бинаря снят|b'\0' in data[:PROBE], data[:PROBE])|False, data[:PROBE])
порог снят|    if size > LIMIT and not allowed('size', path):|    if False:
слияние не судится|git(['rev-list', '--objects', '--filter=object:type=blob'] + rng)|git(['rev-list', '--objects', '--no-merges', '--filter=object:type=blob'] + rng)
атрибуты дерева перекрашивают бинарь|    size, is_bin, head = info[blob]|    size, is_bin, head = info[blob]; is_bin = is_bin and not subprocess.run(['git', 'check-attr', 'diff', '--', path], capture_output=True).stdout.strip().endswith(b': set')
опубликованное не вычитается|rng = tcs + prev + ['--not', '--remotes=' + remote]|rng = tcs
строка перечня без предмета принята|    if not a[2]:|    if False:
строка перечня без довода принята|        elif len(f) < 3 or not f[2].strip():|        elif False:
EOF
    exit "$rc"
fi

for t in git python3 file mktemp dd tr head; do
    command -v "$t" > /dev/null 2>&1 || { echo "prepush-added-files-inject: НЕ СУДИМО — нет $t" >&2; exit 2; }
done
ELF="$(type -P true || true)"
[ -z "$ELF" ] || ELF="$(readlink -f "$ELF")"
elf_kind="$( [ -z "$ELF" ] || file -b "$ELF" 2> /dev/null || true)"
if [ "${elf_kind#ELF}" = "$elf_kind" ]; then
    echo "prepush-added-files-inject: НЕ СУДИМО — настоящего исполняемого файла для инъекции нет ($ELF не ELF)" >&2
    exit 2
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
H="$TMP/home"
mkdir -p "$H/.config"
printf '[user]\n\tname = probe\n\temail = probe@example.invalid\n[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n' > "$H/.gitconfig"
sb() {
    env -u GIT_AUTHOR_NAME -u GIT_AUTHOR_EMAIL -u GIT_COMMITTER_NAME -u GIT_COMMITTER_EMAIL \
        -u GIT_CONFIG_GLOBAL -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT \
        -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE -u GIT_OBJECT_DIRECTORY \
        -u GIT_ALTERNATE_OBJECT_DIRECTORIES -u GIT_COMMON_DIR -u GIT_PREFIX -u "$SKIP_VAR" \
        HOME="$H" XDG_CONFIG_HOME="$H/.config" GIT_CONFIG_NOSYSTEM=1 "$@"
}
pgit() { sb git -c core.hooksPath=/dev/null "$@"; }

ZERO=0000000000000000000000000000000000000000
COLL="services/x/tests/newman/collections"
box="$TMP/box"
mkdir -p "$box/scripts/hooks" "$box/$COLL"
findings=0 examined=0 lawful=0 unlawful=0
fail() { echo "prepush-added-files-inject: НАХОДКА — $1" >&2; findings=$((findings + 1)); }

for f in "${KIT[@]}"; do
    if [ -f "$here/$f" ]; then
        cp "$here/$f" "$box/scripts/hooks/"
    else
        echo "prepush-added-files-inject: НЕ СУДИМО — в дереве нет scripts/hooks/$f" >&2
        exit 2
    fi
done
have_guard=1
if [ -f "$GUARD" ]; then
    cp "$GUARD" "$box/scripts/hooks/prepush-added-files.sh"
else
    have_guard=0
    fail "стража scripts/hooks/prepush-added-files.sh нет в дереве — бинарь уезжает без суда"
fi
chmod +x "$box/scripts/hooks/"*
printf 'size services/*/tests/newman/collections/*.postman_collection.json коллекция newman — текст, порождаемый gen.py\n' \
    > "$box/scripts/hooks/prepush-added-files.allow"
printf '{"info":{}}\n' > "$box/$COLL/a.postman_collection.json"
# Опубликованное в main: малый текст, крупный текст и ELF — предметы правки,
# переименования и копии.
printf 'старый текст\n' > "$box/old.txt"
head -c 1200000 /dev/zero | tr '\0' 'b' > "$box/big1.txt"
cp "$ELF" "$box/pub.bin"
# Свой вариант ELF (байт 300): копии `true` в пробах ниже остаются НОВЫМИ blob'ами.
printf '\001' | dd of="$box/pub.bin" bs=1 seek=300 conv=notrunc status=none
pgit -C "$box" init -q -b main
pgit -C "$box" add -A
pgit -C "$box" commit -q -m "#$NUM песочница пробы"
pgit init -q --bare "$box.git"
pgit -C "$box" remote add origin "$box.git"
pgit -C "$box" push -q origin main
BASE="$(pgit -C "$box" rev-parse HEAD)"

branch() { pgit -C "$box" checkout -q -f -B "$1" "$BASE"; }
commit() { local m="$1"; shift; pgit -C "$box" add -- "$@"; pgit -C "$box" commit -q -m "#$NUM $m"; }
text() { head -c "$2" /dev/zero | tr '\0' 'a' > "$box/$1"; }
head_sha() { pgit -C "$box" rev-parse HEAD; }
create() { printf 'refs/heads/%s %s refs/heads/%s %s' "$REF_BRANCH" "$(head_sha)" "$REF_BRANCH" "$ZERO"; }
flip() { printf '\001' | dd of="$box/$1" bs=1 seek=200 conv=notrunc status=none; }

# judge <вход git> — «<код>» первой строкой и вывод стража следом.
judge() {
    local out code
    out="$(cd "$box" && printf '%s\n' "$1" | sb timeout 50 bash ./scripts/hooks/prepush-added-files.sh origin 2>&1)" && code=0 || code=$?
    printf '%s\n%s\n' "$code" "$out"
}
# hook <вход git> [env…] — то же для настоящего хука песочницы.
hook() {
    local input="$1" out code
    shift
    out="$(cd "$box" && printf '%s\n' "$input" | sb env "$@" timeout 50 bash ./scripts/hooks/pre-push origin "$box.git" 2>&1)" && code=0 || code=$?
    printf '%s\n%s\n' "$code" "$out"
}
expect_pass() {  # <описание> <вход>
    local res
    examined=$((examined + 1)); lawful=$((lawful + 1))
    res="$(judge "$2")"
    [ "$(head -1 <<< "$res")" -eq 0 ] ||
        fail "$1 — отвергнуто (код $(head -1 <<< "$res")), а отправка законна: $(grep -m2 'ОТКАЗ\|^  ' <<< "$res" | tr '\n' ' ')"
}
expect_refuse() {  # <что обязан назвать> <описание> <вход>
    local res
    examined=$((examined + 1)); unlawful=$((unlawful + 1))
    res="$(judge "$3")"
    if [ "$(head -1 <<< "$res")" -ne 1 ]; then
        fail "$2 — страж вышел $(head -1 <<< "$res") вместо 1: файл уехал бы в публичную историю"
    elif ! tail -n +2 <<< "$res" | grep -qF -- "$1"; then
        fail "$2 — отказ не называет «$1»"
    fi
}
hook_refuse() {  # <что обязан назвать> <описание> <вход> [env…]
    local name="$1" d="$2" in="$3" res
    shift 3
    examined=$((examined + 1)); unlawful=$((unlawful + 1))
    res="$(hook "$in" "$@")"
    if [ "$(head -1 <<< "$res")" -ne 1 ]; then
        fail "$d — хук вышел $(head -1 <<< "$res") вместо 1: файл уехал бы в публичную историю"
    elif ! tail -n +2 <<< "$res" | grep -qF -- "$name"; then
        fail "$d — отказ хука не называет «$name»"
    fi
}

# ── Предпосылка: законная отправка текста через хук при обходе проходит ─────
branch lawful; printf 'текст\n' > "$box/note.md"; commit "текст" note.md
LAWFUL_TIP="$(head_sha)"
res="$(hook "$(create)" "$SKIP_VAR=1")"
examined=$((examined + 1)); lawful=$((lawful + 1))
if [ "$(head -1 <<< "$res")" -ne 0 ] && grep -qE 'prepush-added-files: (ОТКАЗ|НЕ СУДИМО)' <<< "$res"; then
    # Отказал именно страж новых blob'ов — это находка о нём, а не непригодность хука.
    fail "законная отправка текста через хук отвергнута стражем новых blob'ов: $(grep -m2 'ОТКАЗ\|^  ' <<< "$res" | tr '\n' ' ' | head -c 300)"
elif [ "$(head -1 <<< "$res")" -ne 0 ]; then
    echo "prepush-added-files-inject: НЕ СУДИМО — законная отправка текста через хук вернула $(head -1 <<< "$res"); остальные пробы на ней недоказательны: $(tail -n +2 <<< "$res" | tr '\n' ' ' | head -c 600)" >&2
    exit 2
fi

# ── Хук: страж стоит до обхода и до проверок ────────────────────────────────
branch elf; cp "$ELF" "$box/edge"; chmod +x "$box/edge"; commit "бинарь" edge
hook_refuse edge "ELF при $SKIP_VAR=1 — страж стоит до обхода" "$(create)" "$SKIP_VAR=1"

if [ "$have_guard" = 1 ]; then
    mv "$box/scripts/hooks/prepush-added-files.sh" "$TMP/guard.away"
    hook_refuse "стража добавленных файлов нет" "стража нет рядом с хуком" "$(create)" "$SKIP_VAR=1"
    mv "$TMP/guard.away" "$box/scripts/hooks/prepush-added-files.sh"
fi

if [ "$have_guard" = 1 ]; then
# ── Страж: отказы ───────────────────────────────────────────────────────────
expect_refuse edge "настоящий исполняемый файл (ELF) среди добавленных" "$(create)"
expect_refuse "исполняемый бинарь" "отказ называет вид: исполняемый бинарь" "$(create)"

branch blob; head -c 4096 "$ELF" > "$box/blob.dat"; commit "бинарь" blob.dat
expect_refuse blob.dat "бинарь без режима исполнения" "$(create)"

branch big; text big.txt 1048577; commit "крупное" big.txt
expect_refuse big.txt "текст больше 1 МБ вне перечня" "$(create)"

branch gone; cp "$ELF" "$box/gone"; commit "бинарь" gone
pgit -C "$box" rm -q gone; pgit -C "$box" commit -q -m "#$NUM снят"
expect_refuse gone "бинарь добавлен и снят следующим коммитом — в истории он есть" "$(create)"

branch side; printf 's\n' > "$box/side.md"; commit "бок" side.md
branch evil; printf 'm\n' > "$box/main.md"; commit "ствол" main.md
pgit -C "$box" merge -q --no-commit --no-ff side > /dev/null 2>&1
cp "$ELF" "$box/evil"; pgit -C "$box" add evil; pgit -C "$box" commit -q -m "#$NUM merge main: слияние"
expect_refuse evil "бинарь, внесённый руками в слияние" "$(create)"

branch attr; printf '*.bin diff\n' > "$box/.gitattributes"; cp "$ELF" "$box/x.bin"
commit "маска" .gitattributes x.bin
expect_refuse x.bin "бинарь под строкой «*.bin diff» в .gitattributes" "$(create)"

branch elf2; cp "$ELF" "$box/edge"; commit "бинарь" edge
expect_refuse edge "законная ветка рядом с бинарём (антимаска)" \
    "refs/heads/a $LAWFUL_TIP refs/heads/a $ZERO
$(create)"

branch renelf; pgit -C "$box" mv pub.bin renamed2.bin; flip renamed2.bin; commit "переименован с правкой" renamed2.bin
expect_refuse renamed2.bin "ELF переименован с правкой байта (R) — новый blob" "$(create)"
pgit -C "$box" config diff.renames copies
expect_refuse renamed2.bin "то же при diff.renames=copies — вердикт от настройки не зависит" "$(create)"
pgit -C "$box" config --unset diff.renames
branch renbig; pgit -C "$box" mv big1.txt big2.txt; printf 'x' >> "$box/big2.txt"; commit "крупный переименован с правкой" big2.txt
expect_refuse big2.txt "текст 1,2 МБ переименован с правкой (R) — новый blob" "$(create)"
branch overelf; cp "$ELF" "$box/old.txt"; commit "перезапись" old.txt
expect_refuse old.txt "опубликованный old.txt перезаписан ELF (M)" "$(create)"
branch grow; text old.txt 2097152; commit "рост" old.txt
expect_refuse old.txt "опубликованный old.txt раздут до 2 МБ (M)" "$(create)"
branch first; cp "$ELF" "$box/first.bin"; commit "бинарь первым" first.bin
printf 'т\n' > "$box/second.md"; commit "текст вторым" second.md
expect_refuse first.bin "ELF в первом коммите отправки, текст во втором" "$(create)"
pgit -C "$box" checkout -q -f elf
mkdir -p "$box/.git/info"; printf '* diff\n' > "$box/.git/info/attributes"
expect_refuse edge "ELF при «* diff» в .git/info/attributes" "$(create)"
rm -f "$box/.git/info/attributes"
printf '* diff\n' > "$TMP/attrs"; pgit -C "$box" config core.attributesFile "$TMP/attrs"
expect_refuse edge "ELF при «* diff» в core.attributesFile" "$(create)"
pgit -C "$box" config --unset core.attributesFile

ALLOW="$box/scripts/hooks/prepush-added-files.allow"
cp "$ALLOW" "$TMP/allow"
branch lawful2; printf 'т\n' > "$box/n2.md"; commit "текст" n2.md
printf 'binary deploy/*.tgz архив чарта\n' >> "$ALLOW"
expect_refuse "deploy/*.tgz" "строка перечня без предмета" "$(create)"
cp "$TMP/allow" "$ALLOW"; printf 'size %s/*.json\n' "$COLL" >> "$ALLOW"
expect_refuse "без довода" "строка перечня без довода" "$(create)"
cp "$TMP/allow" "$ALLOW"

# ── Страж: законные близнецы ────────────────────────────────────────────────
branch mv; pgit -C "$box" mv big1.txt big4.txt; commit "переименован без правки" big4.txt
expect_pass "git mv крупного текста без правки — blob прежний, опубликован" "$(create)"
branch copy; cp "$box/big1.txt" "$box/big3.txt"; commit "копия" big3.txt
expect_pass "копия опубликованного крупного текста — нового blob'а нет" "$(create)"
pgit -C "$box" config diff.renames copies
expect_pass "та же копия при diff.renames=copies — тот же исход" "$(create)"
pgit -C "$box" config --unset diff.renames
branch small; printf 'ещё строка\n' >> "$box/old.txt"; commit "правка" old.txt
expect_pass "правка текстового файла меньше порога" "$(create)"
branch allow; printf 'size big1.txt крупный текст песочницы — опубликован в main\n' >> "$ALLOW"
commit "строка перечня" scripts/hooks/prepush-added-files.allow
expect_pass "запись строки перечня исключений с предметом" "$(create)"
pgit -C "$box" checkout -q -f "$BASE" -- scripts/hooks/prepush-added-files.allow
branch newman; text "$COLL/b.postman_collection.json" 1500000; commit "коллекция" "$COLL/b.postman_collection.json"
expect_pass "коллекция newman больше 1 МБ по строке перечня — законный близнец" "$(create)"
branch newman2; text "$COLL/b.json" 1500000; commit "не коллекция" "$COLL/b.json"
expect_refuse "$COLL/b.json" "тот же текст мимо шаблона перечня — отказ (перечень, а не каталог)" "$(create)"
branch script; printf '#!/usr/bin/env bash\necho ok\n' > "$box/run.sh"; chmod +x "$box/run.sh"; commit "скрипт" run.sh
expect_pass "исполняемый текстовый скрипт" "$(create)"
branch edge1m; text exact.txt 1048576; commit "ровно 1 МБ" exact.txt
expect_pass "текст ровно 1 МБ — граница порога" "$(create)"
branch pub; cp "$ELF" "$box/pub"; flip pub; printf '\002' | dd of="$box/pub" bs=1 seek=400 conv=notrunc status=none; commit "бинарь" pub
pgit -C "$box" push -q --no-verify origin pub
printf 'п\n' > "$box/after.md"; commit "дальше" after.md
expect_pass "бинарь уже опубликован на удалённом — уезжает лишь текст" \
    "refs/heads/pub $(head_sha) refs/heads/pub $(pgit -C "$box" rev-parse HEAD~1)"
expect_pass "снятие ветки" "(delete) $ZERO refs/heads/pub $(pgit -C "$box" rev-parse HEAD~1)"

# Много новых blob'ов (больше буфера трубы на входе и выходе cat-file).
branch many; mkdir -p "$box/many"
for i in $(seq 1 3000); do printf 'f%s\n' "$i" > "$box/many/$i.txt"; done
commit "много файлов" many
expect_pass "3000 новых blob'ов — страж завершается сам (124 — встал насмерть)" "$(create)"

# ── Не судимо: битая ссылка удалённого ──────────────────────────────────────
branch void; printf 'в\n' > "$box/v.md"; commit "текст" v.md
printf 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n' > "$box/.git/refs/remotes/origin/broken"
examined=$((examined + 1)); unlawful=$((unlawful + 1))
res="$(judge "$(create)")"
{ [ "$(head -1 <<< "$res")" -eq 2 ] && grep -qF 'НЕ СУДИМО' <<< "$res"; } ||
    fail "битая ссылка удалённого: страж вышел $(head -1 <<< "$res") вместо 2 — пустой обход прошёл бы зелёным"
rm -f "$box/.git/refs/remotes/origin/broken"
# Хук читает код стража, а не его вывод: «не судимо» (2) — отказ хука. Заглушка
# отделяет провязку от стража правила, который на битой ссылке судил бы первым.
mv "$box/scripts/hooks/prepush-added-files.sh" "$TMP/guard.away"
printf '#!/usr/bin/env bash\necho "заглушка: НЕ СУДИМО" >&2\nexit 2\n' > "$box/scripts/hooks/prepush-added-files.sh"
hook_refuse "код стража 2" "страж не смог судить (код 2) — хук отказывает" "$(create)" "$SKIP_VAR=1"
mv "$TMP/guard.away" "$box/scripts/hooks/prepush-added-files.sh"

# Корневой коммит при log.showRoot=false и без ссылок удалённого: судится вся история.
solo="$TMP/solo"; mkdir -p "$solo/g"; cp "$GUARD" "$solo/g/prepush-added-files.sh"
pgit init -q -b main "$solo/r"; cp "$ELF" "$solo/r/root.bin"; pgit -C "$solo/r" add root.bin
pgit -C "$solo/r" commit -q -m "#$NUM корень"; pgit -C "$solo/r" config log.showRoot false
examined=$((examined + 1)); unlawful=$((unlawful + 1)); code=0
(cd "$solo/r" && sb bash "$solo/g/prepush-added-files.sh" origin < /dev/null > "$TMP/solo.out" 2>&1) || code=$?
{ [ "$code" -eq 1 ] && grep -qF 'root.bin' "$TMP/solo.out"; } ||
    fail "ELF в корневом коммите при log.showRoot=false: страж вышел $code вместо 1 либо не назвал root.bin"
fi

echo "prepush-added-files-inject: осмотрено проб $examined (законных $lawful, отвергаемых и не судимых $unlawful); находок $findings" >&2
[ "$findings" -eq 0 ] || exit 1
echo "prepush-added-files-inject: ЗЕЛЁНОЕ — хук отвергает бинарь и файл больше 1 МБ среди новых blob'ов до обхода и называет файл; перечень с доводом и без предмета судится; законные близнецы проходят; пустой обход — не зелёный" >&2
