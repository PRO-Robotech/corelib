#!/usr/bin/env bash
# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: Apache-2.0
#
# Страж НОВЫХ BLOB'ОВ отправки (#117; образец и родитель —
# kacho-workspace#1189). Зовёт его scripts/hooks/pre-push вторым, после стража
# правила, — до обхода CORELIB_SKIP_PREPUSH. Проба —
# scripts/hooks/prepush-added-files-inject.sh.
#
# Признак: в kacho#3284 коммит ветки нёс собранный ELF края на 39 МБ со вшитыми
# путями машины; поймало его ревью, не хук. Репозитории публичны, и blob в
# опубликованной истории не отзывается без её переписи.
#
# ПРЕДМЕТ — новый blob в публичной истории, а не статус файла: судится КАЖДЫЙ
# blob, достижимый из коммитов отправки и недостижимый со ссылок удалённого и
# с прежней вершины ссылки (`git rev-list --objects --filter=object:type=blob`).
# Добавление, правка, переименование с правкой, смена типа, файл, добавленный и
# снятый следующим коммитом, внесённое руками в слияние, корневой коммит — всё
# это новые blob'ы. Переименование и копия БЕЗ правки нового blob'а не вносят
# (содержимое уже опубликовано) и не судятся — одинаково при любой настройке
# `diff.renames`. Ссылок удалённого нет — судится вся история вершины.
#
# НЕЗАВИСИМОСТЬ ОТ НАСТРОЕК ПОЛЬЗОВАТЕЛЯ. Перепись — плюмбинг `rev-list`, его не
# трогают `log.*` и `diff.*`. Бинарность — НУЛЕВОЙ байт в первых 8000 байтах
# содержимого, прочитанного `git cat-file --batch`: ни атрибуты дерева, ни
# `.git/info/attributes`, ни `core.attributesFile` её не перекрашивают. Размер —
# из того же заголовка. Режим (исполняемый) и коммит для текста отказа — из
# `git log --raw -z --root --no-renames --diff-merges=first-parent`; они лишь
# называют находку и вердикт не решают.
#
# Отказ называет файл: бинарный (исполняемый — режим 100755; вид — `file`) либо
# больше 1 МБ. Исключение — только строкой перечня `prepush-added-files.allow`
# рядом, с доводом; строка, под шаблон которой не подпадает ни один файл дерева
# судимой вершины, — находка: исключение истекает вместе с предметом. Перечня
# нет — исключений нет.
#
# Вход — строки git `<local_ref> <local_sha> <remote_ref> <remote_sha>` на stdin;
# $1 — имя удалённого (по умолчанию origin). Пустой вход — судится HEAD.
# ИСХОДЫ: 0 — отвергнутых нет (перепись напечатана); 1 — отвергнутые файлы либо
# негодный перечень, у каждого имя; 2 — судить не смог (нет python3, вершина не
# сводится к коммиту, git не прочитал диапазон): пустой обход зелёным не бывает.
set -uo pipefail

here="${BASH_SOURCE[0]%/*}"
[ "$here" != "${BASH_SOURCE[0]}" ] || here=.
input=""
[ -t 0 ] || input="$(cat)"
if ! command -v python3 > /dev/null 2>&1; then
    echo "prepush-added-files: НЕ СУДИМО — нет python3. Пустой обход зелёным не бывает." >&2
    exit 2
fi
PREPUSH_INPUT="$input" exec python3 - "${1:-origin}" "$here/prepush-added-files.allow" <<'PY'
import fnmatch, os, subprocess, sys, tempfile

remote, allow_path = sys.argv[1], sys.argv[2]
LIMIT = 1048576
PROBE = 8000


def say(m):
    print('prepush-added-files: ' + m, file=sys.stderr)


def void(m):
    say('НЕ СУДИМО — ' + m + '. Пустой обход зелёным не бывает.')
    sys.exit(2)


def git(args, inp=None):
    r = subprocess.run(['git', '-c', 'core.quotePath=false'] + args, input=inp, capture_output=True)
    if r.returncode != 0:
        err = r.stderr.decode('utf-8', 'replace').strip().split('\n')[0]
        void('git ' + ' '.join(args[:2]) + ' вышел ' + str(r.returncode) + ': ' + err)
    return r.stdout


targets, prev = [], []
inp = os.environ.get('PREPUSH_INPUT', '')
if inp.strip():
    for line in inp.splitlines():
        f = line.split()
        if len(f) < 2 or not f[1].strip('0'):
            continue
        targets.append(f[1])
        # Прежняя вершина ссылки на удалённом — уже опубликованное.
        if len(f) > 3 and f[3].strip('0') and subprocess.run(
                ['git', 'cat-file', '-e', f[3] + '^{commit}'], capture_output=True).returncode == 0:
            prev.append('^' + f[3])
    if not targets:
        say('только снятие ссылок — коммитов не везёт, судить нечего')
        sys.exit(0)
else:
    targets = ['HEAD']
    say('вход пуст — судится HEAD')
tcs = []
for t in targets:
    r = subprocess.run(['git', 'rev-parse', '--verify', '-q', t + '^{commit}'], capture_output=True, text=True)
    if r.returncode != 0:
        void('вершина «' + t + '» не сводится к коммиту')
    tcs.append(r.stdout.strip())
rng = tcs + prev + ['--not', '--remotes=' + remote]

n_new = len(git(['rev-list'] + rng).split())
# Новые blob'ы: sha → первый путь, под которым rev-list его встретил.
new = {}
for line in git(['rev-list', '--objects', '--filter=object:type=blob'] + rng).decode('utf-8', 'surrogateescape').splitlines():
    sha, _, path = line.partition(' ')
    if path:
        new.setdefault(sha, path)
# Имя, режим и коммит находки — из записей коммитов, где blob появился.
cands = {}
cur = ''
toks = git(['log', '--no-color', '--no-renames', '--root', '--no-ext-diff', '--no-textconv',
            '--no-relative', '--raw', '--no-abbrev', '-z', '--diff-merges=first-parent',
            '--format=@%H'] + rng).decode('utf-8', 'surrogateescape').split('\0')
i = 0
while i < len(toks):
    t = toks[i].lstrip('\n')
    if t.startswith('@'):
        cur = t[1:]
    elif t.startswith(':') and i + 1 < len(toks):
        meta = t[1:].split()
        mode, blob, path = meta[1], meta[3], toks[i + 1]
        i += 1
        if blob in new:
            cands.setdefault((blob, path), (mode, cur))
    i += 1
seen = {b for b, _ in cands}
for blob, path in new.items():
    if blob not in seen:
        cands[(blob, path)] = ('', '')

# Содержимое — одним `cat-file --batch`: размер из заголовка, бинарность — NUL в начале.
blobs = sorted({b for b, _ in cands})
info = {}
if blobs:
    # Вход — файлом, а не трубой: запись в трубу при непрочитанном выводе встала бы
    # насмерть, как только вывод заполнит буфер (тысячи blob'ов без ссылок удалённого).
    req = tempfile.TemporaryFile()
    req.write(('\n'.join(blobs) + '\n').encode())
    req.seek(0)
    p = subprocess.Popen(['git', 'cat-file', '--batch'], stdin=req, stdout=subprocess.PIPE)
    for _ in blobs:
        head = p.stdout.readline().split()
        if len(head) != 3:
            void('cat-file не отдал blob: ' + b' '.join(head).decode('utf-8', 'replace'))
        size = int(head[2])
        data = p.stdout.read(size)
        p.stdout.read(1)
        info[head[0].decode()] = (size, b'\0' in data[:PROBE], data[:PROBE])
    if p.wait() != 0:
        void('cat-file --batch вышел ' + str(p.returncode))

# Перечень исключений: `<вид> <шаблон> <довод>`, вид — size | binary.
allow, allow_bad = [], []
if os.path.isfile(allow_path):
    for line in open(allow_path, encoding='utf-8'):
        line = line.rstrip('\n')
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        f = line.split(None, 2)
        if f[0] not in ('size', 'binary'):
            allow_bad.append('«' + line + '» — вид не size и не binary')
        elif len(f) < 3 or not f[2].strip():
            allow_bad.append('«' + line + '» — без довода')
        else:
            allow.append([f[0], f[1], False])


def allowed(kind, path):
    return any(a[0] == kind and fnmatch.fnmatchcase(path, a[1]) for a in allow)


bad, excused = [], 0
for (blob, path), (mode, commit) in sorted(cands.items(), key=lambda x: x[0][1]):
    size, is_bin, head = info[blob]
    what = []
    if is_bin and not allowed('binary', path):
        kind = subprocess.run(['file', '-b', '-'], input=head, capture_output=True).stdout.decode('utf-8', 'replace').split(',')[0].strip()
        what.append(('исполняемый бинарь' if mode == '100755' else 'бинарный') + ' (' + kind + ')')
    if size > LIMIT and not allowed('size', path):
        what.append(str(size) + ' Б > порога 1 МБ (' + str(LIMIT) + ' Б)')
    if what:
        bad.append(path + ' — ' + ' '.join(what) + ' · коммит ' + (commit[:12] or '—'))
    elif is_bin or size > LIMIT:
        excused += 1
# Исключение без предмета: под шаблон не подпадает ни один файл дерева вершины.
if allow:
    for tc in tcs:
        for path in git(['ls-tree', '-r', '--name-only', '-z', tc]).decode('utf-8', 'surrogateescape').split('\0'):
            for a in allow:
                if path and not a[2] and fnmatch.fnmatchcase(path, a[1]):
                    a[2] = True
for a in allow:
    if not a[2]:
        allow_bad.append(a[0] + ' ' + a[1] + ' — под шаблон не подпадает ни один файл судимой вершины: исключать нечего')

say('вершин ' + str(len(tcs)) + ', новых коммитов ' + str(n_new) + ', новых blob ' + str(len(new))
    + ' (пар blob·путь ' + str(len(cands)) + '; по перечню ' + str(excused) + ', отвергнуто ' + str(len(bad))
    + '); строк перечня ' + str(len(allow)))
rc = 0
if allow_bad:
    say('ОТКАЗ — перечень исключений ' + allow_path + ' негоден:')
    for x in allow_bad:
        print('  ' + x, file=sys.stderr)
    rc = 1
if bad:
    say('ОТКАЗ — новые blob\'ы, которым не место в публичной истории:')
    for x in bad:
        print('  ' + x, file=sys.stderr)
    print('  Бинарь собирать вне дерева (go build -o "$TMPDIR/…"), коммитить по именам, а не git add -A.', file=sys.stderr)
    print('  Коммит, внёсший blob, его уже несёт: удаление следующим коммитом историю не очищает —', file=sys.stderr)
    print('  соберите ветку заново от базы без него. Законный крупный текст или бинарь — строкой', file=sys.stderr)
    print('  «<size|binary> <шаблон> <довод>» в ' + allow_path + '.', file=sys.stderr)
    rc = 1
sys.exit(rc)
PY
