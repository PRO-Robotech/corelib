// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Command notifygen — генератор постановки почтовых извещений источника
// (NTF-1, З5). Подключается к дереву потребителя директивой tool в go.mod и
// зовётся `go tool notifygen` (цель make notifications).
//
// Режимы:
//
//	notifygen [-root R] [-base B]          порождает notifications_<name>.gen.go
//	                                       (SendX, XAttrs, xDesc, лимиты) и
//	                                       revision.yaml каждого шаблона
//	notifygen -check [-base B] [-list]     сверка без записи: ревизия против
//	                                       набора, эталон порождённого файла,
//	                                       побайтовое содержимое выпущенных
//	                                       миграций ленты; с -base — и правило
//	                                       базы Р7 со знаменателем
//	notifygen -list                        множество порождаемых файлов
//	notifygen -version                     версия модуля corelib
//	notifygen init -service S -migrations D   миграция ленты при смене версии
//
// Коды выхода: 0 — зелёный, 1 — находки или отказ, 2 — неверный вызов.
// -list печатает множество при любом исходе сверки (D01, D02).
package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, defaultOptions()))
}

// options — то, что проба подменяет: часы метки миграции и набор выпущенных
// версий схемы ленты.
type options struct {
	now     func() time.Time
	schemas schemaCatalog
}

func defaultOptions() options {
	return options{now: time.Now, schemas: releasedSchemas{}}
}

const (
	exitGreen = 0
	exitRed   = 1
	exitUsage = 2
)

// errRed — исход «находки напечатаны».
var errRed = errors.New("notifygen: находки")

func run(args []string, out, errOut io.Writer, o options) int {
	stdout, stderr := &sink{w: out}, &sink{w: errOut}
	code := runSinks(args, stdout, stderr, o)
	if stdout.err != nil && code == exitGreen {
		// Вывод оборван: множество -list неполно — это не зелёный.
		stderr.printf("notifygen: запись вывода: %s\n", stdout.err)
		return exitRed
	}
	return code
}

func runSinks(args []string, stdout, stderr *sink, o options) int {
	fs := flag.NewFlagSet("notifygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "корень дерева источника")
	check := fs.Bool("check", false, "сверка без записи")
	list := fs.Bool("list", false, "печатать множество порождаемых файлов")
	version := fs.Bool("version", false, "версия модуля corelib")
	base := fs.String("base", "", "ревизия ствола для правила базы")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	baseSet := false
	fs.Visit(func(f *flag.Flag) { baseSet = baseSet || f.Name == "base" })

	if *version {
		stdout.printf("%s\n", moduleVersion())
		return exitGreen
	}
	if rest := fs.Args(); len(rest) > 0 {
		if rest[0] != "init" {
			stderr.printf("notifygen: неизвестная команда %q\n", rest[0])
			return exitUsage
		}
		return outcome(runInit(*root, rest[1:], stdout, stderr, o), stderr)
	}

	var b *baseTree
	if baseSet {
		var err error
		if b, err = resolveBase(*root, *base); err != nil {
			stderr.printf("%s\n", err)
			if *list {
				// Множество печатается при любом исходе (D02); исход уже красный.
				_ = printList(*root, stdout, stderr)
			}
			return exitRed
		}
	}
	g := &generator{root: *root, base: b, stdout: stdout, stderr: stderr, opts: o}
	switch {
	case *check:
		err := g.check()
		if *list {
			if lerr := printList(*root, stdout, stderr); lerr != nil && err == nil {
				err = lerr
			}
		}
		return outcome(err, stderr)
	case *list:
		return outcome(printList(*root, stdout, stderr), stderr)
	}
	return outcome(g.generate(), stderr)
}

func outcome(err error, stderr *sink) int {
	switch {
	case err == nil:
		return exitGreen
	case errors.Is(err, errRed):
		return exitRed
	}
	stderr.printf("%s\n", err)
	return exitRed
}
