// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package migratorcli_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/migratorcli"
)

// TestFlagIsAcceptedInBothPositions — несущая проба задачи #1461.
//
// Прямая форма мигратора разбирала аргументы стандартным flag: разбор
// останавливается на первом не-флаге, поэтому флаг ПОСЛЕ подкоманды
// отбрасывался БЕЗ ЕДИНОГО СЛОВА. Опыт, воспроизводящий прежнее поведение
// дословно (flag.FlagSet + Parse + Args):
//
//	--dsn XXX up  -> dsn="XXX" direction="up"
//	up --dsn XXX  -> dsn=""    direction="up"
//
// Второй исход означает накат не на ту базу: пустой DSN уезжает в запасной
// путь (окружение, потом конфигурация сервиса), и оператор получает УСПЕХ на
// чужой схеме. Делегирующая тройка (cobra) принимает оба порядка — проверено
// прогоном настоящих команд vpc.
//
// Утверждается РАВЕНСТВО двух порядков, а не «флаг разобран»: именно равенство
// и есть предмет задачи — оператор, знающий один сервис, применяет знание к
// соседнему.
func TestFlagIsAcceptedInBothPositions(t *testing.T) {
	const dsn = "postgres://u:p@h:5432/db?sslmode=require"

	before, err := migratorcli.Parse("kacho-migrator", []string{"--dsn", dsn, "up"})
	if err != nil {
		t.Fatalf("флаг ПЕРЕД подкомандой отвергнут: %v", err)
	}
	after, err := migratorcli.Parse("kacho-migrator", []string{"up", "--dsn", dsn})
	if err != nil {
		t.Fatalf("флаг ПОСЛЕ подкоманды отвергнут: %v", err)
	}
	// Отказ называет ПРИЧИНУ, а не симптом: два дампа структуры рядом заставляют
	// читателя искать различие глазами, а найти надо ПОЛЕ, которое разошлось, и
	// то, чем это оборачивается для оператора. Поля перебираются разбором типа,
	// а не поимённо: поле, добавленное завтра, обязано попасть в отказ само.
	if before != after {
		vBefore, vAfter := reflect.ValueOf(before), reflect.ValueOf(after)
		for i := 0; i < vBefore.NumField(); i++ {
			gotBefore := vBefore.Field(i).Interface()
			gotAfter := vAfter.Field(i).Interface()
			if reflect.DeepEqual(gotBefore, gotAfter) {
				continue
			}
			t.Errorf("флаг ПОСЛЕ подкоманды отброшен молча: поле %s — до подкоманды %#v, "+
				"после подкоманды %#v. Пустое значение уезжает в запасной путь (окружение, "+
				"затем конфигурация сервиса), поэтому накат идёт на ЧУЖУЮ базу и выглядит "+
				"успехом", vBefore.Type().Field(i).Name, gotBefore, gotAfter)
		}
		t.FailNow()
	}
	if after.DSN != dsn {
		t.Fatalf("DSN потерян при разборе: %q", after.DSN)
	}
}

// TestFlagAfterCommandIsNeverDroppedSilently — форма `--flag=value` тоже.
// Именно её пишет services/nlb/Makefile (`up --dialect=postgres --dsn=…`).
func TestFlagAfterCommandIsNeverDroppedSilently(t *testing.T) {
	const dsn = "postgres://u:p@h:5432/db"
	got, err := migratorcli.Parse("kacho-migrator", []string{"up", "--dialect=postgres", "--dsn=" + dsn})
	if err != nil {
		t.Fatalf("склеенная форма флага отвергнута: %v", err)
	}
	if got.DSN != dsn {
		t.Fatalf("DSN потерян: %q", got.DSN)
	}
	if got.Dialect != "postgres" {
		t.Fatalf("диалект потерян: %q", got.Dialect)
	}
}

// TestTargetIsAcceptedOnUpAndDown — возможность, которой у прямой формы не было
// вовсе (#1461, различие 1). Решение о том, что она обязана быть у всех семи,
// записано в docs/architecture/migrator-cli.md.
func TestTargetIsAcceptedOnUpAndDown(t *testing.T) {
	for _, cmd := range []string{"up", "down"} {
		got, err := migratorcli.Parse("kacho-migrator", []string{cmd, "--target", "800001"})
		if err != nil {
			t.Fatalf("%s --target отвергнут: %v", cmd, err)
		}
		if got.Target != "800001" {
			t.Fatalf("%s: цель потеряна: %q", cmd, got.Target)
		}
		// Тот же флаг перед подкомандой — cobra его на корне НЕ знает, значит и
		// здесь он обязан быть отвергнут: равенство семи важнее удобства.
		if _, err := migratorcli.Parse("kacho-migrator", []string{"--target", "800001", cmd}); err == nil {
			t.Fatalf("%s: --target перед подкомандой принят, а cobra его отвергает", cmd)
		}
	}
}

// TestTargetIsRejectedOnStatus — у status цели нет ни в одной из семи точек.
func TestTargetIsRejectedOnStatus(t *testing.T) {
	_, err := migratorcli.Parse("kacho-migrator", []string{"status", "--target", "1"})
	if err == nil {
		t.Fatal("status --target принят, хотя у status цели не существует")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Fatalf("отказ не называет флаг: %v", err)
	}
}

// TestUnknownFlagIsRejectedExplicitly — в ОБЕИХ позициях. Тихо принятый
// неизвестный флаг — тот же класс, ради которого задача заведена.
func TestUnknownFlagIsRejectedExplicitly(t *testing.T) {
	for _, args := range [][]string{
		{"--nosuchflag", "up"},
		{"up", "--nosuchflag"},
	} {
		_, err := migratorcli.Parse("kacho-migrator", args)
		if err == nil {
			t.Fatalf("%v: неизвестный флаг принят молча", args)
		}
		if !strings.Contains(err.Error(), "nosuchflag") {
			t.Fatalf("%v: отказ не называет флаг: %v", args, err)
		}
	}
}

// TestExtraPositionalIsRejected — `up 800001` (догадка оператора о том, как
// задать цель) обязана получить ответ, а не молчаливый накат до головы.
func TestExtraPositionalIsRejected(t *testing.T) {
	_, err := migratorcli.Parse("kacho-migrator", []string{"up", "800001"})
	if err == nil {
		t.Fatal("лишний позиционный аргумент принят молча")
	}
	if !strings.Contains(err.Error(), "800001") {
		t.Fatalf("отказ не называет аргумент: %v", err)
	}
}

// TestUnknownCommandNamesTheKnownOnes — отказ обязан восстанавливать
// следующий шаг оператора.
func TestUnknownCommandNamesTheKnownOnes(t *testing.T) {
	_, err := migratorcli.Parse("kacho-migrator", []string{"upp"})
	if err == nil {
		t.Fatal("неизвестная подкоманда принята")
	}
	for _, want := range []string{"upp", "up", "down", "status"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("отказ не называет %q: %v", want, err)
		}
	}
}

// TestNoCommandIsRejectedAndUsageIsAvailable — положительный контроль отрицаний
// выше: без него все они зеленели бы на разборе, отвергающем вообще всё.
//
// Форма вызова больше не вшита в текст отказа: делегирующая форма печатает
// помощь отдельно, и вшитая усадка сделала бы первую строку отказа разной у
// семи. Печатает её теперь вызывающий — ровно как cobra печатает Help().
func TestNoCommandIsRejectedAndUsageIsAvailable(t *testing.T) {
	_, err := migratorcli.Parse("kacho-migrator", nil)
	if err == nil {
		t.Fatal("пустая командная строка принята")
	}
	if !errors.Is(err, migratorcli.ErrNoCommand) {
		t.Fatalf("отказ не опознаётся вызывающим: %v", err)
	}
	usage := migratorcli.Usage("kacho-migrator")
	for _, want := range []string{"usage", "kacho-migrator", "up", "down", "status", "--target"} {
		if !strings.Contains(usage, want) {
			t.Errorf("форма вызова не называет %q: %s", want, usage)
		}
	}
}

// TestReportErrorPrintsTheSharedShape — отказ подаётся в одной форме на семь.
// Прямая четвёрка печатала его через журнал, то есть с меткой времени впереди;
// делегирующая тройка — строкой `Error: …`. Проба закрепляет вторую.
func TestReportErrorPrintsTheSharedShape(t *testing.T) {
	var buf bytes.Buffer
	migratorcli.ReportError(&buf, migratorcli.UnknownFlagError("nosuchflag"))
	if got, want := buf.String(), "Error: unknown flag: --nosuchflag\n"; got != want {
		t.Fatalf("форма подачи отказа изменена:\n  получено: %q\n  контракт: %q", got, want)
	}
}

// TestHelpIsRequestable — у делегирующей тройки `--help` есть; равенство семи
// означает, что он есть и здесь, и это НЕ ошибка разбора.
func TestHelpIsRequestable(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"up", "--help"}} {
		if _, err := migratorcli.Parse("kacho-migrator", args); !errors.Is(err, migratorcli.ErrHelpRequested) {
			t.Fatalf("%v: помощь не запрошена, а получено: %v", args, err)
		}
	}
	if !strings.Contains(migratorcli.Usage("kacho-migrator"), "kacho-migrator") {
		t.Fatal("форма вызова не называет бинарь")
	}
}

// TestUnknownDialectIsRejected — единственное законное значение одно, и
// неверное обязано быть названо, а не проигнорировано.
func TestUnknownDialectIsRejected(t *testing.T) {
	if _, err := migratorcli.Parse("kacho-migrator", []string{"--dialect", "mysql", "up"}); err == nil {
		t.Fatal("чужой диалект принят")
	}
	got, err := migratorcli.Parse("kacho-migrator", []string{"up"})
	if err != nil {
		t.Fatalf("умолчание диалекта отвергнуто: %v", err)
	}
	if got.Dialect != "postgres" {
		t.Fatalf("умолчание диалекта не postgres: %q", got.Dialect)
	}
}

// TestResolveDSNPriority — --dsn > KACHO_MIGRATOR_DSN > конфигурация сервиса.
// Порядок был у трёх прямых из четырёх; у compute не было ни флага, ни
// переменной (#1461, различие 2).
func TestResolveDSNPriority(t *testing.T) {
	fromConfig := func() (string, error) { return "dsn-from-config", nil }

	t.Setenv("KACHO_MIGRATOR_DSN", "dsn-from-env")
	if got, err := migratorcli.ResolveDSN("dsn-from-flag", fromConfig); err != nil || got != "dsn-from-flag" {
		t.Fatalf("флаг не перекрыл окружение: %q, %v", got, err)
	}
	if got, err := migratorcli.ResolveDSN("", fromConfig); err != nil || got != "dsn-from-env" {
		t.Fatalf("окружение не перекрыло конфигурацию: %q, %v", got, err)
	}

	t.Setenv("KACHO_MIGRATOR_DSN", "")
	if got, err := migratorcli.ResolveDSN("", fromConfig); err != nil || got != "dsn-from-config" {
		t.Fatalf("запасной путь к конфигурации не сработал: %q, %v", got, err)
	}

	// Отказ конфигурации доезжает до оператора, а не превращается в пустой DSN.
	boom := func() (string, error) { return "", errors.New("config boom") }
	if _, err := migratorcli.ResolveDSN("", boom); err == nil || !strings.Contains(err.Error(), "config boom") {
		t.Fatalf("отказ конфигурации проглочен: %v", err)
	}
}

// TestRefusalTextsAreProducedInOnePlace — тон отказа часть контракта, и
// производитель у него ОДИН (#1461).
//
// Опыт по собранным бинарям на одинаковом входе показал, что предмет отказа
// называют все семь, а ФОРМА у них разная:
//
//	--nosuchflag up  → «unknown flag: --nosuchflag»              (делегирующая тройка)
//	                   «flag provided but not defined: -nosuchflag» (прямая четвёрка)
//	up 800001        → «unknown command …»  против «unexpected argument …»
//	любой отказ      → у прямой четвёрки впереди стояла метка времени
//
// Оператор читает эти строки глазами, а скрипт — образцом; две редакции одного
// отказа означают, что образец, написанный по одному сервису, на соседнем не
// срабатывает. Тексты сведены сюда, и делегирующая тройка берёт их ОТСЮДА —
// поэтому байт-идентичность держится построением, а не сверкой двух копий.
func TestRefusalTextsAreProducedInOnePlace(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{
			name: "неизвестная подкоманда",
			got:  migratorcli.UnknownCommandError("kacho-migrator", "upp").Error(),
			want: `unknown command "upp" for "kacho-migrator" (known: up, down, status)`,
		},
		{
			name: "лишний позиционный аргумент",
			got:  migratorcli.UnexpectedArgumentError("kacho-migrator up", "800001").Error(),
			want: `unexpected argument "800001" for "kacho-migrator up"; a version is given as --target 800001`,
		},
		{
			// Текст ЗАИМСТВОВАН у делегирующей формы дословно: её производит
			// библиотека, переписать её нельзя, а два текста об одном предмете
			// разошлись бы молча. Проба заодно ловит смену формулировки в
			// библиотеке — тогда разойдутся семь, и это станет видно здесь.
			name: "неизвестный флаг",
			got:  migratorcli.UnknownFlagError("nosuchflag").Error(),
			want: "unknown flag: --nosuchflag",
		},
		{
			name: "пустая командная строка",
			got:  migratorcli.ErrNoCommand.Error(),
			want: "no command given",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("тон отказа изменён:\n  получено: %q\n  контракт: %q", tc.got, tc.want)
			}
		})
	}
}

// TestParseSpeaksTheSharedRefusalTexts — разбор прямой формы отвечает ИМЕННО
// этими текстами, а не своими. Без этой пробы производитель существовал бы, а
// разбор продолжал бы печатать редакцию стандартной библиотеки.
func TestParseSpeaksTheSharedRefusalTexts(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--nosuchflag", "up"}, migratorcli.UnknownFlagError("nosuchflag").Error()},
		{[]string{"up", "--nosuchflag"}, migratorcli.UnknownFlagError("nosuchflag").Error()},
		{[]string{"status", "--target", "1"}, migratorcli.UnknownFlagError("target").Error()},
		{[]string{"upp"}, migratorcli.UnknownCommandError("kacho-migrator", "upp").Error()},
		{[]string{"up", "800001"}, migratorcli.UnexpectedArgumentError("kacho-migrator up", "800001").Error()},
		{nil, migratorcli.ErrNoCommand.Error()},
	} {
		_, err := migratorcli.Parse("kacho-migrator", tc.args)
		if err == nil {
			t.Fatalf("%v: принято молча", tc.args)
		}
		if err.Error() != tc.want {
			t.Errorf("%v: разбор говорит своей редакцией:\n  получено: %q\n  общая:    %q",
				tc.args, err.Error(), tc.want)
		}
	}
}

// TestHelpIsAlsoASubcommand — `help` понимается и прямой формой (#1461).
//
// Cobra доводит к дереву команду `help`, снять которую из перечня нельзя иначе
// как зарегистрировав скрытую команду под чужим именем. Равенство семи поэтому
// достигнуто с другой стороны: `kacho-migrator help` работает и там, где дерева
// команд нет вовсе. Иначе `help` оставался бы командой трёх сервисов из семи.
func TestHelpIsAlsoASubcommand(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"help", "up"}} {
		if _, err := migratorcli.Parse("kacho-migrator", args); !errors.Is(err, migratorcli.ErrHelpRequested) {
			t.Errorf("%v: помощь не запрошена, а получено: %v", args, err)
		}
	}
	// Положительный контроль: `help` — не подкоманда наката, и остальные три
	// по-прежнему разбираются как команды, а не как запрос помощи.
	for _, cmd := range []string{"up", "down", "status"} {
		got, err := migratorcli.Parse("kacho-migrator", []string{cmd})
		if err != nil {
			t.Fatalf("%s объявлен запросом помощи: %v", cmd, err)
		}
		if got.Command != cmd {
			t.Fatalf("%s разобран как %q", cmd, got.Command)
		}
	}
	// Форма вызова называет `help` — иначе возможность есть, а узнать о ней
	// неоткуда.
	if !strings.Contains(migratorcli.Usage("kacho-migrator"), "help") {
		t.Errorf("форма вызова не называет help: %s", migratorcli.Usage("kacho-migrator"))
	}
}

// TestParseTargetVersionRejectsATrailingTail — версия читается ЦЕЛИКОМ, а не до
// первого негодного знака (#1461).
//
// Инъекция 5 приёмщика: возврат к разбору форматом (`fmt.Sscanf`) оставил все
// пробы пакета зелёными, потому что проб у этой функции не было ни одной, а
// зовут её четыре точки наката. Форматный разбор на «12abc» отдаёт 12 БЕЗ
// ошибки — то есть накат идёт до версии, которой оператор не называл, и
// выглядит успехом. Это тот же класс, ради которого заведён весь пакет.
//
// Замер инъекции: `ParseTargetVersion("12abc")` → v=12, err=<nil>.
func TestParseTargetVersionRejectsATrailingTail(t *testing.T) {
	for _, in := range []string{"12abc", "800001x", "-1", "-5", "12 34", "1,2", "0x10", "12.0", "+", "-", ""} {
		got, err := migratorcli.ParseTargetVersion(in)
		if err == nil {
			t.Errorf("%q принято как версия %d — накат уехал бы не туда, куда просили", in, got)
			continue
		}
		if !strings.Contains(err.Error(), "--target") {
			t.Errorf("%q: отказ не называет флаг: %v", in, err)
		}
		if !strings.Contains(err.Error(), in) {
			t.Errorf("%q: отказ не называет само значение: %v", in, err)
		}
	}
}

// TestParseTargetVersionAcceptsWhatMigrationFilesUse — положительный контроль к
// пробе выше. Без него она зеленела бы на разборе, отвергающем вообще всё, — а
// тогда `--target` перестал бы работать у четырёх сервисов сразу.
//
// Ведущие нули приняты намеренно: `0010_…sql` — законная запись имени миграции
// в этом дереве, и оператор списывает версию с имени файла.
//
// Здесь стояло `{"-1", -1}` с объяснением «goose принимает отрицательную версию
// как „до нуля"». Объяснение проверено подачей входа, а не чтением назначения
// (#1383), и оказалось верным лишь наполовину, а держало оно ЦЕЛУЮ ось:
//
//   - `down --target -1` действительно откатывает всё — но ровно то же делает
//     `down --target 0`: цикл `DownToContext` кончается по `current.Version <=
//     version`, и для версий ≥ 1 обе границы недостижимы одинаково. Отдельной
//     возможности за `-1` нет;
//   - `up --target -1` возможностью не является вовсе: `CollectMigrations(dir,
//     0, -1)` даёт `n=0, err="no migration files found"` — тот же исход, что у
//     пустого каталога, то есть отказ, называющий оператору НЕ ту причину.
//
// Значение при этом стоило дорого: пока оно числилось законным, отрицательную
// цель нельзя было отвергнуть здесь, а три снятые копии разбора её отвергали, —
// и сведение к общему разбору молча ослабило бы их. Отрицательная цель ушла в
// перечень отвергаемых выше.
func TestParseTargetVersionAcceptsWhatMigrationFilesUse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"800001", 800001},
		{"0010", 10},
		{"0", 0},
		{"20260829120000", 20260829120000},
		{" 800001 ", 800001}, // окружающие пробелы — обычный след копирования
	} {
		got, err := migratorcli.ParseTargetVersion(tc.in)
		if err != nil {
			t.Errorf("%q отвергнуто, хотя это законная запись версии: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q разобрано как %d, ожидалось %d", tc.in, got, tc.want)
		}
	}
}
