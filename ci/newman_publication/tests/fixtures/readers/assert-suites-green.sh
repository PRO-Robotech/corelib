#!/usr/bin/env bash

# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

# tests/newman/scripts/assert-suites-green.sh — вердиктный гейт сквозных наборов,
# ОДИН на дерево. Запускается с cwd = каталог проверяемого набора newman
# (`<владелец>/tests/newman`): рядом с ним лежат `collections/` и `out/`, и гейт
# глобит их относительно cwd — поэтому один и тот же скрипт судит любой из
# восьми наборов, ничего о нём не зная заранее.
#
# ГДЕ ОН ЛЕЖИТ И ПОЧЕМУ ЗДЕСЬ. Предмет у него ОДИН на дерево, значит и мест
# должно быть одно — тот же довод, по которому сюда переехали общий слой
# генератора (`tests/newman/kacholib/gen_shared.py`) и отбор коллекций
# (`kacholib/stems.sh`). Прежде гейт лежал ВНУТРИ одной из суит, и это было
# видно снаружи: семь шагов конвейера из восьми звали его абсолютным путём в
# чужой каталог, а восьмой — относительным, потому что у него он был своим.
# Каталог `kacholib/` при этом остаётся домом ОБЪЯВЛЕНИЙ (их импортируют и
# берут через `source`); здесь живут ИСПОЛНЯЕМЫЕ судьи и их пробы — та же
# раскладка, что у `scripts/` каждой суиты, только уровнем выше.
#
# WHY this is shared (KAC — newman gate consolidation): each repo's
# newman-e2e.yml used to carry its own inline copy of the verdict logic, and the
# copies drifted — fixes for get-malformed (api-gateway#73), delete-binding
# (iam#108) and the user-per-account invite (iam#113, migration 0011) only ever
# reached kaname's copy, so vpc/compute/nlb/api-gateway/deploy stayed RED on the
# very same shared suites that kaname reported GREEN. One script = one source of
# truth; a change to the verdict lands everywhere at once.
#
# NOTHING IS SUBTRACTED FROM THE VERDICT. This gate reports the counts newman
# reported. The "known-RED" whitelist that used to sit in the loop below was
# removed 2026-07-30 after its third narrowing — see the note at the subtraction
# site for why narrowing could never fix it. Outstanding red is carried as a
# counted, named debt in docs/RESULTS.md, not as a silent deduction here.
#
# FOUR OUTCOMES, FOUR NAMES. A request ends one of four ways, and this gate
# reports each separately because folding any of them into another is how a suite
# goes quiet:
#   answered + assertion passed  — the check ran and said yes;
#   answered + assertion failed  — the check ran and said no      (`failed`);
#   answered + SCRIPT CRASHED    — the check never got to run     (`SCRIPT FAILED`);
#   NOT ANSWERED                 — nothing came back              (`UNANSWERED`).
# Plus the degenerate case of a report containing no assertions at all, which is
# printed as NOTHING RAN rather than being allowed to read as "nothing failed".
# None of the three non-passing outcomes is ever subtracted, whitelisted or
# explained away.
#
# ПЯТЫЙ ИСХОД: УСЛОВИЕ НЕ СОЗДАНО — и он НЕ вердикт о продукте.
#
# Страж адреса (`gen.py::require_env_url`) роняет утверждение, когда прогонщик не
# инъектировал переменную с адресом поверхности. Это единственное, что вообще
# доезжает до отчёта от пропущенного запроса: пропуск сам по себе следа не
# оставляет. Но слот у него был ОБЩИЙ с находкой — `assertions.failed`, — и пока
# это так, «адрес не объявлен харнессу» неотличимо от «поверхность сломана» ни
# глазом, ни машиной. Наблюдалось числом: 22 таких утверждения покрасили шард iam
# целиком, и все 22 читались как находки о продукте (#2169).
#
# Такие утверждения несут МЕТКУ, которую ставит ровно один блок дерева
# (`gen.py::PRECONDITION_MARK`, единственность держит
# tests/newman/scripts/precondition_mark_test.py). Здесь они считаются ОТДЕЛЬНО и печатаются
# отдельной строкой.
#
# ЭТО НЕ ВЫЧИТАНИЕ, И РАЗНИЦА НЕ В СЛОВАХ. Снятый отсюда «известно-красный»
# whitelist делал красное ЗЕЛЁНЫМ, угадывая причину по чужому имени шага. Здесь
# ничто не становится зелёным: прогон остаётся ненулевым, суита остаётся
# названной, число печатается. Меняется КАТЕГОРИЯ, в которой читают исход, и
# КОД ВОЗВРАТА, которым он приходит вызывающему.
#
# КОДОВ ВОЗВРАТА ТРИ, И НАХОДКА ОБЪЯВЛЯЕТСЯ ПЕРВОЙ:
#   0 — зелено;
#   1 — НАХОДКА: есть падение, не помеченное как «условие не создано», либо
#       незаданный ответ, либо упавший скрипт, либо пустой отчёт;
#   3 — УСЛОВИЕ НЕ СОЗДАНО: единственное, что не сошлось, — предмет, который
#       обязан был создать харнесс (адрес поверхности от прогонщика, субъект от
#       посева фикстур). Вердикта о продукте у этого прогона нет.
# Тройка НИКОГДА не приходит вместе с находкой: прогон, где есть и то и другое,
# отдаёт 1 — иначе категория стала бы маской. Тот же порядок, что у кода 3
# гейта покрытия исполнения ниже: оба останавливают конвейер и посылают
# читателя в РАЗНЫЕ места.
#
# The third one was missing until 2026-07-29, and it was invisible by construction:
# newman books a test-script exception under `scripts`/`testScripts`, NOT under
# `assertions.failed` (an assertion that never ran cannot fail) and NOT under
# `requests.failed` (a response did arrive). A case whose script threw on its first
# line therefore reached this gate reading exactly like a clean one — `0 failed,
# 0 UNANSWERED` — with none of its checks performed. Measured on newman 6.2.2: a body
# the script cannot parse yields `assertions.failed=0, requests.failed=0,
# testScripts.failed=1`.
set -e
shopt -s nullglob

collections=(collections/*.postman_collection.json)
if [ "${#collections[@]}" -eq 0 ]; then
  echo "FAIL: no collections generated under collections/"
  exit 1
fi

failed_suites=()

# МЕТКА ТРЕТЬЕГО ИСХОДА БЕРЁТСЯ У ПРОИЗВОДИТЕЛЯ ПРОВЕРЯЕМОЙ СУИТЫ.
#
# Её объявляет `<суита>/scripts/gen.py::PRECONDITION_MARK` — тот же блок, который
# её и ставит. Выписать её здесь вторым литералом значило бы завести два места об
# одном предмете: они разошлись бы МОЛЧА и разошлись бы там, где расхождение не
# видно — помеченные утверждения тихо вернулись бы в находки, то есть дефект,
# ради которого метка заведена, вернулся бы вместе с зелёным гейтом.
#
# АДРЕСАТ ЧТЕНИЯ — СУИТА, А НЕ КАТАЛОГ ГЕЙТА. Прежде здесь стояло
# `dirname "${BASH_SOURCE[0]}"`, то есть каталог, где лежит САМ гейт. Это
# работало ровно пока гейт лежал внутри одной из суит: он читал метку у её
# генератора и применял её к отчётам ЛЮБОЙ другой. Гейт один на дерево, лежит
# отдельно от суит и своего `gen.py` не имеет — по прежнему адресу читать нечего,
# и отказ наступал бы первой же строкой у всех восьми наборов сразу.
#
# Производитель есть у КАЖДОЙ суиты: метку объявляют все восемь генераторов, и
# единственность объявления внутри набора держит `precondition_mark_test.py`,
# а по всему дереву — `internal/repohygiene/newmanpreconditionmark_test.go`.
# Читать её у суиты, чьи отчёты судим, — единственная форма, при которой
# «производитель» и «предмет» относятся к одному набору.
#
# УМОЛЧАНИЯ У НЕЁ НЕТ. Не удалось прочитать — ОТКАЗ, а не «возьмём привычную
# строку»: подставленное умолчание сделало бы категорию беспредметной, ничем
# этого не показав. Отказ НАЗЫВАЕТ ПУТЬ: «гейт не там» и «у суиты нет
# генератора» — разные починки, и читатель журнала обязан их различать.
_suite_gen_dir="$PWD/scripts"
PRECONDITION_MARK="$(cd "$_suite_gen_dir" 2>/dev/null && python3 -c 'import gen; print(gen.PRECONDITION_MARK)' 2>/dev/null || true)"
if [ -z "$PRECONDITION_MARK" ]; then
  echo "FAIL: метку третьего исхода не прочитать у производителя ПРОВЕРЯЕМОЙ суиты (${_suite_gen_dir}/gen.py::PRECONDITION_MARK) — категория «условие не создано» стала бы беспредметной. Гейт запускается с cwd = каталог набора newman (рядом collections/ и out/); проверьте, что cwd тот." >&2
  exit 1
fi

# Totals across the suite. Printed as ONE line at the end so a reader gets the
# whole verdict in numbers without adding up per-collection lines: how many
# collections produced a report OUT OF how many exist, plus requests, assertions,
# failed assertions and UNANSWERED requests. "Collections reported out of
# generated" is the number that makes a silently truncated run visible — every
# other counter looks healthy when a collection simply never ran.
tot_reported=0
tot_requests=0
tot_asserts=0
tot_fails=0
tot_scripts=0
tot_unanswered=0
tot_empty=0
# Отдельный счётчик пятой категории — см. шапку. Он НЕ вычитается из tot_fails:
# оба числа печатаются, и читатель видит и целое, и его помеченную часть.
tot_precond=0
# Нашлось ли хоть что-то, что вердиктом О ПРОДУКТЕ является. Только это решает,
# приходит ли вызывающему 1 вместо 3.
saw_finding=0

# ─── Execution-coverage gate ─────────────────────────────────────────────────
# The assertion-based verdict below can only see requests that RAN. A request
# that never executed produces no assertions and therefore no failures — so a
# collection that stopped after 6 of its 46 requests reads exactly like one that
# ran all 46 cleanly, and this gate called both GREEN.
#
# That was not hypothetical: a poll pre-request guard used
# `setNextRequest(null)` meaning "skip this poll when the create was rejected and
# no operation id exists". `setNextRequest(null)` does not skip a request, it ENDS
# THE RUN — and the guard fires deterministically on negative cases. The iam suite
# executed 1177 of 1502 requests (78.4%) while every collection was gated GREEN;
# what went unexecuted was the authz-deny data-leak matrix, the ROLE/INV/USR/ESC
# deny matrices and the fail-closed OpenFGA-outage case.
#
# exec-coverage.py closes that: per collection it requires every leaf request to
# have executed or to be STATICALLY EXPLAINED (an explicit `skipRequest()` guard,
# or a literal forward `setNextRequest('<name>')` that jumps over it), and it bans
# `setNextRequest(null)` outright. It runs BEFORE the assertion loop because a
# truncated run makes the assertion counts meaningless.
#
# Its exit code carries THREE answers, not two, and they are kept apart here:
# 1 = coverage gaps found in this tree's run; 3 = there was no usable run to draw
# a coverage verdict from (out/ empty, or holding another tree's reports). Both
# stop the pipeline, but they send a reader to different places — one to a suite
# that lost requests, the other to a runner that did not produce evidence. Folding
# 3 into the same label would reintroduce, one level up, exactly the confusion the
# gate now refuses to commit: an unasked question wearing an answer's name.
_gate_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "${_gate_dir}/exec-coverage.py" ] && command -v python3 >/dev/null 2>&1; then
  _cov_rc=0
  python3 "${_gate_dir}/exec-coverage.py" \
        --collections-glob 'collections/*.postman_collection.json' \
        --out-dir out || _cov_rc=$?
  case "$_cov_rc" in
    0) ;;
    3) failed_suites+=("execution-coverage(did-not-run)") ;;
    *) failed_suites+=("execution-coverage") ;;
  esac
  echo
else
  echo "FAIL: exec-coverage.py missing next to the gate — execution coverage unverified" >&2
  exit 1
fi

for col in "${collections[@]}"; do
  name=$(basename "$col" .postman_collection.json)
  report="out/${name}.json"
  if [ ! -f "$report" ]; then
    echo "WARN: no report for $name (newman didn't run for this suite)"
    failed_suites+=("$name(no-report)")
    continue
  fi
  fails=$(jq -r '.run.stats.assertions.failed // 0' "$report")
  # UNANSWERED — requests newman attempted that never produced a response (DNS,
  # refused connection, TLS, timeout). NEVER subtracted, for any reason.
  #
  # A request has three possible ends, and this gate used to have names for only
  # two: a response arrived (whatever its status), an assertion said no, or
  # NOTHING CAME BACK. The third had no home, so it was filed under the first.
  #
  # What stood here was a filter that removed EAI_AGAIN/ENOTFOUND/getaddrinfo
  # from this count, on the stated grounds that an unreachable endpoint IS the
  # internal-only invariant being asserted. It is not. "I could not reach it"
  # and "it refused me" are different findings, and only one of them is evidence.
  # The filter, together with a case assertion phrased "if the status code is
  # undefined, return" (a pass) and an execution-coverage check that read a
  # cursor position as proof of execution, meant all three layers reported
  # success for eight ban-#6 negatives — the checks that Internal* RPCs are not
  # reachable on the advertised external endpoint — while none of them ran. The
  # verdict printed "0 failed requests".
  #
  # The harness now reaches the external listener (deploy/scripts/newman-*.sh
  # forward the api-gateway TLS port and inject externalBaseUrl), so an
  # unanswered request here means the harness is broken or the endpoint is down.
  # Either is worth stopping for; neither is a green check.
  unanswered=$(jq -r '.run.stats.requests.failed // 0' "$report")
  # ANSWERED/ASSERTED — printed so that "nothing ran" is visibly different from
  # "nothing failed". Zero of both is the shape every silent failure took.
  asserts=$(jq -r '.run.stats.assertions.total // 0' "$report")
  reqs=$(jq -r '.run.stats.requests.total // 0' "$report")
  # SCRIPT FAILED — the response arrived and the case's own script threw BEFORE
  # reaching its assertions. A fourth end for a request, and it had no home here: it
  # is not `assertions.failed` (nothing was asserted) and not `requests.failed`
  # (something was answered), so a case that checked NOTHING was indistinguishable
  # from a case that checked everything and agreed.
  #
  # Pre-request scripts count too, for a sharper reason than symmetry: in these suites
  # the pre-request script is what upserts the Authorization header naming the SUBJECT
  # the case is about (gen.py::_auth_pre_script). A crash there does not skip the
  # request — it sends it with whatever header was already on it, i.e. as a DIFFERENT
  # principal, and the expected 401/403 then still holds for the wrong reason.
  #
  # NEVER subtracted and never whitelisted: an exception is not a known-RED product
  # gap, it is a case that did not run.
  scripts_failed=$(jq -r '((.run.stats.testScripts.failed // 0) + (.run.stats.prerequestScripts.failed // 0))' "$report")

  # NO SUBTRACTION. The failure count printed and gated below is the one newman
  # reported, whole.
  #
  # What stood here was a "known-RED" whitelist: a jq alternation over case names
  # whose matches were subtracted from `fails` before the verdict. It was narrowed
  # twice and removed on the third audit, and the reason it had to go is the shape
  # of those three revisions rather than the size of any one of them.
  #
  #   v1 subtracted BY FOLDER — 259 assertions across 17 folders, and a folder is
  #      not a finding. It swallowed a P1 leak canary that happened to sit in a
  #      matched folder.
  #   v2 subtracted BY STEP inside those folders — smaller, same kind.
  #   v3 subtracted by STEP-NAME SUFFIX (`-rya<N>`, `get-abs<N>`) — 27 assertions,
  #      including a placement-coherence NEGATIVE
  #      (LST-UPD-STATE-DEFAULT-TG-REGION-MISMATCH).
  #
  # Each narrowing was a correct local fix and none of them touched the defect. The
  # predicate keys on a NAME, and the thing it claims to select — "this failure is
  # the known materialization lag, not a real refusal" — is a statement about the
  # REASON a step failed. The reason is not in the name. So the predicate cannot
  # distinguish its subject from a stranger in principle, at any width, and every
  # narrowing just reduced how many strangers it caught per run without changing
  # that it catches them. A fourth narrowing would have been the same move again.
  #
  # A mechanism that DOES key on the reason already exists and stays: the bounded
  # per-step retry (`retry_until_authorized` / `retry_until_absent` / the poll
  # loops) — it retries while the response says the specific transient thing, and
  # when the budget is spent the real assertion runs on the terminal response, so a
  # genuine deny still fails. That is a check about behaviour; a name-match is not.
  #
  # The debt this uncovers is not hidden, it is COUNTED: docs/RESULTS.md carries the
  # case list and the number. A red on a real disagreement is worth more than a
  # green obtained by not asking (testing.md — "E2E НИКОГДА не пропускаются").
  #
  # HARNESS-CONFIG failures were the one class this whitelist had to explicitly
  # exclude from itself, because 15 guarded requests sat under names it matched.
  # With no subtraction there is nothing to exclude from: they simply count.

  # УСЛОВИЕ НЕ СОЗДАНО — помеченная часть `fails`. Считается по МЕТКЕ в имени
  # утверждения: имя производит тот же блок, что и само утверждение, поэтому
  # причина здесь не угадывается — она объявлена её автором (см. шапку).
  precond=$(jq -r --arg m "$PRECONDITION_MARK" \
    '[.run.failures[]? | select((.error.name? // "") == "AssertionError")
      | select(((.error.test? // "") | startswith($m)))] | length' "$report")
  # Находка = всё, что упало и меткой НЕ помечено. Считается вычитанием, но
  # вычитается оно из числа ПАДЕНИЙ ради КЛАССИФИКАЦИИ, а не из вердикта:
  # ноль находок при непустом precond всё равно останавливает конвейер (код 3).
  findings=$((fails - precond))
  [ "$findings" -lt 0 ] && findings=0

  tot_reported=$((tot_reported + 1))
  tot_requests=$((tot_requests + reqs))
  tot_asserts=$((tot_asserts + asserts))
  tot_fails=$((tot_fails + fails))
  tot_scripts=$((tot_scripts + scripts_failed))
  tot_unanswered=$((tot_unanswered + unanswered))
  tot_precond=$((tot_precond + precond))

  echo "$name: ran $reqs request(s) / $asserts assertion(s) — $fails failed (из них $precond УСЛОВИЕ НЕ СОЗДАНО, находок $findings), $scripts_failed SCRIPT FAILED, $unanswered UNANSWERED"

  # Name what did not answer. A count alone cannot be acted on, and the whole
  # point of separating this category is that somebody reads it.
  # Request-level errors are the ones carrying a transport errno (`error.code`:
  # ECONNREFUSED, ENOTFOUND, ETIMEDOUT…); a script exception carries none, which is
  # what keeps these two listings from describing each other's entries.
  if [ "$unanswered" -gt 0 ]; then
    jq -r '[.run.failures[]? | select((.error.name? // "") != "AssertionError")
            | select((.error.code? // "") != "")
            | "    NOT EXECUTED: \(.source.name? // "?") <- \(.error.message? // "no response")"]
           | unique | .[]' "$report" 2>/dev/null || true
  fi

  # Name what crashed. The message is the whole diagnosis here — "Unexpected token
  # '<'" says the body was not JSON, "X is not defined" says the script was edited
  # without being run — so it is printed rather than counted.
  if [ "$scripts_failed" -gt 0 ]; then
    jq -r '[.run.failures[]? | select((.error.name? // "") != "AssertionError")
            | select((.error.code? // "") == "")
            | "    SCRIPT FAILED (its assertions never ran): \(.source.name? // "?") <- \(.error.name? // "?"): \(.error.message? // "")"]
           | unique | .[]' "$report" 2>/dev/null || true
  fi

  # NOTHING RAN. A report with no assertions is not a clean suite, it is a suite
  # that did not happen — and it reaches this loop reading exactly like a perfect
  # one (0 failed, 0 unanswered). Distinguishing the two is the reason this line
  # exists; without it "zero executed" and "zero failed" are the same verdict.
  empty=0
  if [ "$asserts" -eq 0 ]; then
    echo "    NOTHING RAN: $name produced 0 assertions — a suite that asserts nothing cannot pass"
    empty=1
    tot_empty=$((tot_empty + 1))
  fi

  if [ "$fails" -gt 0 ] || [ "$scripts_failed" -gt 0 ] || [ "$unanswered" -gt 0 ] || [ "$empty" -gt 0 ]; then
    failed_suites+=("$name")
  fi
done

# The verdict in numbers, on one line. Read the FIRST pair first: a run that
# stopped early leaves every other counter looking healthy.
tot_findings=$((tot_fails - tot_precond))
[ "$tot_findings" -lt 0 ] && tot_findings=0
echo "TOTAL: ${tot_reported}/${#collections[@]} collection(s) reported, ${tot_requests} request(s), ${tot_asserts} assertion(s), ${tot_fails} failed, ${tot_scripts} SCRIPT FAILED, ${tot_unanswered} UNANSWERED, ${tot_empty} report(s) with no assertions"
# Пятая категория — СВОЕЙ строкой, и обе величины рядом: «падений N, из них K
# условие не создано, находок N-K». Одно число скрывает ровно тот случай, ради
# которого категория заведена.
echo "TOTAL(категории): находок ${tot_findings}, УСЛОВИЕ НЕ СОЗДАНО ${tot_precond} (метка «${PRECONDITION_MARK}», производитель scripts/gen.py)"
# Находка — это ЛЮБОЙ исход, о продукте говорящий: непомеченное падение, запрос
# без ответа, упавший скрипт, пустой отчёт, отсутствующий отчёт. Последнее уже
# лежит в failed_suites, поэтому здесь считается остальное.
if [ "$tot_findings" -gt 0 ] || [ "$tot_scripts" -gt 0 ] || [ "$tot_unanswered" -gt 0 ] || [ "$tot_empty" -gt 0 ]; then
  saw_finding=1
fi
# Эти три исхода МЕТКОЙ не покрыты, и притворяться, что покрыты, нельзя:
# отчёта нет вовсе · покрытие исполнения не сошлось · у покрытия не было
# пригодного прогона. Первые два — вердикт о дереве; третий — своя третья
# категория, у неё СВОЁ имя и свой адресат, и сводить её в эту значило бы
# завести одно имя на два разных «не выполнилось». Поведение у них прежнее
# (код 1), и меняется оно своим изменением, а не мимоходом этим.
for _s in "${failed_suites[@]:-}"; do
  case "$_s" in *"(no-report)"|*"(did-not-run)"|execution-coverage) saw_finding=1 ;; esac
done

# The same numbers into the job summary when running in CI. Each gate step is
# short, so these blocks land in the interface one after another AS THE STEPS
# FINISH — which is what makes the summary readable during a run, unlike a single
# write at the very end. Each block is self-contained (no shared table header
# somebody else has to have written first).
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  _label="${SUITE_LABEL:-$PWD}"
  # `if`, not `a && b`: under `set -e` a false test at statement level aborts the
  # script — and the abort would happen right where the numbers are printed.
  if [ "${#failed_suites[@]}" -gt 0 ]; then
    _verdict="RED — ${failed_suites[*]}"
  else
    _verdict="GREEN"
  fi
  {
    printf '### гейт `%s` — %s\n\n' "$_label" "$_verdict"
    printf 'коллекций с отчётом **%s из %s** · запросов %s · утверждений %s · **упало %s** · скрипт упал %s · без ответа %s · пустых отчётов %s\n\n' \
      "$tot_reported" "${#collections[@]}" "$tot_requests" "$tot_asserts" \
      "$tot_fails" "$tot_scripts" "$tot_unanswered" "$tot_empty"
    printf 'из них **находок %s**, **УСЛОВИЕ НЕ СОЗДАНО %s** — второе вердиктом о продукте не является\n\n' \
      "$tot_findings" "$tot_precond"
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ "${#failed_suites[@]}" -gt 0 ]; then
  if [ "$saw_finding" -eq 0 ] && [ "$tot_precond" -gt 0 ]; then
    # ТРЕТИЙ ИСХОД. Не зелено и не красно: вердикта о продукте у этого прогона
    # нет вовсе. Конвейер останавливается ровно так же (код ненулевой), но
    # читателя посылает в ДРУГОЕ место — к харнессу, который не создал условия,
    # а не к продукту, который никто не спрашивал.
    #
    # АДРЕСАТ НАЗЫВАЕТСЯ ОБА, а не один. Прежде здесь стоял «страж адреса» и
    # «адреса поверхностей инъектирует прогонщик» — верно, пока производителем
    # метки был один блок. Их стало три (адрес поверхности · субъект посева ·
    # производное удостоверение, #2187), и текст, назвавший одного, посылал бы
    # читателя чинить прогонщик там, где не отработал ПОСЕВ.
    echo "УСЛОВИЕ НЕ СОЗДАНО: ${tot_precond} утверждени(й) стража настройки харнесса не получили своего предмета; suites: ${failed_suites[*]}"
    echo "Это НЕ вердикт о продукте и НЕ зачёт: адреса поверхностей инъектирует прогонщик (deploy/scripts/newman-e2e.sh / newman-parallel.sh), субъектов выдаёт посев фикстур (tests/authz-fixtures/setup.sh) — предмет не создал кто-то из них. Имя переменной названо в самом утверждении."
    exit 3
  fi
  echo "FAIL: suites with failures: ${failed_suites[*]}"
  exit 1
fi
echo "All ${#collections[@]} suites GREEN."
