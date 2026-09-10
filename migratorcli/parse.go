// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package migratorcli — разбор командной строки мигратора, ОДИН на все точки
// наката прямой формы.
//
// # Предмет: у семи сервисов одной платформы был разный инструмент (#1461)
//
// Точек наката семь. Три делегируют своей обёртке и разбирают аргументы cobra,
// четыре звали goose прямо из main.go и разбирали аргументы стандартным
// `flag`. Разница не решалась никем — она накопилась от того, что сервисы
// заводились в разное время.
//
// Оператору это стоило не косметики. `flag.Parse` останавливается на ПЕРВОМ
// не-флаге, поэтому флаг, написанный ПОСЛЕ подкоманды, отбрасывался без
// единого слова:
//
//	--dsn XXX up  -> dsn="XXX"
//	up --dsn XXX  -> dsn=""     ← отброшен молча
//
// Пустой DSN уезжает в запасной путь (окружение, затем конфигурация сервиса),
// поэтому исход выглядит УСПЕХОМ — миграции накатаны, только не туда, куда
// просил оператор. Cobra принимает оба порядка, и `services/nlb/Makefile`
// пишет флаги именно во втором.
//
// # Что этот пакет обещает
//
// Ровно ту поверхность, которую предъявляет делегирующая тройка:
//
//		kacho-migrator [--dsn DSN] [--dialect postgres] {up|down|status} [--target VERSION]
//
//	  - флаг принимается ДО и ПОСЛЕ подкоманды, с одинаковым исходом;
//	  - неизвестный флаг, неизвестная подкоманда и лишний позиционный аргумент
//	    отвергаются ЯВНО и называются в тексте отказа;
//	  - `--target` живёт у up и down, у status его нет ни в одной из семи точек;
//	  - `--help` печатает форму вызова и не является ошибкой.
//
// # Чего этот пакет НЕ делает, и почему
//
// Он не открывает базу и не зовёт goose: накат остаётся в main.go каждого
// сервиса. Сведение самого наката в общий пакет — предмет #1383, и у него
// названо предусловие (у четырёх миграторов из семи нет проб вовсе). Разбор
// аргументов вынесен сюда потому, что он проверяем БЕЗ базы и без стенда, —
// то есть его вынос не требует той сети, которой ждёт #1383.
//
// Тексты отказов — по-английски, и это решение, а не недосмотр: делегирующая
// тройка печатает сообщения cobra, и смешанный язык на одной поверхности читался
// бы как два разных инструмента — ровно то, что задача снимает.
package migratorcli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Подкоманды. Перечень закрыт: у всех семи точек наката он один и тот же.
const (
	CommandUp     = "up"
	CommandDown   = "down"
	CommandStatus = "status"
)

// CommandHelp — запрос формы вызова подкомандой. Cobra доводит эту команду к
// дереву сама и снять её из перечня нельзя иначе как под чужим именем, поэтому
// равенство семи достигнуто с этой стороны: прямая форма её тоже понимает.
const CommandHelp = "help"

// DialectPostgres — единственный поддерживаемый диалект. Продукт Postgres-only;
// флаг существует ради равенства с делегирующей формой и ради того, чтобы
// ЧУЖОЕ значение было названо, а не проигнорировано.
const DialectPostgres = "postgres"

// EnvDSN — переменная окружения второго приоритета. Имя одно на все сервисы:
// оператор, знающий один, применяет знание к соседнему.
const EnvDSN = "KACHO_MIGRATOR_DSN"

// ErrHelpRequested — оператор попросил форму вызова. Это не отказ: вызывающий
// печатает [Usage] и выходит успехом, как это делает cobra у делегирующей тройки.
var ErrHelpRequested = errors.New("migratorcli: help requested")

// ErrNoCommand — командная строка пуста. Это ОТКАЗ, а не помощь: скрипт или
// init-контейнер, потерявший аргумент, иначе объявляется выполнившим накат.
var ErrNoCommand = errors.New("no command given")

// ── Тексты отказа: производитель ОДИН на все семь точек наката ─────────────
//
// Тон сообщений — часть контракта (см. правила API продукта): оператор читает
// их глазами, а скрипт — образцом. Пока каждая форма писала свою редакцию, они
// разошлись: на одном и том же входе прямая четвёрка отвечала редакцией
// стандартной библиотеки, делегирующая тройка — редакцией cobra. Образец,
// написанный по одному сервису, на соседнем не срабатывал.
//
// Делегирующая форма берёт эти тексты ОТСЮДА (через своё поле Args), поэтому
// байт-идентичность держится ПОСТРОЕНИЕМ, а не сверкой двух копий.

// UnknownCommandError — подкоманда не из закрытого перечня. Перечень назван в
// самом отказе: отказ обязан восстанавливать следующий шаг оператора.
func UnknownCommandError(binary, given string) error {
	return fmt.Errorf("unknown command %q for %q (known: %s, %s, %s)",
		given, binary, CommandUp, CommandDown, CommandStatus)
}

// UnexpectedArgumentError — лишний позиционный аргумент. `up 800001` — обычная
// догадка о том, как задать версию, и отказ называет верный способ.
func UnexpectedArgumentError(commandPath, given string) error {
	return fmt.Errorf("unexpected argument %q for %q; a version is given as --target %s",
		given, commandPath, given)
}

// UnknownFlagError — флаг вне набора.
//
// Формулировка ЗАИМСТВОВАНА у делегирующей формы дословно: её производит
// библиотека разбора, переписать её там нельзя, а два текста об одном предмете
// разошлись бы молча. Стандартная библиотека говорит иначе («flag provided but
// not defined: -X»), поэтому её формулировка здесь переводится в общую.
func UnknownFlagError(name string) error {
	return fmt.Errorf("unknown flag: --%s", strings.TrimLeft(name, "-"))
}

// ReportError печатает отказ в форме, одной на семь: `Error: <предмет>`.
//
// Форма взята у делегирующей тройки. Прямая четвёрка печатала отказ через
// журнал, то есть с меткой времени впереди, — для однократного инструмента
// командной строки это шум, и он же делал две редакции из одной.
// Возврат записи здесь намеренно не поднимается выше: печать отказа —
// последнее, что делает инструмент перед выходом с ненулевым кодом, и
// сообщать о неудаче печати уже некуда. Отбрасывание сделано ЯВНЫМ, чтобы
// следующий читатель видел решение, а не пропущенную проверку.
func ReportError(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "Error: %v\n", err)
}

// unknownFlagName вытаскивает имя флага из отказа стандартной библиотеки.
// Форма её сообщения — «flag provided but not defined: -<имя>».
var unknownFlagName = regexp.MustCompile(`^flag provided but not defined: -+(.+)$`)

// Options — разобранная командная строка.
type Options struct {
	// Command — одна из [CommandUp], [CommandDown], [CommandStatus].
	Command string
	// DSN — значение флага --dsn; пустое означает «не задан», см. [ResolveDSN].
	DSN string
	// Dialect — всегда [DialectPostgres]; чужое значение до сюда не доходит.
	Dialect string
	// Target — версия, до которой накатывать или откатывать; пустое означает
	// «до головы» (up) либо «на шаг назад» (down).
	Target string
}

// Usage — форма вызова. Одна на все точки наката прямой формы; имя бинаря
// принимается параметром, потому что оно принадлежит вызывающему.
func Usage(name string) string {
	return fmt.Sprintf(`usage: %s [--dsn DSN] [--dialect %s] {%s|%s|%s} [--target VERSION]
       %s {--help|%s}

  --dsn DSN         database DSN; if empty — read ENV %s,
                    then fall back to the service config
  --dialect NAME    SQL dialect (only %q is supported)
  --target VERSION  %s: apply up to this version (inclusive), default — latest
                    %s: roll back down to this version (inclusive), default — one step
                    not accepted by %s

Flags are accepted both before and after the subcommand.`,
		name, DialectPostgres, CommandUp, CommandDown, CommandStatus,
		name, CommandHelp,
		EnvDSN, DialectPostgres, CommandUp, CommandDown, CommandStatus)
}

// Parse разбирает аргументы БЕЗ имени программы (то есть os.Args[1:]).
//
// Флаги принимаются в обеих позициях: сначала разбирается всё до подкоманды,
// затем — всё после неё, тем же набором флагов плюс флаги самой подкоманды.
// Значение, названное дважды, берётся последним — так же, как у cobra.
func Parse(name string, args []string) (Options, error) {
	opts := Options{Dialect: DialectPostgres}

	// Проход 1 — до подкоманды. Набор здесь БЕЗ --target: cobra тоже не знает
	// его на корне, и `--target 5 up` обязан быть отвергнут одинаково у семи.
	root := newFlagSet(name)
	bindGlobalFlags(root, &opts)
	rest, err := parseWith(root, args)
	if err != nil {
		return Options{}, err
	}
	if len(rest) == 0 {
		return Options{}, ErrNoCommand
	}

	opts.Command = rest[0]
	switch opts.Command {
	case CommandUp, CommandDown, CommandStatus:
	case CommandHelp:
		return Options{}, ErrHelpRequested
	default:
		return Options{}, UnknownCommandError(name, opts.Command)
	}

	// Проход 2 — после подкоманды. Умолчания глобальных флагов равны уже
	// разобранным значениям, поэтому первый проход не затирается.
	path := name + " " + opts.Command
	sub := newFlagSet(path)
	bindGlobalFlags(sub, &opts)
	if opts.Command != CommandStatus {
		sub.StringVar(&opts.Target, "target", opts.Target,
			"version to stop at (inclusive)")
	}
	tail, err := parseWith(sub, rest[1:])
	if err != nil {
		return Options{}, err
	}
	if len(tail) > 0 {
		return Options{}, UnexpectedArgumentError(path, tail[0])
	}

	if opts.Dialect != DialectPostgres {
		return Options{}, fmt.Errorf("unknown dialect %q (supported: %s)", opts.Dialect, DialectPostgres)
	}
	return opts, nil
}

// ResolveDSN выбирает DSN: --dsn > ENV [EnvDSN] > конфигурация сервиса.
//
// fromConfig принадлежит вызывающему — набор переменных у каждого сервиса свой,
// и общий пакет не вправе называть оператору чужое имя. Отказ конфигурации
// доезжает наружу: пустой DSN, полученный молча, означает накат в никуда.
func ResolveDSN(flagDSN string, fromConfig func() (string, error)) (string, error) {
	if v := strings.TrimSpace(flagDSN); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv(EnvDSN)); v != "" {
		return v, nil
	}
	if fromConfig == nil {
		return "", fmt.Errorf("dsn unset (--dsn / %s) and no config fallback is wired", EnvDSN)
	}
	v, err := fromConfig()
	if err != nil {
		return "", fmt.Errorf("dsn unset (--dsn / %s) and service config load failed: %w", EnvDSN, err)
	}
	if v = strings.TrimSpace(v); v == "" {
		return "", fmt.Errorf("dsn unset (--dsn / %s) and service config produced an empty DSN", EnvDSN)
	}
	return v, nil
}

// ParseTargetVersion переводит значение --target в версию goose.
//
// Строг на ДВУХ осях, и это union строгостей двух прежних разборов, а не
// пересечение. До сведения (#1383) одно значение читали две функции, каждая
// пропускала то, что ловила соседняя:
//
//	                "12abc"              "-5"
//	strconv (здесь)  отказ                принимал → goose.UpTo(db, dir, -5)
//	Sscanf (копии)   принимал КАК 12      отказ
//
// Оператор получал разный исход на одном вводе в зависимости от того, какой
// сервис накатывает. Ослабить любую ось ради единообразия значило бы разменять
// живую проверку на симметрию, поэтому взято строгое с обеих сторон.
//
// strconv, а не fmt.Sscanf: Sscanf на "12abc" возвращает 12 БЕЗ ошибки, то есть
// молча накатывает не туда, куда просили, — тот же класс, ради которого этот
// пакет заведён. Ведущие нули приняты (`0010` в имени файла — законная запись).
func ParseTargetVersion(s string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse --target %q: expected a migration version number", s)
	}
	// Версия миграции — номер файла; отрицательной она не бывает ни в одной
	// цепочке дерева. Отказ здесь, а не у goose: `UpTo(db, dir, -5)` — запрос,
	// которому нечего сопоставить, и его исход зависит от версии библиотеки.
	if v < 0 {
		return 0, fmt.Errorf("parse --target %q: migration version must be non-negative", s)
	}
	return v, nil
}

// newFlagSet — набор, который НЕ печатает и НЕ выходит из процесса: отказ
// обязан вернуться вызывающему целиком, чтобы тот решил, как его подать.
func newFlagSet(path string) *flag.FlagSet {
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

func bindGlobalFlags(fs *flag.FlagSet, opts *Options) {
	fs.StringVar(&opts.DSN, "dsn", opts.DSN,
		"database DSN; if empty — ENV "+EnvDSN+", then the service config")
	fs.StringVar(&opts.Dialect, "dialect", opts.Dialect,
		"SQL dialect ("+DialectPostgres+")")
}

func parseWith(fs *flag.FlagSet, args []string) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, ErrHelpRequested
		}
		// Редакция стандартной библиотеки переводится в общую: иначе на одном и
		// том же входе семь сервисов отвечают двумя разными фразами.
		if m := unknownFlagName.FindStringSubmatch(err.Error()); m != nil {
			return nil, UnknownFlagError(m[1])
		}
		return nil, err
	}
	return fs.Args(), nil
}
