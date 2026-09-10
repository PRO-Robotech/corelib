// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// preconditions.go — что обязано быть заполнено ДО обращения к базе, объявлено
// ОДИН раз на дерево.
//
// # Предмет: один набор проверок, три текста отказа
//
// Проверки эти жили тремя копиями в `services/{vpc,iam,nlb}/internal/apps/
// migrator/runner.go` (#1383). Пять из шести были побайтово одинаковы, шестая —
// про источник DSN — расходилась, и расхождение объяснялось тем, что каждый
// сервис называет свой источник.
//
// # Почему объяснение больше не действует, и это замер, а не мнение
//
// После #1461 DSN у ВСЕХ СЕМИ резолвит один [ResolveDSN] с приоритетом
// `--dsn` > [EnvDSN] > конфигурация сервиса. То есть два первых источника живы
// везде — а собственный текст `nlb` называл только ТРЕТИЙ и умалчивал ВТОРОЙ,
// тот, что его перебивает. Оператор `nlb`, прочитав отказ, узнавал про
// конфигурацию и не узнавал про переменную окружения, которая её переопределяет.
//
// Так расхождение перестало быть «каждый прав по-своему» и стало обычной
// неполнотой, пережившей свой довод.
//
// # Форма, которая не даёт этому повториться
//
// Два всегда живых источника печатает ПАКЕТ, сервис объявляет лишь
// [RunnerPreconditions.DSNExtraSources] — то, что у него СВЕРХ них. Умолчать
// перебивающий источник теперь нельзя by construction: его печатает не сервис.
//
// # Чего здесь НЕТ и почему
//
// Самого диалекта: его тип у трёх копий разный (у двух интерфейс, у одной
// структура), и сведение типа тянет за собой накат — часть тракта, которая
// ходит в базу и потому ждёт проб (предусловие названо в
// `docs/architecture/migrator-form.md`). Сюда вынесено ровно то, что
// проверяется БЕЗ базы: заполненность полей, их порядок и тексты отказа.
// Вызывающий сводит свой диалект к двум значениям — задан ли он и как
// называется его spec.
package migratorcli

import (
	"errors"
	"fmt"
	"strings"
)

// RunnerPreconditions — поля, которые накат обязан иметь до первого обращения к
// базе, в форме, не зависящей от типа диалекта конкретного сервиса.
type RunnerPreconditions struct {
	// Service — имя сервиса, чью цепочку применяет накат. Обязательное: без
	// него живой счёт строк перед сносом не может назвать, что он стережёт, а
	// безымянный отказ в логе init-контейнера некому адресовать.
	Service string

	// DialectSet — диалект задан. Отдельным флагом, а не самим диалектом:
	// тип диалекта у сервисов разный, а вопрос к нему здесь один.
	DialectSet bool

	// DialectSpecName — имя spec'а заданного диалекта. Читается вызывающим
	// ТОЛЬКО когда DialectSet, иначе это разыменование nil.
	DialectSpecName string

	// DSN — строка подключения.
	DSN string

	// DSNExtraSources — источники DSN СВЕРХ двух общих (`--dsn` и [EnvDSN]),
	// перечисленные в порядке убывания приоритета. Пусто у сервиса, который
	// ничем сверх них DSN не заполняет.
	DSNExtraSources []string

	// MigrationsFSSet — файловая система с миграциями передана.
	MigrationsFSSet bool

	// MigrationsDir — путь внутри неё; для корня embed — ".".
	MigrationsDir string
}

// Validate проверяет предусловия в том порядке, в каком их обязан читать
// вызывающий. Порядок — часть контракта: «диалект задан» стоит раньше «как он
// называется» ровно потому, что второе на незаданном диалекте не вычисляется.
func (p RunnerPreconditions) Validate() error {
	if p.Service == "" {
		return errors.New("service is empty (the live drop preflight would have nothing to name)")
	}
	if !p.DialectSet {
		return errors.New("dialect is not set")
	}
	if p.DialectSpecName == "" {
		return errors.New("dialect spec.Name is empty")
	}
	if p.DSN == "" {
		return fmt.Errorf("dsn is empty (set %s)", DSNSourceList(p.DSNExtraSources...))
	}
	if !p.MigrationsFSSet {
		return errors.New("migrations FS is nil")
	}
	if p.MigrationsDir == "" {
		return errors.New("migrations dir is empty")
	}
	return nil
}

// DSNSourceList перечисляет источники DSN в порядке убывания приоритета — два
// общих всегда, объявленные сервисом следом.
//
// Два общих не параметр: их читает [ResolveDSN] у всех семи, поэтому сервис не
// вправе ни отменить их, ни забыть назвать. Именно это и произошло однажды —
// текст назвал третий источник и умолчал второй.
func DSNSourceList(extra ...string) string {
	sources := append([]string{"--dsn", EnvDSN}, extra...)
	if len(sources) == 2 {
		return sources[0] + " or " + sources[1]
	}
	return strings.Join(sources[:len(sources)-1], ", ") + ", or " + sources[len(sources)-1]
}
